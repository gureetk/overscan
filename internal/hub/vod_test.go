// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// fileServer is a fictional provider's files: a film and an episode, served
// slowly, with ranges, to the right login. It counts the requests it has
// open at once.
type fileServer struct {
	mu      sync.Mutex
	open    int
	most    int
	ranges  []string
	refuse  int  // answer this status instead, if set
	refuses int  // with refuse, refuse only this many requests, if set
	stall   bool // send the first part of a request for the whole file, then wait until it is cancelled
	fast    bool // send as fast as the client reads, rather than a part every 2 ms
	settle  bool // let a request that ends as another comes end first, as stall does
	content []byte
}

func newFileServer(t *testing.T) (*fileServer, *httptest.Server) {
	t.Helper()
	f := &fileServer{content: make([]byte, 256<<10)}
	for i := range f.content {
		f.content[i] = byte(i * 7)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		// The server notices a closed connection a moment late; a request
		// that comes meanwhile waits for the one before to end, as a
		// provider sees the close first. One that does not end in half a
		// second is still open.
		for i := 0; (f.stall || f.settle) && f.open > 0 && i < 100; i++ {
			f.mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			f.mu.Lock()
		}
		f.open++
		f.most = max(f.most, f.open)
		f.ranges = append(f.ranges, r.Header.Get("Range"))
		refuse, stall, fast := f.refuse, f.stall && r.Header.Get("Range") == "", f.fast
		if refuse != 0 && f.refuses > 0 {
			f.refuses--
			if f.refuses == 0 {
				f.refuse = 0
			}
		}
		content := f.content
		f.mu.Unlock()
		defer func() {
			f.mu.Lock()
			f.open--
			f.mu.Unlock()
		}()
		if r.URL.Path != "/movie/viewer/"+secret+"/5001.mkv" && r.URL.Path != "/series/viewer/"+secret+"/9001.mp4" {
			http.NotFound(w, r)
			return
		}
		if refuse != 0 {
			w.WriteHeader(refuse)
			return
		}
		start, end := 0, len(content)-1
		status := http.StatusOK
		if rg := r.Header.Get("Range"); rg != "" {
			from, to, _ := strings.Cut(strings.TrimPrefix(rg, "bytes="), "-")
			start, _ = strconv.Atoi(from)
			if to != "" {
				end, _ = strconv.Atoi(to)
			}
			if start >= len(content) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(content)))
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				return
			}
			end = min(end, len(content)-1)
			status = http.StatusPartialContent
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(content)))
		}
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.Header().Set("Set-Cookie", "provider=1")
		w.WriteHeader(status)
		if r.Method == http.MethodHead {
			return
		}
		rc := http.NewResponseController(w)
		for at := start; at <= end; at += 16 << 10 {
			if _, err := w.Write(content[at:min(at+16<<10, end+1)]); err != nil {
				return
			}
			_ = rc.Flush()
			if stall {
				<-r.Context().Done()
				return
			}
			if fast {
				continue
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

// itemHarness is a hub with a film and an episode on a fictional Xtream
// source.
type itemHarness struct {
	*harness
	files   *fileServer
	film    store.PlayableItem
	episode store.PlayableItem
}

func newItemHarness(t *testing.T, limit int) *itemHarness {
	t.Helper()
	ctx := context.Background()
	files, srv := newFileServer(t)
	h := &itemHarness{harness: &harness{t: t, logs: &syncBuffer{}}, files: files}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeXtream, URL: srv.URL,
		Username: "viewer", Password: secret, MaxConnections: limit})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if _, err := st.ApplyLibrary(ctx, srcID, store.KindMovie, nil, []store.LibraryTitle{
		{Key: "5001", Name: "The Long Field", Container: "mkv"}}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyLibrary(ctx, srcID, store.KindSeries, nil, []store.LibraryTitle{{Key: "7001", Name: "Coastline"}}, at); err != nil {
		t.Fatal(err)
	}
	recent, err := st.SearchTitles(ctx, "o", 5, store.EverySource)
	if err != nil || len(recent) != 2 {
		t.Fatalf("titles %+v, %v", recent, err)
	}
	if err := st.SetSeriesDetails(ctx, recent[1].ID, store.TitleDetails{}, []store.LibraryEpisode{
		{Key: "9001", Season: 1, Number: 1, Name: "Rocks", Container: "mp4"}}, at); err != nil {
		t.Fatal(err)
	}
	eps, err := st.Episodes(ctx, recent[1].ID)
	if err != nil || len(eps) != 1 {
		t.Fatalf("episodes %+v, %v", eps, err)
	}
	if h.film, err = st.PlayableMovie(ctx, recent[0].ID); err != nil {
		t.Fatal(err)
	}
	if h.episode, err = st.PlayableEpisode(ctx, eps[0].ID); err != nil {
		t.Fatal(err)
	}
	h.clock.Store(time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC).UnixNano())
	h.hub = New(st, slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), "test",
		map[int64]Source{srcID: {Name: "main", MaxConnections: limit}})
	h.hub.now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.front = httptest.NewServer(h.hub)
	t.Cleanup(h.front.Close)
	return h
}

