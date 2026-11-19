// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/config"
)

func getFrom(t *testing.T, h http.Handler, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestAdminSharesTheMainAddress(t *testing.T) {
	s := newServer(t, config.Default())
	if s.AdminHandler() != nil {
		t.Error("an admin handler of its own without admin_listen")
	}
	if rec := getFrom(t, s.Handler(), "/admin/sign-in"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Overscan") {
		t.Errorf("sign-in page: status %d", rec.Code)
	}
	if rec := getFrom(t, s.Handler(), "/admin"); rec.Code/100 != 3 || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("/admin: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestAdminAPIIsRouted(t *testing.T) {
	s := newServer(t, config.Default())
	rec := getFrom(t, s.Handler(), "/api/admin/v1/session")
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"unauthenticated"`) {
		t.Errorf("on the main address: status %d, %s", rec.Code, rec.Body)
	}

	cfg := config.Default()
	cfg.AdminListen = "127.0.0.1:8081"
	s = newServer(t, cfg)
	if rec = getFrom(t, s.Handler(), "/api/admin/v1/session"); rec.Code != http.StatusNotFound {
		t.Errorf("the main address serves the API with admin_listen set: status %d", rec.Code)
	}
	if rec = getFrom(t, s.AdminHandler(), "/api/admin/v1/session"); rec.Code != http.StatusUnauthorized {
		t.Errorf("on the admin address: status %d", rec.Code)
	}
}

func TestAdminOnItsOwnAddress(t *testing.T) {
	cfg := config.Default()
	cfg.AdminListen = "127.0.0.1:8081"
	s := newServer(t, cfg)
	if rec := getFrom(t, s.Handler(), "/admin/sign-in"); rec.Code != http.StatusNotFound {
		t.Errorf("the main address serves the admin: status %d", rec.Code)
	}
	a := s.AdminHandler()
	if a == nil {
		t.Fatal("no admin handler")
	}
	if rec := getFrom(t, a, "/admin/sign-in"); rec.Code != http.StatusOK {
		t.Errorf("sign-in page: status %d", rec.Code)
	}
	if rec := getFrom(t, a, "/"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("root: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range []string{"/c/", "/healthz", "/portal.php"} {
		if rec := getFrom(t, a, path); rec.Code != http.StatusNotFound {
			t.Errorf("the admin address serves %s: status %d", path, rec.Code)
		}
	}
}

func TestServeAdmin(t *testing.T) {
	cfg := config.Default()
	cfg.AdminListen = "127.0.0.1:0"
	s := newServer(t, cfg)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.ServeAdmin(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/admin/sign-in")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "Sign in") {
		t.Errorf("status %d", resp.StatusCode)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("ServeAdmin returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ServeAdmin did not return")
	}

	if err := newServer(t, config.Default()).ServeAdmin(context.Background(), ln); err == nil {
		t.Error("ServeAdmin without admin_listen did not fail")
	}
}

func TestRunFailsWhenTheAdminAddressIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	cfg := config.Default()
	cfg.Listen = "127.0.0.1:0"
	cfg.AdminListen = taken.Addr().String()
	errc := make(chan error, 1)
	go func() { errc <- newServer(t, cfg).Run(context.Background()) }()
	select {
	case err := <-errc:
		if err == nil {
			t.Error("no error for an admin address already in use")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept serving without its admin address")
	}
}
