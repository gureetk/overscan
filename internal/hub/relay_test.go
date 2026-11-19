// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHLS is a fictional HLS source. Its playlists are written anew for
// every load and served as plain text, as many sources do; a playlist that
// comes out empty fails with 500. A segment at /seg/<name>.ts holds
// "<name>;", and one at /enc/<seq>.ts holds "<seq>;" encrypted with key.
// /redirect sends players on to /channel?token=abc.
type fakeHLS struct {
	srv       *httptest.Server
	playlists map[string]func(load int) string
	key       []byte // at /key
	iv        []byte // nil to use the segment's sequence number

	mu       sync.Mutex
	loads    map[string]int
	hits     map[string]int   // requests by path
	failures map[string][]int // statuses to answer for a path, before serving it
}

func newFakeHLS(t *testing.T, playlists map[string]func(load int) string) *fakeHLS {
	t.Helper()
	f := &fakeHLS{playlists: playlists, loads: map[string]int{}, hits: map[string]int{}, failures: map[string][]int{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeHLS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	f.mu.Lock()
	f.hits[p]++
	status := 0
	if q := f.failures[p]; len(q) > 0 {
		status, f.failures[p] = q[0], q[1:]
	}
	load := f.loads[p]
	f.loads[p]++
	playlist := f.playlists[p]
	f.mu.Unlock()

	switch {
	case status != 0:
		http.Error(w, "refused", status)
	case p == "/redirect":
		http.Redirect(w, r, "/channel?token=abc", http.StatusFound)
	case playlist != nil:
		text := playlist(load)
		if text == "" {
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, text)
	case p == "/key":
		w.Write(f.key)
	case strings.HasPrefix(p, "/seg/"):
		w.Header().Set("Content-Type", "video/mp2t")
		io.WriteString(w, strings.TrimSuffix(strings.TrimPrefix(p, "/seg/"), ".ts")+";")
	case strings.HasPrefix(p, "/enc/"):
		name := strings.TrimSuffix(strings.TrimPrefix(p, "/enc/"), ".ts")
		seq, _ := strconv.ParseInt(name, 10, 64)
		w.Write(encrypt(f.key, f.ivFor(seq), []byte(name+";")))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeHLS) ivFor(seq int64) []byte {
	if f.iv != nil {
		return f.iv
	}
	iv := make([]byte, 16)
	binary.BigEndian.PutUint64(iv[8:], uint64(seq))
	return iv
}

func (f *fakeHLS) hitsOf(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[path]
}

func (f *fakeHLS) totalHits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

// settledHits returns the number of requests once none has arrived for a
// moment, or after a second. A request that the relay sent just before it
// stopped can reach the server after the relay has gone, and counts as sent
// before.
func (f *fakeHLS) settledHits() int {
	n := f.totalHits()
	for range 40 {
		time.Sleep(25 * time.Millisecond)
		m := f.totalHits()
		if m == n {
			break
		}
		n = m
	}
	return n
}

// encrypt pads plain as PKCS#7 does and encrypts it with AES-128-CBC.
func encrypt(key, iv, plain []byte) []byte {
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append(bytes.Clone(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(err)
	}
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(data, data)
	return data
}

// livePlaylist writes a live media playlist with a target duration of two
// seconds, whose segments are uris, numbered from seq.
func livePlaylist(seq int64, uris []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", seq)
	for _, u := range uris {
		fmt.Fprintf(&b, "#EXTINF:2.000,\n%s\n", u)
	}
	return b.String()
}

// segs returns the addresses of segments first to last under dir, named
// prefix and their number.
func segs(dir, prefix string, first, last int64) []string {
	var out []string
	for n := first; n <= last; n++ {
		out = append(out, fmt.Sprintf("/%s/%s%d.ts", dir, prefix, n))
	}
	return out
}

// names returns the markers of segments first to last named prefix and
// their number.
func names(prefix string, first, last int64) []string {
	var out []string
	for n := first; n <= last; n++ {
		out = append(out, prefix+strconv.FormatInt(n, 10))
	}
	return out
}

// window is a live playlist that moves on by a segment with every load,
// listing six segments and ending at 100 on the first load.
func window(load int) string {
	last := int64(100 + load)
	return livePlaylist(last-5, segs("seg", "", last-5, last))
}

// relayHarness plays channels of f, one per path, in virtual time: the
// hub's waits move its clock on at once.
func relayHarness(t *testing.T, f *fakeHLS, paths ...string) *harness {
	t.Helper()
	h := newHarnessWith(t, 1, f.srv.URL, paths)
	h.hub.sleep = func(ctx context.Context, d time.Duration) error {
		h.advance(d)
		return ctx.Err()
	}
	return h
}

// open starts playing a link, failing the test on an error status.
func (h *harness) open(link string) *http.Response {
	h.t.Helper()
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(link)
	if err != nil {
		h.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		h.t.Fatalf("status %s: %s", resp.Status, body(h.t, resp))
	}
	return resp
}

// markers reads the segment markers of a relayed stream until it has n of
// them, or to the end of the stream if n is 0.
func markers(t *testing.T, r io.Reader, n int) []string {
	t.Helper()
	br := bufio.NewReader(r)
	var got []string
	for n == 0 || len(got) < n {
		m, err := br.ReadString(';')
		if err != nil {
			if err != io.EOF || m != "" {
				t.Errorf("after %q: %q, %v", got, m, err)
			}
			break
		}
		got = append(got, strings.TrimSuffix(m, ";"))
	}
	return got
}

func TestRelayFollowsALivePlaylist(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": window})
	h := relayHarness(t, f, "/channel")
	link := h.link(7, "channel")
	if !strings.HasSuffix(link, ".ts") {
		t.Fatalf("link %q", link)
	}
	resp := h.open(link)
	if ct := resp.Header.Get("Content-Type"); ct != "video/mp2t" {
		t.Errorf("content type %q", ct)
	}

	// Three segments from the live edge, then each new one once.
	if got, want := markers(t, resp.Body, 8), names("", 98, 105); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	resp.Body.Close()
	h.waitIdle()
	for n := int64(95); n <= 105; n++ {
		want := 1
		if n < 98 {
			want = 0
		}
		if got := f.hitsOf(fmt.Sprintf("/seg/%d.ts", n)); got != want {
			t.Errorf("segment %d fetched %d times, want %d", n, got, want)
		}
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `msg="relaying an HLS stream as one continuous stream" channel=channel source=main live=true segments=6 target_duration=2s`) {
		t.Errorf("logs: %s", logs)
	}
	if strings.Contains(logs, "HLS relay ended") || strings.Contains(logs, f.srv.URL) {
		t.Errorf("logs: %s", logs)
	}
}

func TestRelayStopsWithThePlayer(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": window})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	markers(t, resp.Body, 4)
	resp.Body.Close()
	h.waitIdle()
	before := f.settledHits()
	time.Sleep(50 * time.Millisecond)
	if after := f.totalHits(); after != before {
		t.Errorf("the source was asked %d more times after the player left", after-before)
	}
	if !h.hub.Available(h.entries["channel"].SourceID, 8) {
		t.Error("the relay still holds the source's connection")
	}
}

func TestRelayDecryptsAES128(t *testing.T) {
	key := []byte("0123456789abcdef")
	for name, iv := range map[string][]byte{
		"sequence number": nil,
		"explicit":        bytes.Repeat([]byte{0xA5}, 16),
	} {
		t.Run(name, func(t *testing.T) {
			ivAttr := ""
			if iv != nil {
				ivAttr = ",IV=0x" + strings.Repeat("a5", 16)
			}
			f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string {
				last := int64(100 + load)
				pl := livePlaylist(last-5, segs("enc", "", last-5, last))
				return strings.Replace(pl, "#EXTINF", `#EXT-X-KEY:METHOD=AES-128,URI="/key"`+ivAttr+"\n#EXTINF", 1)
			}})
			f.key, f.iv = key, iv
			h := relayHarness(t, f, "/channel")
			resp := h.open(h.link(7, "channel"))
			defer resp.Body.Close()
			if got, want := markers(t, resp.Body, 4), names("", 98, 101); !reflect.DeepEqual(got, want) {
				t.Errorf("segments %q, want %q", got, want)
			}
			if n := f.hitsOf("/key"); n != 1 {
				t.Errorf("key fetched %d times", n)
			}
		})
	}
}

func TestRelayPicksAVariant(t *testing.T) {
	variant := func(name string) func(int) string {
		return func(load int) string {
			last := int64(100 + load)
			return livePlaylist(last-5, segs("seg", name+"-", last-5, last))
		}
	}
	f := newFakeHLS(t, map[string]func(int) string{
		"/channel": func(int) string {
			return "#EXTM3U\n" +
				`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",URI="/variant/audio"` + "\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360\n/variant/low\n" +
				`#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2"` + "\n/variant/hd\n" +
				"#EXT-X-STREAM-INF:BANDWIDTH=15000000,RESOLUTION=3840x2160\n/variant/uhd\n" +
				`#EXT-X-STREAM-INF:BANDWIDTH=9000000,RESOLUTION=1920x1080,AUDIO="aud"` + "\n/variant/silent\n"
		},
		"/variant/low":    variant("low"),
		"/variant/hd":     variant("hd"),
		"/variant/uhd":    variant("uhd"),
		"/variant/silent": variant("silent"),
	})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	if got, want := markers(t, resp.Body, 4), names("hd-", 98, 101); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	resp.Body.Close()
	h.waitIdle()
	if n := f.hitsOf("/channel"); n != 1 {
		t.Errorf("master playlist loaded %d times", n)
	}
	for _, v := range []string{"/variant/low", "/variant/uhd", "/variant/silent", "/variant/audio"} {
		if n := f.hitsOf(v); n != 0 {
			t.Errorf("%s loaded %d times", v, n)
		}
	}
}

func TestRelayPlaysAnEndedPlaylist(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(int) string {
		return livePlaylist(0, segs("seg", "", 0, 3)) + "#EXT-X-ENDLIST\n"
	}})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()
	if got, want := markers(t, resp.Body, 0), names("", 0, 3); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, `msg="HLS relay ended" channel=channel source=main reason="the playlist ended"`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestRelayEndsWhenTheSourceStalls(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string { return window(min(load, 2)) }})
	h := relayHarness(t, f, "/channel")
	start := h.hub.now()
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()
	if got, want := markers(t, resp.Body, 0), names("", 98, 102); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	h.waitIdle()
	if logs := h.logs.String(); !strings.Contains(logs, `reason="the source stopped adding segments"`) {
		t.Errorf("logs: %s", logs)
	}
	if took := h.hub.now().Sub(start); took < minStall || took > minStall+10*time.Second {
		t.Errorf("ended %s after the start", took)
	}
}

