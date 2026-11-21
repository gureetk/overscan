// SPDX-License-Identifier: AGPL-3.0-or-later

package upstream

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/catalog"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// setup stores a source that reads p as the box its provider registered,
// with the given limit of streams, and reads its channels.
func setup(t *testing.T, p *stalkertest.Portal, limit int) (*store.Store, *stalker.Pool, int64, *syncBuffer) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	settings := source.Settings{Name: "main", Type: source.TypeStalker, URL: p.URL + "/stalker_portal/c/",
		Device: source.Device{MAC: stalkertest.MAC}, Refresh: time.Hour, MaxConnections: limit,
		Numbers: source.NumbersPlaylist}
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, nil))
	pool := stalker.NewPool(nil)
	r := catalog.NewRefresher(st, log, "test")
	r.UseStalker(pool)
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	return st, pool, id, logs
}

func TestResolve(t *testing.T) {
	p := stalkertest.New(t)
	st, pool, id, logs := setup(t, p, 1)
	links := New(st, pool, slog.New(slog.NewTextHandler(logs, nil)))
	ctx := context.Background()

	res, err := links.Resolve(ctx, id, hub.PlaysChannel, "ffrt http://localhost/ch/101_")
	if err != nil || !strings.HasPrefix(res.Address, p.URL+"/play/live.ts?token=") || res.Hold == nil {
		t.Fatalf("resolved %+v, %v", res, err)
	}
	c, err := pool.Client(id, stalker.FromSettings(source.Settings{Type: source.TypeStalker, URL: p.URL + "/stalker_portal/c/",
		Device: source.Device{MAC: stalkertest.MAC}}))
	if err != nil {
		t.Fatal(err)
	}
	release := res.Hold()
	if !c.Held() {
		t.Error("the stream does not hold the source's session")
	}
	release()
	if c.Held() {
		t.Error("the session is still held")
	}
	if n := p.Calls("stb/handshake"); n != 1 {
		t.Errorf("%d handshakes, want 1: the link was not asked for in the refresh's session", n)
	}

	if _, err := links.Resolve(ctx, id, hub.PlaysMovie, "/media/1001.mpg"); err == nil {
		t.Error("a film resolved")
	}
	other, err := st.AddSource(ctx, source.Settings{Name: "one", Type: source.TypeM3U, URL: "http://provider.example/one.m3u",
		Refresh: time.Hour, Numbers: source.NumbersPlaylist})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := links.Resolve(ctx, other, hub.PlaysChannel, "ffrt http://localhost/ch/101_"); err == nil {
		t.Error("a playlist's channel resolved")
	}
}

func TestResolveLimit(t *testing.T) {
	p := stalkertest.New(t)
	p.LinkError = "limit"
	st, pool, id, logs := setup(t, p, 1)
	links := New(st, pool, slog.New(slog.NewTextHandler(logs, nil)))
	_, err := links.Resolve(context.Background(), id, hub.PlaysChannel, "ffrt http://localhost/ch/101_")
	var lerr *stalker.LinkError
	if !errors.Is(err, hub.ErrLimit) || !errors.As(err, &lerr) {
		t.Errorf("error %v", err)
	}
}

