// SPDX-License-Identifier: AGPL-3.0-or-later

package m3u

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// An account's HLS for players (D153): off, its playlist gives .ts
// addresses whatever it is asked, and a channel has no .m3u8; on, the
// playlist read with ?output=m3u8 gives .m3u8 addresses, and one read
// without it .ts as before; a channel that the hub cannot cut, here for
// want of a folder for buffers, plays at its .m3u8 address as at its .ts.
func TestPlaylistHLS(t *testing.T) {
	h := newHarness(t)
	a := h.ids[`Alpha "One"`]
	if a == 0 {
		t.Fatal("no Alpha")
	}
	hlsLink := fmt.Sprintf("/playlist/%s/%d.m3u8", h.token, a)
	tsLink := fmt.Sprintf("/playlist/%s/%d.ts", h.token, a)
	has := func(query, link string) bool {
		_, body := h.get("/playlist/" + h.token + ".m3u" + query)
		return strings.Contains(body, h.front.URL+link+"\n")
	}
	if !has("?output=m3u8", tsLink) || has("?output=m3u8", hlsLink) {
		t.Error("the playlist with output=m3u8 while HLS is off")
	}
	if resp, _ := h.get(hlsLink); resp.StatusCode != http.StatusNotFound {
		t.Errorf(".m3u8 while off: %s", resp.Status)
	}

	if err := h.store.SetAccountOutputs(context.Background(), store.HouseholdAccount, store.Outputs{M3U: true, HLS: true}); err != nil {
		t.Fatal(err)
	}
	if !has("?output=m3u8", hlsLink) || has("?output=m3u8", tsLink) {
		t.Error("the playlist with output=m3u8 while HLS is on")
	}
	if !has("", tsLink) || has("", hlsLink) {
		t.Error("the playlist without output while HLS is on")
	}
	// A radio station keeps its stream's address.
	if !has("?output=m3u8", fmt.Sprintf("/playlist/%s/%d.ts", h.token, h.ids["Radio <Three>"])) {
		t.Error("the radio station's address in the playlist with output=m3u8")
	}
	resp, err := http.Get(h.front.URL + hlsLink)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 188)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || resp.Header.Get("Content-Type") != "video/mp2t" || buf[0] != 0x47 {
		t.Errorf(".m3u8 of a channel that the hub cannot cut: %s %s %v", resp.Status, resp.Header.Get("Content-Type"), err)
	}
	for _, bad := range []string{"/0.m3u8", "/x.m3u8", fmt.Sprintf("/%d.m3u8.ts", a), fmt.Sprintf("/0%d.m3u8", a)} {
		if resp, _ := h.get("/playlist/" + h.token + bad); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %s", bad, resp.Status)
		}
	}
}
