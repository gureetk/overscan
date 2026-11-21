// SPDX-License-Identifier: AGPL-3.0-or-later

package m3u

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

type harness struct {
	t     *testing.T
	store *store.Store
	hub   *hub.Hub
	out   *Output
	front *httptest.Server
	now   time.Time
	token string
	logs  bytes.Buffer
	ids   map[string]int64 // entry IDs by channel name
}

// newHarness puts fictional channels in the household's lineup, with a
// guide, turns the outputs on, and gives the household's account a token
// and its playlist.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, now: time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC), ids: map[string]int64{}}
	log := slog.New(slog.NewTextHandler(&h.logs, nil))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.store = st

	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		rc := http.NewResponseController(w)
		for {
			if _, err := w.Write(bytes.Repeat([]byte{0x47, 0x1f, 0xff, 0x10}, 47)); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}))
	t.Cleanup(up.Close)

	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	channels := []store.CatalogChannel{
		{IdentityKey: "a", Name: `Alpha "One"`, Category: "News", XMLTVID: "alpha.example", Logo: "http://logos.example/a.png", StreamURL: up.URL + "/a.ts"},
		{IdentityKey: "b", Name: "Bravo & Co", Category: "Sport", StreamURL: up.URL + "/b.ts",
			ArchiveDays: 2, Catchup: catchup.Playlist{Mode: catchup.ModeDefault, Source: up.URL + "/archive?from={utc}"}},
		{IdentityKey: "r", Name: "Radio <Three>", Category: "Radio", StreamURL: up.URL + "/r.ts", Radio: true},
		{IdentityKey: "x", Name: "Adults", Category: "Late", StreamURL: up.URL + "/x.ts", Adult: true},
	}
	if _, err := st.ApplyCatalog(ctx, srcID, channels, h.now); err != nil {
		t.Fatal(err)
	}
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	channelIDs := map[string]int64{}
	for _, c := range lineup {
		h.ids[c.Name] = c.EntryID
		channelIDs[c.Name] = c.ChannelID
	}
	gw, err := st.BeginGuide(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	start := h.now.Truncate(time.Hour).Add(-30 * time.Hour)
	for i := 0; i < 40; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		for key, name := range map[string]string{"a": `Alpha "One"`, "b": "Bravo & Co"} {
			if err := gw.Add(ctx, store.GuideProgramme{ChannelID: channelIDs[name], Start: at, Stop: at.Add(time.Hour),
				Title: fmt.Sprintf("%s & programme %d", key, i), Description: "A made-up programme."}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := gw.Commit(ctx, h.now, "test"); err != nil {
		t.Fatal(err)
	}

	h.hub = hub.New(st, log, "test", map[int64]hub.Source{srcID: {Name: "main"}})
	logins := admin.NewLogins(st)
	h.out = New(st, h.hub, logins, log)
	h.out.now = func() time.Time { return h.now }
	mux := routes.NewMux()
	h.out.Register(mux)
	mux.Handle("GET /live/", h.hub)
	mux.Handle("GET /archive/", h.hub)
	h.front = httptest.NewServer(mux)
	t.Cleanup(h.front.Close)

	if err := st.SetOutputs(ctx, true); err != nil {
		t.Fatal(err)
	}
	if h.token, err = st.NewAccountToken(ctx, store.HouseholdAccount); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{M3U: true}); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) get(path string) (*http.Response, string) {
	h.t.Helper()
	resp, err := http.Get(h.front.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestPlaylist(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get("/playlist/" + h.token + ".m3u")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "audio/x-mpegurl") {
		t.Fatalf("status %s, %s", resp.Status, resp.Header.Get("Content-Type"))
	}
	base := h.front.URL
	a, b, r := h.ids[`Alpha "One"`], h.ids["Bravo & Co"], h.ids["Radio <Three>"]
	want := []string{
		fmt.Sprintf(`#EXTM3U url-tvg="%s/epg/%s.xml.gz" x-tvg-url="%[1]s/epg/%[2]s.xml.gz"`, base, h.token),
		fmt.Sprintf(`#EXTINF:-1 tvg-id="%d" tvg-name="Alpha 'One'" tvg-chno="1" tvg-logo="%s/artwork/logo/%d/`, a, base, a),
		`group-title="News",Alpha "One"`,
		fmt.Sprintf("%s/playlist/%s/%d.ts", base, h.token, a),
		fmt.Sprintf(`#EXTINF:-1 tvg-id="%d" tvg-name="Bravo & Co" tvg-chno="2" group-title="Sport" catchup="default" catchup-source="%s/playlist/%s/archive/%d.ts?start={utc}&end={utcend}" catchup-days="2",Bravo & Co`, b, base, h.token, b),
		fmt.Sprintf(`#EXTINF:-1 tvg-id="%d" tvg-name="Radio <Three>" tvg-chno="3" group-title="Radio" radio="true",Radio <Three>`, r),
	}
	for _, w := range want {
		if !strings.Contains(body, w) {
			t.Errorf("the playlist lacks %q:\n%s", w, body)
		}
	}
	// Nothing of the source's own addresses.
	if strings.Contains(body, "127.0.0.1:") && strings.Count(body, h.front.URL) != strings.Count(body, "127.0.0.1:") {
		t.Errorf("the playlist names another server:\n%s", body)
	}
	if strings.Contains(body, "logos.example") || strings.Contains(body, "/a.ts\n") {
		t.Errorf("the playlist has a source's address:\n%s", body)
	}
	// The adult channel is locked by default, which does nothing without a
	// PIN: it is listed. With a PIN, it is left out.
	if !strings.Contains(body, "Adults") {
		t.Error("the adult channel is missing without a PIN")
	}
	if err := h.store.SetAccountPIN(context.Background(), store.HouseholdAccount, "hash"); err != nil {
		t.Fatal(err)
	}
	if _, body := h.get("/playlist/" + h.token + ".m3u"); strings.Contains(body, "Adults") {
		t.Error("a locked channel is listed while the account has a PIN")
	}
	if !strings.Contains(h.logs.String(), "a player read its playlist") || strings.Contains(h.logs.String(), h.token) {
		t.Errorf("the log: %s", h.logs.String())
	}
}

func TestPlaylistRefusals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	path := "/playlist/" + h.token + ".m3u"
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/playlist/" + h.token + ".txt", http.StatusNotFound},
		{"/playlist/.m3u", http.StatusNotFound},
		{"/playlist/not-a-token.m3u", http.StatusNotFound},
		{"/epg/not-a-token.xml.gz", http.StatusNotFound},
		{"/playlist/not-a-token/1.ts", http.StatusNotFound},
		{"/playlist/" + h.token + "/x.ts", http.StatusNotFound},
		{"/playlist/" + h.token + "/01.ts", http.StatusNotFound},
		{"/playlist/" + h.token + "/999.ts", http.StatusNotFound},
	} {
		if resp, _ := h.get(tc.path); resp.StatusCode != tc.status {
			t.Errorf("%s: %s, want %d", tc.path, resp.Status, tc.status)
		}
	}
	// The account's own switch.
	if err := h.store.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{}); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.get(path); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "may not use playlists") {
		t.Errorf("with the account's playlist off: %s %q", resp.Status, body)
	}
	if err := h.store.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{M3U: true}); err != nil {
		t.Fatal(err)
	}
	// An account that has ended.
	if err := h.store.SetAccountExpiry(ctx, store.HouseholdAccount, h.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.get(path); resp.StatusCode != http.StatusForbidden || !strings.Contains(body, "has ended") {
		t.Errorf("an ended account: %s %q", resp.Status, body)
	}
	if err := h.store.SetAccountExpiry(ctx, store.HouseholdAccount, time.Time{}); err != nil {
		t.Fatal(err)
	}
	// A new token stops the old one.
	old := h.token
	if _, err := h.store.NewAccountToken(ctx, store.HouseholdAccount); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.get("/playlist/" + old + ".m3u"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an old token: %s", resp.Status)
	}
	// With the outputs off, nothing is there.
	if err := h.store.SetOutputs(ctx, false); err != nil {
		t.Fatal(err)
	}
	if resp, _ := h.get(path); resp.StatusCode != http.StatusNotFound {
		t.Errorf("with the outputs off: %s", resp.Status)
	}
}

