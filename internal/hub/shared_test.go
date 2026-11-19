// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sharedHarness is a hub with the shared tuner on and timeshift set to
// the given minutes, and two channels from a burst upstream.
func sharedHarness(t *testing.T, limit, timeshift int) (*harness, *burstUpstream) {
	t.Helper()
	h, u := shiftHarness(t, limit)
	ctx := context.Background()
	if err := h.hub.store.SetTimeshift(ctx, timeshift); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.store.SetSharedTuner(ctx, true); err != nil {
		t.Fatal(err)
	}
	return h, u
}

// tunerOf returns the shared tuner that plays a channel, or nil.
func (h *harness) tunerOf(name string) *session {
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	return h.hub.shared[h.entries[name].ChannelID]
}

// Two boxes and a recording on one channel play it from one connection to
// the source, within a limit of one.
func TestSharedTunerShares(t *testing.T) {
	h, u := sharedHarness(t, 1, 0)
	u.bursts <- tsBurst(1, 9)
	first := get(t, h.link(7, "tv/1"))
	if first.StatusCode != http.StatusOK || first.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("first: %s", first.Status)
	}
	defer first.Body.Close()
	if p := readPackets(t, first.Body, 2); !isPAT(p) || p[tsPacket+5] != 1 {
		t.Errorf("first's packets %x", p[:8])
	}
	if !h.hub.AvailableChannel(context.Background(), h.entries["tv/1"], 8) {
		t.Error("the shared channel is not available to another box")
	}
	if h.hub.AvailableChannel(context.Background(), h.entries["tv/2"], 8) {
		t.Error("another channel is available to another box at the limit")
	}
	second := get(t, h.link(8, "tv/1"))
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second: %s", second.Status)
	}
	defer second.Body.Close()
	// It starts from the buffer's last place to start from, the table.
	if p := readPackets(t, second.Body, 2); !isPAT(p) || p[tsPacket+5] != 1 {
		t.Errorf("second's packets %x", p[:8])
	}
	recording := get(t, h.link(-5, "tv/1"))
	if recording.StatusCode != http.StatusOK {
		t.Fatalf("recording: %s", recording.Status)
	}
	defer recording.Body.Close()
	readPackets(t, recording.Body, 2)

	h.advance(time.Second)
	u.bursts <- tsBurst(2, 9)
	for i, resp := range []*http.Response{first, second, recording} {
		// The rest of the first burst, with the packet that the repeat
		// filter held until the next came, then the second.
		readPackets(t, resp.Body, 8)
		if p := readPackets(t, resp.Body, 2); !isPAT(p) || p[tsPacket+5] != 2 {
			t.Errorf("reader %d: %x", i, p[:8])
		}
	}
	if requests, open := u.counts(); requests != 1 || open != 1 {
		t.Errorf("%d requests to the source, %d open", requests, open)
	}
	streams := h.hub.Streams()
	if len(streams) != 3 {
		t.Fatalf("streams %+v", streams)
	}
	for _, s := range streams {
		if !s.Shared || s.Timeshift || s.Bytes == 0 || s.Channel != "tv/1" || !s.Playing {
			t.Errorf("stream %+v", s)
		}
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "a channel plays from a shared tuner") || !strings.Contains(logs, "shared_by=3") {
		t.Errorf("logs: %s", logs)
	}
}

