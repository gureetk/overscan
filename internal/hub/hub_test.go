// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

const secret = "s3cret"

// upstream is a fictional provider: an endless TS stream, an HLS master
// playlist with a variant, segments and a key, and a playlist served as
// plain text, alone and as the variant of a master playlist.
func upstream(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/live/{n}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		packet := bytes.Repeat([]byte{0x47}, 188)
		rc := http.NewResponseController(w)
		for {
			if _, err := w.Write(packet); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("/restarting", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(restarting())
	})
	mux.HandleFunc("/short", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(bytes.Repeat([]byte{0x47}, 188*10))
	})
	mux.HandleFunc("/hls/master.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nv1/index.m3u8?auth="+secret+"\n")
	})
	mux.HandleFunc("/hls/v1/index.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-mpegURL")
		io.WriteString(w, "#EXTM3U\r\n#EXT-X-TARGETDURATION:4\r\n"+
			"#EXT-X-KEY:METHOD=AES-128,URI=\"key.bin?auth="+secret+"\"\r\n"+
			"#EXTINF:4.0,\r\nseg1.ts?auth="+secret+"\r\n#EXTINF:4.0,\r\n/hls/v1/seg2.ts\r\n"+
			"#EXTINF:4.0,\r\nftp://elsewhere.example/seg3.ts\r\n")
	})
	mux.HandleFunc("/hls/v1/seg1.ts", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "SEGMENT1") })
	mux.HandleFunc("/hls/v1/key.bin", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "KEY") })
	mux.HandleFunc("/hls/text.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		io.WriteString(w, "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\n/sniffed\n")
	})
	mux.HandleFunc("/sniffed", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "#EXTM3U\n#EXTINF:4.0,\nhls/v1/seg1.ts\n")
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type harness struct {
	t       *testing.T
	hub     *Hub
	front   *httptest.Server
	entries map[string]store.PlayableEntry
	logs    *syncBuffer
	clock   atomic.Int64 // Unix nanoseconds
}

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

// newHarness puts fictional channels, one per path of the upstream fixture,
// in a lineup, all from one source with the given connection limit.
func newHarness(t *testing.T, limit int) *harness {
	t.Helper()
	paths := []string{"/live/1?u=user&p=" + secret, "/live/2?u=user&p=" + secret, "/hls/master.m3u8", "/hls/text.m3u8", "/sniffed", "/missing", "/short", "/restarting"}
	return newHarnessWith(t, limit, upstream(t).URL, paths)
}

// newHarnessWith puts fictional channels, one per path under base, in a
// lineup, all from one source with the given connection limit. A channel is
// named after its path, without the first slash and the query.
func newHarnessWith(t *testing.T, limit int, base string, paths []string) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, logs: &syncBuffer{}, entries: map[string]store.PlayableEntry{}}
	log := slog.New(slog.NewTextHandler(h.logs, nil))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: limit})
	if err != nil {
		t.Fatal(err)
	}
	var channels []store.CatalogChannel
	for _, p := range paths {
		name := strings.SplitN(strings.TrimPrefix(p, "/"), "?", 2)[0]
		channels = append(channels, store.CatalogChannel{IdentityKey: p, Name: name, StreamURL: base + p})
	}
	if _, err := st.ApplyCatalog(ctx, srcID, channels, time.Now()); err != nil {
		t.Fatal(err)
	}
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range lineup {
		e, err := st.PlayableEntry(ctx, c.EntryID)
		if err != nil {
			t.Fatal(err)
		}
		h.entries[c.Name] = e
	}

	h.clock.Store(time.Date(2026, 11, 13, 4, 0, 0, 0, time.UTC).UnixNano())
	h.hub = New(st, log, "test", map[int64]Source{srcID: {Name: "main", MaxConnections: limit}})
	h.hub.now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.front = httptest.NewServer(h.hub)
	t.Cleanup(h.front.Close)
	return h
}

func (h *harness) advance(d time.Duration) { h.clock.Add(int64(d)) }

