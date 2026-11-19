// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/m3u"
	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// A fictional playlist, as an Xtream-style provider might publish it.
const playlist = `#EXTM3U
#EXTINF:-1 tvg-id="news.example" tvg-chno="101" tvg-logo="http://logos.example/news.png" group-title="News",Example News
http://streams.example/live/u/p/101.ts
#EXTINF:-1 tvg-id="sports.example" catchup="default" catchup-days="3" group-title="Sports",Example Sports
http://streams.example/live/u/p/102.ts
#EXTINF:-1 group-title="Movies",A Film (2024)
http://streams.example/movie/u/p/900.mkv
#EXTINF:-1 group-title="Series",A Show S01 E01
http://streams.example/series/u/p/901.mkv
#EXTINF:-1,No address
not a url
`

const secret = "s3cret-password"

type harness struct {
	t        *testing.T
	store    *store.Store
	r        *Refresher
	logs     *syncBuffer
	srcID    int64
	server   *httptest.Server
	body     atomic.Value // string served by server
	status   atomic.Int32
	requests atomic.Int32
}

// syncBuffer is a log that the refresher's goroutines and the test share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, logs: &syncBuffer{}}
	log := slog.New(slog.NewTextHandler(h.logs, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.store = st
	h.r = NewRefresher(st, log, "test")

	h.body.Store(playlist)
	h.status.Store(http.StatusOK)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.UserAgent(); got != "Overscan/test" && got != "TestPlayer/1.0" {
			t.Errorf("user agent %q", got)
		}
		h.requests.Add(1)
		w.WriteHeader(int(h.status.Load()))
		io.WriteString(w, h.body.Load().(string))
	}))
	t.Cleanup(h.server.Close)
	if h.srcID, err = st.AddSource(context.Background(), h.source()); err != nil {
		t.Fatal(err)
	}
	return h
}

// source returns the test source's settings; its address carries a
// password.
func (h *harness) source() source.Settings {
	return source.Settings{
		Name:           "main",
		Type:           source.TypeM3U,
		URL:            h.server.URL + "/get.php?username=user&password=" + secret + "&type=m3u_plus",
		Refresh:        time.Hour,
		MaxConnections: 1,
		Numbers:        source.NumbersPlaylist,
	}
}

// waitFor waits up to five seconds for ok.
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited in vain for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// run runs the refresher until the test ends.
func (h *harness) run() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.r.Run(ctx)
		close(done)
	}()
	h.t.Cleanup(func() {
		cancel()
		<-done
	})
}

func (h *harness) channelNames() []string {
	h.t.Helper()
	var names []string
	cs, err := h.store.LineupChannels(context.Background(), store.DefaultLineupID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range cs {
		names = append(names, c.Name)
	}
	return names
}

func TestRefresh(t *testing.T) {
	h := newHarness(t)
	ch, err := h.r.Refresh(context.Background(), h.source(), h.srcID)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Added != 2 {
		t.Errorf("changes %+v", ch)
	}
	if got, want := h.channelNames(), []string{"Example News", "Example Sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("channels %v, want %v", got, want)
	}
	if !strings.Contains(h.logs.String(), "skipped=3") {
		t.Errorf("log does not count the skipped entries: %s", h.logs)
	}
	srcs, err := h.store.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if srcs[0].RefreshedAt.IsZero() || srcs[0].RefreshError != "" {
		t.Errorf("source %+v", srcs[0])
	}
}

func TestFailedRefreshKeepsCatalog(t *testing.T) {
	for name, set := range map[string]func(h *harness){
		"error status":   func(h *harness) { h.status.Store(http.StatusInternalServerError) },
		"error page":     func(h *harness) { h.body.Store("<html><body>Account expired</body></html>") },
		"empty playlist": func(h *harness) { h.body.Store("#EXTM3U\n") },
		"server down":    func(h *harness) { h.server.Close() },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if _, err := h.r.Refresh(ctx, h.source(), h.srcID); err != nil {
				t.Fatal(err)
			}
			set(h)
			_, err := h.r.Refresh(ctx, h.source(), h.srcID)
			if err == nil {
				t.Fatal("no error")
			}
			if len(h.channelNames()) != 2 {
				t.Errorf("channels %v after a failed refresh", h.channelNames())
			}
			srcs, _ := h.store.Sources(ctx)
			for what, text := range map[string]string{"error": err.Error(), "stored error": srcs[0].RefreshError, "log": h.logs.String()} {
				if strings.Contains(text, secret) {
					t.Errorf("the %s shows the source's password: %s", what, text)
				}
			}
			if srcs[0].RefreshError == "" {
				t.Error("no stored error")
			}
		})
	}
}

