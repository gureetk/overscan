// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// channelNames returns the names of the channels that the API lists, sorted.
func channelNames(chs map[string]apiEntry) []string {
	var out []string
	for name := range chs {
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// A new lineup of the household's own groups starts empty; a group takes
// the categories ticked on its page, or sent to the API, and the lineup
// holds their channels (D136).
func TestLineupOfOwnGroupsInTheAdmin(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	c, h := ta.withLineup()
	csrf := h[1]
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}
	sources, err := ta.st.Sources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources %+v, %v", sources, err)
	}
	main := sources[0].ID

	rec := ta.do("POST", "/admin/lineups/new", lineupForm(csrf, "", "name", "Ours", "merge", "guide"), c, hx...)
	ours := ta.lineupsByName(c)["Ours"]
	if rec.Code != http.StatusOK || ours.ID == 0 || !ours.OwnGroups || len(ours.Viewers) != 0 {
		t.Fatalf("new: status %d, %+v\n%s", rec.Code, ours, rec.Body)
	}
	mustContain(t, "the new lineup's page", rec.Body.String(), "Made the lineup Ours",
		"they take nothing yet", "No account sees it yet")
	if !strings.Contains(logs.String(), `msg="a lineup made in the admin" lineup=Ours own_groups=true merge=guide`) {
		t.Errorf("the log:\n%s", logs.String())
	}
	if ours.Rules != 1 {
		t.Errorf("its merge rule: %d rules", ours.Rules)
	}
	in := fmt.Sprintf("?lineup=%d", ours.ID)
	back := url.Values{"lineup": {fmt.Sprint(ours.ID)}}.Encode()
	if chs, total := ta.channels(c, in); total != 0 {
		t.Errorf("a new lineup's channels: %v", chs)
	}

	// A group, and its categories from the page.
	rec = ta.do("POST", "/admin/lineup/groups", lineupForm(csrf, back, "name", "Picked"), c, hx...)
	if rec.Code != http.StatusOK {
		t.Fatalf("a group: status %d\n%s", rec.Code, rec.Body)
	}
	var g apiGroup
	for _, x := range ta.groupsIn(c, in) {
		g = x
	}
	if g.Name != "Picked" {
		t.Fatalf("the group %+v", g)
	}
	page := ta.do("GET", fmt.Sprintf("/admin/lineup/groups/%d/categories%s", g.ID, in), nil, c).Body.String()
	news, sports := fmt.Sprintf("%d/News", main), fmt.Sprintf("%d/Sports", main)
	mustContain(t, "the categories' page", page, "What Picked takes", `name="listed" value="`+news+`"`,
		`name="tick" value="`+sports+`"> <span class="c-source">main</span> <span class="c-name">Sports</span>`, "2 channels")
	gback := url.Values{"lineup": {fmt.Sprint(ours.ID)}, "group": {fmt.Sprint(g.ID)}}.Encode()
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/categories", g.ID),
		lineupForm(csrf, gback, "listed", news, "listed", sports, "tick", news), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Picked takes 1 category.") {
		t.Fatalf("saving the categories: status %d\n%s", rec.Code, rec.Body)
	}
	if chs, _ := ta.channels(c, in); !slices.Equal(channelNames(chs), []string{"Alpha", "Charlie"}) {
		t.Errorf("the lineup holds %v", channelNames(chs))
	}
	if !strings.Contains(logs.String(), `msg="a group's categories chosen in the admin" lineup=Ours`) {
		t.Errorf("the log:\n%s", logs.String())
	}
	page = ta.do("GET", fmt.Sprintf("/admin/lineup/groups/%d/categories%s", g.ID, in), nil, c).Body.String()
	mustContain(t, "the categories' page", page, `value="`+news+`" checked>`, "1 of 2 ticked")
	// Without a page in the form, nothing changes but what it lists.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/categories", g.ID), lineupForm(csrf, gback), c, hx...)
	if chs, _ := ta.channels(c, in); rec.Code != http.StatusOK || len(chs) != 2 {
		t.Errorf("an empty form: status %d, %v", rec.Code, channelNames(chs))
	}

	// The API lists the categories, and sets a group's.
	rec = ta.apiDo("GET", "lineup/categories"+in, "", c)
	var cats struct {
		Categories []apiLineupCategory
		Total      int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &cats); err != nil || rec.Code != http.StatusOK || cats.Total != 2 ||
		cats.Categories[0].Name != "News" || cats.Categories[0].GroupID == nil || *cats.Categories[0].GroupID != g.ID ||
		cats.Categories[1].GroupID != nil || cats.Categories[1].Channels != 2 {
		t.Errorf("the API's categories: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PUT", fmt.Sprintf("lineup/groups/%d/categories%s", g.ID, in),
		fmt.Sprintf(`{"categories": [{"source_id": %d, "name": "Sports"}]}`, main), c, h...)
	var got apiGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || len(got.Categories) != 1 ||
		got.Categories[0].Name != "Sports" {
		t.Errorf("the API, a group's categories: status %d, %s", rec.Code, rec.Body)
	}
	if chs, _ := ta.channels(c, in); !slices.Equal(channelNames(chs), []string{"Bravo", "Delta"}) {
		t.Errorf("the lineup holds %v", channelNames(chs))
	}
	if chs, _ := ta.channels(c, ""); len(chs) != 4 {
		t.Errorf("the household's lineup changed: %v", channelNames(chs))
	}

	// Mistakes.
	for _, m := range []struct {
		method, path, body string
		status             int
	}{
		{"PUT", fmt.Sprintf("lineup/groups/%d/categories%s", g.ID, in), fmt.Sprintf(`{"categories": [{"source_id": %d, "name": "Weather"}]}`, main), http.StatusNotFound},
		{"PUT", fmt.Sprintf("lineup/groups/%d/categories", g.ID), `{"categories": []}`, http.StatusNotFound}, // another lineup's group
		{"POST", "lineups", `{"name": "Both", "own_groups": true, "copy_of": 1}`, http.StatusBadRequest},
		{"POST", "lineups", `{"name": "Colour", "own_groups": true, "merge": "colour"}`, http.StatusBadRequest},
		{"POST", "lineups", `{"name": "Copy", "merge": "guide"}`, http.StatusBadRequest},
		{"POST", "lineups", `{"name": "Ours", "own_groups": true}`, http.StatusConflict},
	} {
		if rec := ta.apiDo(m.method, m.path, m.body, c, h...); rec.Code != m.status {
			t.Errorf("%s %s %s: status %d, want %d: %s", m.method, m.path, m.body, rec.Code, m.status, rec.Body)
		}
	}
	if rec := ta.do("GET", "/admin/lineup/groups/999/categories"+in, nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("no such group: status %d", rec.Code)
	}

	// The API makes one too.
	rec = ta.apiDo("POST", "lineups", `{"name": "Theirs", "own_groups": true}`, c, h...)
	var theirs apiLineup
	if err := json.Unmarshal(rec.Body.Bytes(), &theirs); err != nil || rec.Code != http.StatusCreated || !theirs.OwnGroups ||
		theirs.Rules != 0 {
		t.Errorf("the API, a new lineup: status %d, %s", rec.Code, rec.Body)
	}
}

