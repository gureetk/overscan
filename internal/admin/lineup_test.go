// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// withLineup gives the test admin a source, main, with channels a to d in
// News and Sports, and signs in.
func (ta *testAdmin) withLineup() (*http.Cookie, []string) {
	ta.t.Helper()
	ctx := context.Background()
	srcID, err := ta.st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1})
	if err != nil {
		ta.t.Fatal(err)
	}
	var chs []store.CatalogChannel
	for _, spec := range []string{"Alpha/News", "Bravo/Sports", "Charlie/News", "Delta/Sports"} {
		name, category, _ := strings.Cut(spec, "/")
		chs = append(chs, store.CatalogChannel{IdentityKey: name, Name: name, Category: category, StreamURL: "http://streams.example/" + name})
	}
	if _, err := ta.st.ApplyCatalog(ctx, srcID, chs, ta.clock); err != nil {
		ta.t.Fatal(err)
	}
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	return c, []string{csrfHeader, csrf}
}

// groups returns the lineup's groups by name.
func (ta *testAdmin) groups(c *http.Cookie) map[string]apiGroup {
	ta.t.Helper()
	rec := ta.apiDo("GET", "lineup/groups", "", c)
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

// channels returns the lineup's channels, as the API lists them with the
// query, by their source's name.
func (ta *testAdmin) channels(c *http.Cookie, query string) (map[string]apiEntry, int) {
	ta.t.Helper()
	rec := ta.apiDo("GET", "lineup/channels"+query, "", c)
	var body struct {
		Total    int
		Channels []apiEntry
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		ta.t.Fatalf("channels: status %d, %s", rec.Code, rec.Body)
	}
	out := map[string]apiEntry{}
	for _, e := range body.Channels {
		out[e.ChannelName] = e
	}
	return out, body.Total
}

// seen returns what devices see: "number name group" for each channel.
func (ta *testAdmin) seen() []string {
	ta.t.Helper()
	cs, err := ta.st.LineupChannels(context.Background(), store.DefaultLineupID)
	if err != nil {
		ta.t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%d %s %s", c.Number, c.Name, c.Group))
	}
	return out
}

func TestLineupGroupsAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	gs := ta.groups(c)
	news, sports := gs["News"], gs["Sports"]
	if news.Channels != 2 || news.Shown != 2 || news.FirstNumber == nil || *news.FirstNumber != 1 ||
		len(news.Categories) != 1 || news.Categories[0].ID == nil || news.Categories[0].Source != "main" {
		t.Errorf("News %+v", news)
	}

	rec := ta.apiDo("POST", "lineup/groups", `{"name": " Favourites "}`, c, h...)
	var made apiGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil || rec.Code != http.StatusCreated || made.Name != "Favourites" || !made.Edited {
		t.Fatalf("create: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "lineup/groups", `{"name": "News"}`, c, h...), http.StatusConflict, "name_taken")
	apiErrorOf(t, ta.apiDo("POST", "lineup/groups", `{"name": " "}`, c, h...), http.StatusBadRequest, "bad_name")
	apiErrorOf(t, ta.apiDo("POST", "lineup/groups", `{"name": "`+strings.Repeat("x", 101)+`"}`, c, h...), http.StatusBadRequest, "bad_name")

	path := fmt.Sprintf("lineup/groups/%d", sports.ID)
	rec = ta.apiDo("PATCH", path, `{"name": "Sport", "hidden": true}`, c, h...)
	var changed apiGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil || changed.Name != "Sport" || !changed.Hidden || changed.Shown != 0 {
		t.Errorf("rename and hide: status %d, %s", rec.Code, rec.Body)
	}
	if got := ta.seen(); len(got) != 2 {
		t.Errorf("devices see %v with Sport hidden", got)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/groups/9999", `{"hidden": false}`, c, h...), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"hidden": false}`, c), http.StatusForbidden, "csrf")

	// Sport's category goes to Favourites, then News merges into it.
	rec = ta.apiDo("PUT", fmt.Sprintf("lineup/categories/%d", *sports.Categories[0].ID), fmt.Sprintf(`{"group_id": %d}`, made.ID), c, h...)
	if rec.Code != http.StatusOK {
		t.Errorf("move category: status %d, %s", rec.Code, rec.Body)
	}
	if rec = ta.apiDo("POST", fmt.Sprintf("lineup/groups/%d/merge", news.ID), fmt.Sprintf(`{"into": %d}`, made.ID), c, h...); rec.Code != http.StatusOK {
		t.Errorf("merge: status %d, %s", rec.Code, rec.Body)
	}
	gs = ta.groups(c)
	if len(gs) != 2 || gs["Favourites"].Channels != 4 || len(gs["Favourites"].Categories) != 2 {
		t.Errorf("after moving and merging: %+v", gs)
	}

	// Number the group from 101.
	rec = ta.apiDo("POST", fmt.Sprintf("lineup/groups/%d/number", made.ID), `{"from": 101}`, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"numbered": 4`) {
		t.Errorf("number: status %d, %s", rec.Code, rec.Body)
	}
	if got, want := strings.Join(ta.seen(), ", "), "101 Alpha Favourites, 102 Bravo Favourites, 103 Charlie Favourites, 104 Delta Favourites"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	apiErrorOf(t, ta.apiDo("POST", fmt.Sprintf("lineup/groups/%d/number", made.ID), `{"from": 0}`, c, h...), http.StatusBadRequest, "bad_number")

	apiErrorOf(t, ta.apiDo("DELETE", fmt.Sprintf("lineup/groups/%d", made.ID), "", c, h...), http.StatusConflict, "group_not_empty")
	if rec = ta.apiDo("DELETE", path, "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("delete the emptied Sport: status %d, %s", rec.Code, rec.Body)
	}
}

func TestLineupChannelsAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	gs := ta.groups(c)

	all, total := ta.channels(c, "")
	if total != 4 || all["Alpha"].Number == nil || *all["Alpha"].Number != 1 || *all["Alpha"].Group != "News" || all["Alpha"].Visibility != "group" {
		t.Fatalf("channels %+v, total %d", all, total)
	}
	news, total := ta.channels(c, fmt.Sprintf("?group=%d", gs["News"].ID))
	if total != 2 || len(news) != 2 {
		t.Errorf("News: %d of %d", len(news), total)
	}
	if _, total = ta.channels(c, "?q=rav"); total != 1 {
		t.Errorf("search: %d", total)
	}
	if page, total := ta.channels(c, "?limit=1&offset=1"); total != 4 || len(page) != 1 || page["Bravo"].ID == 0 {
		t.Errorf("paging: %+v of %d", page, total)
	}
	apiErrorOf(t, ta.apiDo("GET", "lineup/channels?limit=0", "", c), http.StatusBadRequest, "bad_query")
	apiErrorOf(t, ta.apiDo("GET", "lineup/channels?group=x", "", c), http.StatusBadRequest, "bad_query")

	alpha := fmt.Sprintf("lineup/channels/%d", all["Alpha"].ID)
	rec := ta.apiDo("PATCH", alpha, `{"name": "Alpha One", "number": 3, "visibility": "shown"}`, c, h...)
	var e apiEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if e.Name != "Alpha One" || !e.Renamed || *e.Number != 3 || !e.Pinned || e.Visibility != "shown" {
		t.Errorf("Alpha %+v", e)
	}
	if got, want := strings.Join(ta.seen(), ", "), "1 Charlie News, 2 Bravo Sports, 3 Alpha One News, 4 Delta Sports"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}

	// null gives a field back to Overscan. Alpha wants 1, which Charlie, a
	// listed channel, holds since the swap, so it keeps 3, unpinned.
	rec = ta.apiDo("PATCH", alpha, `{"name": null, "number": null, "group_id": null}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Name != "Alpha" || e.Renamed || e.Pinned || *e.Number != 3 {
		t.Errorf("reset: status %d, %s", rec.Code, rec.Body)
	}

	for body, code := range map[string]string{
		`{}`:                         "bad_json",
		`{"nickname": "x"}`:          "bad_json",
		`{"number": 0}`:              "bad_json",
		`{"number": "one"}`:          "bad_json",
		`{"visibility": "maybe"}`:    "bad_json",
		`{"group_id": -2}`:           "bad_json",
		`{"name": "` + "\x07" + `"}`: "bad_json",
		`{"number": 1000000}`:        "bad_number",
		`{"name": "\u0007bell"}`:     "bad_name",
	} {
		rec := ta.apiDo("PATCH", alpha, body, c, h...)
		var got apiErrorBody
		_ = json.Unmarshal(rec.Body.Bytes(), &got)
		if rec.Code != http.StatusBadRequest || got.Error != code {
			t.Errorf("%s: status %d, %+v; want %s", body, rec.Code, got, code)
		}
	}
	apiErrorOf(t, ta.apiDo("PATCH", alpha, `{"group_id": 9999}`, c, h...), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/channels/9999", `{"visibility": "hidden"}`, c, h...), http.StatusNotFound, "not_found")

	// Several at once.
	ids := fmt.Sprintf("[%d, %d]", all["Bravo"].ID, all["Delta"].ID)
	rec = ta.apiDo("PATCH", "lineup/channels", `{"ids": `+ids+`, "visibility": "hidden", "group_id": `+fmt.Sprint(gs["News"].ID)+`}`, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changed": 2`) {
		t.Errorf("bulk: status %d, %s", rec.Code, rec.Body)
	}
	if got, want := strings.Join(ta.seen(), ", "), "1 Charlie News, 3 Alpha News"; got != want {
		t.Errorf("devices see %q, want %q", got, want)
	}
	if after, _ := ta.channels(c, ""); !after["Bravo"].Moved || after["Bravo"].Visibility != "hidden" || !after["Bravo"].Hidden {
		t.Errorf("Bravo %+v", after["Bravo"])
	}
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/channels", `{"ids": `+ids+`, "name": "x"}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/channels", `{"visibility": "shown"}`, c, h...), http.StatusBadRequest, "no_channels")
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/channels", `{"ids": [9999], "visibility": "shown"}`, c, h...), http.StatusNotFound, "not_found")
}
