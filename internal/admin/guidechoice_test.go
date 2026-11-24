// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/store"
)

// withChoices gives the test admin withGuides' lineup, Alpha's programmes
// from its source's guide, and a guide of the household's own that lists
// Bravo's and Other's channels and fills Bravo by name; and the fake
// reader suggestions for Charlie and a list to search. It returns the
// source's channels by name and the guide's channels by guide ID.
func (ta *testAdmin) withChoices() (*http.Cookie, string, *fakeGuides, map[string]int64, map[string]int64) {
	ta.t.Helper()
	c, csrf, f := ta.withGuides()
	ctx := context.Background()
	chans := map[string]int64{}
	gcs, err := ta.st.GuideChannels(ctx, 1)
	if err != nil {
		ta.t.Fatal(err)
	}
	for _, ch := range gcs {
		chans[ch.Name] = ch.ID
	}
	now := ta.clock
	w, err := ta.st.BeginGuide(ctx, 1)
	if err != nil {
		ta.t.Fatal(err)
	}
	for _, p := range []store.GuideProgramme{
		{ChannelID: chans["Alpha"], Start: now.Add(-30 * time.Minute), Stop: now.Add(30 * time.Minute), Title: "Alpha Morning"},
		{ChannelID: chans["Alpha"], Start: now.Add(30 * time.Minute), Stop: now.Add(90 * time.Minute), Title: "Alpha Noon"},
	} {
		if err := w.Add(ctx, p); err != nil {
			ta.t.Fatal(err)
		}
	}
	if err := w.Commit(ctx, now, "own"); err != nil {
		ta.t.Fatal(err)
	}
	g, err := ta.st.AddGuide(ctx, store.GuideSettings{Name: "extra", Address: secretGuide}, now)
	if err != nil {
		ta.t.Fatal(err)
	}
	r, err := ta.st.BeginGuideRead(ctx, g)
	if err != nil {
		ta.t.Fatal(err)
	}
	ids, err := r.ListChannels(ctx, []store.GuideListing{{XMLTVID: "bravo.example", Names: []string{"Bravo TV"}},
		{XMLTVID: "other.example", Names: []string{"Other"}}}, now)
	if err != nil {
		ta.t.Fatal(err)
	}
	if err := r.Add(ctx, store.GuideProgramme{ChannelID: ids["bravo.example"], Start: now.Add(-10 * time.Minute),
		Stop: now.Add(50 * time.Minute), Title: "Bravo Match"}); err != nil {
		ta.t.Fatal(err)
	}
	if err := r.Commit(ctx, now, "x", store.GuideRead{Listed: 2, Kept: []int64{ids["bravo.example"]},
		Matches: []store.GuideMatch{{ChannelID: chans["Bravo"], GuideChannelID: ids["bravo.example"], ByName: true}}}); err != nil {
		ta.t.Fatal(err)
	}
	other := guide.Option{GuideChannelID: ids["other.example"], Names: []string{"Other"}, XMLTVID: "other.example", GuideID: g, Guide: "extra"}
	alpha := guide.Option{ChannelID: chans["Alpha"], Names: []string{"Alpha"}, SourceID: 1, Source: "main"}
	f.options = []guide.Option{other, alpha}
	f.suggest = map[int64][]guide.Option{chans["Charlie"]: {other, alpha}}
	return c, csrf, f, chans, ids
}

