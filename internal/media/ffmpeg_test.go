// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"bytes"
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tools returns the paths of ffmpeg and ffprobe, skipping the test without
// them.
func tools(t *testing.T) (ffmpeg, ffprobe string) {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg:", err)
	}
	ffprobe, err = exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe:", err)
	}
	return ffmpeg, ffprobe
}

// run runs a program with a time limit and returns what it wrote.
func run(t *testing.T, program string, args ...string) []byte {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%s %s: %v: %s", filepath.Base(program), strings.Join(args, " "), err, stderr.String())
	}
	return out
}

// TestConvertWithFFmpeg makes a file with a DTS track and a stereo AAC one,
// serves it over HTTP with ranges, as the hub does, and converts it from a
// time with what Decide and Args say.
func TestConvertWithFFmpeg(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "film.mkv")
	run(t, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=12",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=12",
		"-map", "0:v", "-map", "1:a", "-map", "1:a",
		"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-bf", "2",
		"-c:a:0", "dca", "-strict", "-2", "-ac:a:0", "6", "-c:a:1", "aac", "-ac:a:1", "2",
		"-metadata:s:a:0", "language=eng", "-metadata:s:a:1", "language=fre", src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, src)
	}))
	defer srv.Close()
	input := srv.URL + "/in/film"

	info, err := ParseProbe(run(t, ffprobe, ProbeArgs(input)...))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.Summary(), "matroska: h264 320x180 8-bit; sound dts 6ch eng, aac 2ch fre") || info.Duration < 11 {
		t.Fatalf("probed %s, %v s", info.Summary(), info.Duration)
	}
	plan := Decide(info, MAG322)
	if !plan.Convert || plan.Why != "its sound (dts)" || len(plan.Tracks) != 2 || !plan.Tracks[0].Convert || plan.Tracks[1].Convert {
		t.Fatalf("Decide = %+v", plan)
	}

	// Asked for 5 s in, the stream starts at the picture before: the test
	// pattern has one every 2 s. The file's clocks start 21 ms before its
	// start, for the AAC track's priming. Its video has B-frames, which
	// make ffmpeg seek a little earlier than asked.
	if info.Start != -0.021 {
		t.Fatalf("file starts at %v on its clocks", info.Start)
	}
	at, ok := info.ParseKeyframe(run(t, ffprobe, info.KeyframeArgs(input, plan.Video, 5)...))
	if !ok || math.Abs(at-4.021) > 1e-9 {
		t.Fatalf("keyframe at %v, %v; want 4.021", at, ok)
	}
	out := filepath.Join(dir, "out.ts")
	stream := run(t, ffmpeg, plan.Args(input, at)...)
	if err := os.WriteFile(out, stream, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ParseProbe(run(t, ffprobe, "-hide_banner", "-loglevel", "error", "-show_format", "-show_streams", "-print_format", "json", out))
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "mpegts" || !strings.HasPrefix(got.Summary(), "mpegts: h264 320x180 8-bit; sound ac3 6ch eng, aac 2ch fre") {
		t.Errorf("made %s", got.Summary())
	}
	if got.Duration < 7.9 || got.Duration > 8.1 {
		t.Errorf("made %v s, want the 8 s from the picture at 4.021 s", got.Duration)
	}
}

// TestWhitelist checks that ffprobe and ffmpeg refuse a "file" that is a
// playlist, which would have them fetch other addresses.
func TestWhitelist(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	fetched := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/in/film" {
			fetched <- r.URL.Path
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:10\n#EXTINF:10,\n/elsewhere.ts\n#EXT-X-ENDLIST\n"))
	}))
	defer srv.Close()
	input := srv.URL + "/in/film"
	for _, c := range []struct {
		program string
		args    []string
	}{
		{ffprobe, ProbeArgs(input)},
		{ffmpeg, Plan{Video: 0}.Args(input, 0)},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		out, err := exec.CommandContext(ctx, c.program, c.args...).CombinedOutput()
		cancel()
		if err == nil {
			t.Errorf("%s took a playlist: %s", filepath.Base(c.program), out)
		}
	}
	select {
	case p := <-fetched:
		t.Errorf("a playlist made them fetch %s", p)
	default:
	}
}

// hdrTags is the filter that tags a test picture as high dynamic range:
// BT.2020 with PQ.
const hdrTags = "setparams=color_primaries=bt2020:color_trc=smpte2084:colorspace=bt2020nc"

// TestEncodeWithFFmpeg makes a file of HDR video at 10 bits, which a box
// that plays no H.265 cannot play, and transcodes it from a time with what
// EncodePlan and Args say: to H.264 at 8 bits, mapped to the standard
// dynamic range where ffmpeg has the filters to, starting at the very time
// asked.
func TestEncodeWithFFmpeg(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	if !strings.Contains(string(run(t, ffmpeg, "-hide_banner", "-encoders")), " libx265 ") {
		t.Skip("ffmpeg has no libx265 to make the test file with")
	}
	filters := string(run(t, ffmpeg, "-hide_banner", "-filters"))
	toneMap := strings.Contains(filters, " zscale ") && strings.Contains(filters, " tonemap ")
	dir := t.TempDir()
	src := filepath.Join(dir, "film.mkv")
	// The pictures carry the colour tags, which newer ffmpeg gives the
	// encoder in place of its own options, and x265 writes them into the
	// video too.
	run(t, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=size=640x360:rate=25:duration=4,"+hdrTags,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=4",
		"-c:v", "libx265", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le",
		"-x265-params", "log-level=error:colorprim=bt2020:transfer=smpte2084:colormatrix=bt2020nc",
		"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc",
		"-c:a", "aac", src)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, src)
	}))
	defer srv.Close()
	input := srv.URL + "/in/film"

	info, err := ParseProbe(run(t, ffprobe, ProbeArgs(input)...))
	if err != nil {
		t.Fatal(err)
	}
	if v := info.First(Video); v == nil || v.Describe() != "hevc 640x360 10-bit HDR" || v.FrameRate != 25 {
		t.Fatalf("probed %s", info.Summary())
	}
	noHEVC := MAG322.With(map[string]bool{"video:hevc": false}, "")
	if plan := Decide(info, noHEVC); plan.VideoNote == "" || plan.Convert {
		t.Fatalf("Decide = %+v", plan)
	}
	plan := EncodePlan(info, noHEVC, toneMap)
	if !plan.Encode || plan.Width != 640 || plan.Height != 360 || plan.ToneMap != toneMap {
		t.Fatalf("EncodePlan = %+v", plan)
	}
	out := filepath.Join(dir, "out.ts")
	if err := os.WriteFile(out, run(t, ffmpeg, plan.Args(input, 1.5)...), 0o600); err != nil {
		t.Fatal(err)
	}
	probed := run(t, ffprobe, "-hide_banner", "-loglevel", "error", "-show_format", "-show_streams", "-print_format", "json", out)
	got, err := ParseProbe(probed)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.Summary(), "mpegts: h264 640x360 8-bit; sound ac3 1ch") {
		t.Errorf("made %s", got.Summary())
	}
	if toneMap && !strings.Contains(string(probed), `"color_transfer": "bt709"`) {
		t.Errorf("tone-mapped video is not tagged BT.709: %s", probed)
	}
	// From 1.5 s exactly, not from the picture before: 2.5 s are left.
	if got.Duration < 2.4 || got.Duration > 2.6 {
		t.Errorf("made %v s, want the 2.5 s from 1.5 s", got.Duration)
	}
}
