// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

// Timeshift (D92). While it is on, a box's live channel plays through a
// buffer on disk. A tuner writes the stream into the buffer as the box
// would get it, after the repeat filter and repair, a file of a few
// megabytes at a time, and the box's requests read from the buffer: from
// its end, or from a while before it. The tuner goes on while the box is
// paused, so that the box picks up where it stopped, and the box can go
// back as far as the buffer keeps.

const (
	// shiftRate is the most bytes a second that a buffer keeps room for,
	// about what a 4K channel sends.
	shiftRate = 2_500_000
	// shiftMarkEvery is how often a buffer notes a place that a player can
	// start from; shiftNoTable how long it waits for a packet of the
	// stream's table before it notes another packet instead.
	shiftMarkEvery = 250 * time.Millisecond
	shiftNoTable   = time.Second
	// shiftMinFree is the disk space that buffers leave: below it, a
	// buffer drops its oldest file instead of growing.
	shiftMinFree = 1 << 30
	// shiftLive is how near the end a request for a while before it
	// plays from the end instead.
	shiftLive = 2 * time.Second
	// shiftNotTS is how much of a stream shows that it is not MPEG-TS, when
	// no packet has been found in it.
	shiftNotTS = 1 << 20
	// tsPacket is the size of an MPEG-TS packet.
	tsPacket = 188
)

// They are variables so that tests can shorten them.
var (
	// shiftChunkBytes is the size of a buffer's files.
	shiftChunkBytes int64 = 8 << 20
	// shiftFirstWait is how long a request waits for the stream's first
	// bytes.
	shiftFirstWait = 30 * time.Second
	// shiftStall is how long a request at the buffer's end waits for more
	// before it ends, so that the player sees the stream stop, as it would
	// without a buffer.
	shiftStall = 20 * time.Second
	// shiftRetry and shiftMaxRetry pace the tuner once its source ends the
	// stream or breaks off: it asks again after shiftRetry, then twice as
	// long each time that nothing came, up to shiftMaxRetry.
	shiftRetry    = time.Second
	shiftMaxRetry = 30 * time.Second
	// shiftTick is how often waiting requests look at the time.
	shiftTick = time.Second
)

var errShiftClosed = errors.New("the timeshift buffer is closed")

// shift is a live session's timeshift buffer. It is the tuner's
// http.ResponseWriter: what the source answers, and the stream it sends,
// go into it.
type shift struct {
	dir  string
	keep time.Duration // how far back it keeps the stream
	most int64         // the most bytes it keeps
	now  func() time.Time
	free func(dir string) (int64, bool) // the disk space left there, if known
	// retry and maxRetry pace its tuner, as shiftRetry and shiftMaxRetry
	// were when it was made.
	retry, maxRetry time.Duration

	mu   sync.Mutex
	cond *sync.Cond // broadcast when bytes come, an answer comes, a tick passes or it closes
	// chunks are its files, oldest first; next numbers the next one.
	chunks []*shiftChunk
	next   int
	end    int64 // the bytes written in all: the stream's offset at the buffer's end
	wrote  time.Time
	marks  []shiftMark
	// header is the tuner's answer to come, status what the source
	// answered the tuner's latest request (0 until it has), and ok the
	// header of the last answer with the stream.
	header http.Header
	status int
	ok     http.Header
	tuning bool // the tuner's request to the source runs now
	closed bool

	// The scan for places to start from: the bytes of a packet that a
	// write cut short, whether the scan follows the stream's packets, and
	// whether the stream has shown any.
	carry   []byte
	aligned bool
	ts      bool
}

// shiftChunk is one of a buffer's files.
type shiftChunk struct {
	f     *os.File
	start int64     // the stream's offset at its first byte
	n     int64     // its bytes
	at    time.Time // when its first byte came
}

// shiftMark is a place that a player can start from: in MPEG-TS, a packet
// of the table that says what the stream holds (PID 0), or, in a stream
// that has gone a while without one, any packet; in another stream, where
// a write began.
type shiftMark struct {
	off   int64
	at    time.Time
	table bool // a packet of PID 0
}

// newShift makes a buffer in dir that keeps keep of a stream.
func newShift(dir string, keep time.Duration, now func() time.Time) (*shift, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	b := &shift{dir: dir, keep: keep, most: int64(keep.Seconds() * shiftRate), now: now, free: freeSpace,
		retry: shiftRetry, maxRetry: shiftMaxRetry, header: http.Header{}}
	b.most = max(b.most, 2*shiftChunkBytes)
	b.cond = sync.NewCond(&b.mu)
	go b.tick(shiftTick)
	return b, nil
}

