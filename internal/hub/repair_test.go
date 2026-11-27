// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/mpegts"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tsinfo/tstest"
)

// The tests run this test binary as a fake ffmpeg, through a script that
// names how it behaves.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == "fake-ffmpeg" {
		os.Exit(fakeFFmpeg(os.Args[2], os.Args[3:]))
	}
	if len(os.Args) > 3 && os.Args[1] == "fake-gpu-ffmpeg" {
		os.Exit(fakeGPUFFmpeg(os.Args[2], os.Args[3], os.Args[4:]))
	}
	os.Exit(m.Run())
}

// fakeMark starts what the fake ffmpeg sends.
const fakeMark = "REPAIRED;"

// fakeFFmpeg acts as ffmpeg would, as far as the hub can tell: "copy"
// sends the mark and then the stream, "fail" reads a little and stops
// without sending anything, and "stop" sends the mark and twenty packets,
// then stops.
func fakeFFmpeg(mode string, args []string) int {
	if !slices.Equal(args, repairArgs) {
		fmt.Fprintf(os.Stderr, "unexpected arguments %q\n", args)
		return 2
	}
	// The shell that runs the fake adds PWD; Overscan should pass nothing.
	if env := slices.DeleteFunc(os.Environ(), func(v string) bool { return strings.HasPrefix(v, "PWD=") }); len(env) != 0 {
		fmt.Fprintf(os.Stderr, "an environment: %q\n", env)
		return 2
	}
	switch mode {
	case "copy":
		io.WriteString(os.Stdout, fakeMark)
		io.Copy(os.Stdout, os.Stdin)
		return 0
	case "fail":
		io.ReadFull(os.Stdin, make([]byte, 1000))
		fmt.Fprintln(os.Stderr, "[out#0/mpegts] Could not write header (incorrect codec parameters ?): Invalid argument")
		return 1
	case "stop":
		io.WriteString(os.Stdout, fakeMark)
		io.CopyN(os.Stdout, os.Stdin, 20*mpegts.PacketSize)
		fmt.Fprintln(os.Stderr, "Error muxing a packet")
		return 1
	}
	return 2
}

// fakeFFmpegPath writes a script that runs the fake ffmpeg in mode.
func fakeFFmpegPath(t *testing.T, mode string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := fmt.Sprintf("#!/bin/sh\nexec '%s' fake-ffmpeg %s \"$@\"\n", self, mode)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// program is ten seconds of a fictional MPEG-TS stream.
func program() []byte {
	m := tstest.New()
	m.Program(0, 250, "")
	return m.Bytes()
}

// repairHarness is a hub whose ffmpeg is the fake in mode, with channels
// for a finite stream, the same starting part way through a packet, an
// endless one, a text file, an HLS stream whose segments hold the finite
// stream behind an address that does not say so and behind one that does,
// and HLS with fragmented MP4 segments, which the relay cannot play.
func repairHarness(t *testing.T, mode string) *harness {
	t.Helper()
	stream := program()
	mux := http.NewServeMux()
	mux.HandleFunc("/program", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Header().Set("Content-Length", fmt.Sprint(len(stream)))
		w.Write(stream)
	})
	mux.HandleFunc("/endless", func(w http.ResponseWriter, r *http.Request) {
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
			case <-time.After(50 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/partway", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(stream[100:])
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "this is not a stream")
	})
	relayed := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXTINF:5,\nseg/1.ts\n#EXTINF:5,\nseg/2.ts\n#EXT-X-ENDLIST\n")
	}
	mux.HandleFunc("/relayed", relayed)
	mux.HandleFunc("/hls.m3u8", relayed)
	mux.HandleFunc("/fmp4.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:5\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:5,\nseg/1.m4s\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/seg/{n}", func(w http.ResponseWriter, r *http.Request) {
		half := len(stream) / mpegts.PacketSize / 2 * mpegts.PacketSize
		if r.PathValue("n") == "1.ts" {
			w.Write(stream[:half])
		} else {
			w.Write(stream[half:])
		}
	})
	srv := newServer(t, mux)
	h := newHarnessWith(t, 0, srv.URL, []string{"/program", "/partway", "/endless", "/text", "/relayed", "/hls.m3u8", "/fmp4.m3u8"})
	h.hub.UseFFmpeg(fakeFFmpegPath(t, mode), RepairSet)
	return h
}

