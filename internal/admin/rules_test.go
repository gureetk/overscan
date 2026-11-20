// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/rules"
)

// rulesOf returns a lineup's rules, as the API lists them.
func (ta *testAdmin) rulesOf(c *http.Cookie, query string) []apiRule {
	ta.t.Helper()
	rec := ta.apiDo("GET", "lineup/rules"+query, "", c)
	var body struct{ Rules []apiRule }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		ta.t.Fatalf("rules: status %d, %s", rec.Code, rec.Body)
	}
	return body.Rules
}

// The Rules page adds rules, which act on the channels at once, saves,
// moves, turns off and on, and removes them; a rule that cannot be saved
// comes back in its form (D107).
func TestRulesPage(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}

	rec := ta.do("GET", "/admin/lineup/rules", nil, c)
	mustContain(t, "the empty page", rec.Body.String(), "No rules yet", "Add a rule", `aria-current="page">Rules`,
		`<option value="exclude">Hide them</option>`, `<option value="0">Any source</option>`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if page := ta.do("GET", "/admin/lineup", nil, c).Body.String(); !strings.Contains(page, `href="/admin/lineup/rules">Rules</a>`) {
		t.Errorf("the channels page has no link to the rules:\n%s", page)
	}

	// Hide the news.
	rec = ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, "", "action", "exclude", "group", "news"), c, hx...)
	mustContain(t, "the page after adding", rec.Body.String(), "Added the rule. It matches 2 channels.",
		"Hides channels in categories like “news”.", "It matches 2 channels.")
	if rec.Code != http.StatusOK || !strings.Contains(logs.String(), `msg="a lineup's rule added in the admin" lineup=Default action=exclude`) {
		t.Errorf("add: status %d, log:\n%s", rec.Code, logs.String())
	}
	chs, _ := ta.channels(c, "")
	if !chs["Alpha"].Hidden || !chs["Charlie"].Hidden || chs["Bravo"].Hidden || chs["Alpha"].Visibility != "group" ||
		fmt.Sprint(chs["Alpha"].Rules) != "[exclude]" || len(chs["Bravo"].Rules) != 0 {
		t.Errorf("after hiding the news: %+v", chs)
	}
	page := ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "the channels page", page, `title="A rule hides it.">`, `Rules <span class="count quiet">1</span>`)

	// A rule that cannot be saved comes back in its form, with what was sent.
	rec = ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, "", "action", "regroup", "name", "Bra*", "to_group", " "), c, hx...)
	mustContain(t, "a bad rule", rec.Body.String(), "Name the group to move the channels to.", `id="new-name" name="name" maxlength="200" value="Bra*"`,
		`<option value="regroup" selected>`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a bad rule: status %d", rec.Code)
	}

	// Rename Bravo, then move the rule up.
	rec = ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, "", "action", "rename", "name", "Bravo", "rename", "name", "new_name", "Bravo Sport"), c, hx...)
	mustContain(t, "a rename", rec.Body.String(), "Names channels named like “Bravo” “Bravo Sport”.", "It matches 1 channel.")
	list := ta.rulesOf(c, "")
	if len(list) != 2 || list[1].Name != "Bravo Sport" {
		t.Fatalf("the rules: %+v", list)
	}
	if chs, _ := ta.channels(c, ""); chs["Bravo"].Name != "Bravo Sport" || chs["Bravo"].Renamed {
		t.Errorf("Bravo after the rename: %+v", chs["Bravo"])
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/rules/%d/up", list[1].ID), lineupForm(csrf, ""), c, hx...)
	if list := ta.rulesOf(c, ""); rec.Code != http.StatusOK || list[0].Action != rules.Rename || list[0].Position != 1 ||
		!strings.Contains(rec.Body.String(), "Moved the rule.") {
		t.Errorf("up: status %d, %+v", rec.Code, list)
	}

	// Turn the first off: Alpha and Charlie come back; and on again.
	hide := list[0]
	res := ta.do("POST", fmt.Sprintf("/admin/lineup/rules/%d/off", hide.ID), lineupForm(csrf, ""), c).Result()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/lineup/rules" {
		t.Errorf("off without htmx: status %d, Location %q", res.StatusCode, res.Header.Get("Location"))
	}
	if chs, _ := ta.channels(c, ""); chs["Alpha"].Hidden {
		t.Errorf("Alpha with the rule off: %+v", chs["Alpha"])
	}
	rec = ta.do("GET", "/admin/lineup/rules", nil, c)
	mustContain(t, "the page with a rule off", rec.Body.String(), "Off: it does nothing until it is on again. It matches 2 channels.",
		"Turn it on", `<input type="hidden" name="off" value="1">`)
	// Saving it keeps it off.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/rules/%d", hide.ID), lineupForm(csrf, "", "action", "exclude", "group", "sports", "off", "1"), c, hx...)
	if list := ta.rulesOf(c, ""); rec.Code != http.StatusOK || !list[1].Off || list[1].Match.Group != "sports" || list[1].Matches != 2 {
		t.Errorf("save: status %d, %+v", rec.Code, list)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/rules/%d/on", hide.ID), lineupForm(csrf, ""), c, hx...)
	if chs, _ := ta.channels(c, ""); rec.Code != http.StatusOK || !chs["Bravo"].Hidden || !chs["Delta"].Hidden || chs["Alpha"].Hidden {
		t.Errorf("on: status %d, %+v", rec.Code, chs)
	}

	// A hand edit wins over a rule.
	bravo := chs["Bravo"]
	if rec := ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", bravo.ID), `{"name": "B", "visibility": "shown"}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("hand edit: status %d, %s", rec.Code, rec.Body)
	}
	if chs, _ := ta.channels(c, ""); chs["Bravo"].Name != "B" || chs["Bravo"].Hidden || fmt.Sprint(chs["Bravo"].Rules) != "[exclude rename]" {
		t.Errorf("Bravo with hand edits: %+v", chs["Bravo"])
	}
	page = ta.do("GET", "/admin/lineup", nil, c).Body.String()
	if strings.Contains(page, `title="A rule hides it and names it.">`) {
		t.Errorf("the row says that rules do what hand edits undo")
	}

	// Remove it: Bravo and Delta come back.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/rules/%d/remove", hide.ID), lineupForm(csrf, ""), c, hx...)
	if chs, _ := ta.channels(c, ""); rec.Code != http.StatusOK || chs["Delta"].Hidden || len(ta.rulesOf(c, "")) != 1 ||
		!strings.Contains(rec.Body.String(), "Removed the rule. What it did to channels is undone.") {
		t.Errorf("remove: status %d, %+v", rec.Code, chs)
	}
	for _, path := range []string{"/admin/lineup/rules/999/remove", "/admin/lineup/rules/999/up"} {
		if rec := ta.do("POST", path, lineupForm(csrf, ""), c, hx...); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "There is no such rule.") {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
	if rec := ta.do("POST", fmt.Sprintf("/admin/lineup/rules/%d/sideways", list[0].ID), lineupForm(csrf, ""), c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown action: status %d", rec.Code)
	}

	// Another lineup's rules are its own.
	rec = ta.apiDo("POST", "lineups", `{"name": "Kids"}`, c, h...)
	var kids apiLineup
	if err := json.Unmarshal(rec.Body.Bytes(), &kids); err != nil || kids.Rules != 1 {
		t.Fatalf("copy: %s", rec.Body)
	}
	back := fmt.Sprintf("lineup=%d", kids.ID)
	rec = ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, back, "action", "renumber", "from", "500"), c, hx...)
	if rec.Code != http.StatusOK || len(ta.rulesOf(c, "?"+back)) != 2 || len(ta.rulesOf(c, "")) != 1 {
		t.Errorf("a rule in Kids: status %d", rec.Code)
	}
	if chs, _ := ta.channels(c, "?"+back); chs["Alpha"].Number == nil || *chs["Alpha"].Number != 500 {
		t.Errorf("Kids' Alpha: %+v", chs["Alpha"])
	}
	if chs, _ := ta.channels(c, ""); *chs["Alpha"].Number != 1 {
		t.Errorf("the first's Alpha: %+v", chs["Alpha"])
	}
	page = ta.do("GET", "/admin/lineup/rules?"+back, nil, c).Body.String()
	mustContain(t, "Kids' rules", page, `name="back" value="`+back+`"`, `action="/admin/lineup/rules" class="lineup-pick"`,
		"Numbers every channel from 500.")
}

func TestRulesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	rec := ta.apiDo("POST", "lineup/rules", `{"match": {"name": "*a*"}, "action": "regroup", "group": "With an A"}`, c, h...)
	var r apiRule
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || rec.Code != http.StatusCreated || r.ID == 0 || r.Position != 1 ||
		r.Matches != 4 || r.Group != "With an A" {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	if gs := ta.groups(c); gs["With an A"].Channels != 4 {
		t.Errorf("groups: %+v", gs)
	}
	all, err := ta.st.Sources(context.Background())
	if err != nil || len(all) != 1 {
		t.Fatalf("sources: %v, %v", all, err)
	}
	src := all[0]
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "lineup/rules", `{"action": "regroup"}`, http.StatusBadRequest, "bad_rule"},
		{"POST", "lineup/rules", `{"action": "jump"}`, http.StatusBadRequest, "bad_rule"},
		{"POST", "lineup/rules", `{"match": {"name": "/(/"}, "action": "exclude"}`, http.StatusBadRequest, "bad_rule"},
		{"POST", "lineup/rules", `{"match": {"source_id": 999}, "action": "exclude"}`, http.StatusBadRequest, "bad_rule"},
		{"POST", "lineup/rules", `{"action": "logo", "logo": "ftp://logos.example/a.png"}`, http.StatusBadRequest, "bad_rule"},
		{"POST", "lineup/rules", `{"action": "exclude", "colour": "red"}`, http.StatusBadRequest, "bad_json"},
		{"POST", "lineup/rules?lineup=999", `{"action": "exclude"}`, http.StatusNotFound, "not_found"},
		{"PUT", "lineup/rules/999", `{"action": "exclude"}`, http.StatusNotFound, "not_found"},
		{"DELETE", "lineup/rules/999", "", http.StatusNotFound, "not_found"},
		{"POST", "lineup/rules/999/move", `{"position": 1}`, http.StatusNotFound, "not_found"},
		{"POST", fmt.Sprintf("lineup/rules/%d/move", r.ID), `{"position": 0}`, http.StatusBadRequest, "bad_position"},
	} {
		rec := ta.apiDo(tc.method, tc.path, tc.body, c, h...)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%s %s %s: status %d, %s", tc.method, tc.path, tc.body, rec.Code, rec.Body)
		}
	}
	// Replace it with one for the source, add another, and move that first.
	rec = ta.apiDo("PUT", fmt.Sprintf("lineup/rules/%d", r.ID), fmt.Sprintf(`{"match": {"source_id": %d, "quality": "hd"}, "action": "exclude"}`, src.ID), c, h...)
	r = apiRule{}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || rec.Code != http.StatusOK || r.Match.Quality != "HD" || r.Matches != 0 || r.Group != "" {
		t.Errorf("replace: status %d, %s", rec.Code, rec.Body)
	}
	if gs := ta.groups(c); gs["With an A"].ID != 0 {
		t.Errorf("the rule's group stays: %+v", gs)
	}
	rec = ta.apiDo("POST", "lineup/rules", `{"action": "logo", "logo": "https://logos.example/all.png"}`, c, h...)
	var logo apiRule
	if err := json.Unmarshal(rec.Body.Bytes(), &logo); err != nil || rec.Code != http.StatusCreated || logo.Position != 2 {
		t.Fatalf("add a logo: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("POST", fmt.Sprintf("lineup/rules/%d/move", logo.ID), `{"position": 1}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &logo); err != nil || rec.Code != http.StatusOK || logo.Position != 1 {
		t.Errorf("move: status %d, %s", rec.Code, rec.Body)
	}
	if chs, _ := ta.channels(c, ""); fmt.Sprint(chs["Alpha"].Rules) != "[logo]" {
		t.Errorf("Alpha: %+v", chs["Alpha"])
	}
	if l := ta.lineupsByName(c)["Default"]; l.Rules != 2 {
		t.Errorf("the lineup: %+v", l)
	}
	if rec := ta.apiDo("DELETE", fmt.Sprintf("lineup/rules/%d", logo.ID), "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("remove: status %d, %s", rec.Code, rec.Body)
	}
	if list := ta.rulesOf(c, ""); len(list) != 1 || list[0].ID != r.ID || list[0].Position != 1 {
		t.Errorf("the rules left: %+v", list)
	}
	// Without the CSRF header, nothing changes.
	if rec := ta.apiDo("POST", "lineup/rules", `{"action": "exclude"}`, c); rec.Code != http.StatusForbidden {
		t.Errorf("without CSRF: status %d", rec.Code)
	}
	// Removing the source removes the rules that name it.
	if rec := ta.apiDo("DELETE", fmt.Sprintf("sources/%d", src.ID), "", c, h...); rec.Code != http.StatusNoContent {
		t.Fatalf("remove the source: status %d, %s", rec.Code, rec.Body)
	}
	if list := ta.rulesOf(c, ""); len(list) != 0 {
		t.Errorf("the rules after the source went: %+v", list)
	}
}

