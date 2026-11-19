// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// tsBurst is a stretch of an MPEG-TS stream: a packet of PID 0, then n
// packets of PID 0x100, each packet's payload filled with the byte b.
func tsBurst(b byte, n int) []byte {
	var out []byte
	for i := 0; i <= n; i++ {
		p := bytes.Repeat([]byte{b}, tsPacket)
		p[0], p[1], p[2], p[3] = 0x47, 0x40, 0x00, 0x10 // PID 0
		if i > 0 {
			p[1] = 0x01 // PID 0x100
		}
		out = append(out, p...)
	}
	return out
}

// isPAT reports whether p starts with a packet of PID 0.
func isPAT(p []byte) bool { return len(p) >= 3 && p[0] == 0x47 && p[1]&0x1f == 0 && p[2] == 0 }

// fakeClock is a clock that tests move by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestShift(t *testing.T, keep time.Duration) (*shift, *fakeClock) {
	t.Helper()
	clock := &fakeClock{t: time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)}
	b, err := newShift(filepath.Join(t.TempDir(), "a1"), keep, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	b.free = func(string) (int64, bool) { return 0, false }
	t.Cleanup(b.close)
	b.begin()
	b.Header().Set("Content-Type", "video/mp2t")
	b.WriteHeader(http.StatusOK)
	return b, clock
}

func shortChunks(t *testing.T, n int64) {
	t.Helper()
	was := shiftChunkBytes
	shiftChunkBytes = n
	t.Cleanup(func() { shiftChunkBytes = was })
}

// The buffer notes a place to start from at each packet of PID 0, also
// when a write cuts one short, and finds where a time behind its end is.
func TestShiftMarks(t *testing.T) {
	b, clock := newTestShift(t, time.Hour)
	for i := byte(1); i <= 3; i++ {
		burst := tsBurst(i, 9)
		// Cut in two, in the middle of a packet.
		if _, err := b.Write(burst[:300]); err != nil {
			t.Fatal(err)
		}
		if _, err := b.Write(burst[300:]); err != nil {
			t.Fatal(err)
		}
		clock.add(10 * time.Second)
	}
	if len(b.marks) != 3 {
		t.Fatalf("marks %+v", b.marks)
	}
	size := int64(len(tsBurst(0, 9)))
	b.mu.Lock()
	defer b.mu.Unlock()
	for behind, want := range map[time.Duration]int64{0: 2 * size, time.Second: 2 * size, 15 * time.Second: size,
		25 * time.Second: 0, time.Hour: 0} {
		if got := b.place(behind); got != want {
			t.Errorf("behind %v: at %d, want %d", behind, got, want)
		}
	}
}

// A place to start from is noted at most each shiftMarkEvery, and a
// stream that is not MPEG-TS gets them where writes begin.
func TestShiftMarksPaced(t *testing.T) {
	b, clock := newTestShift(t, time.Hour)
	b.Write(tsBurst(1, 3))
	b.Write(tsBurst(2, 3))
	clock.add(shiftMarkEvery)
	b.Write(tsBurst(3, 3))
	if len(b.marks) != 2 || b.marks[1].off != 2*int64(len(tsBurst(0, 3))) {
		t.Errorf("marks %+v", b.marks)
	}

	// A stream without its table gets a packet each shiftNoTable; a packet
	// of the table takes the place of one noted a moment before.
	null := bytes.Repeat(append([]byte{0x47, 0x1f, 0xff, 0x10}, bytes.Repeat([]byte{0xff}, 184)...), 4)
	bare, clock := newTestShift(t, time.Hour)
	for range 3 {
		bare.Write(null)
		clock.add(shiftNoTable / 2)
	}
	clock.add(shiftNoTable / 2)
	bare.Write(append(null, tsBurst(1, 1)...))
	size := int64(len(null))
	if len(bare.marks) != 3 || bare.marks[0].off != 0 || bare.marks[1].off != 2*size ||
		bare.marks[2].off != 4*size || !bare.marks[2].table || bare.marks[1].table {
		t.Errorf("marks without a table %+v", bare.marks)
	}

	other, clock := newTestShift(t, time.Hour)
	text := bytes.Repeat([]byte("ID3 not a transport stream "), 1+shiftNotTS/27)
	other.Write(text)
	clock.add(time.Second)
	other.Write([]byte("more"))
	if len(other.marks) != 1 || other.marks[0].off != int64(len(text)) {
		t.Errorf("marks of another stream %+v", other.marks)
	}
}

