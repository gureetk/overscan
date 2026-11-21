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

// fakeResolver gives the upstream fixture's endless streams for the
// references "ffrt http://localhost/ch/<n>", as a portal's links.
type fakeResolver struct {
	h    *harness
	base string // the upstream fixture

	mu        sync.Mutex
	calls     []string // kind and reference of each call
	holds     int
	releases  int
	err       error
	streaming []int // how many streams of the source played at each call
}

func (f *fakeResolver) Resolve(_ context.Context, sourceID int64, kind, ref string) (Resolved, error) {
	playing := 0
	for _, s := range f.h.hub.Streams() {
		if s.Playing {
			playing++
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind+" "+ref)
	f.streaming = append(f.streaming, playing)
	if f.err != nil {
		return Resolved{}, f.err
	}
	n := ref[strings.LastIndexByte(ref, '/')+1:]
	return Resolved{Address: f.base + "/live/" + n + "?p=" + secret, Hold: func() func() {
		f.mu.Lock()
		f.holds++
		f.mu.Unlock()
		return func() {
			f.mu.Lock()
			f.releases++
			f.mu.Unlock()
		}
	}}, nil
}

func (f *fakeResolver) counts() (calls, holds, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls), f.holds, f.releases
}

// newResolving returns a harness whose channels 1 and 2 come from a source
// that resolves, with the given limit, and the resolver it uses.
func newResolving(t *testing.T, limit int) (*harness, *fakeResolver) {
	t.Helper()
	base := upstream(t).URL
	h := newHarnessWith(t, limit, "ffrt http://localhost/ch", []string{"/1", "/2"})
	src := h.entries["1"].SourceID
	h.hub.SetSources(map[int64]Source{src: {Name: "main", MaxConnections: limit, Resolves: true}})
	f := &fakeResolver{h: h, base: base}
	h.hub.UseResolver(f)
	return h, f
}

// sweepAll ends the sessions whose grace has passed.
func (h *harness) sweepAll() {
	h.hub.mu.Lock()
	h.hub.sweep(h.hub.now())
	h.hub.mu.Unlock()
}

func TestResolvedStreams(t *testing.T) {
	h, f := newResolving(t, 1)
	link := h.link(7, "1")
	if !strings.HasSuffix(link, ".ts") {
		t.Errorf("link %q", link)
	}
	resp := h.play(link)
	if calls, holds, _ := f.counts(); calls != 1 || holds != 1 {
		t.Errorf("%d resolves and %d holds, want 1 and 1", calls, holds)
	}
	if f.calls[0] != "live ffrt http://localhost/ch/1" {
		t.Errorf("resolved %q", f.calls[0])
	}
	resp.Body.Close()
	h.waitIdle()

	// A player that reconnects gets a new link, in the same session.
	h.play(link).Body.Close()
	h.waitIdle()
	if calls, holds, releases := f.counts(); calls != 2 || holds != 1 || releases != 0 {
		t.Errorf("after a reconnect: %d resolves, %d holds, %d releases; want 2, 1, 0", calls, holds, releases)
	}

	// The session's end lets go of the source.
	h.advance(reconnectGrace + 1)
	h.sweepAll()
	if _, _, releases := f.counts(); releases != 1 {
		t.Errorf("%d releases after the session ended, want 1", releases)
	}
	if logs := h.logs.String(); strings.Contains(logs, secret) || strings.Contains(logs, "localhost/ch") {
		t.Errorf("the log shows an address:\n%s", logs)
	}
}

func TestResolvedStreamsKeepToTheLimit(t *testing.T) {
	h, f := newResolving(t, 1)
	first := h.play(h.link(7, "1"))
	defer first.Body.Close()

	// Another device is refused without asking the source.
	resp := get(t, h.link(8, "2"))
	resp.Body.Close()
	if calls, _, _ := f.counts(); resp.StatusCode != http.StatusServiceUnavailable || calls != 1 {
		t.Errorf("second device: %s after %d resolves", resp.Status, calls)
	}

	// The device's own stream ends before the source is asked for the
	// next one.
	second := h.play(h.link(7, "2"))
	defer second.Body.Close()
	f.mu.Lock()
	streaming := f.streaming[len(f.streaming)-1]
	f.mu.Unlock()
	if streaming != 0 {
		t.Errorf("%d streams played when the next was asked for", streaming)
	}
	if _, holds, releases := f.counts(); holds != 2 || releases != 1 {
		t.Errorf("%d holds and %d releases, want 2 and 1", holds, releases)
	}
}

func TestResolveFailures(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		log    string
	}{
		{fmt.Errorf("%w: the portal gave no link: limit", ErrLimit), http.StatusServiceUnavailable,
			`msg="stream refused: the source is at its connection limit" channel=1 source=main limit=1 device=7 error=`},
		{errors.New("the portal gave no link: nothing_to_play"), http.StatusBadGateway,
			`msg="the source gave no address for a stream" channel=1 source=main device=7 error=`},
	} {
		h, f := newResolving(t, 1)
		f.err = tc.err
		resp := get(t, h.link(7, "1"))
		body(t, resp)
		if resp.StatusCode != tc.status {
			t.Errorf("%v: %s", tc.err, resp.Status)
		}
		if logs := h.logs.String(); !strings.Contains(logs, tc.log) {
			t.Errorf("%v: no %s in\n%s", tc.err, tc.log, logs)
		}
		if n := len(h.hub.Streams()); n != 0 {
			t.Errorf("%v: %d streams", tc.err, n)
		}
	}

	// Without a resolver, such a source's streams cannot play.
	h, _ := newResolving(t, 1)
	h.hub.UseResolver(nil)
	resp := get(t, h.link(7, "1"))
	body(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("without a resolver: %s", resp.Status)
	}
}

