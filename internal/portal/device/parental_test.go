// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/store"
)

// setPIN gives the household account a parental PIN.
func (h *harness) setPIN(pin string) {
	h.t.Helper()
	ctx := context.Background()
	hash, err := admin.HashPassword(ctx, pin, admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.store.SetAccountPIN(ctx, 1, hash); err != nil {
		h.t.Fatal(err)
	}
}

// A locked film is marked, and plays only with the PIN, once the account
// has one; a series locked by its shelf locks its episodes; and the box can
// check the PIN before it shows what is locked (D85).
func TestLockedTitles(t *testing.T) {
	h := newHarness(t)
	_, srv := newLibraryPanel(t)
	token := h.withLibrary(srv)
	ctx := context.Background()
	var movies, series libraryResponse
	h.call("GET", Prefix+"movies", token, "", &movies)
	h.call("GET", Prefix+"series", token, "", &series)
	var recent shelfResponse
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	ids := map[string]int64{}
	for _, r := range recent.Titles {
		ids[r.Name] = r.ID
	}
	if _, err := h.store.SetTitlesLocked(ctx, []int64{ids["The Long Field"]}, true); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetShelfLocked(ctx, store.KindSeries, "name:Documentary", true); err != nil {
		t.Fatal(err)
	}
	film := fmt.Sprintf("%smovies/%d", Prefix, ids["The Long Field"])
	lockedRows := func() map[string]bool {
		t.Helper()
		var part shelfResponse
		h.call("GET", Prefix+"movies/shelves/recent", token, "", &part)
		out := map[string]bool{}
		for _, r := range part.Titles {
			out[r.Name] = r.Locked
		}
		return out
	}

	// Without a PIN, locks do nothing.
	if got := lockedRows(); got["The Long Field"] {
		t.Errorf("marked without a PIN: %v", got)
	}
	if code := h.call("POST", film+"/play", token, "", nil); code != http.StatusOK {
		t.Errorf("play without a PIN to ask: %d", code)
	}
	var ok struct{ OK bool }
	if code := h.call("POST", Prefix+"pin", token, `{"pin":"0000"}`, &ok); code != http.StatusOK || !ok.OK {
		t.Errorf("a PIN checked without one set: %d %+v", code, ok)
	}

	h.setPIN("2468")
	if got := lockedRows(); !got["The Long Field"] || got["Quiet Harbour"] {
		t.Errorf("marked: %v", got)
	}
	var opened titleResponse
	if code := h.call("GET", film, token, "", &opened); code != http.StatusOK || !opened.Locked {
		t.Errorf("opened: %d %+v", code, opened)
	}
	for body, want := range map[string]string{`{}`: errPINRequired, `{"pin":"1357"}`: errPINWrong} {
		if code, e := h.errorCodeBody("POST", film+"/play", token, body); code != http.StatusForbidden || e != want {
			t.Errorf("play with %s: %d %s, want %s", body, code, e, want)
		}
	}
	var link itemPlayResponse
	if code := h.call("POST", film+"/play", token, `{"pin":"2468"}`, &link); code != http.StatusOK || link.URL == "" {
		t.Errorf("play with the PIN: %d %+v", code, link)
	}
	if code := h.call("POST", Prefix+"pin", token, `{"pin":"2468"}`, &ok); code != http.StatusOK || !ok.OK {
		t.Errorf("the right PIN: %d", code)
	}
	if code, e := h.errorCodeBody("POST", Prefix+"pin", token, `{"pin":"1111"}`); code != http.StatusForbidden || e != errPINWrong {
		t.Errorf("a wrong PIN: %d %s", code, e)
	}
	if code, e := h.errorCodeBody("POST", Prefix+"pin", token, `pin`); code != http.StatusBadRequest || e != errBadRequest {
		t.Errorf("not JSON: %d %s", code, e)
	}

	// The series' shelf is locked: so are its episodes.
	var docs shelfResponse
	h.call("GET", Prefix+"series/shelves/"+shelfID(t, series, "Documentary"), token, "", &docs)
	if len(docs.Titles) != 1 || !docs.Titles[0].Locked {
		t.Fatalf("series %+v", docs.Titles)
	}
	var coast titleResponse
	h.call("GET", fmt.Sprintf("%sseries/%d", Prefix, docs.Titles[0].ID), token, "", &coast)
	if !coast.Locked || len(coast.Seasons) == 0 {
		t.Fatalf("series opened %+v", coast)
	}
	episode := fmt.Sprintf("%sepisodes/%d/play", Prefix, coast.Seasons[0].Episodes[0].ID)
	if code, e := h.errorCodeBody("POST", episode, token, `{}`); code != http.StatusForbidden || e != errPINRequired {
		t.Errorf("an episode without the PIN: %d %s", code, e)
	}
	if code := h.call("POST", episode, token, `{"pin":"2468"}`, nil); code != http.StatusOK {
		t.Errorf("an episode with the PIN: %d", code)
	}

	// Five wrong PINs, of either call, and the box waits.
	for i := 0; i < 5; i++ {
		h.errorCodeBody("POST", Prefix+"pin", token, `{"pin":"1111"}`)
	}
	if code, e := h.errorCodeBody("POST", film+"/play", token, `{"pin":"2468"}`); code != http.StatusTooManyRequests || e != errPINLocked {
		t.Errorf("after five wrong: %d %s", code, e)
	}
}
