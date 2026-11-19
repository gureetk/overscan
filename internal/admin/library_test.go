// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// withLibrary gives the test admin the lineup of withLineup, and films and
// series on an Xtream source, films, in Drama and Comedy, and signs in.
func (ta *testAdmin) withLibrary() (*http.Cookie, []string) {
	ta.t.Helper()
	c, h := ta.withLineup()
	ctx := context.Background()
	src, err := ta.st.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "fictional"})
	if err != nil {
		ta.t.Fatal(err)
	}
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)
	if _, err := ta.st.ApplyLibrary(ctx, src, store.KindMovie, []store.LibraryCategory{{Key: "1", Name: "Drama"}, {Key: "2", Name: "Comedy"}},
		[]store.LibraryTitle{
			{Key: "11", Category: "1", Name: "The Long Field", Year: 2024},
			{Key: "12", Category: "1", Name: "Quiet Harbour"},
			{Key: "13", Category: "2", Name: "Paper Lanterns", Year: 2019},
		}, at); err != nil {
		ta.t.Fatal(err)
	}
	if _, err := ta.st.ApplyLibrary(ctx, src, store.KindSeries, []store.LibraryCategory{{Key: "5", Name: "Nature"}},
		[]store.LibraryTitle{{Key: "51", Category: "5", Name: "Coastline"}}, at); err != nil {
		ta.t.Fatal(err)
	}
	return c, h
}

