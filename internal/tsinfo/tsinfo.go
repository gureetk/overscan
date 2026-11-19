// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tsinfo watches an MPEG-TS stream go by and describes it: its
// streams, the packets lost or scrambled, and its clock. The clock is the
// presentation timestamps of the first video stream, or of the first audio
// stream if there is no video. When the clock jumps back, tsinfo tells
// whether the pictures after the jump repeat ones it has already seen, by
// comparing their first bytes. Overscan uses it to find out what is wrong
// with a channel.
package tsinfo

import (
	"time"

	"overscan.gureet.ca/internal/mpegts"
)

const (
	packetSize = mpegts.PacketSize
	syncByte   = mpegts.SyncByte
	clockRate  = mpegts.ClockRate

	// minJumpBack and minJumpAhead are how far the clock must move against
	// the run of the stream to count as a jump: in decode order, pictures
	// step back a little all the time.
	minJumpBack  = 1 * clockRate
	minJumpAhead = 3 * clockRate

	// maxHistory caps the pictures remembered to recognise a repeat: some
	// minutes of video.
	maxHistory = 30000

	// notTSAfter is how much data without two packets in a row means that
	// the stream is not MPEG-TS.
	notTSAfter = 64 << 10
)

// Stream is an elementary stream, as the stream's first program map lists it.
type Stream struct {
	PID      uint16
	Kind     string // video, audio, subtitles or data
	Codec    string // such as H.264 or AAC
	Language string // from an ISO 639 language descriptor; "" if none
}

// Jump is a place where the clock jumped.
type Jump struct {
	At     time.Duration // how far into the reading
	By     time.Duration // how far the clock moved; negative is back
	Repeat bool          // a jump back to pictures already seen
}

// Clock is what the timestamps of one stream did.
type Clock struct {
	PID   uint16
	Kind  string
	Codec string
	// Covered is the time that the timestamps moved on through new
	// pictures, jumps and repeats aside.
	Covered time.Duration
	// Repeated is the time that they moved through pictures seen before.
	Repeated time.Duration
	Jumps    []Jump
}

// Report is what an Analyzer has seen.
type Report struct {
	Bytes      int64
	Elapsed    time.Duration
	LongestGap time.Duration // the longest wait between pieces of data
	TS         bool          // the data is MPEG-TS
	Start      []byte        // the first bytes of the data

	Programs        int
	Streams         []Stream // of the first program map seen
	Clock           *Clock   // nil if no stream with timestamps was found
	SyncLosses      int      // times the packets fell out of step
	Lost            int      // packets missing, by their continuity counters
	Scrambled       int64    // packets that are encrypted
	Discontinuities int      // packets that announce a discontinuity
}

// Analyzer reads a stream as it arrives: write the stream to it, then ask
// for its Report.
type Analyzer struct {
	elapsed func() time.Duration // time since the reading began
	at      time.Duration        // when the data being read arrived
	r       Report

	buf    []byte
	synced bool // the next byte starts a packet
	locked bool // packets were found
	gaveUp bool // not MPEG-TS

	pmts     map[uint16]bool
	pmtRead  bool
	cc       map[uint16]byte
	clockPID uint16

	pes        []byte // the start of the clock stream's current PES packet
	collecting bool
	unwrapper  mpegts.Unwrapper

	maxPTS    int64
	havePTS   bool
	repeating bool
	history   map[int64]uint64 // picture fingerprints by timestamp
	order     []int64          // timestamps in history, oldest first
}

// New returns an Analyzer that tells the time since the reading began with
// elapsed.
func New(elapsed func() time.Duration) *Analyzer {
	return &Analyzer{
		elapsed: elapsed,
		pmts:    map[uint16]bool{},
		cc:      map[uint16]byte{},
		history: map[int64]uint64{},
	}
}

// Write reads the next piece of the stream. It never fails.
func (a *Analyzer) Write(p []byte) (int, error) {
	now := a.elapsed()
	if a.r.Bytes > 0 && now-a.at > a.r.LongestGap {
		a.r.LongestGap = now - a.at
	}
	a.at = now
	a.r.Bytes += int64(len(p))
	if n := min(len(p), 16-len(a.r.Start)); n > 0 {
		a.r.Start = append(a.r.Start, p[:n]...)
	}
	if !a.gaveUp {
		a.buf = append(a.buf, p...)
		a.scan()
	}
	return len(p), nil
}

// Report returns what the Analyzer has seen so far.
func (a *Analyzer) Report() Report {
	a.finishPES()
	r := a.r
	r.Elapsed = a.elapsed()
	r.TS = a.locked
	if r.Clock != nil {
		c := *r.Clock
		r.Clock = &c
	}
	return r
}