// A box plays the portal's channels through the hub: each stream asks the
// portal for a link as it starts, and again when its player reconnects,
// and a box that changes channel stops its stream before it asks for the
// next.
func TestPlayThroughTheHub(t *testing.T) {
	p := stalkertest.New(t)
	p.OneUseLinks = true
	st, pool, id, logs := setup(t, p, 1)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(logs, nil))
	h := hub.New(st, log, "test", map[int64]hub.Source{id: {Name: "main", MaxConnections: 1, Resolves: true}})
	h.UseResolver(New(st, pool, log))
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]string{}
	for _, c := range lineup {
		e, err := st.PlayableEntry(ctx, c.EntryID)
		if err != nil {
			t.Fatal(err)
		}
		links[c.Name] = front.URL + h.Link(7, e)
	}

	play := func(link string) *http.Response {
		t.Helper()
		resp, err := http.Get(link)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("status %s: %s", resp.Status, b)
		}
		buf := make([]byte, 188*4)
		if _, err := io.ReadFull(resp.Body, buf); err != nil || buf[0] != 0x47 {
			t.Fatalf("read %v, first byte %#x", err, buf[0])
		}
		return resp
	}
	waitStreams := func(want int) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
			if now, _ := p.Streams(); now == want {
				return
			}
			if time.Now().After(deadline) {
				now, _ := p.Streams()
				t.Fatalf("%d streams from the portal, want %d", now, want)
			}
		}
	}

	news := play(links["Example News"])
	news.Body.Close()
	waitStreams(0)
	// The player reconnects; the link it had played is gone.
	play(links["Example News"]).Body.Close()
	waitStreams(0)
	if n := p.Calls("itv/create_link"); n != 2 {
		t.Errorf("%d links for a stream and its reconnect, want 2", n)
	}

	sports := play(links["Example Sports"])
	weather := play(links["Example Weather"])
	defer weather.Body.Close()
	if _, err := io.Copy(io.Discard, sports.Body); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Logf("the replaced stream ended with %v", err)
	}
	sports.Body.Close()
	if _, most := p.Streams(); most != 1 {
		t.Errorf("the portal played %d streams at once, want 1", most)
	}
	if l := logs.String(); strings.Contains(l, stalkertest.MAC) || strings.Contains(l, "token=") {
		t.Errorf("the log shows the box or a link:\n%s", l)
	}
}