// Players from outside the server's own networks are refused, unless the
// outputs answer anywhere.
func TestPlaylistOwnNetworks(t *testing.T) {
	h := newHarness(t)
	mux := routes.NewMux()
	h.out.Register(mux)
	ask := func(from string) int {
		r := httptest.NewRequest("GET", "/playlist/"+h.token+".m3u", nil)
		r.RemoteAddr = from
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}
	for addr, want := range map[string]int{
		"192.0.2.10:5000":        http.StatusForbidden, // documentation space, not private
		"203.0.113.7:5000":       http.StatusForbidden,
		"192.168.1.20:5000":      http.StatusOK,
		"10.1.2.3:5000":          http.StatusOK,
		"172.16.0.9:5000":        http.StatusOK,
		"100.100.1.2:5000":       http.StatusOK,
		"[fd12:3456::7]:5000":    http.StatusOK,
		"[2001:db8::7]:5000":     http.StatusForbidden,
		"[::ffff:10.0.0.5]:5000": http.StatusOK,
		"127.0.0.1:5000":         http.StatusOK,
	} {
		if got := ask(addr); got != want {
			t.Errorf("from %s: %d, want %d", addr, got, want)
		}
	}
	if err := h.store.SetOutputsAnywhere(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := ask("203.0.113.7:5000"); got != http.StatusOK {
		t.Errorf("anywhere, from outside: %d", got)
	}
}

func TestGuide(t *testing.T) {
	h := newHarness(t)
	resp, body := h.get("/epg/" + h.token + ".xml.gz")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/gzip" {
		t.Fatalf("status %s, %s", resp.Status, resp.Header.Get("Content-Type"))
	}
	zr, err := gzip.NewReader(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	var tv struct {
		Channels []struct {
			ID   string `xml:"id,attr"`
			Name string `xml:"display-name"`
			Icon *struct {
				Src string `xml:"src,attr"`
			} `xml:"icon"`
		} `xml:"channel"`
		Programmes []struct {
			Start   string `xml:"start,attr"`
			Stop    string `xml:"stop,attr"`
			Channel string `xml:"channel,attr"`
			Title   string `xml:"title"`
		} `xml:"programme"`
	}
	if err := xml.Unmarshal(plain, &tv); err != nil {
		t.Fatalf("the guide is not XML: %v\n%s", err, plain)
	}
	a, b := fmt.Sprint(h.ids[`Alpha "One"`]), fmt.Sprint(h.ids["Bravo & Co"])
	if len(tv.Channels) != 4 || tv.Channels[0].ID != a || tv.Channels[0].Name != `Alpha "One"` || tv.Channels[0].Icon == nil ||
		!strings.HasPrefix(tv.Channels[0].Icon.Src, h.front.URL+"/artwork/logo/") {
		t.Errorf("channels: %+v", tv.Channels)
	}
	counts := map[string]int{}
	for _, p := range tv.Programmes {
		counts[p.Channel]++
		if !strings.HasSuffix(p.Start, " +0000") || !strings.HasSuffix(p.Stop, " +0000") {
			t.Errorf("a time not in UTC: %+v", p)
		}
	}
	// Alpha goes back a day; Bravo, with two days of archive, as far back
	// as the guide has: 30 hours before now.
	if counts[a] != 34 || counts[b] != 40 {
		t.Errorf("programmes by channel: %v", counts)
	}
	first := tv.Programmes[0]
	if first.Title != "a & programme 6" || first.Start != XMLTVTime(h.now.Truncate(time.Hour).Add(-24*time.Hour)) {
		t.Errorf("first programme: %+v", first)
	}
	// The plain form.
	resp, body = h.get("/epg/" + h.token + ".xml")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(body, "<?xml") || !strings.Contains(body, "a &amp; programme 6") {
		t.Errorf("plain guide: %s %.200q", resp.Status, body)
	}
}

func TestLiveAndArchive(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.store.SetAccountStreams(ctx, store.HouseholdAccount, 1); err != nil {
		t.Fatal(err)
	}
	a, b := h.ids[`Alpha "One"`], h.ids["Bravo & Co"]
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/playlist/%s/%d.ts", h.front.URL, h.token, a), nil)
	req.Header.Set("User-Agent", "Kodi/21.1 (fictional)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 188*2)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || resp.Header.Get("Content-Type") != "video/mp2t" || buf[0] != 0x47 {
		t.Fatalf("live: %s %s %v %x", resp.Status, resp.Header.Get("Content-Type"), err, buf[:4])
	}
	streams := h.hub.Streams()
	if len(streams) != 1 || streams[0].AccountID != store.HouseholdAccount || !hub.IsPlayer(streams[0].DeviceID) ||
		!strings.HasPrefix(streams[0].Player, "Kodi/21.1 at 127.0.0.1") {
		t.Errorf("streams: %+v", streams)
	}
	// Another player of the account is refused at its limit of one.
	other, _ := http.NewRequest("GET", fmt.Sprintf("%s/playlist/%s/%d.ts", h.front.URL, h.token, b), nil)
	other.Header.Set("User-Agent", "VLC/3.0 (fictional)")
	resp2, err := http.DefaultClient.Do(other)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(text), "account") {
		t.Errorf("the second player: %s %q", resp2.Status, text)
	}
	resp.Body.Close()
	if err := h.store.SetAccountStreams(ctx, store.HouseholdAccount, 0); err != nil {
		t.Fatal(err)
	}

	// A past programme of Bravo, within its archive.
	start := h.now.Truncate(time.Hour).Add(-5 * time.Hour)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{fmt.Sprintf("start=%d&end=%d", start.Unix(), start.Add(time.Hour).Unix()), http.StatusOK},
		{fmt.Sprintf("start=%d", h.now.Add(time.Hour).Unix()), http.StatusBadRequest},
		{fmt.Sprintf("start=%d", h.now.Add(-72*time.Hour).Unix()), http.StatusBadRequest},
		{"start=soon", http.StatusBadRequest},
	} {
		r, err := http.Get(fmt.Sprintf("%s/playlist/%s/archive/%d.ts?%s", h.front.URL, h.token, b, tc.query))
		if err != nil {
			t.Fatal(err)
		}
		if r.StatusCode != tc.status {
			t.Errorf("archive %s: %s, want %d", tc.query, r.Status, tc.status)
		}
		r.Body.Close()
	}
	// A channel without an archive has none.
	if r, _ := h.get(fmt.Sprintf("/playlist/%s/archive/%d.ts?start=%d", h.token, a, start.Unix())); r.StatusCode != http.StatusNotFound {
		t.Errorf("archive of a channel without one: %s", r.Status)
	}
}

