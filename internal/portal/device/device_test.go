// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/devices"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Fictional MAC addresses.
const (
	macA = "00:1A:79:00:00:01"
	macB = "00:1A:79:00:00:02"
)

type harness struct {
	t       *testing.T
	store   *store.Store
	api     *API
	front   *httptest.Server
	now     time.Time
	logs    bytes.Buffer
	secrets map[string]string // what each box keeps, by MAC address
	srcID   int64
	catalog []store.CatalogChannel // the source's channels
}

// newHarness serves the API and the hub, with a fictional provider whose
// channels stream endlessly from one source with a single connection.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, now: time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC), secrets: map[string]string{}}
	log := slog.New(slog.NewTextHandler(&h.logs, nil))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.store = st

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		rc := http.NewResponseController(w)
		for {
			if _, err := w.Write(bytes.Repeat([]byte{0x47}, 188)); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(up.Close)

	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	var channels []store.CatalogChannel
	for i := range 3 {
		cat := "News"
		if i == 1 {
			cat = "Sports"
		}
		channels = append(channels, store.CatalogChannel{
			IdentityKey: fmt.Sprint(i), Name: fmt.Sprintf("Channel %d", i), Category: cat,
			StreamURL: fmt.Sprintf("%s/live/%d.ts", up.URL, i),
		})
	}
	channels = append(channels,
		store.CatalogChannel{IdentityKey: "hls", Name: "HLS channel", Category: "News", StreamURL: up.URL + "/hls/index.m3u8"},
		store.CatalogChannel{IdentityKey: "loose", Name: "Ungrouped", StreamURL: up.URL + "/live/9.ts"},
	)
	if _, err := st.ApplyCatalog(ctx, srcID, channels, h.now); err != nil {
		t.Fatal(err)
	}
	h.srcID, h.catalog = srcID, channels

	hb := hub.New(st, log, "test", map[int64]hub.Source{srcID: {Name: "main", MaxConnections: 1}})
	reg := devices.New(st, log)
	reg.Now = func() time.Time { return h.now }
	h.api = New(st, reg, hb, log, "v9.9.9")
	h.api.now = func() time.Time { return h.now }

	mux := http.NewServeMux()
	mux.Handle(Prefix, h.api)
	mux.Handle("GET /live/", hb)
	mux.Handle("GET /archive/", hb)
	h.front = httptest.NewServer(mux)
	t.Cleanup(h.front.Close)
	return h
}

