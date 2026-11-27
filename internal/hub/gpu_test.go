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
	"overscan.gureet.ca/internal/mpegts"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streaminfo"
	"overscan.gureet.ca/internal/streamprofile"
)

// The marks with which the fake ffmpeg starts what it sends, by how it
// was asked to transcode: on the graphics, decoding there or in software,
// or in software.
const (
	markDecode   = "GPU-DECODE;"
	markUpload   = "GPU-UPLOAD;"
	markSoftware = "SOFTWARE;"
)

// fakeGPUFFmpeg acts as ffmpeg would on a server with graphics, as far as
// the hub can tell. It notes each run in the file at notes, by how it was
// asked to transcode, and does as mode says for that way: such as
// "decode=fail,upload=stop". "copy", the default, sends its mark and then
// the stream; "fail" reads a little and stops without sending anything;
// "stop" sends its mark and twenty packets, then stops.
func fakeGPUFFmpeg(notes, mode string, args []string) int {
	way, mark := "software", markSoftware
	switch {
	case slices.Contains(args, "-hwaccel"):
		way, mark = "decode", markDecode
	case slices.Contains(args, "-init_hw_device"):
		way, mark = "upload", markUpload
	}
	f, err := os.OpenFile(notes, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return 2
	}
	fmt.Fprintf(f, "%s %s\n", way, strings.Join(args, " "))
	f.Close()
	do := "copy"
	for _, part := range strings.Split(mode, ",") {
		if w, d, ok := strings.Cut(part, "="); ok && w == way {
			do = d
		}
	}
	switch do {
	case "copy":
		io.WriteString(os.Stdout, mark)
		io.Copy(os.Stdout, os.Stdin)
		return 0
	case "fail":
		io.ReadFull(os.Stdin, make([]byte, 1000))
		fmt.Fprintln(os.Stderr, "[vf#0:0] Error reinitializing filters!")
		return 1
	case "stop":
		io.WriteString(os.Stdout, mark)
		io.CopyN(os.Stdout, os.Stdin, 20*mpegts.PacketSize)
		fmt.Fprintln(os.Stderr, "[h264 @ 0x1] Failed to end picture decode issue: 23 (internal decoding error).")
		return 1
	}
	return 2
}

// gpuHarness is a hub whose ffmpeg is the fake with graphics, in mode, as
// the test found them in found, with a channel set to be transcoded
// always with a profile, and a player that takes channels as their
// sources send them.
type gpuHarness struct {
	*harness
	notes  string
	player int64
}

func newGPUHarness(t *testing.T, mode string, found gpu.Found) *gpuHarness {
	t.Helper()
	stream := program()
	mux := http.NewServeMux()
	mux.HandleFunc("/program", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(stream)
	})
	h := newHarnessWith(t, 0, newServer(t, mux).URL, []string{"/program"})
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
	h.hub.UseEncoder(Encoder{H264: true})
	h.hub.UseGPU(found)
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
	e := h.entries["program"]
	if err := st.SetEntriesTranscode(ctx, e.LineupID, []int64{e.EntryID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	return &gpuHarness{harness: h, notes: notes, player: h.hub.Player(store.HouseholdAccount, "m3u", "192.0.2.5", "VLC/3.0")}
}

// play plays the channel, and returns what the player got.
func (g *gpuHarness) play() string {
	g.t.Helper()
	resp := get(g.t, g.front.URL+g.hub.Link(g.player, g.entries["program"]))
	if resp.StatusCode != http.StatusOK {
		g.t.Fatalf("status %s", resp.Status)
	}
	defer g.waitIdle()
	return body(g.t, resp)
}

// runs returns how ffmpeg was asked to transcode, run by run, and
// forgets them.
func (g *gpuHarness) runs() []string {
	g.t.Helper()
	b, err := os.ReadFile(g.notes)
	if err != nil && !os.IsNotExist(err) {
		g.t.Fatal(err)
	}
	os.Remove(g.notes)
	var out []string
	for line := range strings.Lines(string(b)) {
		way, _, _ := strings.Cut(line, " ")
		out = append(out, way)
	}
	return out
}

// args returns the arguments of the last run, as the notes have them.
func (g *gpuHarness) lastArgs() string {
	b, _ := os.ReadFile(g.notes)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	return lines[len(lines)-1]
}

// intel is what the test finds of graphics that do everything.
var intel = gpu.Found{Device: "/dev/dri/renderD128", Driver: "Intel iHD driver",
	Encodes:     map[string]gpu.Encode{gpu.H264: {OK: true, ICQ: true, VBR: true}, gpu.HEVC: {OK: true, LowPower: true, ICQ: true}},
	Decodes:     map[string]bool{gpu.H264: true, gpu.HEVC: true, gpu.MPEG2: true},
	Deinterlace: true, Scale: true, Rate: true}

func TestGraphicsTranscode(t *testing.T) {
	g := newGPUHarness(t, "", intel)
	got := g.play()
	if !strings.HasPrefix(got, markDecode) || got != markDecode+string(program()) {
		t.Errorf("got %d bytes, starting %q", len(got), got[:min(len(got), 20)])
	}
	args := g.lastArgs()
	for _, want := range []string{"-init_hw_device vaapi=va:/dev/dri/renderD128", "-hwaccel vaapi", "-c:v h264_vaapi", "-rc_mode ICQ -global_quality 29"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %s", want, args)
		}
	}
	if runs := g.runs(); !slices.Equal(runs, []string{"decode"}) {
		t.Errorf("runs %v", runs)
	}
	logs := g.logs.String()
	if !strings.Contains(logs, `msg="transcoding a stream with ffmpeg" channel=program source=main`) ||
		!strings.Contains(logs, "profile=small set_by=channel on=graphics decode=graphics") {
		t.Errorf("logs:\n%s", logs)
	}
	if n := len(g.hub.gpuEncodes); n != 0 {
		t.Errorf("%d devices hold the graphics", n)
	}
}

