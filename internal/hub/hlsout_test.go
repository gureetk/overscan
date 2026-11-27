// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hlscut"
	"overscan.gureet.ca/internal/mpegts"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// testFeed returns a feed that no tuner feeds, for its playlist alone.
func testFeed() *hlsFeed {
	f := &hlsFeed{}
	f.cond = sync.NewCond(&f.mu)
	f.cutter = hlscut.New(hlsMin, hlsMaxTarget, f.add)
	return f
}

// mediaOf reads a media playlist: its target duration, its media and
// discontinuity sequences, its segments' lengths and names, and before
// which of them a discontinuity comes.
type mediaOf struct {
	target, seq, discSeq int
	lengths              []float64
	names                []string
	discs                []int
}

func readMedia(t *testing.T, text string) mediaOf {
	t.Helper()
	var p mediaOf
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) == 0 || lines[0] != "#EXTM3U" {
		t.Fatalf("not a playlist: %q", text)
	}
	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, "#EXT-X-TARGETDURATION:"):
			p.target, _ = strconv.Atoi(strings.TrimPrefix(l, "#EXT-X-TARGETDURATION:"))
		case strings.HasPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"):
			p.seq, _ = strconv.Atoi(strings.TrimPrefix(l, "#EXT-X-MEDIA-SEQUENCE:"))
		case strings.HasPrefix(l, "#EXT-X-DISCONTINUITY-SEQUENCE:"):
			p.discSeq, _ = strconv.Atoi(strings.TrimPrefix(l, "#EXT-X-DISCONTINUITY-SEQUENCE:"))
		case l == "#EXT-X-DISCONTINUITY":
			p.discs = append(p.discs, len(p.names))
		case strings.HasPrefix(l, "#EXTINF:"):
			v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimPrefix(l, "#EXTINF:"), ","), 64)
			if err != nil {
				t.Fatalf("EXTINF %q", l)
			}
			p.lengths = append(p.lengths, v)
		case !strings.HasPrefix(l, "#"):
			p.names = append(p.names, l)
		}
	}
	return p
}

// TestCutPlaylistRules adds segments to a feed and reads its playlist after
// each, as players would: it keeps to RFC 8216's rules for a live playlist.
func TestCutPlaylistRules(t *testing.T) {
	f := testFeed()
	lengths := []time.Duration{5 * time.Second, 4 * time.Second}
	for range 40 {
		lengths = append(lengths, 4*time.Second, 4200*time.Millisecond, 4*time.Second, 5*time.Second)
	}
	var off int64
	lastSeq, lastDisc := -1, -1
	for i, d := range lengths {
		// A discontinuity every 13th segment.
		f.add(hlscut.Segment{Off: off, Len: 1000, Duration: d, Key: true, Discontinuity: i > 0 && i%13 == 0})
		off += 1000
		if i < hlsFirst-1 {
			if f.target != 0 {
				t.Fatalf("a target duration after %d segments", i+1)
			}
			continue
		}
		p := readMedia(t, f.mediaPlaylist())
		if p.target != 5 {
			t.Fatalf("target duration %d", p.target)
		}
		if p.seq < lastSeq || p.discSeq < lastDisc {
			t.Fatalf("after %d segments, the sequences went back: %d after %d, %d after %d", i+1, p.seq, lastSeq,
				p.discSeq, lastDisc)
		}
		lastSeq, lastDisc = p.seq, p.discSeq
		var total float64
		for k, l := range p.lengths {
			total += l
			if int(l+0.5) > p.target {
				t.Errorf("segment of %vs over the target %d", l, p.target)
			}
			if want := strconv.Itoa(p.seq+k) + ".ts"; p.names[k] != want {
				t.Fatalf("segment %d is %q, not %q", k, p.names[k], want)
			}
		}
		if p.seq > 0 && (len(p.names) < hlsWindow || total < 3*float64(p.target)) {
			t.Errorf("after %d segments the playlist lasts %vs in %d segments", i+1, total, len(p.names))
		}
		// Each segment's discontinuity sequence number: the playlist's,
		// and the tags before it.
		for k := range p.names {
			n := p.discSeq
			for _, d := range p.discs {
				if d <= k {
					n++
				}
			}
			seq := int64(p.seq + k)
			if s, ok := f.segment(seq); !ok || s.discs != int64(n) {
				t.Fatalf("segment %d: discontinuity sequence %d, the feed's %+v", seq, n, s)
			}
		}
	}
	if _, ok := f.segment(0); ok {
		t.Error("the feed still remembers its first segment")
	}
	if s, ok := f.segment(int64(len(lengths) - 1)); !ok || s.Off != off-1000 {
		t.Errorf("the newest segment: %+v, %v", s, ok)
	}
	if _, ok := f.segment(int64(len(lengths))); ok {
		t.Error("a segment to come")
	}
	if bw := f.bandwidth(); bw != 2000 {
		t.Errorf("bandwidth %d", bw)
	}
}

