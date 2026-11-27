// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/gpu"
	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// What is transcoded of films and series (D150).

// filmProfile adds the profile small: H.264 up to 576 lines, at a low
// quality, with AAC in stereo.
func filmProfile(t *testing.T, st *store.Store) {
	t.Helper()
	small, err := streamprofile.Check(streamprofile.Profile{Name: "small", MaxLines: 576, Quality: streamprofile.Low})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTranscodeProfile(context.Background(), small); err != nil {
		t.Fatal(err)
	}
}

// playable returns an item harness with a film that the MAG322 plays as
// it is: 4 seconds of H.264 at 1280×720 with AAC, and the profile small.
func playable(t *testing.T) *itemHarness {
	t.Helper()
	ffmpeg, ffprobe := realFFmpeg(t), realFFprobe(t)
	h := newItemHarness(t, 3)
	h.files.mu.Lock()
	h.files.content = makeMedia(t, ffmpeg, 4, "1280x720", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "50"}, "aac")
	h.files.settle = true
	h.files.mu.Unlock()
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseFFprobe(ffprobe)
	h.hub.UseEncoder(Encoder{H264: true, ToneMap: true})
	filmProfile(t, h.hub.store)
	return h
}

func TestFilmAlways(t *testing.T) {
	h := playable(t)
	ctx := context.Background()
	st := h.hub.store

	// As needed, the box plays it as it is.
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted {
		t.Fatalf("as needed: Prepare = %+v, %v", pb, err)
	}

	// Always, with small, while transcoding is off: as needed still.
	if _, err := st.SetTitlesTranscode(ctx, []int64{h.film.TitleID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if pb, err = h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil || pb.Converted {
		t.Fatalf("transcoding off: Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="a title is set to be transcoded always, but this server cannot transcode with its profile now; it is transcoded as needed" title="The Long Field" profile=small set_by=title needs=libx264`) {
		t.Errorf("logs: %s", logs)
	}

	// Once it may, ffmpeg transcodes it with the profile.
	if err := st.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if pb, err = h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil || !pb.Converted || !strings.HasSuffix(pb.Path, ".ts") {
		t.Fatalf("always: Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="a title plays through ffmpeg, which transcodes it as its setting says" title="The Long Field" source=main device=7 with=small set_by=title video="h264 1024x576" sound="aac 2ch to aac"`) {
		t.Errorf("logs: %s", logs)
	}
	resp := get(t, h.front.URL+pb.Path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := probeFile(t, data)
	if v := got.First(media.Video); v == nil || v.Codec != "h264" || v.Width != 1024 || v.Height != 576 {
		t.Errorf("made %s", got.Summary())
	}
	if a := got.First(media.Audio); a == nil || a.Codec != "aac" || a.Channels != 2 {
		t.Errorf("made %s", got.Summary())
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, `msg="transcoding a title with ffmpeg" title="The Long Field" source=main device=7 with=small from=0:00:00`) {
		t.Errorf("logs: %s", logs)
	}

	// Its shelf, or films and series as a whole, say the same where the
	// title says nothing.
	if _, err := st.SetTitlesTranscode(ctx, []int64{h.film.TitleID}, store.Transcode{}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetVODTranscode(ctx, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if pb, err = h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil || !pb.Converted {
		t.Fatalf("films and series always: Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `with=small set_by="films and series"`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestFilmNever(t *testing.T) {
	h := transcoding(t, 1)
	ctx := context.Background()
	if err := h.hub.store.SetVODTranscode(ctx, store.Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted || !strings.HasSuffix(pb.Path, ".mkv") {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="the box may not play a title's video, which is set never to be transcoded, so it plays as it is" title="The Long Field" source=main video="h264 320x180 10-bit HDR" profile="MAG322 (datasheet)" set_by="films and series"`) {
		t.Errorf("logs: %s", logs)
	}

	// A title's own as needed is as films and series say; its own always
	// wins over them.
	filmProfile(t, h.hub.store)
	if _, err := h.hub.store.SetTitlesTranscode(ctx, []int64{h.film.TitleID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if pb, err = h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil || !pb.Converted {
		t.Fatalf("its own always: Prepare = %+v, %v", pb, err)
	}
}

// filmGPU returns an item harness whose ffmpeg is the fake with graphics,
// in mode, with the film of transcoding, which the MAG322 cannot play,
// as found finds the graphics.
func filmGPU(t *testing.T, mode string, found gpu.Found) (*itemHarness, string) {
	t.Helper()
	h := transcoding(t, 1)
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path, notes := filepath.Join(dir, "ffmpeg"), filepath.Join(dir, "notes")
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' fake-gpu-ffmpeg '%s' %s \"$@\"\n", self, notes, mode)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	h.hub.UseFFmpeg(path, RepairSet)
	h.hub.UseGPU(found)
	return h, notes
}

// filmRuns returns how ffmpeg was asked to transcode, run by run, and the
// arguments of the last, and forgets them.
func filmRuns(t *testing.T, notes string) ([]string, string) {
	t.Helper()
	b, err := os.ReadFile(notes)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	os.Remove(notes)
	var out []string
	last := ""
	for line := range strings.Lines(string(b)) {
		way, args, _ := strings.Cut(strings.TrimSpace(line), " ")
		out, last = append(out, way), args
	}
	return out, last
}

// playFilm prepares and plays h's film, and returns what the box got.
func playFilm(t *testing.T, h *itemHarness) string {
	t.Helper()
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	resp := get(t, h.front.URL+pb.Path)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s", resp.Status)
	}
	defer h.waitIdle()
	return body(t, resp)
}

// mapsHDR is intel, with tone mapping.
var mapsHDR = func() gpu.Found {
	f := intel
	f.ToneMap = true
	return f
}()

func TestFilmOnGraphics(t *testing.T) {
	// A transcode that the box needs, on the graphics, which map its
	// HDR10; at 10 bits, its H.264 is decoded in software.
	h, notes := filmGPU(t, "", mapsHDR)
	if got := playFilm(t, h); !strings.HasPrefix(got, markUpload) {
		t.Errorf("sent %.20q", got)
	}
	runs, args := filmRuns(t, notes)
	if !slices.Equal(runs, []string{"upload"}) || !strings.Contains(args, "tonemap_vaapi=format=nv12:t=bt709:m=bt709:p=bt709") ||
		!strings.Contains(args, "-c:v h264_vaapi") || !strings.Contains(args, "-c:a:0 ac3") {
		t.Errorf("runs %v, args %s", runs, args)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="transcoding a title with ffmpeg" title="The Long Field" source=main device=7 on=graphics decode=software from=0:00:00`) {
		t.Errorf("logs: %s", logs)
	}

	// Graphics that cannot map HDR10 leave it to software, which can.
	h, notes = filmGPU(t, "", intel)
	if got := playFilm(t, h); !strings.HasPrefix(got, markSoftware) {
		t.Errorf("sent %.20q", got)
	}
	if runs, _ := filmRuns(t, notes); !slices.Equal(runs, []string{"software"}) {
		t.Errorf("runs %v", runs)
	}
	// With transcoding in software off, the graphics transcode it as it
	// is, rather than not at all.
	if err := h.hub.store.SetTranscodes(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if got := playFilm(t, h); !strings.HasPrefix(got, markUpload) {
		t.Errorf("software off: sent %.20q", got)
	}
	if runs, args := filmRuns(t, notes); !slices.Equal(runs, []string{"upload"}) || strings.Contains(args, "tonemap") {
		t.Errorf("software off: runs %v, args %s", runs, args)
	}
}

func TestFilmGraphicsFallBack(t *testing.T) {
	// A title set always with small, which the graphics decode: when they
	// fail before sending anything, they try again decoding in software,
	// then software takes over.
	h, notes := filmGPU(t, "decode=fail,upload=fail", intel)
	filmProfile(t, h.hub.store)
	h.files.mu.Lock()
	h.files.content = makeMedia(t, realFFmpeg(t), 4, "1280x720", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "50"}, "aac")
	h.files.mu.Unlock()
	if _, err := h.hub.store.SetTitlesTranscode(context.Background(), []int64{h.film.TitleID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if got := playFilm(t, h); !strings.HasPrefix(got, markSoftware) {
		t.Errorf("sent %.20q", got)
	}
	runs, args := filmRuns(t, notes)
	if !slices.Equal(runs, []string{"decode", "upload", "software"}) || !strings.Contains(args, "-c:v libx264") || !strings.Contains(args, "-c:a:0 aac") {
		t.Errorf("runs %v, args %s", runs, args)
	}
	logs := h.logs.String()
	for _, want := range []string{
		`msg="ffmpeg could not transcode a title on the graphics; it is transcoded on the graphics, decoded in software" title="The Long Field"`,
		`msg="ffmpeg could not transcode a title on the graphics; it is transcoded in software" title="The Long Field"`,
		`msg="transcoding a title with ffmpeg" title="The Long Field" source=main device=7 with=small from=0:00:00`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s: %s", want, logs)
		}
	}
	h.hub.encodeMu.Lock()
	gpuLeft, softLeft := len(h.hub.gpuEncodes), len(h.hub.encodes)
	h.hub.encodeMu.Unlock()
	if gpuLeft != 0 || softLeft != 0 {
		t.Errorf("still counted: %d on the graphics, %d in software", gpuLeft, softLeft)
	}

	// The graphics' own number at a time: none, and it goes to software.
	if err := h.hub.store.SetGPUTranscodes(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	if got := playFilm(t, h); !strings.HasPrefix(got, markSoftware) {
		t.Errorf("no graphics: sent %.20q", got)
	}
	if runs, _ := filmRuns(t, notes); !slices.Equal(runs, []string{"software"}) {
		t.Errorf("no graphics: runs %v", runs)
	}
}