func (h *itemHarness) itemLink(device int64, item store.PlayableItem) string {
	return h.front.URL + h.hub.ItemLink(device, item)
}

// ask makes a request of the hub, with a range if one is given.
func ask(t *testing.T, method, url, rangeHeader string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rangeHeader != "" {
		req.Header.Set("Range", rangeHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestFilmPlays(t *testing.T) {
	h := newItemHarness(t, 1)
	link := h.itemLink(7, h.film)
	if !strings.HasSuffix(link, ".mkv") || !strings.Contains(link, "/vod/") || strings.Contains(link, secret) {
		t.Errorf("link %q", link)
	}
	resp := ask(t, http.MethodGet, link, "")
	got := body(t, resp)
	if resp.StatusCode != http.StatusOK || !bytes.Equal([]byte(got), h.files.content) ||
		resp.Header.Get("Content-Length") != strconv.Itoa(len(h.files.content)) ||
		resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Content-Type") != "video/x-matroska" ||
		resp.Header.Get("Set-Cookie") != "" {
		t.Errorf("status %s, %d bytes, header %v", resp.Status, len(got), resp.Header)
	}
	h.waitIdle()

	// The player seeks: the same link, with a range, after the link expired.
	h.advance(linkTTL + time.Minute)
	resp = ask(t, http.MethodGet, link, "bytes=1000-1999")
	got = body(t, resp)
	if resp.StatusCode != http.StatusPartialContent || got != string(h.files.content[1000:2000]) ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 1000-1999/%d", len(h.files.content)) {
		t.Errorf("range: status %s, %d bytes, header %v", resp.Status, len(got), resp.Header)
	}
	h.waitIdle()

	resp = ask(t, http.MethodGet, link, fmt.Sprintf("bytes=%d-", len(h.files.content)))
	if body(t, resp); resp.StatusCode != http.StatusRequestedRangeNotSatisfiable || resp.Header.Get("Content-Range") == "" {
		t.Errorf("past the end: status %s, header %v", resp.Status, resp.Header)
	}
	resp = ask(t, http.MethodHead, link, "")
	if body(t, resp); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Length") != strconv.Itoa(len(h.files.content)) {
		t.Errorf("HEAD: status %s, header %v", resp.Status, resp.Header)
	}
	// A range that is not plain bytes asks for the whole file.
	resp = ask(t, http.MethodGet, link, "pages=1-2")
	if body(t, resp); resp.StatusCode != http.StatusOK {
		t.Errorf("odd range: status %s", resp.Status)
	}
	h.waitIdle()
	h.files.mu.Lock()
	ranges := h.files.ranges
	h.files.mu.Unlock()
	want := []string{"", "bytes=1000-1999", fmt.Sprintf("bytes=%d-", len(h.files.content)), "", ""}
	if strings.Join(ranges, "|") != strings.Join(want, "|") {
		t.Errorf("the source was asked for %q, want %q", ranges, want)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `msg="stream started" title="The Long Field" source=main device=7`) || strings.Contains(logs, secret) {
		t.Errorf("logs:\n%s", logs)
	}
}

func TestSeekEndsTheRequestBefore(t *testing.T) {
	h := newItemHarness(t, 1)
	h.files.mu.Lock()
	h.files.stall = true
	h.files.mu.Unlock()
	link := h.itemLink(7, h.film)
	first := ask(t, http.MethodGet, link, "")
	if _, err := io.ReadFull(first.Body, make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	second := ask(t, http.MethodGet, link, "bytes=200000-")
	got := body(t, second)
	if second.StatusCode != http.StatusPartialContent || got != string(h.files.content[200000:]) {
		t.Errorf("seek: status %s, %d bytes", second.Status, len(got))
	}
	// The seek ends the first request: it gives at most what the source
	// sent before it stalled, and less if the hub had yet to write some of
	// it when its turn ended, which cuts its answer short (D68).
	rest, _ := io.ReadAll(first.Body)
	first.Body.Close()
	if got := 4096 + len(rest); got > 16<<10 {
		t.Errorf("the first request gave %d bytes", got)
	}
	h.waitIdle()
	h.files.mu.Lock()
	defer h.files.mu.Unlock()
	if h.files.most != 1 {
		t.Errorf("%d requests at once to the source", h.files.most)
	}
}

func TestPausedFilmGoesOn(t *testing.T) {
	h := newItemHarness(t, 1)
	link := h.itemLink(7, h.film)
	body(t, ask(t, http.MethodGet, link, "bytes=0-99"))
	h.waitIdle()
	// Paused for two hours, without a connection: the session holds none of
	// the source's, so another device can play...
	h.advance(2 * time.Hour)
	other := ask(t, http.MethodGet, h.itemLink(8, h.episode), "bytes=0-99")
	if body(t, other); other.StatusCode != http.StatusPartialContent {
		t.Errorf("another device: %s", other.Status)
	}
	h.waitIdle()
	// ...and the film goes on where it was.
	resp := ask(t, http.MethodGet, link, "bytes=100-199")
	if got := body(t, resp); resp.StatusCode != http.StatusPartialContent || got != string(h.files.content[100:200]) {
		t.Errorf("after the pause: %s", resp.Status)
	}
	h.waitIdle()
	h.advance(fileGrace + time.Minute)
	resp = ask(t, http.MethodGet, link, "bytes=200-299")
	if body(t, resp); resp.StatusCode != http.StatusForbidden {
		t.Errorf("after the grace: %s", resp.Status)
	}
}

func TestFilmsKeepTheLimit(t *testing.T) {
	h := newItemHarness(t, 1)
	// The film's source sends a first part and then waits, so that the film
	// still holds the source's connection when the episodes ask, however
	// slowly the test runs; the episodes' requests get their whole file.
	h.files.mu.Lock()
	h.files.stall = true
	h.files.mu.Unlock()
	film := ask(t, http.MethodGet, h.itemLink(1, h.film), "")
	if _, err := io.ReadFull(film.Body, make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	defer film.Body.Close()
	h.files.mu.Lock()
	h.files.stall = false
	h.files.mu.Unlock()
	if h.hub.Available(h.film.SourceID, 2) {
		t.Error("another device may play while a film plays")
	}
	resp := ask(t, http.MethodGet, h.itemLink(2, h.episode), "")
	if body(t, resp); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("another device: %s", resp.Status)
	}
	// The same device's episode takes the film's place.
	resp = ask(t, http.MethodGet, h.itemLink(1, h.episode), "")
	if body(t, resp); resp.StatusCode != http.StatusOK {
		t.Errorf("the same device: %s", resp.Status)
	}
	if !strings.Contains(h.logs.String(), `msg="stream ended" title="The Long Field" source=main device=1 reason="replaced by a new stream from the same device"`) {
		t.Errorf("logs:\n%s", h.logs)
	}
}

func TestItemLinksAndKinds(t *testing.T) {
	h := newItemHarness(t, 0)
	episode := h.itemLink(3, h.episode)
	if !strings.HasSuffix(episode, ".mp4") {
		t.Errorf("link %q", episode)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(episode, h.front.URL+"/vod/"), ".mp4")
	for name, u := range map[string]string{
		"an item's token as a channel's": h.front.URL + "/live/" + token + ".ts",
		"a resource of an item":          h.front.URL + "/vod/" + token + ".mp4/r/1",
		"no token":                       h.front.URL + "/vod/",
		"a channel's token as an item's": h.front.URL + "/vod/" + h.hub.sign(claims{entryID: 1, deviceID: 3, expires: 1 << 40, nonce: 1}) + ".ts",
	} {
		resp := get(t, u)
		if body(t, resp); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %s", name, resp.Status)
		}
	}
	resp := ask(t, http.MethodGet, episode, "bytes=0-9")
	if body(t, resp); resp.StatusCode != http.StatusPartialContent {
		t.Errorf("episode: %s", resp.Status)
	}
	got := h.hub.Streams()
	if len(got) != 1 || got[0].Kind != PlaysEpisode || got[0].ItemID != h.episode.ID || got[0].EntryID != 0 ||
		got[0].Channel != "Coastline S1 E1" {
		t.Errorf("streams %+v", got)
	}
	c := claims{kind: kindMovie, entryID: 12, deviceID: 34, expires: 56, nonce: 78}
	if back, ok := h.hub.verify(h.hub.sign(c)); !ok || back != c {
		t.Errorf("verify(sign(%+v)) = %+v, %v", c, back, ok)
	}
}

func TestRefusedFilm(t *testing.T) {
	h := newItemHarness(t, 0)
	h.files.mu.Lock()
	h.files.refuse = 461
	h.files.mu.Unlock()
	resp := ask(t, http.MethodGet, h.itemLink(7, h.film), "")
	if body(t, resp); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %s", resp.Status)
	}
	if !strings.Contains(h.logs.String(), `msg="a title's source refused it" title="The Long Field" source=main status="461`) {
		t.Errorf("logs:\n%s", h.logs)
	}
}

