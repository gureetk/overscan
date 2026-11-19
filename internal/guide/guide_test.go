// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

const secret = "s3cret-password"

// now is the fixed time of the tests, a Friday evening in UTC.
var now = time.Date(2026, 12, 4, 18, 30, 0, 0, time.UTC)

// A fictional guide for three of the source's channels: one by guide ID,
// one by a name with a country tag, and one whose programmes lack stops.
const guideXML = `<?xml version="1.0" encoding="UTF-8"?>
<tv>
  <channel id="news.example"><display-name>Example News</display-name></channel>
  <channel id="sport-guide"><display-name>Example Sports</display-name></channel>
  <channel id="film.example"><display-name>Film Four</display-name></channel>
  <channel id="other.example"><display-name>Not Ours</display-name></channel>
  <programme start="20261130120000 +0000" stop="20261130130000 +0000" channel="news.example"><title>Too old</title></programme>
  <programme start="20261204170000 +0000" stop="20261204180000 +0000" channel="news.example"><title>Early News</title></programme>
  <programme start="20261204180000 +0000" stop="20261204190000 +0000" channel="news.example">
    <title>Evening News</title><sub-title>Headlines</sub-title><desc>The day's news.</desc><category>News</category></programme>
  <programme start="20261221120000 +0000" stop="20261221130000 +0000" channel="news.example"><title>Too far</title></programme>
  <programme start="20261204180000 +0100" stop="20261204200000 +0100" channel="sport-guide"><title>Match</title></programme>
  <programme start="20261204180000" channel="film.example"><title>First Film</title></programme>
  <programme start="20261204200000" channel="film.example"><title>Second Film</title></programme>
  <programme start="20261204220000" channel="film.example"><title>Last Film</title></programme>
  <programme start="20261204180000" stop="20261204190000" channel="other.example"><title>Not ours</title></programme>
</tv>`

type harness struct {
	t      *testing.T
	st     *store.Store
	g      *Reader
	logs   *bytes.Buffer
	srv    *httptest.Server
	mu     sync.Mutex
	body   string
	status int
	gz     bool
	hits   int
	id     int64
	chans  map[string]int64 // channel IDs by name
}

