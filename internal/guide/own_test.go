// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// A fictional guide of the household's own, with traps for matching: the
// same name in two countries, a name that holds another, and a channel
// that the source's own guide fills already. The first programmes of each
// channel are the evening's; each lasts an hour.
const ownXML = `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="GraniteNews.uk"><display-name>Granite News</display-name><icon src="http://logos.example/granite-uk.png"/></channel>
  <channel id="GraniteNews.us"><display-name>Granite News</display-name></channel>
  <channel id="GraniteNewsExtra.uk"><display-name>Granite News Extra</display-name></channel>
  <channel id="harbour.example"><display-name>Harbour Sport</display-name></channel>
  <channel id="covered.example"><display-name>Covered</display-name></channel>
  <channel id="spare.example"><display-name>Spare Channel</display-name></channel>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="GraniteNews.uk"><title>UK Evening</title></programme>
  <programme start="20261204190000 +0000" stop="20261204200000 +0000" channel="GraniteNews.uk"><title>UK Late</title></programme>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="GraniteNews.us"><title>US Evening</title></programme>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="GraniteNewsExtra.uk"><title>Extra Evening</title></programme>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="harbour.example"><title>Harbour Match</title></programme>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="covered.example"><title>Not used</title></programme>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="spare.example"><title>Spare Evening</title></programme>
</tv>`

type ownHarness struct {
	t     *testing.T
	st    *store.Store
	o     *Own
	logs  *bytes.Buffer
	srv   *httptest.Server
	mu    sync.Mutex
	body  string
	gz    bool
	hits  int
	now   time.Time
	src   int64
	chans map[string]int64 // the source's channels by name
}

