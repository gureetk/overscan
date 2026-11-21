// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// realFFprobe returns the path of ffprobe, or skips the test.
func realFFprobe(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe in PATH")
	}
	return path
}

// film makes 12 seconds of a test picture in H.264, with a picture to
// start from every 2 seconds and B-frames, and a tone on each sound track
// given by codec, as Matroska.
func film(t *testing.T, ffmpeg string, codecs ...string) []byte {
	return makeFilm(t, ffmpeg, 12, "320x180", "", codecs...)
}

// makeFilm makes a film as film does, of the given length in seconds,
// picture size and video bit rate ("" for the encoder's own).
func makeFilm(t *testing.T, ffmpeg string, seconds int, size, rate string, codecs ...string) []byte {
	t.Helper()
	video := []string{"-c:v", "libx264", "-preset", "ultrafast", "-g", "50", "-bf", "2"}
	if rate != "" {
		video = append(video, "-b:v", rate)
	}
	return makeMedia(t, ffmpeg, seconds, size, video, codecs...)
}

// makeMedia makes a film of a test picture of the given length in seconds
// and size, which ffmpeg's arguments video encode, and a tone on each sound
// track given by codec, as Matroska.
func makeMedia(t *testing.T, ffmpeg string, seconds int, size string, video []string, codecs ...string) []byte {
	t.Helper()
	length := strconv.Itoa(seconds)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=size=" + size + ":rate=25:duration=" + length,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=" + length,
		"-map", "0:v"}
	args = append(args, video...)
	for i, codec := range codecs {
		n := strconv.Itoa(i)
		args = append(args, "-map", "1:a", "-c:a:"+n, codec, "-ac:a:"+n, "2")
		if codec == "dca" {
			args = append(args, "-ac:a:"+n, "6", "-strict", "-2")
		}
	}
	// To a file, which ffmpeg can go back in to write the duration and
	// the index.
	path := filepath.Join(t.TempDir(), "film.mkv")
	cmd := exec.Command(ffmpeg, append(args, path)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("making a film: %v: %s", err, stderr.String())
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// converting returns an item harness whose hub converts, with the film on
// its source made of the given sound tracks.
func converting(t *testing.T, limit int, codecs ...string) *itemHarness {
	t.Helper()
	ffmpeg, ffprobe := realFFmpeg(t), realFFprobe(t)
	h := newItemHarness(t, limit)
	h.files.mu.Lock()
	h.files.content = film(t, ffmpeg, codecs...)
	h.files.settle = true
	h.files.mu.Unlock()
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseFFprobe(ffprobe)
	return h
}

// startAt asks a converted link's start path where the stream from a time
// starts.
func (h *itemHarness) startAt(pb Playback, at string) float64 {
	h.t.Helper()
	resp := get(h.t, h.front.URL+pb.StartPath+"?t="+at)
	var out struct{ T float64 }
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK {
		h.t.Fatalf("start: status %s, %v", resp.Status, err)
	}
	resp.Body.Close()
	return out.T
}

// probeFile returns what ffprobe finds in data.
func probeFile(t *testing.T, data []byte) media.Info {
	t.Helper()
	path := filepath.Join(t.TempDir(), "out.ts")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(realFFprobe(t), "-hide_banner", "-loglevel", "error", "-show_format", "-show_streams",
		"-print_format", "json", path).Output()
	if err != nil {
		t.Fatal(err)
	}
	info, err := media.ParseProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestPrepareWithoutFFmpeg(t *testing.T) {
	h := newItemHarness(t, 1)
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted || !strings.HasPrefix(pb.Path, "/vod/") || !strings.HasSuffix(pb.Path, ".mkv") || pb.Duration != 0 {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	if n := len(h.hub.Streams()); n != 0 {
		t.Errorf("%d sessions before the box asks", n)
	}
	resp := get(t, h.front.URL+pb.Path)
	if got := body(t, resp); resp.StatusCode != http.StatusOK || len(got) != len(h.files.content) {
		t.Errorf("status %s, %d bytes", resp.Status, len(got))
	}
}

func TestConvertFilm(t *testing.T) {
	h := converting(t, 1, "dca", "aac")
	ctx := context.Background()
	pb, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !pb.Converted || !strings.HasSuffix(pb.Path, ".ts") || pb.StartPath != strings.TrimSuffix(pb.Path, ".ts")+"/start" ||
		pb.Duration < 11.9 || pb.Duration > 12.1 || len(pb.Tracks) != 2 || !pb.Tracks[0].Convert || pb.Tracks[1].Convert {
		t.Fatalf("Prepare = %+v", pb)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `msg="read what a title's file holds" title="The Long Field" source=main file="matroska: h264 320x180 8-bit; sound dts 6ch, aac 2ch"`) ||
		!strings.Contains(logs, `msg="a title plays through ffmpeg, which converts what the box cannot play" title="The Long Field" source=main device=7 why="its sound (dts)" sound="dts 6ch to ac3, aac 2ch"`) ||
		strings.Contains(logs, secret) {
		t.Errorf("logs: %s", logs)
	}
	titleID, episodeID := h.film.MediaKey()
	if info, ok, err := h.hub.store.Media(ctx, titleID, episodeID); !ok || err != nil || info.Size != int64(len(h.files.content)) {
		t.Errorf("kept %+v, %v, %v", info, ok, err)
	}

	// The box asks where the stream from 5 s starts: at the picture at
	// 4 s, which is 4.021 s into the file, whose clocks start 21 ms early
	// for the AAC track's priming.
	if at := h.startAt(pb, "5"); at != 4.021 {
		t.Errorf("start from 5 s at %v, want 4.021", at)
	}
	if at := h.startAt(pb, "0"); at != 0 {
		t.Errorf("start from 0 at %v", at)
	}
	resp := get(t, h.front.URL+pb.Path+"?t=4.021")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" || resp.Header.Get("Accept-Ranges") != "none" {
		t.Fatalf("status %s, header %v", resp.Status, resp.Header)
	}
	first := make([]byte, 188)
	if _, err := io.ReadFull(resp.Body, first); err != nil {
		t.Fatal(err)
	}
	if s := h.hub.Streams(); len(s) != 1 || !s[0].Converted || !s[0].Playing || s[0].Kind != PlaysMovie {
		t.Errorf("streams %+v", s)
	}
	rest, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	got := probeFile(t, append(first, rest...))
	if got.Format != "mpegts" || !strings.HasPrefix(got.Summary(), "mpegts: h264 320x180 8-bit; sound ac3 6ch, aac 2ch") ||
		got.Duration < 7.9 || got.Duration > 8.1 {
		t.Errorf("made %s, %v s", got.Summary(), got.Duration)
	}
	h.waitIdle()
	if s := h.hub.Streams(); len(s) != 1 || s[0].Converted || s[0].Playing {
		t.Errorf("streams after %+v", s)
	}

	// The next time, the hub knows what the file holds.
	if _, err := h.hub.Prepare(ctx, 7, h.film, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(h.logs.String(), "read what a title's file holds"); n != 1 {
		t.Errorf("read the file %d times", n)
	}
	h.files.mu.Lock()
	most := h.files.most
	h.files.mu.Unlock()
	if most != 1 {
		t.Errorf("%d requests to the source at once, over its limit of 1", most)
	}
}

// TestConvertNothingInAWhile stops ffmpeg that makes nothing of a film in
// a while, so that the player is not left waiting for a stream that never
// comes, and answers it with an error.
func TestConvertNothingInAWhile(t *testing.T) {
	h := converting(t, 1, "dca", "aac")
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	first := convertFirstOutput
	convertFirstOutput = 300 * time.Millisecond
	t.Cleanup(func() { convertFirstOutput = first })
	// An ffmpeg that waits and makes nothing.
	silent := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(silent, []byte("#!/bin/sh\nexec sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	h.hub.UseFFmpeg(silent, RepairSet)
	start := time.Now()
	resp := get(t, h.front.URL+pb.Path)
	body(t, resp)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Errorf("status %s", resp.Status)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Errorf("the answer took %s", took)
	}
	if !strings.Contains(h.logs.String(), `msg="ffmpeg made nothing of a title in a while, so the hub stopped it; the player can ask again" title="The Long Field" source=main device=7 waited=300ms`) {
		t.Errorf("logs:\n%s", h.logs)
	}
}

func TestConvertedStreamSeeks(t *testing.T) {
	h := converting(t, 1, "dca")
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	// The player is part way through when it seeks: the stream before
	// ends, and the new one starts at the picture asked for.
	resp := get(t, h.front.URL+pb.Path)
	if _, err := io.ReadFull(resp.Body, make([]byte, 188*10)); err != nil {
		t.Fatal(err)
	}
	at := h.startAt(pb, "9")
	if at != 8.0 {
		t.Errorf("start from 9 s at %v, want 8", at)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	resp = get(t, h.front.URL+pb.Path+"?t=8.000")
	data, err2 := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err2 != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("status %s, %v", resp.Status, err2)
	}
	if got := probeFile(t, data); got.Duration < 3.9 || got.Duration > 4.1 {
		t.Errorf("made %v s from 8 s, want 4", got.Duration)
	}
	h.waitIdle()
	h.files.mu.Lock()
	most := h.files.most
	h.files.mu.Unlock()
	if most != 1 {
		t.Errorf("%d requests to the source at once", most)
	}
	_ = err
	if logs := h.logs.String(); strings.Count(logs, `msg="converting a title with ffmpeg"`) != 2 ||
		!strings.Contains(logs, `from=0:00:08`) || strings.Contains(logs, "ffmpeg stopped") {
		t.Errorf("logs: %s", logs)
	}
}

func TestPlaysAsItIs(t *testing.T) {
	h := converting(t, 1, "aac")
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted || !strings.HasSuffix(pb.Path, ".mkv") || pb.Duration < 11.9 {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	// The probe opened the session, which the box's request goes on with.
	if s := h.hub.Streams(); len(s) != 1 || s[0].Playing {
		t.Errorf("streams %+v", s)
	}
	resp := get(t, h.front.URL+pb.Path)
	if got := body(t, resp); resp.StatusCode != http.StatusOK || len(got) != len(h.files.content) {
		t.Errorf("status %s, %d bytes", resp.Status, len(got))
	}
	if n := strings.Count(h.logs.String(), "stream started"); n != 1 {
		t.Errorf("%d sessions", n)
	}

	// A file that has changed is read again the next time.
	h.waitIdle()
	h.files.mu.Lock()
	h.files.content = append(h.files.content, make([]byte, 1000)...)
	h.files.mu.Unlock()
	resp = get(t, h.front.URL+pb.Path)
	body(t, resp)
	titleID, episodeID := h.film.MediaKey()
	if _, ok, _ := h.hub.store.Media(context.Background(), titleID, episodeID); ok {
		t.Error("what the changed file held is still kept")
	}
	if logs := h.logs.String(); !strings.Contains(logs, "a title's file has changed since Overscan read what it holds") {
		t.Errorf("logs: %s", logs)
	}

	// So does one played from a link whose session starts when the box
	// asks for it, once what the file holds is kept.
	h.waitIdle()
	if _, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := h.hub.store.Media(context.Background(), titleID, episodeID); !ok {
		t.Fatal("not read again")
	}
	h.waitIdle()
	h.files.mu.Lock()
	h.files.content = h.files.content[:len(h.files.content)-500]
	h.files.mu.Unlock()
	body(t, get(t, h.itemLink(7, h.film)))
	if _, ok, _ := h.hub.store.Media(context.Background(), titleID, episodeID); ok {
		t.Error("what the changed file held is still kept, played from a link of its own")
	}
}

func TestProbeFails(t *testing.T) {
	h := converting(t, 1, "aac")
	h.files.mu.Lock()
	h.files.content = bytes.Repeat([]byte("not a film "), 1000)
	h.files.mu.Unlock()
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || pb.Converted || pb.Duration != 0 {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	titleID, episodeID := h.film.MediaKey()
	if _, ok, _ := h.hub.store.Media(context.Background(), titleID, episodeID); ok {
		t.Error("kept what a file that could not be read holds")
	}
	if logs := h.logs.String(); !strings.Contains(logs, "cannot read what a title's file holds, so it plays as it is") {
		t.Errorf("logs: %s", logs)
	}
}

func TestPrepareAtTheLimit(t *testing.T) {
	h := converting(t, 1, "dca")
	// Another device plays the episode, from the same source.
	other := get(t, h.itemLink(8, h.episode))
	defer other.Body.Close()
	if _, err := io.ReadFull(other.Body, make([]byte, 1000)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil); !errors.Is(err, ErrLimit) {
		t.Errorf("Prepare at the limit: %v", err)
	}
}

func TestConvertedLinks(t *testing.T) {
	h := converting(t, 1, "dca")
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	direct := h.itemLink(7, h.film)
	for _, c := range []struct {
		url  string
		want int
	}{
		{h.front.URL + pb.Path + "?t=x", http.StatusBadRequest},
		{h.front.URL + pb.StartPath + "?t=-1", http.StatusBadRequest},
		{h.front.URL + strings.TrimSuffix(pb.Path, ".ts") + "/other", http.StatusForbidden},
		// A link to the file as it is has no start.
		{strings.TrimSuffix(direct, ".mkv") + "/start?t=5", http.StatusForbidden},
	} {
		resp := get(t, c.url)
		body(t, resp)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %s, want %d", strings.TrimPrefix(c.url, h.front.URL), resp.Status, c.want)
		}
	}
	// A converted link needs the session that Prepare opened.
	h.hub.mu.Lock()
	for _, s := range h.hub.sessions {
		h.hub.end(s, "test")
	}
	h.hub.mu.Unlock()
	resp := get(t, h.front.URL+pb.Path)
	if body(t, resp); resp.StatusCode != http.StatusForbidden {
		t.Errorf("after the session ended: status %s", resp.Status)
	}

	// At most maxConverts streams are made at once.
	pb, err = h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	h.hub.convertsRunning.Store(maxConverts)
	resp = get(t, h.front.URL+pb.Path)
	if body(t, resp); resp.StatusCode != http.StatusServiceUnavailable ||
		!strings.Contains(h.logs.String(), "too many titles are being converted") {
		t.Errorf("over the cap: status %s", resp.Status)
	}
	h.hub.convertsRunning.Store(0)
}

// TestStartInABigFile finds where a stream starts near the end of a file
// too big to sit in the connection's buffers. ffprobe reads the start of the
// file, then asks for its end, where Matroska keeps its index, before it
// closes its first request: that request must end, though ffprobe no
// longer reads it, or the second waits for it until the lookup gives up.
func TestStartInABigFile(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	h := converting(t, 1, "dca")
	h.files.mu.Lock()
	h.files.content = makeFilm(t, ffmpeg, 40, "1280x720", "8M", "dca")
	h.files.fast = true
	h.files.mu.Unlock()
	if len(h.files.content) < 20<<20 {
		t.Fatalf("a film of only %d bytes", len(h.files.content))
	}
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	started := time.Now()
	if at := h.startAt(pb, "35"); at < 33.9 || at > 34.1 {
		t.Errorf("start from 35 s at %v, want the picture at 34 s", at)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("finding where the stream starts took %v", took)
	}
	if logs := h.logs.String(); strings.Contains(logs, "cannot find where") {
		t.Errorf("logs: %s", logs)
	}
}

// TestConvertFromFolder converts a film in a folder, read through the
// folder, and reads it again once the file has changed.
func TestConvertFromFolder(t *testing.T) {
	ffmpeg, ffprobe := realFFmpeg(t), realFFprobe(t)
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "Quiet Harbour (2019).mkv")
	if err := os.WriteFile(path, film(t, ffmpeg, "dca"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	srcID, err := st.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeFolder, URL: dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyLibrary(ctx, srcID, store.KindMovie, nil, []store.LibraryTitle{
		{Key: "Quiet Harbour (2019).mkv", Name: "Quiet Harbour", Container: "mkv", AddedAt: time.Now()}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := st.RecentTitles(ctx, store.KindMovie, 1, store.EverySource)
	if err != nil || len(rows) != 1 {
		t.Fatalf("titles %+v, %v", rows, err)
	}
	item, err := st.PlayableMovie(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	hb := New(st, slog.New(slog.NewTextHandler(logs, nil)), "test", map[int64]Source{srcID: {Name: "films"}})
	hb.UseFFmpeg(ffmpeg, RepairSet)
	hb.UseFFprobe(ffprobe)
	front := httptest.NewServer(hb)
	t.Cleanup(front.Close)

	pb, err := hb.Prepare(ctx, 7, item, media.MAG322, nil)
	if err != nil || !pb.Converted {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	resp := get(t, front.URL+pb.Path+"?t=4.021")
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if got := probeFile(t, data); !strings.HasPrefix(got.Summary(), "mpegts: h264 320x180 8-bit; sound ac3 6ch") {
		t.Errorf("made %s", got.Summary())
	}
	if _, err := hb.Prepare(ctx, 7, item, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "read what a title's file holds"); n != 1 {
		t.Errorf("read the file %d times", n)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	if _, err := hb.Prepare(ctx, 7, item, media.MAG322, nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(logs.String(), "read what a title's file holds"); n != 2 {
		t.Errorf("read the changed file %d times in all, want 2", n)
	}
}
