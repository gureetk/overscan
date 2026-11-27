// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hlscut cuts an MPEG-TS stream into the segments of an HLS
// playlist, at its keyframes, without decoding or re-encoding it (D153).
//
// It is written from RFC 8216, for what a segment holds, and from the
// MPEG-TS, H.264 and HEVC standards (ITU-T H.222.0, H.264 and H.265), for
// the tables and the pictures that start a segment.
package hlscut

import (
	"time"

	"overscan.gureet.ca/internal/mpegts"
)

// Segment is a stretch of the stream that a playlist lists.
type Segment struct {
	// Off is the stream's offset at the segment's first byte, and Len its
	// length in bytes.
	Off, Len int64
	// Duration is how long it plays: from the timestamp of its first
	// picture to that of the next segment's.
	Duration time.Duration
	// Key says that it starts with a keyframe, which a player can start to
	// play from; a segment that reached the longest a segment may be
	// without one does not.
	Key bool
	// Discontinuity says that its timestamps do not follow those of the
	// segment before it: the stream broke off, or its clock jumped.
	Discontinuity bool
	// Tables are the stream's PAT and PMT as they were at the segment's
	// start, whole packets, to send before it.
	Tables []byte
}

const (
	clockRate = 90_000 // a timestamp's ticks a second
	// jumpFactor is how many times the longest segment a gap between two
	// timestamps of the clock may be before it counts as a jump.
	jumpFactor = 3
	// maxTablePackets is how many packets of a program map the cutter keeps.
	maxTablePackets = 4
)

// Cutter follows a stream, as its bytes come with their offsets, and says
// where each segment ends. A segment ends at the first keyframe at least
// Min after it began, or, failing one, at the first picture once it has
// lasted Max.
type Cutter struct {
	min, max int64 // in clock ticks
	cut      func(Segment)

	// next is the offset that the next byte should have, and carry the
	// bytes of a packet that a write cut short.
	next    int64
	started bool
	carry   []byte
	aligned bool

	// What the tables say: the program map's PID, the clock's PID and its
	// stream type, and the latest packets of the PAT and of the PMT.
	pmtPID   uint16
	clockPID uint16
	clockTyp byte
	pat      []byte
	pmt      []byte
	pmtNext  int // packets of the PMT still to come after its first

	// The open segment: where it started, its first timestamp, the latest
	// timestamp seen, and the step to it from the one before.
	open     bool
	start    int64
	startPTS int64
	lastPTS  int64
	lastStep int64
	key      bool
	disc     bool
	tables   []byte
	unwrap   mpegts.Unwrapper
	// broke says that the stream broke off since the last segment, so the
	// next one follows a discontinuity.
	broke bool
}

// New returns a cutter that calls cut with each segment as it ends.
func New(min, max time.Duration, cut func(Segment)) *Cutter {
	c := &Cutter{cut: cut}
	c.SetLimits(min, max)
	return c
}

// SetLimits changes how long segments last, from the next segment on.
func (c *Cutter) SetLimits(min, max time.Duration) {
	c.min = ticks(min)
	c.max = max64(ticks(max), c.min)
}

func ticks(d time.Duration) int64 { return int64(d) * clockRate / int64(time.Second) }

func duration(t int64) time.Duration { return time.Duration(t) * time.Second / clockRate }

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// Write takes in p, the stream's bytes from offset at. Bytes that do not
// follow those before them start the stream again: the open segment is
// dropped, and the next follows a discontinuity.
func (c *Cutter) Write(at int64, p []byte) {
	if c.started && at != c.next {
		c.breakOff()
		if at < c.next {
			// What is written again is skipped.
			if skip := c.next - at; skip < int64(len(p)) {
				p, at = p[skip:], c.next
			} else {
				return
			}
		}
	}
	c.started = true
	c.next = at + int64(len(p))
	data := p
	base := at
	if len(c.carry) > 0 {
		base -= int64(len(c.carry))
		data = append(c.carry, p...)
	}
	i := 0
	for i+mpegts.PacketSize <= len(data) {
		if !c.aligned {
			// Two packets in a row, or one at the end of what came.
			if data[i] != mpegts.SyncByte || (i+mpegts.PacketSize < len(data) && data[i+mpegts.PacketSize] != mpegts.SyncByte) {
				i++
				continue
			}
			c.aligned = true
		}
		if data[i] != mpegts.SyncByte {
			c.aligned = false
			c.breakOff()
			continue
		}
		c.packet(base+int64(i), mpegts.Packet(data[i:i+mpegts.PacketSize]))
		i += mpegts.PacketSize
	}
	c.carry = append(c.carry[:0:0], data[i:]...)
}

// breakOff drops the open segment: the stream broke off part way.
func (c *Cutter) breakOff() {
	c.open, c.broke = false, true
	c.carry, c.aligned = nil, false
}

// packet takes in one packet, at offset off.
func (c *Cutter) packet(off int64, p mpegts.Packet) {
	pid := p.PID()
	switch {
	case pid == 0:
		c.readPAT(p)
	case pid == c.pmtPID && c.pmtPID != 0:
		c.readPMT(p)
	case pid == c.clockPID && c.clockPID != 0 && p.Start():
		c.picture(off, p)
	}
}

