// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// A box gets the administrator's messages with the lineup and its version,
// and each goes once it has been read (D96).
func TestMessages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	h.approve(macB)
	token := h.session(macA).Token
	other := h.session(macB).Token
	a, err := h.store.Device(ctx, macA)
	if err != nil {
		t.Fatal(err)
	}

	var version struct {
		Version  string
		Messages []boxMessage
	}
	h.call("GET", Prefix+"channels/version", token, "", &version)
	if version.Version == "" || len(version.Messages) != 0 {
		t.Fatalf("before any message: %+v", version)
	}
	every, err := h.store.AddMessage(ctx, 1, 0, "Dinner is ready.\nCome down.", h.now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.AddMessage(ctx, 1, a.ID, "<b>Bedtime</b> & lights out.", h.now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	var lineup channelsResponse
	h.call("GET", Prefix+"channels", token, "", &lineup)
	if len(lineup.Messages) != 2 || lineup.Messages[0].Text != "Dinner is ready.\nCome down." ||
		lineup.Messages[1].Text != "<b>Bedtime</b> & lights out." || lineup.Messages[0].Sent != h.now.Unix() {
		t.Fatalf("with the lineup: %+v", lineup.Messages)
	}
	h.call("GET", Prefix+"channels/version", token, "", &version)
	if len(version.Messages) != 2 || version.Messages[0].ID != lineup.Messages[0].ID {
		t.Fatalf("with the version: %+v", version.Messages)
	}

	// Reading one: it goes, from this box alone.
	first := lineup.Messages[0].ID
	path := fmt.Sprintf("%smessages/%d/read", Prefix, first)
	if code := h.call("POST", path, token, "", nil); code != http.StatusOK {
		t.Fatalf("read: %d", code)
	}
	if code := h.call("POST", path, token, "", nil); code != http.StatusOK {
		t.Errorf("read again: %d", code)
	}
	version.Messages = nil
	h.call("GET", Prefix+"channels/version", token, "", &version)
	if len(version.Messages) != 1 || version.Messages[0].ID == first {
		t.Errorf("after reading: %+v", version.Messages)
	}
	version.Messages = nil
	h.call("GET", Prefix+"channels/version", other, "", &version)
	if len(version.Messages) != 1 || version.Messages[0].Text != "Dinner is ready.\nCome down." {
		t.Errorf("the other box: %+v", version.Messages)
	}
	if code, e := h.errorCodeBody("POST", fmt.Sprintf("%smessages/%d/read", Prefix, version.Messages[0].ID), token, ""); code != http.StatusNotFound || e != errNotFound {
		t.Errorf("reading another box's: %d %q", code, e)
	}
	if code, _ := h.errorCodeBody("POST", Prefix+"messages/x/read", token, ""); code != http.StatusNotFound {
		t.Errorf("a message that is not a number: %d", code)
	}
	if m, err := h.store.Message(ctx, 1, every.ID); err != nil || m.ReadBoxes != 1 || m.Boxes != 2 {
		t.Errorf("as the admin sees it: %+v, %v", m, err)
	}
}
