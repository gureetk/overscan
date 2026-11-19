// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// lineupForm is a form from the lineup page, with the session's token and
// the view it came from.
func lineupForm(csrf, back string, kv ...string) url.Values {
	v := url.Values{"csrf": {csrf}, "back": {back}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Add(kv[i], kv[i+1])
	}
	return v
}

// mustContain fails the test for each of want that body lacks.
func mustContain(t *testing.T, what, body string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("%s lacks %q", what, w)
		}
	}
}

func TestLineupPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withLineup()
	gs := ta.groups(c)

	rec := ta.do("GET", "/admin/lineup", nil, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	mustContain(t, "the page", rec.Body.String(),
		`<a href="/admin/lineup" aria-current="page" title="Live TV">`, `<h2 id="channels-title">All channels</h2>`,
		`<span class="g-name">News</span>`, `<span class="g-name">Sports</span>`,
		"Alpha", "Bravo", "Charlie", "Delta", "1–4 of 4")

	// A group's channels, with the group's own forms.
	news := gs["News"]
	rec = ta.do("GET", fmt.Sprintf("/admin/lineup?group=%d", news.ID), nil, c)
	body := rec.Body.String()
	mustContain(t, "News", body, "Alpha", "Charlie", fmt.Sprintf(`action="/admin/lineup/groups/%d/rename"`, news.ID), "From main: News.")
	if strings.Contains(body, "Bravo") || strings.Contains(body, fmt.Sprintf(`/admin/lineup/groups/%d/delete`, news.ID)) {
		t.Errorf("News shows Bravo, or can be deleted: %s", body)
	}

	// Finding channels by name, in any case.
	rec = ta.do("GET", "/admin/lineup?q=RAV", nil, c)
	if body = rec.Body.String(); !strings.Contains(body, "Bravo") || strings.Contains(body, "Alpha") {
		t.Errorf("q=RAV: %s", body)
	}
	mustContain(t, "q=zzz", ta.do("GET", "/admin/lineup?q=zzz", nil, c).Body.String(), "No channel here has “zzz” in its name.")
	mustContain(t, "group=0", ta.do("GET", "/admin/lineup?group=0", nil, c).Body.String(), `<h2 id="channels-title">In no group</h2>`, "No channels here.")
	mustContain(t, "group=999", ta.do("GET", "/admin/lineup?group=999", nil, c).Body.String(), `<h2 id="channels-title">No such group</h2>`)

	// Every form says which view it came from, to come back to it.
	rec = ta.do("GET", fmt.Sprintf("/admin/lineup?group=%d&q=ar", news.ID), nil, c)
	mustContain(t, "a view", rec.Body.String(), fmt.Sprintf(`name="back" value="group=%d&amp;q=ar"`, news.ID))
}

func TestLineupPages(t *testing.T) {
	ta := newTestAdmin(t)
	ctx := context.Background()
	srcID, err := ta.st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	var chs []store.CatalogChannel
	for i := 1; i <= 150; i++ {
		name := fmt.Sprintf("Channel %03d", i)
		chs = append(chs, store.CatalogChannel{IdentityKey: name, Name: name, Category: "All", StreamURL: "http://streams.example/" + name})
	}
	if _, err := ta.st.ApplyCatalog(ctx, srcID, chs, ta.clock); err != nil {
		t.Fatal(err)
	}
	ta.setPassword("correct horse")
	c, _ := ta.signIn("correct horse")

	body := ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "page 1", body, "1–100 of 150", "Channel 100", `<a href="/admin/lineup?page=2">Next</a>`)
	if strings.Contains(body, "Channel 101") || strings.Contains(body, "Previous") {
		t.Errorf("page 1 shows Channel 101, or a previous page")
	}
	body = ta.do("GET", "/admin/lineup?page=2", nil, c).Body.String()
	mustContain(t, "page 2", body, "101–150 of 150", "Channel 150", `<a href="/admin/lineup">Previous</a>`)
	// A page past the end, which a change can leave the view on, shows the
	// last.
	mustContain(t, "page 9", ta.do("GET", "/admin/lineup?page=9", nil, c).Body.String(), "101–150 of 150")

	// The search for channels to add lists the first 50.
	id, err := ta.st.CreateLineupGroup(ctx, store.DefaultLineupID, "Favourites")
	if err != nil {
		t.Fatal(err)
	}
	found := addResults(t, ta.do("GET", fmt.Sprintf("/admin/lineup?group=%d&add=channel", id), nil, c).Body.String())
	if n := strings.Count(found, `name="ids"`); n != 50 || !strings.Contains(found, "The first 50 of 150.") {
		t.Errorf("%d channels listed: %s", n, found)
	}
}