func newOwnHarness(t *testing.T) *ownHarness {
	t.Helper()
	h := &ownHarness{t: t, logs: &bytes.Buffer{}, body: ownXML, now: now, chans: map[string]int64{}}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.st = st
	h.o = NewOwn(st, slog.New(slog.NewTextHandler(h.logs, nil)), "test")
	h.o.now = func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.hits++
		if r.URL.Query().Get("token") != secret {
			http.NotFound(w, r)
			return
		}
		if h.gz {
			zw := gzip.NewWriter(w)
			io.WriteString(zw, h.body)
			zw.Close()
			return
		}
		io.WriteString(w, h.body)
	}))
	t.Cleanup(h.srv.Close)
	ctx := context.Background()
	if h.src, err = st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeM3U, URL: "/srv/main.m3u",
		Refresh: time.Hour, Numbers: source.NumbersPlaylist}); err != nil {
		t.Fatal(err)
	}
	h.apply(
		store.CatalogChannel{IdentityKey: "a", Name: "UK: Granite News HD", StreamURL: "http://streams.example/a"},
		store.CatalogChannel{IdentityKey: "b", Name: "Granite News", Category: "US | News", StreamURL: "http://streams.example/b"},
		store.CatalogChannel{IdentityKey: "c", Name: "Granite News", StreamURL: "http://streams.example/c"},
		store.CatalogChannel{IdentityKey: "d", Name: "Granite", StreamURL: "http://streams.example/d"},
		store.CatalogChannel{IdentityKey: "e", Name: "Harbour", XMLTVID: "HARBOUR.example", StreamURL: "http://streams.example/e"},
		store.CatalogChannel{IdentityKey: "f", Name: "Covered", StreamURL: "http://streams.example/f"},
	)
	// The source's own guide fills Covered.
	w, err := st.BeginGuide(ctx, h.src)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Add(ctx, store.GuideProgramme{ChannelID: h.chans["Covered"], Start: now, Stop: now.Add(time.Hour),
		Title: "Its own"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(ctx, now, "own"); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *ownHarness) apply(chs ...store.CatalogChannel) {
	h.t.Helper()
	ctx := context.Background()
	if _, err := h.st.ApplyCatalog(ctx, h.src, chs, now); err != nil {
		h.t.Fatal(err)
	}
	gcs, err := h.st.GuideChannels(ctx, h.src)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range gcs {
		h.chans[c.Name] = c.ID
	}
}

func (h *ownHarness) set(fn func(h *ownHarness)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

func (h *ownHarness) address() string { return h.srv.URL + "/guide.xml?token=" + secret }

func (h *ownHarness) addGuide(name, address string) int64 {
	h.t.Helper()
	id, err := h.st.AddGuide(context.Background(), store.GuideSettings{Name: name, Address: address}, now)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

// shown returns, by the source's channel's name, the titles that the
// household's lineup shows on it around now.
func (h *ownHarness) shown() map[string]string {
	h.t.Helper()
	ctx := context.Background()
	cs, err := h.st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		h.t.Fatal(err)
	}
	names := map[int64]string{}
	for _, c := range cs {
		names[c.EntryID] = c.Name
	}
	out := map[string]string{}
	if err := h.st.EachLineupProgramme(ctx, store.DefaultLineupID, now.Add(-24*time.Hour), now.Add(24*time.Hour),
		func(entry int64, p store.Programme) error {
			if out[names[entry]] != "" {
				out[names[entry]] += ", "
			}
			out[names[entry]] += p.Title
			return nil
		}); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func (h *ownHarness) fetches() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.hits
}

func TestOwnGuide(t *testing.T) {
	h := newOwnHarness(t)
	ctx := context.Background()
	// Without a guide, nothing is fetched or matched.
	h.o.step(ctx)
	if h.fetches() != 0 {
		t.Fatal("fetched without a guide")
	}
	id := h.addGuide("extra", h.address())
	h.o.Changed()
	h.o.step(ctx)
	if h.fetches() != 1 {
		t.Fatalf("%d fetches, want 1", h.fetches())
	}
	want := map[string]string{
		"UK: Granite News HD": "UK Evening, UK Late",
		"Granite News":        "US Evening", // the one whose group says US; the other ties, and gets none
		"Harbour":             "Harbour Match",
		"Covered":             "Its own",
	}
	if got := h.shown(); !reflect.DeepEqual(got, want) {
		t.Errorf("shown %v, want %v", got, want)
	}
	g, err := h.st.Guide(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if g.Listed != 6 || g.Matched != 3 || g.ByName != 2 || g.Waiting != 0 || g.Channels != 3 || g.Programmes != 4 ||
		!g.ReadAt.Equal(now) || g.ReadError != "" {
		t.Errorf("the guide %+v", g)
	}
	if log := h.logs.String(); !strings.Contains(log, "guide read") || !strings.Contains(log, "guide=extra") ||
		!strings.Contains(log, "fills=3") || !strings.Contains(log, "by_name=2") || strings.Contains(log, secret) {
		t.Errorf("the log: %s", log)
	}
	// Not due again until the interval.
	h.o.step(ctx)
	if h.fetches() != 1 {
		t.Errorf("read again at once: %d fetches", h.fetches())
	}

	// A channel that the source adds is matched at once, and waits for the
	// guide's next read, an hour after the last.
	h.apply(
		store.CatalogChannel{IdentityKey: "a", Name: "UK: Granite News HD", StreamURL: "http://streams.example/a"},
		store.CatalogChannel{IdentityKey: "b", Name: "Granite News", Category: "US | News", StreamURL: "http://streams.example/b"},
		store.CatalogChannel{IdentityKey: "c", Name: "Granite News", StreamURL: "http://streams.example/c"},
		store.CatalogChannel{IdentityKey: "d", Name: "Granite", StreamURL: "http://streams.example/d"},
		store.CatalogChannel{IdentityKey: "e", Name: "Harbour", XMLTVID: "HARBOUR.example", StreamURL: "http://streams.example/e"},
		store.CatalogChannel{IdentityKey: "f", Name: "Covered", StreamURL: "http://streams.example/f"},
		store.CatalogChannel{IdentityKey: "g", Name: "Spare Channel", StreamURL: "http://streams.example/g"},
	)
	h.o.SourcesChanged()
	h.o.step(ctx)
	if g, _ := h.st.Guide(ctx, id); g.Matched != 4 || g.Waiting != 1 || h.fetches() != 1 {
		t.Errorf("after the source added a channel: %+v, %d fetches", g, h.fetches())
	}
	h.set(func(h *ownHarness) { h.now = now.Add(MatchedAgainAfter) })
	h.o.step(ctx)
	if h.fetches() != 2 {
		t.Fatalf("not read for the new channel: %d fetches", h.fetches())
	}
	if got := h.shown()["Spare Channel"]; got != "Spare Evening" {
		t.Errorf("the new channel shows %q", got)
	}
	if g, _ := h.st.Guide(ctx, id); g.Waiting != 0 || g.Matched != 4 {
		t.Errorf("after the read: %+v", g)
	}

	// A channel whose own source's guide comes to fill it leaves the
	// guide, at once.
	w, err := h.st.BeginGuide(ctx, h.src)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Covered", "Harbour"} {
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: h.chans[name], Start: now, Stop: now.Add(time.Hour), Title: "Its own"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(ctx, now, "own"); err != nil {
		t.Fatal(err)
	}
	h.o.SourcesChanged()
	h.o.step(ctx)
	if got := h.shown()["Harbour"]; got != "Its own" {
		t.Errorf("Harbour shows %q, want its own source's", got)
	}
	if g, _ := h.st.Guide(ctx, id); g.Matched != 3 {
		t.Errorf("after the source's guide filled Harbour: %+v", g)
	}

	// Turned off, the guide fills nothing; removed, it is gone.
	if err := h.st.SetGuideEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	h.o.Changed()
	h.o.step(ctx)
	if got := h.shown(); !reflect.DeepEqual(got, map[string]string{"Covered": "Its own", "Harbour": "Its own"}) {
		t.Errorf("with the guide off: %v", got)
	}
	if ms, err := h.st.GuideMatches(ctx); err != nil || len(ms) != 0 {
		t.Errorf("matches with the guide off: %+v, %v", ms, err)
	}
}

func TestOwnGuideOrderAndRead(t *testing.T) {
	h := newOwnHarness(t)
	ctx := context.Background()
	first := h.addGuide("first", h.address())
	// A second guide in a file, gzip, that knows Granite News only in the UK.
	dir := t.TempDir()
	path := filepath.Join(dir, "second.xml.gz")
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	io.WriteString(zw, `<tv><channel id="GraniteNews.uk"><display-name>Granite News</display-name></channel>
		<channel id="harbour.example"><display-name>Harbour</display-name></channel>
		<programme start="20261204180000" stop="20261204190000" channel="GraniteNews.uk"><title>Second's news</title></programme>
		<programme start="20261204180000" stop="20261204190000" channel="harbour.example"><title>Second's match</title></programme>
		</tv>`)
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	second := h.addGuide("second", path)
	h.o.Changed()
	h.o.step(ctx)
	// The first guide fills what it can; the second, what is left. An ID
	// in both is the first's.
	want := map[string]string{
		"UK: Granite News HD": "UK Evening, UK Late",
		"Granite News":        "US Evening",
		"Harbour":             "Harbour Match",
		"Covered":             "Its own",
	}
	if got := h.shown(); !reflect.DeepEqual(got, want) {
		t.Errorf("shown %v, want %v", got, want)
	}
	// Second first: it fills the UK channel and Harbour, and the first guide's
	// channels that it no longer needs wait for nothing.
	if err := h.st.MoveGuide(ctx, second, 1); err != nil {
		t.Fatal(err)
	}
	h.o.Changed()
	h.o.step(ctx)
	want = map[string]string{
		"UK: Granite News HD": "Second's news",
		"Granite News":        "US Evening",
		"Harbour":             "Second's match",
		"Covered":             "Its own",
	}
	if got := h.shown(); !reflect.DeepEqual(got, want) {
		t.Errorf("with the second first: %v, want %v", got, want)
	}
	if g, _ := h.st.Guide(ctx, first); g.Matched != 1 {
		t.Errorf("the first guide %+v", g)
	}

	// The admin's Read now reads a guide whatever its schedule.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { h.o.Run(runCtx); close(done) }()
	defer func() { cancel(); <-done }()
	res, err := h.o.ReadNow(ctx, first)
	if err != nil || res.Listed != 6 || res.Fills != 1 || res.Channels != 1 {
		t.Errorf("read now: %+v, %v", res, err)
	}
	if _, err := h.o.ReadNow(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("read now of no guide: %v", err)
	}
}

func TestOwnGuideFailures(t *testing.T) {
	h := newOwnHarness(t)
	ctx := context.Background()
	id := h.addGuide("extra", h.address())
	h.o.Changed()
	h.o.step(ctx)
	if got := h.shown()["Harbour"]; got != "Harbour Match" {
		t.Fatalf("Harbour shows %q", got)
	}
	settings, err := h.st.GuideSettings(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	read := func() store.Guide {
		t.Helper()
		g, err := h.st.Guide(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.o.read(ctx, g, settings); err == nil {
			t.Error("the read worked")
		}
		g, _ = h.st.Guide(ctx, id)
		if got := h.shown()["Harbour"]; got != "Harbour Match" {
			t.Errorf("after a failed read, Harbour shows %q", got)
		}
		if strings.Contains(g.ReadError, secret) || strings.Contains(h.logs.String(), secret) {
			t.Errorf("the error or the log shows the address: %q", g.ReadError)
		}
		return g
	}
	// Not XMLTV.
	h.set(func(h *ownHarness) { h.body = "<html>not a guide</html>" })
	if g := read(); !strings.Contains(g.ReadError, "not XMLTV") {
		t.Errorf("error %q", g.ReadError)
	}
	// Larger than the limit.
	h.set(func(h *ownHarness) { h.body = ownXML })
	h.o.reader.maxBytes = 100
	if g := read(); !strings.Contains(g.ReadError, "larger than") {
		t.Errorf("error %q", g.ReadError)
	}
	h.o.reader.maxBytes = maxGuideBytes
	// A file that is not a regular one, and one that is not there.
	for _, c := range []struct{ path, want string }{{t.TempDir(), "not a regular file"}, {"/srv/none/guide.xml", "does not exist"}} {
		settings.Address = c.path
		if g := read(); !strings.Contains(g.ReadError, c.want) || strings.Contains(g.ReadError, c.path) {
			t.Errorf("%s: error %q", c.path, g.ReadError)
		}
	}
	// A guide kept to public addresses does not read one at home (D121).
	settings.Address = h.address()
	settings.PublicOnly = true
	before := h.fetches()
	if g := read(); !strings.Contains(g.ReadError, "public") {
		t.Errorf("error %q", g.ReadError)
	}
	if h.fetches() != before {
		t.Error("the guide's server heard the request")
	}
	// A failed read is tried again an hour later.
	h.set(func(h *ownHarness) { h.now = now.Add(RetryAfter - time.Minute) })
	if g, _ := h.st.Guide(ctx, id); h.o.due(g, h.address(), false, false) {
		t.Error("due before the hour")
	}
	h.set(func(h *ownHarness) { h.now = now.Add(RetryAfter) })
	if g, _ := h.st.Guide(ctx, id); !h.o.due(g, h.address(), false, false) {
		t.Error("not due after the hour")
	}
}

// TestOwnGuideKeepsThePast checks that a read keeps what the read before
// had and that has ended, as a source's guide does (D83).
func TestOwnGuideKeepsThePast(t *testing.T) {
	h := newOwnHarness(t)
	ctx := context.Background()
	h.addGuide("extra", h.address())
	h.o.Changed()
	h.o.step(ctx)
	later := now.Add(DefaultInterval)
	h.set(func(h *ownHarness) {
		h.now = later
		h.body = strings.NewReplacer("20261204180000", "20261205060000", "20261204190000", "20261205070000",
			"20261204200000", "20261205080000").Replace(ownXML)
	})
	h.o.step(ctx)
	if h.fetches() != 2 {
		t.Fatalf("%d fetches", h.fetches())
	}
	ps, err := h.st.EntryProgrammes(ctx, store.DefaultLineupID, h.entry("Harbour"), now.Add(-time.Hour), later.Add(24*time.Hour))
	var titles []string
	for _, p := range ps {
		titles = append(titles, fmt.Sprintf("%s %s", p.Title, p.Start.Format("02 15:04")))
	}
	sort.Strings(titles)
	if err != nil || !reflect.DeepEqual(titles, []string{"Harbour Match 04 18:00", "Harbour Match 05 06:00"}) {
		t.Errorf("Harbour's programmes %v, %v", titles, err)
	}
}

func (h *ownHarness) entry(name string) int64 {
	h.t.Helper()
	cs, err := h.st.LineupChannels(context.Background(), store.DefaultLineupID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			return c.EntryID
		}
	}
	h.t.Fatalf("no channel %q", name)
	return 0
}
