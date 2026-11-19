// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/devices"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/store"
)

// Fictional MAC addresses.
const (
	macA = "00:1A:79:00:00:01"
	macB = "00:1A:79:00:00:02"
)

type harness struct {
	t     *testing.T
	api   *API
	hub   *hub.Hub
	store *store.Store
	now   time.Time
	logs  *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h := &harness{t: t, store: st, now: time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC), logs: logs}
	h.hub = hub.New(st, log, "test", map[int64]hub.Source{})
	reg := devices.New(st, log)
	reg.Now = func() time.Time { return h.now }
	h.api = New(st, reg, h.hub, log)
	h.api.now = func() time.Time { return h.now }
	h.api.local = time.UTC
	return h
}

// call sends a portal call with the given query (type, action and so on),
// as the device with the given MAC (none if empty) and bearer token.
func (h *harness) call(query, mac, token string, header ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/stalker_portal/server/load.php?"+query+"&JsHttpRequest=1-xml", nil)
	if mac != "" {
		r.AddCookie(&http.Cookie{Name: "mac", Value: url.QueryEscape(mac)})
		r.AddCookie(&http.Cookie{Name: "stb_lang", Value: "en"})
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	h.api.ServeHTTP(rec, r)
	return rec
}

// js decodes the "js" member of a JSON answer into v.
func (h *harness) js(rec *httptest.ResponseRecorder, v any) {
	h.t.Helper()
	if rec.Code != http.StatusOK {
		h.t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct{ JS json.RawMessage }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		h.t.Fatalf("answer %q: %v", rec.Body, err)
	}
	if err := json.Unmarshal(body.JS, v); err != nil {
		h.t.Fatalf("js %s: %v", body.JS, err)
	}
}

func (h *harness) handshake(mac string) string {
	h.t.Helper()
	var js struct{ Token string }
	h.js(h.call("type=stb&action=handshake&token=", mac, ""), &js)
	return js.Token
}

func (h *harness) approve(mac string) {
	h.t.Helper()
	if _, err := h.store.ApproveDevice(context.Background(), mac, 1, "", h.now); err != nil {
		h.t.Fatal(err)
	}
}

func wantAuthFailed(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Body.String(), "Authorization failed.") {
		t.Errorf("status %d, body %q; want 401 and Authorization failed.", rec.Code, rec.Body)
	}
}

func TestUnknownDeviceWaitsForApproval(t *testing.T) {
	h := newHarness(t)
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", macA, "", "X-User-Agent", "Model: MAG250; Link: WiFi"))

	d, err := h.store.Device(context.Background(), macA)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != store.DevicePending || d.Model != "MAG250" || !d.LastSeen.Equal(h.now) {
		t.Errorf("device %+v", d)
	}

	// It stays refused until it is approved.
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", macA, ""))
	h.approve(macA)
	if token := h.handshake(macA); !regexp.MustCompile(`^[0-9A-F]{32}$`).MatchString(token) {
		t.Errorf("token %q", token)
	}
}

func TestMissingOrInvalidMACIsRefused(t *testing.T) {
	h := newHarness(t)
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", "", ""))
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", "not-a-mac", ""))
	if ds, err := h.store.Devices(context.Background()); err != nil || len(ds) != 0 {
		t.Errorf("devices %+v, %v", ds, err)
	}
}

func TestSession(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.handshake(macA)

	var p profile
	h.js(h.call("type=stb&action=get_profile&hd=1&stb_type=MAG322&sn=TESTSERIAL", macA, token), &p)
	d, err := h.store.Device(context.Background(), macA)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID != fmt.Sprint(d.ID) || p.Blocked != "0" || p.WatchdogTimeout != "120" || p.ExpireBillingDate != "" {
		t.Errorf("profile %+v", p)
	}
	if d.Model != "MAG322" {
		t.Errorf("model %q, want the one from stb_type", d.Model)
	}

	var ok bool
	h.js(h.call("type=stb&action=do_auth&login=a&password=b", macA, token), &ok)
	if !ok {
		t.Error("do_auth answered false")
	}

	var info map[string]string
	h.js(h.call("type=account_info&action=get_main_info", macA, token), &info)
	if len(info) != 0 {
		t.Errorf("main info %v for an account without an end date", info)
	}

	var events struct{ Data struct{ Msgs int } }
	h.js(h.call("type=watchdog&action=get_events&init=0&cur_play_type=1&event_active_id=0", macA, token), &events)
	if events.Data.Msgs != 0 {
		t.Errorf("events %+v", events)
	}
}

