// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tsinfo/tstest"
)

// A parameter set that ffmpeg's H.264 encoder wrote for fictional 720p
// test pictures at 25 a second.
const sps720p = "6764001facd9405005bb011000000300100000030320f1831960"

// detailed is a source whose live streams are MPEG-TS with H.264 video and
// AAC audio, as a channel sends them, and whose /missing is not found.
func detailed(t *testing.T) *httptest.Server {
	t.Helper()
	sps, _ := hex.DecodeString(sps720p)
	m := tstest.New()
	for i := range 10 {
		picture := append(append([]byte{0, 0, 0, 1}, sps...), 0, 0, 0, 1, 0x65)
		m.Picture(int64(i)*tstest.Frame, append(picture, tstest.Content(i, "detailed")...))
	}
	stream := m.Bytes()
	mux := http.NewServeMux()
	mux.HandleFunc("/live/{n}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		rc := http.NewResponseController(w)
		for {
			if _, err := w.Write(stream); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(20 * time.Millisecond):
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// waitHealth waits until a channel's health meets ok.
func (h *harness) waitHealth(channelID int64, ok func(store.ChannelHealth) bool) store.ChannelHealth {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got, err := h.hub.store.HealthOf(context.Background(), channelID)
		if err != nil {
			h.t.Fatal(err)
		}
		if ok(got) {
			return got
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("channel %d's health: %+v", channelID, got)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A channel's stream notes that it played, and what it holds, as it passes;
// one that fails to start is noted; and the admin can stop a stream (D138).
func TestChannelHealthFromWhatHappens(t *testing.T) {
	src := detailed(t)
	h := newHarnessWith(t, 2, src.URL, []string{"/live/1", "/missing"})
	resp := h.play(h.link(7, "live/1"))
	defer resp.Body.Close()
	live := h.entries["live/1"].ChannelID
	got := h.waitHealth(live, func(c store.ChannelHealth) bool { return !c.PlayedAt.IsZero() && !c.SeenAt.IsZero() })
	if d := got.Details; d.Video != "H.264" || d.Width != 1280 || d.Height != 720 || d.FrameRate != 25 ||
		d.DynamicRange != "SDR" || len(d.Audio) != 1 || d.Audio[0] != "AAC" {
		t.Errorf("details %+v", d)
	}
	streams := h.hub.Streams()
	if len(streams) != 1 || streams[0].ID == "" || streams[0].Details != "720p H.264, AAC" {
		t.Fatalf("streams %+v", streams)
	}

	// Stopped from the admin, the stream ends, and the log says why.
	if !h.hub.StopStream(streams[0].ID, "stopped in the admin") {
		t.Fatal("not stopped")
	}
	if h.hub.StopStream(streams[0].ID, "again") || h.hub.StopStream("not a stream", "x") {
		t.Error("stopped twice, or a stream that is not")
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the stopped stream goes on")
	}
	if logs := h.logs.String(); !strings.Contains(logs, `reason="stopped in the admin"`) {
		t.Errorf("logs: %s", logs)
	}

	// A stream that fails to start.
	missing := get(t, h.link(7, "missing"))
	missing.Body.Close()
	if missing.StatusCode != http.StatusBadGateway {
		t.Errorf("missing: %s", missing.Status)
	}
	failed := h.waitHealth(h.entries["missing"].ChannelID, func(c store.ChannelHealth) bool { return c.Failing() })
	if failed.Failures != 1 || !failed.PlayedAt.IsZero() {
		t.Errorf("the failed channel %+v", failed)
	}
}