func (h *harness) link(device int64, name string) string {
	h.t.Helper()
	e, ok := h.entries[name]
	if !ok {
		h.t.Fatalf("no entry %q", name)
	}
	return h.front.URL + h.hub.Link(device, e)
}

func get(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// play starts a stream and reads its first bytes. Closing the response stops
// it.
func (h *harness) play(url string) *http.Response {
	h.t.Helper()
	resp := get(h.t, url)
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status %s: %s", resp.Status, body(h.t, resp))
	}
	if _, err := io.ReadFull(resp.Body, make([]byte, 188*4)); err != nil {
		h.t.Fatal(err)
	}
	return resp
}

// waitIdle waits until no request of the hub is in flight.
func (h *harness) waitIdle() {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.hub.mu.Lock()
		busy := 0
		for _, s := range h.hub.sessions {
			busy += s.streams
		}
		h.hub.mu.Unlock()
		if busy == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("streams still running")
}

func TestStreamIsProxied(t *testing.T) {
	h := newHarness(t, 1)
	link := h.link(7, "live/1")
	if !strings.HasSuffix(link, ".ts") || strings.Contains(link, secret) {
		t.Errorf("link %q", link)
	}
	resp := h.play(link)
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("content type %q", ct)
	}
	resp.Body.Close()
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, "stream started") || strings.Contains(logs, secret) {
		t.Errorf("logs: %s", logs)
	}
}

func TestBadLinks(t *testing.T) {
	h := newHarness(t, 1)
	link := h.link(7, "live/1")

	tampered := []byte(link)
	i := len(h.front.URL) + len("/live/") + 5
	tampered[i] ^= 1
	for name, u := range map[string]string{"tampered": string(tampered), "unknown path": h.front.URL + "/live/x/y/z"} {
		resp := get(t, u)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("%s link: %s", name, resp.Status)
		}
	}

	h.advance(linkTTL + time.Second)
	resp := get(t, link)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("expired link: %s", resp.Status)
	}
}

func TestReconnectAfterTheLinkExpires(t *testing.T) {
	h := newHarness(t, 1)
	link := h.link(7, "live/1")
	h.advance(50 * time.Second)
	h.play(link).Body.Close()
	h.waitIdle()

	// The link has expired, but the session it started carries the
	// reconnect.
	h.advance(20 * time.Second)
	h.play(link).Body.Close()
	h.waitIdle()

	// Long after the session stopped, it is gone.
	h.advance(reconnectGrace + time.Second)
	resp := get(t, link)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("reconnect after the grace period: %s", resp.Status)
	}
}

func TestStreamEndingsAndReconnectsAreLogged(t *testing.T) {
	h := newHarness(t, 1)
	link := h.link(7, "short")
	if got := body(t, get(t, link)); len(got) != 188*10 {
		t.Errorf("%d bytes", len(got))
	}
	h.waitIdle()
	req, err := http.NewRequest(http.MethodGet, link, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", "bytes=1880-")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body(t, resp)
	h.waitIdle()

	// A player that goes away is no news.
	h.play(h.link(7, "live/1")).Body.Close()
	h.waitIdle()

	logs := h.logs.String()
	for _, want := range []string{
		`msg="the source ended a stream" channel=short source=main after=0s bytes=1880`,
		`msg="a player reconnected to a stream" channel=short source=main device=7 range="bytes=1880-"`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("no %s in\n%s", want, logs)
		}
	}
	if strings.Count(logs, "the source ended a stream") != 2 || strings.Contains(logs, "channel=live/1 source=main after") {
		t.Errorf("logs:\n%s", logs)
	}
}

func TestConnectionLimit(t *testing.T) {
	h := newHarness(t, 1)
	src := h.entries["live/1"].SourceID

	first := h.play(h.link(1, "live/1"))
	defer first.Body.Close()
	if h.hub.Available(src, 2) || !h.hub.Available(src, 1) {
		t.Error("availability does not follow the limit")
	}

	// Another device is refused while the first one plays.
	resp := get(t, h.link(2, "live/2"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("second device: %s", resp.Status)
	}

	// The same device switching channels replaces its own stream.
	second := h.play(h.link(1, "live/2"))
	defer second.Body.Close()
	if _, err := io.Copy(io.Discard, first.Body); err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Logf("first stream ended with %v", err)
	}
	if !strings.Contains(h.logs.String(), "replaced by a new stream from the same device") {
		t.Errorf("logs: %s", h.logs)
	}
}

