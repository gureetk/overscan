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

	"overscan.gureet.ca/internal/store"
)

// profilesOf returns the household's profiles by name.
func (ta *testAdmin) profilesOf() map[string]store.Profile {
	ta.t.Helper()
	list, err := ta.st.Profiles(context.Background(), householdAccount)
	if err != nil {
		ta.t.Fatal(err)
	}
	out := map[string]store.Profile{}
	for _, p := range list {
		out[p.Name] = p
	}
	return out
}

func TestProfilesPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ta.waitingDevices()
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	if _, err := ta.approveCode(ctx, boxCode, householdAccount, "Living room"); err != nil {
		t.Fatal(err)
	}

	// With one profile, the page lists it and the devices choose none.
	body := ta.do("GET", "/admin/devices", nil, c).Body.String()
	for _, want := range []string{`<section id="profiles"`, "<strong>Household</strong>", "The first profile · 1 device plays as it", `action="/admin/profiles"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	for _, unwanted := range []string{"<th>Profile</th>", "Remove the profile", "Remove the PIN"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("with one profile, the page has %q", unwanted)
		}
	}

	rec := ta.do("POST", "/admin/profiles", url.Values{"csrf": {csrf}, "name": {"  Alex "}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Added the profile Alex. Boxes can switch to it now.") {
		t.Fatalf("add: status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "<th>Profile</th>") || !strings.Contains(body, `action="/admin/devices/`+boxMAC+`/profile"`) || !strings.Contains(body, "Remove the profile") {
		t.Error("with two profiles, the page has no choice of profile for the box, or no way to remove one")
	}
	rec = ta.do("POST", "/admin/profiles", url.Values{"csrf": {csrf}, "name": {"ALEX"}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is not another profile") || !strings.Contains(rec.Body.String(), `value="ALEX"`) {
		t.Errorf("a name taken: status %d", rec.Code)
	}
	alex := ta.profilesOf()["Alex"]

	// The box plays as Alex.
	rec = ta.do("POST", "/admin/devices/"+boxMAC+"/profile", url.Values{"csrf": {csrf}, "profile": {fmt.Sprint(alex.ID)}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Living room ("+boxMAC+") plays as Alex now.") {
		t.Errorf("choose the box's profile: status %d", rec.Code)
	}
	if d := ta.device(boxMAC); d.ProfileID != alex.ID {
		t.Errorf("the box plays as %d, want %d", d.ProfileID, alex.ID)
	}
	if !strings.Contains(rec.Body.String(), fmt.Sprintf(`<option value="%d" selected>Alex</option>`, alex.ID)) {
		t.Error("the box's choice of profile does not show Alex")
	}
	// A device that waits plays as no profile.
	rec = ta.do("POST", "/admin/devices/"+clientMAC+"/profile", url.Values{"csrf": {csrf}, "profile": {fmt.Sprint(alex.ID)}}, c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "is not approved, so it plays as no profile") {
		t.Errorf("a waiting device: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/devices/"+boxMAC+"/profile", url.Values{"csrf": {csrf}, "profile": {"999"}}, c, hx...)
	if rec.Code != http.StatusNotFound {
		t.Errorf("a missing profile: status %d", rec.Code)
	}

	// Rename, a PIN and its removal.
	rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/rename", alex.ID), url.Values{"csrf": {csrf}, "name": {"Alexandra"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Renamed the profile Alexandra.") {
		t.Errorf("rename: status %d", rec.Code)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/pin", alex.ID), url.Values{"csrf": {csrf}, "pin": {"12"}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "A PIN has 4 to 8 digits.") {
		t.Errorf("a short PIN: status %d", rec.Code)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/pin", alex.ID), url.Values{"csrf": {csrf}, "pin": {"2468"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Set a PIN for Alexandra.") || !strings.Contains(rec.Body.String(), "Remove the PIN") {
		t.Errorf("set a PIN: status %d", rec.Code)
	}
	hash := ta.profilesOf()["Alexandra"].PINHash
	if ok, err := CheckPassword(ctx, hash, "2468"); !ok || err != nil {
		t.Errorf("the profile's hash %q does not hold its PIN: %v, %v", hash, ok, err)
	}
	if ta.pinHash() != "" {
		t.Error("a profile's PIN became the parental PIN")
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/remove-pin", alex.ID), url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || ta.profilesOf()["Alexandra"].PINHash != "" {
		t.Errorf("remove the PIN: status %d", rec.Code)
	}

	// Removing it puts the box back on the first profile; the last one stays.
	rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/remove", alex.ID), url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed the profile Alexandra, with its favourites and where it stopped.") {
		t.Errorf("remove: status %d", rec.Code)
	}
	first := ta.profilesOf()["Household"]
	if d := ta.device(boxMAC); d.ProfileID != first.ID {
		t.Errorf("after removing its profile, the box plays as %d", d.ProfileID)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/remove", first.ID), url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "only profile cannot be removed") {
		t.Errorf("remove the last: status %d", rec.Code)
	}
	if rec = ta.do("POST", "/admin/profiles/x/rename", url.Values{"csrf": {csrf}, "name": {"Sam"}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("a profile that is not a number: status %d", rec.Code)
	}
	if rec = ta.do("POST", fmt.Sprintf("/admin/profiles/%d/explode", first.ID), url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: status %d", rec.Code)
	}

	// Without htmx, the page comes back with a redirect; without the CSRF
	// token, nothing changes.
	rec = ta.do("POST", "/admin/profiles", url.Values{"csrf": {csrf}, "name": {"Sam"}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/devices" {
		t.Errorf("without htmx: status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec = ta.do("POST", "/admin/profiles", url.Values{"name": {"Kim"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}
	if _, ok := ta.profilesOf()["Kim"]; ok {
		t.Error("a profile was added without the CSRF token")
	}
}

// Text that the administrator types shows as text.
func TestProfileNamesAreEscaped(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	rec := ta.do("POST", "/admin/profiles", url.Values{"csrf": {csrf}, "name": {`<b>"Sam"</b>`}}, c, "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if body := rec.Body.String(); strings.Contains(body, `<b>"Sam"</b>`) || !strings.Contains(body, "&lt;b&gt;&#34;Sam&#34;&lt;/b&gt;") {
		t.Error("a profile's name went into the page as markup")
	}
}

func TestProfilesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	h := []string{csrfHeader, csrf}
	ta.waitingDevices()
	if _, err := ta.approveCode(context.Background(), boxCode, householdAccount, "Living room"); err != nil {
		t.Fatal(err)
	}
	decode := func(rec interface{ Result() *http.Response }, v any) int {
		t.Helper()
		res := rec.Result()
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatalf("status %d: %v", res.StatusCode, err)
		}
		return res.StatusCode
	}

	var list struct {
		Profiles []apiProfile `json:"profiles"`
	}
	if status := decode(ta.apiDo("GET", "profiles", "", c), &list); status != http.StatusOK || len(list.Profiles) != 1 ||
		list.Profiles[0] != (apiProfile{ID: list.Profiles[0].ID, AccountID: 1, Name: "Household", First: true, Devices: 1}) {
		t.Fatalf("status %d, %+v", status, list)
	}
	first := list.Profiles[0]

	var sam apiProfile
	if status := decode(ta.apiDo("POST", "profiles", `{"name": "Sam"}`, c, h...), &sam); status != http.StatusCreated || sam.Name != "Sam" || sam.First || sam.PIN {
		t.Fatalf("add: status %d, %+v", status, sam)
	}
	apiErrorOf(t, ta.apiDo("POST", "profiles", `{"name": "sam"}`, c, h...), http.StatusBadRequest, "bad_name")
	apiErrorOf(t, ta.apiDo("POST", "profiles", `{"name": ""}`, c, h...), http.StatusBadRequest, "bad_name")
	apiErrorOf(t, ta.apiDo("POST", "profiles", `{"name": "Kim"}`, c), http.StatusForbidden, "csrf")

	path := fmt.Sprintf("profiles/%d", sam.ID)
	var got apiProfile
	if status := decode(ta.apiDo("PATCH", path, `{"name": "Sam B", "pin": "1357"}`, c, h...), &got); status != http.StatusOK || got.Name != "Sam B" || !got.PIN {
		t.Errorf("change: status %d, %+v", status, got)
	}
	// A bad PIN changes nothing, the name included.
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"name": "Sam C", "pin": "12"}`, c, h...), http.StatusBadRequest, "bad_pin")
	if p := ta.profilesOf()["Sam B"]; p.ID != sam.ID {
		t.Errorf("a refused change renamed the profile: %+v", ta.profilesOf())
	}
	if status := decode(ta.apiDo("PATCH", path, `{"pin": ""}`, c, h...), &got); status != http.StatusOK || got.PIN {
		t.Errorf("remove the PIN: status %d, %+v", status, got)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"name": "Household"}`, c, h...), http.StatusBadRequest, "bad_name")
	apiErrorOf(t, ta.apiDo("PATCH", "profiles/999", `{"name": "Nobody"}`, c, h...), http.StatusNotFound, "no_such_profile")

	// The box plays as Sam, until Sam goes.
	var d apiDevice
	if status := decode(ta.apiDo("PATCH", "devices/"+boxMAC, fmt.Sprintf(`{"profile_id": %d}`, sam.ID), c, h...), &d); status != http.StatusOK ||
		d.ProfileID == nil || *d.ProfileID != sam.ID || d.Label != "Living room" {
		t.Errorf("the box's profile: status %d, %+v", status, d)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "devices/"+clientMAC, fmt.Sprintf(`{"profile_id": %d}`, sam.ID), c, h...), http.StatusConflict, "not_approved")
	apiErrorOf(t, ta.apiDo("PATCH", "devices/"+boxMAC, `{"profile_id": 999}`, c, h...), http.StatusNotFound, "no_such_profile")
	apiErrorOf(t, ta.apiDo("PATCH", "devices/"+boxMAC, `{}`, c, h...), http.StatusBadRequest, "bad_json")

	rec := ta.apiDo("DELETE", path, "", c, h...)
	if rec.Code != http.StatusNoContent {
		t.Errorf("remove: status %d, %s", rec.Code, rec.Body)
	}
	if p := ta.device(boxMAC).ProfileID; p != first.ID {
		t.Errorf("after removing its profile, the box plays as %d", p)
	}
	apiErrorOf(t, ta.apiDo("DELETE", path, "", c, h...), http.StatusNotFound, "no_such_profile")
	apiErrorOf(t, ta.apiDo("DELETE", fmt.Sprintf("profiles/%d", first.ID), "", c, h...), http.StatusConflict, "last_profile")
}
