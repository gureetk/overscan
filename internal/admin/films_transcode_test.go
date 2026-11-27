// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// What is transcoded of films and series as a whole, a shelf and a title,
// from the pages and the API (D150).
func TestFilmsTranscoding(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLibrary()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	ids := ta.shelfIDs(store.KindMovie)
	drama := ids["Drama"]
	byName := map[string]int64{}
	titles, _, _ := ta.st.AdminTitles(ctx, store.KindMovie, nil, "", 0, 10)
	for _, ti := range titles {
		byName[ti.Name] = ti.ID
	}
	rec := ta.apiDo("POST", "transcode-profiles", `{"name": "small", "max_lines": 720}`, c, h...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("the profile: %d %s", rec.Code, rec.Body)
	}

	// The Transcoding page's card.
	page := ta.do("GET", "/admin/transcoding", nil, c).Body.String()
	mustContain(t, "the Transcoding page", page, `<h2 id="films-title">Films and series</h2>`, `action="/admin/transcoding/films"`,
		"As needed: a film or an episode is transcoded only when the box cannot play its video")
	rec = ta.do("POST", "/admin/transcoding/films", lineupForm(csrf, "", "transcode", "always:small"), c, hx...)
	mustContain(t, "every film", rec.Body.String(), "Films and series are transcoded always, with small, where their shelves and titles say nothing of their own.",
		"Always: every film and episode is transcoded with small (H.264, up to 720p", `<option value="always:small" selected>`, "every film and series")
	if v, _ := ta.st.VODTranscode(ctx); v.Profile != "small" {
		t.Errorf("films and series: %+v", v)
	}
	rec = ta.do("POST", "/admin/transcoding/films", lineupForm(csrf, "", "transcode", "always:source"), c, hx...)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a profile that does not transcode: %d", rec.Code)
	}

	// A shelf, from its head.
	shelf := fmt.Sprintf("/admin/movies/shelves/%d/", drama)
	back := fmt.Sprintf("shelf=%d", drama)
	page = ta.do("GET", "/admin/movies?"+back, nil, c).Body.String()
	mustContain(t, "the shelf's head", page, "Transcoding…", `<option value="as_needed" selected>As films and series say: always, with small</option>`,
		`<option value="always:small">Always,`)
	rec = ta.do("POST", shelf+"transcode", lineupForm(csrf, back, "transcode", "never"), c, hx...)
	mustContain(t, "the shelf never", rec.Body.String(), "The shelf&#39;s titles are transcoded never, unless a title says otherwise.",
		">Transcoded never</summary>")
	if tc, by, _ := ta.st.TitleTranscode(ctx, byName["Quiet Harbour"]); !tc.Never || by != store.TranscodeByShelf {
		t.Errorf("Quiet Harbour: %+v by %s", tc, by)
	}
	if rec = ta.do("POST", shelf+"transcode", lineupForm(csrf, back, "transcode", "always:nobody"), c, hx...); rec.Code != http.StatusBadRequest {
		t.Errorf("no such profile: %d", rec.Code)
	}

	// Titles, ticked.
	form := lineupForm(csrf, back, "action", "transcode", "transcode", "always:small", "ids", fmt.Sprint(byName["The Long Field"]))
	rec = ta.do("POST", "/admin/movies/titles", form, c, hx...)
	mustContain(t, "a title always", rec.Body.String(), "Set 1 film to be transcoded always, with small.", ">Transcoded: small</span>")
	if tc, by, _ := ta.st.TitleTranscode(ctx, byName["The Long Field"]); tc.Profile != "small" || by != store.TranscodeByTitle {
		t.Errorf("The Long Field: %+v by %s", tc, by)
	}
	form = lineupForm(csrf, "", "action", "transcode", "transcode", "never", "ids", fmt.Sprint(byName["Paper Lanterns"]))
	rec = ta.do("POST", "/admin/movies/titles", form, c, hx...)
	mustContain(t, "a title never", rec.Body.String(), "Set 1 film never to be transcoded", ">Not transcoded</span>")
	form = lineupForm(csrf, "", "action", "transcode", "transcode", "as_needed", "ids", fmt.Sprint(byName["Paper Lanterns"]))
	rec = ta.do("POST", "/admin/movies/titles", form, c, hx...)
	mustContain(t, "a title as its shelf", rec.Body.String(), "Set 1 film to be transcoded as their shelf says.")
	if strings.Contains(rec.Body.String(), ">Not transcoded</span>") {
		t.Error("Paper Lanterns still says it is not transcoded")
	}
	for _, bad := range []string{"", "always", "sometimes"} {
		form := lineupForm(csrf, "", "action", "transcode", "transcode", bad, "ids", fmt.Sprint(byName["Paper Lanterns"]))
		if rec := ta.do("POST", "/admin/movies/titles", form, c, hx...); rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d", bad, rec.Code)
		}
	}

	// The API gives them, and changes them.
	var settings apiSettings
	rec = ta.apiDo("GET", "settings", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil || settings.VODTranscode.Mode != "always" || *settings.VODTranscode.Profile != "small" {
		t.Errorf("settings: %d %s", rec.Code, rec.Body)
	}
	var list struct {
		Titles []apiTitle
	}
	rec = ta.apiDo("GET", "library/movies/titles", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("titles: %d %s", rec.Code, rec.Body)
	}
	for _, ti := range list.Titles {
		want := map[string]string{"The Long Field": "always title always", "Quiet Harbour": "never shelf as_needed",
			"Paper Lanterns": "always films_and_series as_needed"}[ti.Name]
		if got := ti.Transcode.Mode + " " + *ti.Transcode.SetBy + " " + ti.TranscodeOwn.Mode; got != want {
			t.Errorf("%s: %s, want %s", ti.Name, got, want)
		}
	}
	var sh apiShelf
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/movies/shelves/%d", drama), `{"transcode": {"mode": "always", "profile": "h264-1080p"}}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &sh); err != nil || rec.Code != http.StatusOK || sh.Transcode.Mode != "always" ||
		*sh.Transcode.Profile != "h264-1080p" {
		t.Errorf("the shelf: %d %s", rec.Code, rec.Body)
	}
	var ti apiTitle
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/titles/%d", byName["Quiet Harbour"]), `{"transcode": {"mode": "never"}}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &ti); err != nil || rec.Code != http.StatusOK || ti.TranscodeOwn.Mode != "never" ||
		*ti.Transcode.SetBy != "title" {
		t.Errorf("Quiet Harbour: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []struct {
		path, body, code string
	}{
		{"settings", `{"vod_transcode": {"mode": "always"}}`, "bad_transcode"},
		{"settings", `{"vod_transcode": "never"}`, "bad_transcode"},
		{fmt.Sprintf("library/movies/shelves/%d", drama), `{"transcode": {"mode": "always", "profile": "nobody"}}`, "bad_transcode"},
		{fmt.Sprintf("library/titles/%d", byName["Quiet Harbour"]), `{"transcode": {"mode": "sometimes"}}`, "bad_transcode"},
	} {
		apiErrorOf(t, ta.apiDo("PATCH", bad.path, bad.body, c, h...), http.StatusBadRequest, bad.code)
	}

	// The profile counts what names it, and follows a rename.
	var p apiTranscodeProfile
	rec = ta.apiDo("GET", "transcode-profiles/small", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || !p.Uses.FilmsAndSeries || p.Uses.Titles != 1 || p.Uses.Shelves != 0 {
		t.Errorf("small's uses: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("DELETE", "transcode-profiles/small", "", c, h...), http.StatusConflict, "profile_in_use")
	if rec := ta.apiDo("PATCH", "transcode-profiles/small", `{"name": "little"}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("renamed: %d %s", rec.Code, rec.Body)
	}
	if tc, _, _ := ta.st.TitleTranscode(ctx, byName["The Long Field"]); tc.Profile != "little" {
		t.Errorf("The Long Field after the rename: %+v", tc)
	}
	if v, _ := ta.st.VODTranscode(ctx); v.Profile != "little" {
		t.Errorf("films and series after the rename: %+v", v)
	}

	// The support bundle counts them.
	lineup, err := ta.bundleLineup(ctx, ta.clock)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "the bundle", lineup, "Transcoding of films and series (D150): all: always; shelves always: 1, never: 0; titles always: 1, never: 1")
}