func TestDescribeRule(t *testing.T) {
	sources := map[int64]string{1: "main"}
	for _, tc := range []struct {
		rule rules.Rule
		want string
	}{
		{rules.Rule{Action: rules.Exclude}, "Hides every channel."},
		{rules.Rule{Match: rules.Match{Source: 1, Name: "*Backup*"}, Action: rules.Exclude},
			"Hides channels from main and named like “*Backup*”."},
		{rules.Rule{Match: rules.Match{Source: 2, Quality: "4K"}, Action: rules.Include},
			"Shows channels from a source that is gone and in 4K, even in a hidden group."},
		{rules.Rule{Match: rules.Match{Group: "ZZ*", Guide: "alpha*"}, Action: rules.Rename, Replace: "ZZ: "},
			"Takes “ZZ: ” out of the names of channels in categories like “ZZ*” and with guide IDs like “alpha*”."},
		{rules.Rule{Action: rules.Rename, Replace: "HD", With: "High"}, "Replaces “HD” with “High” in every channel's name."},
		{rules.Rule{Action: rules.Rename, Name: "News"}, "Names every channel “News”."},
		{rules.Rule{Action: rules.Regroup, Group: "Sport"}, "Moves every channel to the group “Sport”."},
		{rules.Rule{Action: rules.Renumber, From: 100}, "Numbers every channel from 100."},
		{rules.Rule{Action: rules.Logo, Logo: "https://logos.example/a.png"}, "Gives every channel the logo at https://logos.example/a.png."},
		{rules.Rule{Action: rules.Guide, Channel: "Alpha"}, "Gives every channel the guide of the channel named “Alpha”."},
		{rules.Rule{Match: rules.Match{Name: "* +1"}, Action: rules.Guide, Replace: "+1"},
			"Gives channels named like “* +1” the guide of the channel with the same name without “+1”."},
		{rules.Rule{Action: rules.Guide, Replace: "East", With: "West"},
			"Gives every channel the guide of the channel with the same name, “East” replaced by “West”."},
		{rules.Rule{Action: rules.Shift, Minutes: 60}, "Shows the guide of every channel 60 minutes later."},
		{rules.Rule{Action: rules.Shift, Minutes: -1}, "Shows the guide of every channel 1 minute earlier."},
	} {
		if got := describeRule(tc.rule, sources); got != tc.want {
			t.Errorf("%+v:\n got %s\nwant %s", tc.rule, got, tc.want)
		}
	}
}

