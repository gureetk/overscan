// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stalker is the client side of the Stalker portal protocol, as
// docs/protocol/stalker-protocol-notes.md describes it: Overscan signs in to
// an upstream portal as the box that its provider registered, reads the
// portal's genres, channels, guide, films and series, and asks it for a
// link when something plays.
//
// A portal knows a box by the MAC address in its cookie, and gives it a
// token at each handshake, which a new handshake replaces (section 3). So a
// Client keeps one session, which its calls share as a box's do, signs in
// one call at a time, and signs in again once when the portal says that its
// token is no longer good. A Pool gives each source one Client. The notes
// come from a client tested against fakes; where this package goes beyond
// them, its comments say so.
//
// No error from this package quotes the portal's address, the MAC address,
// the device's details or the login.
package stalker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DefaultModel is the model a client says it is when none is given: the
// one that the clients behind the protocol notes imitate (section 2).
const DefaultModel = "MAG250"

// DefaultMaxBytes is the most that one answer may hold, unless a client
// sets another limit. The guide is read as it comes, within MaxGuideBytes.
const DefaultMaxBytes = 64 << 20

// Watchdog intervals (section 7): two thirds of the profile's timeout,
// within these bounds.
const (
	minWatchdog     = 30 * time.Second
	maxWatchdog     = 120 * time.Second
	defaultWatchdog = 80 * time.Second // for a profile without a timeout: two thirds of 120 s
)

// Config is what a client needs to sign in as a box: the portal's address
// as its provider gave it, the box's MAC address and the details that the
// provider registered with it, and a login if the portal wants one.
type Config struct {
	// Portal is the address as typed, such as http://portal.example/c/ or
	// http://portal.example:8080/stalker_portal/c/.
	Portal string
	// MAC is the box's MAC address, such as 00:1A:79:12:34:56.
	MAC string
	// Serial, DeviceID, DeviceID2 and Signature are what the provider may
	// check besides the MAC address; "" if it does not.
	Serial, DeviceID, DeviceID2, Signature string
	// Model is the box's model, DefaultModel if "".
	Model string
	// Login and Password, if given, are sent with stb/do_auth.
	Login, Password string
	// UserAgent replaces the box's own user agent, if given.
	UserAgent string
	// TimeZone is the IANA zone that the box's cookie names, UTC if "".
	TimeZone string
}

// Errors that say why a portal cannot be read.
var (
	// ErrNotPortal is returned when no address that the portal's could be
	// answers as a Stalker portal.
	ErrNotPortal = errors.New("no Stalker portal answers at that address")
	// ErrRefused is returned when the portal refuses the box even after a
	// new handshake: it does not know the MAC address, or not with these
	// details.
	ErrRefused = errors.New("the portal refused the box")
	// ErrBlocked is returned when the portal's profile says that the box is
	// blocked.
	ErrBlocked = errors.New("the portal says the box is blocked")
	// ErrLogin is returned when the portal refuses the login and password.
	ErrLogin = errors.New("the portal refused the login and password")
	// ErrNotJSON is returned for an answer that is not the JSON a portal
	// sends, such as a web page.
	ErrNotJSON = errors.New("the portal did not answer with JSON")
	// ErrNoAction is returned when the portal answers 404: it lacks the
	// action, or the module (section 3, point 6).
	ErrNoAction = errors.New("the portal lacks that action")
	// ErrTooLarge is returned for an answer larger than the client allows.
	ErrTooLarge = errors.New("the portal's answer is too large")
)

// errAuth is a call that the portal refused for want of a valid token.
var errAuth = errors.New("authorization failed")

// LinkError is a portal's reason for giving no link (section 4), such as
// nothing_to_play or limit.
type LinkError struct {
	Reason string // as the portal gives it, cut short and cleaned
}

func (e *LinkError) Error() string { return "the portal gave no link: " + e.Reason }

// Limit reports whether the portal gave no link because the box plays as
// many streams as it may.
func (e *LinkError) Limit() bool { return e.Reason == "limit" }

// Profile is what the portal's profile says about the box.
type Profile struct {
	ID              string
	WatchdogTimeout time.Duration // 0 if the portal gives none
	Expires         time.Time     // zero if the portal gives no end date
}

