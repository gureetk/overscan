// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
)

// transcoding returns an item harness whose hub may transcode limit titles
// at once, with a film on its source whose video the MAG322 cannot play:
// 12 seconds of H.264 at 10 bits, tagged as high dynamic range, with a
// picture to start from every 2 seconds, and an AAC tone.
func transcoding(t *testing.T, limit int) *itemHarness {
	t.Helper()
	ffmpeg, ffprobe := realFFmpeg(t), realFFprobe(t)
	h := newItemHarness(t, 3)
	h.files.mu.Lock()
	// The pictures carry the colour tags, which newer ffmpeg gives the
	// encoder in place of its own options, and x264 writes them into the
	// video too.
	h.files.content = makeMedia(t, ffmpeg, 12, "320x180", []string{
		"-vf", "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-pix_fmt", "yuv420p10le",
		"-x264-params", "colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc"}, "aac")
	h.files.settle = true
	h.files.mu.Unlock()
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseFFprobe(ffprobe)
	h.hub.UseEncoder(Encoder{H264: true, ToneMap: true})
	if err := h.hub.store.SetTranscodes(context.Background(), limit); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestTranscodeFilm(t *testing.T) {
	h := transcoding(t, 1)
	ctx := context.Background()
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil {
		t.Fatal(err)
	}
	// It goes as a stream through ffmpeg with every sound track, as one
	// that ffmpeg converts does, not one sound track at a time.
	if !pb.Converted || pb.Transcoded || !strings.HasSuffix(pb.Path, ".ts") || pb.Duration < 11.9 ||
		len(pb.Tracks) != 1 || !pb.Tracks[0].Convert {
		t.Fatalf("Prepare = %+v", pb)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `msg="a title plays through ffmpeg, which transcodes its video for the box" title="The Long Field" source=main device=7 why="its video (h264 320x180 10-bit HDR)" video="h264 320x180, mapped to the standard dynamic range" sound="aac 2ch to ac3"`) ||
		strings.Contains(logs, secret) {
		t.Errorf("logs: %s", logs)
	}

	// ffmpeg transcodes from the very time asked.
	if at := h.startAt(pb, "5"); at != 5 {
		t.Errorf("start from 5 s at %v, want 5", at)
	}
	resp := get(t, h.front.URL+pb.Path+"?t=5")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("status %s, header %v", resp.Status, resp.Header)
	}
	first := make([]byte, 188)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	if s := h.hub.Streams(); len(s) != 1 || !s[0].Transcoded || s[0].TranscodedBy != TranscodedByOverscan || !s[0].Converted {
		t.Errorf("streams %+v", s)
	}
	rest, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := probeFile(t, append(first, rest...))
	video := got.First(media.Video)
	if !strings.HasPrefix(got.Summary(), "mpegts: h264 320x180 8-bit; sound ac3 2ch") || video == nil || video.HDR != "" ||
		got.Duration < 6.9 || got.Duration > 7.1 {
		t.Errorf("made %s, %v s", got.Summary(), got.Duration)
	}
	h.waitIdle()
	if s := h.hub.Streams(); len(s) != 1 || s[0].Transcoded || s[0].TranscodedBy != "" {
		t.Errorf("streams after %+v", s)
	}
	logs = h.logs.String()
	if !strings.Contains(logs, `msg="transcoding a title with ffmpeg" title="The Long Field" source=main device=7 from=0:00:05`) ||
		!strings.Contains(logs, `msg="ffmpeg's transcode of a title ended" title="The Long Field" source=main device=7 made=0:00:0`) ||
		strings.Contains(logs, "ffmpeg stopped") || strings.Contains(logs, "more slowly") {
		t.Errorf("logs: %s", logs)
	}
	h.hub.encodeMu.Lock()
	n := len(h.hub.encodes)
	h.hub.encodeMu.Unlock()
	if n != 0 {
		t.Errorf("%d devices still counted as transcoding", n)
	}
}

func TestTranscodeNotSet(t *testing.T) {
	h := transcoding(t, 0)
	ctx := context.Background()
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted || !strings.HasSuffix(pb.Path, ".mkv") {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="the box may not play a title's video, which Overscan is not set to transcode, so it plays as it is" title="The Long Field" source=main video="h264 320x180 10-bit HDR"`) {
		t.Errorf("logs: %s", logs)
	}

	// An ffmpeg without libx264 cannot, whatever the setting.
	if err := h.hub.store.SetTranscodes(ctx, 2); err != nil {
		t.Fatal(err)
	}
	h.hub.UseEncoder(Encoder{})
	if h.hub.CanTranscode() {
		t.Error("transcodes without libx264")
	}
	if pb, err = h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil || pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `which Overscan does not convert, so it plays as it is`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestTranscodeWithoutToneMapping(t *testing.T) {
	h := transcoding(t, 1)
	h.hub.UseEncoder(Encoder{H264: true})
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `msg="this server's ffmpeg cannot map a title's high dynamic range to the standard one, so its colours may look washed out" title="The Long Field"`) ||
		!strings.Contains(logs, `video="h264 320x180" sound=`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestTranscodesAtOnce(t *testing.T) {
	h := transcoding(t, 1)
	ctx := context.Background()
	// Another box transcodes now.
	if !h.hub.takeEncode(8, 1) {
		t.Fatal("no room for the first transcode")
	}
	if _, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("Prepare: %v, want ErrBusy", err)
	}
	if s := h.hub.Streams(); len(s) != 0 {
		t.Errorf("streams %+v after a busy answer", s)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="a title's video must be transcoded, and Overscan transcodes as many titles as it may at once, so it cannot start" title="The Long Field" source=main device=7 most=1`) {
		t.Errorf("logs: %s", logs)
	}
	// The box's own transcode does not count: its new stream ends it.
	if pb, err := h.hub.Prepare(ctx, 8, h.film, media.MAG322, nil); err != nil || !pb.Converted {
		t.Fatalf("Prepare for the box that transcodes = %+v, %v", pb, err)
	}
	// With room for two, the other box's starts too.
	if err := h.hub.store.SetTranscodes(ctx, 2); err != nil {
		t.Fatal(err)
	}
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare with room for two = %+v, %v", pb, err)
	}

	// A link that came while there was room cannot start once there is
	// none.
	if err := h.hub.store.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	resp := get(t, h.front.URL+pb.Path)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status %s with no room", resp.Status)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="Overscan transcodes as many titles as it may at once; this one cannot start" title="The Long Field" source=main device=7 most=1`) {
		t.Errorf("logs: %s", logs)
	}
	// Turned off, a link already made still plays, as the one transcode.
	if err := h.hub.store.SetTranscodes(ctx, 0); err != nil {
		t.Fatal(err)
	}
	h.hub.endEncode(8)
	resp = get(t, h.front.URL+pb.Path+"?t=10")
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s, %v", resp.Status, err)
	}
	if got := probeFile(t, data); got.Duration < 1.9 || got.Duration > 2.1 {
		t.Errorf("made %v s from 10 s, want 2", got.Duration)
	}
	h.waitIdle()
}

