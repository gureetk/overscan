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

// pairedBoxes approves the waiting box by its code, which pairs it, and the
// Stalker client by its MAC address.
func (ta *testAdmin) pairedBoxes() {
	ta.t.Helper()
	ctx := context.Background()
	ta.waitingDevices()
	if _, err := ta.approveCode(ctx, boxCode, householdAccount, "Living room"); err != nil {
		ta.t.Fatal(err)
	}
	if _, err := ta.approveDevice(ctx, clientMAC, householdAccount, "Phone"); err != nil {
		ta.t.Fatal(err)
	}
}

func TestMessagesOnTheDevicesPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}

	if body := ta.do("GET", "/admin/devices", nil, c).Body.String(); !strings.Contains(body, "No box runs Overscan&#39;s page yet") &&
		!strings.Contains(body, "No box runs Overscan's page yet") {
		t.Error("with no box, the page does not say there is none to send to")
	}
	ta.pairedBoxes()
	body := ta.do("GET", "/admin/devices", nil, c).Body.String()
	if !strings.Contains(body, `<option value="`+boxMAC+`">To Living room</option>`) || strings.Contains(body, `<option value="`+clientMAC+`"`) {
		t.Error("the boxes to send to are not the paired box alone")
	}

	rec := ta.do("POST", "/admin/messages", url.Values{"csrf": {csrf}, "text": {"Dinner is ready.\r\nCome down."}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sent the message. Boxes show it within five minutes.") {
		t.Fatalf("send: status %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, `<p class="message-text">Dinner is ready.`+"\n"+`Come down.</p>`) || !strings.Contains(body, "To every box · sent just now · Waiting") {
		t.Error("the list does not show the message as sent")
	}
	list, err := ta.st.Messages(context.Background(), householdAccount, 10)
	if err != nil || len(list) != 1 || !list[0].EveryBox || list[0].Boxes != 1 {
		t.Fatalf("messages %+v, %v", list, err)
	}

	// Markup is text, on the page as on the boxes.
	rec = ta.do("POST", "/admin/messages", url.Values{"csrf": {csrf}, "text": {`<script>alert(1)</script>`}, "to": {boxMAC}}, c, hx...)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<script>alert(1)</script>") ||
		!strings.Contains(rec.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") || !strings.Contains(rec.Body.String(), "To Living room") {
		t.Errorf("to one box: status %d", rec.Code)
	}

	for _, bad := range []url.Values{
		{"csrf": {csrf}, "text": {"  "}},
		{"csrf": {csrf}, "text": {strings.Repeat("x", store.MaxMessageText+1)}},
		{"csrf": {csrf}, "text": {"a\x07b"}},
	} {
		if rec = ta.do("POST", "/admin/messages", bad, c, hx...); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "A message has 1 to 500 characters") {
			t.Errorf("%q: status %d", bad.Get("text"), rec.Code)
		}
	}
	rec = ta.do("POST", "/admin/messages", url.Values{"csrf": {csrf}, "text": {"Hello"}, "to": {clientMAC}}, c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Stalker clients get none") || !strings.Contains(rec.Body.String(), ">Hello</textarea>") {
		t.Errorf("to a Stalker client: status %d", rec.Code)
	}

	// Deleting.
	rec = ta.do("POST", fmt.Sprintf("/admin/messages/%d/delete", list[0].ID), url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Deleted the message.") {
		t.Errorf("delete: status %d", rec.Code)
	}
	if rec = ta.do("POST", fmt.Sprintf("/admin/messages/%d/delete", list[0].ID), url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("delete again: status %d", rec.Code)
	}
	if rec = ta.do("POST", "/admin/messages/x/delete", url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("a message that is not a number: status %d", rec.Code)
	}
	if rec = ta.do("POST", "/admin/messages", url.Values{"text": {"No token"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}
}

func TestMessagesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	h := []string{csrfHeader, csrf}

	apiErrorOf(t, ta.apiDo("POST", "messages", `{"text": "Hello"}`, c, h...), http.StatusConflict, "no_boxes")
	ta.pairedBoxes()

	var m apiMessage
	rec := ta.apiDo("POST", "messages", `{"text": "Hello", "mac": "00-1a-79-00-00-01"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil || rec.Code != http.StatusCreated || m.EveryBox || m.MAC == nil || *m.MAC != boxMAC ||
		m.Boxes != 1 || m.Read != 0 || !m.Expires.Equal(m.SentAt.Add(store.MessageLife)) {
		t.Fatalf("send: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "messages", `{"text": ""}`, c, h...), http.StatusBadRequest, "bad_text")
	apiErrorOf(t, ta.apiDo("POST", "messages", `{"text": "Hello", "mac": "nonsense"}`, c, h...), http.StatusBadRequest, "bad_mac")
	apiErrorOf(t, ta.apiDo("POST", "messages", `{"text": "Hello", "mac": "00:1A:79:00:00:09"}`, c, h...), http.StatusNotFound, "no_such_device")
	apiErrorOf(t, ta.apiDo("POST", "messages", `{"text": "Hello", "mac": "`+clientMAC+`"}`, c, h...), http.StatusConflict, "not_a_box")
	apiErrorOf(t, ta.apiDo("POST", "messages", `{"text": "Hello"}`, c), http.StatusForbidden, "csrf")

	var list struct {
		Messages []apiMessage `json:"messages"`
	}
	if rec = ta.apiDo("GET", "messages", "", c); json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Messages) != 1 || list.Messages[0].ID != m.ID {
		t.Errorf("list: %s", rec.Body)
	}
	if rec = ta.apiDo("DELETE", fmt.Sprintf("messages/%d", m.ID), "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d", rec.Code)
	}
	apiErrorOf(t, ta.apiDo("DELETE", fmt.Sprintf("messages/%d", m.ID), "", c, h...), http.StatusNotFound, "no_such_message")
}

// A message goes to the boxes of every account, or to one box of any
// account.
func TestMessagesToEveryAccount(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	ta.pairedBoxes()
	sam, err := ta.st.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	const samBox = "00:1A:79:00:00:03"
	if _, err := ta.st.RequestPairing(ctx, samBox, "MAG322", "135791", store.HashSecret("sam's secret"), ta.clock); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.approveCode(ctx, "135791", sam.ID, "Sam's room"); err != nil {
		t.Fatal(err)
	}
	page := ta.do("GET", "/admin/devices", nil, c).Body.String()
	mustContain(t, "the form", page, `To Living room (Household)</option>`, `To Sam&#39;s room (Sam)</option>`)

	rec := ta.do("POST", "/admin/messages", url.Values{"csrf": {csrf}, "text": {"Dinner is ready."}, "to": {""}}, c, hx...)
	mustContain(t, "to every box", rec.Body.String(), "Sent the message to 2 boxes.")
	rec = ta.do("POST", "/admin/messages", url.Values{"csrf": {csrf}, "text": {"Bedtime."}, "to": {samBox}}, c, hx...)
	mustContain(t, "to Sam's box", rec.Body.String(), "Sent the message.", "To Sam&#39;s room")
	d, err := ta.st.Device(ctx, samBox)
	if err != nil {
		t.Fatal(err)
	}
	waiting, err := ta.st.BoxMessages(ctx, d.ID, ta.clock)
	if err != nil || len(waiting) != 2 {
		t.Errorf("Sam's box waits for %+v, %v", waiting, err)
	}
	// Deleting the message to every box takes it from both accounts' boxes.
	list, err := ta.st.Messages(ctx, 0, 10)
	if err != nil || len(list) != 2 {
		t.Fatalf("messages: %+v, %v", list, err)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/messages/%d/delete", list[1].ID), url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK {
		t.Errorf("delete: status %d", rec.Code)
	}
	if waiting, _ := ta.st.BoxMessages(ctx, d.ID, ta.clock); len(waiting) != 1 {
		t.Errorf("after the delete, Sam's box waits for %+v", waiting)
	}
}