// call sends a request to the API with the given bearer token ("" for none)
// and body ("" for none), and decodes the JSON answer into v unless v is nil.
func (h *harness) call(method, path, token, body string, v any) int {
	h.t.Helper()
	req, err := http.NewRequest(method, h.front.URL+path, strings.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		h.t.Errorf("%s %s: Cache-Control %q", method, path, cc)
	}
	if ct := resp.Header.Get("Content-Type"); resp.StatusCode != http.StatusMethodNotAllowed && ct != "application/json; charset=utf-8" {
		h.t.Errorf("%s %s: Content-Type %q", method, path, ct)
	}
	if v != nil {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			h.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
	return resp.StatusCode
}

// session starts a session as the box with the given MAC address does: it
// sends the secret the box keeps, and keeps any new one it gets.
func (h *harness) session(mac string) sessionResponse {
	h.t.Helper()
	key, _ := store.ParseMAC(mac)
	body, err := json.Marshal(sessionRequest{MAC: mac, Model: "MAG322", Secret: h.secrets[key]})
	if err != nil {
		h.t.Fatal(err)
	}
	var s sessionResponse
	if code := h.call("POST", Prefix+"session", "", string(body), &s); code != http.StatusOK {
		h.t.Fatalf("session: status %d", code)
	}
	if s.Secret != "" {
		h.secrets[key] = s.Secret
	}
	return s
}

func (h *harness) approve(mac string) {
	h.t.Helper()
	if _, err := h.store.ApproveDevice(context.Background(), mac, 1, "Living room", h.now); err != nil {
		h.t.Fatal(err)
	}
}

// errorCode returns the status and error code of a call.
func (h *harness) errorCode(method, path, token string) (int, string) {
	h.t.Helper()
	return h.errorCodeBody(method, path, token, "")
}

// errorCodeBody returns the status and error code of a call with a body.
func (h *harness) errorCodeBody(method, path, token, body string) (int, string) {
	h.t.Helper()
	var e struct{ Error string }
	code := h.call(method, path, token, body, &e)
	return code, e.Error
}

func TestSession(t *testing.T) {
	h := newHarness(t)

	// A new box asks to be paired: it gets a secret to keep and a code.
	s := h.session("00-1a-79-00-00-01")
	if s.State != store.DevicePending || s.MAC != macA || s.Token != "" || s.Version != "v9.9.9" ||
		len(s.Code) != 6 || len(s.Secret) != 43 {
		t.Errorf("unknown box: %+v", s)
	}
	d, err := h.store.Device(context.Background(), macA)
	if err != nil || d.State != store.DevicePending || d.Model != "MAG322" || d.PairingCode != s.Code {
		t.Errorf("recorded device %+v, %v", d, err)
	}
	if !strings.Contains(h.logs.String(), "device asks to be paired") || !strings.Contains(h.logs.String(), "overscan devices approve "+s.Code) {
		t.Errorf("logs: %s", h.logs.String())
	}

	// While it waits, it keeps its code.
	code := s.Code
	if s = h.session(macA); s.State != store.DevicePending || s.Code != code || s.Secret != "" {
		t.Errorf("waiting box: %+v", s)
	}

	// Approved, it is paired with its secret and gets a session.
	h.approve(macA)
	s = h.session(macA)
	if s.State != store.DeviceApproved || s.Token == "" || s.Secret != "" || s.Code != "" || s.Label != "Living room" ||
		s.Expires != h.now.Add(sessionTTL).Unix() {
		t.Errorf("approved box: %+v", s)
	}
	if !strings.Contains(h.logs.String(), "device session started") {
		t.Errorf("logs: %s", h.logs.String())
	}

	if _, err := h.store.BlockDevice(context.Background(), macA); err != nil {
		t.Fatal(err)
	}
	if s := h.session(macA); s.State != store.DeviceBlocked || s.Token != "" {
		t.Errorf("blocked box: %+v", s)
	}
}

func TestSessionBadRequests(t *testing.T) {
	h := newHarness(t)
	for _, body := range []string{"", "{", `{"mac":"not-a-mac"}`, `{"mac":""}`, `["` + macA + `"]`,
		`{"mac":"` + macA + `","secret":"` + strings.Repeat("s", maxSecretLen+1) + `"}`} {
		var e struct{ Error string }
		if code := h.call("POST", Prefix+"session", "", body, &e); code != http.StatusBadRequest || e.Error != errBadRequest {
			t.Errorf("body %q: status %d, error %q", body, code, e.Error)
		}
	}
	big := `{"mac":"` + macA + `","model":"` + strings.Repeat("x", maxBodyBytes) + `"}`
	if code := h.call("POST", Prefix+"session", "", big, nil); code != http.StatusBadRequest {
		t.Errorf("oversized body: status %d", code)
	}
	if code, e := h.errorCode("GET", Prefix+"session", ""); code != http.StatusNotFound || e != errNotFound {
		t.Errorf("GET session: status %d, error %q", code, e)
	}
	if code, e := h.errorCode("GET", Prefix+"nothing", ""); code != http.StatusNotFound || e != errNotFound {
		t.Errorf("unknown call: status %d, error %q", code, e)
	}
}

func TestCallsNeedASession(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	h.approve(macB) // so that macA, removed and approved again, gets a new ID
	token := h.session(macA).Token

	if code, _ := h.errorCode("GET", Prefix+"channels", token); code != http.StatusOK {
		t.Fatalf("with a token: status %d", code)
	}
	for name, tok := range map[string]string{
		"no token":       "",
		"garbage":        "garbage",
		"altered token":  token[:len(token)-2] + "AA",
		"another server": New(h.store, devices.New(h.store, slog.New(slog.DiscardHandler)), nil, slog.New(slog.DiscardHandler), "").sign(sessionClaims{deviceID: 1, mac: macA, issued: h.now.Unix()}),
	} {
		if code, e := h.errorCode("GET", Prefix+"channels", tok); code != http.StatusUnauthorized || e != errUnauthorized {
			t.Errorf("%s: status %d, error %q", name, code, e)
		}
	}

	// A token lasts a day.
	h.now = h.now.Add(sessionTTL - time.Second)
	if code, _ := h.errorCode("GET", Prefix+"channels", token); code != http.StatusOK {
		t.Errorf("before expiry: status %d", code)
	}
	h.now = h.now.Add(time.Second)
	if code, e := h.errorCode("GET", Prefix+"channels", token); code != http.StatusUnauthorized || e != errUnauthorized {
		t.Errorf("expired: status %d, error %q", code, e)
	}
	token = h.session(macA).Token

	// A blocked box is told so.
	if _, err := h.store.BlockDevice(ctx, macA); err != nil {
		t.Fatal(err)
	}
	if code, e := h.errorCode("GET", Prefix+"channels", token); code != http.StatusForbidden || e != errBlocked {
		t.Errorf("blocked: status %d, error %q", code, e)
	}

	// A removed box's token is no good, even once the box is back with
	// another ID.
	if err := h.store.RemoveDevice(ctx, macA); err != nil {
		t.Fatal(err)
	}
	if code, e := h.errorCode("GET", Prefix+"channels", token); code != http.StatusUnauthorized || e != errUnauthorized {
		t.Errorf("removed: status %d, error %q", code, e)
	}
	h.approve(macA)
	if code, e := h.errorCode("GET", Prefix+"channels", token); code != http.StatusUnauthorized || e != errUnauthorized {
		t.Errorf("approved again: status %d, error %q", code, e)
	}
}

func TestChannels(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token

	var got channelsResponse
	if code := h.call("GET", Prefix+"channels", token, "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(got.Groups) != 2 || got.Groups[0].Name != "News" || got.Groups[1].Name != "Sports" {
		t.Fatalf("groups %+v", got.Groups)
	}
	news, sports := got.Groups[0].ID, got.Groups[1].ID
	want := []struct {
		number int
		name   string
		group  int64
	}{{1, "Channel 0", news}, {2, "Channel 1", sports}, {3, "Channel 2", news}, {4, "HLS channel", news}, {5, "Ungrouped", 0}}
	if len(got.Channels) != len(want) {
		t.Fatalf("channels %+v", got.Channels)
	}
	for i, w := range want {
		if c := got.Channels[i]; c.ID == 0 || c.Number != w.number || c.Name != w.name || c.Group != w.group {
			t.Errorf("channel %d: %+v, want %+v", i, c, w)
		}
	}

	// Numbers are numbers, and an empty lineup gives empty lists.
	var raw map[string]json.RawMessage
	h.call("GET", Prefix+"channels", token, "", &raw)
	if !bytes.Contains(raw["channels"], []byte(`"number":1,`)) {
		t.Errorf("channels %s", raw["channels"])
	}
}

func TestEmptyLineup(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	if _, err := h.store.ApplyCatalog(context.Background(), 1, nil, h.now.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	h.call("GET", Prefix+"channels", token, "", &raw)
	if string(raw["groups"]) != "[]" || string(raw["channels"]) != "[]" {
		t.Errorf("empty lineup: %s %s", raw["groups"], raw["channels"])
	}
}

func TestPlay(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.approve(macB)
	tokenA, tokenB := h.session(macA).Token, h.session(macB).Token
	var list channelsResponse
	h.call("GET", Prefix+"channels", tokenA, "", &list)
	ts, hls := list.Channels[0].ID, list.Channels[3].ID

	var p playResponse
	if code := h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, ts), tokenA, "", &p); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if p.Solution != "ffrt" || !strings.HasPrefix(p.URL, h.front.URL+"/live/") || !strings.HasSuffix(p.URL, ".ts") {
		t.Errorf("TS channel: %+v", p)
	}
	if code := h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, hls), tokenA, "", &p); code != http.StatusOK || p.Solution != "ffmpeg" || !strings.HasSuffix(p.URL, ".m3u8") {
		t.Errorf("HLS channel: status %d, %+v", code, p)
	}

	for _, id := range []string{"0", "-1", "x", "999999"} {
		if code, e := h.errorCode("POST", Prefix+"channels/"+id+"/play", tokenA); code != http.StatusNotFound || e != errNotFound {
			t.Errorf("channel %s: status %d, error %q", id, code, e)
		}
	}
	if code, _ := h.errorCode("POST", fmt.Sprintf("%schannels/%d/play", Prefix, ts), ""); code != http.StatusUnauthorized {
		t.Errorf("without a session: status %d", code)
	}

	// While A watches, the source's one connection is taken: B is refused,
	// and A can switch channels.
	h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, ts), tokenA, "", &p)
	resp, err := http.Get(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 188)); err != nil {
		t.Fatal(err)
	}
	if code, e := h.errorCode("POST", fmt.Sprintf("%schannels/%d/play", Prefix, list.Channels[1].ID), tokenB); code != http.StatusServiceUnavailable || e != errLimit {
		t.Errorf("second box: status %d, error %q", code, e)
	}
	if code, _ := h.errorCode("POST", fmt.Sprintf("%schannels/%d/play", Prefix, list.Channels[1].ID), tokenA); code != http.StatusOK {
		t.Errorf("same box, another channel: status %d", code)
	}
}