// The Rules page's form gives a channel another's guide and moves its times
// (D108), and the channels page says so.
func TestGuideRulesPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	rec := ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, "", "action", "guide", "name", "Bravo", "guide_from", "channel",
		"guide_channel", "alpha", "guide_replace", "ignored"), c, hx...)
	mustContain(t, "a guide rule", rec.Body.String(), "Gives channels named like “Bravo” the guide of the channel named “alpha”.",
		"It matches 1 channel.")
	rec = ta.do("POST", "/admin/lineup/rules", lineupForm(csrf, "", "action", "timeshift", "name", "Bravo", "minutes", "90",
		"when", "earlier"), c, hx...)
	mustContain(t, "a time shift", rec.Body.String(), "Shows the guide of channels named like “Bravo” 90 minutes earlier.",
		`value="90"`, `<option value="earlier" selected>earlier</option>`)
	list := ta.rulesOf(c, "")
	if len(list) != 2 || list[0].Channel != "alpha" || list[0].Replace != "" || list[1].Minutes != -90 {
		t.Fatalf("the rules: %+v", list)
	}
	chs, _ := ta.channels(c, "")
	if fmt.Sprint(chs["Bravo"].Rules) != "[guide timeshift]" || len(chs["Alpha"].Rules) != 0 {
		t.Errorf("Bravo: %+v", chs["Bravo"])
	}
	page := ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "the channels page", page, `title="A rule gives it the guide of Alpha and shows its guide 90 minutes earlier.">`)
	// A guide with its name changed, through the API.
	rec = ta.apiDo("POST", "lineup/rules", `{"match": {"name": "Charlie"}, "action": "guide", "replace": "/^C.*/", "with": "Delta"}`, c, h...)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"with": "Delta"`) {
		t.Errorf("the API: status %d, %s", rec.Code, rec.Body)
	}
	if chs, _ := ta.channels(c, ""); fmt.Sprint(chs["Charlie"].Rules) != "[guide]" {
		t.Errorf("Charlie: %+v", chs["Charlie"])
	}
	for _, body := range []string{`{"action": "guide"}`, `{"action": "timeshift", "minutes": 0}`, `{"action": "timeshift", "minutes": 2000}`} {
		if rec := ta.apiDo("POST", "lineup/rules", body, c, h...); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d", body, rec.Code)
		}
	}
}