// TestRefusedFilmAskedAgain plays a film whose source refuses it at first,
// as a source does that still counts the connection that just closed: the
// hub asks again in a moment, and the film plays.
func TestRefusedFilmAskedAgain(t *testing.T) {
	waits := refusedWaits
	refusedWaits = []time.Duration{10 * time.Millisecond, 20 * time.Millisecond}
	t.Cleanup(func() { refusedWaits = waits })
	h := newItemHarness(t, 1)
	h.files.mu.Lock()
	h.files.refuse, h.files.refuses = http.StatusForbidden, 2
	h.files.mu.Unlock()
	resp := ask(t, http.MethodGet, h.itemLink(7, h.film), "bytes=0-1023")
	if b := body(t, resp); resp.StatusCode != http.StatusPartialContent || len(b) != 1024 {
		t.Errorf("status %s, %d bytes", resp.Status, len(b))
	}
	h.files.mu.Lock()
	asked := len(h.files.ranges)
	h.files.mu.Unlock()
	if asked != 3 {
		t.Errorf("the source was asked %d times", asked)
	}
	if n := strings.Count(h.logs.String(), "a title's source refused it, as if it had no free connection"); n != 1 {
		t.Errorf("%d lines about asking again; logs:\n%s", n, h.logs)
	}

	// A source that goes on refusing is refused in the end.
	h.files.mu.Lock()
	h.files.refuse, h.files.refuses = http.StatusServiceUnavailable, 0
	h.files.mu.Unlock()
	resp = ask(t, http.MethodGet, h.itemLink(7, h.film), "")
	if body(t, resp); resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %s", resp.Status)
	}
	if !strings.Contains(h.logs.String(), `msg="a title's source refused it" title="The Long Field" source=main status="503`) {
		t.Errorf("logs:\n%s", h.logs)
	}
}