func TestLineupRows(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withLineup()
	chs, _ := ta.channels(c, "")
	alpha := chs["Alpha"]

	// A name opens its channel in the panel beside the list.
	page := ta.do("GET", "/admin/lineup?q=al", nil, c).Body.String()
	mustContain(t, "the rows", page, fmt.Sprintf(`hx-get="/admin/lineup/channels/%d/edit?q=al"`, alpha.ID),
		fmt.Sprintf(`href="/admin/lineup?edit=%d&amp;q=al"`, alpha.ID), `<div id="channel-panel"></div>`)
	rec := ta.do("GET", fmt.Sprintf("/admin/lineup/channels/%d/edit?q=al", alpha.ID), nil, c, "HX-Request", "true")
	body := strings.TrimSpace(rec.Body.String())
	if rec.Code != http.StatusOK || !strings.HasPrefix(body, `<aside id="channel-panel" class="card channel-panel"`) || strings.Contains(body, "<html") {
		t.Errorf("the panel: status %d, %s", rec.Code, body)
	}
	mustContain(t, "the panel", body, `name="back" value="q=al"`, `placeholder="Alpha"`, "Its category's group: News",
		"From main, as Alpha, in its category News", `name="visibility" value="group" checked`, `name="repair" value="" checked`)
	for _, path := range []string{"/admin/lineup/channels/9999/edit", "/admin/lineup/channels/x/edit", fmt.Sprintf("/admin/lineup/channels/%d/row", alpha.ID)} {
		if rec = ta.do("GET", path, nil, c); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}

	// Without htmx, the page opens with the panel, and the row marked.
	mustContain(t, "edit=", ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", alpha.ID), nil, c).Body.String(),
		`<aside id="channel-panel" class="card channel-panel"`, fmt.Sprintf(`<tr id="entry-%d" class="open">`, alpha.ID))
	// A channel that is not there leaves the panel shut.
	if body := ta.do("GET", "/admin/lineup?edit=9999", nil, c).Body.String(); !strings.Contains(body, `<div id="channel-panel"></div>`) {
		t.Error("edit=9999 opened a panel")
	}
}

func TestSaveAChannel(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	chs, _ := ta.channels(c, "")
	gs := ta.groups(c)
	path := fmt.Sprintf("/admin/lineup/channels/%d", chs["Alpha"].ID)
	hx := []string{"HX-Request", "true"}

	// Without htmx, the browser goes back to the view the form came from.
	rec := ta.do("POST", path, lineupForm(csrf, "q=al", "name", " Alpha One ", "group", fmt.Sprint(gs["Sports"].ID), "visibility", "group", "number", "50"), c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/lineup?q=al" {
		t.Errorf("save: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
	if got, want := strings.Join(ta.seen(), ", "), "2 Bravo Sports, 3 Charlie News, 4 Delta Sports, 50 Alpha One Sports"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}

	// Empty fields give the channel back to its source; with htmx, the
	// page comes back with what was done.
	rec = ta.do("POST", path, lineupForm(csrf, "", "name", "", "group", "0", "visibility", "hidden", "number", ""), c, hx...)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Replace-Url") != "/admin/lineup" {
		t.Errorf("reset: status %d, HX-Replace-Url %q", rec.Code, rec.Header().Get("HX-Replace-Url"))
	}
	mustContain(t, "the answer", rec.Body.String(), `<p class="alert notice" role="status">Saved Alpha.</p>`)
	a, _ := ta.channels(c, "")
	if e := a["Alpha"]; e.Renamed || e.Moved || e.Pinned || e.Number == nil || *e.Number != 1 || e.Visibility != "hidden" {
		t.Errorf("after the reset: %+v", e)
	}

	// Ticking Pinned pins the number it has; unticking it lets it go.
	ta.do("POST", path, lineupForm(csrf, "", "number", "1", "pinned", "1", "visibility", "hidden"), c, hx...)
	if a, _ = ta.channels(c, ""); !a["Alpha"].Pinned || *a["Alpha"].Number != 1 {
		t.Errorf("after pinning: %+v", a["Alpha"])
	}
	rec = ta.do("POST", path, lineupForm(csrf, "", "number", "1", "pinned", "1", "visibility", "hidden"), c, hx...)
	mustContain(t, "the same again", rec.Body.String(), "Nothing to change.")
	ta.do("POST", path, lineupForm(csrf, "", "number", "1", "visibility", "hidden"), c, hx...)
	if a, _ = ta.channels(c, ""); a["Alpha"].Pinned {
		t.Errorf("after unpinning: %+v", a["Alpha"])
	}

	// A mistake comes back with the row still open.
	for _, tc := range []struct {
		form   url.Values
		status int
		text   string
	}{
		{lineupForm(csrf, "", "number", "x"), http.StatusBadRequest, "A number runs from 1 to 999999."},
		{lineupForm(csrf, "", "number", "1000000"), http.StatusBadRequest, "A number runs from 1 to 999999."},
		{lineupForm(csrf, "", "group", "-1"), http.StatusBadRequest, "Choose a group."},
		{lineupForm(csrf, "", "group", "9999"), http.StatusNotFound, "There is no such group."},
		{lineupForm(csrf, "", "visibility", "sometimes"), http.StatusBadRequest, ""},
		{lineupForm(csrf, "", "name", "Bad\x07name"), http.StatusBadRequest, "control characters"},
	} {
		rec = ta.do("POST", path, tc.form, c, hx...)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.text) || !strings.Contains(rec.Body.String(), `<aside id="channel-panel"`) {
			t.Errorf("%v: status %d, want %d with %q", tc.form, rec.Code, tc.status, tc.text)
		}
	}
	if rec = ta.do("POST", "/admin/lineup/channels/9999", lineupForm(csrf, "", "name", "X"), c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown channel: status %d", rec.Code)
	}
}

