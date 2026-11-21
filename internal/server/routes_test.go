// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/store"
)

// Who may call a route (docs/security.md, "Who can reach what").
type access int

const (
	// anyone: the health check, the box's page and its files, the probe
	// page, and the admin's sign-in with its files.
	open access = iota
	// a signed-in administrator, with the session's CSRF token for a change;
	// others are sent to the sign-in page.
	adminPage
	// the same, answering 401 and 403 in JSON.
	adminAPI
	// a box with a session's token.
	deviceAPI
	// a box without a session: starting one, and the playback check's clips,
	// whose addresses carry a token of their own.
	deviceOpen
	// a Stalker client that an administrator approved, with a token from its
	// handshake.
	stalkerAPI
	// a player with a link signed for its device.
	link
	// a player with an account's token or login, on the server's own
	// networks, while the outputs are on.
	output
	// whoever has the admin token in the data directory.
	debug
	// anyone, as plain images: the path names a picture by a key that only
	// the lists that show it give.
	picture
)

var accessNames = map[access]string{open: "open", adminPage: "admin page", adminAPI: "admin API", deviceAPI: "device API",
	deviceOpen: "device API without a session", stalkerAPI: "Stalker API", link: "signed link", output: "output",
	debug: "debug", picture: "picture"}

// routeAccess says who may call each route that the server has. A new
// route must be added here, which makes its author say who may call it.
var routeAccess = map[string]access{
	"GET /healthz":           open,
	"GET /probe/":            open,
	"POST /probe/report":     open,
	"GET /c/":                open,
	"GET /stalker_portal/c/": open,
	"GET /{$}":               open,

	"/portal.php":                     stalkerAPI,
	"/server/load.php":                stalkerAPI,
	"/stalker_portal/server/load.php": stalkerAPI,

	"POST /api/device/v1/session":             deviceOpen,
	"GET /api/device/v1/check/{token}/{file}": deviceOpen,
	"/api/device/v1/":                         deviceOpen,

	"GET /artwork/logo/{id}/{key}":     picture,
	"GET /artwork/poster/{id}/{key}":   picture,
	"GET /artwork/thumb/{id}/{key}":    picture,
	"GET /artwork/backdrop/{id}/{key}": picture,
	"GET /artwork/still/{id}/{key}":    picture,
	"/artwork/":                        picture,

	"GET /live/":    link,
	"GET /vod/":     link,
	"GET /archive/": link,

	"GET /playlist/{file}":                                  output,
	"GET /epg/{file}":                                       output,
	"GET /playlist/{token}/{file}":                          output,
	"GET /playlist/{token}/archive/{file}":                  output,
	"GET /player_api.php":                                   output,
	"POST /player_api.php":                                  output,
	"GET /get.php":                                          output,
	"GET /xmltv.php":                                        output,
	"GET /live/{user}/{pass}/{file}":                        output,
	"GET /movie/{user}/{pass}/{file}":                       output,
	"GET /series/{user}/{pass}/{file}":                      output,
	"GET /timeshift/{user}/{pass}/{file}":                   output,
	"GET /timeshift/{user}/{pass}/{minutes}/{start}/{file}": output,

	"GET /debug/lineup.m3u":   debug,
	"GET /debug/play/{entry}": debug,

	"POST /api/admin/v1/session":  open,
	"GET /admin/static/{name...}": open,
	"GET /admin/sign-in":          open,
	"POST /admin/sign-in":         open,
	"/admin/":                     open, // says that there is no such page
}

// The rest of the device API and the admin's need their sessions.
var (
	deviceRoute    = regexp.MustCompile(`^[A-Z]+ /api/device/v1/`)
	adminAPIRoute  = regexp.MustCompile(`^[A-Z]+ /api/admin/v1/`)
	adminPageRoute = regexp.MustCompile(`^[A-Z]+ /admin/`)
)

func accessOf(pattern string) (access, bool) {
	if a, ok := routeAccess[pattern]; ok {
		return a, true
	}
	switch {
	case deviceRoute.MatchString(pattern):
		return deviceAPI, true
	case adminAPIRoute.MatchString(pattern):
		return adminAPI, true
	case adminPageRoute.MatchString(pattern):
		return adminPage, true
	}
	return 0, false
}

