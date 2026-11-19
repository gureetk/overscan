// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// apiDo sends a JSON request to the API, with the session's cookie if there
// is one.
func (ta *testAdmin) apiDo(method, path, body string, cookie *http.Cookie, header ...string) *httptest.ResponseRecorder {
	ta.t.Helper()
	r := httptest.NewRequest(method, APIPrefix+path, strings.NewReader(body))
	r.RemoteAddr = "192.0.2.10:40000"
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
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

// apiErrorOf decodes an error answer and checks its status and code.
func apiErrorOf(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var body apiErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("status %d, body %q: %v", rec.Code, rec.Body, err)
	}
	if rec.Code != status || body.Error != code || body.Message == "" {
		t.Errorf("status %d, %+v; want %d and %q", rec.Code, body, status, code)
	}
	if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("Content-Type %q", rec.Header().Get("Content-Type"))
	}
}

func TestAPISignIn(t *testing.T) {
	ta := newTestAdmin(t)
	creds := `{"username": "admin", "password": "correct horse"}`

	apiErrorOf(t, ta.apiDo("POST", "session", creds, nil), http.StatusForbidden, "no_administrator")
	ta.setPassword("correct horse")

	apiErrorOf(t, ta.apiDo("POST", "session", `{"username": "admin", "password": "wrong horse"}`, nil), http.StatusUnauthorized, "wrong_credentials")
	apiErrorOf(t, ta.apiDo("POST", "session", `{"username": "admin", "password": "correct horse", "extra": 1}`, nil), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("POST", "session", creds+creds, nil), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("POST", "session", `{"username": "`+strings.Repeat("a", maxJSONBytes)+`"}`, nil), http.StatusRequestEntityTooLarge, "too_large")
	rec := ta.apiDo("POST", "session", "", nil)
	apiErrorOf(t, rec, http.StatusUnsupportedMediaType, "not_json")
	rec = ta.apiDo("POST", "session", creds, nil, "Content-Type", "text/plain")
	apiErrorOf(t, rec, http.StatusUnsupportedMediaType, "not_json")

	rec = ta.apiDo("POST", "session", creds, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var s apiSessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	c := sessionCookieOf(t, rec)
	if s.Username != "admin" || s.CSRFToken == "" || !s.CreatedAt.Equal(ta.clock) || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Errorf("session %+v, cookie %+v", s, c)
	}

	// The session works for the pages too.
	if rec = ta.do("GET", "/admin/", nil, c); rec.Code != http.StatusOK {
		t.Errorf("a page with the API's session: status %d", rec.Code)
	}
}

func TestAPISessionAndSignOut(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	apiErrorOf(t, ta.apiDo("GET", "session", "", nil), http.StatusUnauthorized, "unauthenticated")

	c, csrf := ta.signIn("correct horse")
	rec := ta.apiDo("GET", "session", "", c)
	var s apiSessionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil || rec.Code != http.StatusOK || s.CSRFToken != csrf {
		t.Fatalf("status %d, %+v, %v", rec.Code, s, err)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control %q", rec.Header().Get("Cache-Control"))
	}

	apiErrorOf(t, ta.apiDo("DELETE", "session", "", c), http.StatusForbidden, "csrf")
	apiErrorOf(t, ta.apiDo("DELETE", "session", "", c, csrfHeader, "wrong"), http.StatusForbidden, "csrf")
	// A form field is not enough for the API.
	r := httptest.NewRequest("DELETE", APIPrefix+"session", strings.NewReader("csrf="+csrf))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(c)
	rec = httptest.NewRecorder()
	ta.handler.ServeHTTP(rec, r)
	apiErrorOf(t, rec, http.StatusForbidden, "csrf")

	rec = ta.apiDo("DELETE", "session", "", c, csrfHeader, csrf)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("sign-out: status %d, %q", rec.Code, rec.Body)
	}
	if cleared := sessionCookieOf(t, rec); cleared.MaxAge >= 0 {
		t.Errorf("the cookie was not cleared: %+v", cleared)
	}
	apiErrorOf(t, ta.apiDo("GET", "session", "", c), http.StatusUnauthorized, "unauthenticated")
}