func TestTickedChannels(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	chs, _ := ta.channels(c, "")
	gs := ta.groups(c)
	hx := []string{"HX-Request", "true"}
	tick := func(form url.Values, names ...string) url.Values {
		for _, n := range names {
			form.Add("ids", fmt.Sprint(chs[n].ID))
		}
		return form
	}
	send := func(form url.Values, notice string) {
		t.Helper()
		rec := ta.do("POST", "/admin/lineup/channels", form, c, hx...)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), notice) {
			t.Errorf("%v: status %d, want %q", form, rec.Code, notice)
		}
	}

	send(tick(lineupForm(csrf, "", "action", "hide"), "Alpha", "Bravo"), "Hid 2 channels.")
	if got, want := strings.Join(ta.seen(), ", "), "3 Charlie News, 4 Delta Sports"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	send(tick(lineupForm(csrf, "", "action", "move", "group", fmt.Sprint(gs["News"].ID)), "Bravo", "Delta"), "Moved 2 channels.")
	send(tick(lineupForm(csrf, "", "action", "show"), "Alpha", "Bravo"), "Showed 2 channels.")
	if got, want := strings.Join(ta.seen(), ", "), "1 Alpha News, 2 Bravo News, 3 Charlie News, 4 Delta News"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	send(tick(lineupForm(csrf, "", "action", "move", "group", "0"), "Bravo", "Delta"), "Put 2 channels back in their categories&#39; groups.")
	send(tick(lineupForm(csrf, "", "action", "group"), "Alpha"), "Set as their group: 1 channel.")
	if got, want := strings.Join(ta.seen(), ", "), "1 Alpha News, 2 Bravo Sports, 3 Charlie News, 4 Delta Sports"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}

	for _, tc := range []struct {
		form   url.Values
		status int
		text   string
	}{
		{lineupForm(csrf, "", "action", "hide"), http.StatusBadRequest, "Choose at least one channel."},
		{tick(lineupForm(csrf, "", "action", "explode"), "Alpha"), http.StatusBadRequest, "Choose what to do with the ticked channels."},
		{lineupForm(csrf, "", "action", "hide", "ids", "x"), http.StatusBadRequest, "The form could not be read."},
		{lineupForm(csrf, "", "action", "hide", "ids", "9999"), http.StatusNotFound, "There is no such channel."},
		{tick(lineupForm(csrf, "", "action", "move", "group", "-2"), "Alpha"), http.StatusBadRequest, "Choose a group."},
		{tick(lineupForm(csrf, "", "action", "move"), "Alpha"), http.StatusBadRequest, "Choose a group."},
	} {
		rec := ta.do("POST", "/admin/lineup/channels", tc.form, c, hx...)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.text) {
			t.Errorf("%v: status %d, want %d with %q", tc.form, rec.Code, tc.status, tc.text)
		}
	}
}