// A device's new stream replaces its own: a tuner that only its screens
// play from ends to make room, and one that another device's screen plays
// from does not.
func TestSharedTunerMakesRoom(t *testing.T) {
	h, u := sharedHarness(t, 1, 0)
	u.bursts <- tsBurst(1, 9)
	box := h.play(h.link(7, "tv/1"))
	defer box.Body.Close()
	recording := h.play(h.link(-5, "tv/1"))
	if resp := get(t, h.link(7, "tv/2")); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("another channel while a recording shares the tuner: %s", resp.Status)
	}
	if h.hub.Available(h.entries["tv/2"].SourceID, 7) {
		t.Error("the source is available to the box while a recording shares its tuner")
	}

	// The recording stops: the tuner is the box's own.
	recording.Body.Close()
	waitFor(t, "the recording to stop", func() bool {
		h.hub.mu.Lock()
		defer h.hub.mu.Unlock()
		for _, s := range h.hub.sessions {
			if s.deviceID == -5 && s.streams > 0 {
				return false
			}
		}
		return true
	})
	if !h.hub.Available(h.entries["tv/2"].SourceID, 7) {
		t.Error("the source is not available to the box whose tuner it is")
	}
	if h.hub.Available(h.entries["tv/2"].SourceID, 8) {
		t.Error("the source is available to another box")
	}
	old := h.tunerOf("tv/1")
	next := h.playNext(u, h.link(7, "tv/2"))
	defer next.Body.Close()
	if h.tunerOf("tv/1") != nil || !old.ended || old.ctx.Err() == nil {
		t.Error("the old channel's tuner goes on")
	}
	if !strings.Contains(h.logs.String(), "replaced by a new stream from the same device") {
		t.Errorf("logs: %s", h.logs)
	}
	waitFor(t, "the old stream to close", func() bool { _, open := u.counts(); return open == 1 })
}

// A tuner that no screen plays from holds no connection: another device's
// stream ends it.
func TestSharedTunerIdle(t *testing.T) {
	h, u := sharedHarness(t, 1, 0)
	u.bursts <- tsBurst(1, 9)
	box := h.play(h.link(7, "tv/1"))
	box.Body.Close()
	h.waitIdle()
	if !h.hub.Available(h.entries["tv/2"].SourceID, 8) {
		t.Error("the source is not available while its tuner is idle")
	}
	other := h.playNext(u, h.link(8, "tv/2"))
	defer other.Body.Close()
	if h.tunerOf("tv/1") != nil {
		t.Error("the idle tuner goes on")
	}
	if !strings.Contains(h.logs.String(), "no screen plays it any more") {
		t.Errorf("logs: %s", h.logs)
	}
}

// The last screen to stop ends the tuner, its stream from the source and
// its buffer.
func TestSharedTunerEnds(t *testing.T) {
	h, u := sharedHarness(t, 2, 0)
	u.bursts <- tsBurst(1, 9)
	link := h.link(7, "tv/1")
	box := h.play(link)
	defer box.Body.Close()
	other := h.play(h.link(8, "tv/1"))
	tuner := h.tunerOf("tv/1")
	dir := tuner.shift.dir
	if _, err := os.Stat(dir); err != nil {
		t.Fatal(err)
	}
	other.Body.Close()
	if resp := get(t, link+"?stop=1"); resp.StatusCode != http.StatusNoContent {
		t.Errorf("stop: %s", resp.Status)
	}
	h.waitIdle()
	// The other box's screen goes once its grace passes.
	h.advance(reconnectGrace + time.Second)
	h.hub.mu.Lock()
	h.hub.sweep(h.hub.now())
	h.hub.mu.Unlock()
	if !tuner.ended || h.tunerOf("tv/1") != nil {
		t.Fatal("the tuner goes on")
	}
	h.hub.tuners.Wait()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the buffer is still there: %v", err)
	}
	waitFor(t, "the source's stream to close", func() bool { _, open := u.counts(); return open == 0 })
	if !strings.Contains(h.logs.String(), "a shared channel's stream ended") {
		t.Errorf("logs: %s", h.logs)
	}
}

// With the shared tuner off, each screen has a connection of its own.
func TestSharedTunerOff(t *testing.T) {
	h, u := sharedHarness(t, 1, 0)
	if err := h.hub.store.SetSharedTuner(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	u.bursts <- tsBurst(1, 9)
	box := h.play(h.link(7, "tv/1"))
	defer box.Body.Close()
	if resp := get(t, h.link(8, "tv/1")); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a second box: %s", resp.Status)
	}
	if h.hub.AvailableChannel(context.Background(), h.entries["tv/1"], 8) {
		t.Error("the channel is available to a second box")
	}
	if streams := h.hub.Streams(); len(streams) != 1 || streams[0].Shared {
		t.Errorf("streams %+v", streams)
	}
}