// shelfIDs returns the films' shelves by their sources' names.
func (ta *testAdmin) shelfIDs(kind string) map[string]int64 {
	ta.t.Helper()
	shelves, err := ta.st.LibraryShelves(context.Background(), kind)
	if err != nil {
		ta.t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sh := range shelves {
		out[sh.Own] = sh.ID
	}
	return out
}

// TestLibraryPages hides, renames and moves shelves, and hides titles, from
// the Movies page, and shows the Series page (D79).
func TestLibraryPages(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLibrary()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ids := ta.shelfIDs(store.KindMovie)
	drama, comedy := ids["Drama"], ids["Comedy"]

	page := ta.do("GET", "/admin/movies", nil, c).Body.String()
	mustContain(t, "the Movies page", page, "<h1>Movies</h1>", `<a href="/admin/movies" aria-current="page" title="Movies">`,
		`<a href="/admin/lineup" title="Live TV">`, "<strong>3</strong> of 3 films shown", ">All films<",
		fmt.Sprintf(`<a href="/admin/movies?shelf=%d" data-name="Drama">`, drama), "The Long Field", "Paper Lanterns")

	shelf := fmt.Sprintf("/admin/movies/shelves/%d/", drama)
	back := url.Values{"shelf": {fmt.Sprint(drama)}}.Encode()
	rec := ta.do("POST", shelf+"hide", lineupForm(csrf, back), c, hx...)
	mustContain(t, "hidden", rec.Body.String(), "Hid Drama: devices no longer see it, nor anything on it.",
		"<strong>1</strong> of 3 films shown", `<span class="badge">`, "Show the shelf")
	if shelves, _ := ta.st.Shelves(context.Background(), store.KindMovie); len(shelves) != 1 || shelves[0].Name != "Comedy" {
		t.Errorf("devices' shelves %+v", shelves)
	}
	rec = ta.do("POST", shelf+"rename", lineupForm(csrf, back, "name", "Serious Films"), c, hx...)
	mustContain(t, "renamed", rec.Body.String(), "Renamed the shelf Serious Films.", "Its sources call it Drama.")
	rec = ta.do("POST", shelf+"show", lineupForm(csrf, back), c, hx...)
	mustContain(t, "shown", rec.Body.String(), "Showed Serious Films.")
	rec = ta.do("POST", fmt.Sprintf("/admin/movies/shelves/%d/up", comedy), lineupForm(csrf, ""), c, hx...)
	mustContain(t, "moved", rec.Body.String(), "Moved Comedy up.", "Sources' order")
	if shelves, _ := ta.st.Shelves(context.Background(), store.KindMovie); len(shelves) != 2 || shelves[0].Name != "Comedy" ||
		shelves[1].Name != "Serious Films" {
		t.Errorf("devices' shelves %+v", shelves)
	}
	rec = ta.do("POST", "/admin/movies/order/reset", lineupForm(csrf, ""), c, hx...)
	mustContain(t, "reset", rec.Body.String(), "The shelves are in their sources&#39; order again.")
	rec = ta.do("POST", shelf+"rename", lineupForm(csrf, back, "name", ""), c, hx...)
	mustContain(t, "renamed back", rec.Body.String(), "The shelf has its sources&#39; name again: Drama.")
	rec = ta.do("POST", shelf+"rename", lineupForm(csrf, back, "name", "tab\there"), c, hx...)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a bad name: status %d", rec.Code)
	}
	mustContain(t, "a bad name", rec.Body.String(), "A shelf&#39;s name is 1 to 100 characters.")
	if rec = ta.do("POST", "/admin/movies/shelves/99999/hide", lineupForm(csrf, ""), c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("no such shelf: status %d", rec.Code)
	}

	// Titles hidden and shown again.
	byName := map[string]int64{}
	titles, _, _ := ta.st.AdminTitles(context.Background(), store.KindMovie, nil, "", 0, 10)
	for _, ti := range titles {
		byName[ti.Name] = ti.ID
	}
	form := lineupForm(csrf, back, "action", "hide", "ids", fmt.Sprint(byName["Quiet Harbour"]))
	rec = ta.do("POST", "/admin/movies/titles", form, c, hx...)
	mustContain(t, "a title hidden", rec.Body.String(), "Hid 1 film from devices.", "<strong>2</strong> of 3 films shown", `title="1 of 2 shown">1/2</span>`)
	rec = ta.do("POST", "/admin/movies/titles", lineupForm(csrf, back, "action", "show", "ids", fmt.Sprint(byName["Quiet Harbour"])), c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/movies?"+back {
		t.Errorf("without htmx: status %d, to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, bad := range []url.Values{lineupForm(csrf, "", "action", "lose", "ids", "1"), lineupForm(csrf, "", "action", "hide"),
		lineupForm(csrf, "", "action", "hide", "ids", "x")} {
		if rec := ta.do("POST", "/admin/movies/titles", bad, c, hx...); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d", bad, rec.Code)
		}
	}
	if rec := ta.do("POST", shelf+"hide", lineupForm("wrong", ""), c, hx...); rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}

	// Search, and the Series page.
	mustContain(t, "found", ta.do("GET", "/admin/movies?q=lantern", nil, c).Body.String(), "Paper Lanterns", "1–1 of 1")
	page = ta.do("GET", "/admin/series", nil, c).Body.String()
	mustContain(t, "the Series page", page, "<h1>Series</h1>", "<strong>1</strong> of 1 series shown", "Coastline", ">All series<")
	page = ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "the Live TV page", page, "<h1>Live TV</h1>", "<title>Live TV · Overscan admin</title>")
}