// liveClip makes seconds of a test picture in H.264, with a keyframe every
// gop pictures at 25 a second, and AAC sound, as MPEG-TS, as a channel's
// source would send it.
func liveClip(t *testing.T, ffmpeg string, seconds, gop int) []byte {
	return liveClipOf(t, ffmpeg, "320x180", seconds, gop)
}

// liveClipOf makes a clip as liveClip does, of pictures of the given size.
func liveClipOf(t *testing.T, ffmpeg, size string, seconds, gop int) []byte {
	t.Helper()
	length := strconv.Itoa(seconds)
	path := filepath.Join(t.TempDir(), "clip.ts")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=size="+size+":rate=25:duration="+length,
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration="+length,
		"-c:v", "libx264", "-preset", "ultrafast", "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop),
		"-sc_threshold", "0", "-bf", "2", "-c:a", "aac", "-ac", "2", "-f", "mpegts", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Skipf("ffmpeg cannot make an H.264 clip: %v: %s", err, stderr.String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// clipUpstream sends a clip at /clip and /other, then holds the
// connection open, as a live source would between bursts, until the
// request ends.
type clipUpstream struct {
	srv  *httptest.Server
	clip []byte
	mu   sync.Mutex
	reqs int
	open int
}

func newClipUpstream(t *testing.T, clip []byte) *clipUpstream {
	u := &clipUpstream{clip: clip}
	stop := make(chan struct{})
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.reqs++
		u.open++
		u.mu.Unlock()
		defer func() {
			u.mu.Lock()
			u.open--
			u.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "video/mp2t")
		_, _ = w.Write(u.clip)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(func() {
		close(stop)
		u.srv.Close()
	})
	return u
}

func (u *clipUpstream) counts() (reqs, open int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.reqs, u.open
}

// cutHarness is a hub with a folder for buffers, timeshift off and the
// shared tuner off, and two channels whose source sends clip.
func cutHarness(t *testing.T, limit int, clip []byte) (*harness, *clipUpstream) {
	t.Helper()
	u := newClipUpstream(t, clip)
	h := newHarnessWith(t, limit, u.srv.URL, []string{"/clip", "/other"})
	if err := h.hub.UseTimeshift(filepath.Join(t.TempDir(), "timeshift")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.hub.mu.Lock()
		for _, s := range h.hub.sessions {
			h.hub.end(s, "the test ended")
		}
		h.hub.mu.Unlock()
		h.hub.tuners.Wait()
	})
	return h, u
}

// master reads a playlist of one rendition and returns its rendition's
// address and bandwidth.
func master(t *testing.T, resp *http.Response) (string, int) {
	t.Helper()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Fatalf("the playlist of renditions: %s, %s: %s", resp.Status, resp.Header.Get("Content-Type"), body(t, resp))
	}
	lines := strings.Split(strings.TrimSpace(body(t, resp)), "\n")
	if len(lines) != 4 || lines[0] != "#EXTM3U" || !strings.HasPrefix(lines[2], "#EXT-X-STREAM-INF:BANDWIDTH=") {
		t.Fatalf("the playlist of renditions: %q", lines)
	}
	bw, _ := strconv.Atoi(strings.TrimPrefix(lines[2], "#EXT-X-STREAM-INF:BANDWIDTH="))
	return lines[3], bw
}

