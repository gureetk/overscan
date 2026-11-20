// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A box whose account has passed its end date gets no session, and its
// calls are refused; a later end date, or none, lets it in again (D95).
func TestExpiredAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	ends := h.now.Add(time.Hour)
	if err := h.store.SetAccountExpiry(ctx, 1, ends); err != nil {
		t.Fatal(err)
	}
	s := h.session(macA)
	if s.State != "approved" || s.Token == "" || s.Ends != ends.Unix() {
		t.Fatalf("before its end: %+v", s)
	}
	token := s.Token
	if code := h.call("GET", Prefix+"channels/version", token, "", nil); code != http.StatusOK {
		t.Fatalf("before its end: status %d", code)
	}

	h.now = ends
	for _, path := range []string{"channels", "channels/version", "settings", "profiles"} {
		if code, e := h.errorCode("GET", Prefix+path, token); code != http.StatusForbidden || e != errExpired {
			t.Errorf("GET %s once it has ended: %d %q", path, code, e)
		}
	}
	if code, e := h.errorCodeBody("POST", Prefix+"channels/1/play", token, "{}"); code != http.StatusForbidden || e != errExpired {
		t.Errorf("play once it has ended: %d %q", code, e)
	}
	s = h.session(macA)
	if s.State != stateExpired || s.Token != "" || s.Ends != ends.Unix() || s.Label != "Living room" {
		t.Errorf("a session once it has ended: %+v", s)
	}
	if n := strings.Count(h.logs.String(), "device session started"); n != 1 {
		t.Errorf("%d sessions started, want the one before the end", n)
	}

	// No end date: in again, with a new session.
	if err := h.store.SetAccountExpiry(ctx, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if s = h.session(macA); s.State != "approved" || s.Token == "" || s.Ends != 0 {
		t.Errorf("without an end date: %+v", s)
	}
	if code := h.call("GET", Prefix+"channels/version", s.Token, "", nil); code != http.StatusOK {
		t.Errorf("without an end date: status %d", code)
	}
}

// A box approved before it first loaded the page is paired at its first
// session even after its account ended, so that it keeps its secret.
func TestExpiredAccountStillPairs(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	if err := h.store.SetAccountExpiry(context.Background(), 1, h.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	s := h.session(macA)
	if s.State != stateExpired || s.Secret == "" || s.Token != "" {
		t.Errorf("first session: %+v", s)
	}
	if d, err := h.store.Device(context.Background(), macA); err != nil || !d.Paired() {
		t.Errorf("not paired: %+v, %v", d, err)
	}
}