// The buffer drops its oldest files once they are older than it keeps, or
// it is over its size, and a request that falls behind goes on from the
// oldest place it keeps.
func TestShiftDropsOld(t *testing.T) {
	shortChunks(t, 4*tsPacket)
	b, clock := newTestShift(t, 30*time.Second)
	for i := byte(1); i <= 10; i++ {
		b.Write(tsBurst(i, 3)) // one file each
		clock.add(10 * time.Second)
	}
	// Each file began 10 s after the one before: those that ended more
	// than 30 s ago are gone, and the buffer keeps at least 30 s.
	if len(b.chunks) != 5 || b.chunks[0].start != 5*4*tsPacket {
		t.Errorf("%d files, the first at %d", len(b.chunks), b.chunks[0].start)
	}
	files, _ := os.ReadDir(b.dir)
	if len(files) != 5 {
		t.Errorf("%d files on disk", len(files))
	}
	if b.marks[0].off != b.chunks[0].start {
		t.Errorf("first mark %+v, first file at %d", b.marks[0], b.chunks[0].start)
	}

	// A request from before the oldest file starts with it.
	var out bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for out2 := 0; out2 < 100; out2++ {
			time.Sleep(10 * time.Millisecond)
		}
		cancel()
	}()
	b.copyTo(ctx, &limitWriter{w: &out, n: 2 * tsPacket, cancel: cancel}, func() {}, 0)
	if got := out.Bytes(); len(got) < tsPacket || !isPAT(got) || got[tsPacket-1] != 6 {
		t.Errorf("read from the oldest: %d bytes, %x", len(got), got[:min(len(got), 8)])
	}

	// Over its size, without waiting for the time.
	big, clock := newTestShift(t, time.Hour)
	big.most = 3 * shiftChunkBytes
	for i := byte(1); i <= 6; i++ {
		big.Write(tsBurst(i, 3))
		clock.add(time.Second)
	}
	if len(big.chunks) != 3 {
		t.Errorf("%d files with room for 3", len(big.chunks))
	}

	// Short of disk space: the file before the one it writes to, no more.
	short, _ := newTestShift(t, time.Hour)
	short.free = func(string) (int64, bool) { return shiftMinFree - 1, true }
	for i := byte(1); i <= 4; i++ {
		short.Write(tsBurst(i, 3))
	}
	if len(short.chunks) != 2 {
		t.Errorf("%d files on a full disk", len(short.chunks))
	}
}

// limitWriter takes n bytes, then cancels.
type limitWriter struct {
	w      io.Writer
	n      int
	cancel func()
}

func (l *limitWriter) Write(p []byte) (int, error) {
	k := min(len(p), l.n)
	l.w.Write(p[:k])
	l.n -= k
	if l.n <= 0 {
		l.cancel()
	}
	return len(p), nil
}

// A request at the end waits for more, and ends once nothing comes for
// shiftStall, or when the buffer closes.
func TestShiftWaitsAtTheEnd(t *testing.T) {
	stall := shiftStall
	shiftStall = 50 * time.Millisecond
	t.Cleanup(func() { shiftStall = stall })
	tick := shiftTick
	shiftTick = 10 * time.Millisecond
	t.Cleanup(func() { shiftTick = tick })

	b, clock := newTestShift(t, time.Hour)
	b.Write(tsBurst(1, 3))
	var out syncBuffer
	done := make(chan struct{})
	go func() {
		b.copyTo(context.Background(), &out, func() {}, 0)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	b.Write(tsBurst(2, 3))
	time.Sleep(20 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("the request ended while the stream came")
	default:
	}
	clock.add(time.Second)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the request did not end")
	}
	if got := len(out.String()); got != 2*len(tsBurst(0, 3)) {
		t.Errorf("read %d bytes", got)
	}
}