func TestRelayEndsWhenThePlaylistFails(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string {
		if load >= 2 {
			return ""
		}
		return window(load)
	}})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()
	if got, want := markers(t, resp.Body, 0), names("", 98, 101); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	h.waitIdle()
	if n := f.hitsOf("/channel"); n != 2+playlistFailures {
		t.Errorf("playlist loaded %d times", n)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `reason="the source's playlist cannot be loaded: the source answered 500 Internal Server Error"`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestRelaySkipsFailedSegments(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": window})
	f.failures["/seg/101.ts"] = []int{http.StatusNotFound}
	f.failures["/seg/102.ts"] = []int{http.StatusServiceUnavailable}
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()

	// A segment that is not found is skipped; one that fails on the
	// source's side is tried again.
	want := []string{"98", "99", "100", "102", "103"}
	if got := markers(t, resp.Body, 5); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	if a, b := f.hitsOf("/seg/101.ts"), f.hitsOf("/seg/102.ts"); a != 1 || b != 2 {
		t.Errorf("segments 101 and 102 fetched %d and %d times", a, b)
	}
}

func TestRelayFollowsARestartedStream(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string {
		if load < 2 {
			return window(load)
		}
		// The source starts again, numbering from 0.
		last := int64(load - 2)
		first := max(0, last-5)
		return livePlaylist(first, segs("seg", "new-", first, last))
	}})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()
	want := append(names("", 98, 101), names("new-", 0, 2)...)
	if got := markers(t, resp.Body, len(want)); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
}