// TestLibraryAPI does in the API what the Movies and Series pages do.
func TestLibraryAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLibrary()
	ids := ta.shelfIDs(store.KindMovie)

	var shelves struct{ Shelves []apiShelf }
	rec := ta.apiDo("GET", "library/movies/shelves", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &shelves); err != nil || len(shelves.Shelves) != 2 ||
		shelves.Shelves[0].Name != "Drama" || shelves.Shelves[0].Titles != 2 || shelves.Shelves[0].Sources[0] != "films" {
		t.Fatalf("shelves: status %d, %s", rec.Code, rec.Body)
	}
	var sh apiShelf
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/movies/shelves/%d", ids["Comedy"]), `{"hidden": true, "name": "Laughs"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &sh); err != nil || !sh.Hidden || sh.Name != "Laughs" || sh.Own != "Comedy" {
		t.Errorf("changed: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/movies/shelves/%d", ids["Comedy"]), `{"hidden": false, "name": null}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &sh); err != nil || sh.Hidden || sh.Name != "Comedy" {
		t.Errorf("changed back: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PUT", "library/movies/order", fmt.Sprintf(`{"shelves": [%d]}`, ids["Comedy"]), c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &shelves); err != nil || shelves.Shelves[0].Name != "Comedy" || !shelves.Shelves[0].Placed {
		t.Errorf("ordered: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("DELETE", "library/movies/order", "", c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &shelves); err != nil || shelves.Shelves[0].Name != "Drama" || shelves.Shelves[0].Placed {
		t.Errorf("order reset: status %d, %s", rec.Code, rec.Body)
	}

	var titles struct {
		Total  int
		Titles []apiTitle
	}
	rec = ta.apiDo("GET", fmt.Sprintf("library/movies/titles?shelf=%d&limit=1&offset=1", ids["Drama"]), "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &titles); err != nil || titles.Total != 2 || len(titles.Titles) != 1 ||
		titles.Titles[0].Name != "The Long Field" || titles.Titles[0].Source != "films" || titles.Titles[0].Shelf != "Drama" {
		t.Fatalf("titles: status %d, %s", rec.Code, rec.Body)
	}
	var ti apiTitle
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/titles/%d", titles.Titles[0].ID), `{"hidden": true}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &ti); err != nil || !ti.Hidden || ti.Name != "The Long Field" {
		t.Errorf("hidden: status %d, %s", rec.Code, rec.Body)
	}
	if found, _ := ta.st.SearchTitles(context.Background(), "long", 5); len(found) != 0 {
		t.Errorf("devices find a hidden title: %+v", found)
	}

	for _, bad := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"GET", "library/films/shelves", "", http.StatusNotFound, "not_found"},
		{"PATCH", "library/movies/shelves/99999", `{"hidden": true}`, http.StatusNotFound, "not_found"},
		{"PATCH", fmt.Sprintf("library/movies/shelves/%d", ids["Drama"]), `{"hidden": "yes"}`, http.StatusBadRequest, "bad_json"},
		{"PATCH", fmt.Sprintf("library/movies/shelves/%d", ids["Drama"]), `{"locked": "yes"}`, http.StatusBadRequest, "bad_json"},
		{"PATCH", fmt.Sprintf("library/movies/shelves/%d", ids["Drama"]), `{"pinned": true}`, http.StatusBadRequest, "bad_json"},
		{"PATCH", fmt.Sprintf("library/movies/shelves/%d", ids["Drama"]), `{"name": "a\u0007"}`, http.StatusBadRequest, "bad_name"},
		{"PUT", "library/movies/order", fmt.Sprintf(`{"shelves": [%d, %d]}`, ids["Drama"], ids["Drama"]), http.StatusBadRequest, "bad_shelves"},
		{"PUT", "library/movies/order", `{"shelves": [99999]}`, http.StatusBadRequest, "bad_shelves"},
		{"GET", "library/movies/titles?shelf=x", "", http.StatusBadRequest, "bad_query"},
		{"GET", "library/movies/titles?limit=0", "", http.StatusBadRequest, "bad_query"},
		{"GET", "library/movies/titles?shelf=99999", "", http.StatusNotFound, "not_found"},
		{"PATCH", "library/titles/99999", `{"hidden": true}`, http.StatusNotFound, "not_found"},
		{"PATCH", fmt.Sprintf("library/titles/%d", titles.Titles[0].ID), `{}`, http.StatusBadRequest, "bad_json"},
	} {
		var header []string
		if bad.method != "GET" {
			header = h
		}
		apiErrorOf(t, ta.apiDo(bad.method, bad.path, bad.body, c, header...), bad.status, bad.code)
	}
}

