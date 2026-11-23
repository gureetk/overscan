// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
)

// A group's first number, sorting by name, and find and replace in the
// names of groups, from the Live TV page and the API (D137).
func TestGroupNumbersInTheAdmin(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	news := ta.groups(c)["News"]
	back := url.Values{"group": {fmt.Sprint(news.ID)}}.Encode()

	page := ta.do("GET", "/admin/lineup?"+back, nil, c).Body.String()
	mustContain(t, "the group's page", page, "Numbers from…", "Sort by name", "Find and replace in the groups")

	rec := ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/numbers-from", news.ID), lineupForm(csrf, back, "from", "100"), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "numbered from 100") ||
		!strings.Contains(rec.Body.String(), "Numbers from 100") {
		t.Fatalf("numbers from 100: status %d\n%s", rec.Code, rec.Body)
	}
	if g := ta.groups(c)["News"]; g.NumbersFrom == nil || *g.NumbersFrom != 100 {
		t.Errorf("the API's numbers_from: %+v", g)
	}
	chs, _ := ta.channels(c, fmt.Sprintf("?group=%d", news.ID))
	if chs["Alpha"].Number == nil || *chs["Alpha"].Number != 100 || chs["Charlie"].Number == nil || *chs["Charlie"].Number != 101 {
		t.Errorf("News's numbers: %+v", chs)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/numbers-from", news.ID), lineupForm(csrf, back, "from", "0"), c, hx...)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("from 0: status %d", rec.Code)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/numbers-from", news.ID), lineupForm(csrf, back, "stop", "1"), c, hx...)
	if g := ta.groups(c)["News"]; rec.Code != http.StatusOK || g.NumbersFrom != nil || !strings.Contains(rec.Body.String(), "no first number") {
		t.Errorf("no first number: status %d, %+v", rec.Code, g)
	}
	if chs, _ := ta.channels(c, fmt.Sprintf("?group=%d", news.ID)); *chs["Alpha"].Number != 100 {
		t.Errorf("Alpha's number after: %+v", chs["Alpha"])
	}

	// Sort by name.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/sort", news.ID), lineupForm(csrf, back), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sorted the group") {
		t.Errorf("sort: status %d", rec.Code)
	}

	// Find and replace in the groups' names.
	rec = ta.do("POST", "/admin/lineup/groups/replace", lineupForm(csrf, back, "find", "/^(sport)s$/", "with", "$1"), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Renamed 1 group: Sports → Sport.") {
		t.Errorf("replace: status %d\n%s", rec.Code, alertsOf(rec.Body.String()))
	}
	rec = ta.do("POST", "/admin/lineup/groups/replace", lineupForm(csrf, back, "find", "Sport", "with", "News"), c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "would both be named News") {
		t.Errorf("a clash: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/lineup/groups/replace", lineupForm(csrf, back, "find", "nothing like it", "with", "x"), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "nothing changed") {
		t.Errorf("no match: status %d", rec.Code)
	}

	// The API.
	rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/groups/%d", news.ID), `{"numbers_from": 500}`, c, h...)
	var g apiGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || rec.Code != http.StatusOK || g.NumbersFrom == nil || *g.NumbersFrom != 500 {
		t.Errorf("the API, numbers_from: status %d, %s", rec.Code, rec.Body)
	}
	if rec := ta.apiDo("PATCH", fmt.Sprintf("lineup/groups/%d", news.ID), `{"numbers_from": 1000000}`, c, h...); rec.Code != http.StatusBadRequest {
		t.Errorf("the API, too high: status %d", rec.Code)
	}
	rec = ta.apiDo("POST", fmt.Sprintf("lineup/groups/%d/sort", news.ID), "", c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sorted"`) {
		t.Errorf("the API, sort: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("POST", "lineup/groups/replace", `{"find": "Sport", "replace": "Sports"}`, c, h...)
	var body struct{ Renamed []renamed }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Renamed) != 1 ||
		body.Renamed[0].To != "Sports" {
		t.Errorf("the API, replace: status %d, %s", rec.Code, rec.Body)
	}
	if rec := ta.apiDo("POST", "lineup/groups/replace", `{"find": "/(/"}`, c, h...); rec.Code != http.StatusBadRequest {
		t.Errorf("the API, a bad expression: status %d", rec.Code)
	}
}

// Find and replace in the names of the shelves, from the Movies page and
// the API (D137).
func TestReplaceShelfNames(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLibrary()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	rec := ta.do("POST", "/admin/movies/shelves/replace", url.Values{"csrf": {csrf}, "find": {"/y$/"}, "with": {"ies"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Renamed 1 shelf: Comedy → Comedies.") {
		t.Errorf("replace: status %d\n%s", rec.Code, alertsOf(rec.Body.String()))
	}
	rec = ta.apiDo("POST", "library/movies/shelves/replace", `{"find": "/^(.*)$/", "replace": "Films: $1"}`, c, h...)
	var body struct{ Renamed []renamed }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK || len(body.Renamed) != 2 {
		t.Errorf("the API: status %d, %s", rec.Code, rec.Body)
	}
	if ids := ta.shelfIDs("movie"); len(ids) != 2 {
		t.Errorf("shelves %v", ids)
	}
	shelves, _ := ta.st.LibraryShelves(t.Context(), "movie")
	if shelves[0].Name != "Films: Drama" || shelves[1].Name != "Films: Comedies" {
		t.Errorf("shelves %+v", shelves)
	}
}

// alertsOf returns the alerts of a page, for a test's message.
func alertsOf(page string) string {
	var out []string
	for _, part := range strings.Split(page, `class="alert`)[1:] {
		end := strings.Index(part, "</")
		out = append(out, part[:max(end, 0)])
	}
	return strings.Join(out, "\n")
}

// A merge rule puts sources in order, from the Rules tab and the API, and
// says so (D137).
func TestMergeRuleSources(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := t.Context()
	sources, err := ta.st.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ta.st.AddSource(ctx, source.Settings{Name: "one", URL: "http://provider.example/one.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	main := sources[0].ID
	page := ta.do("GET", "/admin/lineup/rules", nil, c).Body.String()
	mustContain(t, "the rules page", page, "Sources to show first", `name="first_source"`)
	rec := ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, "", "action", "merge", "copies", "guide",
		"first_source", fmt.Sprint(other), "first_source", fmt.Sprint(main), "first_source", ""), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "the first from one, then from main, else the first in the lineup") {
		t.Errorf("a merge with sources: status %d\n%s", rec.Code, alertsOf(rec.Body.String()))
	}
	rec = ta.apiDo("GET", "lineup/rules", "", c)
	if !strings.Contains(strings.Join(strings.Fields(rec.Body.String()), ""), fmt.Sprintf(`"sources":[%d,%d]`, other, main)) {
		t.Errorf("the API's rule: %s", rec.Body)
	}
	if rec := ta.apiDo("POST", "lineup/rules", `{"action": "merge", "copies": "guide", "sources": [999]}`, c, h...); rec.Code != http.StatusBadRequest {
		t.Errorf("the API, a source that is not: status %d, %s", rec.Code, rec.Body)
	}
}