func TestGraphicsFallBack(t *testing.T) {
	cases := []struct {
		name, mode string
		found      func(gpu.Found) gpu.Found
		cap        int // the graphics' number at a time; -1 leaves the default
		software   int // the processor's
		runs       []string
		mark       string
		logs       []string
		then       []string // the runs when the channel plays again
		seen       string   // the codec that the channel last showed
	}{
		{name: "decoding fails at start", mode: "decode=fail", cap: -1, software: 1,
			runs: []string{"decode", "upload"}, mark: markUpload,
			logs: []string{`msg="ffmpeg could not transcode a stream on the graphics; it is transcoded on the graphics, decoded in software"`,
				`ffmpeg="[vf#0:0] Error reinitializing filters!"`, "on=graphics decode=software"},
			then: []string{"upload"}},
		{name: "the graphics fail at start", mode: "decode=fail,upload=fail", cap: -1, software: 1,
			runs: []string{"decode", "upload", "software"}, mark: markSoftware,
			logs: []string{`msg="ffmpeg could not transcode a stream on the graphics; it is transcoded in software"`}},
		{name: "and software is off", mode: "decode=fail,upload=fail", cap: -1, software: 0,
			runs: []string{"decode", "upload"}, mark: "",
			logs: []string{`msg="ffmpeg could not transcode a stream, so it plays as it is"`}},
		{name: "decoding stops part way", mode: "decode=stop", cap: -1, software: 1,
			runs: []string{"decode", "upload"}, mark: markDecode,
			logs: []string{`msg="ffmpeg stopped transcoding a stream on the graphics; it starts again, decoding the stream in software"`},
			then: []string{"upload"}},
		{name: "the graphics do not decode it", found: func(f gpu.Found) gpu.Found { f.Decodes = nil; return f }, cap: -1, software: 1,
			seen: "H.264", runs: []string{"upload"}, mark: markUpload},
		{name: "a codec that the test does not try", cap: -1, software: 1,
			seen: "MPEG-1 video", runs: []string{"upload"}, mark: markUpload},
		{name: "no filters on the graphics", found: func(f gpu.Found) gpu.Found { f.Scale = false; return f }, cap: -1, software: 1,
			runs: []string{"upload"}, mark: markUpload, logs: []string{"decode=software"}},
		{name: "the graphics are off", cap: 0, software: 1, runs: []string{"software"}, mark: markSoftware},
		{name: "no codec on the graphics", found: func(f gpu.Found) gpu.Found { f.Encodes = map[string]gpu.Encode{gpu.HEVC: {OK: true}}; return f },
			cap: -1, software: 1, runs: []string{"software"}, mark: markSoftware},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			found := intel
			if c.found != nil {
				found = c.found(intel)
			}
			g := newGPUHarness(t, c.mode, found)
			ctx := context.Background()
			if c.cap >= 0 {
				if err := g.hub.store.SetGPUTranscodes(ctx, c.cap); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.hub.store.SetTranscodes(ctx, c.software); err != nil {
				t.Fatal(err)
			}
			if c.seen != "" {
				if err := g.hub.store.NoteStreamDetails(ctx, g.entries["program"].ChannelID, streaminfo.Details{Video: c.seen}, g.hub.now()); err != nil {
					t.Fatal(err)
				}
			}
			got := g.play()
			want := c.mark + string(program())
			if c.mode == "decode=stop" {
				// Twenty packets on the graphics, then the rest
				// from the graphics again, decoding in software.
				if !strings.HasPrefix(got, markDecode) || !strings.Contains(got, markUpload) {
					t.Errorf("got %d bytes, starting %q", len(got), got[:min(len(got), 20)])
				}
			} else if got != want {
				t.Errorf("got %d bytes, starting %q; want %q", len(got), got[:min(len(got), 20)], c.mark)
			}
			if runs := g.runs(); !slices.Equal(runs, c.runs) {
				t.Errorf("runs %v, want %v", runs, c.runs)
			}
			logs := g.logs.String()
			for _, l := range c.logs {
				if !strings.Contains(logs, l) {
					t.Errorf("logs lack %q:\n%s", l, logs)
				}
			}
			if c.then != nil {
				g.play()
				if runs := g.runs(); !slices.Equal(runs, c.then) {
					t.Errorf("then runs %v, want %v", runs, c.then)
				}
				// A new test forgets it.
				g.hub.UseGPU(found)
				g.play()
				if runs := g.runs(); runs[0] != "decode" {
					t.Errorf("after a new test, runs %v", runs)
				}
			}
			if n := len(g.hub.gpuEncodes) + len(g.hub.encodes); n != 0 {
				t.Errorf("%d devices hold slots", n)
			}
		})
	}
}