func TestEndDate(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	if err := h.store.SetAccountExpiry(context.Background(), 1, time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	token := h.handshake(macA)

	var p profile
	h.js(h.call("type=stb&action=get_profile", macA, token), &p)
	var info struct {
		EndDate string `json:"end_date"`
	}
	h.js(h.call("type=account_info&action=get_main_info", macA, token), &info)
	for _, got := range []string{p.ExpireBillingDate, info.EndDate} {
		if got != "2027-03-12 00:00:00" {
			t.Errorf("end date %q", got)
		}
	}
}

func TestTokens(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.approve(macB)
	const profileCall = "type=stb&action=get_profile"

	wantAuthFailed(t, h.call(profileCall, macA, ""))
	wantAuthFailed(t, h.call(profileCall, macA, "0123456789ABCDEF0123456789ABCDEF"))

	first := h.handshake(macA)
	second := h.handshake(macA)
	if first == second {
		t.Fatal("two handshakes gave the same token")
	}
	wantAuthFailed(t, h.call(profileCall, macA, first))
	if rec := h.call(profileCall, macA, second); rec.Code != http.StatusOK {
		t.Errorf("current token: status %d", rec.Code)
	}

	// A token works only for the device it was given to.
	wantAuthFailed(t, h.call(profileCall, macB, second))

	// Removing the device ends its session.
	if err := h.store.RemoveDevice(context.Background(), macA); err != nil {
		t.Fatal(err)
	}
	wantAuthFailed(t, h.call(profileCall, macA, second))
}

func TestBlockedDeviceSeesOnlyItsProfile(t *testing.T) {
	h := newHarness(t)
	if _, err := h.store.BlockDevice(context.Background(), macA); err != nil {
		t.Fatal(err)
	}
	token := h.handshake(macA)
	var p profile
	h.js(h.call("type=stb&action=get_profile", macA, token), &p)
	if p.Blocked != "1" {
		t.Errorf("profile %+v", p)
	}
	wantAuthFailed(t, h.call("type=watchdog&action=get_events", macA, token))
}

func TestUnknownActionIsCaptured(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.handshake(macA)

	rec := h.call("type=itv&action=set_fav&fav_ch=5&sn=TESTSERIAL&login=someone&api_signature=x", macA, token,
		"User-Agent", "TestClient/1.0")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	cs, err := h.store.Captures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 1 {
		t.Fatalf("captures %+v", cs)
	}
	c := cs[0]
	if c.Module != "itv" || c.Action != "set_fav" || c.Params != "api_signature,fav_ch,login,sn" || c.UserAgent != "TestClient/1.0" {
		t.Errorf("capture %+v", c)
	}
	example, err := url.ParseQuery(c.Example)
	if err != nil {
		t.Fatal(err)
	}
	if example.Get("fav_ch") != "5" || example.Get("sn") != redacted || example.Get("login") != redacted || example.Get("api_signature") != redacted {
		t.Errorf("example %q", c.Example)
	}
}

func TestUnknownRequestsFromUnknownDevicesAreNotCaptured(t *testing.T) {
	h := newHarness(t)
	h.call("type=itv&action=set_fav", macA, "")
	if cs, err := h.store.Captures(context.Background()); err != nil || len(cs) != 0 {
		t.Errorf("captures %+v, %v", cs, err)
	}
}

func TestPendingDevicesAreCapped(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for i := range devices.MaxPending {
		mac := fmt.Sprintf("00:1A:79:00:%02X:%02X", i/256, i%256)
		if _, err := h.store.SeeDevice(ctx, mac, "", h.now); err != nil {
			t.Fatal(err)
		}
	}
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", "00:1A:79:FF:FF:FF", ""))
	if _, err := h.store.Device(ctx, "00:1A:79:FF:FF:FF"); err == nil {
		t.Error("a device past the cap was recorded")
	}
}

func TestLastSeenIsWrittenAtMostOnceAMinute(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	start := h.now
	token := h.handshake(macA)

	h.now = start.Add(30 * time.Second)
	h.call("type=watchdog&action=get_events", macA, token)
	if d, _ := h.store.Device(ctx, macA); !d.LastSeen.Equal(start) {
		t.Errorf("last seen %v after 30 s, want %v", d.LastSeen, start)
	}

	h.now = start.Add(2 * time.Minute)
	h.call("type=watchdog&action=get_events", macA, token)
	if d, _ := h.store.Device(ctx, macA); !d.LastSeen.Equal(h.now) {
		t.Errorf("last seen %v after 2 min, want %v", d.LastSeen, h.now)
	}
}

func TestPostAndMethods(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)

	r := httptest.NewRequest(http.MethodPost, "/portal.php", strings.NewReader("type=stb&action=handshake&token="))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "mac", Value: url.QueryEscape(macA)})
	rec := httptest.NewRecorder()
	h.api.ServeHTTP(rec, r)
	var js struct{ Token string }
	h.js(rec, &js)
	if js.Token == "" {
		t.Error("no token from a POST handshake")
	}

	rec = httptest.NewRecorder()
	h.api.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/portal.php", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("PUT: status %d", rec.Code)
	}
}

func TestReportedModel(t *testing.T) {
	for header, want := range map[string]string{
		"Model: MAG250; Link: WiFi":     "MAG250",
		"Link: Ethernet; model: MAG322": "MAG322",
		"":                              "",
		"nonsense":                      "",
	} {
		r := httptest.NewRequest(http.MethodGet, "/portal.php", nil)
		r.Header.Set("X-User-Agent", header)
		_ = r.ParseForm()
		if got := reportedModel(r); got != want {
			t.Errorf("X-User-Agent %q: model %q, want %q", header, got, want)
		}
	}
}

func TestPairedBoxIsRefused(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	if _, err := h.store.AdoptSecret(context.Background(), macA, store.HashSecret("the box's secret"), h.now); err != nil {
		t.Fatal(err)
	}
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", macA, ""))
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", macA, ""))
	if n := strings.Count(h.logs.String(), "belongs to a box paired with the MAG page"); n != 1 {
		t.Errorf("logged %d times, want 1: %s", n, h.logs)
	}
	h.now = h.now.Add(refusalLogInterval)
	wantAuthFailed(t, h.call("type=stb&action=handshake&token=", macA, ""))
	if n := strings.Count(h.logs.String(), "belongs to a box paired with the MAG page"); n != 2 {
		t.Errorf("logged %d times after %s, want 2", n, refusalLogInterval)
	}
}
