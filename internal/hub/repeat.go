// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"io"
	"time"

	"overscan.gureet.ca/internal/mpegts"
)

// The repeat filter passes a continuous MPEG-TS stream on to a player,
// leaving out what the source sends again (D43). Some sources, when their
// stream restarts on their side, send their last seconds again with the
// clock set back. A player that keeps track of what it has played skips
// them, but others, the MAG box among them, play them again, and the
// picture goes back in time.
//
// The filter follows the stream's clock, the timestamps of its first video
// stream (or audio stream, if it has no video), and remembers the pictures
// that it has passed on. When the clock jumps back to a picture that it has
// passed on, the same down to its first 2 KiB, it leaves out each stream's
// packets until that stream passes the point it had reached, so that the
// player gets each moment once. Continuity counters are made to run on over
// what is left out. Pictures that are new but come with an old clock pass on
// as they are, and so does anything that is not MPEG-TS. A stream that
// starts part way through a packet, or after some bytes that are not
// packets, starts at its first whole packet.

const (
	// repeatMinBack is how far the clock must jump back to be checked for a
	// repeat: in decode order, pictures step back a little all the time.
	repeatMinBack = 1 * mpegts.ClockRate

	// repeatHistory caps the pictures remembered: a few minutes of video.
	repeatHistory = 8000

	// repeatMaxHeld caps the packets held back while a jump is checked.
	repeatMaxHeld = 512

	// repeatRelease is how far the clock must run past where it had been
	// before streams that have not caught up after a repeat pass on anyway.
	repeatRelease = 10 * mpegts.ClockRate
)

// Modes of a repeat filter.
const (
	sniffing  = iota // waiting to see whether the stream is MPEG-TS
	filtering        // it is
	passing          // it is not, so it passes on as it is
)

// repeatFilter is an io.Writer that writes what it is given to w, less what
// the source sends again. Close passes on what remains at the end.
type repeatFilter struct {
	w       io.Writer
	dropped func(back time.Duration, bytes int64) // reports a repeat left out
	skipped func(bytes int)                       // reports what came before the first whole packet

	mode int
	in   []byte // bytes not yet read as packets
	out  []byte // packets to write

	pmts     map[uint16]bool
	streams  map[uint16]*esState // the elementary streams of the first program map
	clockPID uint16
	clock    *esState            // nil until a stream with a clock is known
	counters map[uint16]*counter // continuity, by PID

	history map[int64]picture // pictures passed on, by timestamp
	order   []int64           // their timestamps, oldest first

	// The clock's picture being passed on, to remember it.
	passing  bool
	passPTS  int64
	passData []byte

	// Checking a jump back: the packets held back, and the clock's picture
	// that jumped back, with as much of its data as has come.
	holding  bool
	held     []byte
	heldPTS  int64
	heldData []byte

	// Leaving out a repeat: the highest timestamp of the clock before it
	// went back, where it went back to, and what has been left out.
	repeat     bool
	mark       int64
	repeatFrom int64
	leftOut    int64
	catchingUp bool // some stream has not passed its mark yet
}

// esState is what the filter knows of an elementary stream.
type esState struct {
	unwrap  mpegts.Unwrapper
	max     int64 // the highest timestamp passed on
	haveMax bool
	drop    bool  // the current PES packet is left out
	mark    int64 // after a repeat, only what is beyond this passes on
	behind  bool  // the mark applies
}

// counter makes a PID's continuity counters run on over what the filter
// leaves out: it keeps the counter last passed on, how much is added to
// the source's, and whether packets were left out since.
type counter struct {
	last  byte
	delta byte
	gap   bool
	seen  bool
}

// picture is a picture's fingerprint, and how many bytes of its data it
// took in.
type picture struct {
	fp uint64
	n  int
}

// repeatFilter returns a filter that writes to w for s, and logs every
// repeat that it leaves out, and what it leaves out before the first whole
// packet.
func (h *Hub) repeatFilter(w io.Writer, s *session) *repeatFilter {
	f := newRepeatFilter(w, func(back time.Duration, bytes int64) {
		h.log.Info("the source sent a stretch again; the hub left it out", "channel", s.channel,
			"source", h.source(s.sourceID).Name, "back", back.Round(100*time.Millisecond), "bytes", bytes)
	})
	f.skipped = func(bytes int) {
		h.log.Info("a stream did not start with a whole packet; the hub left out what came before the first",
			"channel", s.channel, "source", h.source(s.sourceID).Name, "bytes", bytes)
	}
	return f
}

// newRepeatFilter returns a filter that writes to w and calls dropped,
// which may be nil, for every repeat it leaves out.
func newRepeatFilter(w io.Writer, dropped func(back time.Duration, bytes int64)) *repeatFilter {
	return &repeatFilter{w: w, dropped: dropped, history: map[int64]picture{}, counters: map[uint16]*counter{}}
}