// freshLink returns a new link for a channel as a portal makes it, from the
// lineup entry as the store has it now.
func (h *harness) freshLink(device int64, name string) string {
	h.t.Helper()
	e, err := h.hub.store.PlayableEntry(context.Background(), h.entries[name].EntryID)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.front.URL + h.hub.Link(device, e)
}

// setRepairHLS turns the repair of HLS streams on or off (D61).
func (h *harness) setRepairHLS(on bool) {
	h.t.Helper()
	if err := h.hub.store.SetRepairHLS(context.Background(), on); err != nil {
		h.t.Fatal(err)
	}
}

// setRepair turns repair on or off for a channel.
func (h *harness) setRepair(name string, on bool) {
	h.t.Helper()
	e := h.entries[name]
	if err := h.hub.store.SetEntriesRepair(context.Background(), store.DefaultLineupID, []int64{e.EntryID}, &on); err != nil {
		h.t.Fatal(err)
	}
}

func TestRepairGoesThroughFFmpeg(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("program", true)
	resp := get(t, h.link(7, "program"))
	if resp.Header.Get("Content-Length") != "" {
		t.Errorf("a repaired stream has the source's length: %s", resp.Header.Get("Content-Length"))
	}
	if got := body(t, resp); got != fakeMark+string(program()) {
		t.Errorf("got %d bytes, starting %q", len(got), got[:min(len(got), 20)])
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, "repairing a stream with ffmpeg") {
		t.Errorf("logs: %s", logs)
	}
	if n := h.hub.repairsRunning.Load(); n != 0 {
		t.Errorf("%d repairs running", n)
	}
}

func TestRepairIsOffUnlessSet(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("program", true)
	h.setRepair("program", false)
	resp := get(t, h.link(7, "program"))
	if resp.Header.Get("Content-Length") == "" {
		t.Error("the source's length is missing")
	}
	if got := body(t, resp); got != string(program()) {
		t.Errorf("got %d bytes, starting %q", len(got), got[:min(len(got), 20)])
	}
}

