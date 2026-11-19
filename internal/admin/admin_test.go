// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/store"
)

// testAdmin is an admin with a fake clock, cheap hashes and a store in a
// temporary directory.
type testAdmin struct {
	*Admin
	t       *testing.T
	st      *store.Store
	clock   time.Time
	handler http.Handler
}

func newTestAdmin(t *testing.T) *testAdmin {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), log)
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	ta := &testAdmin{t: t, st: st, clock: time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)}
	ta.Admin = New(st, log, "test")
	ta.params = cheap
	ta.now = func() time.Time { return ta.clock }
	ta.handler = ta.Handler()
	return ta
}

// setPassword makes the administrator, with the username "admin".
func (ta *testAdmin) setPassword(password string) {
	ta.t.Helper()
	hash, err := HashPassword(context.Background(), password, cheap)
	if err != nil {
		ta.t.Fatal(err)
	}
	if err := ta.st.SetAdminPassword(context.Background(), "admin", hash, ta.clock, 0); err != nil {
		ta.t.Fatal(err)
	}
}

// do sends a request, with the session's cookie if there is one, and a form
// if form is not nil.
func (ta *testAdmin) do(method, target string, form url.Values, cookie *http.Cookie, header ...string) *httptest.ResponseRecorder {
	ta.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "192.0.2.10:40000"
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		r.AddCookie(cookie)
	}
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	ta.handler.ServeHTTP(rec, r)
	return rec
}

// signIn signs in and returns the session's cookie and CSRF token.
func (ta *testAdmin) signIn(password string) (*http.Cookie, string) {
	ta.t.Helper()
	rec := ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {password}}, nil)
	if rec.Code != http.StatusSeeOther {
		ta.t.Fatalf("sign-in: status %d: %s", rec.Code, rec.Body)
	}
	c := sessionCookieOf(ta.t, rec)
	s, err := ta.st.AdminSession(context.Background(), store.HashSecret(c.Value))
	if err != nil {
		ta.t.Fatal(err)
	}
	return c, s.CSRFToken
}

func sessionCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			return c
		}
	}
	t.Fatalf("no %s cookie in %v", CookieName, rec.Header()["Set-Cookie"])
	return nil
}

