// SPDX-License-Identifier: AGPL-3.0-or-later

// Package xtream reads a server that answers the Xtream API: the account,
// the live categories and the live channels. What it expects of a server is
// written up in docs/protocol/xtream-api-notes.md; servers disagree about
// types, so it reads every field leniently.
//
// The username and password go into every request, so no error from this
// package quotes an address.
package xtream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"overscan.gureet.ca/internal/source"
)

// DefaultMaxBytes is the most that one answer may hold, unless a client
// sets another limit.
const DefaultMaxBytes = 64 << 20

// Client reads one server with one login.
type Client struct {
	http      *http.Client
	server    string // the server's address, without a slash at the end
	username  string
	password  string
	userAgent string
	// MaxBytes is the most that one answer may hold, and MaxListBytes one
	// list of films or series, which is read an entry at a time.
	MaxBytes     int64
	MaxListBytes int64
}

// New returns a client for an Xtream source's settings. It sends userAgent
// unless the settings name another.
func New(settings source.Settings, client *http.Client, userAgent string) *Client {
	if settings.UserAgent != "" {
		userAgent = settings.UserAgent
	}
	return &Client{
		http:         client,
		server:       strings.TrimRight(settings.URL, "/"),
		username:     settings.Username,
		password:     settings.Password,
		userAgent:    userAgent,
		MaxBytes:     DefaultMaxBytes,
		MaxListBytes: DefaultMaxListBytes,
	}
}

// Errors that say why a server cannot be read.
var (
	// ErrLogin is returned when the server refuses the username and
	// password.
	ErrLogin = errors.New("the server refused the username and password")
	// ErrNotJSON is returned for an answer that is not JSON, such as a web
	// page.
	ErrNotJSON = errors.New("the server did not answer with JSON")
)

// AccountError is returned for an account that the server knows but that
// cannot play, such as an expired one.
type AccountError struct {
	Status string // as the server gives it, cut short and cleaned
}

func (e *AccountError) Error() string { return "the server says the account is " + e.Status }

// Account is what the server says about the account.
type Account struct {
	Status            string    // "Active" for an account that can play
	ExpiresAt         time.Time // zero if it does not expire
	MaxConnections    int       // how many streams it may run at once; 0 if the server does not say
	ActiveConnections int       // how many it runs now
	Formats           []string  // the stream formats it may use, such as ts and m3u8
	TimeZone          string    // the server's IANA time zone, "" if it gives none
}

// Format returns the stream format to ask for. It is ts, one continuous
// MPEG-TS stream, unless the account lists formats without it and with
// m3u8.
func (a Account) Format() string {
	if !slices.Contains(a.Formats, "ts") && slices.Contains(a.Formats, "m3u8") {
		return "m3u8"
	}
	return "ts"
}

// Category is a category of live channels.
type Category struct {
	ID   string
	Name string
}

// Stream is a live channel as the server lists it.
type Stream struct {
	ID           int64  // the server's ID, which goes into the stream's address
	Num          int64  // the server's order or number, 0 if none
	Name         string // as given; the caller cleans it
	Type         string // live, or another kind such as radio_streams
	Icon         string // a logo address, "" if none
	EPGChannelID string // its ID in the server's guide, "" if none
	CategoryID   string // "" if none
	ArchiveDays  int    // how many days of catch-up it keeps, 0 for none
}

// Account signs in and returns what the server says about the account. A
// login that the server refuses gives ErrLogin, and an account that cannot
// play, such as an expired one, an *AccountError.
func (c *Client) Account(ctx context.Context) (Account, error) {
	var answer struct {
		UserInfo *struct {
			Auth           flag     `json:"auth"`
			Status         text     `json:"status"`
			ExpDate        number   `json:"exp_date"`
			MaxConnections number   `json:"max_connections"`
			ActiveCons     number   `json:"active_cons"`
			Formats        textList `json:"allowed_output_formats"`
		} `json:"user_info"`
		ServerInfo struct {
			Timezone text `json:"timezone"`
		} `json:"server_info"`
	}
	if err := c.get(ctx, c.api("", nil), &answer); err != nil {
		if errors.Is(err, errShape) {
			err = ErrLogin
		}
		return Account{}, err
	}
	u := answer.UserInfo
	if u == nil || !bool(u.Auth) {
		return Account{}, ErrLogin
	}
	a := Account{
		Status:            clean(string(u.Status), 40),
		MaxConnections:    int(max(0, min(u.MaxConnections, 10_000))),
		ActiveConnections: int(max(0, min(u.ActiveCons, 10_000))),
		TimeZone:          clean(string(answer.ServerInfo.Timezone), 64),
	}
	if a.Status == "" {
		a.Status = "Active"
	}
	if u.ExpDate > 0 && u.ExpDate < 1<<40 {
		a.ExpiresAt = time.Unix(int64(u.ExpDate), 0).UTC()
	}
	for _, f := range u.Formats {
		if f = strings.ToLower(clean(f, 16)); f != "" && !slices.Contains(a.Formats, f) {
			a.Formats = append(a.Formats, f)
		}
	}
	if !strings.EqualFold(a.Status, "active") {
		return a, &AccountError{Status: a.Status}
	}
	return a, nil
}