// tick wakes the requests that wait each d, so that they can see how long
// they have waited, until the buffer closes.
func (b *shift) tick(d time.Duration) {
	t := time.NewTicker(d)
	defer t.Stop()
	for range t.C {
		b.mu.Lock()
		closed := b.closed
		b.cond.Broadcast()
		b.mu.Unlock()
		if closed {
			return
		}
	}
}

// close removes the buffer and wakes its requests, which then end.
func (b *shift) close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	for _, c := range b.chunks {
		_ = c.f.Close()
	}
	b.chunks = nil
	_ = os.RemoveAll(b.dir)
	b.cond.Broadcast()
}

// begin readies the buffer for a request of the tuner to the source.
func (b *shift) begin() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.header, b.status, b.tuning = http.Header{}, 0, true
	// A new request starts a new stream, whose packets the scan finds
	// again.
	b.carry, b.aligned = nil, false
}

// done says that the tuner's request has ended.
func (b *shift) done() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.tuning = false
	if b.status == 0 {
		b.status = http.StatusBadGateway
	}
	b.cond.Broadcast()
}

// refused says that the source gave the tuner no stream, with status.
func (b *shift) refused(status int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = status
	b.cond.Broadcast()
}

// isTuning reports whether the tuner's request to the source runs now.
func (b *shift) isTuning() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.tuning
}

// written returns how many bytes of the stream have come.
func (b *shift) written() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.end
}

// Header, WriteHeader and Write are the tuner's http.ResponseWriter.
func (b *shift) Header() http.Header {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.header
}

func (b *shift) WriteHeader(status int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.status != 0 {
		return
	}
	b.status = status
	if status == http.StatusOK {
		b.ok = b.header.Clone()
	}
	b.cond.Broadcast()
}

func (b *shift) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, errShiftClosed
	}
	if b.status == 0 {
		b.status, b.ok = http.StatusOK, b.header.Clone()
	}
	if b.status != http.StatusOK {
		// The text of an error is not the stream.
		return len(p), nil
	}
	b.scan(p, b.end)
	n := 0
	for len(p) > 0 {
		var c *shiftChunk
		if k := len(b.chunks); k > 0 && b.chunks[k-1].n < shiftChunkBytes {
			c = b.chunks[k-1]
		} else {
			var err error
			if c, err = b.grow(); err != nil {
				return n, err
			}
		}
		k := int(min(int64(len(p)), shiftChunkBytes-c.n))
		if _, err := c.f.WriteAt(p[:k], c.n); err != nil {
			return n, err
		}
		c.n += int64(k)
		b.end += int64(k)
		n += k
		p = p[k:]
	}
	b.wrote = b.now()
	b.cond.Broadcast()
	return n, nil
}