// wildcardValue gives a request's path a value for each wildcard of a
// pattern.
var wildcardValue = map[string]string{
	"id": "1", "entry": "1", "mac": "00:1A:79:00:00:01", "code": "123456", "kind": "movies",
	"key": "000000000000000000000000", "file": "1.ts", "token": strings.Repeat("A", 43),
	"user": "nobody", "pass": "nothing", "minutes": "60", "start": "2026-12-04:10-00",
	"action": "approve", "which": "m3u", "name...": "admin.css", "$": "",
}

var wildcard = regexp.MustCompile(`\{([a-z$.]+)\}`)

// fileValue gives some routes' last wildcard a name of the form they take,
// so that a request gets past the name to what the route checks first.
var fileValue = map[string]string{
	"GET /playlist/{file}": strings.Repeat("A", 43) + ".m3u",
	"GET /epg/{file}":      strings.Repeat("A", 43) + ".xml.gz",
}

// requestFor returns a request for a pattern, its wildcards filled in with
// values, or with value for the last one when it is given.
func requestFor(t *testing.T, pattern, last string) *http.Request {
	t.Helper()
	if last == "" {
		last = fileValue[pattern]
	}
	method, path, ok := strings.Cut(pattern, " ")
	if !ok {
		method, path = "GET", pattern
	}
	all := wildcard.FindAllStringSubmatchIndex(path, -1)
	var b strings.Builder
	at := 0
	for i, m := range all {
		b.WriteString(path[at:m[0]])
		name := path[m[2]:m[3]]
		v, ok := wildcardValue[name]
		if !ok {
			t.Fatalf("%s: no value for {%s}", pattern, name)
		}
		if i == len(all)-1 && last != "" {
			v = last
		}
		b.WriteString(v)
		at = m[1]
	}
	b.WriteString(path[at:])
	target := b.String()
	if len(all) == 0 && last != "" && strings.HasSuffix(target, "/") {
		target += last // a prefix's files
	}
	r := httptest.NewRequest(method, target, nil)
	r.RemoteAddr = "192.0.2.20:51000"
	return r
}

type routeHarness struct {
	t      *testing.T
	srv    *Server
	st     *store.Store
	cookie *http.Cookie
	csrf   string
	key    []byte // the key file, which no route may serve
}