// A player that asks for a channel as HLS gets a playlist of one rendition
// that points to its session; the session's playlist lists 4-second
// segments that start at keyframes with the PAT and the PMT; a second
// player shares the one connection to the source while the shared tuner is
// off; ffmpeg plays it as a player would; and once the players stop asking,
// the session and the connection end (D153).
func TestCutHLS(t *testing.T) {
	ffmpeg, ffprobe := realFFmpeg(t), realFFprobe(t)
	h, u := cutHarness(t, 1, liveClip(t, ffmpeg, 30, 50))
	entry := h.entries["clip"]
	vlc := h.hub.Player(0, "xtream", "192.0.2.10", "VLC/3.0.21 LibVLC/3.0.21")
	link := h.hub.CutLink(vlc, entry)
	if !strings.HasPrefix(link, "/live/") || !strings.HasSuffix(link, ".m3u8") {
		t.Fatalf("link %q", link)
	}
	if again := h.hub.CutLink(vlc, entry); again != link {
		t.Errorf("the player's link again: %q, not %q", again, link)
	}
	if other := h.hub.CutLink(vlc, h.entries["other"]); other == link || other == "" {
		t.Errorf("another channel's link: %q", other)
	}
	uri, bw := master(t, get(t, h.front.URL+link))
	token := strings.TrimSuffix(strings.TrimPrefix(link, "/live/"), ".m3u8")
	if uri != "/live/"+token+"/hls.m3u8" || bw < 10_000 {
		t.Errorf("rendition %q at %d bit/s", uri, bw)
	}
	if strings.Contains(uri, u.srv.URL) || strings.Contains(link, "clip") {
		t.Errorf("the addresses show the source: %s, %s", link, uri)
	}

	resp := get(t, h.front.URL+uri)
	if resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" {
		t.Errorf("media playlist: %s", resp.Header.Get("Content-Type"))
	}
	// The first playlist comes with the first two segments; the source sent
	// the whole clip at once, so the rest follow.
	p := readMedia(t, body(t, resp))
	if p.target != 4 || p.seq != 0 || len(p.names) < hlsFirst {
		t.Fatalf("the first media playlist %+v", p)
	}
	waitFor(t, "seven segments", func() bool {
		p = readMedia(t, body(t, get(t, h.front.URL+uri)))
		return p.seq+len(p.names) >= 7
	})
	// 30 s with a keyframe every 2 s: seven segments end at keyframes
	// before the clip's last 2 s, and the playlist lists the newest six.
	if p.target != 4 || p.seq != 1 || len(p.names) != hlsWindow || p.names[0] != "1.ts" || len(p.discs) != 0 {
		t.Fatalf("media playlist %+v", p)
	}
	for i, l := range p.lengths {
		if l < 3.99 || l > 4.01 {
			t.Errorf("segment %d lasts %vs", i, l)
		}
	}

	// The first segment has left the playlist, but is still there for a
	// player that read an older one.
	seg := get(t, h.front.URL+"/live/"+token+"/0.ts")
	data, err := io.ReadAll(seg.Body)
	seg.Body.Close()
	if err != nil || seg.StatusCode != http.StatusOK || seg.Header.Get("Content-Type") != "video/mp2t" ||
		seg.Header.Get("Content-Length") != strconv.Itoa(len(data)) {
		t.Fatalf("segment 0: %s, %v, %d bytes, %s", seg.Status, err, len(data), seg.Header.Get("Content-Length"))
	}
	if len(data) < 3*mpegts.PacketSize || mpegts.Packet(data[:mpegts.PacketSize]).PID() != 0 ||
		mpegts.Packet(data[mpegts.PacketSize:2*mpegts.PacketSize]).PID() == 0 {
		t.Fatalf("segment 0 does not start with the PAT and the PMT")
	}
	segPath := filepath.Join(t.TempDir(), "0.ts")
	if err := os.WriteFile(segPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-count_frames",
		"-show_entries", "stream=codec_name,nb_read_frames", "-of", "csv=p=0", segPath).Output()
	// ffprobe lists an MPEG-TS stream under its program too.
	if f := strings.Fields(string(out)); err != nil || len(f) == 0 || f[0] != "h264,100" {
		t.Errorf("ffprobe of segment 0: %q, %v", out, err)
	}
	for _, missing := range []string{"/99.ts", "/-1.ts", "/01.ts", "/x.ts", "/other"} {
		if resp := get(t, h.front.URL+"/live/"+token+missing); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %s", missing, resp.Status)
		}
	}

	// A second player of the channel shares the tuner, within a limit of
	// one connection, while the shared tuner is off.
	tivi := h.hub.Player(0, "xtream", "192.0.2.11", "TiviMate/5.1.6")
	link2 := h.hub.CutLink(tivi, entry)
	uri2, _ := master(t, get(t, h.front.URL+link2))
	if uri2 == uri {
		t.Error("the second player's session is the first's")
	}
	if p2 := readMedia(t, body(t, get(t, h.front.URL+uri2))); p2.seq != p.seq || len(p2.names) != len(p.names) {
		t.Errorf("the second player's playlist: %+v", p2)
	}
	if reqs, open := u.counts(); reqs != 1 || open != 1 {
		t.Errorf("%d requests to the source, %d open", reqs, open)
	}

	// ffmpeg reads the HLS as a player would.
	lavf := h.hub.Player(0, "xtream", "192.0.2.12", "Lavf/60.16.100")
	played := filepath.Join(t.TempDir(), "played.ts")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", h.front.URL+h.hub.CutLink(lavf, entry), "-map", "0", "-c", "copy", "-t", "6", "-f", "mpegts", played)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("ffmpeg as a player: %v: %s", err, stderr.String())
	}
	out, err = exec.Command(ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", played).Output()
	if d, perr := strconv.ParseFloat(strings.TrimSpace(string(out)), 64); err != nil || perr != nil || d < 5 {
		t.Errorf("what ffmpeg played: %q, %v", out, err)
	}

	streams := h.hub.Streams()
	if len(streams) != 3 {
		t.Fatalf("streams %+v", streams)
	}
	for _, s := range streams {
		if !s.Cut || !s.Shared || s.Channel != "clip" || s.Player == "" {
			t.Errorf("stream %+v", s)
		}
	}
	logs := h.logs.String()
	for _, want := range []string{"a channel is cut into HLS segments for players, from its shared tuner",
		"a player plays a channel as HLS that the hub cuts from its shared tuner", "shared_by="} {
		if !strings.Contains(logs, want) {
			t.Errorf("the log lacks %q", want)
		}
	}

	// The players stop asking: their sessions end, then the tuner, and with
	// it the connection.
	h.advance(idleTimeout + time.Second)
	if s := h.hub.lookup(0); s != nil {
		t.Errorf("a session: %+v", s)
	}
	if len(h.hub.Streams()) != 0 || h.tunerOf("clip") != nil {
		t.Errorf("streams after the players stopped: %+v", h.hub.Streams())
	}
	waitFor(t, "the source's connection to close", func() bool { _, open := u.counts(); return open == 0 })
	if resp := get(t, h.front.URL+uri); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the ended session's playlist: %s", resp.Status)
	}
	// The link itself has expired by then, so the player asks its output
	// again, which gives a new one.
	h.advance(linkTTL)
	if resp := get(t, h.front.URL+link); resp.StatusCode != http.StatusForbidden {
		t.Errorf("the expired link: %s", resp.Status)
	}
	if again := h.hub.CutLink(vlc, entry); again == link {
		t.Error("the expired link again")
	}
}

