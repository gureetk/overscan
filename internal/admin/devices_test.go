// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Fictional devices.
const (
	boxMAC    = "00:1A:79:00:00:01" // a box running the MAG page, waiting with a code
	clientMAC = "00:1A:79:00:00:02" // a Stalker client, waiting without one
	boxCode   = "246810"
)

// waitingDevices records a box that asks to be paired and a Stalker client
// that has connected.
func (ta *testAdmin) waitingDevices() {
	ta.t.Helper()
	ctx := context.Background()
	if _, err := ta.st.RequestPairing(ctx, boxMAC, "MAG322", boxCode, store.HashSecret("box secret"), ta.clock); err != nil {
		ta.t.Fatal(err)
	}
	if _, err := ta.st.SeeDevice(ctx, clientMAC, "", ta.clock); err != nil {
		ta.t.Fatal(err)
	}
}

func (ta *testAdmin) device(mac string) store.Device {
	ta.t.Helper()
	d, err := ta.st.Device(context.Background(), mac)
	if err != nil {
		ta.t.Fatal(err)
	}
	return d
}

func TestDevicesPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, _ := ta.signIn("correct horse")
	ta.waitingDevices()

	rec := ta.do("GET", "/admin/devices", nil, c)
	body := rec.Body.String()
	for _, want := range []string{boxCode, boxMAC, clientMAC, "MAG322", "Waiting for approval", `title="2 waiting for approval"`, "No device is approved or blocked yet.",
		`<span class="code">246 810</span>`, `<input id="pair-code" class="code-field" name="code"`, `action="/admin/devices/approve"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status %d", rec.Code)
	}

	// The waiting list, which the page refreshes, comes on its own.
	rec = ta.do("GET", "/admin/devices/waiting", nil, c)
	body = rec.Body.String()
	if rec.Code != http.StatusOK || !strings.HasPrefix(strings.TrimSpace(body), `<section id="waiting"`) || !strings.Contains(body, boxCode) || strings.Contains(body, "<html") {
		t.Errorf("waiting list: status %d, %q", rec.Code, body)
	}
	// With it comes the bar's count, which htmx puts in place.
	if !strings.Contains(body, `<a id="nav-devices" hx-swap-oob="true"`) || !strings.Contains(body, `<a id="tab-devices" hx-swap-oob="true"`) ||
		!strings.Contains(body, `title="2 waiting for approval"`) {
		t.Errorf("the waiting list brings no count for the bar: %q", body)
	}
}

func TestPairABoxByItsCode(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ta.waitingDevices()

	rec := ta.do("POST", "/admin/devices/pair", url.Values{"csrf": {csrf}, "code": {"135791"}}, c)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "No box is waiting with the code 135791.") {
		t.Errorf("unknown code: status %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `value="135791"`) {
		t.Error("the code typed is not kept after a mistake")
	}
	rec = ta.do("POST", "/admin/devices/pair", url.Values{"csrf": {csrf}, "code": {"12ab"}}, c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "A code has six digits.") {
		t.Errorf("bad code: status %d", rec.Code)
	}

	rec = ta.do("POST", "/admin/devices/pair", url.Values{"csrf": {csrf}, "code": {" " + boxCode + " "}, "label": {"Living room"}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/devices" {
		t.Fatalf("status %d, location %q", rec.Code, rec.Header().Get("Location"))
	}
	d := ta.device(boxMAC)
	if d.State != store.DeviceApproved || !d.Paired() || d.Label != "Living room" || d.AccountID != householdAccount || d.PairingCode != "" {
		t.Errorf("after pairing: %+v", d)
	}
}

func TestDeviceActionsWithHTMX(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ta.waitingDevices()
	hx := []string{"HX-Request", "true"}

	rec := ta.do("POST", "/admin/devices/"+clientMAC+"/approve", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Approved "+clientMAC+".") {
		t.Errorf("approve: status %d", rec.Code)
	}
	if d := ta.device(clientMAC); d.State != store.DeviceApproved || d.Paired() {
		t.Errorf("after approving: %+v", d)
	}

	rec = ta.do("POST", "/admin/devices/"+clientMAC+"/label", url.Values{"csrf": {csrf}, "label": {"Phone"}}, c, hx...)
	if rec.Code != http.StatusOK || ta.device(clientMAC).Label != "Phone" {
		t.Errorf("label: status %d, %q", rec.Code, ta.device(clientMAC).Label)
	}
	rec = ta.do("POST", "/admin/devices/"+clientMAC+"/label", url.Values{"csrf": {csrf}, "label": {strings.Repeat("x", 101)}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most 100 characters") {
		t.Errorf("long label: status %d", rec.Code)
	}

	rec = ta.do("POST", "/admin/devices/"+clientMAC+"/block", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || ta.device(clientMAC).State != store.DeviceBlocked || !strings.Contains(rec.Body.String(), "Blocked Phone ("+clientMAC+").") {
		t.Errorf("block: status %d", rec.Code)
	}

	rec = ta.do("POST", "/admin/devices/"+clientMAC+"/remove", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed "+clientMAC+".") {
		t.Errorf("remove: status %d", rec.Code)
	}
	if _, err := ta.st.Device(context.Background(), clientMAC); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the device survived: %v", err)
	}
	rec = ta.do("POST", "/admin/devices/"+clientMAC+"/remove", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusNotFound {
		t.Errorf("removing it again: status %d", rec.Code)
	}
	if rec = ta.do("POST", "/admin/devices/"+clientMAC+"/explode", url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("unknown action: status %d", rec.Code)
	}
}

func TestApproveAMACAddressBeforeItConnects(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")

	rec := ta.do("POST", "/admin/devices/approve", url.Values{"csrf": {csrf}, "mac": {"not a mac"}}, c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is not a MAC address") {
		t.Errorf("bad MAC: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/devices/approve", url.Values{"csrf": {csrf}, "mac": {"00-1a-79-00-00-09"}, "label": {"Tablet"}}, c)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if d := ta.device("00:1A:79:00:00:09"); d.State != store.DeviceApproved || d.Label != "Tablet" {
		t.Errorf("got %+v", d)
	}
	rec = ta.do("POST", "/admin/devices/approve", url.Values{"csrf": {csrf}, "mac": {"00:1A:79:00:00:09"}, "account": {"7"}}, c)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "There is no account 7.") {
		t.Errorf("unknown account: status %d", rec.Code)
	}
}

func TestDeviceActionsNeedTheCSRFToken(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, _ := ta.signIn("correct horse")
	ta.waitingDevices()
	if rec := ta.do("POST", "/admin/devices/pair", url.Values{"code": {boxCode}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("status %d", rec.Code)
	}
	if ta.device(boxMAC).State != store.DevicePending {
		t.Error("the box was approved without the CSRF token")
	}
}

func TestReplacingAPairing(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ctx := context.Background()
	ta.waitingDevices()
	if _, err := ta.approveCode(ctx, boxCode, householdAccount, "Living room"); err != nil {
		t.Fatal(err)
	}
	// A box without the secret asks to be paired with the same MAC address.
	if _, err := ta.st.RequestPairing(ctx, boxMAC, "MAG322", "975310", store.HashSecret("another secret"), ta.clock); err != nil {
		t.Fatal(err)
	}
	body := ta.do("GET", "/admin/devices", nil, c).Body.String()
	if !strings.Contains(body, "Already paired as Living room.") || !strings.Contains(body, "Replace the pairing") {
		t.Error("the page does not say that a box asks to replace a pairing")
	}
	ta.do("POST", "/admin/devices/pair", url.Values{"csrf": {csrf}, "code": {"975310"}}, c)
	if d := ta.device(boxMAC); d.SecretHash != store.HashSecret("another secret") || d.PairingCode != "" {
		t.Errorf("the pairing was not replaced: %+v", d)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		10 * time.Second:        "just now",
		time.Minute:             "1 minute ago",
		5 * time.Minute:         "5 minutes ago",
		2 * time.Hour:           "2 hours ago",
		3 * 24 * time.Hour:      "3 days ago",
		45 * 24 * time.Hour:     "29 September 2026",
		-time.Duration(1 << 62): "never", // stands for the zero time below
	} {
		at := now.Add(-d)
		if d < 0 {
			at = time.Time{}
		}
		if got := ago(at, now); got != want {
			t.Errorf("%v ago: %q, want %q", d, got, want)
		}
	}
}

func TestDevicesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ta.waitingDevices()
	h := []string{csrfHeader, csrf}

	rec := ta.apiDo("GET", "devices", "", c)
	var list struct{ Devices []apiDevice }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Devices) != 2 {
		t.Fatalf("status %d, %s", rec.Code, rec.Body)
	}
	box := list.Devices[0]
	if box.MAC != boxMAC || box.State != "pending" || box.PairingCode == nil || *box.PairingCode != boxCode ||
		box.PairingRequestedAt == nil || box.AccountID != nil || box.Paired || box.Model != "MAG322" {
		t.Errorf("box %+v", box)
	}

	rec = ta.apiDo("POST", "pairings/"+boxCode+"/approve", `{"label": "Living room"}`, c, h...)
	var d apiDevice
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("approve code: status %d, %s", rec.Code, rec.Body)
	}
	if d.State != "approved" || !d.Paired || d.Label != "Living room" || d.AccountID == nil || *d.AccountID != 1 || d.PairingCode != nil {
		t.Errorf("approved box %+v", d)
	}
	apiErrorOf(t, ta.apiDo("POST", "pairings/"+boxCode+"/approve", "", c, h...), http.StatusNotFound, "no_such_code")
	apiErrorOf(t, ta.apiDo("POST", "pairings/12/approve", "", c, h...), http.StatusBadRequest, "bad_code")

	// Approving without a body.
	if rec = ta.apiDo("POST", "devices/"+clientMAC+"/approve", "", c, h...); rec.Code != http.StatusOK {
		t.Errorf("approve by MAC: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "devices/"+clientMAC+"/approve", `{"account_id": 9}`, c, h...), http.StatusBadRequest, "no_account")
	apiErrorOf(t, ta.apiDo("POST", "devices/nonsense/approve", "", c, h...), http.StatusBadRequest, "bad_mac")
	apiErrorOf(t, ta.apiDo("POST", "devices/"+clientMAC+"/approve", "", c), http.StatusForbidden, "csrf")

	rec = ta.apiDo("PATCH", "devices/"+clientMAC, `{"label": "Phone"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.Label != "Phone" {
		t.Errorf("label: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "devices/"+clientMAC, `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "devices/00:1A:79:00:00:77", `{"label": "x"}`, c, h...), http.StatusNotFound, "no_such_device")

	rec = ta.apiDo("POST", "devices/"+clientMAC+"/block", "", c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.State != "blocked" {
		t.Errorf("block: status %d, %s", rec.Code, rec.Body)
	}
	if rec = ta.apiDo("DELETE", "devices/"+clientMAC, "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("remove: status %d", rec.Code)
	}
	apiErrorOf(t, ta.apiDo("DELETE", "devices/"+clientMAC, "", c, h...), http.StatusNotFound, "no_such_device")
}

// checkedBoxes approves the waiting box and a second box of its model, and
// keeps a playback check of the first (D70).
func (ta *testAdmin) checkedBoxes() (box, other store.Device) {
	ta.t.Helper()
	ctx := context.Background()
	ta.waitingDevices()
	box, err := ta.st.ApprovePairing(ctx, boxCode, householdAccount, "Living room", ta.clock)
	if err != nil {
		ta.t.Fatal(err)
	}
	other, err = ta.st.SeeDevice(ctx, "00:1A:79:00:00:03", "MAG322", ta.clock)
	if err != nil {
		ta.t.Fatal(err)
	}
	if other, err = ta.st.ApproveDevice(ctx, other.MAC, householdAccount, "Bedroom", ta.clock); err != nil {
		ta.t.Fatal(err)
	}
	if err := ta.st.SetPlaybackCheck(ctx, store.PlaybackCheck{DeviceID: box.ID, Model: "MAG322", Firmware: "2.20.16-322",
		Findings: map[string]bool{"audio:dts": false, "audio:eac3": true, "format:matroska": true}, CheckedAt: ta.clock.Add(-2 * time.Hour)}); err != nil {
		ta.t.Fatal(err)
	}
	return box, other
}

func TestDevicesPageShowsChecks(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	box, _ := ta.checkedBoxes()

	body := ta.do("GET", "/admin/devices", nil, c).Body.String()
	for _, want := range []string{"<th>Plays</th>", fmt.Sprintf(`<a href="#check-%d">Checked 2 hours ago</a>`, box.ID), "As Living room (" + boxMAC + ")",
		"Not checked", "What the boxes play", fmt.Sprintf(`<li id="check-%d">`, box.ID), "MAG322, firmware 2.20.16-322, checked 2 hours ago",
		`<span class="check-label">Plays</span> MKV, Dolby Digital Plus</p>`, `<span class="check-label">Does not play</span> DTS</p>`,
		`action="/admin/devices/` + boxMAC + `/forget-check"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	if n := strings.Count(body, "/forget-check\""); n != 2 { // action and hx-post of one form
		t.Errorf("%d forget forms' addresses, want those of one", n)
	}

	hx := []string{"HX-Request", "true"}
	rec := ta.do("POST", "/admin/devices/"+boxMAC+"/forget-check", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Forgot what the playback check of Living room ("+boxMAC+") found.") {
		t.Errorf("forget: status %d", rec.Code)
	}
	if _, err := ta.st.PlaybackCheck(context.Background(), box.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("the check survived: %v", err)
	}
	if strings.Contains(rec.Body.String(), "Checked 2 hours ago") || strings.Contains(rec.Body.String(), "What the boxes play") {
		t.Error("the page still shows the check")
	}
	rec = ta.do("POST", "/admin/devices/"+boxMAC+"/forget-check", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "has not been checked") {
		t.Errorf("forgetting again: status %d", rec.Code)
	}
}

func TestDevicesAPIChecks(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ta.checkedBoxes()
	h := []string{csrfHeader, csrf}

	rec := ta.apiDo("GET", "devices", "", c)
	var list struct{ Devices []apiDevice }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("status %d, %s", rec.Code, rec.Body)
	}
	found := 0
	for _, d := range list.Devices {
		if d.PlaybackCheck == nil {
			continue
		}
		found++
		pc := d.PlaybackCheck
		if d.MAC != boxMAC || pc.Firmware != "2.20.16-322" || !pc.CheckedAt.Equal(ta.clock.Add(-2*time.Hour)) ||
			strings.Join(pc.Plays, ", ") != "MKV, Dolby Digital Plus" || strings.Join(pc.DoesNotPlay, ", ") != "DTS" {
			t.Errorf("check %+v of %s", pc, d.MAC)
		}
	}
	if found != 1 || !strings.Contains(rec.Body.String(), `"playback_check": null`) {
		t.Errorf("%d checks: %s", found, rec.Body)
	}
	rec = ta.apiDo("PATCH", "devices/"+boxMAC, `{"label": "Lounge"}`, c, h...)
	var d apiDevice
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil || d.PlaybackCheck == nil {
		t.Errorf("a changed device without its check: %s", rec.Body)
	}

	if rec = ta.apiDo("DELETE", "devices/"+boxMAC+"/playback-check", "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("forget: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("DELETE", "devices/"+boxMAC+"/playback-check", "", c, h...), http.StatusNotFound, "no_check")
	apiErrorOf(t, ta.apiDo("DELETE", "devices/00:1A:79:00:00:77/playback-check", "", c, h...), http.StatusNotFound, "no_such_device")
	apiErrorOf(t, ta.apiDo("DELETE", "devices/"+boxMAC+"/playback-check", "", c), http.StatusForbidden, "csrf")
}