func TestRefreshFromFile(t *testing.T) {
	h := newHarness(t)
	path := filepath.Join(t.TempDir(), "playlist.m3u")
	if err := os.WriteFile(path, []byte(playlist), 0o600); err != nil {
		t.Fatal(err)
	}
	src := h.source()
	src.URL = path
	if _, err := h.r.Refresh(context.Background(), src, h.srcID); err != nil {
		t.Fatal(err)
	}
	if len(h.channelNames()) != 2 {
		t.Errorf("channels %v", h.channelNames())
	}
}

func TestUserAgentFromSource(t *testing.T) {
	h := newHarness(t)
	src := h.source()
	src.UserAgent = "TestPlayer/1.0"
	if _, err := h.r.Refresh(context.Background(), src, h.srcID); err != nil {
		t.Fatal(err)
	}
}

func TestOversizedPlaylistIsRefused(t *testing.T) {
	h := newHarness(t)
	h.r.maxBytes = 100
	if _, err := h.r.Refresh(context.Background(), h.source(), h.srcID); err == nil || !strings.Contains(err.Error(), "larger") {
		t.Errorf("error %v", err)
	}
}

func TestRunRetriesAfterFailure(t *testing.T) {
	h := newHarness(t)
	h.r.firstRetry = 10 * time.Millisecond
	h.status.Store(http.StatusServiceUnavailable)
	h.run()
	time.Sleep(50 * time.Millisecond)
	h.status.Store(http.StatusOK)
	waitFor(t, "the source to recover", func() bool { return len(h.channelNames()) == 2 })
}

func TestRunFollowsTheSources(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var seen atomic.Value
	var syncs atomic.Int32
	h.r.OnChange(func(sources []store.Source) {
		var names []string
		for _, src := range sources {
			names = append(names, src.Name)
		}
		seen.Store(strings.Join(names, " "))
		syncs.Add(1)
	})
	// reload asks the refresher to read the sources again, and waits until
	// it has begun to.
	reload := func() {
		n := syncs.Load()
		h.r.Reload()
		waitFor(t, "a reload", func() bool { return syncs.Load() > n })
	}
	h.run()
	waitFor(t, "the first refresh", func() bool { return len(h.channelNames()) == 2 })
	if seen.Load() != "main" {
		t.Errorf("told of %v", seen.Load())
	}

	// A source added later is read once the refresher reloads.
	second := h.source()
	second.Name = "second"
	second.URL = h.server.URL + "/second.m3u"
	if _, err := h.store.AddSource(ctx, second); err != nil {
		t.Fatal(err)
	}
	before := h.requests.Load()
	reload()
	waitFor(t, "the second source", func() bool { return seen.Load() == "main second" && h.requests.Load() == before+1 })

	// Changed settings are read again at once; unchanged ones are not.
	changed := h.source()
	changed.Refresh = 2 * time.Hour
	if err := h.store.UpdateSource(ctx, h.srcID, changed); err != nil {
		t.Fatal(err)
	}
	reload()
	waitFor(t, "a refresh with the new settings", func() bool { return h.requests.Load() == before+2 })

	// A source turned off is no longer read.
	if err := h.store.SetSourceEnabled(ctx, h.srcID, false); err != nil {
		t.Fatal(err)
	}
	reload()
	if err := h.store.SetSourceEnabled(ctx, h.srcID, true); err != nil {
		t.Fatal(err)
	}
	reload()
	waitFor(t, "a refresh after turning it back on", func() bool { return h.requests.Load() == before+3 })
	time.Sleep(50 * time.Millisecond)
	if n := h.requests.Load(); n != before+3 {
		t.Errorf("%d requests after the changes, want %d", n, before+3)
	}
}

func TestRefreshNowAndTest(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	ch, err := h.r.RefreshNow(ctx, h.srcID)
	if err != nil || ch.Added != 2 {
		t.Fatalf("refresh now: %+v, %v", ch, err)
	}
	if _, err := h.r.RefreshNow(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown source: %v", err)
	}

	// A test reads the playlist and stores nothing.
	other := h.source()
	other.URL = h.server.URL + "/other.m3u"
	h.body.Store(playlist + "#EXTINF:-1,Third\nhttp://streams.example/live/u/p/103.ts\n")
	got, err := h.r.Test(ctx, other)
	if err != nil || !reflect.DeepEqual(got, Tested{Channels: 3, Skipped: 3}) {
		t.Errorf("test: %+v, %v", got, err)
	}
	if len(h.channelNames()) != 2 {
		t.Errorf("a test changed the lineup: %v", h.channelNames())
	}
	h.status.Store(http.StatusForbidden)
	if _, err := h.r.Test(ctx, other); err == nil || strings.Contains(err.Error(), secret) {
		t.Errorf("a refused test: %v", err)
	}
}

