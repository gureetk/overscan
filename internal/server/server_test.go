// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/store"
)

// testAdminToken is the admin token of test servers.
const testAdminToken = "0123456789abcdef0123456789abcdef"

func newServer(t *testing.T, cfg config.Config) *Server {
	t.Helper()
	s, _ := newServerWithStore(t, cfg, nil)
	return s
}

// newServerWithStore returns a server and its store, whose sources the hub
// knows by the given IDs.
func newServerWithStore(t *testing.T, cfg config.Config, sources map[int64]hub.Source) (*Server, *store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st := testStore(t)
	return New(cfg, log, "test", Services{Store: st, Hub: hub.New(st, log, "test", sources), AdminToken: testAdminToken}), st
}

// testStore opens a database in a temporary directory.
func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(t, config.Default()).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var body struct{ Status, Version string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "ok" || body.Version != "test" {
		t.Errorf("got %+v", body)
	}
}

func TestHealthRejectsOtherMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(t, config.Default()).Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/healthz", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status %d, want 405", rec.Code)
	}
}

func TestServeStopsWhenContextIsDone(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- newServer(t, config.Default()).Serve(ctx, ln) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Serve returned %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after the context was cancelled")
	}
}

func TestRunFailsWhenAddressIsTaken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := config.Default()
	cfg.Listen = ln.Addr().String()
	if err := newServer(t, cfg).Run(context.Background()); err == nil {
		t.Error("no error for an address already in use")
	}
}

func TestStalkerPathsAreRouted(t *testing.T) {
	s := newServer(t, config.Default())
	for _, path := range stalkerPaths {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path+"?type=stb&action=handshake&token=", nil))
		// No mac cookie: the API refuses the call, which shows it was reached.
		if rec.Code != http.StatusUnauthorized || rec.Body.String() != "Authorization failed." {
			t.Errorf("%s: status %d, body %q", path, rec.Code, rec.Body)
		}
	}
}

func TestStreamPathsAreRouted(t *testing.T) {
	s := newServer(t, config.Default())
	for _, path := range []string{"/live/nothing.ts", "/vod/nothing.mp4"} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
			// A link that is not one: the hub refuses it, which shows it was reached.
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s: status %d, body %q", method, path, rec.Code, rec.Body)
			}
		}
	}
}

func TestDeviceAPIIsRouted(t *testing.T) {
	s, st := newServerWithStore(t, config.Default(), nil)
	rec := httptest.NewRecorder()
	body := strings.NewReader(`{"mac":"00:1A:79:00:00:01","model":"MAG322"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/device/v1/session", body)
	req.Header.Set("Content-Type", "application/json")
	s.Handler().ServeHTTP(rec, req)
	var got struct{ State, Version string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rec.Code, rec.Body)
	}
	if got.State != "pending" || got.Version != "test" {
		t.Errorf("answer %+v", got)
	}
	// The box waits in the same registry as Stalker clients.
	if d, err := st.Device(context.Background(), "00:1A:79:00:00:01"); err != nil || d.State != store.DevicePending {
		t.Errorf("device %+v, %v", d, err)
	}
}