func TestGraphicsFull(t *testing.T) {
	g := newGPUHarness(t, "", intel)
	ctx := context.Background()
	if err := g.hub.store.SetGPUTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	// Another device holds the graphics' one slot.
	if !g.hub.takeGPU(99, 1) {
		t.Fatal("cannot take the slot")
	}
	if got := g.play(); !strings.HasPrefix(got, markSoftware) {
		t.Errorf("got %q", got[:min(len(got), 20)])
	}
	if runs := g.runs(); !slices.Equal(runs, []string{"software"}) {
		t.Errorf("runs %v", runs)
	}
	if logs := g.logs.String(); !strings.Contains(logs, `msg="the graphics transcode as many streams as they may; this one is transcoded in software"`) ||
		!strings.Contains(logs, "most=1") || strings.Count(logs, "the graphics transcode as many") != 1 {
		t.Errorf("logs:\n%s", logs)
	}
	// With the processor's slot taken too, it plays as its source sends
	// it.
	if !g.hub.takeEncode(98, 1) {
		t.Fatal("cannot take the processor's slot")
	}
	g.logs.Reset()
	if got := g.play(); got != string(program()) {
		t.Errorf("got %q", got[:min(len(got), 20)])
	}
	if logs := g.logs.String(); !strings.Contains(logs, "the server transcodes as many streams as it may; this one plays as its source sends it") {
		t.Errorf("logs:\n%s", logs)
	}
	// The device that holds the slot is not held up by its own.
	g.hub.endEncode(98)
	if !g.hub.takeGPU(99, 1) {
		t.Error("a device's own transcode held it up")
	}
}

// Without libx265, a profile that makes HEVC is transcoded on graphics
// that make HEVC, and plays as its source sends it on a server without
// them.
func TestGraphicsMakeWhatSoftwareCannot(t *testing.T) {
	g := newGPUHarness(t, "", intel)
	ctx := context.Background()
	hevc, _ := streamprofile.Check(streamprofile.Profile{Name: "hevc", Codec: streamprofile.HEVC, MaxKbps: 4000})
	if err := g.hub.store.AddTranscodeProfile(ctx, hevc); err != nil {
		t.Fatal(err)
	}
	e := g.entries["program"]
	if err := g.hub.store.SetEntriesTranscode(ctx, e.LineupID, []int64{e.EntryID}, store.Transcode{Profile: "hevc"}); err != nil {
		t.Fatal(err)
	}
	if got := g.play(); !strings.HasPrefix(got, markDecode) {
		t.Errorf("got %q", got[:min(len(got), 20)])
	}
	if args := g.lastArgs(); !strings.Contains(args, "-c:v hevc_vaapi -profile:v main -low_power 1 -rc_mode ICQ") {
		t.Errorf("args %s", args)
	}
	g.runs()
	g.hub.UseGPU(gpu.Found{Why: "no graphics"})
	g.logs.Reset()
	if got := g.play(); got != string(program()) {
		t.Errorf("got %q", got[:min(len(got), 20)])
	}
	if logs := g.logs.String(); !strings.Contains(logs, "lacks the encoder that it needs") {
		t.Errorf("logs:\n%s", logs)
	}
}
