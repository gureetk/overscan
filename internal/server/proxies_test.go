// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/store"
)

// newProxiedServer returns a server that trusts the given reverse proxies
// (D119), with the admin's password set and the household's playlist on,
// and the playlist's token.
func newProxiedServer(t *testing.T, proxies ...string) (*Server, string) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.TrustedProxies = proxies
	srv, st := newServerWithStore(t, cfg, nil)
	ctx := context.Background()
	hash, err := admin.HashPassword(ctx, "correct horse battery", admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAdminPassword(ctx, "admin", hash, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOutputs(ctx, true); err != nil {
		t.Fatal(err)
	}
	token, err := st.NewAccountToken(ctx, store.HouseholdAccount)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{M3U: true}); err != nil {
		t.Fatal(err)
	}
	return srv, token
}

// proxied returns a request from remote with the given headers, each
// "Name: value".
func proxied(method, target, body, remote string, headers ...string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r.RemoteAddr = remote
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		r.Header.Add(name, value)
	}
	return r
}

const proxy = "192.168.1.2:40000"

func TestOutputsThroughATrustedProxy(t *testing.T) {
	srv, token := newProxiedServer(t, "192.168.1.2")
	for _, tt := range []struct {
		name    string
		from    string
		headers []string
		want    int
	}{
		{"a player at home, through the proxy", proxy, []string{"X-Forwarded-For: 192.168.1.20"}, http.StatusOK},
		{"a player outside, through the proxy", proxy, []string{"X-Forwarded-For: 203.0.113.7"}, http.StatusForbidden},
		{"a player outside who says it is at home, through the proxy", proxy,
			[]string{"X-Forwarded-For: 192.168.1.20, 203.0.113.7"}, http.StatusForbidden},
		{"through the proxy, which names no one", proxy, nil, http.StatusForbidden},
		{"a player at home, straight", "192.168.1.20:5000", nil, http.StatusOK},
		{"a player at home that names another, straight", "192.168.1.20:5000",
			[]string{"X-Forwarded-For: 203.0.113.7"}, http.StatusOK},
		{"a player outside that says it is at home, straight", "203.0.113.7:5000",
			[]string{"X-Forwarded-For: 192.168.1.20"}, http.StatusForbidden},
	} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, proxied("GET", "/playlist/"+token+".m3u", "", tt.from, tt.headers...))
		if rec.Code != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, rec.Code, tt.want)
		}
	}
}

func TestAdminThroughATrustedProxy(t *testing.T) {
	srv, _ := newProxiedServer(t, "192.168.1.2")
	signIn := func(from, password string, headers ...string) (int, *http.Cookie) {
		t.Helper()
		r := proxied("POST", "/api/admin/v1/session", `{"username": "admin", "password": "`+password+`"}`, from,
			append(headers, "Content-Type: application/json")...)
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, r)
		for _, c := range rec.Result().Cookies() {
			if c.Name == admin.CookieName {
				return rec.Code, c
			}
		}
		return rec.Code, nil
	}

	// The session's cookie is Secure when the proxy says the browser came
	// over HTTPS, and only then.
	for _, tt := range []struct {
		name    string
		from    string
		headers []string
		secure  bool
	}{
		{"over HTTPS through the proxy", proxy, []string{"X-Forwarded-For: 192.168.1.20", "X-Forwarded-Proto: https"}, true},
		{"over HTTP through the proxy", proxy, []string{"X-Forwarded-For: 192.168.1.20", "X-Forwarded-Proto: http"}, false},
		{"through the proxy, which does not say", proxy, []string{"X-Forwarded-For: 192.168.1.20"}, false},
		{"straight, saying HTTPS", "192.168.1.30:5000", []string{"X-Forwarded-Proto: https"}, false},
	} {
		code, c := signIn(tt.from, "correct horse battery", tt.headers...)
		if code != http.StatusOK || c == nil {
			t.Fatalf("%s: %d, cookie %v", tt.name, code, c)
		}
		if c.Secure != tt.secure {
			t.Errorf("%s: Secure = %v, want %v", tt.name, c.Secure, tt.secure)
		}
	}

	// Wrong passwords count against the client that the proxy names, not
	// against everyone behind it.
	for range 5 {
		if code, _ := signIn(proxy, "wrong horse battery", "X-Forwarded-For: 203.0.113.7"); code != http.StatusUnauthorized {
			t.Fatalf("a wrong password: %d", code)
		}
	}
	if code, _ := signIn(proxy, "correct horse battery", "X-Forwarded-For: 203.0.113.7"); code != http.StatusTooManyRequests {
		t.Errorf("the right password from the client that failed: %d, want %d", code, http.StatusTooManyRequests)
	}
	if code, _ := signIn(proxy, "correct horse battery", "X-Forwarded-For: 192.168.1.20"); code != http.StatusOK {
		t.Errorf("another client through the same proxy: %d, want %d", code, http.StatusOK)
	}
}

func TestNoTrustedProxyByDefault(t *testing.T) {
	srv, token := newProxiedServer(t)
	// Without trusted proxies, a proxy's requests come from the proxy, as
	// before, and its headers count for nothing.
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, proxied("GET", "/playlist/"+token+".m3u", "", proxy, "X-Forwarded-For: 203.0.113.7"))
	if rec.Code != http.StatusOK {
		t.Errorf("a player through an untrusted proxy: %d, want %d", rec.Code, http.StatusOK)
	}
}
