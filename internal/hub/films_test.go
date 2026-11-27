// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

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

// A film set always reaches a player transcoded at a constant rate, which
// it seeks in by bytes (D151); one set as needed reaches it as it is.
func TestFilmForPlayers(t *testing.T) {
	h := playable(t)
	h.files.mu.Lock()
	h.files.content = makeMedia(t, realFFmpeg(t), 12, "1280x720", []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "25"}, "aac")
	h.files.mu.Unlock()
	ctx := context.Background()
	st := h.hub.store
	if err := st.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}

	// As needed: the file as it is, without reading it first.
	link, err := h.hub.PreparePlayer(ctx, 7, h.film)
	if err != nil || !strings.HasSuffix(link, ".mkv") {
		t.Fatalf("as needed: PreparePlayer = %q, %v", link, err)
	}
	if strings.Contains(h.logs.String(), "read what a title's file holds") {
		t.Error("the title was read for a player that gets it as it is")
	}

	if _, err := st.SetTitlesTranscode(ctx, []int64{h.film.TitleID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if link, err = h.hub.PreparePlayer(ctx, 7, h.film); err != nil || !strings.HasSuffix(link, ".ts") {
		t.Fatalf("always: PreparePlayer = %q, %v", link, err)
	}
	if logs := h.logs.String(); !strings.Contains(logs,
		`msg="a title plays through ffmpeg, which transcodes it as its setting says, at a constant rate for a player to seek in" title="The Long Field" source=main player=7 with=small set_by=title video="h264 1024x576" sound="aac 2ch to aac" rate=2511k`) {
		t.Errorf("logs: %s", logs)
	}
	// 2000 kbit/s for 576 lines and 128 for AAC, with room, a second.
	const rate = 2511000.0
	request := func(method, rng string) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, h.front.URL+link, nil)
		if err != nil {
			t.Fatal(err)
		}
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	resp := request("HEAD", "")
	resp.Body.Close()
	total, _ := strconv.ParseInt(resp.Header.Get("Content-Length"), 10, 64)
	// The 12 s of the film, in whole packets.
	if seconds := float64(total) * 8 / rate; resp.StatusCode != http.StatusOK || total%188 != 0 || seconds < 12 || seconds > 12.1 ||
		resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("HEAD: %s, %v", resp.Status, resp.Header)
	}

	// From the start: the whole length.
	resp = request("GET", "")
	whole, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || int64(len(whole)) != total {
		t.Fatalf("GET: %s, %d bytes, %v", resp.Status, len(whole), err)
	}
	got := probeFile(t, whole)
	if v := got.First(media.Video); v == nil || v.Codec != "h264" || v.Width != 1024 || v.Height != 576 {
		t.Errorf("made %s", got.Summary())
	}
	if a := got.First(media.Audio); a == nil || a.Codec != "aac" || a.Channels != 2 {
		t.Errorf("made %s", got.Summary())
	}
	h.waitIdle()

	// The player's next request goes on with the same session.
	if again, err := h.hub.PreparePlayer(ctx, 7, h.film); err != nil || again != link {
		t.Errorf("again: PreparePlayer = %q, %v; want %q", again, err, link)
	}

	// From a byte: the rest, starting at that byte's time.
	offset := int64(math.Ceil(6*rate/8/188))*188 + 100
	resp = request("GET", fmt.Sprintf("bytes=%d-", offset))
	rest, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusPartialContent || int64(len(rest)) != total-offset ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, total-1, total) {
		t.Fatalf("from %d: %s, %v, %d bytes, %v", offset, resp.Status, resp.Header, len(rest), err)
	}
	aligned := rest[188-offset%188:]
	if aligned[0] != 0x47 {
		t.Errorf("the packets are not in step with the stream's: %x", aligned[:4])
	}
	if pts := firstVideoPTS(t, aligned[:256<<10]); math.Abs(pts-firstVideoPTS(t, whole[:256<<10])-6) > 0.5 {
		t.Errorf("from 6 s: the first picture at %v s", pts)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="transcoding a title with ffmpeg" title="The Long Field" source=main device=7 with=small from=0:00:06`) {
		t.Errorf("logs: %s", logs)
	}
	h.waitIdle()

	// A range that ends, and one past the end.
	resp = request("GET", "bytes=1000-1999")
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(part) != 1000 || part[188-1000%188] != 0x47 ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 1000-1999/%d", total) {
		t.Errorf("bytes 1000 to 1999: %s, %v, %d bytes", resp.Status, resp.Header, len(part))
	}
	h.waitIdle()
	resp = request("GET", fmt.Sprintf("bytes=%d-", total))
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Range") != fmt.Sprintf("bytes */%d", total) {
		t.Errorf("past the end: %s, %v", resp.Status, resp.Header)
	}

	// A player that seeks by bytes, here ffprobe, which reads the
	// stream's start and end for its length, then seeks to 8 s, finds the
	// picture of that time. It reads the end in many small steps, which
	// come from the end made once.
	began := time.Now()
	cmd := exec.Command(realFFprobe(t), "-v", "error", "-select_streams", "v:0", "-read_intervals", "8%+0.5",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", h.front.URL+link)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe: %v: %s\n%s", err, stderr.String(), h.logs.String())
	}
	pts, _ := strconv.ParseFloat(strings.Trim(strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), ","), 64)
	if pts < 7.5 || pts > 8.6 {
		t.Errorf("ffprobe seeking to 8 s found a picture at %v s", pts)
	}
	if took := time.Since(began); took > 20*time.Second {
		t.Errorf("ffprobe took %v to seek", took)
	}
	h.waitIdle()
	resp = request("GET", fmt.Sprintf("bytes=%d-", total-188))
	last, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(last) != 188 || last[0] != 0x47 {
		t.Errorf("the last packet: %s, %d bytes", resp.Status, len(last))
	}
	if n := strings.Count(h.logs.String(), "part=end with=small from="); n != 1 {
		t.Errorf("the stream's end was made %d times: %s", n, h.logs.String())
	}
	h.waitIdle()

	// After a long pause, the session has ended: the same link is
	// prepared again.
	h.advance(reconnectGrace + time.Second)
	h.hub.sweep(h.hub.now())
	resp = request("GET", "bytes=0-187")
	again, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || len(again) != 188 || again[0] != 0x47 {
		t.Errorf("after a pause: %s, %d bytes", resp.Status, len(again))
	}
	h.waitIdle()

	// Never: the file as it is again.
	if _, err := st.SetTitlesTranscode(ctx, []int64{h.film.TitleID}, store.Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	if link, err := h.hub.PreparePlayer(ctx, 7, h.film); err != nil || !strings.HasSuffix(link, ".mkv") {
		t.Errorf("never: PreparePlayer = %q, %v", link, err)
	}
}

// firstVideoPTS returns the time of the first picture in a piece of an
// MPEG-TS stream.
func firstVideoPTS(t *testing.T, b []byte) float64 {
	t.Helper()
	path := filepath.Join(t.TempDir(), "piece.ts")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(realFFprobe(t), "-v", "quiet", "-select_streams", "v:0", "-show_entries", "packet=pts_time",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(out)) {
		if v, err := strconv.ParseFloat(strings.Trim(strings.TrimSpace(line), ","), 64); err == nil {
			return v
		}
	}
	t.Fatalf("no picture in %d bytes", len(b))
	return 0
}

// Never is of Overscan's own transcodes: a source's server still
// transcodes what the box cannot play (D71, D151).
func TestFilmNeverLetsTheSourceTranscode(t *testing.T) {
	h := converting(t, 1, "dca", "aac")
	_, srv := newTranscoder(t)
	h.film.TranscodeURL = srv.URL + "/jf/Videos/abc/stream.ts?ApiKey=k3y"
	ctx := context.Background()
	if _, err := h.hub.store.SetTitlesTranscode(ctx, []int64{h.film.TitleID}, store.Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	pb, err := h.hub.Prepare(ctx, 7, h.film, noH264, nil)
	if err != nil || !pb.Converted || !pb.Transcoded {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if logs := h.logs.String(); strings.Contains(logs, "set never to be transcoded") {
		t.Errorf("logs: %s", logs)
	}
}