func newRouteHarness(t *testing.T) *routeHarness {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Default()
	cfg.DataDir = dir
	srv, st := newServerWithStore(t, cfg, nil)
	h := &routeHarness{t: t, srv: srv, st: st}
	// A key file with a mark that no answer may hold.
	h.key = []byte("the-key-file-that-no-route-serves-" + strings.Repeat("k", 30))
	if err := os.WriteFile(filepath.Join(dir, config.KeyFileName), h.key, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	hash, err := admin.HashPassword(ctx, "correct horse battery", admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAdminPassword(ctx, "admin", hash, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/admin/v1/session", strings.NewReader(`{"username": "admin", "password": "correct horse battery"}`))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.0.2.20:51000"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("signing in: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == admin.CookieName {
			h.cookie = c
		}
	}
	h.csrf = regexp.MustCompile(`"csrf_token":\s*"([^"]+)"`).FindStringSubmatch(rec.Body.String())[1]
	return h
}

func (h *routeHarness) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.srv.Handler().ServeHTTP(rec, r)
	return rec
}

// TestEveryRouteIsGuarded walks every route of the server and checks that
// a request without what the route asks for is refused, as its access
// says: the auth half of the plan's "auth and path-traversal test set".
func TestEveryRouteIsGuarded(t *testing.T) {
	h := newRouteHarness(t)
	main, _ := h.srv.Patterns()
	for _, p := range main {
		if _, ok := accessOf(p); !ok {
			t.Errorf("route %q has no access in routeAccess: say who may call it", p)
		}
	}
	for p := range routeAccess {
		if !slices.Contains(main, p) {
			t.Errorf("routeAccess names %q, which the server does not serve", p)
		}
	}

	refused := func(p string, a access, rec *httptest.ResponseRecorder) bool {
		switch a {
		case adminPage:
			return rec.Code == http.StatusSeeOther && strings.HasPrefix(rec.Header().Get("Location"), "/admin/sign-in")
		case adminAPI:
			return rec.Code == http.StatusUnauthorized && strings.Contains(rec.Body.String(), `"unauthenticated"`)
		case deviceAPI, stalkerAPI:
			return rec.Code == http.StatusUnauthorized
		case deviceOpen, link, output, debug, picture:
			return rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound ||
				rec.Code == http.StatusBadRequest || rec.Code == http.StatusUnsupportedMediaType
		}
		return rec.Code < 500
	}
	check := func(label string, outputs bool) {
		for _, p := range main {
			a, _ := accessOf(p)
			if a == output && !outputs {
				continue
			}
			rec := h.serve(requestFor(t, p, ""))
			if !refused(p, a, rec) {
				t.Errorf("%s: %s (%s) answered %d without credentials: %.200s", label, p, accessNames[a], rec.Code, rec.Body)
			}
		}
	}
	check("outputs off", true)

	// With the outputs on, a player still needs a token or a login, and
	// one from outside the server's own networks is refused before it is
	// asked for either.
	ctx := context.Background()
	if err := h.st.SetOutputs(ctx, true); err != nil {
		t.Fatal(err)
	}
	check("outputs on", true)
	for _, p := range main {
		if a, _ := accessOf(p); a != output {
			continue
		}
		r := requestFor(t, p, "")
		r.RemoteAddr = "203.0.113.7:40000"
		if rec := h.serve(r); rec.Code != http.StatusForbidden {
			t.Errorf("%s from outside the server's networks: %d, want 403", p, rec.Code)
		}
	}
}

// The admin on an address of its own leaves the main address, and is
// guarded there as it is on the main one.
func TestAdminOnItsOwnAddressIsGuarded(t *testing.T) {
	cfg := config.Default()
	cfg.DataDir = t.TempDir()
	cfg.AdminListen = "127.0.0.1:8081"
	srv, _ := newServerWithStore(t, cfg, nil)
	main, adm := srv.Patterns()
	for _, p := range main {
		if a, _ := accessOf(p); a == adminAPI || a == adminPage || strings.Contains(p, "/admin/") {
			t.Errorf("the main address serves %s with the admin on an address of its own", p)
		}
	}
	if len(adm) < 100 {
		t.Fatalf("the admin's address has %d routes", len(adm))
	}
	for _, p := range adm {
		a, ok := accessOf(p)
		if !ok && p == "GET /{$}" {
			continue // sends the browser to /admin/
		}
		if !ok || (a != adminAPI && a != adminPage && a != open) {
			t.Errorf("the admin's address serves %s (%s)", p, accessNames[a])
			continue
		}
		rec := httptest.NewRecorder()
		srv.AdminHandler().ServeHTTP(rec, requestFor(t, p, ""))
		switch {
		case a == adminAPI && rec.Code != http.StatusUnauthorized,
			a == adminPage && rec.Code != http.StatusSeeOther:
			t.Errorf("%s on the admin's address answered %d without a session", p, rec.Code)
		}
	}
}

// stalkerActions are the calls that the Stalker API answers, as its switch
// names them.
var stalkerAction = regexp.MustCompile(`case "([a-z_]+/[a-z_]+)"(?:, "([a-z_]+/[a-z_]+)")*:`)

// Every call of the Stalker API but the handshake needs the token that the
// handshake gives, from a device that the administrator approved.
func TestStalkerCallsNeedAToken(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "portal", "stalker", "stalker.go"))
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, m := range regexp.MustCompile(`"([a-z_]+/[a-z_]+)"`).FindAllStringSubmatch(strings.Join(stalkerAction.FindAllString(string(data), -1), " "), -1) {
		actions = append(actions, m[1])
	}
	if len(actions) < 15 {
		t.Fatalf("found %d Stalker calls: %v", len(actions), actions)
	}
	h := newRouteHarness(t)
	ctx := context.Background()
	const mac = "00:1A:79:00:00:31"
	ask := func(action string) int {
		module, act, _ := strings.Cut(action, "/")
		r := httptest.NewRequest("GET", "/stalker_portal/server/load.php?type="+module+"&action="+act+"&JsHttpRequest=1-xml", nil)
		r.AddCookie(&http.Cookie{Name: "mac", Value: url.QueryEscape(mac)})
		r.Header.Set("Authorization", "Bearer "+strings.Repeat("0", 32))
		return h.serve(r).Code
	}
	for _, action := range append(actions, "stb/handshake") {
		if code := ask(action); code != http.StatusUnauthorized {
			t.Errorf("%s from a device that waits for approval: %d, want 401", action, code)
		}
	}
	if _, err := h.st.ApproveDevice(ctx, mac, 1, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, action := range actions {
		if code := ask(action); code != http.StatusUnauthorized {
			t.Errorf("%s from an approved device with a token it was never given: %d, want 401", action, code)
		}
	}
	if code := ask("stb/handshake"); code != http.StatusOK {
		t.Errorf("the approved device's handshake: %d", code)
	}
}