func TestGuideChoicePages(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f, chans, ids := ta.withChoices()
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}

	// The list, without a guide first: Charlie and Delta, with
	// suggestions for Charlie.
	body := ta.do("GET", "/admin/guides/channels", nil, c).Body.String()
	mustContain(t, "the list", body, "<h1>Channels and their guides</h1>", `title="Channels without a guide">No guide <span class="count quiet">2</span>`,
		"By name <span class=\"count quiet\">1</span>", "Charlie", "Delta", "Use Other", "Use Alpha", "the guide of main&#39;s channel",
		`href="/admin/guides/channels/`+fmt.Sprint(chans["Charlie"])+`?back=%2fadmin%2fguides%2fchannels%3fshow%3dwithout"`)
	if strings.Contains(body, ">Alpha</span>") || strings.Contains(body, "s3cret") {
		t.Error("the list shows Alpha, or a guide's address")
	}
	body = ta.do("GET", "/admin/guides/channels?show=by_name", nil, c).Body.String()
	mustContain(t, "matched by name", body, "Bravo", "extra&#39;s Bravo TV, matched by its name.", "Bravo Match")
	body = ta.do("GET", "/admin/guides/channels?show=without&find=delt", nil, c).Body.String()
	mustContain(t, "found", body, "Delta", "Show every one")
	if strings.Contains(body, "Charlie") {
		t.Error("finding Delta shows Charlie")
	}

	// Use a suggestion from the list: Charlie takes Other's guide.
	rec := ta.do("POST", fmt.Sprintf("/admin/guides/channels/%d", chans["Charlie"]), url.Values{"csrf": {csrf},
		"list": {"show=without"}, "choice": {fmt.Sprintf("guide:%d", ids["other.example"])}}, c, hx...)
	mustContain(t, "after using one", rec.Body.String(), "Charlie shows extra&#39;s Other.", "Overscan reads its programmes from extra now",
		`No guide <span class="count quiet">1</span>`)
	if rec.Header().Get("HX-Replace-Url") != "/admin/guides/channels?show=without" {
		t.Errorf("the address after a choice: %q", rec.Header().Get("HX-Replace-Url"))
	}
	if f.changed() != 1 {
		t.Errorf("%d changes, want 1", f.changed())
	}
	if g, _ := ta.st.ChannelGuideOf(ctx, chans["Charlie"]); !g.Chosen || g.GuideChannelID != ids["other.example"] {
		t.Errorf("Charlie's guide %+v", g)
	}
	// Without htmx, the browser goes back to the list.
	rec = ta.do("POST", fmt.Sprintf("/admin/guides/channels/%d", chans["Delta"]), url.Values{"csrf": {csrf},
		"list": {"show=without&find=delt"}, "choice": {fmt.Sprintf("channel:%d", chans["Alpha"])}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/guides/channels?find=delt&show=without" {
		t.Errorf("without htmx: %d, %q", rec.Code, rec.Header().Get("Location"))
	}
	body = ta.do("GET", "/admin/guides/channels?show=by_hand", nil, c).Body.String()
	mustContain(t, "chosen by hand", body, "Charlie", "extra&#39;s Other, chosen by hand.", "The guide of Alpha, from main, chosen by hand.",
		"Alpha Morning", "Undo")

	// A channel's own page: where its programmes come from, suggestions,
	// a search, and what is on.
	page := fmt.Sprintf("/admin/guides/channels/%d", chans["Delta"])
	body = ta.do("GET", page+"?back="+url.QueryEscape("/admin/lineup?edit=4"), nil, c).Body.String()
	mustContain(t, "Delta's page", body, "<h1>The guide of Delta</h1>", "From main, in its category Sports.", "The guide of Alpha, from main, chosen by hand.",
		"Alpha Morning", "Alpha Noon", "Find it as before", "No guide", "Back to Live TV", `href="/admin/lineup?edit=4"`)
	body = ta.do("GET", page+"?find=oth", nil, c).Body.String()
	mustContain(t, "a search", body, `<div id="guide-found">`, "<strong>Other</strong>", "Overscan reads its programmes once you choose it.", "Use this")
	rec = ta.do("GET", page+"?find=nothing", nil, c, "HX-Request", "true", "HX-Target", "guide-found")
	if b := rec.Body.String(); !strings.HasPrefix(strings.TrimSpace(b), `<div id="guide-found">`) || !strings.Contains(b, "Nothing found with") ||
		strings.Contains(b, "<h1>") {
		t.Errorf("the search's fragment: %s", b)
	}
	// None, then back to before.
	rec = ta.do("POST", page, url.Values{"csrf": {csrf}, "choice": {"none"}, "back": {"/admin/lineup?edit=4"}}, c, hx...)
	mustContain(t, "none", rec.Body.String(), "Delta shows no guide.", "No guide, chosen by hand.", "Find it as before")
	rec = ta.do("POST", page, url.Values{"csrf": {csrf}, "choice": {"auto"}}, c, hx...)
	mustContain(t, "found as before", rec.Body.String(), "Delta&#39;s guide is found as before")
	if g, _ := ta.st.ChannelGuideOf(ctx, chans["Delta"]); g.Chosen {
		t.Errorf("Delta still has a choice: %+v", g)
	}
	for _, bad := range []string{"", "guide:0", "channel:x", "none:1", "maybe"} {
		if rec := ta.do("POST", page, url.Values{"csrf": {csrf}, "choice": {bad}}, c, hx...); rec.Code != http.StatusBadRequest {
			t.Errorf("choice %q: status %d", bad, rec.Code)
		}
	}
	if rec := ta.do("POST", page, url.Values{"csrf": {csrf}, "choice": {"guide:999"}}, c, hx...); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "There is no such channel to take a guide from") {
		t.Errorf("an unknown guide's channel: status %d", rec.Code)
	}
	if rec := ta.do("GET", "/admin/guides/channels/999", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown channel: status %d", rec.Code)
	}
	// A back address that leaves the admin is dropped.
	body = ta.do("GET", page+"?back="+url.QueryEscape("https://elsewhere.example/"), nil, c).Body.String()
	if strings.Contains(body, "elsewhere.example") {
		t.Error("the page links outside the admin")
	}

	// The panel on Live TV says where the channel's programmes come from.
	body = ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", entryOf(t, ta, c, "Charlie")), nil, c).Body.String()
	mustContain(t, "Charlie's panel", body, "<h3 id=\"guide-title\">Its guide</h3>", "extra&#39;s Other, chosen by hand.",
		"Choose its guide", fmt.Sprintf("/admin/guides/channels/%d?back=%%2Fadmin%%2Flineup", chans["Charlie"]))
	body = ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", entryOf(t, ta, c, "Alpha")), nil, c).Body.String()
	mustContain(t, "Alpha's panel", body, "Its source&#39;s guide.", "Alpha Morning")

	// Without a reader of guides, a choice can still be none or undone.
	ta.UseGuides(nil)
	body = ta.do("GET", page, nil, c).Body.String()
	mustContain(t, "without a reader", body, "it can only show no guide")
}