// scan reads the whole packets in a.buf.
func (a *Analyzer) scan() {
	b := a.buf
	for len(b) >= packetSize {
		if b[0] == syncByte && (a.synced || (len(b) >= 2*packetSize && b[packetSize] == syncByte)) {
			a.synced, a.locked = true, true
			a.packet(b[:packetSize])
			b = b[packetSize:]
			continue
		}
		if b[0] == syncByte && len(b) < 2*packetSize {
			break // wait to see whether the next packet starts where it should
		}
		if a.synced {
			a.r.SyncLosses++
			a.synced = false
		}
		i := 1
		for i < len(b) && b[i] != syncByte {
			i++
		}
		b = b[i:]
	}
	if !a.locked && a.r.Bytes > notTSAfter {
		a.gaveUp = true
		b = nil
	}
	a.buf = append(a.buf[:0], b...)
}

// packet reads one transport stream packet.
func (a *Analyzer) packet(b []byte) {
	p := mpegts.Packet(b)
	pid := p.PID()
	if pid == mpegts.NullPID {
		return
	}
	discontinuity := p.Discontinuity()
	if discontinuity {
		a.r.Discontinuities++
	}
	payload := p.Payload()
	if !p.HasPayload() {
		return // the counter does not move
	}
	cc := p.CC()
	if last, ok := a.cc[pid]; ok && !discontinuity && cc != last && cc != (last+1)&0x0F {
		a.r.Lost += int((cc - last - 1) & 0x0F)
	}
	a.cc[pid] = cc
	if p.Scrambled() {
		a.r.Scrambled++
		return
	}
	switch {
	case payload == nil:
	case pid == 0:
		if p.Start() && a.r.Programs == 0 {
			for _, pmt := range mpegts.PAT(mpegts.Section(payload)) {
				a.pmts[pmt] = true
				a.r.Programs++
			}
		}
	case a.pmts[pid]:
		if p.Start() && !a.pmtRead {
			a.readPMT(mpegts.Section(payload))
		}
	case a.r.Clock != nil && pid == a.clockPID:
		a.clockData(payload, p.Start())
	}
}

// readPMT reads the first program map: the program's streams, and which of
// them makes the clock.
func (a *Analyzer) readPMT(sec []byte) {
	streams := mpegts.PMT(sec)
	if streams == nil {
		return
	}
	a.pmtRead = true
	for _, e := range streams {
		kind, codec, language := e.Describe()
		a.r.Streams = append(a.r.Streams, Stream{PID: e.PID, Kind: kind, Codec: codec, Language: language})
	}
	if e, ok := mpegts.Clock(streams); ok {
		kind, codec, _ := e.Describe()
		a.clockPID = e.PID
		a.r.Clock = &Clock{PID: e.PID, Kind: kind, Codec: codec}
	}
}

// clockData reads a packet of the clock's stream, collecting the start of
// each PES packet: its header, with the timestamp, and enough of its data to
// tell it from others.
func (a *Analyzer) clockData(payload []byte, start bool) {
	if start {
		a.finishPES()
		a.pes = append(a.pes[:0], payload...)
		a.collecting = true
	} else if a.collecting {
		a.pes = append(a.pes, payload...)
	}
	if a.collecting && len(a.pes) >= 9 && len(a.pes) >= 9+int(a.pes[8])+mpegts.FingerprintBytes {
		a.finishPES()
	}
}

// finishPES reads the PES packet collected so far.
func (a *Analyzer) finishPES() {
	if !a.collecting {
		return
	}
	a.collecting = false
	if pts, header, ok := mpegts.PTS(a.pes); ok {
		a.picture(a.unwrapper.Unwrap(pts), mpegts.Fingerprint(a.pes[header:]))
	}
}

// picture follows the clock through one more picture (or audio frame) with
// timestamp pts, whose data has fingerprint fp.
func (a *Analyzer) picture(pts int64, fp uint64) {
	c := a.r.Clock
	defer a.remember(pts, fp)
	if !a.havePTS {
		a.maxPTS, a.havePTS = pts, true
		return
	}
	seen, ok := a.history[pts]
	again := ok && seen == fp
	switch d := pts - a.maxPTS; {
	case d < -minJumpBack:
		c.Jumps = append(c.Jumps, Jump{At: a.at, By: ticks(d), Repeat: again})
		a.maxPTS, a.repeating = pts, again
	case d > minJumpAhead:
		c.Jumps = append(c.Jumps, Jump{At: a.at, By: ticks(d)})
		a.maxPTS, a.repeating = pts, false
	case d > 0:
		if a.repeating && again {
			c.Repeated += ticks(d)
		} else {
			c.Covered += ticks(d)
			a.repeating = false
		}
		a.maxPTS = pts
	}
}

// remember keeps a picture's fingerprint, forgetting the oldest beyond
// maxHistory.
func (a *Analyzer) remember(pts int64, fp uint64) {
	if _, ok := a.history[pts]; !ok {
		a.order = append(a.order, pts)
	}
	a.history[pts] = fp
	if len(a.order) > maxHistory {
		drop := len(a.order) - maxHistory*3/4
		for _, old := range a.order[:drop] {
			delete(a.history, old)
		}
		a.order = append(a.order[:0], a.order[drop:]...)
	}
}

// ticks converts a timestamp difference to a duration.
func ticks(d int64) time.Duration {
	return time.Duration(d) * time.Second / clockRate
}