// LiveCategories returns the categories of live channels, in the server's
// order.
func (c *Client) LiveCategories(ctx context.Context) ([]Category, error) {
	raw, _, err := getList[struct {
		ID   text `json:"category_id"`
		Name text `json:"category_name"`
	}](ctx, c, c.api("get_live_categories", nil))
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(raw))
	for _, r := range raw {
		if id := strings.TrimSpace(string(r.ID)); id != "" {
			out = append(out, Category{ID: id, Name: string(r.Name)})
		}
	}
	return out, nil
}

// LiveStreams returns every live channel, in the server's order, and how
// many entries it left out because they were not channels with a usable ID.
func (c *Client) LiveStreams(ctx context.Context) ([]Stream, int, error) {
	raw, skipped, err := getList[struct {
		ID          number `json:"stream_id"`
		Num         number `json:"num"`
		Name        text   `json:"name"`
		Type        text   `json:"stream_type"`
		Icon        text   `json:"stream_icon"`
		EPG         text   `json:"epg_channel_id"`
		Category    text   `json:"category_id"`
		Archive     flag   `json:"tv_archive"`
		ArchiveDays number `json:"tv_archive_duration"`
	}](ctx, c, c.api("get_live_streams", nil))
	if err != nil {
		return nil, 0, err
	}
	out := make([]Stream, 0, len(raw))
	for _, r := range raw {
		if r.ID <= 0 {
			skipped++
			continue
		}
		s := Stream{
			ID:           int64(r.ID),
			Num:          int64(max(0, r.Num)),
			Name:         string(r.Name),
			Type:         strings.TrimSpace(string(r.Type)),
			Icon:         strings.TrimSpace(string(r.Icon)),
			EPGChannelID: strings.TrimSpace(string(r.EPG)),
			CategoryID:   strings.TrimSpace(string(r.Category)),
		}
		if r.Archive && r.ArchiveDays > 0 {
			s.ArchiveDays = int(min(r.ArchiveDays, 365))
		}
		out = append(out, s)
	}
	return out, skipped, nil
}

// api returns the address of a call of player_api.php.
func (c *Client) api(action string, extra url.Values) string {
	q := url.Values{"username": {c.username}, "password": {c.password}}
	if action != "" {
		q.Set("action", action)
	}
	for k, v := range extra {
		q[k] = v
	}
	return c.server + "/player_api.php?" + q.Encode()
}

var (
	// errShape is returned for JSON that is not the shape a call expects.
	errShape = errors.New("the server's answer is not in the form expected")
	// errBroken is returned for JSON that breaks off or is malformed.
	errBroken = errors.New("the server's answer is broken or cut short")
)

// getList fetches a list, and returns its entries and how many it left out
// because they were not objects. A server that has nothing to list may
// answer null, false or an empty object instead of an empty array.
func getList[T any](ctx context.Context, c *Client, address string) ([]T, int, error) {
	var raw json.RawMessage
	if err := c.get(ctx, address, &raw); err != nil {
		return nil, 0, err
	}
	switch t := bytes.TrimSpace(raw); {
	case len(t) > 0 && t[0] == '[':
	case bytes.Equal(t, []byte("null")), bytes.Equal(t, []byte("false")), bytes.Equal(t, []byte("{}")):
		return nil, 0, nil
	default:
		return nil, 0, errShape
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, 0, errShape
	}
	out := make([]T, 0, len(items))
	skipped := 0
	for _, it := range items {
		var v T
		if t := bytes.TrimSpace(it); len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &v) != nil {
			skipped++
			continue
		}
		out = append(out, v)
	}
	return out, skipped, nil
}

// get fetches address and decodes its JSON answer into into.
func (c *Client) get(ctx context.Context, address string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("the server's address is not valid")
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the server answered %s", clean(resp.Status, 60))
	}
	limit := c.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("the server's answer is larger than %d MiB", limit>>20)
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if !json.Valid(data) {
		if len(data) > 0 && (data[0] == '{' || data[0] == '[') {
			return errBroken
		}
		return ErrNotJSON
	}
	if err := json.Unmarshal(data, into); err != nil {
		return errShape
	}
	return nil
}

// withoutAddress drops the address that a url.Error quotes.
func withoutAddress(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// clean makes text from a server fit to show: one line, without control
// characters, of at most n bytes.
func clean(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f || r == ' ' }), " ")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}
