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
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// lineupsByName returns the lineups by name, as the API lists them.
func (ta *testAdmin) lineupsByName(c *http.Cookie) map[string]apiLineup {
	ta.t.Helper()
	rec := ta.apiDo("GET", "lineups", "", c)
	var body struct{ Lineups []apiLineup }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		ta.t.Fatalf("lineups: status %d, %s", rec.Code, rec.Body)
	}
	out := map[string]apiLineup{}
	for _, l := range body.Lineups {
		out[l.Name] = l
	}
	return out
}

// The Live TV page copies the lineup it shows, shows and edits another one
// at its address, renames it, and removes it once no account sees it; the
// Accounts page gives an account a lineup (D106).
func TestLineupsOnTheLiveTVPage(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	c, h := ta.withLineup()
	csrf := h[1]
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}

	rec := ta.do("GET", "/admin/lineup", nil, c)
	if !strings.Contains(rec.Body.String(), "Copy this lineup") || strings.Contains(rec.Body.String(), `id="lineup-pick"`) {
		t.Errorf("with one lineup, the page has no picker but a copy:\n%s", rec.Body)
	}
	rec = ta.do("POST", "/admin/lineups/copy", lineupForm(csrf, "", "name", "Kids"), c, hx...)
	kids := ta.lineupsByName(c)["Kids"]
	if rec.Code != http.StatusOK || kids.ID == 0 || !strings.Contains(rec.Body.String(), "Made the lineup Kids") ||
		!strings.Contains(rec.Body.String(), fmt.Sprintf(`<option value="%d" selected>Kids</option>`, kids.ID)) ||
		!strings.Contains(rec.Body.String(), "No account sees it yet") {
		t.Fatalf("copy: status %d, %+v\n%s", rec.Code, kids, rec.Body)
	}
	if rec.Header().Get("HX-Replace-Url") != fmt.Sprintf("/admin/lineup?lineup=%d", kids.ID) {
		t.Errorf("the address after a copy: %q", rec.Header().Get("HX-Replace-Url"))
	}
	if !strings.Contains(logs.String(), `msg="a lineup made in the admin" lineup=Kids copy_of=Default`) {
		t.Errorf("the log:\n%s", logs.String())
	}
	rec = ta.do("POST", "/admin/lineups/copy", lineupForm(csrf, "", "name", "Kids"), c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Another lineup has that name") {
		t.Errorf("a second Kids: status %d", rec.Code)
	}

	// An edit in Kids, from its page, leaves the first lineup alone.
	back := fmt.Sprintf("lineup=%d", kids.ID)
	in := fmt.Sprintf("?lineup=%d", kids.ID)
	groups := func(query string) map[string]apiGroup {
		t.Helper()
		rec := ta.apiDo("GET", "lineup/groups"+query, "", c)
		var body struct{ Groups []apiGroup }
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("groups %s: status %d", query, rec.Code)
		}
		out := map[string]apiGroup{}
		for _, g := range body.Groups {
			out[g.Name] = g
		}
		return out
	}
	sports := groups(in)["Sports"]
	res := ta.do("POST", fmt.Sprintf("/admin/lineup/groups/%d/hide", sports.ID), lineupForm(csrf, back), c).Result()
	if res.StatusCode != http.StatusSeeOther || res.Header.Get("Location") != "/admin/lineup?"+back {
		t.Errorf("hide in Kids: status %d, Location %q", res.StatusCode, res.Header.Get("Location"))
	}
	if !groups(in)["Sports"].Hidden || groups("")["Sports"].Hidden {
		t.Errorf("Kids' Sports: %+v; the first's: %+v", groups(in)["Sports"], groups("")["Sports"])
	}
	page := ta.do("GET", "/admin/lineup"+in, nil, c).Body.String()
	if !strings.Contains(page, `<input type="hidden" name="lineup" value="`+fmt.Sprint(kids.ID)+`">`) ||
		!strings.Contains(page, `name="back" value="lineup=`+fmt.Sprint(kids.ID)) {
		t.Errorf("Kids' page does not keep its lineup in its forms:\n%s", page)
	}
	if rec := ta.do("GET", "/admin/lineup?lineup=999", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("a lineup that does not exist: status %d", rec.Code)
	}
	if rec := ta.apiDo("GET", "lineup/channels?lineup=999", "", c); rec.Code != http.StatusNotFound {
		t.Errorf("the API, a lineup that does not exist: status %d", rec.Code)
	}

	// Rename it.
	rec = ta.do("POST", "/admin/lineups/rename", lineupForm(csrf, back, "name", "Children"), c, hx...)
	if rec.Code != http.StatusOK || ta.lineupsByName(c)["Children"].ID != kids.ID {
		t.Errorf("rename: status %d", rec.Code)
	}

	// Sam sees it, from the Accounts page: it cannot be removed then.
	sam, err := ta.st.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/accounts/%d/lineup", sam.ID), url.Values{"csrf": {csrf}, "lineup": {fmt.Sprint(kids.ID)}}, c, hx...)
	if a, _ := ta.st.Account(ctx, sam.ID); rec.Code != http.StatusOK || a.LineupID != kids.ID ||
		!strings.Contains(rec.Body.String(), "Sam sees the lineup Children now") || !strings.Contains(rec.Body.String(), "· sees Children") {
		t.Errorf("Sam's lineup: status %d, %+v", rec.Code, a)
	}
	page = ta.do("GET", "/admin/lineup"+in, nil, c).Body.String()
	if !strings.Contains(page, "Seen by Sam.") || strings.Contains(page, "Remove this lineup") {
		t.Errorf("Kids' page while Sam sees it:\n%s", page)
	}
	rec = ta.do("POST", "/admin/lineups/remove", lineupForm(csrf, back), c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "An account sees this lineup") {
		t.Errorf("removing a lineup that Sam sees: status %d", rec.Code)
	}
	// Back to the first, then it goes.
	h2 := []string{csrfHeader, csrf}
	if rec := ta.apiDo("PATCH", fmt.Sprintf("accounts/%d", sam.ID), fmt.Sprintf(`{"lineup_id": %d}`, store.DefaultLineupID), c, h2...); rec.Code != http.StatusOK {
		t.Fatalf("the API, Sam's lineup: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.do("POST", "/admin/lineups/remove", lineupForm(csrf, back), c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed the lineup Children") {
		t.Errorf("remove: status %d", rec.Code)
	}
	if _, ok := ta.lineupsByName(c)["Children"]; ok {
		t.Error("Children is still there")
	}
}

func TestLineupsAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	rec := ta.apiDo("POST", "lineups", `{"name": "Guests"}`, c, h...)
	var guests apiLineup
	if err := json.Unmarshal(rec.Body.Bytes(), &guests); err != nil || rec.Code != http.StatusCreated || guests.Name != "Guests" ||
		guests.Shown != 4 || len(guests.Accounts) != 0 {
		t.Fatalf("copy: status %d, %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		method, path, body string
		status             int
		code               string
	}{
		{"POST", "lineups", `{"name": "Guests"}`, http.StatusConflict, "name_taken"},
		{"POST", "lineups", `{"name": ""}`, http.StatusBadRequest, "bad_name"},
		{"POST", "lineups", `{"name": "Other", "copy_of": 999}`, http.StatusNotFound, "not_found"},
		{"PATCH", fmt.Sprintf("lineups/%d", guests.ID), `{"name": "Default"}`, http.StatusConflict, "name_taken"},
		{"PATCH", "lineups/999", `{"name": "Other"}`, http.StatusNotFound, "not_found"},
		{"DELETE", fmt.Sprintf("lineups/%d", store.DefaultLineupID), "", http.StatusConflict, "lineup_in_use"},
		{"PATCH", "accounts/1", `{"lineup_id": 999}`, http.StatusNotFound, "not_found"},
	} {
		rec := ta.apiDo(tc.method, tc.path, tc.body, c, h...)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("%s %s %s: status %d, %s", tc.method, tc.path, tc.body, rec.Code, rec.Body)
		}
	}
	// A rename of a channel in Guests, through the API's lineup parameter.
	q := fmt.Sprintf("?lineup=%d", guests.ID)
	chs, _ := ta.channels(c, q)
	alpha := chs["Alpha"]
	if rec := ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d%s", alpha.ID, q), `{"name": "Alpha Guest"}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("rename in Guests: status %d, %s", rec.Code, rec.Body)
	}
	if chs, _ := ta.channels(c, q); chs["Alpha"].Name != "Alpha Guest" {
		t.Errorf("Guests' Alpha: %+v", chs["Alpha"])
	}
	if chs, _ := ta.channels(c, ""); chs["Alpha"].Name != "Alpha" {
		t.Errorf("the first's Alpha: %+v", chs["Alpha"])
	}
	// The first lineup's entry is not Guests'.
	first, _ := ta.channels(c, "")
	if rec := ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d%s", first["Bravo"].ID, q), `{"name": "Nope"}`, c, h...); rec.Code != http.StatusNotFound {
		t.Errorf("another lineup's channel: status %d", rec.Code)
	}
	if rec := ta.apiDo("DELETE", fmt.Sprintf("lineups/%d", guests.ID), "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("remove: status %d, %s", rec.Code, rec.Body)
	}
}