// readPAT keeps the latest PAT, and follows its first program's map.
func (c *Cutter) readPAT(p mpegts.Packet) {
	if !p.Start() {
		return
	}
	pids := mpegts.PAT(mpegts.Section(p.Payload()))
	if len(pids) == 0 {
		return
	}
	if pids[0] != c.pmtPID {
		c.pmtPID, c.clockPID, c.pmt = pids[0], 0, nil
	}
	c.pat = append(c.pat[:0:0], p...)
}

// readPMT keeps the latest PMT, the packets of a section that starts in p
// and those that follow it, and finds the clock's stream.
func (c *Cutter) readPMT(p mpegts.Packet) {
	if !p.Start() {
		if c.pmtNext > 0 {
			c.pmt = append(c.pmt, p...)
			c.pmtNext--
		}
		return
	}
	payload := p.Payload()
	sec := mpegts.Section(payload)
	if len(sec) < 3 {
		return
	}
	// A section longer than what this packet holds goes on in the next.
	length := 3 + (int(sec[1]&0x0F)<<8 | int(sec[2]))
	c.pmtNext = 0
	if length > len(sec) {
		c.pmtNext = min(maxTablePackets-1, (length-len(sec)+mpegts.PacketSize-5)/(mpegts.PacketSize-4))
	}
	c.pmt = append(c.pmt[:0:0], p...)
	if e, ok := mpegts.Clock(mpegts.PMT(sec)); ok {
		c.clockPID, c.clockTyp = e.PID, e.Type
	}
}

// picture takes in the first packet of a PES packet of the clock's stream:
// a picture, or a frame of sound in a stream without pictures.
func (c *Cutter) picture(off int64, p mpegts.Packet) {
	payload := p.Payload()
	raw, header, ok := mpegts.PTS(payload)
	if !ok {
		return
	}
	pts := c.unwrap.Unwrap(raw)
	key := c.keyframe(p, payload[header:])
	if !c.open {
		if key {
			c.begin(off, pts, true)
		}
		return
	}
	elapsed := pts - c.startPTS
	if elapsed < 0 || elapsed > jumpFactor*c.max+clockRate {
		// The clock jumped: the segment ends with the last picture seen,
		// and the next follows a discontinuity.
		c.end(off, c.lastPTS-c.startPTS+c.lastStep)
		c.broke = true
		c.open = false
		if key {
			c.begin(off, pts, true)
		}
		return
	}
	c.lastPTS, c.lastStep = pts, pts-c.lastPTS
	switch {
	case key && elapsed >= c.min:
		c.end(off, elapsed)
		c.begin(off, pts, true)
	case elapsed >= c.max:
		c.end(off, elapsed)
		c.begin(off, pts, key)
	}
}

// begin opens a segment at off, whose first timestamp is pts.
func (c *Cutter) begin(off, pts int64, key bool) {
	c.open, c.start, c.startPTS, c.lastPTS, c.lastStep, c.key = true, off, pts, pts, 0, key
	c.disc, c.broke = c.broke, false
	c.tables = append(append(make([]byte, 0, len(c.pat)+len(c.pmt)), c.pat...), c.pmt...)
}

// end ends the open segment before off, having lasted d ticks.
func (c *Cutter) end(off, d int64) {
	if off <= c.start {
		return
	}
	c.cut(Segment{Off: c.start, Len: off - c.start, Duration: duration(max64(d, 0)), Key: c.key,
		Discontinuity: c.disc, Tables: c.tables})
}

// keyframe reports whether a PES packet, of which data is the first
// packet's part after the PES header, starts with a picture that a player
// can start from: the packet says so, or, for H.264 and HEVC, the picture
// is an IDR or random access picture, or comes with the sequence's
// parameters. In a stream without pictures, every frame of sound is one.
func (c *Cutter) keyframe(p mpegts.Packet, data []byte) bool {
	if randomAccess(p) {
		return true
	}
	switch c.clockTyp {
	case 0x1B: // H.264
		return nalTypes(data, func(b byte) bool { t := b & 0x1F; return t == 5 || t == 7 })
	case 0x24: // HEVC
		return nalTypes(data, func(b byte) bool { t := b >> 1 & 0x3F; return t >= 16 && t <= 23 || t == 32 || t == 33 })
	case 0x01, 0x02: // MPEG-1 and MPEG-2 video: a sequence header
		return startCode(data, 0xB3)
	}
	kind, _, _ := mpegts.Elementary{Type: c.clockTyp}.Describe()
	return kind == "audio"
}

// randomAccess reports whether a packet's adaptation field marks a place
// that a player can start from.
func randomAccess(p mpegts.Packet) bool {
	return p[3]&0x20 != 0 && p[4] > 0 && 5 < len(p) && p[5]&0x40 != 0
}

// nalTypes reports whether any NAL unit that starts in data has a header
// byte for which is says yes.
func nalTypes(data []byte, is func(byte) bool) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			if is(data[i+3]) {
				return true
			}
			i += 2
		}
	}
	return false
}

// startCode reports whether data holds the start code 00 00 01 code.
func startCode(data []byte, code byte) bool {
	for i := 0; i+3 < len(data); i++ {
		if data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 && data[i+3] == code {
			return true
		}
	}
	return false
}