func TestRepairByDefault(t *testing.T) {
	h := repairHarness(t, "copy")
	if err := h.hub.store.SetRepairDefault(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := body(t, get(t, h.link(7, "program"))); !strings.HasPrefix(got, fakeMark) {
		t.Error("the default left a channel as it was")
	}
	h.setRepair("program", false)
	if got := body(t, get(t, h.link(7, "program"))); got != string(program()) {
		t.Error("a channel set not to be repaired was repaired")
	}
}

func TestRepairChoice(t *testing.T) {
	h := repairHarness(t, "copy")
	h.hub.UseFFmpeg(h.hub.ffmpeg, RepairAll)
	if got := body(t, get(t, h.link(7, "program"))); !strings.HasPrefix(got, fakeMark) {
		t.Error("repairing every stream left one as it was")
	}
	h.setRepair("program", true)
	h.hub.UseFFmpeg(h.hub.ffmpeg, RepairNone)
	if got := body(t, get(t, h.link(7, "program"))); got != string(program()) {
		t.Error("repairing none repaired one")
	}
	if h.hub.CanRepair() {
		t.Error("it can repair, repairing none")
	}
}

func TestRepairPlaysAsItIsWhenFFmpegFails(t *testing.T) {
	h := repairHarness(t, "fail")
	h.setRepair("program", true)
	if got := body(t, get(t, h.link(7, "program"))); got != string(program()) {
		t.Errorf("got %d bytes, want the stream's %d as it is", len(got), len(program()))
	}
	h.waitIdle()
	logs := h.logs.String()
	if !strings.Contains(logs, "ffmpeg could not repair a stream, so it plays as it is") ||
		!strings.Contains(logs, "Could not write header") {
		t.Errorf("logs: %s", logs)
	}
}

func TestRepairEndsTheStreamWhenFFmpegStops(t *testing.T) {
	h := repairHarness(t, "stop")
	h.setRepair("program", true)
	got := body(t, get(t, h.link(7, "program")))
	if want := fakeMark + string(program()[:20*mpegts.PacketSize]); got != want {
		t.Errorf("got %d bytes, want %d", len(got), len(want))
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, "ffmpeg stopped repairing a stream") || !strings.Contains(logs, "Error muxing") {
		t.Errorf("logs: %s", logs)
	}
}

func TestRepairLeavesWhatIsNotMPEGTS(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("text", true)
	if got := body(t, get(t, h.link(7, "text"))); got != "this is not a stream" {
		t.Errorf("got %q", got)
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, "not repairing a stream that is not MPEG-TS") ||
		!strings.Contains(logs, `data="text starting \"this is not a stream\""`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestRepairAStreamThatStartsPartWayThroughAPacket(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("partway", true)
	got := body(t, get(t, h.link(7, "partway")))
	if want := fakeMark + string(program()[mpegts.PacketSize:]); got != want {
		t.Errorf("got %d bytes, want %d", len(got), len(want))
	}
	h.waitIdle()
	logs := h.logs.String()
	for _, want := range []string{"did not start with a whole packet", "bytes=88", "repairing a stream with ffmpeg"} {
		if !strings.Contains(logs, want) {
			t.Errorf("no %q in logs: %s", want, logs)
		}
	}
}

func TestRepairWithoutFFmpeg(t *testing.T) {
	h := repairHarness(t, "copy")
	h.hub.UseFFmpeg("", RepairSet)
	h.setRepair("program", true)
	if got := body(t, get(t, h.link(7, "program"))); got != string(program()) {
		t.Errorf("got %d bytes", len(got))
	}
	if logs := h.logs.String(); !strings.Contains(logs, "no ffmpeg to repair it with") {
		t.Errorf("logs: %s", logs)
	}
	if h.hub.CanRepair() {
		t.Error("it can repair without ffmpeg")
	}
}

func TestRepairsAreCapped(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("program", true)
	h.hub.repairsRunning.Store(maxRepairs)
	if got := body(t, get(t, h.link(7, "program"))); got != string(program()) {
		t.Errorf("got %d bytes", len(got))
	}
	if logs := h.logs.String(); !strings.Contains(logs, "too many streams are being repaired") {
		t.Errorf("logs: %s", logs)
	}
	if n := h.hub.repairsRunning.Load(); n != maxRepairs {
		t.Errorf("%d repairs running", n)
	}
}

func TestRepairStopsWithThePlayer(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("endless", true)
	resp := h.play(h.link(7, "endless"))
	streams := h.hub.Streams()
	if len(streams) != 1 || !streams[0].Repaired {
		t.Errorf("streams %+v", streams)
	}
	resp.Body.Close()
	h.waitIdle()
	deadline := time.Now().Add(5 * time.Second)
	for h.hub.repairsRunning.Load() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("ffmpeg still runs")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if streams := h.hub.Streams(); len(streams) != 1 || streams[0].Repaired {
		t.Errorf("streams after the player left %+v", streams)
	}
}

func TestRepairOfARelayedStream(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("relayed", true)
	link := h.link(7, "relayed")
	if !strings.HasSuffix(link, ".ts") {
		t.Fatalf("link %q", link)
	}
	if got := body(t, get(t, link)); got != fakeMark+string(program()) {
		t.Errorf("got %d bytes, starting %q", len(got), got[:min(len(got), 20)])
	}
}

func TestRepairOfHLS(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("hls.m3u8", true)
	if link := h.freshLink(7, "hls.m3u8"); !strings.HasSuffix(link, ".m3u8") {
		t.Errorf("with HLS not repaired, link %q", link)
	}

	h.setRepairHLS(true)
	link := h.freshLink(7, "hls.m3u8")
	if !strings.HasSuffix(link, ".ts") {
		t.Fatalf("with HLS repaired, link %q", link)
	}
	if got := body(t, get(t, link)); got != fakeMark+string(program()) {
		t.Errorf("got %d bytes, starting %q", len(got), got[:min(len(got), 20)])
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, "relaying an HLS stream as one continuous stream") ||
		!strings.Contains(logs, "repairing a stream with ffmpeg") {
		t.Errorf("logs: %s", logs)
	}

	// Only channels that are repaired, and only with an ffmpeg to do it.
	h.setRepair("hls.m3u8", false)
	if link := h.freshLink(7, "hls.m3u8"); !strings.HasSuffix(link, ".m3u8") {
		t.Errorf("not repaired, link %q", link)
	}
	h.setRepair("hls.m3u8", true)
	h.hub.UseFFmpeg("", RepairSet)
	if link := h.freshLink(7, "hls.m3u8"); !strings.HasSuffix(link, ".m3u8") {
		t.Errorf("without ffmpeg, link %q", link)
	}
}

func TestRepairOfHLSThatTheRelayCannotPlay(t *testing.T) {
	h := repairHarness(t, "copy")
	h.setRepair("fmp4.m3u8", true)
	h.setRepairHLS(true)
	link := h.freshLink(7, "fmp4.m3u8")
	if !strings.HasSuffix(link, ".ts") {
		t.Fatalf("link %q", link)
	}
	// The player, asking for one stream, is refused rather than given a
	// playlist, and its next link is HLS.
	resp := get(t, link)
	if text := body(t, resp); resp.StatusCode != http.StatusBadGateway || !strings.Contains(text, "ask for the channel again") {
		t.Errorf("status %s: %s", resp.Status, text)
	}
	if logs := h.logs.String(); !strings.Contains(logs, "cannot make an HLS stream one stream to repair it") ||
		!strings.Contains(logs, `reason="it has fragmented MP4 segments"`) {
		t.Errorf("logs: %s", logs)
	}
	if link := h.freshLink(7, "fmp4.m3u8"); !strings.HasSuffix(link, ".m3u8") {
		t.Errorf("after the refusal, link %q", link)
	}
	// Some hours later, the hub tries again.
	h.advance(unrelayableFor)
	if link := h.freshLink(7, "fmp4.m3u8"); !strings.HasSuffix(link, ".ts") {
		t.Errorf("later, link %q", link)
	}
}

func TestMessageTail(t *testing.T) {
	var logs syncBuffer
	m := &messageTail{log: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		about: []any{"channel", "News"}}
	io.WriteString(m, "one\ntwo\n\nthree")
	io.WriteString(m, " more\nfour\nfive\nsix\n")
	io.WriteString(m, strings.Repeat("x", maxMessage+10))
	if got := m.String(); got != "three more | four | five | six | "+strings.Repeat("x", maxMessage+10)[:maxMessage]+"…" {
		t.Errorf("last messages %q", got)
	}
	if !strings.Contains(logs.String(), `msg="ffmpeg says" channel=News message=one`) {
		t.Errorf("logs: %s", logs.String())
	}
}

// The tests below run the real ffmpeg, if there is one.

// realFFmpeg returns the path of ffmpeg, or skips the test.
func realFFmpeg(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg in PATH")
	}
	return path
}

// encode makes seconds of a test picture and tone with ffmpeg, as MPEG-TS
// with MPEG-2 video and sound, which every ffmpeg can encode. offset sets
// the clock's start, and pattern is the picture: testsrc, smptebars and so
// on.
func encode(t *testing.T, ffmpeg, pattern string, seconds, offset int) []byte {
	t.Helper()
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", pattern+"=size=320x180:rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", fmt.Sprint(seconds), "-c:v", "mpeg2video", "-g", "25", "-c:a", "mp2",
		"-output_ts_offset", fmt.Sprint(offset), "-f", "mpegts", "pipe:1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("encoding: %v: %s", err, stderr.String())
	}
	return out
}

// withoutPID returns data without the packets on pid.
func withoutPID(data []byte, pid uint16) []byte {
	var out []byte
	for ; len(data) >= mpegts.PacketSize; data = data[mpegts.PacketSize:] {
		if mpegts.Packet(data[:mpegts.PacketSize]).PID() != pid {
			out = append(out, data[:mpegts.PacketSize]...)
		}
	}
	return out
}

// newServer starts a server that the test closes.
func newServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestRepairWithFFmpeg(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	// Six seconds, then six seconds of new pictures with the clock set
	// back four seconds, as a source's restart can send.
	jumping := append(encode(t, ffmpeg, "testsrc", 6, 0), encode(t, ffmpeg, "smptebars", 6, 2)...)
	// A stream whose program map lists a sound track that carries nothing.
	silent := withoutPID(encode(t, ffmpeg, "testsrc", 4, 0), 0x101)

	mux := http.NewServeMux()
	mux.HandleFunc("/jumping/{n}", func(w http.ResponseWriter, r *http.Request) { w.Write(jumping) })
	mux.HandleFunc("/silent", func(w http.ResponseWriter, r *http.Request) { w.Write(silent) })
	srv := newServer(t, mux)
	h := newHarnessWith(t, 0, srv.URL, []string{"/jumping/1", "/jumping/2", "/silent"})
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.setRepair("jumping/1", true)
	h.setRepair("silent", true)

	r := report([]byte(body(t, get(t, h.link(7, "jumping/2")))))
	if r.Clock == nil || len(r.Clock.Jumps) != 1 || r.Clock.Jumps[0].By > -3*time.Second {
		t.Fatalf("as it is, the clock should jump back: %+v", r.Clock)
	}

	repaired := []byte(body(t, get(t, h.link(7, "jumping/1"))))
	r = report(repaired)
	if r.Clock == nil || len(r.Clock.Jumps) != 0 {
		t.Errorf("repaired, the clock still jumps: %+v", r.Clock)
	}
	if r.Clock != nil && (r.Clock.Covered < 11*time.Second || r.Clock.Covered > 13*time.Second) {
		t.Errorf("repaired, the clock covers %s", r.Clock.Covered)
	}
	if len(r.Streams) != 2 || r.Streams[0].Kind != "video" || r.Streams[1].Kind != "audio" {
		t.Errorf("repaired, the streams are %+v", r.Streams)
	}
	if r.Lost != 0 || r.SyncLosses != 0 {
		t.Errorf("repaired: %d packets lost, out of step %d times", r.Lost, r.SyncLosses)
	}

	if got := []byte(body(t, get(t, h.link(7, "silent")))); !bytes.Equal(got, silent) {
		t.Errorf("got %d bytes, want the stream's %d as it is", len(got), len(silent))
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, "ffmpeg could not repair a stream, so it plays as it is") {
		t.Errorf("logs: %s", logs)
	}
}

func TestRepairOfHLSWithFFmpeg(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	// Two segments: six seconds, then six seconds of new pictures with the
	// clock set back four seconds.
	first, second := encode(t, ffmpeg, "testsrc", 6, 0), encode(t, ffmpeg, "smptebars", 6, 2)
	mux := http.NewServeMux()
	mux.HandleFunc("/jumping.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\nseg/a.ts\n#EXTINF:6,\nseg/b.ts\n#EXT-X-ENDLIST\n")
	})
	mux.HandleFunc("/seg/a.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(first) })
	mux.HandleFunc("/seg/b.ts", func(w http.ResponseWriter, r *http.Request) { w.Write(second) })
	srv := newServer(t, mux)
	h := newHarnessWith(t, 0, srv.URL, []string{"/jumping.m3u8"})
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.setRepair("jumping.m3u8", true)
	h.setRepairHLS(true)

	link := h.freshLink(7, "jumping.m3u8")
	if !strings.HasSuffix(link, ".ts") {
		t.Fatalf("link %q", link)
	}
	r := report([]byte(body(t, get(t, link))))
	if r.Clock == nil || len(r.Clock.Jumps) != 0 {
		t.Errorf("repaired, the clock still jumps: %+v", r.Clock)
	}
	if r.Clock != nil && (r.Clock.Covered < 11*time.Second || r.Clock.Covered > 13*time.Second) {
		t.Errorf("repaired, the clock covers %s", r.Clock.Covered)
	}
	if r.Lost != 0 || r.SyncLosses != 0 {
		t.Errorf("repaired: %d packets lost, out of step %d times", r.Lost, r.SyncLosses)
	}
}
