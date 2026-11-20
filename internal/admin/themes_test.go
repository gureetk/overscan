// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/theme"
)

// Theme files for the tests, made up.
const (
	evergreenTheme = `{"overscan_theme": 1, "name": "Evergreen", "background": "#0e1512", "text": "#eef4f0", "accent": "#5bd69c",
		"focus": "#f4d35e", "focus_text": "#1a1405", "corners": "small", "brand": "The Den"}`
	paperTheme = `{"overscan_theme": 1, "name": "Paper", "background": "#f4f1ea", "text": "#1d1b17", "accent": "#b5452f",
		"font": "Sans", "corners": "square"}`
)

// upload sends a theme file as the Settings page's form does, through
// htmx; with no name, it sends the form without a file.
func (ta *testAdmin) upload(name string, data []byte, cookie *http.Cookie, csrf string) *httptest.ResponseRecorder {
	ta.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("csrf", csrf); err != nil {
		ta.t.Fatal(err)
	}
	if name != "" {
		fw, err := mw.CreateFormFile("file", name)
		if err != nil {
			ta.t.Fatal(err)
		}
		_, _ = fw.Write(data)
	}
	if err := mw.Close(); err != nil {
		ta.t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/admin/settings/themes", &body)
	r.RemoteAddr = "192.0.2.10:40000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("HX-Request", "true")
	r.Header.Set(csrfHeader, csrf)
	r.AddCookie(cookie)
	rec := httptest.NewRecorder()
	ta.handler.ServeHTTP(rec, r)
	return rec
}

// themesByName returns the installed themes by name.
func (ta *testAdmin) themesByName() map[string]store.Theme {
	ta.t.Helper()
	list, err := ta.st.Themes(context.Background())
	if err != nil {
		ta.t.Fatal(err)
	}
	out := map[string]store.Theme{}
	for _, th := range list {
		out[th.Name] = th
	}
	return out
}

func TestThemesOnTheSettingsPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()

	body := ta.do("GET", "/admin/settings", nil, c).Body.String()
	if !strings.Contains(body, `id="themes"`) || !strings.Contains(body, `<option value="0">Overscan</option>`) || strings.Contains(body, `<ul class="themes">`) {
		t.Fatal("the Themes panel does not show Overscan's own theme alone")
	}

	rec := ta.upload("evergreen.json", []byte(evergreenTheme), c, csrf)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Installed the theme Evergreen.") {
		t.Fatalf("install: status %d", rec.Code)
	}
	if !strings.Contains(body, "<strong>Evergreen</strong>") || !strings.Contains(body, "Not chosen · shows “The Den” beside the mark") ||
		!strings.Contains(body, `fill="#0e1512"`) || !strings.Contains(body, `fill="#f4d35e"`) {
		t.Error("the list does not show the theme, its brand and its colours")
	}
	evergreen := ta.themesByName()["Evergreen"]
	if _, err := theme.Parse(evergreen.Data); err != nil || !bytes.Contains(evergreen.Data, []byte(`"overscan_theme": 1`)) {
		t.Errorf("kept %s: %v", evergreen.Data, err)
	}

	// Mistakes say what is wrong, and install nothing.
	for _, bad := range []struct {
		data   string
		status int
		says   string
	}{
		{`{"overscan_theme": 1, "name": "Murk", "background": "#222222", "text": "#333333"}`, http.StatusBadRequest, "hard to read"},
		{`{"name": "No format", "background": "#000000", "text": "#ffffff"}`, http.StatusBadRequest, "it is not a theme file"},
		{`{"overscan_theme": 1, "name": "Overscan", "background": "#000000", "text": "#ffffff"}`, http.StatusBadRequest, "the name of Overscan&#39;s own theme"},
		{`{"overscan_theme": 1, "name": "Script", "background": "#000000", "text": "#ffffff", "css": "body{}"}`, http.StatusBadRequest, "A theme has no &#34;css&#34;"},
		{`{"overscan_theme": 1, "name": "Long", "background": "#000000", "text": "#ffffff"}` + strings.Repeat(" ", theme.MaxFileBytes), http.StatusBadRequest, "larger than 16 KB"},
		{strings.Repeat("x", maxFormBytes+1), http.StatusRequestEntityTooLarge, "too large to be a theme"},
	} {
		rec = ta.upload("bad.json", []byte(bad.data), c, csrf)
		if rec.Code != bad.status || !strings.Contains(rec.Body.String(), bad.says) {
			t.Errorf("%.40s: status %d, %q not said", bad.data, rec.Code, bad.says)
		}
	}
	if rec = ta.upload("", nil, c, csrf); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Choose a theme file.") {
		t.Errorf("no file: status %d", rec.Code)
	}
	if n := len(ta.themesByName()); n != 1 {
		t.Errorf("%d themes after the mistakes", n)
	}

	// A name is text, never markup.
	rec = ta.upload("odd.json", []byte(`{"overscan_theme": 1, "name": "<b>Bold</b>", "background": "#000000", "text": "#ffffff"}`), c, csrf)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<b>Bold</b>") || !strings.Contains(rec.Body.String(), "&lt;b&gt;Bold&lt;/b&gt;") {
		t.Errorf("a name with markup: status %d", rec.Code)
	}

	// The same name, whatever its case, replaces the theme and keeps its ID.
	if rec = ta.upload("paper.json", []byte(paperTheme), c, csrf); rec.Code != http.StatusOK {
		t.Fatalf("install Paper: status %d", rec.Code)
	}
	again := strings.Replace(strings.Replace(evergreenTheme, "#5bd69c", "#7ce0b0", 1), `"Evergreen"`, `"EVERGREEN"`, 1)
	if rec = ta.upload("evergreen.json", []byte(again), c, csrf); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Replaced the theme EVERGREEN.") {
		t.Errorf("replace: status %d", rec.Code)
	}
	themes := ta.themesByName()
	if len(themes) != 3 || themes["EVERGREEN"].ID != evergreen.ID || !bytes.Contains(themes["EVERGREEN"].Data, []byte("#7ce0b0")) {
		t.Fatalf("after replacing: %+v", themes)
	}
	paper := themes["Paper"]

	// The household's theme.
	choose := func(form url.Values) *httptest.ResponseRecorder {
		form.Set("csrf", csrf)
		return ta.do("POST", "/admin/settings/themes/choose", form, c, hx...)
	}
	rec = choose(url.Values{"theme": {fmt.Sprint(evergreen.ID)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Chose the household&#39;s theme.") ||
		!strings.Contains(rec.Body.String(), fmt.Sprintf(`<option value="%d" selected>EVERGREEN</option>`, evergreen.ID)) ||
		!strings.Contains(rec.Body.String(), "Chosen for the household") {
		t.Errorf("choose the household's: status %d", rec.Code)
	}
	if acct, err := ta.st.Account(ctx, householdAccount); err != nil || acct.ThemeID != evergreen.ID {
		t.Errorf("the household's theme %d, %v", acct.ThemeID, err)
	}

	// A profile's: its selects show once there are two profiles.
	if strings.Contains(rec.Body.String(), "Profile: ") {
		t.Error("with one profile, the panel offers a theme for it")
	}
	guest, err := ta.st.AddProfile(ctx, householdAccount, "Guest")
	if err != nil {
		t.Fatal(err)
	}
	rec = choose(url.Values{"theme": {fmt.Sprint(paper.ID)}, "profile": {fmt.Sprint(guest.ID)}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Chose the profile&#39;s theme.") ||
		!strings.Contains(rec.Body.String(), `<label for="profile-theme-`+fmt.Sprint(guest.ID)+`">Profile: Guest</label>`) ||
		!strings.Contains(rec.Body.String(), "Chosen for Guest") {
		t.Errorf("choose a profile's: status %d", rec.Code)
	}
	if p := ta.profilesOf()["Guest"]; p.ThemeID != paper.ID {
		t.Errorf("Guest's theme %d", p.ThemeID)
	}
	for _, bad := range []struct {
		form   url.Values
		status int
	}{
		{url.Values{"theme": {"x"}}, http.StatusBadRequest},
		{url.Values{"theme": {"999"}}, http.StatusNotFound},
		{url.Values{"theme": {"999"}, "profile": {fmt.Sprint(guest.ID)}}, http.StatusNotFound},
		{url.Values{"theme": {fmt.Sprint(paper.ID)}, "profile": {"999"}}, http.StatusNotFound},
		{url.Values{"theme": {fmt.Sprint(paper.ID)}, "profile": {"x"}}, http.StatusBadRequest},
	} {
		if rec = choose(bad.form); rec.Code != bad.status {
			t.Errorf("%v: status %d, want %d", bad.form, rec.Code, bad.status)
		}
	}

	// Downloading a theme gives its file back.
	rec = ta.do("GET", fmt.Sprintf("/admin/themes/%d.json", evergreen.ID), nil, c)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Disposition") != `attachment; filename="evergreen.json"` {
		t.Errorf("download: status %d, %q", rec.Code, rec.Header().Get("Content-Disposition"))
	}
	if got, err := theme.Parse(rec.Body.Bytes()); err != nil || got.Name != "EVERGREEN" || got.Accent != "#7ce0b0" {
		t.Errorf("downloaded %+v, %v", got, err)
	}
	for _, path := range []string{"/admin/themes/999.json", "/admin/themes/x.json", fmt.Sprintf("/admin/themes/%d", evergreen.ID)} {
		if rec = ta.do("GET", path, nil, c); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}

	// Removing a theme gives what had it the household's, or Overscan's own.
	rec = ta.do("POST", fmt.Sprintf("/admin/settings/themes/%d/remove", evergreen.ID), url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed the theme EVERGREEN.") {
		t.Errorf("remove: status %d", rec.Code)
	}
	if acct, err := ta.st.Account(ctx, householdAccount); err != nil || acct.ThemeID != 0 {
		t.Errorf("the household's theme after removing it: %d, %v", acct.ThemeID, err)
	}
	if rec = ta.do("POST", fmt.Sprintf("/admin/settings/themes/%d/remove", evergreen.ID), url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("remove again: status %d", rec.Code)
	}
	if rec = ta.do("POST", fmt.Sprintf("/admin/settings/themes/%d/remove", paper.ID), url.Values{}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}
}

func TestThemesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	h := []string{csrfHeader, csrf}
	ctx := context.Background()

	type themesBody struct {
		Household int64      `json:"household"`
		Themes    []apiTheme `json:"themes"`
	}
	var list themesBody
	if rec := ta.apiDo("GET", "themes", "", c); json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.Household != 0 || list.Themes == nil || len(list.Themes) != 0 {
		t.Fatalf("at first: %s", rec.Body)
	}

	apiErrorOf(t, ta.apiDo("POST", "themes", evergreenTheme, c, append(h, "Content-Type", "text/plain")...), http.StatusUnsupportedMediaType, "not_json")
	apiErrorOf(t, ta.apiDo("POST", "themes", `{"overscan_theme": 1, "name": "Dim", "background": "#000000", "text": "#111111"}`, c, h...), http.StatusBadRequest, "bad_theme")
	apiErrorOf(t, ta.apiDo("POST", "themes", evergreenTheme, c), http.StatusForbidden, "csrf")

	var th apiTheme
	rec := ta.apiDo("POST", "themes", evergreenTheme, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &th); err != nil || rec.Code != http.StatusCreated || th.Name != "Evergreen" || len(th.Uses) != 0 {
		t.Fatalf("install: status %d, %s", rec.Code, rec.Body)
	}
	if got, err := theme.Parse([]byte(th.File)); err != nil || got.Brand != "The Den" {
		t.Errorf("the file: %+v, %v", got, err)
	}
	if rec = ta.apiDo("POST", "themes", evergreenTheme, c, h...); rec.Code != http.StatusOK {
		t.Errorf("install again: status %d", rec.Code)
	}

	// The household's theme.
	rec = ta.apiDo("PUT", "theme", fmt.Sprintf(`{"theme_id": %d}`, th.ID), c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || list.Household != th.ID ||
		len(list.Themes) != 1 || len(list.Themes[0].Uses) != 1 {
		t.Errorf("choose: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PUT", "theme", `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PUT", "theme", `{"theme_id": -1}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PUT", "theme", `{"theme_id": 999}`, c, h...), http.StatusNotFound, "no_such_theme")

	// A profile's.
	guest, err := ta.st.AddProfile(ctx, householdAccount, "Guest")
	if err != nil {
		t.Fatal(err)
	}
	var p apiProfile
	rec = ta.apiDo("PATCH", fmt.Sprintf("profiles/%d", guest.ID), fmt.Sprintf(`{"theme_id": %d}`, th.ID), c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusOK || p.ThemeID != th.ID {
		t.Errorf("a profile's: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("profiles/%d", guest.ID), `{"theme_id": 999}`, c, h...), http.StatusNotFound, "no_such_theme")
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("profiles/%d", guest.ID), `{"theme_id": -2}`, c, h...), http.StatusBadRequest, "no_such_theme")

	// Removing it.
	if rec = ta.apiDo("DELETE", fmt.Sprintf("themes/%d", th.ID), "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("remove: status %d", rec.Code)
	}
	if rec = ta.apiDo("GET", "themes", "", c); json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.Household != 0 || len(list.Themes) != 0 {
		t.Errorf("after removing: %s", rec.Body)
	}
	if g := ta.profilesOf()["Guest"]; g.ThemeID != 0 {
		t.Errorf("Guest's theme after removing it: %d", g.ThemeID)
	}
	apiErrorOf(t, ta.apiDo("DELETE", fmt.Sprintf("themes/%d", th.ID), "", c, h...), http.StatusNotFound, "no_such_theme")
	apiErrorOf(t, ta.apiDo("DELETE", "themes/x", "", c, h...), http.StatusNotFound, "no_such_theme")
}