func TestGroupForms(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	gs := ta.groups(c)
	news, sports := gs["News"], gs["Sports"]
	hx := []string{"HX-Request", "true"}
	group := func(id int64, action string, kv ...string) *http.Response {
		t.Helper()
		back := fmt.Sprintf("group=%d", id)
		return ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/%s", id, action), lineupForm(csrf, back, kv...), c).Result()
	}

	// A new group: the page moves to it.
	rec := ta.do("POST", "/admin/lineup/groups", lineupForm(csrf, "q=x", "name", "Films"), c)
	films := ta.groups(c)["Films"]
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != fmt.Sprintf("/admin/lineup?group=%d", films.ID) || !films.Edited {
		t.Errorf("new group: status %d, Location %q, %+v", rec.Code, rec.Header().Get("Location"), films)
	}
	rec = ta.do("POST", "/admin/lineup/groups", lineupForm(csrf, "", "name", "News"), c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Another group has that name") {
		t.Errorf("a second News: status %d", rec.Code)
	}

	if res := group(films.ID, "rename", "name", "Movies"); res.StatusCode != http.StatusSeeOther || ta.groups(c)["Movies"].ID != films.ID {
		t.Errorf("rename: status %d", res.StatusCode)
	}
	if res := group(sports.ID, "hide"); res.StatusCode != http.StatusSeeOther || !ta.groups(c)["Sports"].Hidden {
		t.Errorf("hide: status %d", res.StatusCode)
	}
	if got, want := strings.Join(ta.seen(), ", "), "1 Alpha News, 3 Charlie News"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	if res := group(sports.ID, "show"); res.StatusCode != http.StatusSeeOther || ta.groups(c)["Sports"].Hidden {
		t.Errorf("show: status %d", res.StatusCode)
	}
	if res := group(news.ID, "number", "from", "100"); res.StatusCode != http.StatusSeeOther {
		t.Errorf("number: status %d", res.StatusCode)
	}
	if got, want := strings.Join(ta.seen(), ", "), "2 Bravo Sports, 4 Delta Sports, 100 Alpha News, 101 Charlie News"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}

	// Merging moves the page to the group merged into.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/merge", sports.ID), lineupForm(csrf, fmt.Sprintf("group=%d", sports.ID), "into", fmt.Sprint(news.ID)), c, hx...)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Replace-Url") != fmt.Sprintf("/admin/lineup?group=%d", news.ID) {
		t.Errorf("merge: status %d, HX-Replace-Url %q", rec.Code, rec.Header().Get("HX-Replace-Url"))
	}
	mustContain(t, "after merging", rec.Body.String(), "Merged the group.", "Bravo", "From main: News, main: Sports.")
	if _, ok := ta.groups(c)["Sports"]; ok {
		t.Error("Sports is still there after merging it")
	}

	// Only an empty group can be deleted; then the page shows every channel.
	if res := group(news.ID, "delete"); res.StatusCode != http.StatusConflict {
		t.Errorf("delete News: status %d", res.StatusCode)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/delete", films.ID), lineupForm(csrf, fmt.Sprintf("group=%d", films.ID)), c, hx...)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Replace-Url") != "/admin/lineup" {
		t.Errorf("delete Movies: status %d, HX-Replace-Url %q", rec.Code, rec.Header().Get("HX-Replace-Url"))
	}

	// With htmx sending the token in a header, as the pages do.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/hide", news.ID), url.Values{"back": {""}}, c, append(hx, h...)...)
	if rec.Code != http.StatusOK || !ta.groups(c)["News"].Hidden {
		t.Errorf("hide with the token in a header: status %d", rec.Code)
	}

	for path, want := range map[string]int{
		fmt.Sprintf("/admin/lineup/groups/%d/explode", news.ID): http.StatusNotFound,
		"/admin/lineup/groups/x/hide":                           http.StatusNotFound,
		"/admin/lineup/groups/9999/hide":                        http.StatusNotFound,
		fmt.Sprintf("/admin/lineup/groups/%d/merge", news.ID):   http.StatusBadRequest,
		fmt.Sprintf("/admin/lineup/groups/%d/number", news.ID):  http.StatusBadRequest,
		fmt.Sprintf("/admin/lineup/groups/%d/rename", news.ID):  http.StatusBadRequest,
	} {
		if rec = ta.do("POST", path, lineupForm(csrf, ""), c, hx...); rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
}

func TestCategoryAndSourceForms(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	gs := ta.groups(c)
	hx := []string{"HX-Request", "true"}

	// Sports' category goes to News, with its channels.
	sportsCategory := fmt.Sprint(*gs["Sports"].Categories[0].ID)
	rec := ta.do("POST", "/admin/lineup/categories", lineupForm(csrf, "", "category", sportsCategory, "group", fmt.Sprint(gs["News"].ID)), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Moved the category, with its channels.") {
		t.Errorf("move: status %d", rec.Code)
	}
	if got, want := strings.Join(ta.seen(), ", "), "1 Alpha News, 2 Bravo News, 3 Charlie News, 4 Delta News"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	if rec = ta.do("POST", "/admin/lineup/categories", lineupForm(csrf, "", "category", sportsCategory), c, hx...); rec.Code != http.StatusBadRequest {
		t.Errorf("no group: status %d", rec.Code)
	}

}