func newHarness(t *testing.T, typ string) *harness {
	t.Helper()
	h := &harness{t: t, logs: &bytes.Buffer{}, body: guideXML, status: http.StatusOK, chans: map[string]int64{}}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.st = st
	h.g = New(st, slog.New(slog.NewTextHandler(h.logs, nil)), "test")
	h.g.now = func() time.Time { return now }
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.hits++
		if typ == source.TypeXtream && (r.URL.Path != "/xmltv.php" || r.URL.Query().Get("password") != secret) {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(h.status)
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
	if h.id, err = st.AddSource(ctx, h.settings(typ)); err != nil {
		t.Fatal(err)
	}
	channels := []store.CatalogChannel{
		{IdentityKey: "a", Name: "Example News", XMLTVID: "NEWS.example", StreamURL: "live/1.ts", ArchiveDays: 0},
		{IdentityKey: "b", Name: "UK: Example Sports HD", StreamURL: "live/2.ts"},
		{IdentityKey: "c", Name: "Film Four", XMLTVID: "film.example", StreamURL: "live/3.ts"},
		{IdentityKey: "d", Name: "Example Sports +1", StreamURL: "live/4.ts"},
		{IdentityKey: "e", Name: "Unmatched", XMLTVID: "nowhere", StreamURL: "live/5.ts"},
	}
	if _, err := st.ApplyCatalog(ctx, h.id, channels, now); err != nil {
		t.Fatal(err)
	}
	gcs, err := st.GuideChannels(ctx, h.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range gcs {
		h.chans[c.Name] = c.ID
	}
	return h
}

func (h *harness) settings(typ string) source.Settings {
	if typ == source.TypeXtream {
		return source.Settings{Name: "main", Type: typ, URL: h.srv.URL, Username: "viewer", Password: secret,
			Refresh: time.Hour, Numbers: source.NumbersPlaylist}
	}
	return source.Settings{Name: "main", Type: source.TypeM3U, URL: "/srv/main.m3u", Refresh: time.Hour,
		Numbers: source.NumbersPlaylist}
}

func (h *harness) set(fn func(h *harness)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	fn(h)
}

// titles returns the titles of a channel's programmes around now.
func (h *harness) titles(name string) string {
	h.t.Helper()
	ps, err := h.st.Programmes(context.Background(), h.chans[name], now.Add(-30*24*time.Hour), now.Add(60*24*time.Hour))
	if err != nil {
		h.t.Fatal(err)
	}
	var out []string
	for _, p := range ps {
		out = append(out, fmt.Sprintf("%s %s-%s", p.Title, p.Start.Format("15:04"), p.Stop.Format("15:04")))
	}
	return strings.Join(out, ", ")
}

func TestReadXtreamGuide(t *testing.T) {
	h := newHarness(t, source.TypeXtream)
	ctx := context.Background()
	read, res, err := h.g.ReadIfDue(ctx, h.settings(source.TypeXtream), h.id)
	if err != nil || !read {
		t.Fatalf("read %v, %v", read, err)
	}
	if res.Channels != 3 || res.Programmes != 5 || res.Skipped != 1 || res.Listed != 4 || res.ByName != 1 {
		t.Errorf("result %+v", res)
	}
	for name, want := range map[string]string{
		"Example News":          "Early News 17:00-18:00, Evening News 18:00-19:00",
		"UK: Example Sports HD": "Match 17:00-19:00",
		"Film Four":             "First Film 18:00-20:00, Second Film 20:00-22:00",
		"Example Sports +1":     "",
		"Unmatched":             "",
	} {
		if got := h.titles(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	ps, _ := h.st.Programmes(ctx, h.chans["Example News"], now, now.Add(time.Hour))
	if len(ps) != 1 || ps[0].SubTitle != "Headlines" || ps[0].Description != "The day's news." || ps[0].Category != "News" {
		t.Errorf("programme %+v", ps)
	}
	src, _ := h.st.Source(ctx, h.id)
	if g := src.Guide; g.Channels != 3 || g.Programmes != 5 || g.Error != "" || !g.ReadAt.Equal(now) || g.Listed {
		t.Errorf("state %+v", g)
	}
	if strings.Contains(h.logs.String(), secret) {
		t.Errorf("the log shows the password: %s", h.logs)
	}

	// Not due again until the interval passes.
	if read, _, _ := h.g.ReadIfDue(ctx, h.settings(source.TypeXtream), h.id); read {
		t.Error("read again at once")
	}
	h.g.now = func() time.Time { return now.Add(DefaultInterval) }
	h.set(func(h *harness) { h.gz = true; h.body = strings.Replace(guideXML, "Evening News", "Late News", 1) })
	if read, _, err := h.g.ReadIfDue(ctx, h.settings(source.TypeXtream), h.id); !read || err != nil {
		t.Fatalf("read after the interval: %v, %v", read, err)
	}
	if got := h.titles("Example News"); got != "Early News 17:00-18:00, Late News 18:00-19:00" {
		t.Errorf("after a second read: %q", got)
	}
}

// A channel whose guide ID is missing, or not in the guide, takes the guide
// by its name, also from a guide channel that another channel's ID matched:
// the same channel in another quality, say. A channel that its ID matched
// keeps to that.
func TestNamesBesideGuideIDs(t *testing.T) {
	h := newHarness(t, source.TypeXtream)
	ctx := context.Background()
	channels := []store.CatalogChannel{
		{IdentityKey: "a", Name: "Example News", XMLTVID: "NEWS.example", StreamURL: "live/1.ts"},
		{IdentityKey: "b", Name: "UK: Example Sports HD", StreamURL: "live/2.ts"},
		{IdentityKey: "c", Name: "Film Four", XMLTVID: "film.example", StreamURL: "live/3.ts"},
		{IdentityKey: "d", Name: "Example Sports +1", StreamURL: "live/4.ts"},
		{IdentityKey: "e", Name: "Unmatched", XMLTVID: "nowhere", StreamURL: "live/5.ts"},
		{IdentityKey: "f", Name: "Example News FHD", StreamURL: "live/6.ts"},
		{IdentityKey: "g", Name: "UK: Film Four (backup)", XMLTVID: "film4.elsewhere", StreamURL: "live/7.ts"},
		{IdentityKey: "h", Name: "Example Sports", XMLTVID: "news.EXAMPLE", StreamURL: "live/8.ts"},
	}
	if _, err := h.st.ApplyCatalog(ctx, h.id, channels, now); err != nil {
		t.Fatal(err)
	}
	gcs, err := h.st.GuideChannels(ctx, h.id)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range gcs {
		h.chans[c.Name] = c.ID
	}
	_, res, err := h.g.ReadIfDue(ctx, h.settings(source.TypeXtream), h.id)
	if err != nil {
		t.Fatal(err)
	}
	if res.Channels != 6 || res.Programmes != 11 || res.Listed != 4 || res.ByName != 3 {
		t.Errorf("result %+v", res)
	}
	news := "Early News 17:00-18:00, Evening News 18:00-19:00"
	films := "First Film 18:00-20:00, Second Film 20:00-22:00"
	for name, want := range map[string]string{
		"Example News":           news,
		"Example News FHD":       news,
		"Example Sports":         news,
		"UK: Example Sports HD":  "Match 17:00-19:00",
		"Film Four":              films,
		"UK: Film Four (backup)": films,
		"Example Sports +1":      "",
		"Unmatched":              "",
	} {
		if got := h.titles(name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
	if logs := h.logs.String(); !strings.Contains(logs, "guide_channels=4 by_name=3") {
		t.Errorf("logs: %s", logs)
	}
}

// A guide that an older version matched is read again at the next refresh,
// so that a change to the matching applies at once.
func TestNewMatchingReadsAgain(t *testing.T) {
	h := newHarness(t, source.TypeXtream)
	ctx := context.Background()
	settings := h.settings(source.TypeXtream)
	w, err := h.st.BeginGuide(ctx, h.id)
	if err != nil {
		t.Fatal(err)
	}
	// What the first matching recorded: a hash of the address alone.
	if err := w.Commit(ctx, now, store.GuideAddressHash(Address(settings, ""))); err != nil {
		t.Fatal(err)
	}
	if read, _, err := h.g.ReadIfDue(ctx, settings, h.id); !read || err != nil {
		t.Fatalf("read %v, %v", read, err)
	}
	if read, _, _ := h.g.ReadIfDue(ctx, settings, h.id); read {
		t.Error("read again at once")
	}
}

func TestFailedReadKeepsTheGuide(t *testing.T) {
	h := newHarness(t, source.TypeXtream)
	ctx := context.Background()
	if _, err := h.g.Read(ctx, h.settings(source.TypeXtream), h.id); err != nil {
		t.Fatal(err)
	}
	for name, set := range map[string]func(h *harness){
		"status":    func(h *harness) { h.status = http.StatusForbidden },
		"html":      func(h *harness) { h.body = "<html><body>" + secret + "</body></html>" },
		"cut short": func(h *harness) { h.body = guideXML[:len(guideXML)/2] },
		"too large": func(h *harness) {},
	} {
		h.set(func(h *harness) { h.status, h.body, h.gz = http.StatusOK, guideXML, false })
		h.set(set)
		h.g.maxBytes = maxGuideBytes
		if name == "too large" {
			h.g.maxBytes = 1000
		}
		_, err := h.g.Read(ctx, h.settings(source.TypeXtream), h.id)
		if err == nil || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "xmltv.php") {
			t.Errorf("%s: %v", name, err)
		}
		if got := h.titles("Example News"); got != "Early News 17:00-18:00, Evening News 18:00-19:00" {
			t.Errorf("%s: the guide changed: %q", name, got)
		}
		src, _ := h.st.Source(ctx, h.id)
		if src.Guide.Error == "" || src.Guide.Programmes != 5 {
			t.Errorf("%s: state %+v", name, src.Guide)
		}
	}
	// A failed read is tried again after RetryAfter, not at once.
	h.g.maxBytes = maxGuideBytes
	h.set(func(h *harness) { h.status, h.body = http.StatusOK, guideXML })
	if read, _, _ := h.g.ReadIfDue(ctx, h.settings(source.TypeXtream), h.id); read {
		t.Error("a failed read tried again at once")
	}
	h.g.now = func() time.Time { return now.Add(RetryAfter) }
	if read, _, err := h.g.ReadIfDue(ctx, h.settings(source.TypeXtream), h.id); !read || err != nil {
		t.Errorf("retry: %v, %v", read, err)
	}
	if strings.Contains(h.logs.String(), secret) {
		t.Errorf("the log shows the password: %s", h.logs)
	}
}

func TestPlaylistGuide(t *testing.T) {
	h := newHarness(t, source.TypeM3U)
	ctx := context.Background()
	settings := h.settings(source.TypeM3U)
	if read, _, err := h.g.ReadIfDue(ctx, settings, h.id); read || err != nil {
		t.Errorf("a playlist without a guide: %v, %v", read, err)
	}
	if _, err := h.g.Read(ctx, settings, h.id); err == nil {
		t.Error("read a guide that the playlist does not name")
	}
	listed := h.srv.URL + "/guide.xml.gz?key=" + secret
	if err := h.st.SetListedGuide(ctx, h.id, listed); err != nil {
		t.Fatal(err)
	}
	if read, res, err := h.g.ReadIfDue(ctx, settings, h.id); !read || err != nil || res.Programmes != 5 {
		t.Fatalf("read %v, %+v, %v", read, res, err)
	}
	// A new address is read at once.
	if err := h.st.SetListedGuide(ctx, h.id, listed+"&v=2"); err != nil {
		t.Fatal(err)
	}
	if read, _, _ := h.g.ReadIfDue(ctx, settings, h.id); !read {
		t.Error("a new address was not read")
	}
	src, _ := h.st.Source(ctx, h.id)
	if !src.Guide.Listed {
		t.Errorf("state %+v", src.Guide)
	}
}

func TestDeletedChannelsTakeTheirProgrammes(t *testing.T) {
	h := newHarness(t, source.TypeXtream)
	ctx := context.Background()
	if _, err := h.g.Read(ctx, h.settings(source.TypeXtream), h.id); err != nil {
		t.Fatal(err)
	}
	if err := h.st.DeleteSource(ctx, h.id); err != nil {
		t.Fatal(err)
	}
	if ps, err := h.st.Programmes(ctx, h.chans["Example News"], now.Add(-time.Hour), now.Add(time.Hour)); err != nil || len(ps) != 0 {
		t.Errorf("programmes %+v, %v", ps, err)
	}
}

func TestAddress(t *testing.T) {
	x := source.Settings{Type: source.TypeXtream, URL: "http://provider.example:8080/", Username: "viewer", Password: "p&ss"}
	if got := Address(x, "ignored"); got != "http://provider.example:8080/xmltv.php?password=p%26ss&username=viewer" {
		t.Errorf("xtream %q", got)
	}
	if got := Address(source.Settings{Type: source.TypeM3U}, "http://g.example/a.xml"); got != "http://g.example/a.xml" {
		t.Errorf("m3u %q", got)
	}
	jf := source.Settings{Type: source.TypeJellyfin, URL: "http://jellyfin.example:8096", Password: "k"}
	if got := Address(jf, "http://g.example/left-from-a-playlist.xml"); got != "" {
		t.Errorf("jellyfin %q", got)
	}
	for header, want := range map[string]string{
		`url-tvg="http://g.example/a.xml.gz"`:           "http://g.example/a.xml.gz",
		`x-tvg-url="ftp://x, https://g.example/b.xml"`:  "https://g.example/b.xml",
		`url-tvg="" x-tvg-url="http://g.example/c.xml"`: "http://g.example/c.xml",
		`url-tvg="guide.xml"`:                           "",
		`tvg-shift="1"`:                                 "",
	} {
		attrs := map[string]string{}
		for _, kv := range strings.Fields(strings.ReplaceAll(header, ", ", ",")) {
			k, v, _ := strings.Cut(kv, "=")
			attrs[k] = strings.ReplaceAll(strings.Trim(v, `"`), ",", ", ")
		}
		if got := ListedGuide(attrs); got != want {
			t.Errorf("%s: %q, want %q", header, got, want)
		}
	}
}

func TestNameKey(t *testing.T) {
	for in, want := range map[string]string{
		"UK: BBC One HD":          "bbcone",
		"BBC One":                 "bbcone",
		"de | Das Erste FHD":      "daserste",
		"Channel 4 +1":            "channel4+1",
		"Channel 4":               "channel4",
		"[VIP] Sports (backup)":   "sports",
		"HD":                      "hd",
		"Movies 24/7 1080p 50fps": "movies247",
		"Ünïcödé Kanal":           "ünïcödékanal",
		"1: Number First":         "1numberfirst",
	} {
		if got := NameKey(in); got != want {
			t.Errorf("NameKey(%q) = %q, want %q", in, got, want)
		}
	}
}

// A Stalker portal's guide comes through its API, by the portal's own
// channel IDs, with no matching by guide ID or name (D76).
func TestStalkerGuide(t *testing.T) {
	p := stalkertest.New(t)
	p.Now = func() time.Time { return now }
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	logs := &bytes.Buffer{}
	g := New(st, slog.New(slog.NewTextHandler(logs, nil)), "test")
	g.now = func() time.Time { return now }
	pool := stalker.NewPool(nil)
	g.UseStalker(pool)
	settings := source.Settings{Name: "main", Type: source.TypeStalker, URL: p.URL + "/stalker_portal/c/",
		Device: source.Device{MAC: stalkertest.MAC}, Refresh: time.Hour, Numbers: source.NumbersPlaylist}
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, id, []store.CatalogChannel{
		{IdentityKey: "stalker:101", Name: "Example News", StreamURL: "ffrt http://localhost/ch/101_"},
		// A guide ID that another guide would match by is no matter.
		{IdentityKey: "stalker:102", Name: "Example Sports", XMLTVID: "news.example", StreamURL: "ffrt http://localhost/ch/102_"},
		{IdentityKey: "stalker:103", Name: "Example Weather", StreamURL: "ffmpeg http://localhost/ch/103_"},
		{IdentityKey: "stalker:104", Name: "Example News", StreamURL: "ffrt http://localhost/ch/104_"},
	}, now); err != nil {
		t.Fatal(err)
	}
	channels, err := st.GuideChannels(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, c := range channels {
		ids[c.IdentityKey] = c.ID
	}
	count := func(key string) int {
		t.Helper()
		ps, err := st.Programmes(ctx, ids[key], now.Add(-30*24*time.Hour), now.Add(60*24*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return len(ps)
	}

	read, res, err := g.ReadIfDue(ctx, settings, id)
	if err != nil || !read {
		t.Fatalf("read %v, %v", read, err)
	}
	// From the hour before now, over StalkerHours and one more: hourly
	// news and two-hourly sports.
	news, sports := StalkerHours+1, (StalkerHours+2)/2
	if res.Channels != 2 || res.Programmes != news+sports || res.Listed != 2 || res.ByName != 0 {
		t.Errorf("result %+v", res)
	}
	for key, want := range map[string]int{"stalker:101": news, "stalker:102": sports, "stalker:103": 0, "stalker:104": 0} {
		if got := count(key); got != want {
			t.Errorf("%s: %d programmes, want %d", key, got, want)
		}
	}
	ps, _ := st.Programmes(ctx, ids["stalker:101"], now, now.Add(time.Minute))
	if len(ps) != 1 || ps[0].Title != "News 1" || ps[0].Description != "A fictional programme." || ps[0].Category != "News" ||
		!ps[0].Start.Equal(time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)) {
		t.Errorf("programme now %+v", ps)
	}

	// It is read in the source's session, and again after StalkerInterval.
	c, err := pool.Client(id, stalker.FromSettings(settings))
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile().ID == "" {
		t.Error("the pool's client did not read the guide")
	}
	g.now = func() time.Time { return now.Add(StalkerInterval - time.Minute) }
	if read, _, _ := g.ReadIfDue(ctx, settings, id); read {
		t.Error("read again before the interval")
	}
	g.now = func() time.Time { return now.Add(StalkerInterval) }
	if read, _, err := g.ReadIfDue(ctx, settings, id); !read || err != nil {
		t.Errorf("read after the interval: %v, %v", read, err)
	}
	if n := p.Calls("itv/get_epg_info"); n != 2 {
		t.Errorf("%d guide calls, want 2", n)
	}
	if n := p.Calls("stb/handshake"); n != 1 {
		t.Errorf("%d handshakes, want 1", n)
	}
	if strings.Contains(logs.String(), stalkertest.MAC) {
		t.Errorf("the log shows the MAC address:\n%s", logs)
	}

	// Another box's guide is another guide.
	other := settings
	other.Device.MAC = "00:1A:79:00:00:52"
	if Address(other, "") == Address(settings, "") {
		t.Error("two boxes share a guide address")
	}
}