// The buffer waits for the source's answer: a refusal gives no stream,
// and the text of an error is not kept as the stream.
func TestShiftStart(t *testing.T) {
	wait := shiftFirstWait
	shiftFirstWait = 50 * time.Millisecond
	t.Cleanup(func() { shiftFirstWait = wait })

	clock := &fakeClock{t: time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)}
	b, err := newShift(filepath.Join(t.TempDir(), "b2"), time.Hour, clock.now)
	if err != nil {
		t.Fatal(err)
	}
	defer b.close()
	if status, _, _ := b.start(context.Background(), 0); status != http.StatusGatewayTimeout {
		t.Errorf("nothing yet: %d", status)
	}
	b.begin()
	http.Error(b, "the source answered 403 Forbidden", http.StatusBadGateway)
	b.done()
	if status, _, _ := b.start(context.Background(), 0); status != http.StatusBadGateway || b.written() != 0 {
		t.Errorf("refused: %d, %d bytes", status, b.written())
	}
	b.begin()
	b.Header().Set("Content-Type", "video/mp2t")
	b.Write(tsBurst(1, 3))
	status, header, off := b.start(context.Background(), 0)
	if status != http.StatusOK || header.Get("Content-Type") != "video/mp2t" || off != 0 {
		t.Errorf("a stream: %d, %v, at %d", status, header, off)
	}
	b.close()
	if _, err := os.Stat(b.dir); !os.IsNotExist(err) {
		t.Errorf("the buffer's folder: %v", err)
	}
	if status, _, _ := b.start(context.Background(), 0); status != http.StatusServiceUnavailable {
		t.Errorf("closed: %d", status)
	}
	if _, err := b.Write([]byte{0x47}); err != errShiftClosed {
		t.Errorf("a write after closing: %v", err)
	}
}

// burstUpstream sends a stream a burst at a time, as the test says.
type burstUpstream struct {
	srv      *httptest.Server
	bursts   chan []byte
	mu       sync.Mutex
	requests int
	open     int
}

func newBurstUpstream(t *testing.T) *burstUpstream {
	t.Helper()
	u := &burstUpstream{bursts: make(chan []byte, 16)}
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.requests++
		u.open++
		u.mu.Unlock()
		defer func() {
			u.mu.Lock()
			u.open--
			u.mu.Unlock()
		}()
		w.Header().Set("Content-Type", "video/mp2t")
		w.WriteHeader(http.StatusOK)
		rc := http.NewResponseController(w)
		_ = rc.Flush()
		for {
			select {
			case <-r.Context().Done():
				return
			case b, ok := <-u.bursts:
				if !ok || b == nil {
					return // the source ends the stream
				}
				if _, err := w.Write(b); err != nil {
					return
				}
				_ = rc.Flush()
			}
		}
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *burstUpstream) counts() (requests, open int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.requests, u.open
}

// shiftHarness is a hub with timeshift on, and one channel from a burst
// upstream.
func shiftHarness(t *testing.T, limit int) (*harness, *burstUpstream) {
	t.Helper()
	tick := shiftTick
	shiftTick = 10 * time.Millisecond
	t.Cleanup(func() { shiftTick = tick })
	u := newBurstUpstream(t)
	h := newHarnessWith(t, limit, u.srv.URL, []string{"/tv/1", "/tv/2"})
	if err := h.hub.UseTimeshift(filepath.Join(t.TempDir(), "timeshift")); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.store.SetTimeshift(context.Background(), 30); err != nil {
		t.Fatal(err)
	}
	// The sessions end, and their tuners with them, before the servers
	// close, which wait for their requests.
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

func (h *harness) shiftLink(device int64, name string) string {
	h.t.Helper()
	link, ok := h.hub.ShiftLink(device, h.entries[name])
	if !ok {
		h.t.Fatalf("%s gets no timeshift link", name)
	}
	return h.front.URL + link
}

// readPackets reads n packets and returns the byte that fills the first
// one's payload.
func readPackets(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n*tsPacket)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatal(err)
	}
	return buf
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("waited too long for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A box's live channel plays through a timeshift buffer: the source's
// stream goes on into it while the box does not read, and the box plays
// from a while before the end.
func TestTimeshiftPlays(t *testing.T) {
	h, u := shiftHarness(t, 1)
	link := h.shiftLink(7, "tv/1")
	if !strings.HasSuffix(link, ".ts") {
		t.Errorf("link %s", link)
	}
	u.bursts <- tsBurst(1, 9)
	resp := get(t, link)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" {
		t.Fatalf("status %s", resp.Status)
	}
	if p := readPackets(t, resp.Body, 2); !isPAT(p) || p[tsPacket+5] != 1 {
		t.Errorf("first packets %x", p[:8])
	}
	streams := h.hub.Streams()
	if len(streams) != 1 || !streams[0].Timeshift {
		t.Errorf("streams %+v", streams)
	}
	// The box pauses: it reads no more, and the stream goes on, a burst
	// each 10 s. The repeat filter passes a packet on once the next one
	// comes.
	s := h.hub.lookup(h.nonceOf(link))
	size := int64(len(tsBurst(0, 9)))
	for i := byte(2); i <= 3; i++ {
		h.advance(10 * time.Second)
		u.bursts <- tsBurst(i, 9)
		waitFor(t, "the stream to go on", func() bool { return s.shift.written() == int64(i)*size-tsPacket })
	}
	resp.Body.Close()

	// From 15 s before the end: the first burst's place, then the
	// second's... and from 5 s before, the second's.
	for behind, want := range map[string]byte{"15": 1, "5": 2, "": 3, "0": 3, "3600": 1} {
		u := link
		if behind != "" {
			u += "?behind=" + behind
		}
		resp := get(t, u)
		if p := readPackets(t, resp.Body, 2); !isPAT(p) || p[tsPacket+5] != want {
			t.Errorf("behind %q: %x … %d, want burst %d", behind, p[:4], p[tsPacket+5], want)
		}
		resp.Body.Close()
	}
	if !strings.Contains(h.logs.String(), "a box plays its channel from its timeshift buffer") {
		t.Errorf("no log of playing from the buffer: %s", h.logs)
	}
	if requests, _ := u.counts(); requests != 1 {
		t.Errorf("%d requests to the source", requests)
	}
	if resp := get(t, link+"?behind=x"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("behind=x: %s", resp.Status)
	}
}

