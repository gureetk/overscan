// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
)

// transcoder stands in for a Jellyfin server's transcodes: it keeps what
// each request asked for, and sends a stream until the request ends.
type transcoder struct {
	mu      sync.Mutex
	asked   []url.Values
	stopped []url.Values
	refuse  int // answer this status instead, if set
}

func newTranscoder(t *testing.T) (*transcoder, *httptest.Server) {
	t.Helper()
	tr := &transcoder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tr.mu.Lock()
		refuse := tr.refuse
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/jf/Videos/ActiveEncodings":
			tr.stopped = append(tr.stopped, r.URL.Query())
			tr.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		case r.Method != http.MethodGet || r.URL.Path != "/jf/Videos/abc/stream.ts" || r.URL.Query().Get("ApiKey") != "k3y":
			tr.mu.Unlock()
			http.NotFound(w, r)
			return
		}
		tr.asked = append(tr.asked, r.URL.Query())
		tr.mu.Unlock()
		if refuse != 0 {
			http.Error(w, "no", refuse)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		rc := http.NewResponseController(w)
		packet := append([]byte{0x47}, make([]byte, 187)...)
		for {
			if _, err := w.Write(packet); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return tr, srv
}

func (tr *transcoder) requests() (asked, stopped []url.Values) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return append([]url.Values(nil), tr.asked...), append([]url.Values(nil), tr.stopped...)
}

// noH264 is a box that does not play H.264, which the test films have.
var noH264 = media.MAG322.With(map[string]bool{"video:h264": false}, "MAG322 without H.264")