func TestApprovedBoxIsPairedOnFirstContact(t *testing.T) {
	h := newHarness(t)
	h.approve(macA) // by MAC address, before the box loads the page

	s := h.session(macA)
	if s.State != store.DeviceApproved || s.Token == "" || len(s.Secret) != 43 {
		t.Fatalf("first contact: %+v", s)
	}
	if d, err := h.store.Device(context.Background(), macA); err != nil || d.SecretHash != store.HashSecret(s.Secret) || !d.PairedAt.Equal(h.now) {
		t.Errorf("device %+v, %v", d, err)
	}
	if s := h.session(macA); s.State != store.DeviceApproved || s.Secret != "" {
		t.Errorf("with its secret: %+v", s)
	}
}

func TestBoxWithoutTheSecret(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	paired := h.session(macA)

	// A box that sends the MAC address without the secret, such as a copy
	// or a box that lost it, asks to be paired, and the paired box carries
	// on.
	delete(h.secrets, macA)
	s := h.session(macA)
	if s.State != store.DevicePending || s.Token != "" || len(s.Code) != 6 || s.Secret == "" || s.Secret == paired.Secret {
		t.Fatalf("without the secret: %+v", s)
	}
	h.secrets[macA] = "a made-up secret"
	if s := h.session(macA); s.State != store.DevicePending || s.Token != "" {
		t.Errorf("with a wrong secret: %+v", s)
	}
	h.secrets[macA] = paired.Secret
	if s := h.session(macA); s.State != store.DeviceApproved {
		t.Errorf("the paired box: %+v", s)
	}
	if code, _ := h.errorCode("GET", Prefix+"channels", paired.Token); code != http.StatusOK {
		t.Errorf("the paired box's session: status %d", code)
	}
}