func TestTranscodeReadsAgain(t *testing.T) {
	h := transcoding(t, 1)
	ctx := context.Background()
	if _, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	// What a server before D73 kept of the file: without its dynamic range
	// or its frame rate.
	titleID, episodeID := h.film.MediaKey()
	info, ok, err := h.hub.store.Media(ctx, titleID, episodeID)
	if err != nil || !ok || info.Probed != media.ProbeVersion {
		t.Fatalf("kept %+v, %v, %v", info, ok, err)
	}
	old := info
	old.Probed = 0
	old.Streams = append([]media.Stream(nil), info.Streams...)
	for i := range old.Streams {
		old.Streams[i].HDR, old.Streams[i].FrameRate = "", 0
	}
	if err := h.hub.store.SetMedia(ctx, titleID, episodeID, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	// With transcoding off, what was kept does for the file as it is.
	if err := h.hub.store.SetTranscodes(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(h.logs.String(), "read what a title's file holds"); n != 1 {
		t.Errorf("read the file %d times with transcoding off", n)
	}
	// On, the file is read again for its transcode.
	if err := h.hub.store.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	logs := h.logs.String()
	if n := strings.Count(logs, `msg="read what a title's file holds"`); n != 2 {
		t.Errorf("read the file %d times", n)
	}
	if n := strings.Count(logs, `video="h264 320x180, mapped to the standard dynamic range"`); n != 2 {
		t.Errorf("logs: %s", logs)
	}
	if info, _, _ := h.hub.store.Media(ctx, titleID, episodeID); info.Probed != media.ProbeVersion {
		t.Errorf("kept %+v", info)
	}

	// A file that cannot be read again plays as it was read before.
	if err := h.hub.store.SetMedia(ctx, titleID, episodeID, old, time.Now()); err != nil {
		t.Fatal(err)
	}
	h.files.mu.Lock()
	h.files.refuse = http.StatusInternalServerError
	h.files.mu.Unlock()
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	logs = h.logs.String()
	if !strings.Contains(logs, `msg="cannot read again what a title's file holds; it plays as Overscan read it before" title="The Long Field" source=main`) ||
		!strings.Contains(logs, `video="h264 320x180" sound=`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestFollowProgress(t *testing.T) {
	r, w := io.Pipe()
	p := followProgress(r)
	go func() {
		_, _ = io.WriteString(w, "frame=0\nout_time_us=N/A\nprogress=continue\n")
		_, _ = io.WriteString(w, "frame=75\nout_time_us=2880000\nout_time_ms=2880000\nprogress=end\n")
		// ffmpeg never writes a line this long, but if it did, it would
		// still not wait for it to be read.
		_, _ = io.WriteString(w, strings.Repeat("x", 100<<10)+"\nout_time_us=9\n")
		w.Close()
	}()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the progress was not read to its end")
	}
	if got := p.made.Load(); got != 2880000 {
		t.Errorf("made %d µs", got)
	}

	// The wait before ffmpeg's first output, and for the player, do not
	// count.
	p.read(0, 3*time.Second)
	p.read(188, time.Second)
	if _, ok := p.speed(time.Second); ok {
		t.Error("judged before ffmpeg worked")
	}
	p.read(188, 2*time.Second)
	if speed, ok := p.speed(time.Second); !ok || speed < 1.43 || speed > 1.45 {
		t.Errorf("speed %v, %v; want 1.44", speed, ok)
	}
	if got := formatSpeed(1.44); got != "1.44x" {
		t.Errorf("formatSpeed = %q", got)
	}
}

func TestEncodeCount(t *testing.T) {
	h := newItemHarness(t, 1)
	if !h.hub.takeEncode(7, 1) || !h.hub.takeEncode(7, 1) {
		t.Fatal("a box's own transcodes count against it")
	}
	if h.hub.takeEncode(8, 2) {
		t.Error("room for a third transcode with room for two")
	}
	if n := h.hub.encodesElsewhere(8); n != 2 {
		t.Errorf("%d transcodes elsewhere", n)
	}
	h.hub.endEncode(7)
	if !h.hub.takeEncode(8, 2) {
		t.Error("no room once one ended")
	}
	h.hub.endEncode(7)
	h.hub.endEncode(8)
	if len(h.hub.encodes) != 0 {
		t.Errorf("counts left: %v", h.hub.encodes)
	}
}
