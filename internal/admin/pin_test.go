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
)

// pinHash returns the household account's PIN hash.
func (ta *testAdmin) pinHash() string {
	ta.t.Helper()
	acct, err := ta.st.Account(context.Background(), householdAccount)
	if err != nil {
		ta.t.Fatal(err)
	}
	return acct.PINHash
}

func TestPINAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	state := func(rec interface{ Result() *http.Response }) (bool, int) {
		t.Helper()
		res := rec.Result()
		var body apiPINState
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("status %d: %v", res.StatusCode, err)
		}
		return body.Set, res.StatusCode
	}

	if set, status := state(ta.apiDo("GET", "pin", "", c)); set || status != http.StatusOK {
		t.Errorf("at first: set %v, status %d", set, status)
	}
	for _, body := range []string{`{"pin": "123"}`, `{"pin": "123456789"}`, `{"pin": "12a4"}`, `{"pin": "١٢٣٤"}`, `{"pin": ""}`} {
		apiErrorOf(t, ta.apiDo("PUT", "pin", body, c, h...), http.StatusBadRequest, "bad_pin")
	}
	apiErrorOf(t, ta.apiDo("PUT", "pin", `{"pin": 1234}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PUT", "pin", `{"pin": "1234"}`, c), http.StatusForbidden, "csrf")
	if ta.pinHash() != "" {
		t.Fatal("a refused PIN was kept")
	}

	if set, status := state(ta.apiDo("PUT", "pin", `{"pin": "2468"}`, c, h...)); !set || status != http.StatusOK {
		t.Errorf("set: %v, status %d", set, status)
	}
	hash := ta.pinHash()
	if ok, err := CheckPassword(context.Background(), hash, "2468"); !ok || err != nil || strings.Contains(hash, "2468") {
		t.Errorf("the hash %q does not hold 2468: %v, %v", hash, ok, err)
	}
	if set, _ := state(ta.apiDo("GET", "pin", "", c)); !set {
		t.Error("GET says no PIN after setting one")
	}
	apiErrorOf(t, ta.apiDo("DELETE", "pin", "", c), http.StatusForbidden, "csrf")
	if set, status := state(ta.apiDo("DELETE", "pin", "", c, h...)); set || status != http.StatusOK || ta.pinHash() != "" {
		t.Errorf("remove: %v, status %d, hash %q", set, status, ta.pinHash())
	}
}

func TestLocksInTheAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	news := ta.groups(c)["News"]
	chs, _ := ta.channels(c, "")

	rec := ta.apiDo("PATCH", fmt.Sprintf("lineup/groups/%d", news.ID), `{"locked": true}`, c, h...)
	var g apiGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || !g.Locked || g.Hidden {
		t.Errorf("lock News: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", "lineup/channels", fmt.Sprintf(`{"ids": [%d], "locked": true}`, chs["Bravo"].ID), c, h...)
	if rec.Code != http.StatusOK {
		t.Errorf("lock Bravo: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Delta"].ID), `{"locked": "yes"}`, c, h...),
		http.StatusBadRequest, "bad_json")

	chs, _ = ta.channels(c, "")
	for name, want := range map[string][2]bool{
		"Alpha": {false, true}, "Bravo": {true, false}, "Charlie": {false, true}, "Delta": {false, false},
	} {
		if got := [2]bool{chs[name].Locked, chs[name].GroupLocked}; got != want {
			t.Errorf("%s: locked, group_locked %v, want %v", name, got, want)
		}
	}
	// Locks change what devices are told, not what they see.
	if got := len(ta.seen()); got != 4 {
		t.Errorf("devices see %d channels", got)
	}

	rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Bravo"].ID), `{"locked": false}`, c, h...)
	var e apiEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Locked {
		t.Errorf("unlock Bravo: status %d, %s", rec.Code, rec.Body)
	}
	if rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/groups/%d", news.ID), `{"locked": false}`, c, h...); strings.Contains(rec.Body.String(), `"locked": true`) {
		t.Errorf("unlock News: %s", rec.Body)
	}
}

func TestLockAndPINForms(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	chs, _ := ta.channels(c, "")
	news := ta.groups(c)["News"]
	hx := []string{"HX-Request", "true"}
	send := func(path string, form url.Values, status int, want ...string) string {
		t.Helper()
		rec := ta.do("POST", path, form, c, hx...)
		if rec.Code != status {
			t.Errorf("%s %v: status %d, want %d", path, form, rec.Code, status)
		}
		mustContain(t, path, rec.Body.String(), want...)
		return rec.Body.String()
	}

	mustContain(t, "the settings", ta.do("GET", "/admin/settings", nil, c).Body.String(),
		`<h2 id="pin-title">Parental PIN</h2>`, "and set a PIN that boxes ask for", `action="/admin/settings/pin"`)

	send("/admin/lineup/channels", lineupForm(csrf, "", "action", "lock", "ids", fmt.Sprint(chs["Delta"].ID)), http.StatusOK,
		"Locked 1 channel.", `<span class="badge locked" title="It needs the parental PIN to play">`,
		"The lineup's lock does nothing until there is a parental PIN.")
	page := send(fmt.Sprintf("/admin/lineup/groups/%d/lock", news.ID), lineupForm(csrf, fmt.Sprintf("group=%d", news.ID)), http.StatusOK,
		"Locked the group", "Unlock the group", `<span class="badge locked">`,
		"The lineup's 2 locks do nothing until there is a parental PIN.")
	if strings.Contains(page, "Remove the PIN") {
		t.Error("the page offers to remove a PIN that is not set")
	}
	// The group's view says the group is locked once, in its head; the list
	// of every channel marks each of its channels.
	if strings.Contains(page, `title="Its group is locked: it needs the parental PIN to play"`) {
		t.Error("the group's own view marks its channels as locked by the group")
	}
	mustContain(t, "every channel", ta.do("GET", "/admin/lineup", nil, c).Body.String(), `title="Its group is locked: it needs the parental PIN to play"`)
	mustContain(t, "the settings", ta.do("GET", "/admin/settings", nil, c).Body.String(),
		"Not set, so the lineup's 2 locks do nothing yet.")

	send("/admin/settings/pin", lineupForm(csrf, "", "pin", "12"), http.StatusBadRequest, "A PIN has 4 to 8 digits.")
	send("/admin/settings/pin", lineupForm(csrf, "", "pin", "12 34"), http.StatusBadRequest, "A PIN has only digits")
	send("/admin/settings/pin", lineupForm(csrf, ""), http.StatusBadRequest, "Type the new PIN.")
	page = send("/admin/settings/pin", lineupForm(csrf, "", "pin", "1357"), http.StatusOK,
		"Set the PIN.", `<span class="badge ok">Set</span>`, "The lineup has 2 locks.", "Remove the PIN")
	if strings.Contains(page, "do nothing") {
		t.Error("with a PIN, the settings still say that the locks do nothing")
	}
	if strings.Contains(ta.do("GET", "/admin/lineup", nil, c).Body.String(), "until there is a parental PIN") {
		t.Error("with a PIN, the lineup still says that its locks do nothing")
	}
	if ok, _ := CheckPassword(context.Background(), ta.pinHash(), "1357"); !ok {
		t.Error("the form did not set 1357")
	}
	if rec := ta.do("POST", "/admin/settings/pin", url.Values{"pin": {"1111"}}, c, hx...); rec.Code != http.StatusForbidden {
		t.Errorf("without the token: status %d", rec.Code)
	}

	// The edit row's box locks or unlocks one channel.
	save := lineupForm(csrf, "", "name", "", "group", "0", "visibility", "group", "number", "4")
	send(fmt.Sprintf("/admin/lineup/channels/%d", chs["Delta"].ID), save, http.StatusOK, "Saved Delta.")
	if e, _ := ta.channels(c, ""); e["Delta"].Locked {
		t.Error("Delta is still locked after saving without the box ticked")
	}
	save.Set("locked", "1")
	send(fmt.Sprintf("/admin/lineup/channels/%d", chs["Delta"].ID), save, http.StatusOK, "Saved Delta.")
	if e, _ := ta.channels(c, ""); !e["Delta"].Locked {
		t.Error("Delta is not locked after saving with the box ticked")
	}

	send("/admin/lineup/channels", lineupForm(csrf, "", "action", "unlock", "ids", fmt.Sprint(chs["Alpha"].ID)), http.StatusOK,
		"Unlocked 1 channel. Those in a locked group still need the PIN.")
	send(fmt.Sprintf("/admin/lineup/groups/%d/unlock", news.ID), lineupForm(csrf, ""), http.StatusOK, "Unlocked the group.")
	send("/admin/settings/pin", lineupForm(csrf, "", "remove", "1"), http.StatusOK, "Removed the PIN.")
	if ta.pinHash() != "" {
		t.Error("the PIN is still set")
	}
}