func TestRelayWithSequenceNumbersThatStayPut(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string {
		last := int64(100 + load)
		return livePlaylist(0, segs("seg", "", last-5, last))
	}})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()
	if got, want := markers(t, resp.Body, 6), names("", 98, 103); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
}

func TestRelayWithTokensInSegmentAddresses(t *testing.T) {
	// A new segment every other load, and a new token in every address
	// at every load.
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string {
		last := int64(100 + load/2)
		uris := segs("seg", "", last-5, last)
		for i := range uris {
			uris[i] += "?token=" + strconv.Itoa(load)
		}
		return livePlaylist(last-5, uris)
	}})
	h := relayHarness(t, f, "/channel")
	resp := h.open(h.link(7, "channel"))
	defer resp.Body.Close()
	if got, want := markers(t, resp.Body, 6), names("", 98, 103); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
}

func TestRelayReloadsWhereTheSourceRedirected(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": window})
	h := relayHarness(t, f, "/redirect")
	resp := h.open(h.link(7, "redirect"))
	defer resp.Body.Close()
	if got, want := markers(t, resp.Body, 6), names("", 98, 103); !reflect.DeepEqual(got, want) {
		t.Errorf("segments %q, want %q", got, want)
	}
	if a, b := f.hitsOf("/redirect"), f.hitsOf("/channel"); a != 1 || b < 3 {
		t.Errorf("the redirecting address was loaded %d times, its target %d", a, b)
	}
}

