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

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// withNewSource adds a source "two" with UK: Alpha and Bravo HD, which
// match main's Alpha and Bravo by name, and Echo, which main lacks.
func (ta *testAdmin) withNewSource() int64 {
	ta.t.Helper()
	ctx := context.Background()
	id, err := ta.st.AddSource(ctx, source.Settings{Name: "two", URL: "http://provider.example/two.m3u", MaxConnections: 1})
	if err != nil {
		ta.t.Fatal(err)
	}
	if _, err := ta.st.ApplyCatalog(ctx, id, []store.CatalogChannel{
		{IdentityKey: "a", Name: "UK: Alpha", Category: "General", StreamURL: "http://streams.example/two/a"},
		{IdentityKey: "b", Name: "Bravo HD", Category: "General", StreamURL: "http://streams.example/two/b"},
		{IdentityKey: "e", Name: "Echo", Category: "General", StreamURL: "http://streams.example/two/e"},
	}, ta.clock); err != nil {
		ta.t.Fatal(err)
	}
	return id
}

// TestMovePage finds where main's channels would move, moves the ticked
// ones, and shows them playing from the new source (D144).
func TestMovePage(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	srcs, err := ta.st.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	main := srcs[0].ID
	page := ta.do("GET", fmt.Sprintf("/admin/sources/%d/move", main), nil, c).Body.String()
	mustContain(t, "without another source", page, "<h1>Move main's channels</h1>", "There is no other source of channels to move to.")
	mustContain(t, "the sources page", ta.do("GET", "/admin/sources", nil, c).Body.String(),
		fmt.Sprintf(`<a href="/admin/sources/%d/move">Move its channels to other sources…</a>`, main))

	two := ta.withNewSource()
	path := fmt.Sprintf("/admin/sources/%d/move", main)
	page = ta.do("GET", path, nil, c).Body.String()
	mustContain(t, "with two", page, fmt.Sprintf(`<input type="checkbox" name="to" value="%d">`, two), "Find matches")
	page = ta.do("GET", fmt.Sprintf("%s?to=%d", path, two), nil, c).Body.String()
	mustContain(t, "the matches", page, "2 of main's channels have a match in two: 0 by guide ID, 2 by name.",
		"2 have none, and stay on main.", "Move the ticked channels", "UK: Alpha", "Bravo HD", "2 without a match, which stay on main",
		"Charlie", "Delta")
	if rec := ta.do("GET", fmt.Sprintf("%s?to=%d", path, main), nil, c); rec.Code != http.StatusBadRequest {
		t.Errorf("moving to itself: %d", rec.Code)
	}
	if rec := ta.do("GET", "/admin/sources/999/move", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("no such source: %d", rec.Code)
	}

	// Move Alpha only.
	ids := map[string]int64{}
	for _, srcID := range []int64{main, two} {
		cs, err := ta.st.MovableChannels(ctx, srcID)
		if err != nil {
			t.Fatal(err)
		}
		for _, ch := range cs {
			ids[fmt.Sprintf("%d %s", srcID, ch.Name)] = ch.ID
		}
	}
	alpha := fmt.Sprintf("%d:%d", ids[fmt.Sprintf("%d Alpha", main)], ids[fmt.Sprintf("%d UK: Alpha", two)])
	rec := ta.do("POST", path, url.Values{"csrf": {csrf}, "to": {fmt.Sprint(two)}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Tick the channels to move.") {
		t.Errorf("nothing ticked: %d", rec.Code)
	}
	// As htmx sends it: the CSRF token in a header.
	rec = ta.do("POST", path, url.Values{"to": {fmt.Sprint(two)}, "move": {alpha}}, c, append(hx, csrfHeader, csrf)...)
	mustContain(t, "moved", rec.Body.String(), "Moved 1 channel of main: their entries in every lineup play from the other sources now")
	if !strings.Contains(logs.String(), `msg="channels moved to other sources in the admin" source=main channels=1 entries=1 merged=1`) {
		t.Errorf("the log: %s", logs.String())
	}
	cs, err := ta.st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	var shown []string
	for _, ch := range cs {
		shown = append(shown, fmt.Sprintf("%d %s %d", ch.Number, ch.Name, ch.SourceID))
	}
	if len(cs) != 6 || shown[0] != fmt.Sprintf("1 Alpha %d", two) {
		t.Errorf("the lineup: %v", shown)
	}
	page = ta.do("GET", fmt.Sprintf("%s?to=%d", path, two), nil, c).Body.String()
	mustContain(t, "after the move", page, "1 of main's channels moved to other sources before.",
		"1 of main's channels has a match in two")
	// Moving it again is refused.
	rec = ta.do("POST", path, url.Values{"csrf": {csrf}, "to": {fmt.Sprint(two)}, "move": {alpha}}, c, hx...)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("moving again: %d", rec.Code)
	}
}

// TestMoveAPI plans and makes a move through the API (D144).
func TestMoveAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	ctx := context.Background()
	srcs, err := ta.st.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	main := srcs[0].ID
	two := ta.withNewSource()
	var plan apiMovePlan
	rec := ta.apiDo("GET", fmt.Sprintf("sources/%d/moves?to=%d", main, two), "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &plan); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("plan: %d %s", rec.Code, rec.Body)
	}
	if len(plan.Matches) != 2 || plan.Matches[0].Channel.Name != "Alpha" || plan.Matches[0].To.Name != "UK: Alpha" ||
		plan.Matches[0].By != "name" || len(plan.Unmatched) != 2 || plan.Unmatched[0].Reason != "no_match" {
		t.Errorf("plan %+v", plan)
	}
	apiErrorOf(t, ta.apiDo("GET", fmt.Sprintf("sources/%d/moves", main), "", c), http.StatusBadRequest, "bad_target")
	apiErrorOf(t, ta.apiDo("GET", fmt.Sprintf("sources/%d/moves?to=%d", main, main), "", c), http.StatusBadRequest, "bad_target")
	apiErrorOf(t, ta.apiDo("GET", fmt.Sprintf("sources/999/moves?to=%d", two), "", c), http.StatusNotFound, "not_found")

	body := fmt.Sprintf(`{"moves": [{"channel": %d, "to": %d}, {"channel": %d, "to": %d}]}`, plan.Matches[0].Channel.ID,
		plan.Matches[0].To.ID, plan.Matches[1].Channel.ID, plan.Matches[1].To.ID)
	rec = ta.apiDo("POST", fmt.Sprintf("sources/%d/moves", main), body, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"channels": 2`) {
		t.Errorf("move: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", fmt.Sprintf("sources/%d/moves", main), body, c, h...), http.StatusBadRequest, "bad_move")
	apiErrorOf(t, ta.apiDo("POST", fmt.Sprintf("sources/%d/moves", two), `{"moves": [{"channel": 1, "to": 2}]}`, c, h...),
		http.StatusBadRequest, "bad_move")
	apiErrorOf(t, ta.apiDo("POST", fmt.Sprintf("sources/%d/moves", main), `{"moves": []}`, c, h...), http.StatusBadRequest,
		"nothing_to_move")
}