// Client reads one portal as one box.
type Client struct {
	http   *http.Client
	config Config
	portal *url.URL // what was typed
	now    func() time.Time
	// MaxBytes is the most that one answer may hold.
	MaxBytes int64

	// signing, which holds one, lets one sign-in run at a time; calls that
	// need a session wait for it, or for their contexts to end.
	signing chan struct{}

	mu      sync.Mutex // guards the session
	api     string     // the address of the API, once found
	token   string     // "" when signed out
	signIns uint64     // the sign-ins that worked
	profile Profile

	// While a stream plays, the client is held, and calls the watchdog
	// (Hold).
	holdMu   sync.Mutex
	holds    int
	stopHold context.CancelFunc
	// watchEvery replaces WatchdogEvery in tests.
	watchEvery time.Duration
}

// New returns a client for a portal. It checks the portal's address and
// the MAC address, but makes no request until it is used.
func New(config Config, client *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimSpace(config.Portal))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, errors.New("the portal's address is an http or https address such as http://portal.example/c/")
	}
	u.RawQuery, u.Fragment = "", ""
	mac, ok := normalMAC(config.MAC)
	if !ok {
		return nil, errors.New("the MAC address is six pairs of hex digits, such as 00:1A:79:12:34:56")
	}
	config.MAC = mac
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.TimeZone == "" {
		config.TimeZone = "UTC"
	}
	return &Client{http: client, config: config, portal: u, now: time.Now, MaxBytes: DefaultMaxBytes,
		signing: make(chan struct{}, 1)}, nil
}

// normalMAC returns a MAC address in capitals with colons, if s is one.
func normalMAC(s string) (string, bool) {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "-", ":")
	parts := strings.Split(s, ":")
	if len(parts) != 6 {
		return "", false
	}
	for _, p := range parts {
		if len(p) != 2 || strings.Trim(p, "0123456789ABCDEF") != "" {
			return "", false
		}
	}
	return s, true
}

// candidates returns the addresses where the portal's API may be, in the
// order that the notes give (section 1): what was typed; the base, which is
// what was typed without c/, with server/load.php; the host's portal.php;
// and, for a bare host, /stalker_portal/server/load.php.
func candidates(typed *url.URL) []string {
	host := typed.Scheme + "://" + typed.Host
	p := typed.EscapedPath()
	base := p
	switch {
	case strings.HasSuffix(base, "/c/"):
		base = strings.TrimSuffix(base, "c/")
	case strings.HasSuffix(base, "/c"):
		base = strings.TrimSuffix(base, "c")
	case strings.HasSuffix(strings.ToLower(base), ".php"):
		base = path.Dir(base)
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}
	out := []string{host + p, host + base + "server/load.php", host + "/portal.php"}
	if p == "" || p == "/" {
		out = append(out, host+"/stalker_portal/server/load.php")
	}
	seen := map[string]bool{}
	kept := out[:0]
	for _, c := range out {
		if !seen[c] {
			seen[c] = true
			kept = append(kept, c)
		}
	}
	return kept
}

// referer returns what a box sends as its Referer: the portal's base with
// c/ (section 2).
func (c *Client) referer() string {
	p := c.portal.EscapedPath()
	switch {
	case strings.HasSuffix(p, "/c/"):
	case strings.HasSuffix(p, "/c"):
		p += "/"
	case strings.HasSuffix(strings.ToLower(p), ".php"):
		p = strings.TrimSuffix(path.Dir(p), "/") + "/c/"
	default:
		p = strings.TrimSuffix(p, "/") + "/c/"
	}
	return c.portal.Scheme + "://" + c.portal.Host + p
}

// userAgent is what the box says it is (section 2).
func (c *Client) userAgent() string {
	if c.config.UserAgent != "" {
		return c.config.UserAgent
	}
	return DefaultUserAgent
}

// DefaultUserAgent is the box's own user agent (section 2), which a client
// sends unless its config names another.
const DefaultUserAgent = "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG200 stbapp ver: 2 rev: 250 Safari/533.3"

// SignIn starts a new session: it finds the portal's API if it has not
// yet, hands shake, reads the box's profile, and sends the login if there
// is one. Calls sign in when they need to; SignIn says at once whether the
// portal takes the box.
func (c *Client) SignIn(ctx context.Context) (Profile, error) {
	if err := c.lockSigning(ctx); err != nil {
		return Profile{}, err
	}
	defer c.unlockSigning()
	if err := c.signIn(ctx); err != nil {
		return Profile{}, err
	}
	return c.Profile(), nil
}

// Profile returns what the portal's profile said at the last sign-in.
func (c *Client) Profile() Profile {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.profile
}

// session is a signed-in session: where the API is, its token, and the
// sign-in that gave it.
type session struct {
	api, token string
	signIn     uint64
}

