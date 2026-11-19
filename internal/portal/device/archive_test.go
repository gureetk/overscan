// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/store"
)

// withArchive gives Channel 0 an archive of a day, which its playlist
// describes, and the guide of addGuide. It returns the channels' IDs by
// name.
func (h *harness) withArchive() map[string]int64 {
	h.t.Helper()
	h.catalog[0].ArchiveDays = 1
	h.catalog[0].Catchup = catchup.Playlist{Mode: catchup.ModeShift}
	if _, err := h.store.ApplyCatalog(context.Background(), h.srcID, h.catalog, h.now); err != nil {
		h.t.Fatal(err)
	}
	return h.addGuide()
}

func TestPlayArchive(t *testing.T) {
	h := newHarness(t)
	ids := h.withArchive()
	h.approve(macA)
	token := h.session(macA).Token

	var list channelsResponse
	h.call("GET", Prefix+"channels", token, "", &list)
	for _, c := range list.Channels {
		if want := map[bool]int{true: 1}[c.ID == ids["Channel 0"]]; c.Archive != want {
			t.Errorf("%s: archive %d, want %d", c.Name, c.Archive, want)
		}
	}

	ask := func(id int64, body string) (int, archiveResponse, string) {
		t.Helper()
		var out json.RawMessage
		code := h.call("POST", fmt.Sprintf("%schannels/%d/archive", Prefix, id), token, body, &out)
		var a archiveResponse
		var e struct{ Error string }
		_ = json.Unmarshal(out, &a)
		_ = json.Unmarshal(out, &e)
		return code, a, e.Error
	}
	// P0 began two hours ago and P1 an hour ago; P2 is on now.
	p0 := h.now.Add(-2 * time.Hour)
	code, a, _ := ask(ids["Channel 0"], fmt.Sprintf(`{"start":%d}`, p0.Unix()))
	if code != http.StatusOK {
		t.Fatalf("P0: %d", code)
	}
	if a.Solution != "ffrt" || a.Seek != "restart" || a.Duration != 3600 || a.Programme.Title != "P0" ||
		!strings.HasPrefix(a.URL, h.front.URL+"/archive/") || !strings.HasSuffix(a.URL, ".ts") ||
		!strings.HasPrefix(a.Start, h.front.URL+"/archive/") || !strings.HasSuffix(a.Start, "/start") {
		t.Errorf("P0: %+v", a)
	}
	// The link plays, through the hub.
	resp, err := http.Get(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 188*2)); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("playing it: %s, %v", resp.Status, err)
	}
	resp.Body.Close()

	for name, tt := range map[string]struct {
		id   int64
		body string
		code int
		err  string
	}{
		"on now":                {ids["Channel 0"], fmt.Sprintf(`{"start":%d}`, h.now.Unix()), http.StatusNotFound, errNoArchive},
		"not a programme start": {ids["Channel 0"], fmt.Sprintf(`{"start":%d}`, p0.Unix()+60), http.StatusNotFound, errNoArchive},
		"a channel without one": {ids["Channel 1"], fmt.Sprintf(`{"start":%d}`, h.now.Add(-time.Minute).Unix()), http.StatusNotFound, errNoArchive},
		"an unknown channel":    {999999, fmt.Sprintf(`{"start":%d}`, p0.Unix()), http.StatusNotFound, errNotFound},
		"no start":              {ids["Channel 0"], `{}`, http.StatusBadRequest, errBadRequest},
		"not JSON":              {ids["Channel 0"], `start`, http.StatusBadRequest, errBadRequest},
	} {
		if code, _, e := ask(tt.id, tt.body); code != tt.code || e != tt.err {
			t.Errorf("%s: %d %s, want %d %s", name, code, e, tt.code, tt.err)
		}
	}

	// A programme older than the archive cannot be played.
	h.now = h.now.Add(22*time.Hour + time.Minute)
	h.api.now = func() time.Time { return h.now }
	if code, _, e := ask(ids["Channel 0"], fmt.Sprintf(`{"start":%d}`, p0.Unix())); code != http.StatusNotFound || e != errNoArchive {
		t.Errorf("older than the archive: %d %s", code, e)
	}
}

func TestPlayArchiveOfALockedChannel(t *testing.T) {
	h := newHarness(t)
	ids := h.withArchive()
	h.approve(macA)
	token := h.session(macA).Token
	ctx := context.Background()
	if err := h.store.SetEntriesLocked(ctx, store.DefaultLineupID, []int64{ids["Channel 0"]}, true); err != nil {
		t.Fatal(err)
	}
	hash, err := admin.HashPassword(ctx, "2468", admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetAccountPIN(ctx, 1, hash); err != nil {
		t.Fatal(err)
	}
	start := h.now.Add(-2 * time.Hour).Unix()
	path := fmt.Sprintf("%schannels/%d/archive", Prefix, ids["Channel 0"])
	for body, want := range map[string]string{
		fmt.Sprintf(`{"start":%d}`, start):              errPINRequired,
		fmt.Sprintf(`{"start":%d,"pin":"1357"}`, start): errPINWrong,
	} {
		if code, e := h.errorCodeBody("POST", path, token, body); code != http.StatusForbidden || e != want {
			t.Errorf("%s: %d %s, want %s", body, code, e, want)
		}
	}
	var a archiveResponse
	if code := h.call("POST", path, token, fmt.Sprintf(`{"start":%d,"pin":"2468"}`, start), &a); code != http.StatusOK || a.URL == "" {
		t.Errorf("the right PIN: %d %+v", code, a)
	}
}