// groupsIn returns a lineup's groups by name, as the API lists them; query
// names the lineup.
func (ta *testAdmin) groupsIn(c *http.Cookie, query string) map[string]apiGroup {
	ta.t.Helper()
	rec := ta.apiDo("GET", "lineup/groups"+query, "", c)
	var body struct{ Groups []apiGroup }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		ta.t.Fatalf("groups: status %d, %s", rec.Code, rec.Body)
	}
	out := map[string]apiGroup{}
	for _, g := range body.Groups {
		out[g.Name] = g
	}
	return out
}

// A lineup's pages say which accounts see it, and through what.
func TestLineupSeenBy(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withLineup()
	ctx := context.Background()
	page := ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "the lineup page", page, `Seen by <a href="/admin/accounts/1">Household</a>, through nothing yet.`)
	for _, mac := range []string{"00:1A:79:00:00:01", "00:1A:79:00:00:02", "00:1A:79:00:00:03"} {
		if _, err := ta.st.ApproveDevice(ctx, mac, store.HouseholdAccount, "", ta.clock); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ta.st.SeeDevice(ctx, "00:1A:79:00:00:04", "MAG322", ta.clock); err != nil { // pending
		t.Fatal(err)
	}
	if _, err := ta.st.AdoptSecret(ctx, "00:1A:79:00:00:01", "x", ta.clock); err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{M3U: true, Xtream: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.st.NewAccountToken(ctx, store.HouseholdAccount); err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetAccountLogin(ctx, store.HouseholdAccount, "family", "x"); err != nil {
		t.Fatal(err)
	}
	page = ta.do("GET", "/admin/lineup/rules", nil, c).Body.String()
	mustContain(t, "the rules page, outputs off", page, `Household</a>, through 1 box and 2 Stalker clients.`)
	if err := ta.st.SetOutputs(ctx, true); err != nil {
		t.Fatal(err)
	}
	page = ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "the lineup page, outputs on", page,
		`Household</a>, through 1 box, 2 Stalker clients, its playlist and guide and its Xtream login.`)
	l := ta.lineupsByName(c)["Default"]
	if len(l.Viewers) != 1 || l.Viewers[0] != (apiViewer{AccountID: 1, Account: "Household", Boxes: 1, StalkerClients: 2,
		Playlist: true, XtreamLogin: true}) {
		t.Errorf("the API: %+v", l.Viewers)
	}
}