// A box whose link may go back does so in the shared tuner's buffer, which
// keeps the timeshift's minutes; a screen whose link may not plays from
// the end.
func TestSharedTunerTimeshift(t *testing.T) {
	h, u := sharedHarness(t, 1, 30)
	size := int64(len(tsBurst(0, 9)))
	u.bursts <- tsBurst(1, 9)
	box := get(t, h.shiftLink(7, "tv/1"))
	if box.StatusCode != http.StatusOK {
		t.Fatalf("box: %s", box.Status)
	}
	readPackets(t, box.Body, 2)
	tuner := h.tunerOf("tv/1")
	if tuner == nil || tuner.shift.keep != 30*time.Minute {
		t.Fatalf("tuner %+v", tuner)
	}
	for i := byte(2); i <= 3; i++ {
		h.advance(10 * time.Second)
		u.bursts <- tsBurst(i, 9)
		waitFor(t, "the stream to go on", func() bool { return tuner.shift.written() == int64(i)*size-tsPacket })
	}
	box.Body.Close()
	back := get(t, h.front.URL+h.hub.Link(8, h.entries["tv/1"])+"?behind=15")
	if p := readPackets(t, back.Body, 2); !isPAT(p) || p[tsPacket+5] != 3 {
		t.Errorf("a screen that may not go back: burst %d", p[tsPacket+5])
	}
	back.Body.Close()

	// The box goes back on its own link.
	boxLink := h.boxLinkOf(7)
	resp := get(t, boxLink+"?behind=15")
	if p := readPackets(t, resp.Body, 2); !isPAT(p) || p[tsPacket+5] != 1 {
		t.Errorf("the box going back: burst %d", p[tsPacket+5])
	}
	resp.Body.Close()
	streams := h.hub.Streams()
	timeshift := 0
	for _, s := range streams {
		if !s.Shared {
			t.Errorf("stream %+v", s)
		}
		if s.Timeshift {
			timeshift++
		}
	}
	if timeshift != 1 {
		t.Errorf("streams %+v", streams)
	}
	if requests, _ := u.counts(); requests != 1 {
		t.Errorf("%d requests to the source", requests)
	}
}

// playNext plays a link whose stream takes the place of one that the
// source plays now: once the source's stream before it has closed and the
// new one has opened, the source sends a burst, which only the new one can
// then take.
func (h *harness) playNext(u *burstUpstream, link string) *http.Response {
	h.t.Helper()
	before, _ := u.counts()
	got := make(chan *http.Response, 1)
	failed := make(chan error, 1)
	go func() {
		resp, err := http.Get(link)
		if err != nil {
			failed <- err
			return
		}
		got <- resp
	}()
	waitFor(h.t, "the source's stream to change", func() bool { requests, open := u.counts(); return requests == before+1 && open == 1 })
	u.bursts <- tsBurst(9, 9)
	select {
	case err := <-failed:
		h.t.Fatal(err)
	case resp := <-got:
		if resp.StatusCode != http.StatusOK {
			h.t.Fatalf("status %s: %s", resp.Status, body(h.t, resp))
		}
		return resp
	}
	return nil
}

// boxLinkOf returns the link of a device's screen on a shared tuner.
func (h *harness) boxLinkOf(device int64) string {
	h.t.Helper()
	h.hub.mu.Lock()
	defer h.hub.mu.Unlock()
	for _, s := range h.hub.sessions {
		if s.deviceID == device && s.tuner != nil {
			return h.front.URL + "/live/" + s.token + ".ts"
		}
	}
	h.t.Fatalf("no screen of device %d", device)
	return ""
}

// A channel whose address is HLS, a radio station, and a hub without a
// folder for buffers, keep connections of their own.
func TestSharedTunerSharable(t *testing.T) {
	h := newHarness(t, 1)
	if err := h.hub.UseTimeshift(filepath.Join(t.TempDir(), "timeshift")); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"live/1": true, "hls/master.m3u8": false} {
		if got := h.hub.sharable(h.entries[name]); got != want {
			t.Errorf("%s: sharable %v", name, got)
		}
	}
	radio := h.entries["live/1"]
	radio.Radio = true
	if h.hub.sharable(radio) {
		t.Error("a radio station is sharable")
	}
	h.hub.shiftDir = ""
	if h.hub.sharable(h.entries["live/1"]) {
		t.Error("sharable without a folder for buffers")
	}
}