// Write takes in the next piece of the stream.
func (f *repeatFilter) Write(p []byte) (int, error) {
	if f.mode == passing {
		_, err := f.w.Write(p)
		return len(p), err
	}
	f.in = append(f.in, p...)
	f.scan(false)
	return len(p), f.flush()
}

// Close passes on what remains at the end of the stream.
func (f *repeatFilter) Close() error {
	if f.mode == passing {
		return nil
	}
	f.scan(true)
	if f.holding {
		f.decide()
	}
	if f.repeat {
		f.endRepeat()
	}
	f.out = append(f.out, f.in...)
	f.in = f.in[:0]
	return f.flush()
}

func (f *repeatFilter) flush() error {
	if len(f.out) == 0 {
		return nil
	}
	_, err := f.w.Write(f.out)
	f.out = f.out[:0]
	return err
}

// pass gives up on filtering: the stream is not MPEG-TS.
func (f *repeatFilter) pass() {
	f.mode = passing
	f.out = append(f.out, f.in...)
	f.in = nil
}

// scan reads the packets in f.in. A packet is read once the next one is
// seen to start in step, or at the end of the stream.
func (f *repeatFilter) scan(final bool) {
	const size = mpegts.PacketSize
	b := f.in
	if f.mode == sniffing {
		at, ts, known := mpegts.Start(b)
		switch {
		case !known && !final:
			return
		case !ts:
			f.pass()
			return
		}
		if at > 0 {
			b = b[at:]
			if f.skipped != nil {
				f.skipped(at)
			}
		}
		f.mode = filtering
	}
	for len(b) >= size {
		more := len(b) > size
		if b[0] == mpegts.SyncByte && (!more || b[size] == mpegts.SyncByte) {
			if !more && !final {
				break
			}
			f.packet(mpegts.Packet(b[:size]))
			b = b[size:]
			continue
		}
		// Out of step: leave out bytes up to the next packet in step, and
		// the rest of each stream's PES packet, whose start was cut off.
		// The counters run on over the break.
		for _, st := range f.streams {
			st.drop = true
		}
		for _, c := range f.counters {
			c.gap = true
		}
		i := 1
		for i < len(b) && (b[i] != mpegts.SyncByte || (i+size < len(b) && b[i+size] != mpegts.SyncByte)) {
			i++
		}
		b = b[i:]
	}
	f.in = append(f.in[:0], b...)
}

// packet takes in one packet.
func (f *repeatFilter) packet(p mpegts.Packet) {
	if f.holding {
		if p.PID() != f.clockPID || !p.Start() {
			f.held = append(f.held, p...)
			if p.PID() == f.clockPID {
				f.heldData = append(f.heldData, p.Payload()...)
			}
			if len(f.heldData) >= mpegts.FingerprintBytes || len(f.held) >= repeatMaxHeld*mpegts.PacketSize {
				f.decide()
			}
			return
		}
		f.decide() // the clock's next picture starts: the held one is whole
	}
	f.tables(p)
	if f.jumpsBack(p) {
		return
	}
	f.route(p)
}

// tables reads the first program map, through the program association
// table.
func (f *repeatFilter) tables(p mpegts.Packet) {
	if f.streams != nil || !p.Start() {
		return
	}
	switch pid := p.PID(); {
	case pid == 0 && f.pmts == nil:
		if pids := mpegts.PAT(mpegts.Section(p.Payload())); len(pids) > 0 {
			f.pmts = map[uint16]bool{}
			for _, pmt := range pids {
				f.pmts[pmt] = true
			}
		}
	case f.pmts[pid]:
		es := mpegts.PMT(mpegts.Section(p.Payload()))
		if es == nil {
			return
		}
		f.streams = map[uint16]*esState{}
		for _, e := range es {
			if kind, _, _ := e.Describe(); kind != "data" {
				f.streams[e.PID] = &esState{}
			}
		}
		if c, ok := mpegts.Clock(es); ok {
			f.clockPID, f.clock = c.PID, f.streams[c.PID]
		}
	}
}

// jumpsBack starts holding packets back if p starts a picture of the clock
// that jumps back to one that was passed on, until the picture can be
// compared with it. It reports whether it did.
func (f *repeatFilter) jumpsBack(p mpegts.Packet) bool {
	if f.clock == nil || f.repeat || !f.clock.haveMax || p.PID() != f.clockPID || !p.Start() {
		return false
	}
	payload := p.Payload()
	raw, header, ok := mpegts.PTS(payload)
	if !ok {
		return false
	}
	t := f.clock.unwrap.Unwrap(raw)
	if t >= f.clock.max-repeatMinBack {
		return false
	}
	if _, seen := f.history[t]; !seen {
		return false
	}
	f.holding = true
	f.held = append(f.held[:0], p...)
	f.heldPTS = t
	f.heldData = append(f.heldData[:0], payload[header:]...)
	if len(f.heldData) >= mpegts.FingerprintBytes {
		f.decide()
	}
	return true
}

