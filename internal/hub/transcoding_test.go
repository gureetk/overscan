// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// What is transcoded (D147): a channel set to always transcodes with its
// profile for a player that takes channels as their sources send them; a
// group set to never sends its channels as their sources send them, whatever
// the player's profile; and a player whose own profile transcodes keeps it.
func TestWhatIsTranscodedWithFFmpeg(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	program := encode(t, ffmpeg, "testsrc", 4, 0)
	mux := http.NewServeMux()
	mux.HandleFunc("/program/{n}", func(w http.ResponseWriter, r *http.Request) { w.Write(program) })
	srv := newServer(t, mux)
	h := newHarnessWith(t, 0, srv.URL, []string{"/program/1", "/program/2"})
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseEncoder(Encoder{H264: true})
	ctx := context.Background()
	st := h.hub.store
	if err := st.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	small, err := streamprofile.Check(streamprofile.Profile{Name: "small", MaxLines: 576, Quality: streamprofile.Low})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTranscodeProfile(ctx, small); err != nil {
		t.Fatal(err)
	}
	one, two := h.entries["program/1"], h.entries["program/2"]
	if err := st.SetEntriesTranscode(ctx, one.LineupID, []int64{one.EntryID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	vlc := h.hub.Player(store.HouseholdAccount, "m3u", "192.0.2.5", "VLC/3.0")
	play := func(name string) []byte {
		t.Helper()
		resp := get(t, h.front.URL+h.hub.Link(vlc, h.entries[name]))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status %s", resp.Status)
		}
		defer h.waitIdle()
		return []byte(body(t, resp))
	}

	// The channel's always, for a player that takes the source as it is.
	r := report(play("program/1"))
	if len(r.Streams) != 2 || r.Streams[0].Codec != "H.264" || r.Streams[1].Codec != "AAC" {
		t.Errorf("transcoded streams: %+v", r.Streams)
	}
	if !strings.Contains(h.logs.String(), `msg="transcoding a stream with ffmpeg" channel=program/1 source=main device=`) ||
		!strings.Contains(h.logs.String(), "profile=small set_by=channel") {
		t.Errorf("logs:\n%s", h.logs)
	}
	if !strings.Contains(h.logs.String(), `msg="ffmpeg's transcode of a live stream ended" channel=program/1`) {
		t.Errorf("no end of the transcode in the logs:\n%s", h.logs)
	}
	// The other channel says nothing: as its source sends it.
	if got := play("program/2"); !bytes.Equal(got, program) {
		t.Errorf("program/2: got %d bytes, want the source's %d", len(got), len(program))
	}

	// The player's own profile transcodes: it keeps it, over the channel's
	// always.
	if err := st.SetAccountOutputProfile(ctx, store.HouseholdAccount, "h264-720p"); err != nil {
		t.Fatal(err)
	}
	h.logs.Reset()
	report(play("program/1"))
	if !strings.Contains(h.logs.String(), "profile=h264-720p set_by=\"device or account\"") {
		t.Errorf("logs:\n%s", h.logs)
	}

	// The group's never wins over the player's profile.
	group, err := st.CreateLineupGroup(ctx, two.LineupID, "Sport")
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetEntriesGroup(ctx, two.LineupID, []int64{two.EntryID}, group); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLineupGroupTranscode(ctx, two.LineupID, group, store.Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	h.logs.Reset()
	if got := play("program/2"); !bytes.Equal(got, program) {
		t.Errorf("never: got %d bytes, want the source's %d", len(got), len(program))
	}
	if !strings.Contains(h.logs.String(), `msg="a stream plays as its source sends it, since it is never transcoded" channel=program/2`) ||
		!strings.Contains(h.logs.String(), "profile=h264-720p set_by=group") || strings.Contains(h.logs.String(), "transcoding a stream") {
		t.Errorf("logs:\n%s", h.logs)
	}
	// So does live TV's, for a channel in a group that says nothing.
	if err := st.SetLineupGroupTranscode(ctx, two.LineupID, group, store.Transcode{}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLiveTranscode(ctx, store.Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	h.logs.Reset()
	if got := play("program/2"); !bytes.Equal(got, program) {
		t.Errorf("live TV's never: got %d bytes, want the source's %d", len(got), len(program))
	}
	if !strings.Contains(h.logs.String(), `set_by="live TV"`) {
		t.Errorf("logs:\n%s", h.logs)
	}
	// A profile that needs an encoder that this ffmpeg lacks: as its
	// source sends it.
	hevc, _ := streamprofile.Check(streamprofile.Profile{Name: "hevc", Codec: streamprofile.HEVC})
	if err := st.AddTranscodeProfile(ctx, hevc); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountOutputProfile(ctx, store.HouseholdAccount, "hevc"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLiveTranscode(ctx, store.Transcode{}); err != nil {
		t.Fatal(err)
	}
	h.logs.Reset()
	if got := play("program/2"); !bytes.Equal(got, program) {
		t.Errorf("without libx265: got %d bytes", len(got))
	}
	if !strings.Contains(h.logs.String(), `lacks the encoder that it needs`) || !strings.Contains(h.logs.String(), "needs=libx265") {
		t.Errorf("logs:\n%s", h.logs)
	}
}

// The watch judges a live transcode behind only when ffmpeg made the video
// slowly and held the stream up, over the last half minute, after it has
// settled (D147).
func TestLiveWatch(t *testing.T) {
	start := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	type step struct {
		rate, waited, held float64 // per second of the clock
	}
	run := func(steps []step, seconds int) (*liveWatch, []int) {
		p := &encodeProgress{done: make(chan struct{})}
		w := newLiveWatch(p, start)
		var changes []int
		var made, waited, held float64
		for i := 1; i <= seconds; i++ {
			s := steps[min(i/30, len(steps)-1)]
			made += s.rate
			waited += s.waited
			held += s.held
			p.made.Store(int64(made * 1e6))
			p.waited = time.Duration(waited * float64(time.Second))
			w.held.Store(int64(held * float64(time.Second)))
			if c := w.note(start.Add(time.Duration(i) * time.Second)); c != 0 {
				changes = append(changes, c)
			}
		}
		return w, changes
	}
	// Keeps up: a second of video in each second that the hub waits.
	if w, c := run([]step{{1, 1, 0}}, 120); len(c) != 0 || w.behind || w.speed < 0.99 {
		t.Errorf("keeping up: %v, behind %v, speed %v", c, w.behind, w.speed)
	}
	// Slow, and it holds the stream up: behind, once, then caught up.
	if w, c := run([]step{{0.8, 1, 0.3}, {0.8, 1, 0.3}, {0.8, 1, 0.3}, {1, 1, 0}, {1, 1, 0}, {1, 1, 0}}, 180); len(c) != 2 || c[0] != 1 || c[1] != -1 || w.behind {
		t.Errorf("slow then fast: %v, behind %v", c, w.behind)
	}
	// The source sends slowly: nothing is held up, so it is not behind.
	if _, c := run([]step{{0.5, 1, 0}}, 120); len(c) != 0 {
		t.Errorf("a slow source: %v", c)
	}
	// A slow player: the hub waits little for ffmpeg, so it cannot tell.
	if _, c := run([]step{{0.2, 0.2, 0.9}}, 120); len(c) != 0 {
		t.Errorf("a slow player: %v", c)
	}
	// Not judged before it settles.
	if _, c := run([]step{{0.5, 1, 0.5}}, 40); len(c) != 0 {
		t.Errorf("before it settled: %v", c)
	}
}