// A portal's films and episodes play through the hub: the hub asks the
// portal for a link as the title starts, and again when a link lapses
// (D77).
func TestFilmsThroughTheHub(t *testing.T) {
	p := stalkertest.New(t)
	p.OneUseFileLinks = true
	st, pool, id, logs := setup(t, p, 1)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(logs, nil))
	lib := library.New(st, log, "test")
	lib.UseStalker(pool)
	libCtx, stop := context.WithCancel(ctx)
	libDone := make(chan struct{})
	go func() {
		lib.Run(libCtx)
		close(libDone)
	}()
	t.Cleanup(func() {
		stop()
		<-libDone
	})
	h := hub.New(st, log, "test", map[int64]hub.Source{id: {Name: "main", MaxConnections: 1, Resolves: true}})
	h.UseResolver(New(st, pool, log))
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)

	part := func(kind, shelf string) []store.TitleListing {
		t.Helper()
		if _, err := lib.Want(ctx, kind, 5*time.Second); err != nil {
			t.Fatal(err)
		}
		shelves, err := st.Shelves(ctx, kind, store.EverySource)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range shelves {
			if s.Name != shelf {
				continue
			}
			cat, err := st.PortalCategory(ctx, kind, s.ID)
			if err != nil {
				t.Fatal(err)
			}
			got, err := lib.PortalPart(ctx, cat, 0, 14)
			if err != nil {
				t.Fatal(err)
			}
			return got.Titles
		}
		t.Fatalf("no shelf %s", shelf)
		return nil
	}
	ranged := func(link string, from, to int) []byte {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, link, nil)
		req.Header.Set("Range", "bytes="+strconv.Itoa(from)+"-"+strconv.Itoa(to))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status %s: %s", resp.Status, b)
		}
		return b
	}

	drama := part(store.KindMovie, "Drama")
	item, err := st.PlayableMovie(ctx, drama[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	link := front.URL + h.ItemLink(7, item)
	if got := ranged(link, 100, 199); !bytes.Equal(got, stalkertest.Content("1002")[100:200]) {
		t.Errorf("film 1002: %q", got)
	}
	// The player seeks, and the first link has lapsed.
	if got := ranged(link, 5000, 5099); !bytes.Equal(got, stalkertest.Content("1002")[5000:5100]) {
		t.Errorf("film 1002 further on: %q", got)
	}
	if n := p.Calls("vod/create_link"); n != 2 {
		t.Errorf("%d links, want 2", n)
	}

	// A film whose file is asked for apart.
	comedy := part(store.KindMovie, "Comedy")
	item, _ = st.PlayableMovie(ctx, comedy[1].ID)
	if got := ranged(front.URL+h.ItemLink(7, item), 0, 99); !bytes.Equal(got, stalkertest.Content("1003")[:100]) {
		t.Errorf("film 1003: %q", got)
	}

	// An episode of a season of the series module.
	series := part(store.KindSeries, "Documentary")
	o, err := lib.Open(ctx, series[0].ID)
	if err != nil || len(o.Episodes) != 5 {
		t.Fatalf("opened %+v, %v", o, err)
	}
	ep, err := st.PlayableEpisode(ctx, o.Episodes[3].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := ranged(front.URL+h.ItemLink(7, ep), 0, 99); !bytes.Equal(got, stalkertest.Content("2001_2e1")[:100]) {
		t.Errorf("S2 E1: %q", got)
	}
	if l := logs.String(); strings.Contains(l, stalkertest.MAC) || strings.Contains(l, "token=") {
		t.Errorf("the log shows the box or a link:\n%s", l)
	}
}

// A portal's past programme plays through the hub: the hub asks the portal
// for its link as it starts, after the channel's guide of the day finds it
// in the archive, and again when the link lapses (D84).
func TestArchiveThroughTheHub(t *testing.T) {
	p := stalkertest.New(t)
	now := time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }
	p.OneUseFileLinks = true
	st, pool, id, logs := setup(t, p, 1)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(logs, nil))
	h := hub.New(st, log, "test", map[int64]hub.Source{id: {Name: "main", MaxConnections: 1, Resolves: true}})
	h.UseResolver(New(st, pool, log))
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	var sports store.ArchiveEntry
	for _, c := range lineup {
		if c.Name == "Example Sports" {
			if sports, err = st.PlayableArchive(ctx, c.EntryID); err != nil {
				t.Fatal(err)
			}
		}
	}
	if sports.Portal != "102" || sports.Days != 1 {
		t.Fatalf("Example Sports: %+v", sports)
	}
	aired := time.Date(2026, 12, 4, 5, 0, 0, 0, time.UTC)
	path, _ := h.ArchiveLink(7, sports.PlayableEntry, aired, aired.Add(2*time.Hour))
	want := stalkertest.Content(stalkertest.ProgrammeID("102", aired))
	ranged := func(from, to int) []byte {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, front.URL+path, nil)
		req.Header.Set("Range", "bytes="+strconv.Itoa(from)+"-"+strconv.Itoa(to))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusPartialContent {
			t.Fatalf("status %s: %s", resp.Status, b)
		}
		return b
	}
	if got := ranged(0, 99); !bytes.Equal(got, want[:100]) {
		t.Errorf("from the start: %q", got)
	}
	// The player seeks, and the first link has lapsed.
	if got := ranged(9000, 9099); !bytes.Equal(got, want[9000:9100]) {
		t.Errorf("further on: %q", got)
	}
	if n := p.Calls("tv_archive/create_link"); n != 2 {
		t.Errorf("%d links, want 2", n)
	}
	streams := h.Streams()
	if len(streams) != 1 || streams[0].Kind != hub.PlaysArchive || !streams[0].Aired.Equal(aired) {
		t.Errorf("streams %+v", streams)
	}

	// A programme that the archive does not hold gives no link.
	path, _ = h.ArchiveLink(7, sports.PlayableEntry, now.Add(-90*time.Minute), now.Add(30*time.Minute))
	resp, err := http.Get(front.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("a programme on now: %s", resp.Status)
	}
	if l := logs.String(); strings.Contains(l, stalkertest.MAC) || strings.Contains(l, "token=") ||
		!strings.Contains(l, "a past programme plays from its source's archive") {
		t.Errorf("the log:\n%s", l)
	}
}