func TestNoLimit(t *testing.T) {
	h := newHarness(t, 0)
	a := h.play(h.link(1, "live/1"))
	defer a.Body.Close()
	b := h.play(h.link(2, "live/2"))
	defer b.Body.Close()
}

var resourceLine = regexp.MustCompile(`(?m)^/live/[A-Za-z0-9_-]+/r/\d+(\.[a-z0-9]+)?$`)

func TestHLSIsRewritten(t *testing.T) {
	h := newHarness(t, 1)
	link := h.link(7, "hls/master.m3u8")
	if !strings.HasSuffix(link, ".m3u8") {
		t.Errorf("link %q", link)
	}
	resp := get(t, link)
	master := body(t, resp)
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" {
		t.Errorf("content type %q", ct)
	}
	variant := resourceLine.FindString(master)
	if variant == "" || !strings.HasSuffix(variant, ".m3u8") || strings.Contains(master, secret) {
		t.Fatalf("master playlist:\n%s", master)
	}

	playlist := body(t, get(t, h.front.URL+variant))
	if strings.Contains(playlist, secret) || strings.Contains(playlist, "127.0.0.1") || strings.Contains(playlist, "elsewhere") {
		t.Errorf("variant playlist gives away upstream addresses:\n%s", playlist)
	}
	segments := resourceLine.FindAllString(playlist, -1)
	if len(segments) != 2 {
		t.Fatalf("segments %q in\n%s", segments, playlist)
	}
	if got := body(t, get(t, h.front.URL+segments[0])); got != "SEGMENT1" {
		t.Errorf("segment %q", got)
	}
	key := regexp.MustCompile(`URI="([^"]+)"`).FindStringSubmatch(playlist)
	if key == nil {
		t.Fatalf("no key in\n%s", playlist)
	}
	if got := body(t, get(t, h.front.URL+key[1])); got != "KEY" {
		t.Errorf("key %q", got)
	}

	// The same playlist again keeps its resource numbers.
	if again := body(t, get(t, h.front.URL+variant)); again != playlist {
		t.Errorf("playlist changed:\n%s\nthen\n%s", playlist, again)
	}

	// An HLS session holds its connection while it plays, and lets it go
	// once idle.
	h.waitIdle()
	src := h.entries["hls/master.m3u8"].SourceID
	if h.hub.Available(src, 8) {
		t.Error("a playing HLS session does not count")
	}
	h.advance(idleTimeout + time.Second)
	if !h.hub.Available(src, 8) {
		t.Error("an idle HLS session still counts")
	}
}

func TestPlaylistFoundByItsContent(t *testing.T) {
	h := newHarness(t, 1)

	// A playlist served as text, behind a link for a continuous stream, is
	// relayed: this one is a plain list of one stream.
	if got := body(t, get(t, h.link(7, "sniffed"))); got != "SEGMENT1" {
		t.Errorf("relayed %q", got)
	}

	// Within an HLS stream, it is rewritten.
	master := body(t, get(t, h.link(7, "hls/text.m3u8")))
	variant := resourceLine.FindString(master)
	if variant == "" {
		t.Fatalf("master playlist:\n%s", master)
	}
	if got := body(t, get(t, h.front.URL+variant)); !resourceLine.MatchString(got) {
		t.Errorf("playlist served as text was not rewritten:\n%s", got)
	}
}

func TestUpstreamErrors(t *testing.T) {
	h := newHarness(t, 1)
	resp := get(t, h.link(7, "missing"))
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("missing upstream: %s", resp.Status)
	}
	body(t, resp)
	h.waitIdle()
	if !h.hub.Available(h.entries["missing"].SourceID, 8) {
		t.Error("a failed stream holds its connection")
	}
	if strings.Contains(h.logs.String(), secret) {
		t.Errorf("logs: %s", h.logs)
	}
}