// TestAdminChangesNeedTheCSRFToken checks every admin route that changes
// something, signed in, without the session's CSRF token, and with it
// from another site.
func TestAdminChangesNeedTheCSRFToken(t *testing.T) {
	h := newRouteHarness(t)
	main, _ := h.srv.Patterns()
	changes := 0
	for _, p := range main {
		a, _ := accessOf(p)
		method, _, _ := strings.Cut(p, " ")
		if (a != adminAPI && a != adminPage) || method == "GET" || method == "HEAD" {
			continue
		}
		changes++
		r := requestFor(t, p, "")
		r.AddCookie(h.cookie)
		rec := h.serve(r)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s signed in, without the CSRF token: %d, want 403: %.200s", p, rec.Code, rec.Body)
		}
		r = requestFor(t, p, "")
		r.AddCookie(h.cookie)
		r.Header.Set("X-CSRF-Token", h.csrf)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		if rec := h.serve(r); rec.Code != http.StatusForbidden {
			t.Errorf("%s signed in, with the CSRF token, from another site: %d, want 403: %.200s", p, rec.Code, rec.Body)
		}
	}
	if changes < 100 {
		t.Errorf("only %d admin routes change something", changes)
	}
}

// traversals are names that try to reach files outside what a route
// serves: escaped slashes, which a path's wildcard keeps, dots, and the
// key file's own path.
func traversals(key string) []string {
	up := strings.Repeat("..%2F", 12)
	abs := url.PathEscape(strings.TrimPrefix(key, "/"))
	return []string{
		up + "etc%2Fpasswd",
		up + abs,
		"%2F" + abs,
		strings.Repeat("%2E%2E%2F", 12) + abs,
		strings.Repeat("..%5C", 12) + strings.ReplaceAll(abs, "%2F", "%5C"),
		"....%2F%2F....%2F%2F" + abs,
		"%00" + abs,
	}
}

// TestNoRouteReachesFilesOutsideItsOwn sends every route that takes a name
// in its path names that try to climb out of what it serves, signed in
// where the route asks for it: the path-traversal half of the test set.
// No answer may hold the key file, or the system's list of users.
func TestNoRouteReachesFilesOutsideItsOwn(t *testing.T) {
	h := newRouteHarness(t)
	keyPath := filepath.Join(h.srv.cfg.DataDir, config.KeyFileName)
	main, _ := h.srv.Patterns()
	tried := 0
	for _, p := range main {
		a, _ := accessOf(p)
		if !strings.Contains(p, "{") && !strings.HasSuffix(p, "/") {
			continue
		}
		for _, name := range traversals(keyPath) {
			r := requestFor(t, p, name)
			if a == adminAPI || a == adminPage {
				r.AddCookie(h.cookie)
				r.Header.Set("X-CSRF-Token", h.csrf)
			}
			rec := h.serve(r)
			tried++
			body := rec.Body.String()
			if strings.Contains(body, string(h.key)) || strings.Contains(body, "root:x:0:0") {
				t.Errorf("%s with %q: %d, and the answer holds a file outside what the route serves", p, name, rec.Code)
			}
		}
	}
	if tried < 50*len(traversals(keyPath)) {
		t.Errorf("only %d requests tried", tried)
	}
}