// grow starts a new file, after dropping the oldest files that the buffer
// no longer keeps: those older than keep, and those over its size or the
// disk's free space. It needs b.mu.
func (b *shift) grow() (*shiftChunk, error) {
	now := b.now()
	short := false
	if free, ok := b.free(b.dir); ok && free < shiftMinFree {
		short = true
	}
	for len(b.chunks) > 1 {
		old := b.chunks[1].at.Before(now.Add(-b.keep))
		full := b.end-b.chunks[0].start+shiftChunkBytes > b.most
		if !old && !full && !short {
			break
		}
		b.drop()
		short = false // one file at a time, as the disk allows
	}
	f, err := os.OpenFile(filepath.Join(b.dir, strconv.Itoa(b.next)+".ts"), os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	b.next++
	c := &shiftChunk{f: f, start: b.end, at: now}
	b.chunks = append(b.chunks, c)
	return c, nil
}

// drop removes the buffer's oldest file, and the places to start from in
// it. It needs b.mu.
func (b *shift) drop() {
	c := b.chunks[0]
	_ = c.f.Close()
	_ = os.Remove(c.f.Name())
	b.chunks = b.chunks[1:]
	start := b.end
	if len(b.chunks) > 0 {
		start = b.chunks[0].start
	}
	i := sort.Search(len(b.marks), func(i int) bool { return b.marks[i].off >= start })
	b.marks = b.marks[i:]
}

// scan notes the places to start from in p, which the stream holds at
// offset at: in MPEG-TS, each packet with PID 0, at most one each
// shiftMarkEvery, and a packet of another PID when there has been none for
// shiftNoTable; in a stream that has shown no packet in shiftNotTS, the
// start of each write. It needs b.mu.
func (b *shift) scan(p []byte, at int64) {
	if !b.ts && at >= shiftNotTS {
		b.mark(at, shiftMarkEvery, false)
		return
	}
	data := p
	if len(b.carry) > 0 {
		data = append(b.carry, p...)
	}
	base := at - int64(len(b.carry))
	i := 0
	for i+tsPacket <= len(data) {
		if !b.aligned {
			// Two packets in a row, or one at the end of what came.
			if data[i] != 0x47 || (i+tsPacket < len(data) && data[i+tsPacket] != 0x47) {
				i++
				continue
			}
			b.aligned, b.ts = true, true
		}
		if data[i] != 0x47 {
			b.aligned = false
			continue
		}
		if pid := uint16(data[i+1]&0x1f)<<8 | uint16(data[i+2]); pid == 0 {
			b.mark(base+int64(i), shiftMarkEvery, true)
		} else {
			b.mark(base+int64(i), shiftNoTable, false)
		}
		i += tsPacket
	}
	b.carry = append(b.carry[:0:0], data[i:]...)
}

// mark notes a place to start from, unless one was noted less than every
// ago; a packet of the table takes the place of another packet noted then.
// It needs b.mu.
func (b *shift) mark(off int64, every time.Duration, table bool) {
	now := b.now()
	if n := len(b.marks); n > 0 && now.Sub(b.marks[n-1].at) < every {
		if table && !b.marks[n-1].table {
			b.marks[n-1] = shiftMark{off: off, at: now, table: true}
		}
		return
	}
	b.marks = append(b.marks, shiftMark{off: off, at: now, table: table})
}

// start waits until the buffer has the stream, then returns where a
// request for it as it was behind ago starts: the last place to start
// from at or before then, or the oldest the buffer keeps. It returns the
// source's answer instead when the source gave no stream, or nothing came
// in shiftFirstWait.
func (b *shift) start(ctx context.Context, behind time.Duration) (int, http.Header, int64) {
	ctx, cancel := context.WithTimeout(ctx, shiftFirstWait)
	defer cancel()
	stop := context.AfterFunc(ctx, b.wake)
	defer stop()
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.end == 0 && !b.closed && ctx.Err() == nil && (b.status == 0 || b.status == http.StatusOK || b.tuning) {
		b.cond.Wait()
	}
	switch {
	case b.closed:
		return http.StatusServiceUnavailable, nil, 0
	case b.end == 0 && b.status != 0 && b.status != http.StatusOK:
		return b.status, nil, 0
	case b.end == 0:
		return http.StatusGatewayTimeout, nil, 0
	}
	return http.StatusOK, b.ok, b.place(behind)
}

// place returns where to play from to be behind the end. It needs b.mu.
func (b *shift) place(behind time.Duration) int64 {
	if len(b.marks) == 0 {
		if behind <= shiftLive || len(b.chunks) == 0 {
			return b.end
		}
		return b.chunks[0].start
	}
	want := b.now().Add(-behind)
	i := sort.Search(len(b.marks), func(i int) bool { return b.marks[i].at.After(want) }) - 1
	if behind <= shiftLive || i >= len(b.marks) {
		i = len(b.marks) - 1
	}
	return b.marks[max(i, 0)].off
}

// kept returns how far back the buffer goes now.
func (b *shift) kept() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.marks) == 0 {
		return 0
	}
	return b.now().Sub(b.marks[0].at)
}

// wake wakes the requests that wait.
func (b *shift) wake() {
	b.mu.Lock()
	b.cond.Broadcast()
	b.mu.Unlock()
}

// copyTo sends the stream from off to w, waiting at the buffer's end for
// more, until ctx ends, the buffer closes, or nothing more comes in
// shiftStall. A request that falls behind what the buffer keeps goes on
// from the oldest place to start from.
func (b *shift) copyTo(ctx context.Context, w io.Writer, flush func(), off int64) {
	stop := context.AfterFunc(ctx, b.wake)
	defer stop()
	buf := make([]byte, copyBufferBytes)
	waited := b.now()
	for {
		b.mu.Lock()
		for off >= b.end && !b.closed && ctx.Err() == nil {
			last := waited
			if b.wrote.After(last) {
				last = b.wrote
			}
			if b.now().Sub(last) > shiftStall {
				b.mu.Unlock()
				return
			}
			b.cond.Wait()
		}
		if b.closed || ctx.Err() != nil {
			b.mu.Unlock()
			return
		}
		if len(b.chunks) == 0 {
			b.mu.Unlock()
			return
		}
		if off < b.chunks[0].start {
			off = b.chunks[0].start
			if len(b.marks) > 0 {
				off = b.marks[0].off
			}
		}
		c := b.chunks[len(b.chunks)-1]
		for _, k := range b.chunks {
			if off < k.start+k.n {
				c = k
				break
			}
		}
		n, err := c.f.ReadAt(buf[:min(int64(len(buf)), c.start+c.n-off)], off-c.start)
		b.mu.Unlock()
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flush()
			off += int64(n)
			waited = b.now()
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return
		}
	}
}

