// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/hub"
)

func probeServer(t *testing.T) (*Server, string, *bytes.Buffer) {
	t.Helper()
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.ProbeReports = true
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	st := testStore(t)
	return New(cfg, log, "test", Services{Store: st, Hub: hub.New(st, log, "test", nil), AdminToken: testAdminToken}), filepath.Join(cfg.DataDir, "probe"), &logs
}

func get(s *Server, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postReport(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/probe/report", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.1.20:5000"
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// Reports are saved only while the configuration lets them, and only from
// the server's own networks (D120).
func TestProbeReportsAreOffUnlessTurnedOn(t *testing.T) {
	s, dir, _ := probeServer(t)
	s.cfg.ProbeReports = false
	rec := postReport(s, `{"environment": {"user_agent": "test"}}`)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "probe_reports") {
		t.Errorf("with reports off: %d %q", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("with reports off, the folder: %v", err)
	}
	// The page is served still, and shows its results on the screen.
	if rec := get(s, "/probe/"); rec.Code != http.StatusOK {
		t.Errorf("the page with reports off: %d", rec.Code)
	}

	s.cfg.ProbeReports = true
	req := httptest.NewRequest(http.MethodPost, "/probe/report", strings.NewReader(`{}`))
	req.RemoteAddr = "203.0.113.7:5000"
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "own networks") {
		t.Errorf("from outside the server's networks: %d %q", rec.Code, rec.Body)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("from outside, the folder: %v", err)
	}
}

func TestProbePageIsServedUncached(t *testing.T) {
	s, _, _ := probeServer(t)
	for path, want := range map[string]string{
		"/probe/":         `<script src="probe.js">`,
		"/probe/probe.js": "PROBE_VERSION",
	} {
		rec := get(s, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: body lacks %q", path, want)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", path, got)
		}
	}
}

func TestProbeWithoutSlashRedirects(t *testing.T) {
	s, _, _ := probeServer(t)
	rec := get(s, "/probe")
	// The status is ServeMux's choice and differs between Go releases (301 in
	// Go 1.24, 307 in Go 1.27). What matters is where it points.
	if rec.Code < 300 || rec.Code > 399 || rec.Header().Get("Location") != "/probe/" {
		t.Errorf("status %d, Location %q, want a redirect to /probe/", rec.Code, rec.Header().Get("Location"))
	}
}

func TestProbeReportIsSaved(t *testing.T) {
	s, dir, logs := probeServer(t)
	report := `{"probe_version":1,"environment":{"user_agent":"TestBrowser/1.0"},"mag":{"gstb":{"info":{"GetDeviceModel":"MAG000"}}}}`
	if rec := postReport(s, report); rec.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) != 1 {
		t.Fatalf("saved files %v, err %v", files, err)
	}
	info, err := os.Stat(files[0])
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file mode %v, want 0600", perm)
	}
	saved, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(saved, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(report), &want); err != nil {
		t.Fatal(err)
	}
	// The server adds what only it sees: no cookie came back here.
	want.(map[string]any)["server"] = map[string]any{"http_cookie_from_last_run": nil}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("saved %s, want the report as sent plus the server's part", saved)
	}
	if out := logs.String(); !strings.Contains(out, "TestBrowser/1.0") || !strings.Contains(out, "MAG000") {
		t.Errorf("log lacks the user agent or model: %s", out)
	}
}

func TestProbeReportRejectsBadBodies(t *testing.T) {
	s, dir, _ := probeServer(t)
	for name, body := range map[string]string{
		"not JSON":  "hello",
		"an array":  "[1, 2]",
		"too large": `{"padding":"` + strings.Repeat("x", maxProbeReportBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := postReport(s, body)
			want := http.StatusBadRequest
			if name == "too large" {
				want = http.StatusRequestEntityTooLarge
			}
			if rec.Code != want {
				t.Errorf("status %d, want %d", rec.Code, want)
			}
		})
	}
	if files, _ := filepath.Glob(filepath.Join(dir, "*.json")); len(files) != 0 {
		t.Errorf("saved %v for bad reports", files)
	}
}

func TestProbeReportsAreCapped(t *testing.T) {
	s, dir, _ := probeServer(t)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	for i := range maxProbeReports {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("old-%03d.json", i)), []byte("{}\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if rec := postReport(s, `{"probe_version":1}`); rec.Code != http.StatusInsufficientStorage {
		t.Errorf("status %d, want 507", rec.Code)
	}
}

func TestProbeCookieFromLastRun(t *testing.T) {
	s, dir, _ := probeServer(t)
	jar := map[string]*http.Cookie{}
	// run loads the page and sends a report as a browser would, keeping
	// cookies in jar, and returns what the saved report says about the
	// cookie from the last run.
	run := func() any {
		t.Helper()
		page := httptest.NewRequest(http.MethodGet, "/probe/", nil)
		for _, c := range jar {
			page.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, page)
		for _, c := range rec.Result().Cookies() {
			if !c.HttpOnly {
				t.Errorf("cookie %s is not HttpOnly", c.Name)
			}
			jar[c.Name] = c
		}

		before, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		report := httptest.NewRequest(http.MethodPost, "/probe/report", strings.NewReader(`{"probe_version":2}`))
		report.RemoteAddr = "192.168.1.20:5000"
		for _, c := range jar {
			report.AddCookie(c)
		}
		rec = httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, report)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("status %d", rec.Code)
		}
		after, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		var newest string
		for _, f := range after {
			if !slices.Contains(before, f) {
				newest = f
			}
		}
		saved, err := os.ReadFile(newest)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Server map[string]any `json:"server"`
		}
		if err := json.Unmarshal(saved, &got); err != nil {
			t.Fatal(err)
		}
		return got.Server["http_cookie_from_last_run"]
	}

	if got := run(); got != nil {
		t.Errorf("first run found %v", got)
	}
	served := jar[probeCookie].Value
	if got := run(); got != served {
		t.Errorf("second run found %v, want %q", got, served)
	}
	if script := get(s, "/probe/probe.js"); len(script.Result().Cookies()) != 0 {
		t.Error("the script sets cookies too")
	}
}