func TestPairingAgainEndsSessions(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	old := h.session(macA)

	delete(h.secrets, macA)
	req := h.session(macA)
	if _, err := h.store.ApprovePairing(ctx, req.Code, 1, "", h.now); err != nil {
		t.Fatal(err)
	}
	if code, e := h.errorCode("GET", Prefix+"channels", old.Token); code != http.StatusUnauthorized || e != errUnauthorized {
		t.Errorf("the old pairing's session: status %d, error %q", code, e)
	}
	s := h.session(macA)
	if s.State != store.DeviceApproved || s.Token == "" {
		t.Fatalf("the new pairing: %+v", s)
	}
	if code, _ := h.errorCode("GET", Prefix+"channels", s.Token); code != http.StatusOK {
		t.Errorf("the new pairing's session: status %d", code)
	}
	h.secrets[macA] = old.Secret
	if s := h.session(macA); s.State != store.DevicePending {
		t.Errorf("the old secret: %+v", s)
	}
}

func TestSessionNeedsJSON(t *testing.T) {
	h := newHarness(t)
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		req, err := http.NewRequest("POST", h.front.URL+Prefix+"session", strings.NewReader(`{"mac":"`+macA+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if ct != "" {
			req.Header.Set("Content-Type", ct)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q: status %d", ct, resp.StatusCode)
		}
	}
	if _, err := h.store.Device(context.Background(), macA); err == nil {
		t.Error("a refused request recorded the box")
	}
	var s sessionResponse
	req := `{"mac":"` + macA + `"}`
	if code := h.call("POST", Prefix+"session", "", req, &s); code != http.StatusOK || s.State != store.DevicePending {
		t.Errorf("with JSON: status %d, %+v", code, s)
	}
}