// UseTimeshift gives the hub a folder for timeshift buffers (D92), and
// removes the buffers that a stop of the server left there. Without it,
// boxes' live channels play as they come.
func (h *Hub) UseTimeshift(dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		// A buffer is a folder named by its session's nonce, in hex.
		if _, err := strconv.ParseUint(e.Name(), 16, 64); err == nil && e.IsDir() {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	h.shiftDir = dir
	return nil
}

// openShift makes the timeshift buffer of a box's session that is about
// to open, while timeshift is on; nil if it is off, or the buffer cannot
// be made, and the channel then plays as it comes.
func (h *Hub) openShift(ctx context.Context, c claims, name string) *shift {
	if h.shiftDir == "" {
		return nil
	}
	minutes, err := h.store.Timeshift(ctx)
	if err != nil {
		h.log.Error("cannot read the timeshift setting", "error", err)
		return nil
	}
	if minutes == 0 {
		return nil
	}
	b, err := newShift(filepath.Join(h.shiftDir, strconv.FormatUint(c.nonce, 16)), time.Duration(minutes)*time.Minute, h.now)
	if err != nil {
		h.log.Warn("cannot make a timeshift buffer, so the channel plays as it comes", "channel", name, "error", err)
		return nil
	}
	return b
}

// tune keeps a session's timeshift buffer fed from its source while the
// session lasts, then removes the buffer. Once the source ends the stream
// or breaks off, it asks again after shiftRetry, then twice as long each
// time that nothing came, up to shiftMaxRetry.
func (h *Hub) tune(s *session) {
	defer s.shift.close()
	src := h.source(s.sourceID).Name
	h.log.Info("a box's channel goes through a timeshift buffer", "channel", s.channel, "source", src,
		"device", s.deviceID, "keeps", s.shift.keep)
	wait := s.shift.retry
	for first := true; s.ctx.Err() == nil; first = false {
		target := s.streamURL
		if s.ref != "" && !first {
			// A source that resolves gives a new address each time.
			res, err := h.resolve(s.ctx, s.sourceID, PlaysChannel, s.ref)
			if err != nil {
				if s.ctx.Err() != nil {
					return
				}
				h.log.Warn("the source gave no address for a stream", "channel", s.channel, "source", src,
					"device", s.deviceID, "error", err)
				s.shift.refused(http.StatusBadGateway)
				if sleepContext(s.ctx, wait) != nil {
					return
				}
				wait = min(2*wait, s.shift.maxRetry)
				continue
			}
			target = res.Address
		}
		before := s.shift.written()
		s.shift.begin()
		h.copyIn(s.ctx, s.shift, s, target, true)
		s.shift.done()
		if s.shift.written() > before {
			wait = s.shift.retry
		} else {
			wait = min(2*wait, s.shift.maxRetry)
		}
		if sleepContext(s.ctx, wait) != nil {
			return
		}
	}
}

// serveShift plays a box's live session that has a timeshift buffer from
// the buffer: from its end, or, with ?behind=<seconds>, from that long
// before it.
func (h *Hub) serveShift(w http.ResponseWriter, r *http.Request, s *session) {
	var behind time.Duration
	if v := r.URL.Query().Get("behind"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		if err != nil || math.IsNaN(n) || n < 0 {
			http.Error(w, "behind is a number of seconds", http.StatusBadRequest)
			return
		}
		behind = time.Duration(min(n, s.shift.keep.Seconds()) * float64(time.Second))
	}

	h.begin(s)
	defer h.finish(s)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	status, header, off := s.shift.start(ctx, behind)
	if status != http.StatusOK {
		if ctx.Err() == nil {
			http.Error(w, shiftRefusal(status), status)
		}
		return
	}
	if ct := header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(http.StatusOK)
	if behind > 0 {
		h.log.Info("a box plays its channel from its timeshift buffer", "channel", s.channel,
			"behind", behind.Round(time.Second), "kept", s.shift.kept().Round(time.Second), "device", s.deviceID)
	}
	rc := http.NewResponseController(w)
	s.shift.copyTo(ctx, w, func() { _ = rc.Flush() }, off)
}

// shiftRefusal says why a request of a timeshift buffer gets no stream.
func shiftRefusal(status int) string {
	switch status {
	case http.StatusGatewayTimeout:
		return "the source sent nothing"
	case http.StatusServiceUnavailable:
		return "this stream has ended"
	}
	return fmt.Sprintf("the source gave no stream (%d %s)", status, http.StatusText(status))
}