// A player's channel change replaces its own HLS session at a full
// source, as a box's does (D36); a channel that the shared tuner cannot
// carry gets no link of its own (D153).
func TestCutHLSLimits(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	h, u := cutHarness(t, 1, liveClip(t, ffmpeg, 12, 50))
	vlc := h.hub.Player(0, "xtream", "192.0.2.10", "VLC/3.0.21 LibVLC/3.0.21")
	first, _ := master(t, get(t, h.front.URL+h.hub.CutLink(vlc, h.entries["clip"])))
	second, _ := master(t, get(t, h.front.URL+h.hub.CutLink(vlc, h.entries["other"])))
	if resp := get(t, h.front.URL+first); resp.StatusCode != http.StatusNotFound {
		t.Errorf("the replaced session's playlist: %s", resp.Status)
	}
	if resp := get(t, h.front.URL+second); resp.StatusCode != http.StatusOK {
		t.Errorf("the new session's playlist: %s", resp.Status)
	}
	if !strings.Contains(h.logs.String(), "replaced by a new stream from the same device") {
		t.Error("the log does not say that the first was replaced")
	}
	waitFor(t, "one connection", func() bool { _, open := u.counts(); return open == 1 })

	// Another player gets nothing more from the full source.
	tivi := h.hub.Player(0, "xtream", "192.0.2.11", "TiviMate/5.1.6")
	if resp := get(t, h.front.URL+h.hub.CutLink(tivi, h.entries["clip"])); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("another channel at the full source: %s", resp.Status)
	}

	radio := h.entries["clip"]
	radio.Radio = true
	hls := h.entries["clip"]
	hls.StreamURL = u.srv.URL + "/live.m3u8"
	for name, e := range map[string]store.PlayableEntry{"a radio station": radio, "an HLS source's channel": hls} {
		if link := h.hub.CutLink(vlc, e); link != "" {
			t.Errorf("%s gets a link: %s", name, link)
		}
	}
}

