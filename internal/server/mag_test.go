// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/hub"
)

func magServer(t *testing.T) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	st := testStore(t)
	return New(config.Default(), log, "test", Services{Store: st, Hub: hub.New(st, log, "test", nil), AdminToken: testAdminToken}), &logs
}

func serve(s *Server, path, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if remote != "" {
		r.RemoteAddr = remote
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	return rec
}

func TestMAGPageIsServed(t *testing.T) {
	s, _ := magServer(t)
	assets := regexp.MustCompile(`(?:src|href)="(app\.[0-9a-f]{10}\.(?:js|css))"`)
	for _, prefix := range magPaths {
		rec := serve(s, prefix, "")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%s: status %d, type %q", prefix, rec.Code, rec.Header().Get("Content-Type"))
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q", prefix, cc)
		}
		names := assets.FindAllStringSubmatch(rec.Body.String(), -1)
		if len(names) != 2 {
			t.Fatalf("%s: assets %q in %s", prefix, names, rec.Body)
		}
		for _, m := range names {
			rec := serve(s, prefix+m[1], "")
			if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
				t.Errorf("%s: status %d", prefix+m[1], rec.Code)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
				t.Errorf("%s: Cache-Control %q", prefix+m[1], cc)
			}
		}
	}

	// The fonts and pictures that the stylesheet and script name.
	var css string
	for _, m := range assets.FindAllStringSubmatch(serve(s, "/c/", "").Body.String(), -1) {
		if strings.HasSuffix(m[1], ".css") {
			css = serve(s, "/c/"+m[1], "").Body.String()
		}
	}
	font := regexp.MustCompile(`url\("?(instrument-sans-500\.[0-9a-f]{10}\.woff)"?\)`).FindStringSubmatch(css)
	if font == nil {
		t.Fatalf("no font in the stylesheet")
	}
	for name, kind := range map[string]string{font[1]: "font/woff", "fonts-OFL.txt": "text/plain"} {
		rec := serve(s, "/c/"+name, "")
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), kind) {
			t.Errorf("%s: status %d, type %q", name, rec.Code, rec.Header().Get("Content-Type"))
		}
		immutable := rec.Header().Get("Cache-Control") == "public, max-age=31536000, immutable"
		if immutable != strings.HasSuffix(name, ".woff") {
			t.Errorf("%s: Cache-Control %q", name, rec.Header().Get("Cache-Control"))
		}
	}

	rec := serve(s, "/c/app.0123456789.js", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("a missing asset: status %d", rec.Code)
	}
	if rec = serve(s, "/c/nothing.txt", ""); rec.Code != http.StatusNotFound || rec.Header().Get("Cache-Control") != "" {
		t.Errorf("another file: status %d, Cache-Control %q", rec.Code, rec.Header().Get("Cache-Control"))
	}
}

func TestRootAndBarePathsLeadToTheMAGPage(t *testing.T) {
	s, _ := magServer(t)
	for path, want := range map[string]string{"/": "/c/", "/c": "/c/", "/stalker_portal/c": "/stalker_portal/c/"} {
		rec := serve(s, path, "")
		if rec.Code < 300 || rec.Code > 399 || rec.Header().Get("Location") != want {
			t.Errorf("%s: status %d, Location %q; want a redirect to %s", path, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	if rec := serve(s, "/nothing", ""); rec.Code != http.StatusNotFound {
		t.Errorf("/nothing: status %d", rec.Code)
	}
}

func TestMAGPageRequestsAreLoggedOncePerBox(t *testing.T) {
	s, logs := magServer(t)
	serve(s, "/stalker_portal/c/", "192.0.2.20:40000")
	serve(s, "/stalker_portal/c/", "192.0.2.20:40001") // the box's second request
	serve(s, "/c/", "192.0.2.21:40000")
	if n := strings.Count(logs.String(), "MAG page requested"); n != 2 {
		t.Errorf("logged %d times, want 2:\n%s", n, logs)
	}
	if !strings.Contains(logs.String(), "from=192.0.2.20") || !strings.Contains(logs.String(), "from=192.0.2.21") {
		t.Errorf("logs:\n%s", logs)
	}
}