// nonceOf returns the nonce of a link's token.
func (h *harness) nonceOf(link string) uint64 {
	h.t.Helper()
	token := strings.TrimSuffix(link[strings.LastIndex(link, "/")+1:], ".ts")
	if i := strings.IndexByte(token, '?'); i >= 0 {
		token = token[:i]
	}
	c, ok := h.hub.verify(token)
	if !ok {
		h.t.Fatalf("bad token in %s", link)
	}
	return c.nonce
}

// A paused box keeps its buffer with holds; without them, or once it
// stops, the buffer and the source's stream end.
func TestTimeshiftHoldAndStop(t *testing.T) {
	h, u := shiftHarness(t, 1)
	link := h.shiftLink(7, "tv/1")
	u.bursts <- tsBurst(1, 9)
	resp := get(t, link)
	readPackets(t, resp.Body, 2)
	resp.Body.Close()
	h.waitIdle()
	s := h.hub.lookup(h.nonceOf(link))
	dir := s.shift.dir

	// While the buffer's tuner runs, it counts against the source's limit.
	if h.hub.Available(s.sourceID, 8) {
		t.Error("another box has room while the buffer holds the source's only connection")
	}
	for range 3 {
		h.advance(20 * time.Second)
		if resp := get(t, link+"?hold=1"); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("hold: %s", resp.Status)
		}
	}
	if h.hub.lookup(h.nonceOf(link)) == nil {
		t.Fatal("held, yet gone")
	}
	h.advance(reconnectGrace + time.Second)
	if h.hub.lookup(h.nonceOf(link)) != nil {
		t.Fatal("not held, yet still there")
	}
	waitFor(t, "the source's stream to end", func() bool { _, open := u.counts(); return open == 0 })
	waitFor(t, "the buffer to go", func() bool { _, err := os.Stat(dir); return os.IsNotExist(err) })
	if resp := get(t, link+"?hold=1"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("hold after the end: %s", resp.Status)
	}

	// A stop ends it at once.
	link = h.shiftLink(7, "tv/1")
	u.bursts <- tsBurst(2, 9)
	resp = get(t, link)
	readPackets(t, resp.Body, 2)
	if resp := get(t, link+"?stop=1"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("stop: %s", resp.Status)
	}
	resp.Body.Close()
	if h.hub.lookup(h.nonceOf(link)) != nil {
		t.Error("stopped, yet still there")
	}
	waitFor(t, "the source's stream to end", func() bool { _, open := u.counts(); return open == 0 })
	if !h.hub.Available(h.entries["tv/1"].SourceID, 8) {
		t.Error("no room after the stop")
	}
	if !strings.Contains(h.logs.String(), `reason="the box stopped it"`) {
		t.Errorf("no log of the stop: %s", h.logs)
	}
}

