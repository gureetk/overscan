// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
)

func newClient(t *testing.T, p *stalkertest.Portal, typed string, change ...func(*Config)) *Client {
	t.Helper()
	cfg := Config{Portal: p.URL + typed, MAC: stalkertest.MAC, TimeZone: "Europe/London"}
	for _, f := range change {
		f(&cfg)
	}
	c, err := New(cfg, &http.Client{Timeout: 10 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewChecksTheAddressAndMAC(t *testing.T) {
	for _, cfg := range []Config{
		{Portal: "ftp://portal.example/c/", MAC: stalkertest.MAC},
		{Portal: "http:///c/", MAC: stalkertest.MAC},
		{Portal: "http://user:pw@portal.example/c/", MAC: stalkertest.MAC},
		{Portal: "http://portal.example/c/", MAC: "00:1A:79:00:00"},
		{Portal: "http://portal.example/c/", MAC: "00:1A:79:00:00:5G"},
	} {
		if _, err := New(cfg, http.DefaultClient); err == nil {
			t.Errorf("%+v: no error", cfg)
		}
	}
	c, err := New(Config{Portal: "http://portal.example/c/", MAC: "00-1a-79-00-00-51"}, http.DefaultClient)
	if err != nil || c.config.MAC != stalkertest.MAC || c.config.Model != DefaultModel || c.config.TimeZone != "UTC" {
		t.Errorf("%+v, %v", c, err)
	}
}

func TestCandidates(t *testing.T) {
	for typed, want := range map[string][]string{
		"http://p.example/c/": {"http://p.example/c/", "http://p.example/server/load.php", "http://p.example/portal.php"},
		"http://p.example:8080/stalker_portal/c/": {"http://p.example:8080/stalker_portal/c/",
			"http://p.example:8080/stalker_portal/server/load.php", "http://p.example:8080/portal.php"},
		"http://p.example/portal.php": {"http://p.example/portal.php", "http://p.example/server/load.php"},
		"http://p.example": {"http://p.example", "http://p.example/server/load.php", "http://p.example/portal.php",
			"http://p.example/stalker_portal/server/load.php"},
		"http://p.example/stalker_portal/c": {"http://p.example/stalker_portal/c",
			"http://p.example/stalker_portal/server/load.php", "http://p.example/portal.php"},
	} {
		c, err := New(Config{Portal: typed, MAC: stalkertest.MAC}, http.DefaultClient)
		if err != nil {
			t.Fatal(err)
		}
		if got := candidates(c.portal); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %q, want %q", typed, got, want)
		}
	}
}

func TestSignInFindsTheAPI(t *testing.T) {
	for _, c := range []struct{ apiPath, typed string }{
		{"", "/stalker_portal/c/"},
		{"", ""},
		{"/portal.php", "/c/"},
		{"/server/load.php", "/c/"},
		{"/portal.php", "/portal.php"},
	} {
		p := stalkertest.New(t)
		p.APIPath = c.apiPath
		p.WatchdogTimeout = 90
		client := newClient(t, p, c.typed)
		prof, err := client.SignIn(context.Background())
		if err != nil {
			t.Errorf("%+v: %v", c, err)
			continue
		}
		if prof.ID != "7" || prof.WatchdogTimeout != 90*time.Second ||
			!prof.Expires.Equal(time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("%+v: profile %+v", c, prof)
		}
		if p.Calls("stb/handshake") != 1 || p.Calls("stb/get_profile") != 1 {
			t.Errorf("%+v: handshakes %d, profiles %d", c, p.Calls("stb/handshake"), p.Calls("stb/get_profile"))
		}
		if ua := p.UserAgents(); len(ua) != 1 || !strings.Contains(ua[0], "MAG200 stbapp") {
			t.Errorf("user agents %q", ua)
		}
	}
}

func TestSignInRefusals(t *testing.T) {
	p := stalkertest.New(t)
	if _, err := newClient(t, p, "/stalker_portal/c/", func(c *Config) { c.MAC = "00:1A:79:00:00:99" }).
		SignIn(context.Background()); !errors.Is(err, ErrRefused) {
		t.Errorf("unknown MAC: %v", err)
	}
	p.Blocked = true
	if _, err := newClient(t, p, "/stalker_portal/c/").SignIn(context.Background()); !errors.Is(err, ErrBlocked) {
		t.Errorf("blocked: %v", err)
	}

	p = stalkertest.New(t)
	p.DeviceID = "fictional-device"
	if _, err := newClient(t, p, "/stalker_portal/c/").SignIn(context.Background()); !errors.Is(err, ErrRefused) {
		t.Errorf("no device ID: %v", err)
	}
	if _, err := newClient(t, p, "/stalker_portal/c/", func(c *Config) { c.DeviceID = "fictional-device" }).
		SignIn(context.Background()); err != nil {
		t.Errorf("with the device ID: %v", err)
	}

	srv := http.Server{Handler: http.NotFoundHandler()}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go srv.Serve(ln)
	defer srv.Close()
	c, _ := New(Config{Portal: "http://" + ln.Addr().String() + "/c/", MAC: stalkertest.MAC}, http.DefaultClient)
	if _, err := c.SignIn(context.Background()); !errors.Is(err, ErrNotPortal) {
		t.Errorf("no portal: %v", err)
	}
}

func TestLogin(t *testing.T) {
	p := stalkertest.New(t)
	p.Login, p.Password = "viewer", "pw/1"
	ctx := context.Background()
	if _, err := newClient(t, p, "/stalker_portal/c/").Genres(ctx); !errors.Is(err, ErrRefused) {
		t.Errorf("without a login: %v", err)
	}
	bad := newClient(t, p, "/stalker_portal/c/", func(c *Config) { c.Login, c.Password = "viewer", "wrong" })
	if _, err := bad.SignIn(ctx); !errors.Is(err, ErrLogin) {
		t.Errorf("a wrong password: %v", err)
	}
	good := newClient(t, p, "/stalker_portal/c/", func(c *Config) { c.Login, c.Password = "viewer", "pw/1" })
	if g, err := good.Genres(ctx); err != nil || len(g) != 2 {
		t.Errorf("with the login: %+v, %v", g, err)
	}
}

func TestTokenLapse(t *testing.T) {
	for _, body := range []bool{false, true} {
		p := stalkertest.New(t)
		p.RefusedBody = body
		c := newClient(t, p, "/stalker_portal/c/")
		ctx := context.Background()
		if _, err := c.Genres(ctx); err != nil {
			t.Fatal(err)
		}
		p.ForgetToken()
		if g, err := c.Genres(ctx); err != nil || len(g) != 2 {
			t.Errorf("after the token lapsed (body %v): %+v, %v", body, g, err)
		}
		if n := p.Calls("stb/handshake"); n != 2 {
			t.Errorf("handshakes %d, want 2", n)
		}
	}
}

func TestErrorsKeepTheAddressOut(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, _ := New(Config{Portal: "http://" + addr + "/c/", MAC: stalkertest.MAC, Login: "viewer", Password: "pw/secret"},
		&http.Client{Timeout: 2 * time.Second})
	_, err := c.SignIn(context.Background())
	if err == nil {
		t.Fatal("no error")
	}
	for _, s := range []string{addr, stalkertest.MAC, "pw/secret", "viewer"} {
		if strings.Contains(err.Error(), s) {
			t.Errorf("error %q holds %q", err, s)
		}
	}
}

func TestAccountAndWatchdog(t *testing.T) {
	p := stalkertest.New(t)
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	end, err := c.Account(ctx)
	if err != nil || !end.Equal(time.Unix(1804809600, 0)) {
		t.Errorf("account %v, %v", end, err)
	}
	if msgs, err := c.Watchdog(ctx); err != nil || msgs != 0 || p.Calls("watchdog/get_events") != 1 {
		t.Errorf("watchdog %d, %v", msgs, err)
	}
	for timeout, want := range map[time.Duration]time.Duration{0: 80 * time.Second, 90 * time.Second: 60 * time.Second,
		20 * time.Second: 30 * time.Second, 600 * time.Second: 120 * time.Second} {
		c.profile.WatchdogTimeout = timeout
		if got := c.WatchdogEvery(); got != want {
			t.Errorf("timeout %v: every %v, want %v", timeout, got, want)
		}
	}
}

func TestParseDate(t *testing.T) {
	for s, want := range map[string]time.Time{
		"1804809600":               time.Unix(1804809600, 0),
		"2027-03-12 08:30:00":      time.Date(2027, 3, 12, 8, 30, 0, 0, time.UTC),
		"2027-03-12":               time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC),
		"March 12, 2027, 12:00 am": time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC),
		"March 12, 2027, 3:15 pm":  time.Date(2027, 3, 12, 15, 15, 0, 0, time.UTC),
	} {
		got, ok := ParseDate(s)
		if !ok || !got.Equal(want) {
			t.Errorf("%q: %v, %v; want %v", s, got, ok, want)
		}
	}
	for _, s := range []string{"", "0", "-5", "unlimited", "12/03/2027", "0000-01-01", "December 1, 0000, 0:00 am"} {
		if _, ok := ParseDate(s); ok {
			t.Errorf("%q read as a date", s)
		}
	}
}