// decide ends holding packets back: if the picture that jumped back is one
// passed on before, a repeat starts. Then the held packets go on their way.
func (f *repeatFilter) decide() {
	f.holding = false
	if pic, seen := f.history[f.heldPTS]; seen && len(f.heldData) >= pic.n && mpegts.Fingerprint(f.heldData[:pic.n]) == pic.fp {
		f.repeat = true
		f.mark, f.repeatFrom, f.leftOut = f.clock.max, f.heldPTS, 0
		for _, st := range f.streams {
			if st != f.clock && st.haveMax {
				st.mark, st.behind = st.max, true
				f.catchingUp = true
			}
		}
	}
	held := f.held
	f.held = nil
	for ; len(held) >= mpegts.PacketSize; held = held[mpegts.PacketSize:] {
		f.route(mpegts.Packet(held[:mpegts.PacketSize]))
	}
}

// route passes a packet on or leaves it out.
func (f *repeatFilter) route(p mpegts.Packet) {
	st := f.streams[p.PID()]
	if st == nil {
		// Tables and padding pass on, except within a repeat, whose
		// tables were sent before.
		if f.repeat {
			f.leave(p)
			return
		}
		f.forward(p)
		return
	}
	payload := p.Payload()
	if p.Start() {
		st.drop = false
		if raw, header, ok := mpegts.PTS(payload); ok {
			t := st.unwrap.Unwrap(raw)
			if st == f.clock {
				f.clockPicture(t, payload[header:])
			} else if st.behind {
				if t <= st.mark {
					st.drop = true
				} else {
					st.behind = false
				}
			}
			if !st.drop && (!st.haveMax || t > st.max) {
				st.max, st.haveMax = t, true
			}
		}
	} else if st == f.clock && f.passing && !st.drop && len(f.passData) < mpegts.FingerprintBytes {
		f.passData = append(f.passData, payload...)
	}
	if st.drop {
		f.leave(p)
		return
	}
	f.forward(p)
}

// leave leaves a packet out.
func (f *repeatFilter) leave(p mpegts.Packet) {
	f.counter(p.PID()).gap = true
	f.leftOut += mpegts.PacketSize
}

// counter returns the continuity counter of pid.
func (f *repeatFilter) counter(pid uint16) *counter {
	c := f.counters[pid]
	if c == nil {
		c = &counter{}
		f.counters[pid] = c
	}
	return c
}

// clockPicture follows the clock through the start of a picture with
// timestamp t, whose data starts with data.
func (f *repeatFilter) clockPicture(t int64, data []byte) {
	st := f.clock
	f.remember()
	switch {
	case f.repeat:
		if _, seen := f.history[t]; seen && t <= f.mark {
			st.drop = true
			return
		}
		f.endRepeat()
	case st.haveMax && t < st.max-repeatMinBack:
		// New pictures with an old clock: they pass on, and the stream
		// starts again from them.
		clear(f.history)
		f.order = f.order[:0]
		st.max = t
		for _, other := range f.streams {
			if other != st {
				other.haveMax, other.behind = false, false
			}
		}
		f.catchingUp = false
	}
	if f.catchingUp && t > f.mark+repeatRelease {
		for _, other := range f.streams {
			other.behind = false
		}
		f.catchingUp = false
	}
	f.passing, f.passPTS = true, t
	f.passData = append(f.passData[:0], data[:min(len(data), mpegts.FingerprintBytes)]...)
}

// remember keeps the fingerprint of the clock's picture that was passed on
// last, forgetting the oldest beyond repeatHistory.
func (f *repeatFilter) remember() {
	if !f.passing {
		return
	}
	f.passing = false
	n := min(len(f.passData), mpegts.FingerprintBytes)
	if _, ok := f.history[f.passPTS]; !ok {
		f.order = append(f.order, f.passPTS)
	}
	f.history[f.passPTS] = picture{fp: mpegts.Fingerprint(f.passData[:n]), n: n}
	if len(f.order) > repeatHistory {
		drop := len(f.order) - repeatHistory*3/4
		for _, old := range f.order[:drop] {
			delete(f.history, old)
		}
		f.order = append(f.order[:0], f.order[drop:]...)
	}
}

// endRepeat reports a repeat left out.
func (f *repeatFilter) endRepeat() {
	f.repeat = false
	if f.dropped != nil {
		f.dropped(time.Duration(f.mark-f.repeatFrom)*time.Second/mpegts.ClockRate, f.leftOut)
	}
}

// forward passes a packet on, with its continuity counter running on from
// the last packet of its PID that was passed on.
func (f *repeatFilter) forward(p mpegts.Packet) {
	start := len(f.out)
	f.out = append(f.out, p...)
	q := mpegts.Packet(f.out[start:])
	c := f.counter(q.PID())
	cc := q.CC()
	if q.HasPayload() {
		if c.gap && c.seen {
			c.delta = (c.last + 1 - cc) & 0x0F
		}
		c.gap = false
		cc = (cc + c.delta) & 0x0F
		c.last, c.seen = cc, true
	} else {
		cc = (cc + c.delta) & 0x0F
	}
	if c.delta != 0 {
		q.SetCC(cc)
	}
}