func TestTranscodeThroughTheSource(t *testing.T) {
	h := converting(t, 1, "dca", "aac")
	tr, srv := newTranscoder(t)
	h.film.TranscodeURL = srv.URL + "/jf/Videos/abc/stream.ts?ApiKey=k3y"
	ctx := context.Background()
	// The source's server transcodes, even where Overscan's own ffmpeg may
	// (D73).
	h.hub.UseEncoder(Encoder{H264: true, ToneMap: true})
	if err := h.hub.store.SetTranscodes(ctx, 3); err != nil {
		t.Fatal(err)
	}
	pb, err := h.hub.Prepare(ctx, 7, h.film, noH264, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !pb.Converted || !pb.Transcoded || !strings.HasSuffix(pb.Path, ".ts") || pb.StartPath == "" ||
		len(pb.Tracks) != 2 || pb.Tracks[0].Codec != "dts" || !pb.Tracks[0].Convert || pb.Tracks[1].Convert {
		t.Fatalf("Prepare = %+v", pb)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="a title plays through its source's server, which transcodes what the box cannot play" title="The Long Field" source=main device=7 why="its video (h264 320x180 8-bit)" sound="dts 6ch to ac3, aac 2ch" profile="MAG322 without H.264"`) {
		t.Errorf("logs: %s", logs)
	}

	// The server starts from the very time asked for.
	if at := h.startAt(pb, "5.25"); at != 5.25 {
		t.Errorf("start from 5.25 s at %v", at)
	}
	resp := get(t, h.front.URL+pb.Path+"?t=5.25&a=1")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" || resp.Header.Get("Accept-Ranges") != "none" {
		t.Fatalf("status %s, header %v", resp.Status, resp.Header)
	}
	first := make([]byte, 188)
	if _, err := io.ReadFull(resp.Body, first); err != nil || first[0] != 0x47 {
		t.Fatalf("read %x, %v", first[:1], err)
	}
	if s := h.hub.Streams(); len(s) != 1 || !s[0].Transcoded || s[0].TranscodedBy != TranscodedBySource || s[0].Converted || !s[0].Playing {
		t.Errorf("streams %+v", s)
	}
	asked, _ := tr.requests()
	if len(asked) != 1 {
		t.Fatalf("asked %v", asked)
	}
	q := asked[0]
	for k, v := range map[string]string{"StartTimeTicks": "52500000", "AudioStreamIndex": "2", "AudioCodec": "ac3,aac,mp3,mp2",
		"VideoCodec": "h264", "AllowVideoStreamCopy": "false", "DeviceId": "overscan-7"} {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	firstSession := q.Get("PlaySessionId")
	if len(firstSession) != 16 {
		t.Errorf("play session %q", firstSession)
	}

	// A seek ends the stream before, and the hub asks the server to stop
	// it.
	second := get(t, h.front.URL+pb.Path+"?t=8")
	if _, err := io.ReadFull(second.Body, first); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(resp.Body); err == nil {
		resp.Body.Close()
	}
	var stopped []url.Values
	for range 200 {
		if asked, stopped = tr.requests(); len(stopped) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(asked) != 2 || len(stopped) != 1 || stopped[0].Get("playSessionId") != firstSession ||
		stopped[0].Get("deviceId") != "overscan-7" || stopped[0].Get("ApiKey") != "k3y" {
		t.Errorf("asked %v, stopped %v", asked, stopped)
	}
	if q := asked[1]; q.Get("StartTimeTicks") != "80000000" || q.Get("AudioStreamIndex") != "1" || q.Get("PlaySessionId") == firstSession {
		t.Errorf("after the seek %v", q)
	}
	second.Body.Close()

	for _, bad := range []string{"?a=2", "?a=x", "?t=-1"} {
		if resp := get(t, h.front.URL+pb.Path+bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s", bad, resp.Status)
		}
	}
	if logs := h.logs.String(); strings.Contains(logs, "k3y") || !strings.Contains(logs, `msg="a title's source transcodes it" title="The Long Field" source=main device=7 from=0:00:05 track=1`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestTranscodeRefused(t *testing.T) {
	h := converting(t, 1, "aac")
	tr, srv := newTranscoder(t)
	tr.refuse = http.StatusInternalServerError
	h.film.TranscodeURL = srv.URL + "/jf/Videos/abc/stream.ts?ApiKey=k3y"
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, noH264, nil)
	if err != nil || !pb.Transcoded {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if resp := get(t, h.front.URL+pb.Path); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %s", resp.Status)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="a title's source would not transcode it"`) || !strings.Contains(logs, `status="500 Internal Server Error"`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestVideoWithoutATranscoder(t *testing.T) {
	// A source without a server to transcode plays it as it is.
	h := converting(t, 1, "aac")
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, noH264, nil)
	if err != nil || pb.Converted || pb.Transcoded || !strings.HasSuffix(pb.Path, ".mkv") {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="the box may not play a title's video, which Overscan does not convert, so it plays as it is"`) {
		t.Errorf("logs: %s", logs)
	}
}

// TestSwitchedOff plays as they are the films that the administrator has
// set Overscan not to convert, and not to have their source's server
// transcode (D78).
func TestSwitchedOff(t *testing.T) {
	ctx := context.Background()

	// Sound that the box cannot play, not converted.
	h := converting(t, 1, "dca", "aac")
	if err := h.hub.store.SetConvertsSound(ctx, false); err != nil {
		t.Fatal(err)
	}
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted || pb.Transcoded || !strings.HasSuffix(pb.Path, ".mkv") {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="the box may not play a title, which Overscan is set not to convert, so it plays as it is" title="The Long Field" source=main why="its sound (dts)"`) {
		t.Errorf("logs: %s", logs)
	}

	// Video that the box cannot play, not transcoded by the source's
	// server, nor by Overscan, which leaves the source's films to it.
	h = converting(t, 1, "aac")
	_, srv := newTranscoder(t)
	h.film.TranscodeURL = srv.URL + "/jf/Videos/abc/stream.ts?ApiKey=k3y"
	h.hub.UseEncoder(Encoder{H264: true, ToneMap: true})
	if err := h.hub.store.SetTranscodes(ctx, 3); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.store.SetServerTranscodes(ctx, false); err != nil {
		t.Fatal(err)
	}
	pb, err = h.hub.Prepare(ctx, 7, h.film, noH264, nil)
	if err != nil || pb.Converted || pb.Transcoded || !strings.HasSuffix(pb.Path, ".mkv") {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="the box may not play a title's video, which its source's server is set not to transcode, so it plays as it is" title="The Long Field" source=main video="h264 320x180 8-bit"`) {
		t.Errorf("logs: %s", logs)
	}

	// Switched on again, the source's server transcodes it.
	if err := h.hub.store.SetServerTranscodes(ctx, true); err != nil {
		t.Fatal(err)
	}
	if pb, err = h.hub.Prepare(ctx, 7, h.film, noH264, nil); err != nil || !pb.Transcoded {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
}