// session returns the client's session. It signs in if the client has
// none, or if its session is still the one that sign-in number refused
// gave, which the portal has refused.
func (c *Client) session(ctx context.Context, refused uint64) (session, error) {
	if err := c.lockSigning(ctx); err != nil {
		return session{}, err
	}
	defer c.unlockSigning()
	c.mu.Lock()
	s := session{api: c.api, token: c.token, signIn: c.signIns}
	c.mu.Unlock()
	if s.token != "" && s.signIn != refused {
		return s, nil
	}
	if err := c.signIn(ctx); err != nil {
		return session{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return session{api: c.api, token: c.token, signIn: c.signIns}, nil
}

// lockSigning waits for the right to sign in, or for ctx to end.
func (c *Client) lockSigning(ctx context.Context) error {
	select {
	case c.signing <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) unlockSigning() { <-c.signing }

// withSession runs try with the session, and once more with a new one if
// the portal refuses its token (section 3, point 5). A second refusal
// signs the client out, so that the next call starts again.
func (c *Client) withSession(ctx context.Context, try func(session) error) error {
	s, err := c.session(ctx, 0)
	if err != nil {
		return err
	}
	err = try(s)
	if !errors.Is(err, errAuth) {
		return err
	}
	if s, err = c.session(ctx, s.signIn); err != nil {
		return err
	}
	if err = try(s); errors.Is(err, errAuth) {
		c.mu.Lock()
		if c.signIns == s.signIn {
			c.token = ""
		}
		c.mu.Unlock()
		err = ErrRefused
	}
	return err
}

// signIn starts a new session. It needs c.signing. If it fails, the client
// has no session.
func (c *Client) signIn(ctx context.Context) error {
	c.mu.Lock()
	api := c.api
	c.token = ""
	c.mu.Unlock()
	var token string
	var err error
	if api == "" {
		api, token, err = c.find(ctx)
	} else {
		token, err = c.handshake(ctx, api)
	}
	if err != nil {
		return err
	}
	profile, renewed, err := c.readProfile(ctx, api, token)
	if err != nil {
		return err
	}
	if renewed != "" {
		token = renewed
	}
	if c.config.Login != "" {
		var ok flag
		err := c.request(ctx, api, "stb", "do_auth", url.Values{"login": {c.config.Login}, "password": {c.config.Password},
			"device_id": {c.config.DeviceID}, "device_id2": {c.config.DeviceID2}}, &ok, token)
		if err == nil && !ok {
			err = ErrLogin
		}
		if err != nil {
			return signInError("stb/do_auth", err)
		}
	}
	c.mu.Lock()
	c.api, c.token, c.profile = api, token, profile
	c.signIns++
	c.mu.Unlock()
	return nil
}

// signInError says what a sign-in step met.
func signInError(step string, err error) error {
	if errors.Is(err, errAuth) {
		return ErrRefused
	}
	if errors.Is(err, ErrLogin) || errors.Is(err, ErrBlocked) || errors.Is(err, ErrRefused) {
		return err
	}
	return fmt.Errorf("%s: %w", step, err)
}

// find tries each address where the API may be, and keeps the first whose
// handshake works. It returns that address and the token.
func (c *Client) find(ctx context.Context) (string, string, error) {
	refused := false
	for _, api := range candidates(c.portal) {
		token, err := c.handshake(ctx, api)
		if err == nil {
			c.mu.Lock()
			c.api = api
			c.mu.Unlock()
			return api, token, nil
		}
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		refused = refused || errors.Is(err, errAuth)
	}
	if refused {
		// A portal answered, and refused the box.
		return "", "", ErrRefused
	}
	return "", "", ErrNotPortal
}

// handshake starts a session with the API at api (section 3, point 1):
// first with an empty token, then, if that gives none, with a random one
// and its SHA-1 as prehash. It returns the token that the portal answers
// with, or the one proposed if it answers without one (Assumed).
func (c *Client) handshake(ctx context.Context, api string) (string, error) {
	var hs struct {
		Token text `json:"token"`
	}
	err := c.request(ctx, api, "stb", "handshake", url.Values{"token": {""}}, &hs, "")
	switch {
	case err == nil && hs.Token != "":
		return string(hs.Token), nil
	case err != nil && !errors.Is(err, errAuth):
		return "", err // no API there, or the address cannot be reached
	}
	proposed := randomToken()
	sum := sha1.Sum([]byte(proposed))
	hs.Token = ""
	err = c.request(ctx, api, "stb", "handshake",
		url.Values{"token": {proposed}, "prehash": {strings.ToUpper(hex.EncodeToString(sum[:]))}}, &hs, "")
	if err != nil {
		return "", err
	}
	if hs.Token != "" {
		return string(hs.Token), nil
	}
	return proposed, nil
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // never fails; see crypto/rand
	return strings.ToUpper(hex.EncodeToString(b))
}

// readProfile reads the box's profile (section 3, point 2), and returns
// it with the token that the portal renews the session with, if any.
func (c *Client) readProfile(ctx context.Context, api, token string) (Profile, string, error) {
	var p struct {
		ID              text   `json:"id"`
		Blocked         flag   `json:"blocked"`
		WatchdogTimeout number `json:"watchdog_timeout"`
		Token           text   `json:"token"`
		ExpireBilling   text   `json:"expire_billing_date"`
		TariffExpired   text   `json:"tariff_expired_date"`
	}
	if err := c.request(ctx, api, "stb", "get_profile", c.profileParams(), &p, token); err != nil {
		return Profile{}, "", signInError("stb/get_profile", err)
	}
	if p.Blocked {
		return Profile{}, "", ErrBlocked
	}
	profile := Profile{ID: string(p.ID)}
	if p.WatchdogTimeout > 0 && p.WatchdogTimeout < 86400 {
		profile.WatchdogTimeout = time.Duration(p.WatchdogTimeout) * time.Second
	}
	for _, d := range []text{p.ExpireBilling, p.TariffExpired} {
		if t, ok := ParseDate(string(d)); ok {
			profile.Expires = t
			break
		}
	}
	return profile, string(p.Token), nil
}

// profileParams are what get_profile sends (section 3, point 2): the box's
// details as the provider registered them, and for the rest the values
// that the conformance suite's client sends (Assumed: the notes name these
// parameters without their values).
func (c *Client) profileParams() url.Values {
	return url.Values{
		"hd":               {"1"},
		"ver":              {"ImageDescription: 0.2.18-r23-250; ImageDate: Fri Jan 1 00:00:00 UTC 2021; PORTAL version: 5.6.6; API Version: JS API version: 343"},
		"num_banks":        {"2"},
		"sn":               {c.config.Serial},
		"stb_type":         {c.config.Model},
		"client_type":      {"STB"},
		"image_version":    {"218"},
		"video_out":        {"hdmi"},
		"device_id":        {c.config.DeviceID},
		"device_id2":       {c.config.DeviceID2},
		"signature":        {c.config.Signature},
		"auth_second_step": {"0"},
		"hw_version":       {"1.7-BD-00"},
		"not_valid_token":  {"0"},
		"hw_version_2":     {""},
		"timestamp":        {strconv.FormatInt(c.now().Unix(), 10)},
		"api_signature":    {"262"},
		"prehash":          {""},
	}
}

// call makes a portal call as the box, in the client's session.
func (c *Client) call(ctx context.Context, module, action string, params url.Values, v any) error {
	return c.withSession(ctx, func(s session) error {
		return c.request(ctx, s.api, module, action, params, v, s.token)
	})
}

// requestError is a request that went wrong, which says nothing of the
// address.
type requestError struct {
	what string // module/action
	err  error
}

func (e *requestError) Error() string { return e.what + ": " + e.err.Error() }
func (e *requestError) Unwrap() error { return e.err }

// request makes a call to the API at api, as the box: GET with type,
// action, the call's parameters and JsHttpRequest (section 1), the box's
// headers and cookie (section 2), and the token unless it is "". It
// decodes the "js" member of the answer into v, if v is not nil.
func (c *Client) request(ctx context.Context, api, module, action string, params url.Values, v any, token string) error {
	body, err := c.fetch(ctx, api, module, action, params, token)
	if err != nil {
		return err
	}
	defer body.Close()
	data, err := io.ReadAll(io.LimitReader(body, c.MaxBytes+1))
	if err != nil {
		return &requestError{what: module + "/" + action, err: withoutAddress(err)}
	}
	if int64(len(data)) > c.MaxBytes {
		return &requestError{what: module + "/" + action, err: ErrTooLarge}
	}
	if bytes.HasPrefix(bytes.TrimSpace(data), []byte("Authorization failed")) {
		return errAuth
	}
	js, err := jsMember(data)
	if err != nil {
		return &requestError{what: module + "/" + action, err: err}
	}
	if v == nil {
		return nil
	}
	if err := decode(js, v); err != nil {
		return &requestError{what: module + "/" + action, err: ErrNotJSON}
	}
	return nil
}

// fetch sends a call and returns the answer's body when it is a success.
func (c *Client) fetch(ctx context.Context, api, module, action string, params url.Values, token string) (io.ReadCloser, error) {
	what := module + "/" + action
	q := url.Values{}
	for k, vs := range params {
		q[k] = vs
	}
	q.Set("type", module)
	q.Set("action", action)
	q.Set("JsHttpRequest", "1-xml")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api+"?"+q.Encode(), nil)
	if err != nil {
		return nil, &requestError{what: what, err: errors.New("the portal's address is not valid")}
	}
	req.Header.Set("User-Agent", c.userAgent())
	req.Header.Set("X-User-Agent", "Model: "+c.config.Model+"; Link: WiFi")
	req.Header.Set("Referer", c.referer())
	req.Header.Set("Cookie", "mac="+url.QueryEscape(c.config.MAC)+"; stb_lang=en; timezone="+url.QueryEscape(c.config.TimeZone))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &requestError{what: what, err: withoutAddress(err)}
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusUnauthorized, http.StatusForbidden:
		resp.Body.Close()
		return nil, errAuth
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, ErrNoAction
	}
	resp.Body.Close()
	return nil, &requestError{what: what, err: fmt.Errorf("the portal answered %s", clean(resp.Status, 60))}
}

// jsMember returns the "js" member of an answer (section 1).
func jsMember(data []byte) (json.RawMessage, error) {
	var answer struct {
		JS json.RawMessage `json:"js"`
	}
	if json.Unmarshal(data, &answer) != nil || answer.JS == nil {
		return nil, ErrNotJSON
	}
	return answer.JS, nil
}

// decode decodes a "js" member into v. A portal may send false, null, or
// an empty list where an object is due: v then stays as it is.
func decode(js json.RawMessage, v any) error {
	t := bytes.TrimSpace(js)
	if bytes.Equal(t, []byte("false")) || bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte("[]")) ||
		bytes.Equal(t, []byte(`""`)) {
		if _, isFlag := v.(*flag); !isFlag {
			return nil
		}
	}
	return json.Unmarshal(js, v)
}