func TestAPIServer(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, _ := ta.signIn("correct horse")
	rec := ta.apiDo("GET", "server", "", c)
	var got struct {
		Version       string `json:"version"`
		SchemaVersion int    `json:"schema_version"`
		StartedAt     string `json:"started_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, %s", rec.Code, rec.Body)
	}
	if got.Version != "test" || got.SchemaVersion < 7 || got.StartedAt == "" {
		t.Errorf("got %+v", got)
	}
}

func TestAPIUnknownPathsAndMethods(t *testing.T) {
	ta := newTestAdmin(t)
	apiErrorOf(t, ta.apiDo("GET", "nope", "", nil), http.StatusNotFound, "not_found")
	rec := ta.apiDo("PUT", "session", "{}", nil)
	apiErrorOf(t, rec, http.StatusMethodNotAllowed, "method_not_allowed")
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") || !strings.Contains(allow, "DELETE") {
		t.Errorf("Allow %q", allow)
	}
}

func TestAPIFailedSignInsAreSlowedDown(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	for range maxFailures {
		ta.apiDo("POST", "session", `{"username": "admin", "password": "wrong horse"}`, nil)
	}
	rec := ta.apiDo("POST", "session", `{"username": "admin", "password": "correct horse"}`, nil)
	apiErrorOf(t, rec, http.StatusTooManyRequests, "too_many_failures")
	if rec.Header().Get("Retry-After") != "900" {
		t.Errorf("Retry-After %q", rec.Header().Get("Retry-After"))
	}
}

func TestAPICrossOriginWritesAreRefused(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	rec := ta.apiDo("DELETE", "session", "", c, csrfHeader, csrf, "Sec-Fetch-Site", "cross-site")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status %d", rec.Code)
	}
}

// TestOpenAPIDescribesTheRoutes checks docs/api/admin.yaml against the API:
// the same operations, those that need no session marked so, those that
// change something needing the CSRF token, and every reference resolving.
func TestOpenAPIDescribesTheRoutes(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "api", "admin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi %v", doc["openapi"])
	}
	if servers, _ := doc["servers"].([]any); len(servers) != 1 || servers[0].(map[string]any)["url"] != strings.TrimSuffix(APIPrefix, "/") {
		t.Errorf("servers %v, want %s", doc["servers"], APIPrefix)
	}

	type op struct{ method, path string }
	routes := newTestAdmin(t).apiRoutes()
	var documented []op
	paths, _ := doc["paths"].(map[string]any)
	for path, item := range paths {
		for method, v := range item.(map[string]any) {
			if !slices.Contains([]string{"get", "put", "post", "delete", "options", "head", "patch", "trace"}, method) {
				continue // parameters and the like, shared by the path's operations
			}
			o := v.(map[string]any)
			documented = append(documented, op{strings.ToUpper(method), path})
			security, hasSecurity := o["security"].([]any)
			var schemes []string
			for _, req := range security {
				for name := range req.(map[string]any) {
					schemes = append(schemes, name)
				}
			}
			for _, rt := range routes {
				if rt.method != strings.ToUpper(method) || "/"+rt.path != path {
					continue
				}
				switch {
				case rt.open && (!hasSecurity || len(security) != 0):
					t.Errorf("%s %s needs no session, but its security is %v", rt.method, path, o["security"])
				case !rt.open && !safeMethod(rt.method) && !slices.Contains(schemes, "csrf"):
					t.Errorf("%s %s changes something, but its security does not name csrf: %v", rt.method, path, o["security"])
				}
			}
		}
	}
	var served []op
	for _, rt := range routes {
		served = append(served, op{rt.method, "/" + rt.path})
	}
	less := func(s []op) func(i, j int) bool {
		return func(i, j int) bool { return s[i].path+s[i].method < s[j].path+s[j].method }
	}
	sort.Slice(documented, less(documented))
	sort.Slice(served, less(served))
	if !slices.Equal(documented, served) {
		t.Errorf("documented %v\nserved     %v", documented, served)
	}

	// Every $ref points at something in the document.
	var walk func(v any)
	walk = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			for k, x := range v {
				if k == "$ref" {
					ref, _ := x.(string)
					if !resolves(doc, ref) {
						t.Errorf("$ref %q resolves to nothing", ref)
					}
				}
				walk(x)
			}
		case []any:
			for _, x := range v {
				walk(x)
			}
		}
	}
	walk(doc)
}

// resolves reports whether a local reference, such as
// "#/components/schemas/Error", names a part of doc.
func resolves(doc map[string]any, ref string) bool {
	path, ok := strings.CutPrefix(ref, "#/")
	if !ok {
		return false
	}
	var cur any = doc
	for _, part := range strings.Split(path, "/") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[part]; !ok {
			return false
		}
	}
	return true
}