func TestUnreadableSourcesAreReported(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	// The address was saved under another key.
	h.store.UseKey(secrets.NewRandomBox())
	h.run()
	waitFor(t, "the warning", func() bool { return strings.Contains(h.logs.String(), "another key file") })
	srcs, err := h.store.Sources(ctx)
	if err != nil || srcs[0].Address != store.AddressLocked || !strings.Contains(srcs[0].RefreshError, "another key file") {
		t.Errorf("source %+v, %v", srcs, err)
	}
	if h.requests.Load() != 0 {
		t.Error("the source was read")
	}
}

func TestIdentityKeys(t *testing.T) {
	entries := []keyFields{
		{name: "One", id: "one.example"},
		{name: "Two HD", group: "A", id: "two.example"},
		{name: "Two SD", group: "A", id: "two.example"},
		{name: "  Three   Lives ", group: "B"},
		{name: "three lives", group: "b"},
		{name: "Four", group: "C", id: " "},
	}
	want := []string{
		"id:one.example",
		"name:two hd|a",
		"name:two sd|a",
		"name:three lives|b",
		"name:three lives|b#2",
		"name:four|c",
	}
	if got := identityKeys(entries); !reflect.DeepEqual(got, want) {
		t.Errorf("keys\n%q\nwant\n%q", got, want)
	}
}

func TestChannel(t *testing.T) {
	e := m3u.Entry{
		Title: "  Name\twith\x01control ",
		Group: "Group",
		URL:   "http://streams.example/1.ts",
		Attrs: map[string]string{
			"tvg-chno":     "12",
			"tvg-logo":     "javascript:alert(1)",
			"catchup-days": "365",
			"tvg-id":       "id.example",
		},
	}
	got := channel(e, nil, "key", 0)
	want := store.CatalogChannel{
		IdentityKey: "key", Name: "Name with control", Number: 12, Category: "Group",
		XMLTVID: "id.example", StreamURL: e.URL, ArchiveDays: maxArchiveDays,
	}
	if got != want {
		t.Errorf("channel\n%+v\nwant\n%+v", got, want)
	}
	if got := channel(m3u.Entry{URL: e.URL, Attrs: map[string]string{}}, nil, "k", 4); got.Name != "Channel 5" {
		t.Errorf("fallback name %q", got.Name)
	}
}

// TestChannelCatchup checks that a playlist's channel gets the catch-up
// that its entry describes, with the header's attributes as defaults
// (D82).
func TestChannelCatchup(t *testing.T) {
	header := map[string]string{"catchup": "append", "catchup-source": "?utc={utc}&lutc={lutc}", "catchup-days": "3"}
	e := m3u.Entry{Title: "One", URL: "http://streams.example/1.m3u8", Attrs: map[string]string{}}
	got := channel(e, header, "k", 0)
	if got.ArchiveDays != 3 || got.Catchup != (catchup.Playlist{Mode: catchup.ModeAppend, Source: "?utc={utc}&lutc={lutc}"}) {
		t.Errorf("from the header: %d days, %+v", got.ArchiveDays, got.Catchup)
	}
	e.Attrs = map[string]string{"catchup": "shift", "catchup-days": "7"}
	got = channel(e, header, "k", 0)
	if got.ArchiveDays != 7 || got.Catchup != (catchup.Playlist{Mode: catchup.ModeShift}) {
		t.Errorf("its own: %d days, %+v", got.ArchiveDays, got.Catchup)
	}
	e.Attrs = map[string]string{"catchup": "vod", "catchup-days": "2"}
	got = channel(e, nil, "k", 0)
	if got.ArchiveDays != 2 || got.Catchup.Mode != "" {
		t.Errorf("a mode Overscan does not play keeps its days for the guide alone: %d days, %+v", got.ArchiveDays, got.Catchup)
	}
}

func TestPlaylistNamesAGuide(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.body.Store(strings.Replace(playlist, "#EXTM3U", `#EXTM3U url-tvg="http://guide.example/epg.xml.gz?key=`+secret+`"`, 1))
	if _, err := h.r.Refresh(ctx, h.source(), h.srcID); err != nil {
		t.Fatal(err)
	}
	if got, err := h.store.ListedGuide(ctx, h.srcID); err != nil || got != "http://guide.example/epg.xml.gz?key="+secret {
		t.Errorf("listed %q, %v", got, err)
	}
	h.body.Store(playlist)
	if _, err := h.r.Refresh(ctx, h.source(), h.srcID); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.store.ListedGuide(ctx, h.srcID); got != "" {
		t.Errorf("a playlist that names no guide left %q", got)
	}
}
