// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/admin"
)

// A box plays as its account's first profile until it switches; each
// profile keeps its own favourites, and a profile's PIN guards switching
// to it (D94).
func TestProfiles(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	token := h.session(macA).Token

	var got profilesResponse
	if code := h.call("GET", Prefix+"profiles", token, "", &got); code != http.StatusOK || len(got.Profiles) != 1 ||
		got.Profiles[0].Name != "Household" || got.Profile != got.Profiles[0].ID || got.Profiles[0].PIN {
		t.Fatalf("profiles at first: %d %+v", code, got)
	}
	first := got.Profile
	alex, err := h.store.AddProfile(ctx, 1, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	sam, err := h.store.AddProfile(ctx, 1, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := admin.HashPassword(ctx, "1357", admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetProfilePIN(ctx, sam.ID, hash); err != nil {
		t.Fatal(err)
	}
	h.call("GET", Prefix+"profiles", token, "", &got)
	if len(got.Profiles) != 3 || got.Profiles[1].Name != "Alex" || got.Profiles[1].PIN || !got.Profiles[2].PIN {
		t.Fatalf("profiles %+v", got)
	}

	var lineup channelsResponse
	h.call("GET", Prefix+"channels", token, "", &lineup)
	one, two := lineup.Channels[1].ID, lineup.Channels[2].ID
	if code := h.call("PUT", fmt.Sprintf("%schannels/%d/favourite", Prefix, one), token, "", nil); code != http.StatusOK {
		t.Fatalf("mark: %d", code)
	}

	// Alex has no PIN: the box switches at once, and sees Alex's lists.
	if code := h.call("PUT", Prefix+"profile", token, fmt.Sprintf(`{"id": %d}`, alex.ID), &got); code != http.StatusOK || got.Profile != alex.ID {
		t.Fatalf("switch to Alex: %d %+v", code, got)
	}
	h.call("GET", Prefix+"channels", token, "", &lineup)
	if len(lineup.Favourites) != 0 {
		t.Errorf("Alex's favourites %v", lineup.Favourites)
	}
	h.call("PUT", fmt.Sprintf("%schannels/%d/favourite", Prefix, two), token, "", nil)
	if fav, _ := h.store.Favourites(ctx, alex.ID); !reflect.DeepEqual(fav, []int64{two}) {
		t.Errorf("Alex's favourites in the store %v", fav)
	}
	if fav, _ := h.store.Favourites(ctx, first); !reflect.DeepEqual(fav, []int64{one}) {
		t.Errorf("the first profile's favourites %v", fav)
	}
	// Another box still plays as the first profile.
	h.approve(macB)
	var theirs channelsResponse
	h.call("GET", Prefix+"channels", h.session(macB).Token, "", &theirs)
	if !reflect.DeepEqual(theirs.Favourites, []int64{one}) {
		t.Errorf("the other box's favourites %v", theirs.Favourites)
	}

	// Sam has a PIN.
	for body, want := range map[string]string{
		fmt.Sprintf(`{"id": %d}`, sam.ID):                "pin_required",
		fmt.Sprintf(`{"id": %d, "pin": "0000"}`, sam.ID): "pin_wrong",
		`{"id": 999}`: "not_found",
		`{"id": 0}`:   "bad_request",
		`{"id": "x"}`: "bad_request",
	} {
		if code, e := h.errorCodeBody("PUT", Prefix+"profile", token, body); e != want || code < 400 {
			t.Errorf("%s: %d %s, want %s", body, code, e, want)
		}
	}
	if !strings.Contains(h.logs.String(), "wrong profile PIN") {
		t.Errorf("logs: %s", h.logs.String())
	}
	if code := h.call("PUT", Prefix+"profile", token, fmt.Sprintf(`{"id": %d, "pin": "1357"}`, sam.ID), &got); code != http.StatusOK || got.Profile != sam.ID {
		t.Fatalf("switch to Sam: %d %+v", code, got)
	}
	if !strings.Contains(h.logs.String(), "a box switched profile") {
		t.Errorf("logs: %s", h.logs.String())
	}
	// Staying on the profile it plays as asks for no PIN.
	if code := h.call("PUT", Prefix+"profile", token, fmt.Sprintf(`{"id": %d}`, sam.ID), &got); code != http.StatusOK {
		t.Errorf("stay on Sam: %d", code)
	}
	// Removing the profile a box plays as puts it on the first.
	if err := h.store.RemoveProfile(ctx, sam.ID); err != nil {
		t.Fatal(err)
	}
	if h.call("GET", Prefix+"profiles", token, "", &got); got.Profile != first {
		t.Errorf("after its profile went, the box plays as %d", got.Profile)
	}
	d, _ := h.store.Device(ctx, macA)
	if d.ProfileID != first {
		t.Errorf("device %+v", d)
	}
}
