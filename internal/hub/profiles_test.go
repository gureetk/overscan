// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// A player whose account's output profile transcodes gets H.264 with AAC
// sound from the hub's ffmpeg, while transcoding is on and has room; and
// the stream as its source sends it otherwise, with the log saying why
// (D110).
func TestOutputProfileWithFFmpeg(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	program := encode(t, ffmpeg, "testsrc", 4, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/program/{n}", func(w http.ResponseWriter, r *http.Request) { w.Write(program) })
	srv := newServer(t, mux)
	h := newHarnessWith(t, 0, srv.URL, []string{"/program/1", "/program/2"})
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseEncoder(Encoder{H264: true})
	ctx := context.Background()
	if err := h.hub.store.SetAccountOutputProfile(ctx, store.HouseholdAccount, "h264-720p"); err != nil {
		t.Fatal(err)
	}
	vlc := h.hub.Player(store.HouseholdAccount, "m3u", "192.0.2.5", "VLC/3.0")
	tivi := h.hub.Player(store.HouseholdAccount, "xtream", "192.0.2.6", "TiviMate/5")
	play := func(player int64, name string) []byte {
		t.Helper()
		resp := get(t, h.front.URL+h.hub.Link(player, h.entries[name]))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %s", resp.Status)
		}
		return []byte(body(t, resp))
	}

	// Transcoding is off: as the source sends it.
	if got := play(vlc, "program/1"); !bytes.Equal(got, program) {
		t.Errorf("with transcoding off, got %d bytes, want the source's %d", len(got), len(program))
	}
	h.waitIdle()
	if !strings.Contains(h.logs.String(), `msg="a stream's output profile transcodes it, but transcoding is off; it plays as its source sends it" channel=program/1 source=main device=`) {
		t.Errorf("logs:\n%s", h.logs)
	}

	// On: H.264 and AAC.
	if err := h.hub.store.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	r := report(play(vlc, "program/1"))
	if len(r.Streams) != 2 || r.Streams[0].Codec != "H.264" || r.Streams[1].Codec != "AAC" {
		t.Errorf("transcoded streams: %+v", r.Streams)
	}
	h.waitIdle()
	if !strings.Contains(h.logs.String(), `msg="transcoding a stream with ffmpeg" channel=program/1 source=main device=`) ||
		!strings.Contains(h.logs.String(), "profile=h264-720p") {
		t.Errorf("logs:\n%s", h.logs)
	}

	// With another player's transcode running, the limit of one leaves
	// none for this one.
	h.hub.encodeMu.Lock()
	h.hub.encodes[tivi]++
	h.hub.encodeMu.Unlock()
	if got := play(vlc, "program/2"); !bytes.Equal(got, program) {
		t.Errorf("at the limit, got %d bytes, want the source's %d", len(got), len(program))
	}
	h.waitIdle()
	if !strings.Contains(h.logs.String(), `msg="the server transcodes as many streams as it may; this one plays as its source sends it"`) {
		t.Errorf("logs:\n%s", h.logs)
	}
	h.hub.endEncode(tivi)

	// Every transcode is counted out once its stream ends.
	h.hub.encodeMu.Lock()
	n := len(h.hub.encodes)
	h.hub.encodeMu.Unlock()
	if n != 0 {
		t.Errorf("transcodes still counted: %d", n)
	}
}
