// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/subtitles"
)

// folderHub returns a hub without ffmpeg whose one source is a folder that
// holds files, and its film, the first file named.
func folderHub(t *testing.T, film string, files map[string]string) (*Hub, store.PlayableItem, *httptest.Server, *syncBuffer) {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
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
		{Key: film, Name: "Quiet Harbour", Container: "mkv", AddedAt: time.Now()}}, time.Now()); err != nil {
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
	front := httptest.NewServer(hb)
	t.Cleanup(front.Close)
	return hb, item, front, logs
}

const srtFile = "1\n00:00:01,000 --> 00:00:03,000\nHello.\n\n2\n00:01:40,000 --> 00:01:42,500\nLater on.\n"

func TestSubtitlesFromAFolder(t *testing.T) {
	hb, item, front, logs := folderHub(t, "Quiet Harbour (2019).mkv", map[string]string{
		"Quiet Harbour (2019).mkv":    "video",
		"Quiet Harbour (2019).en.srt": srtFile,
		"Quiet Harbour (2019).fr.ass": "[Events]\nFormat: Start, End, Text\nDialogue: 0:00:02.00,0:00:04.00,{\\i1}Bonjour{\\i0}\\Nà tous\n",
	})
	subs := []subtitles.Track{
		{Language: "eng", Format: "srt", File: "Quiet Harbour (2019).en.srt"},
		{Language: "fre", Format: "ass", File: "Quiet Harbour (2019).fr.ass"},
		{Language: "ger", Format: "srt", File: "../elsewhere.srt"},
	}
	pb, err := hb.Prepare(context.Background(), 7, item, media.MAG322, subs)
	if err != nil {
		t.Fatal(err)
	}
	if pb.Converted || len(pb.Subtitles) != 3 || !strings.HasSuffix(pb.Subtitles[1].Path, "/sub/1.srt") ||
		!strings.HasPrefix(pb.Subtitles[1].Path, strings.TrimSuffix(pb.Path, ".mkv")+"/") || pb.Subtitles[1].Track.Language != "fre" {
		t.Fatalf("Prepare = %+v", pb)
	}
	fetch := func(path string, want int) string {
		t.Helper()
		resp := get(t, front.URL+path)
		b := body(t, resp)
		if resp.StatusCode != want {
			t.Errorf("%s: status %s, want %d", path, resp.Status, want)
		}
		return b
	}
	if got := fetch(pb.Subtitles[0].Path, http.StatusOK); got != "1\r\n00:00:01,000 --> 00:00:03,000\r\nHello.\r\n\r\n"+
		"2\r\n00:01:40,000 --> 00:01:42,500\r\nLater on.\r\n\r\n" {
		t.Errorf("SRT: %q", got)
	}
	// From 100 s into the film, as a stream through ffmpeg starts there.
	if got := fetch(pb.Subtitles[0].Path+"?t=100", http.StatusOK); got != "1\r\n00:00:00,000 --> 00:00:02,500\r\nLater on.\r\n\r\n" {
		t.Errorf("SRT from 100 s: %q", got)
	}
	if got := fetch(pb.Subtitles[1].Path, http.StatusOK); got != "1\r\n00:00:02,000 --> 00:00:04,000\r\nBonjour\r\nà tous\r\n\r\n" {
		t.Errorf("SubStation as SRT: %q", got)
	}
	fetch(pb.Subtitles[2].Path, http.StatusBadGateway) // outside the folder
	if !strings.Contains(logs.String(), `msg="cannot read a title's subtitles" title="Quiet Harbour" source=films track=2`) {
		t.Errorf("logs: %s", logs)
	}
	base := strings.TrimSuffix(pb.Subtitles[0].Path, "0.srt")
	fetch(base+"3.srt", http.StatusNotFound)
	fetch(base+"01.srt", http.StatusNotFound)
	fetch(base+"0.vtt", http.StatusNotFound)
	fetch(pb.Subtitles[0].Path+"?t=soon", http.StatusBadRequest)

	// Without subtitles, Prepare opens no session.
	pb, err = hb.Prepare(context.Background(), 7, item, media.MAG322, nil)
	if err != nil || len(pb.Subtitles) != 0 {
		t.Fatalf("Prepare without subtitles = %+v, %v", pb, err)
	}
	if n := len(hb.Streams()); n != 1 {
		t.Errorf("%d sessions, want the one with subtitles", n)
	}
	// Once the session ends, its subtitles go with it.
	hb.mu.Lock()
	for _, s := range hb.sessions {
		hb.end(s, "test")
	}
	hb.mu.Unlock()
	fetch(strings.TrimSuffix(base, "/sub/")+"/sub/0.srt", http.StatusForbidden)
}

func TestSubtitlesFromAServer(t *testing.T) {
	var asked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Add(1)
		switch r.URL.Path {
		case "/Videos/a1/ms1/Subtitles/2/Stream.vtt":
			if r.URL.Query().Get("ApiKey") != secret {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, "WEBVTT\n\n00:00:05.000 --> 00:00:06.000\n<v Ana>From the server.</v>\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	h := newItemHarness(t, 1)
	subs := []subtitles.Track{
		{Language: "eng", Format: "vtt", URL: server.URL + "/Videos/a1/ms1/Subtitles/2/Stream.vtt?ApiKey=" + secret},
		{Language: "fre", Format: "srt", URL: server.URL + "/Videos/a1/ms1/Subtitles/3/Stream.srt?ApiKey=" + secret},
	}
	pb, err := h.hub.Prepare(context.Background(), 7, h.film, media.MAG322, subs)
	if err != nil || len(pb.Subtitles) != 2 {
		t.Fatalf("Prepare = %+v, %v", pb, err)
	}
	for range 2 {
		resp := get(t, h.front.URL+pb.Subtitles[0].Path)
		if got := body(t, resp); resp.StatusCode != http.StatusOK || got != "1\r\n00:00:05,000 --> 00:00:06,000\r\nFrom the server.\r\n\r\n" ||
			resp.Header.Get("Content-Type") != "application/x-subrip; charset=utf-8" {
			t.Errorf("status %s, %q", resp.Status, got)
		}
	}
	if n := asked.Load(); n != 1 {
		t.Errorf("asked the server %d times, want once", n)
	}
	resp := get(t, h.front.URL+pb.Subtitles[1].Path)
	if body(t, resp); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("a track the server does not have: status %s", resp.Status)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `track=1 error="the source answered 404 Not Found"`) || strings.Contains(logs, secret) {
		t.Errorf("logs: %s", logs)
	}
}