// entryOf returns the household lineup's entry for a channel's name.
func entryOf(t *testing.T, ta *testAdmin, c *http.Cookie, name string) int64 {
	t.Helper()
	chs, _ := ta.channels(c, "")
	return chs[name].ID
}

func TestGuideChoiceAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f, chans, ids := ta.withChoices()
	h := []string{csrfHeader, csrf}

	var list struct {
		Channels []apiChannelGuide
		Total    int
		Counts   map[string]int
	}
	rec := ta.apiDo("GET", "channel-guides?show=without&suggest=true", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || list.Total != 2 ||
		len(list.Channels) != 2 || list.Channels[0].Name != "Charlie" || len(list.Channels[0].Suggestions) != 2 ||
		list.Counts["without"] != 2 || list.Counts["by_name"] != 1 || list.Counts["by_hand"] != 0 {
		t.Fatalf("without: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("GET", "channel-guides?limit=1&offset=1", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Total != 4 || len(list.Channels) != 1 || list.Channels[0].Name != "Bravo" ||
		list.Channels[0].From != "guide" || list.Channels[0].Guide == nil || !list.Channels[0].Guide.ByName || list.Channels[0].Guide.Name != "Bravo TV" {
		t.Errorf("all from 1: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("GET", "channel-guides?show=some", "", c), http.StatusBadRequest, "bad_show")

	path := fmt.Sprintf("channel-guides/%d", chans["Charlie"])
	var one apiChannelGuide
	rec = ta.apiDo("GET", path, "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || one.From != "" || one.Chosen != nil || len(one.Suggestions) != 2 ||
		one.Suggestions[1].ChannelID != chans["Alpha"] || len(one.Suggestions[1].Programmes) != 2 || len(one.Suggestions[0].Programmes) != 0 {
		t.Errorf("Charlie: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PUT", path, fmt.Sprintf(`{"channel_id": %d}`, chans["Alpha"]), c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || rec.Code != http.StatusOK || one.From != "channel" || one.Chosen == nil ||
		one.Chosen.ChannelID != chans["Alpha"] || one.Chosen.Name != "Alpha" || len(one.Programmes) != 2 || one.Programmes[0].Title != "Alpha Morning" {
		t.Errorf("choose Alpha's: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PUT", path, fmt.Sprintf(`{"guide_channel_id": %d}`, ids["bravo.example"]), c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || one.From != "guide" || one.Guide == nil || !one.Guide.ByHand ||
		one.Chosen == nil || one.Chosen.GuideChannelID != ids["bravo.example"] || len(one.Programmes) != 1 {
		t.Errorf("choose Bravo TV: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PUT", path, `{"none": true}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || one.From != "none" || one.Chosen == nil || !one.Chosen.None {
		t.Errorf("choose none: status %d, %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{}`, `{"none": false}`, `{"none": true, "channel_id": 1}`, `{"channel_id": 0}`, `{"guide_channel_id": -1}`} {
		apiErrorOf(t, ta.apiDo("PUT", path, bad, c, h...), http.StatusBadRequest, "bad_choice")
	}
	apiErrorOf(t, ta.apiDo("PUT", path, fmt.Sprintf(`{"channel_id": %d}`, chans["Charlie"]), c, h...), http.StatusBadRequest, "no_such_guide")
	apiErrorOf(t, ta.apiDo("PUT", path, `{"guide_channel_id": 999}`, c, h...), http.StatusBadRequest, "no_such_guide")
	apiErrorOf(t, ta.apiDo("PUT", "channel-guides/999", `{"none": true}`, c, h...), http.StatusNotFound, "not_found")
	if rec := ta.apiDo("PUT", path, `{"none": true}`, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}

	if rec := ta.apiDo("DELETE", path, "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("take back: status %d, %s", rec.Code, rec.Body)
	}
	if rec := ta.apiDo("DELETE", path, "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("take back again: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("DELETE", "channel-guides/999", "", c, h...), http.StatusNotFound, "not_found")
	if f.changed() != 4 {
		t.Errorf("%d changes, want 4", f.changed())
	}

	var found struct{ Options []apiGuideOption }
	rec = ta.apiDo("GET", "guide-channels?find=alp", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &found); err != nil || len(found.Options) != 1 || found.Options[0].ChannelID != chans["Alpha"] ||
		found.Options[0].Source != "main" || len(found.Options[0].Programmes) != 2 {
		t.Errorf("find: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("GET", "guide-channels", "", c), http.StatusBadRequest, "bad_find")
}
