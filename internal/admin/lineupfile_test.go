// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/store"
)

// uploadTo sends a file as the admin's forms do, through htmx.
func (ta *testAdmin) uploadTo(path, name string, data []byte, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	ta.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("csrf", csrf); err != nil {
		ta.t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", name)
	if err != nil {
		ta.t.Fatal(err)
	}
	_, _ = fw.Write(data)
	if err := mw.Close(); err != nil {
		ta.t.Fatal(err)
	}
	r := httptest.NewRequest("POST", path, &body)
	r.RemoteAddr = "192.0.2.10:40000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	r.AddCookie(cookie)
	rec := httptest.NewRecorder()
	ta.handler.ServeHTTP(rec, r)
	return rec
}

// The Live TV page exports every lineup to one file, and imports the
// lineups of such a file as new ones, as the API does (D112).
func TestLineupFile(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	ctx := context.Background()
	if err := ta.st.RenameLineupGroup(ctx, store.DefaultLineupID, ta.groups(c)["News"].ID, "Headlines"); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.st.AddLineupRule(ctx, store.DefaultLineupID, rules.Rule{Match: rules.Match{Name: "Delta"}, Action: rules.Exclude}); err != nil {
		t.Fatal(err)
	}

	rec := ta.do("GET", "/admin/lineups/export", nil, c)
	file := rec.Body.Bytes()
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != lineupFileType ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="overscan-lineups-`) ||
		!bytes.HasPrefix(file, []byte("# Overscan's lineups")) || !bytes.Contains(file, []byte("name: Headlines")) ||
		!bytes.Contains(file, []byte("action: exclude")) {
		t.Fatalf("export: %d %v\n%s", rec.Code, rec.Header(), file)
	}

	rec = ta.uploadTo("/admin/lineups/import", "lineups.yaml", file, c, csrf)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Added the lineup Default (2) from the file.") {
		t.Fatalf("import: %d %s", rec.Code, rec.Body)
	}
	lineups := ta.lineupsByName(c)
	if got, want := lineups["Default (2)"], lineups["Default"]; got.Shown != want.Shown || got.Rules != 1 || len(got.Accounts) != 0 {
		t.Errorf("imported %+v from %+v", got, want)
	}

	rec = ta.uploadTo("/admin/lineups/import", "notes.yaml", []byte("kind: notes\n"), c, csrf)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "This is not a file of Overscan&#39;s lineups") {
		t.Errorf("a file that is not one: %d %s", rec.Code, rec.Body)
	}
	if rec := ta.uploadTo("/admin/lineups/import", "lineups.yaml", file, c, "wrong"); rec.Code != http.StatusForbidden {
		t.Errorf("an import without the form's token: %d", rec.Code)
	}

	// The API.
	rec = ta.apiDo("GET", "lineups/file", "", c)
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("name: Default (2)")) {
		t.Fatalf("the API's export: %d %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("POST", "lineups/file", string(file), c, h...)
	var body struct{ Lineups []apiLineupImport }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusCreated || len(body.Lineups) != 1 ||
		body.Lineups[0].Name != "Default (3)" || body.Lineups[0].Rules != 1 || body.Lineups[0].LeftOut != (apiLeftOut{}) {
		t.Errorf("the API's import: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "lineups/file", "kind: overscan-lineups\nversion: 9\nlineups: []\n", c, h...),
		http.StatusBadRequest, "bad_file")
	apiErrorOf(t, ta.apiDo("POST", "lineups/file", string(file), c), http.StatusForbidden, "csrf")
}