// segmentVideo fetches a segment of a session's HLS and says what its
// video is, as ffprobe reads it, such as "h264 1024x576".
func segmentVideo(t *testing.T, ffprobe, address string) string {
	t.Helper()
	resp := get(t, address)
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s, %v", address, resp.Status, err)
	}
	path := filepath.Join(t.TempDir(), "segment.ts")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(ffprobe, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	f := strings.Split(strings.Fields(string(out))[0], ",")
	if len(f) != 3 {
		return string(out)
	}
	return f[0] + " " + f[1] + "x" + f[2]
}

// HLS of a transcode (D155): a channel set always with a profile reaches
// its HLS players in the profile's size, through one transcode and one
// connection for both of them, in 4-second segments since the transcode
// starts a picture every 2 s; the Overview shows each player's HLS as
// transcoded; and once they stop asking, the transcode ends and gives its
// slot back.
func TestCutHLSTranscode(t *testing.T) {
	ffmpeg, ffprobe := realFFmpeg(t), realFFprobe(t)
	// Room for two connections: the HLS players' one, and a .ts player's
	// below, which the shared tuner, off, does not share.
	h, u := cutHarness(t, 2, liveClipOf(t, ffmpeg, "1280x720", 30, 75))
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseEncoder(Encoder{H264: true})
	ctx := context.Background()
	st := h.hub.store
	if err := st.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	small, err := streamprofile.Check(streamprofile.Profile{Name: "small", MaxLines: 576, Quality: streamprofile.Low})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddTranscodeProfile(ctx, small); err != nil {
		t.Fatal(err)
	}
	entry := h.entries["clip"]
	if err := st.SetEntriesTranscode(ctx, entry.LineupID, []int64{entry.EntryID}, store.Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if entry, err = st.PlayableEntry(ctx, entry.EntryID); err != nil {
		t.Fatal(err)
	}

	var renditions []string
	for _, agent := range []string{"VLC/3.0.21 LibVLC/3.0.21", "TiviMate/5.1.6"} {
		player := h.hub.Player(store.HouseholdAccount, "xtream", "192.0.2.10", agent)
		link := h.hub.CutLink(player, entry)
		if link == "" {
			t.Fatal("a channel that the hub transcodes gets no HLS")
		}
		uri, _ := master(t, get(t, h.front.URL+link))
		renditions = append(renditions, uri)
	}
	var p mediaOf
	waitFor(t, "three segments", func() bool {
		p = readMedia(t, body(t, get(t, h.front.URL+renditions[0])))
		return len(p.names) >= 3
	})
	if p.target != 4 {
		t.Errorf("target duration %d", p.target)
	}
	for i, l := range p.lengths[:len(p.lengths)-1] {
		if l < 3.99 || l > 4.01 {
			t.Errorf("segment %d lasts %vs", i, l)
		}
	}
	for i, uri := range renditions {
		base := h.front.URL + strings.TrimSuffix(uri, "hls.m3u8")
		if got := segmentVideo(t, ffprobe, base+"0.ts"); got != "h264 1024x576" {
			t.Errorf("player %d's segment: %s", i, got)
		}
	}
	if reqs, _ := u.counts(); reqs != 1 {
		t.Errorf("%d requests to the source", reqs)
	}
	logs := h.logs.String()
	if n := strings.Count(logs, `msg="transcoding a stream with ffmpeg"`); n != 1 {
		t.Errorf("%d transcodes in the log:\n%s", n, logs)
	}
	if !strings.Contains(logs, "a channel is transcoded once for its HLS players, which share it") ||
		!strings.Contains(logs, "profile=small set_by=channel") {
		t.Errorf("the log:\n%s", logs)
	}
	streams := h.hub.Streams()
	if len(streams) != 2 {
		t.Fatalf("streams %+v", streams)
	}
	for _, s := range streams {
		if !s.Cut || !s.Transcoded || s.Profile != "small" || s.TranscodedBy != TranscodedByOverscan {
			t.Errorf("stream %+v", s)
		}
	}

	// A player of the same channel through its .ts address finds the one
	// transcode at a time taken, and gets it as its source sends it.
	box := h.hub.Player(store.HouseholdAccount, "m3u", "192.0.2.11", "Kodi/21.1")
	resp := get(t, h.front.URL+h.hub.Link(box, entry))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("the .ts player: %s", resp.Status)
	}
	readPackets(t, resp.Body, 4)
	resp.Body.Close()
	waitFor(t, "the cap's line", func() bool {
		return strings.Contains(h.logs.String(), "the server transcodes as many streams as it may; this one plays as its source sends it")
	})

	// The players stop asking: the transcode ends and gives back its slot.
	h.advance(idleTimeout + time.Second)
	h.hub.lookup(0)
	waitFor(t, "the transcode to end", func() bool {
		h.hub.encodeMu.Lock()
		defer h.hub.encodeMu.Unlock()
		return len(h.hub.encodes) == 0
	})
	if !strings.Contains(h.logs.String(), `msg="a channel's transcode for HLS players ended" channel=clip`) {
		t.Errorf("the log:\n%s", h.logs.String())
	}
}