func TestResourcesOfEndedSessions(t *testing.T) {
	h := newHarness(t, 1)
	master := body(t, get(t, h.link(7, "hls/master.m3u8")))
	variant := resourceLine.FindString(master)
	h.advance(idleTimeout + reconnectGrace + time.Second)
	h.hub.mu.Lock()
	h.hub.sweep(h.hub.now())
	h.hub.mu.Unlock()
	for u, want := range map[string]int{
		h.front.URL + variant: http.StatusNotFound,
		h.front.URL + "/live/" + strings.Repeat("A", 66) + "/r/0": http.StatusForbidden,
	} {
		resp := get(t, u)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %s, want %d", u, resp.Status, want)
		}
	}
}

func TestTokens(t *testing.T) {
	h := newHarness(t, 1)
	c := claims{entryID: 12, deviceID: 34, expires: 56, nonce: 78}
	token := h.hub.sign(c)
	if got, ok := h.hub.verify(token); !ok || got != c {
		t.Errorf("verify(sign(%+v)) = %+v, %v", c, got, ok)
	}
	other := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "test", nil)
	if _, ok := other.verify(token); ok {
		t.Error("another hub accepted the token")
	}
	for _, bad := range []string{"", "x", token + "A", strings.ToUpper(token)} {
		if _, ok := h.hub.verify(bad); ok && bad != token {
			t.Errorf("accepted %q", bad)
		}
	}
	if fmt.Sprint(len(token)) != "66" {
		t.Errorf("token length %d", len(token))
	}
}

func TestStreamsListsTheSessions(t *testing.T) {
	h := newHarness(t, 0)
	if got := h.hub.Streams(); len(got) != 0 {
		t.Fatalf("streams before playing: %+v", got)
	}
	first := h.play(h.link(7, "live/1"))
	h.advance(time.Second)
	second := h.play(h.link(8, "live/2"))
	got := h.hub.Streams()
	if len(got) != 2 || got[0].DeviceID != 7 || got[0].Channel != "live/1" || got[0].Source != "main" || !got[0].Playing ||
		got[0].Bytes == 0 || got[1].DeviceID != 8 || !got[1].Started.After(got[0].Started) {
		t.Fatalf("streams %+v", got)
	}
	// A stream whose player went away waits for it to come back, then ends.
	first.Body.Close()
	second.Body.Close()
	h.waitIdle()
	if got = h.hub.Streams(); len(got) != 2 || got[0].Playing || got[1].Playing {
		t.Errorf("after the players left: %+v", got)
	}
	h.advance(time.Minute)
	h.hub.mu.Lock()
	h.hub.sweep(h.hub.now())
	h.hub.mu.Unlock()
	if got = h.hub.Streams(); len(got) != 0 {
		t.Errorf("after the grace: %+v", got)
	}
}

// A source kept to public addresses (D121) plays nothing from the server's
// own networks, such as the fixture's upstream on the loopback address: a
// stream, an HLS playlist or a film.
func TestPublicOnlySourceStaysOffTheHomeNetwork(t *testing.T) {
	h := newHarness(t, 2)
	srcID := h.entries["live/1"].SourceID
	h.hub.SetSources(map[int64]Source{srcID: {Name: "main", MaxConnections: 2, PublicOnly: true}})
	for _, name := range []string{"live/1", "hls/master.m3u8"} {
		resp := get(t, h.link(1, name))
		if b := body(t, resp); resp.StatusCode == http.StatusOK {
			t.Errorf("%s played from the loopback address: %.40q", name, b)
		}
	}
	if logs := h.logs.String(); !strings.Contains(logs, "the source may reach public addresses only") {
		t.Errorf("the log does not say why: %s", logs)
	}

	// Without the setting, the same channel plays.
	h.hub.SetSources(map[int64]Source{srcID: {Name: "main", MaxConnections: 2}})
	resp := h.play(h.link(2, "live/1"))
	resp.Body.Close()
}