// lapsingFiles serves a film's bytes, with ranges, at /file?token=N, each
// token for one request only, as a portal's link that lapses once played.
type lapsingFiles struct {
	mu      sync.Mutex
	used    map[string]bool
	content []byte
}

func (f *lapsingFiles) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	f.mu.Lock()
	used := f.used[token]
	f.used[token] = true
	f.mu.Unlock()
	if used || token == "" {
		http.Error(w, "this link has lapsed", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(f.content))
}

// itemResolver gives the lapsing files' links for a film, a new one at
// each call.
type itemResolver struct {
	base string

	mu       sync.Mutex
	calls    []string
	holds    int
	releases int
	err      error
}

func (f *itemResolver) Resolve(_ context.Context, _ int64, kind, ref string) (Resolved, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, kind+" "+ref)
	if f.err != nil {
		return Resolved{}, f.err
	}
	return Resolved{Address: f.base + "/file?token=" + strconv.Itoa(len(f.calls)), Hold: func() func() {
		f.mu.Lock()
		f.holds++
		f.mu.Unlock()
		return func() {
			f.mu.Lock()
			f.releases++
			f.mu.Unlock()
		}
	}}, nil
}

func (f *itemResolver) counts() (calls, holds, releases int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls), f.holds, f.releases
}

// A portal's film plays through the hub by the address that the resolver
// gives, which is asked for again when it lapses (D77).
func TestResolvedFilms(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	src, err := st.AddSource(ctx, source.Settings{Name: "portal", Type: source.TypeStalker, URL: "http://portal.example/c/",
		Device: source.Device{MAC: "00:1A:79:00:00:51"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := st.ApplyPortalCategories(ctx, src, store.KindMovie, []store.LibraryCategory{{Key: "vod:1", Name: "Drama"}}, now); err != nil {
		t.Fatal(err)
	}
	shelves, _ := st.Shelves(ctx, store.KindMovie, store.EverySource)
	if err := st.ApplyPortalPage(ctx, shelves[0].ID, 1, []store.PortalItem{{Kind: store.KindMovie,
		Title: store.LibraryTitle{Key: "vod:1001", Name: "Example Film", File: "cmd=%2Fmedia%2F1001.mpg"}}}, 1, now); err != nil {
		t.Fatal(err)
	}
	part, _ := st.PortalTitles(ctx, shelves[0].ID, 0, 14)
	item, err := st.PlayableMovie(ctx, part[0].ID)
	if err != nil {
		t.Fatal(err)
	}

	files := &lapsingFiles{used: map[string]bool{}, content: bytes.Repeat([]byte("0123456789"), 5000)}
	upstreamFiles := httptest.NewServer(files)
	t.Cleanup(upstreamFiles.Close)
	logs := &syncBuffer{}
	h := New(st, slog.New(slog.NewTextHandler(logs, nil)), "test", map[int64]Source{src: {Name: "portal", MaxConnections: 1, Resolves: true}})
	f := &itemResolver{base: upstreamFiles.URL}
	h.UseResolver(f)
	front := httptest.NewServer(h)
	t.Cleanup(front.Close)
	link := front.URL + h.ItemLink(7, item)

	ranged := func(from, to int) (int, string) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, link, nil)
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", from, to))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body(t, resp)
	}
	if status, got := ranged(100, 109); status != http.StatusPartialContent || got != "0123456789" {
		t.Fatalf("first range: %d %q", status, got)
	}
	// The player seeks: the first link has lapsed, and the hub asks for
	// another, once.
	if status, got := ranged(20005, 20009); status != http.StatusPartialContent || got != "56789" {
		t.Fatalf("second range: %d %q", status, got)
	}
	calls, holds, releases := f.counts()
	if calls != 2 || holds != 1 || releases != 0 || f.calls[0] != "movie cmd=%2Fmedia%2F1001.mpg" {
		t.Errorf("%d resolves (%q), %d holds, %d releases", calls, f.calls, holds, releases)
	}
	if !strings.Contains(logs.String(), "a title's source no longer took its address, and gave another") ||
		strings.Contains(logs.String(), "token=") {
		t.Errorf("log:\n%s", logs)
	}

	// No address for it: the request fails, and nothing is held.
	f.mu.Lock()
	f.err = errors.New("the portal gave no link: nothing_to_play")
	f.mu.Unlock()
	resp := get(t, front.URL+h.ItemLink(8, item))
	body(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("without an address: %s", resp.Status)
	}
	if _, holds, _ := f.counts(); holds != 1 {
		t.Errorf("%d holds", holds)
	}
}