// A player whose channel cannot be transcoded now, here with transcoding
// off, gets its HLS as the source sends it, from the same feed as the
// players of the channel as sent (D155).
func TestCutHLSTranscodeOff(t *testing.T) {
	ffmpeg := realFFmpeg(t)
	h, _ := cutHarness(t, 1, liveClip(t, ffmpeg, 12, 50))
	h.hub.UseFFmpeg(ffmpeg, RepairSet)
	h.hub.UseEncoder(Encoder{H264: true})
	ctx := context.Background()
	st := h.hub.store
	if err := st.SetAccountOutputProfile(ctx, store.HouseholdAccount, "h264-720p"); err != nil {
		t.Fatal(err)
	}
	entry := h.entries["clip"]
	player := h.hub.Player(store.HouseholdAccount, "xtream", "192.0.2.10", "VLC/3.0.21 LibVLC/3.0.21")
	uri, _ := master(t, get(t, h.front.URL+h.hub.CutLink(player, entry)))
	if p := readMedia(t, body(t, get(t, h.front.URL+uri))); len(p.names) < hlsFirst {
		t.Fatalf("media playlist %+v", p)
	}
	tuner := h.tunerOf("clip")
	h.hub.mu.Lock()
	feeds := len(tuner.feeds)
	_, asSent := tuner.feeds[""]
	h.hub.mu.Unlock()
	if feeds != 1 || !asSent {
		t.Errorf("the tuner's feeds: %d, as sent %v", feeds, asSent)
	}
	if !strings.Contains(h.logs.String(), "transcoding is off; it plays as its source sends it") {
		t.Errorf("the log:\n%s", h.logs.String())
	}
	if s := h.hub.Streams(); len(s) != 1 || s[0].Transcoded {
		t.Errorf("streams %+v", s)
	}
}
