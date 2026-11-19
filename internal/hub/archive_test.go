// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// archiveUpstream is a fictional provider with an archive: /tv/<n> is a
// channel's live stream, and with utc and d, its archive from utc for d
// seconds, as a short MPEG-TS stream or, for /tv/hls, an HLS playlist that
// has ended; /timeshift/... is an Xtream server's archive. It keeps the
// requests it had.
type archiveUpstream struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string
	// hold makes an archive stream run until its request ends.
	hold bool
}

func newArchiveUpstream(t *testing.T) *archiveUpstream {
	t.Helper()
	u := &archiveUpstream{}
	mux := http.NewServeMux()
	stream := func(w http.ResponseWriter, r *http.Request, packets int) {
		w.Header().Set("Content-Type", "video/mp2t")
		packet := bytes.Repeat([]byte{0x47}, 188)
		rc := http.NewResponseController(w)
		for i := 0; packets < 0 || i < packets; i++ {
			if _, err := w.Write(packet); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
	}
	mux.HandleFunc("/tv/{n}", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests = append(u.requests, r.URL.RequestURI())
		hold := u.hold
		u.mu.Unlock()
		if r.URL.Query().Get("utc") == "" {
			stream(w, r, -1)
			return
		}
		if r.PathValue("n") == "hls" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-PLAYLIST-TYPE:VOD\n"+
				"#EXTINF:4.0,\nseg/1.ts\n#EXTINF:4.0,\nseg/2.ts\n#EXT-X-ENDLIST\n")
			return
		}
		if hold {
			stream(w, r, -1)
			return
		}
		stream(w, r, 20)
	})
	mux.HandleFunc("/tv/seg/{n}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(bytes.Repeat([]byte{0x47}, 188*5))
	})
	mux.HandleFunc("/timeshift/", func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests = append(u.requests, r.URL.RequestURI())
		u.mu.Unlock()
		stream(w, r, 20)
	})
	u.Server = httptest.NewServer(mux)
	t.Cleanup(u.Close)
	return u
}

func (u *archiveUpstream) seen() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.requests...)
}

// archiveHarness is a hub with a playlist source whose channels keep an
// archive, and an Xtream source with one channel, both from up.
type archiveHarness struct {
	t      *testing.T
	hub    *Hub
	store  *store.Store
	front  *httptest.Server
	up     *archiveUpstream
	logs   *syncBuffer
	byName map[string]store.ArchiveEntry
	plain  store.PlayableEntry // a channel without an archive
}

// airedAt is when the archive's programme began: 4 December 2026, 20:00 UTC.
var airedAt = time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)

func newArchiveHarness(t *testing.T, limit int) *archiveHarness {
	t.Helper()
	ctx := context.Background()
	h := &archiveHarness{t: t, up: newArchiveUpstream(t), logs: &syncBuffer{}, byName: map[string]store.ArchiveEntry{}}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.store = st
	one, err := st.AddSource(ctx, source.Settings{Name: "one", URL: "http://provider.example/one.m3u", MaxConnections: limit})
	if err != nil {
		t.Fatal(err)
	}
	two, err := st.AddSource(ctx, source.Settings{Name: "two", Type: source.TypeXtream, URL: h.up.URL, Username: "viewer",
		Password: secret})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetSourceAccount(ctx, two, store.SourceAccount{Status: "Active", TimeZone: "Europe/Berlin"}); err != nil {
		t.Fatal(err)
	}
	append_ := catchup.Playlist{Mode: catchup.ModeAppend, Source: "?utc={utc}&d={duration}&key=" + secret}
	if _, err := st.ApplyCatalog(ctx, one, []store.CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: h.up.URL + "/tv/1", ArchiveDays: 2, Catchup: append_},
		{IdentityKey: "h", Name: "Hotel", StreamURL: h.up.URL + "/tv/hls", ArchiveDays: 2, Catchup: append_},
		{IdentityKey: "p", Name: "Papa", StreamURL: h.up.URL + "/tv/2"},
	}, airedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, two, []store.CatalogChannel{
		{IdentityKey: "xtream:9", Name: "Xray", StreamURL: "live/9.ts", ArchiveDays: 1},
	}, airedAt); err != nil {
		t.Fatal(err)
	}
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range lineup {
		if c.Name == "Papa" {
			if h.plain, err = st.PlayableEntry(ctx, c.EntryID); err != nil {
				t.Fatal(err)
			}
			continue
		}
		a, err := st.PlayableArchive(ctx, c.EntryID)
		if err != nil {
			t.Fatal(c.Name, err)
		}
		h.byName[c.Name] = a
	}
	log := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.hub = New(st, log, "test", map[int64]Source{one: {Name: "one", MaxConnections: limit}, two: {Name: "two"}})
	clock := airedAt.Add(26 * time.Hour)
	h.hub.now = func() time.Time { return clock }
	h.front = httptest.NewServer(h.hub)
	t.Cleanup(h.front.Close)
	return h
}