// withoutAddress drops the address that a url.Error quotes.
func withoutAddress(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// clean cuts s to at most n bytes of printable text.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > n {
		s = s[:n]
		for len(s) > 0 && s[len(s)-1]&0xc0 == 0x80 {
			s = s[:len(s)-1]
		}
		if len(s) > 0 && s[len(s)-1] >= 0xc0 {
			s = s[:len(s)-1]
		}
	}
	return s
}

// resolve returns an address that the portal gives, which may be a path on
// the portal, as an absolute http or https address; "" if it is neither.
func (c *Client) resolve(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 4096 {
		return ""
	}
	u, err := c.portal.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}
	return u.String()
}

// Account returns when the account ends, from account_info/get_main_info:
// its end_date, or else its phone, where some portals put it (section 3,
// point 4). The time is zero if the portal gives none.
func (c *Client) Account(ctx context.Context) (time.Time, error) {
	var info struct {
		EndDate text `json:"end_date"`
		Phone   text `json:"phone"`
	}
	if err := c.call(ctx, "account_info", "get_main_info", nil, &info); err != nil {
		return time.Time{}, err
	}
	for _, d := range []text{info.EndDate, info.Phone} {
		if t, ok := ParseDate(string(d)); ok {
			return t, nil
		}
	}
	return time.Time{}, nil
}