func TestRelayLeavesWhatItCannotPlayToThePlayer(t *testing.T) {
	f := newFakeHLS(t, map[string]func(int) string{"/channel": func(load int) string {
		return strings.Replace(window(load), "#EXTINF", "#EXT-X-MAP:URI=\"/init.mp4\"\n#EXTINF", 1)
	}})
	h := relayHarness(t, f, "/channel")
	resp := get(t, h.link(7, "channel"))
	playlist := body(t, resp)
	if ct := resp.Header.Get("Content-Type"); ct != "application/vnd.apple.mpegurl" || len(resourceLine.FindAllString(playlist, -1)) != 6 {
		t.Errorf("%s:\n%s", ct, playlist)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="cannot relay an HLS stream; the player gets its playlist" channel=channel source=main reason="it has fragmented MP4 segments"`) {
		t.Errorf("logs: %s", logs)
	}
}

func TestDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef")
	iv := make([]byte, 16)
	for _, plain := range []string{"", "fifteen bytes!!", "sixteen bytes!!!", "a little more than two blocks"} {
		got, err := decrypt(encrypt(key, iv, []byte(plain)), key, iv)
		if err != nil || string(got) != plain {
			t.Errorf("%q: %q, %v", plain, got, err)
		}
	}

	// Padding that is not valid is left in place.
	data := bytes.Repeat([]byte{7}, 32)
	data[31] = 3
	enc := bytes.Clone(data)
	block, _ := aes.NewCipher(key)
	cipher.NewCBCEncrypter(block, iv).CryptBlocks(enc, enc)
	if got, err := decrypt(enc, key, iv); err != nil || !bytes.Equal(got, data) {
		t.Errorf("bad padding: %v, %v", got, err)
	}

	if _, err := decrypt([]byte("not whole blocks"+"!"), key, iv); err == nil {
		t.Error("decrypted a segment that is not whole blocks")
	}
	if _, err := decrypt(nil, key, iv); err == nil {
		t.Error("decrypted an empty segment")
	}
}