// The tuner asks the source again once it ends the stream, and the box
// goes on from the buffer.
func TestTimeshiftTunesAgain(t *testing.T) {
	retry := shiftRetry
	shiftRetry = 10 * time.Millisecond
	t.Cleanup(func() { shiftRetry = retry })
	h, u := shiftHarness(t, 1)
	link := h.shiftLink(7, "tv/1")
	u.bursts <- tsBurst(1, 9)
	resp := get(t, link)
	defer resp.Body.Close()
	// The repeat filter passes on the burst's last packet once the next
	// comes, or the stream ends.
	readPackets(t, resp.Body, 9)
	u.bursts <- nil // the source ends the stream
	if p := readPackets(t, resp.Body, 1); p[5] != 1 {
		t.Errorf("the last packet: %x", p[:8])
	}
	waitFor(t, "a second request", func() bool { requests, _ := u.counts(); return requests == 2 })
	u.bursts <- tsBurst(2, 9)
	if p := readPackets(t, resp.Body, 2); !isPAT(p) || p[tsPacket+5] != 2 {
		t.Errorf("after the source's end: %x", p[:8])
	}
	if !strings.Contains(h.logs.String(), "the source ended a stream") {
		t.Errorf("logs %s", h.logs)
	}
}

// With timeshift off, or no folder for buffers, a box's link plays as it
// comes; an HLS link never goes through a buffer.
func TestTimeshiftOff(t *testing.T) {
	h, u := shiftHarness(t, 1)
	if err := h.hub.store.SetTimeshift(context.Background(), 0); err != nil {
		t.Fatal(err)
	}
	link := h.shiftLink(7, "tv/1")
	u.bursts <- tsBurst(1, 9)
	resp := get(t, link)
	readPackets(t, resp.Body, 2)
	resp.Body.Close()
	if streams := h.hub.Streams(); len(streams) != 1 || streams[0].Timeshift {
		t.Errorf("streams %+v", streams)
	}
	entries, _ := os.ReadDir(h.hub.shiftDir)
	if len(entries) != 0 {
		t.Errorf("%d buffers", len(entries))
	}

	plain := newHarness(t, 1)
	if _, ok := plain.hub.ShiftLink(7, plain.entries["live/1"]); ok {
		t.Error("a timeshift link without a folder for buffers")
	}
	if err := plain.hub.UseTimeshift(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if link, ok := plain.hub.ShiftLink(7, plain.entries["hls/master.m3u8"]); ok || !strings.HasSuffix(link, ".m3u8") {
		t.Errorf("HLS: %s, %v", link, ok)
	}
	if link, ok := plain.hub.ShiftLink(7, plain.entries["live/1"]); !ok || !strings.HasSuffix(link, ".ts") {
		t.Errorf("TS: %s, %v", link, ok)
	}
}

// UseTimeshift removes the buffers that a stop left, and nothing else.
func TestUseTimeshiftCleans(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"a1b2/0.ts", "notes/keep.txt"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(p)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := newHarness(t, 1)
	if err := h.hub.UseTimeshift(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a1b2")); !os.IsNotExist(err) {
		t.Errorf("a buffer left: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes/keep.txt")); err != nil {
		t.Errorf("another file: %v", err)
	}
}

// FuzzShiftScan scans what comes, cut where it says, for places to start
// from: they are in order, each within what came, and in MPEG-TS each is a
// packet.
func FuzzShiftScan(f *testing.F) {
	f.Add(tsBurst(1, 3), uint16(100))
	f.Add(append([]byte{0x00, 0x47}, tsBurst(2, 2)...), uint16(7))
	f.Add(bytes.Repeat([]byte{0x47}, 1000), uint16(188))
	f.Fuzz(func(t *testing.T, data []byte, cut uint16) {
		clock := &fakeClock{t: time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)}
		b := &shift{now: clock.now}
		step := int(cut%512) + 1
		for i := 0; i < len(data); i += step {
			b.scan(data[i:min(len(data), i+step)], int64(i))
			clock.add(shiftMarkEvery)
		}
		for i, m := range b.marks {
			if m.off < 0 || m.off >= int64(len(data)) || (i > 0 && m.off <= b.marks[i-1].off) {
				t.Fatalf("marks %+v of %d bytes", b.marks, len(data))
			}
			if b.ts && data[m.off] != 0x47 {
				t.Fatalf("a mark at %d is not a packet", m.off)
			}
		}
	})
}