// TestLibraryLocks locks shelves and titles from the Movies page and the API
// (D85), and sets what marks for adults do (D86).
func TestLibraryLocks(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLibrary()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	ids := ta.shelfIDs(store.KindMovie)
	drama := ids["Drama"]
	back := url.Values{"shelf": {fmt.Sprint(drama)}}.Encode()

	rec := ta.do("POST", fmt.Sprintf("/admin/movies/shelves/%d/lock", drama), lineupForm(csrf, back), c, hx...)
	mustContain(t, "a shelf locked", rec.Body.String(), "Locked Drama: devices need the parental PIN for anything on it.",
		"Unlock the shelf", `<span class="badge locked">`,
		"The lock on a shelf or a title does nothing until there is a parental PIN.")
	if shelves, _ := ta.st.Shelves(ctx, store.KindMovie); !shelves[0].Locked {
		t.Errorf("devices' shelves %+v", shelves)
	}
	byName := map[string]int64{}
	titles, _, _ := ta.st.AdminTitles(ctx, store.KindMovie, nil, "", 0, 10)
	for _, ti := range titles {
		byName[ti.Name] = ti.ID
	}
	rec = ta.do("POST", "/admin/movies/titles", lineupForm(csrf, back, "action", "lock", "ids", fmt.Sprint(byName["Paper Lanterns"])), c, hx...)
	mustContain(t, "a title locked", rec.Body.String(), "Locked 1 film: devices need the parental PIN to play them.",
		"2 locks on shelves and titles do nothing until there is a parental PIN.")
	rec = ta.do("POST", fmt.Sprintf("/admin/movies/shelves/%d/unlock", drama), lineupForm(csrf, back), c, hx...)
	mustContain(t, "a shelf unlocked", rec.Body.String(), "Unlocked Drama.", "Lock the shelf")
	page := ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "the settings", page, "Not set, so the 1 lock does nothing yet.", "Marked for adults",
		`<option value="lock" selected>`)

	// The API.
	var sh apiShelf
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/movies/shelves/%d", drama), `{"locked": true}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &sh); err != nil || !sh.Locked {
		t.Errorf("shelf locked: status %d, %s", rec.Code, rec.Body)
	}
	var ti apiTitle
	rec = ta.apiDo("PATCH", fmt.Sprintf("library/titles/%d", byName["Paper Lanterns"]), `{"locked": false}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &ti); err != nil || ti.Locked || ti.Adult || ti.PIN {
		t.Errorf("title unlocked: status %d, %s", rec.Code, rec.Body)
	}
	var list struct{ Titles []apiTitle }
	rec = ta.apiDo("GET", fmt.Sprintf("library/movies/titles?shelf=%d", drama), "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Titles) != 2 || list.Titles[0].Locked ||
		!list.Titles[0].PIN {
		t.Errorf("on a locked shelf: status %d, %s", rec.Code, rec.Body)
	}

	// What marks for adults do.
	rec = ta.do("POST", "/admin/settings/adult", lineupForm(csrf, "", "adult", "hide"), c, hx...)
	mustContain(t, "marks hide", rec.Body.String(),
		"Channels, films and series that their sources mark for adults are hidden from devices.", `<option value="hide" selected>`)
	if marks, _ := ta.st.AdultMarks(ctx); marks != store.AdultHide {
		t.Errorf("marks %q", marks)
	}
	if rec = ta.do("POST", "/admin/settings/adult", lineupForm(csrf, "", "adult", "sometimes"), c, hx...); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown choice: status %d", rec.Code)
	}
	var settings apiSettings
	rec = ta.apiDo("PATCH", "settings", `{"adult_marks": "lock"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil || settings.AdultMarks != store.AdultLock {
		t.Errorf("marks lock: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"adult_marks": true}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"adult_marks": "often"}`, c, h...), http.StatusBadRequest, "bad_adult_marks")
}