// link returns a link to the past programme of name that began at start
// and lasted length, and the address that says where its stream starts.
func (h *archiveHarness) link(device int64, name string, start time.Time, length time.Duration) (string, string) {
	h.t.Helper()
	a, ok := h.byName[name]
	if !ok {
		h.t.Fatalf("no channel %q", name)
	}
	link, startPath := h.hub.ArchiveLink(device, a.PlayableEntry, start, start.Add(length))
	return h.front.URL + link, h.front.URL + startPath
}

func TestArchivePlays(t *testing.T) {
	h := newArchiveHarness(t, 1)
	link, _ := h.link(7, "Alpha", airedAt, time.Hour)
	if strings.Contains(link, secret) || !strings.Contains(link, "/archive/") || !strings.HasSuffix(link, ".ts") {
		t.Errorf("link %s", link)
	}
	resp := get(t, link)
	got := body(t, resp)
	if resp.StatusCode != http.StatusOK || len(got) != 188*20 || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("%s, %d bytes, %s", resp.Status, len(got), resp.Header.Get("Content-Type"))
	}
	seen := h.up.seen()
	if len(seen) != 1 || seen[0] != "/tv/1?utc=1796414400&d=3600&key="+secret {
		t.Errorf("upstream saw %q", seen)
	}
	// Seeking asks again from the whole minute at or before the time.
	if resp := get(t, link+"?t=90.5"); resp.StatusCode != http.StatusOK {
		t.Fatalf("seek: %s", resp.Status)
	} else {
		body(t, resp)
	}
	if seen := h.up.seen(); len(seen) != 2 || seen[1] != "/tv/1?utc=1796414460&d=3540&key="+secret {
		t.Errorf("after a seek, upstream saw %q", seen)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, "a past programme plays from its source's archive") || strings.Contains(logs, secret) {
		t.Errorf("logs: %s", logs)
	}
	streams := h.hub.Streams()
	if len(streams) != 1 || streams[0].Kind != PlaysArchive || streams[0].Channel != "Alpha" || !streams[0].Aired.Equal(airedAt) ||
		streams[0].EntryID != h.byName["Alpha"].EntryID {
		t.Errorf("streams %+v", streams)
	}
}

func TestArchiveStart(t *testing.T) {
	h := newArchiveHarness(t, 1)
	_, start := h.link(7, "Alpha", airedAt, time.Hour)
	_, late := h.link(7, "Alpha", airedAt.Add(30*time.Second), time.Hour)
	for _, tt := range []struct {
		url  string
		want int64
	}{
		{start, 0},
		{start + "?t=90", 60},
		{start + "?t=59.9", 0},
		{late, -30},
		{late + "?t=45", 30},
		{late + "?t=95", 90},
	} {
		resp := get(t, tt.url)
		var out struct{ T int64 }
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusOK || out.T != tt.want {
			t.Errorf("%s: %s %d, %v; want %d", tt.url, resp.Status, out.T, err, tt.want)
		}
		resp.Body.Close()
	}
	for _, bad := range []string{"?t=3600", "?t=-1", "?t=x", "?t=NaN"} {
		if resp := get(t, start+bad); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %s", bad, resp.Status)
		}
	}
}