// dateLayouts are the forms of a date that the notes list (section 3,
// point 4), besides Unix seconds. A date without a zone is taken as UTC.
var dateLayouts = []string{"2006-01-02 15:04:05", "2006-01-02", "January 2, 2006, 3:04 pm"}

// ParseDate reads a date in one of the forms that portals send.
func ParseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 0 && n < 1<<35 {
			return time.Unix(n, 0).UTC(), true
		}
		return time.Time{}, false
	}
	for _, layout := range dateLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// Watchdog sends the keep-alive (section 7) and returns how many messages
// the portal has for the box.
func (c *Client) Watchdog(ctx context.Context) (int, error) {
	var events struct {
		Data struct {
			Msgs number `json:"msgs"`
		} `json:"data"`
	}
	err := c.call(ctx, "watchdog", "get_events",
		url.Values{"init": {"0"}, "cur_play_type": {"1"}, "event_active_id": {"0"}}, &events)
	return int(events.Data.Msgs), err
}

// WatchdogEvery returns how often to send the keep-alive: two thirds of
// the profile's watchdog_timeout, from 30 s to 120 s (section 7).
func (c *Client) WatchdogEvery() time.Duration {
	timeout := c.Profile().WatchdogTimeout
	if timeout == 0 {
		return defaultWatchdog
	}
	return min(max(timeout*2/3, minWatchdog), maxWatchdog)
}