func TestLineupFormsNeedTheCSRFToken(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withLineup()
	gs := ta.groups(c)
	for _, path := range []string{
		"/admin/lineup/channels", "/admin/lineup/channels/1", "/admin/lineup/groups",
		fmt.Sprintf("/admin/lineup/groups/%d/hide", gs["News"].ID), "/admin/lineup/categories", "/admin/sources/1/categories-hidden",
	} {
		if rec := ta.do("POST", path, url.Values{"csrf": {"wrong"}, "name": {"X"}}, c); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
	if ta.groups(c)["News"].Hidden {
		t.Error("News was hidden without the token")
	}
	// Signed out, the page sends the browser to sign in first.
	rec := ta.do("GET", "/admin/lineup?group=1", nil, nil)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sign-in?next=%2Fadmin%2Flineup%3Fgroup%3D1" {
		t.Errorf("signed out: status %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

// addResults returns the part of a group's page that lists the channels found
// to add to it.
func addResults(t *testing.T, page string) string {
	t.Helper()
	start := strings.Index(page, `<div id="add-results">`)
	if start < 0 {
		t.Fatalf("no channels to add on the page")
	}
	end := strings.Index(page[start:], "</section>")
	return page[start : start+end]
}

func TestAddChannelsToAGroup(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	chs, _ := ta.channels(c, "")
	hx := []string{"HX-Request", "true"}

	// A new group opens empty, with the search for channels to add.
	rec := ta.do("POST", "/admin/lineup/groups", lineupForm(csrf, "", "name", "Favourites"), c)
	fav := ta.groups(c)["Favourites"]
	if rec.Code != http.StatusSeeOther || fav.ID == 0 {
		t.Fatalf("new group: status %d", rec.Code)
	}
	page := ta.do("GET", fmt.Sprintf("/admin/lineup?group=%d", fav.ID), nil, c).Body.String()
	mustContain(t, "the new group", addResults(t, page), "Type part of a channel")

	// Finding lists the channels in other groups, in any case.
	page = ta.do("GET", fmt.Sprintf("/admin/lineup?group=%d&add=A", fav.ID), nil, c).Body.String()
	found := addResults(t, page)
	mustContain(t, "the channels found", found, "Alpha", "Bravo", "Charlie", "Delta", "in News", "in Sports",
		fmt.Sprintf(`name="back" value="add=A&amp;group=%d"`, fav.ID), "Add the ticked channels to Favourites")

	// Adding them puts them in the group, and the page stays on the search.
	form := lineupForm(csrf, fmt.Sprintf("add=A&group=%d", fav.ID), "action", "move", "group", fmt.Sprint(fav.ID))
	form.Add("ids", fmt.Sprint(chs["Bravo"].ID))
	form.Add("ids", fmt.Sprint(chs["Delta"].ID))
	rec = ta.do("POST", "/admin/lineup/channels", form, c, hx...)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Replace-Url") != fmt.Sprintf("/admin/lineup?add=A&group=%d", fav.ID) {
		t.Fatalf("add: status %d, HX-Replace-Url %q", rec.Code, rec.Header().Get("HX-Replace-Url"))
	}
	if got, want := strings.Join(ta.seen(), ", "), "1 Alpha News, 2 Bravo Favourites, 3 Charlie News, 4 Delta Favourites"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	found = addResults(t, rec.Body.String())
	if strings.Contains(found, "Bravo") || !strings.Contains(found, "Alpha") {
		t.Errorf("after adding, the search lists %s", found)
	}

	// A group's own channels are not offered, and nothing found says so.
	news := ta.groups(c)["News"]
	found = addResults(t, ta.do("GET", fmt.Sprintf("/admin/lineup?group=%d&add=Alpha", news.ID), nil, c).Body.String())
	mustContain(t, "no match", found, "No channel in another group has “Alpha” in its name.")

	// Outside a group of its own, there is nothing to add to.
	for _, path := range []string{"/admin/lineup?add=A", "/admin/lineup?group=0&add=A"} {
		if page := ta.do("GET", path, nil, c).Body.String(); strings.Contains(page, `id="add-results"`) {
			t.Errorf("%s offers channels to add", path)
		}
	}
}