func TestArchiveSeekEndsTheRequestBefore(t *testing.T) {
	h := newArchiveHarness(t, 1)
	h.up.mu.Lock()
	h.up.hold = true
	h.up.mu.Unlock()
	link, _ := h.link(7, "Alpha", airedAt, time.Hour)
	first := get(t, link)
	if _, err := io.ReadFull(first.Body, make([]byte, 188*4)); err != nil {
		t.Fatal(err)
	}
	second := get(t, link+"?t=600")
	if _, err := io.ReadFull(second.Body, make([]byte, 188*4)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, first.Body)
		done <- err
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request before the seek goes on")
	}
	first.Body.Close()
	second.Body.Close()
}

func TestArchiveRefusals(t *testing.T) {
	h := newArchiveHarness(t, 1)
	link, start := h.link(7, "Alpha", airedAt, time.Hour)
	token := strings.TrimSuffix(strings.TrimPrefix(link, h.front.URL+"/archive/"), ".ts")
	for _, path := range []string{"/live/" + token + ".ts", "/vod/" + token + ".ts", "/archive/" + token + "/other",
		"/archive/garbled.ts", "/archive/" + strings.TrimPrefix(h.hub.Link(7, h.plain), "/live/")} {
		if resp := get(t, h.front.URL+path); resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s: %s", path, resp.Status)
		}
	}
	// A channel whose source keeps no archive of it.
	papa, _ := h.hub.ArchiveLink(7, h.plain, airedAt, airedAt.Add(time.Hour))
	if resp := get(t, h.front.URL+papa); resp.StatusCode != http.StatusNotFound {
		t.Errorf("without an archive: %s", resp.Status)
	}
	// A link that has expired starts nothing; the start of a stream is
	// still said.
	clock := h.hub.now().Add(2 * linkTTL)
	h.hub.now = func() time.Time { return clock }
	if resp := get(t, link); resp.StatusCode != http.StatusForbidden {
		t.Errorf("expired: %s", resp.Status)
	}
	if resp := get(t, start); resp.StatusCode != http.StatusOK {
		t.Errorf("expired start: %s", resp.Status)
	}
}

func TestArchiveKeepsToTheLimit(t *testing.T) {
	h := newArchiveHarness(t, 1)
	// Another device plays a channel of the same source.
	live := get(t, h.front.URL+h.hub.Link(8, h.plain))
	if _, err := io.ReadFull(live.Body, make([]byte, 188*4)); err != nil {
		t.Fatal(err)
	}
	defer live.Body.Close()
	link, _ := h.link(7, "Alpha", airedAt, time.Hour)
	if resp := get(t, link); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("at the limit: %s", resp.Status)
	}
	if h.hub.Available(h.byName["Alpha"].SourceID, 7) {
		t.Error("the source is said to be free")
	}
	// The same device's own stream makes room.
	link, _ = h.link(8, "Alpha", airedAt, time.Hour)
	resp := get(t, link)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("the device's own: %s", resp.Status)
	}
	body(t, resp)
}

func TestArchiveRelaysHLS(t *testing.T) {
	h := newArchiveHarness(t, 1)
	link, _ := h.link(7, "Hotel", airedAt, time.Hour)
	resp := get(t, link)
	got := body(t, resp)
	if resp.StatusCode != http.StatusOK || len(got) != 2*188*5 {
		t.Errorf("%s, %d bytes", resp.Status, len(got))
	}
}

func TestArchiveOfAnXtreamServer(t *testing.T) {
	h := newArchiveHarness(t, 1)
	link, _ := h.link(7, "Xray", airedAt.Add(20*time.Second), 45*time.Minute)
	resp := get(t, link)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", resp.Status, body(t, resp))
	}
	body(t, resp)
	// In the server's zone, an hour ahead of UTC in December, from the
	// minute the programme began in, for 46 minutes.
	want := "/timeshift/viewer/" + url.PathEscape(secret) + "/46/2026-12-04:21-00/9.ts"
	if seen := h.up.seen(); len(seen) != 1 || seen[0] != want {
		t.Errorf("upstream saw %q, want %s", seen, want)
	}
}