// TestSilentSource ends a request whose source stops sending, so that the
// player is not left waiting on it, and asks again from where it got to.
func TestSilentSource(t *testing.T) {
	idle := fileIdleTimeout
	fileIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { fileIdleTimeout = idle })
	h := newItemHarness(t, 1)
	h.files.mu.Lock()
	h.files.stall = true
	h.files.mu.Unlock()
	start := time.Now()
	resp := ask(t, http.MethodGet, h.itemLink(7, h.film), "")
	b, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	// The answer ends short of its length, as the player sees.
	if resp.StatusCode != http.StatusOK || len(b) != 16<<10 || !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("status %s, %d bytes, %v", resp.Status, len(b), err)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("the request took %s", took)
	}
	if !strings.Contains(h.logs.String(), `msg="a title's source sent nothing for a while, so the hub ended the request; the player can ask again from where it got to" title="The Long Field" source=main`) {
		t.Errorf("logs:\n%s", h.logs)
	}
}

// TestFolderVideoPlays plays a film from a folder on the server (D67):
// the hub reads the file itself, ranges and all, and never outside the
// folder.
func TestFolderVideoPlays(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "Films")
	content := make([]byte, 300<<10)
	for i := range content {
		content[i] = byte(i * 13)
	}
	if err := os.MkdirAll(filepath.Join(dir, "Calm Water (2021)"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Calm Water (2021)", "Calm Water (2021).mkv"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	h := &itemHarness{harness: &harness{t: t, logs: &syncBuffer{}}}
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
		{Key: "Calm Water (2021)/Calm Water (2021).mkv", Name: "Calm Water", Container: "mkv", AddedAt: time.Now()},
		{Key: "../outside.mkv", Name: "Outside", Container: "mkv", AddedAt: time.Now()},
		{Key: "Gone.mkv", Name: "Gone", Container: "mkv", AddedAt: time.Now()}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	titles, err := st.RecentTitles(ctx, store.KindMovie, 5, store.EverySource)
	if err != nil || len(titles) != 3 {
		t.Fatalf("titles %+v, %v", titles, err)
	}
	items := map[string]store.PlayableItem{}
	for _, row := range titles {
		if items[row.Name], err = st.PlayableMovie(ctx, row.ID); err != nil {
			t.Fatal(err)
		}
	}
	h.clock.Store(time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC).UnixNano())
	h.hub = New(st, slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), "test",
		map[int64]Source{srcID: {Name: "films"}})
	h.hub.now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.front = httptest.NewServer(h.hub)
	t.Cleanup(h.front.Close)

	link := h.itemLink(7, items["Calm Water"])
	if !strings.HasSuffix(link, ".mkv") || strings.Contains(link, "Calm") {
		t.Errorf("link %q", link)
	}
	resp := ask(t, http.MethodGet, link, "")
	if got := body(t, resp); resp.StatusCode != http.StatusOK || got != string(content) ||
		resp.Header.Get("Accept-Ranges") != "bytes" || resp.Header.Get("Last-Modified") == "" {
		t.Errorf("status %s, %d bytes, header %v", resp.Status, len(got), resp.Header)
	}
	h.waitIdle()
	resp = ask(t, http.MethodGet, link, "bytes=1000-1999")
	if got := body(t, resp); resp.StatusCode != http.StatusPartialContent || got != string(content[1000:2000]) ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes 1000-1999/%d", len(content)) {
		t.Errorf("range: status %s, %d bytes, header %v", resp.Status, len(got), resp.Header)
	}
	h.waitIdle()
	resp = ask(t, http.MethodHead, link, "")
	if body(t, resp); resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Length") != strconv.Itoa(len(content)) {
		t.Errorf("HEAD: status %s, header %v", resp.Status, resp.Header)
	}
	h.waitIdle()
	for name, want := range map[string]int{"Outside": http.StatusNotFound, "Gone": http.StatusNotFound} {
		resp := ask(t, http.MethodGet, h.itemLink(7, items[name]), "")
		if body(t, resp); resp.StatusCode != want {
			t.Errorf("%s: status %s", name, resp.Status)
		}
	}
	if logs := h.logs.String(); strings.Contains(logs, dir) || !strings.Contains(logs, "a title's file is gone from its folder") {
		t.Errorf("logs:\n%s", logs)
	}
}