func TestArchiveSpan(t *testing.T) {
	now := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	s := now.Add(-2 * time.Hour).Unix()
	if start, stop, ok := ArchiveSpan(fmt.Sprint(s), fmt.Sprint(s+1800), 1, now); !ok || start.Unix() != s || stop.Unix() != s+1800 {
		t.Errorf("a half-hour programme: %v %v %v", start, stop, ok)
	}
	if _, stop, ok := ArchiveSpan(fmt.Sprint(s), "", 1, now); !ok || stop.Unix() != s+12*3600 {
		t.Errorf("no end: %v %v", stop, ok)
	}
	if _, stop, ok := ArchiveSpan(fmt.Sprint(s), fmt.Sprint(s+13*3600), 1, now); !ok || stop.Unix() != s+12*3600 {
		t.Errorf("too long an end: %v %v", stop, ok)
	}
	for _, bad := range [][2]string{{"", ""}, {fmt.Sprint(now.Unix()), ""}, {fmt.Sprint(now.Add(-25 * time.Hour).Unix()), ""}, {"-1", ""}} {
		if _, _, ok := ArchiveSpan(bad[0], bad[1], 1, now); ok {
			t.Errorf("ArchiveSpan(%q, %q) accepted", bad[0], bad[1])
		}
	}
	if _, _, ok := ArchiveSpan(fmt.Sprint(s), "", 0, now); ok {
		t.Error("a channel without an archive")
	}
}

func FuzzEntryFile(f *testing.F) {
	for _, s := range []string{"1.ts", "123.ts", "01.ts", ".ts", "x.ts", "-1.ts", "99999999999999999999.ts", "1.m3u8", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, file string) {
		id, ok := entryFile(file)
		if ok && (id <= 0 || fmt.Sprintf("%d.ts", id) != file) {
			t.Errorf("entryFile(%q) = %d, true", file, id)
		}
	})
}

func FuzzArchiveSpan(f *testing.F) {
	f.Add("1796378400", "1796382000", 2)
	f.Add("", "", 0)
	f.Add("x", "1", 1)
	now := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	f.Fuzz(func(t *testing.T, start, end string, days int) {
		s, e, ok := ArchiveSpan(start, end, days, now)
		if !ok {
			return
		}
		if !s.Before(now) || !e.After(s) || e.Sub(s) > longestArchive {
			t.Errorf("ArchiveSpan(%q, %q, %d) = %v, %v", start, end, days, s, e)
		}
	})
}