func TestSignInWithoutAPassword(t *testing.T) {
	ta := newTestAdmin(t)
	rec := ta.do("GET", "/admin/sign-in", nil, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "overscan admin password") {
		t.Errorf("status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"anything at all"}}, nil)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "No password is set yet") {
		t.Errorf("sign-in: status %d, %s", rec.Code, rec.Body)
	}
}

func TestSignIn(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")

	rec := ta.do("GET", "/admin/", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sign-in" {
		t.Fatalf("signed out: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}

	rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"wrong horse"}}, nil)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "The username or password is wrong.") {
		t.Errorf("wrong password: status %d", rec.Code)
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a wrong password set a cookie")
	}
	rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"someone"}, "password": {"correct horse"}}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong username: status %d", rec.Code)
	}

	rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}}, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Fatalf("right password: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookieOf(t, rec)
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != int(sessionMax.Seconds()) || len(c.Value) < 40 {
		t.Errorf("cookie %+v", c)
	}

	rec = ta.do("GET", "/admin/", nil, c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<a href="/admin/settings#account" title="Your account">admin</a>`) {
		t.Errorf("signed in: status %d, %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("a page may be cached: %q", rec.Header().Get("Cache-Control"))
	}

	// The sign-in page sends a signed-in administrator on.
	if rec = ta.do("GET", "/admin/sign-in", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("sign-in page when signed in: status %d", rec.Code)
	}
	// A cookie that names no session does not sign in.
	forged := &http.Cookie{Name: CookieName, Value: strings.Repeat("A", 43)}
	if rec = ta.do("GET", "/admin/", nil, forged); rec.Code != http.StatusSeeOther {
		t.Errorf("a forged cookie: status %d", rec.Code)
	}
}

func TestSignInGoesBackToThePageAskedFor(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")

	rec := ta.do("GET", "/admin/password", nil, nil)
	if loc := rec.Header().Get("Location"); loc != "/admin/sign-in?next=%2Fadmin%2Fpassword" {
		t.Errorf("location %q", loc)
	}
	for next, want := range map[string]string{
		"/admin/password":           "/admin/password",
		"/admin/password?x=1":       "/admin/password?x=1",
		"":                          "/admin/",
		"//evil.example/admin/":     "/admin/",
		"https://evil.example/":     "/admin/",
		"/c/":                       "/admin/",
		"/admin/sign-in":            "/admin/",
		"/admin/../c/":              "/admin/",
		"/admin\\..\\c/":            "/admin/",
		"javascript:alert(1)":       "/admin/",
		"/admin/x\r\nSet-Cookie: a": "/admin/",
	} {
		rec := ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}, "next": {next}}, nil)
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("next %q: went to %q, want %q", next, got, want)
		}
	}
}

func TestFailedSignInsAreSlowedDown(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	for range maxFailures {
		ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"wrong horse"}}, nil)
	}
	rec := ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}}, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "900" ||
		!strings.Contains(rec.Body.String(), "Try again in 15 minutes.") {
		t.Errorf("status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	ta.clock = ta.clock.Add(failWindow + time.Second)
	if rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}}, nil); rec.Code != http.StatusSeeOther {
		t.Errorf("after the window: status %d", rec.Code)
	}
}

func TestSignOut(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")

	rec := ta.do("POST", "/admin/sign-out", url.Values{}, c)
	if rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/sign-out", url.Values{"csrf": {"not-the-token"}}, c)
	if rec.Code != http.StatusForbidden {
		t.Errorf("with a wrong CSRF token: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/sign-out", url.Values{"csrf": {csrf}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sign-in" {
		t.Errorf("sign-out: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if cleared := sessionCookieOf(t, rec); cleared.MaxAge >= 0 || cleared.Value != "" {
		t.Errorf("the cookie was not cleared: %+v", cleared)
	}
	if rec = ta.do("GET", "/admin/", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("the session outlived signing out: status %d", rec.Code)
	}
}

func TestCSRFTokenInAHeader(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	rec := ta.do("POST", "/admin/sign-out", nil, c, csrfHeader, csrf)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status %d", rec.Code)
	}
}

func TestSessionsExpire(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")

	c, _ := ta.signIn("correct horse")
	ta.clock = ta.clock.Add(sessionIdle + time.Minute)
	if rec := ta.do("GET", "/admin/", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("an idle session still works: status %d", rec.Code)
	}

	// A session used every six days still ends a month after signing in.
	c, _ = ta.signIn("correct horse")
	signedIn := ta.clock
	for ta.clock.Sub(signedIn) < sessionMax-6*24*time.Hour {
		ta.clock = ta.clock.Add(6 * 24 * time.Hour)
		if rec := ta.do("GET", "/admin/", nil, c); rec.Code != http.StatusOK {
			t.Fatalf("a session in use, %v after signing in: status %d", ta.clock.Sub(signedIn), rec.Code)
		}
	}
	ta.clock = signedIn.Add(sessionMax + time.Minute)
	if rec := ta.do("GET", "/admin/", nil, c); rec.Code != http.StatusSeeOther {
		t.Errorf("a session older than %v still works: status %d", sessionMax, rec.Code)
	}
}

func TestChangePassword(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	other, _ := ta.signIn("correct horse")

	if rec := ta.do("GET", "/admin/password", nil, c); rec.Code != http.StatusOK {
		t.Fatalf("form: status %d", rec.Code)
	}
	for name, tc := range map[string]struct {
		form url.Values
		want string
	}{
		"wrong current": {url.Values{"current": {"wrong horse"}, "new": {"battery staple"}, "again": {"battery staple"}}, "The current password is wrong."},
		"too short":     {url.Values{"current": {"correct horse"}, "new": {"short"}, "again": {"short"}}, "The password needs at least 8 characters."},
		"different":     {url.Values{"current": {"correct horse"}, "new": {"battery staple"}, "again": {"battery stapler"}}, "The two new passwords differ."},
	} {
		tc.form.Set("csrf", csrf)
		rec := ta.do("POST", "/admin/password", tc.form, c)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("%s: status %d, body lacks %q", name, rec.Code, tc.want)
		}
	}

	rec := ta.do("POST", "/admin/password", url.Values{"csrf": {csrf}, "current": {"correct horse"}, "new": {"battery staple"}, "again": {"battery staple"}}, c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The password is changed.") {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if rec = ta.do("GET", "/admin/", nil, c); rec.Code != http.StatusOK {
		t.Errorf("the session that changed the password ended: status %d", rec.Code)
	}
	if rec = ta.do("GET", "/admin/", nil, other); rec.Code != http.StatusSeeOther {
		t.Errorf("another session survived: status %d", rec.Code)
	}
	u, _ := ta.st.AdminUser(context.Background())
	if ok, _ := CheckPassword(context.Background(), u.PasswordHash, "battery staple"); !ok {
		t.Error("the new password does not match the stored hash")
	}
}

func TestCrossOriginRequestsAreRefused(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	rec := ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}}, nil,
		"Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("a cross-site sign-in: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}}, nil,
		"Origin", "http://evil.example")
	if rec.Code != http.StatusForbidden {
		t.Errorf("a sign-in from another origin: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"correct horse"}}, nil,
		"Sec-Fetch-Site", "same-origin")
	if rec.Code != http.StatusSeeOther {
		t.Errorf("a same-origin sign-in: status %d", rec.Code)
	}
}

func TestSecurityHeaders(t *testing.T) {
	ta := newTestAdmin(t)
	rec := ta.do("GET", "/admin/sign-in", nil, nil)
	for k, want := range map[string]string{
		"X-Frame-Options":        "DENY",
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "same-origin",
	} {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("%s: %q, want %q", k, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("Content-Security-Policy %q", csp)
	}
}

func TestStaticFiles(t *testing.T) {
	ta := newTestAdmin(t)
	path := ta.assetPath("admin.css")
	rec := ta.do("GET", path, nil, nil)
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/css") ||
		!strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("%s: status %d, %q, %q", path, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"))
	}
	if rec = ta.do("GET", "/admin/static/admin.css?v=old", nil, nil); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("an old version may be cached: %q", rec.Header().Get("Cache-Control"))
	}
	for _, p := range []string{"/admin/static/nope.css", "/admin/static/", "/admin/templates/layout.html"} {
		if rec = ta.do("GET", p, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", p, rec.Code)
		}
	}
	// A path that climbs out of the static files is sent to its clean form,
	// which is not a static file.
	rec = ta.do("GET", "/admin/static/../templates/layout.html", nil, nil)
	if loc := rec.Header().Get("Location"); rec.Code == http.StatusOK || (loc != "" && loc != "/admin/templates/layout.html") {
		t.Errorf("climbing out: status %d, location %q", rec.Code, loc)
	}
	if rec = ta.do("GET", "/admin/static/icon.svg", nil, nil); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" {
		t.Errorf("icon: status %d, %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestUnknownPages(t *testing.T) {
	ta := newTestAdmin(t)
	if rec := ta.do("GET", "/admin/nope", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}
	if rec := ta.do("DELETE", "/admin/", nil, nil); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE /admin/: status %d", rec.Code)
	}
}

func TestHTMXRequestsAreSentToSignIn(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	// The browser comes back to the page it was on, not to the part of it
	// that htmx asked for.
	for current, want := range map[string]string{
		"http://example.com/admin/devices":        "/admin/sign-in?next=%2Fadmin%2Fdevices",
		"http://example.com/admin/lineup?group=3": "/admin/sign-in?next=%2Fadmin%2Flineup%3Fgroup%3D3",
		"http://example.com/admin/":               "/admin/sign-in",
		"http://elsewhere.example/admin/devices":  "/admin/sign-in",
		"":                                        "/admin/sign-in",
	} {
		rec := ta.do("GET", "/admin/devices/waiting", nil, nil, "HX-Request", "true", "HX-Current-URL", current)
		if rec.Code != http.StatusUnauthorized || rec.Header().Get("HX-Redirect") != want {
			t.Errorf("from %q: status %d, HX-Redirect %q, want %q", current, rec.Code, rec.Header().Get("HX-Redirect"), want)
		}
	}
}

func TestSignInLogNamesNoPassword(t *testing.T) {
	ta := newTestAdmin(t)
	var buf strings.Builder
	ta.log = slog.New(slog.NewTextHandler(&buf, nil))
	ta.handler = ta.Handler()
	ta.setPassword("correct horse")
	ta.do("POST", "/admin/sign-in", url.Values{"username": {"admin"}, "password": {"wrong horse"}}, nil)
	ta.signIn("correct horse")
	out := buf.String()
	if !strings.Contains(out, "admin sign-in failed") || !strings.Contains(out, "admin signed in") {
		t.Errorf("log %q", out)
	}
	if strings.Contains(out, "horse") {
		t.Errorf("the log holds a password: %q", out)
	}
}

func TestNoAdministratorMeansNoSession(t *testing.T) {
	ta := newTestAdmin(t)
	_, err := ta.st.CreateAdminSession(context.Background(), "x", "y", "", "", ta.clock)
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("got %v", err)
	}
}
