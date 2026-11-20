// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// A channel's panel adds backups by number or name, moves and removes them,
// and the API sets them (D109).
func TestBackupsOnThePanel(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	chs, _ := ta.channels(c, "")
	alpha, bravo, charlie := chs["Alpha"], chs["Bravo"], chs["Charlie"]
	path := fmt.Sprintf("/admin/lineup/channels/%d/backups", alpha.ID)

	page := ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", alpha.ID), nil, c).Body.String()
	mustContain(t, "the panel", page, `<h3 id="backups-title">Backups</h3>`, `placeholder="A channel's number or name"`)

	rec := ta.do("POST", path, lineupForm(csrf, "", "op", "add", "backup", fmt.Sprint(*bravo.Number)), c, hx...)
	mustContain(t, "after adding by number", rec.Body.String(), "Added the backup.", fmt.Sprintf("%d · Bravo", *bravo.Number))
	rec = ta.do("POST", path, lineupForm(csrf, "", "op", "add", "backup", "charlie"), c, hx...)
	if rec.Code != http.StatusOK {
		t.Fatalf("add by name: status %d", rec.Code)
	}
	for _, tc := range []struct{ text, want string }{
		{"nobody", "No channel of this lineup has that number or name."},
		{"alpha", "A channel cannot be its own backup, or a backup twice."},
		{"bravo", "A channel cannot be its own backup, or a backup twice."},
	} {
		rec := ta.do("POST", path, lineupForm(csrf, "", "op", "add", "backup", tc.text), c, hx...)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.want) {
			t.Errorf("add %q: status %d", tc.text, rec.Code)
		}
	}
	list := ta.backupsOf(c, alpha.ID)
	if len(list) != 2 || list[0].EntryID != bravo.ID || list[1].EntryID != charlie.ID {
		t.Fatalf("backups: %+v", list)
	}
	if chs, _ := ta.channels(c, ""); chs["Alpha"].Backups != 2 {
		t.Errorf("Alpha: %+v", chs["Alpha"])
	}
	page = ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "the channels", page, `title="When its stream cannot start, one of its 2 backups plays">2 backups</span>`)

	rec = ta.do("POST", path, lineupForm(csrf, "", "op", "up", "backup", fmt.Sprint(charlie.ID)), c, hx...)
	if list := ta.backupsOf(c, alpha.ID); rec.Code != http.StatusOK || list[0].EntryID != charlie.ID {
		t.Errorf("up: status %d, %+v", rec.Code, list)
	}
	res := ta.do("POST", path, lineupForm(csrf, "", "op", "remove", "backup", fmt.Sprint(bravo.ID)), c).Result()
	if list := ta.backupsOf(c, alpha.ID); res.StatusCode != http.StatusSeeOther ||
		res.Header.Get("Location") != fmt.Sprintf("/admin/lineup?edit=%d", alpha.ID) || len(list) != 1 {
		t.Errorf("remove without htmx: status %d, %q, %+v", res.StatusCode, res.Header.Get("Location"), list)
	}
	if rec := ta.do("POST", path, lineupForm(csrf, "", "op", "remove", "backup", fmt.Sprint(bravo.ID)), c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("removing what is not a backup: status %d", rec.Code)
	}

	// The API.
	api := fmt.Sprintf("lineup/channels/%d/backups", alpha.ID)
	rec = ta.apiDo("PUT", api, fmt.Sprintf(`{"entries": [%d, %d]}`, bravo.ID, chs["Delta"].ID), c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name": "Delta"`) {
		t.Errorf("PUT: status %d, %s", rec.Code, rec.Body)
	}
	for _, tc := range []struct {
		body   string
		status int
		code   string
	}{
		{fmt.Sprintf(`{"entries": [%d]}`, alpha.ID), http.StatusBadRequest, "bad_backup"},
		{`{"entries": [1, 2, 3, 4, 5, 6]}`, http.StatusConflict, "too_many_backups"},
		{`{"entries": [999]}`, http.StatusNotFound, "not_found"},
		{`{"channels": []}`, http.StatusBadRequest, "bad_json"},
	} {
		rec := ta.apiDo("PUT", api, tc.body, c, h...)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"`+tc.code+`"`) {
			t.Errorf("PUT %s: status %d, %s", tc.body, rec.Code, rec.Body)
		}
	}
	if rec := ta.apiDo("GET", "lineup/channels/999/backups", "", c); rec.Code != http.StatusNotFound {
		t.Errorf("GET of no channel: status %d", rec.Code)
	}
	if rec := ta.apiDo("PUT", api, `{"entries": []}`, c, h...); rec.Code != http.StatusOK || len(ta.backupsOf(c, alpha.ID)) != 0 {
		t.Errorf("an empty list: status %d", rec.Code)
	}
}

// backupsOf returns a channel's backups, as the API lists them.
func (ta *testAdmin) backupsOf(c *http.Cookie, id int64) []apiBackup {
	ta.t.Helper()
	rec := ta.apiDo("GET", fmt.Sprintf("lineup/channels/%d/backups", id), "", c)
	var body struct{ Backups []apiBackup }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		ta.t.Fatalf("backups: status %d, %s", rec.Code, rec.Body)
	}
	return body.Backups
}
