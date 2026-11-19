// SPDX-License-Identifier: AGPL-3.0-or-later

// Package mpegts reads the parts of MPEG-TS (ISO/IEC 13818-1) that Overscan
// needs: transport packets, the program association and program map
// tables, and the timestamps of PES packets.
package mpegts

import (
	"fmt"
	"hash/fnv"
)

const (
	// PacketSize is the size of a transport packet, and SyncByte its first
	// byte.
	PacketSize = 188
	SyncByte   = 0x47

	// NullPID is the PID of padding packets.
	NullPID = 0x1FFF

	// ClockRate is the rate of timestamps: 90 kHz.
	ClockRate = 90000

	// timestampWrap is where the 33-bit timestamps wrap around.
	timestampWrap = 1 << 33

	// startWithin is how far into a stream that does not start with a
	// packet its first whole packet is looked for, and startRun how many
	// packets in step must follow from there: a few more than the two that
	// a stream starting with a packet needs, so that data of another kind
	// does not pass for MPEG-TS by chance.
	startWithin = 2048
	startRun    = 5

	// StartBytes is as much of a stream's start as Start needs to tell
	// whether it is MPEG-TS.
	StartBytes = startWithin + (startRun-1)*PacketSize + 1
)

// Start tells whether b, the first bytes of a stream, is MPEG-TS, and at
// what offset its first whole packet begins. A stream is MPEG-TS from its
// first byte if it starts with a packet and the next starts in step. One
// that starts part way through a packet, or after some bytes that are not
// packets, is MPEG-TS from the first of startRun packets in step that
// begins within startWithin bytes. known reports whether b is long enough
// to tell; it always is once it holds StartBytes.
func Start(b []byte) (at int, ts, known bool) {
	if len(b) > 0 && b[0] == SyncByte {
		if len(b) <= PacketSize {
			return 0, false, false
		}
		if b[PacketSize] == SyncByte {
			return 0, true, true
		}
	}
	const span = (startRun - 1) * PacketSize
	for i := 0; i <= startWithin; i++ {
		switch {
		case i >= len(b) || (b[i] == SyncByte && i+span >= len(b)):
			return 0, false, false // the bytes still to come may tell
		case b[i] == SyncByte && inStep(b[i:], startRun):
			return i, true, true
		}
	}
	return 0, false, true
}

// inStep reports whether b starts with n packets in a row, each PacketSize
// bytes after the one before.
func inStep(b []byte, n int) bool {
	for k := 0; k < n; k++ {
		if b[k*PacketSize] != SyncByte {
			return false
		}
	}
	return true
}

// Packet is a transport packet of PacketSize bytes.
type Packet []byte

// PID returns the packet's PID.
func (p Packet) PID() uint16 { return uint16(p[1]&0x1F)<<8 | uint16(p[2]) }

// Start reports whether a PES packet or a table section starts in the
// packet's payload.
func (p Packet) Start() bool { return p[1]&0x40 != 0 }

// Scrambled reports whether the packet's payload is encrypted.
func (p Packet) Scrambled() bool { return p[3]>>6 != 0 }

// HasPayload reports whether the packet carries a payload; only such
// packets advance the continuity counter.
func (p Packet) HasPayload() bool { return p[3]&0x10 != 0 }

// CC returns the packet's continuity counter.
func (p Packet) CC() byte { return p[3] & 0x0F }

// SetCC sets the packet's continuity counter.
func (p Packet) SetCC(cc byte) { p[3] = p[3]&0xF0 | cc&0x0F }

// adaptationLength returns the length of the adaptation field with its
// length byte, or -1 if the field is not valid.
func (p Packet) adaptationLength() int {
	if p[3]&0x20 == 0 {
		return 0
	}
	n := int(p[4])
	if n > PacketSize-5 {
		return -1
	}
	return 1 + n
}

// Discontinuity reports whether the packet announces a discontinuity: in
// the clock, or in the continuity counter.
func (p Packet) Discontinuity() bool {
	n := p.adaptationLength()
	return n > 1 && p[5]&0x80 != 0
}

// Payload returns the packet's payload; nil if it has none or its
// adaptation field is not valid.
func (p Packet) Payload() []byte {
	n := p.adaptationLength()
	if n < 0 || !p.HasPayload() {
		return nil
	}
	return p[4+n:]
}

// Section returns the table section that starts in a payload, after its
// pointer field, up to the end of the payload.
func Section(payload []byte) []byte {
	if len(payload) == 0 || 1+int(payload[0]) > len(payload) {
		return nil
	}
	return payload[1+int(payload[0]):]
}

// sectionBody returns the body of a table section with the given table ID,
// after its fixed header of n bytes and before its CRC, within what the
// section holds.
func sectionBody(sec []byte, tableID byte, n int) []byte {
	if len(sec) < n || sec[0] != tableID {
		return nil
	}
	end := min(3+(int(sec[1]&0x0F)<<8|int(sec[2]))-4, len(sec))
	if end < n {
		return nil
	}
	return sec[n:end]
}

// PAT returns the PIDs of the program maps that a program association
// section lists, leaving out the network PID.
func PAT(sec []byte) []uint16 {
	body := sectionBody(sec, 0x00, 8)
	var pids []uint16
	for ; len(body) >= 4; body = body[4:] {
		if program := uint16(body[0])<<8 | uint16(body[1]); program != 0 {
			pids = append(pids, uint16(body[2]&0x1F)<<8|uint16(body[3]))
		}
	}
	return pids
}

// Elementary is an elementary stream that a program map lists.
type Elementary struct {
	PID         uint16
	Type        byte   // the stream type
	Descriptors []byte // its descriptors, as they are
}

// PMT returns the elementary streams that a program map section lists.
func PMT(sec []byte) []Elementary {
	if len(sec) < 12 {
		return nil
	}
	info := int(sec[10]&0x0F)<<8 | int(sec[11])
	body := sectionBody(sec, 0x02, 12)
	if info > len(body) {
		return nil
	}
	var out []Elementary
	for body = body[info:]; len(body) >= 5; {
		n := int(body[3]&0x0F)<<8 | int(body[4])
		out = append(out, Elementary{
			PID:         uint16(body[1]&0x1F)<<8 | uint16(body[2]),
			Type:        body[0],
			Descriptors: body[5:min(5+n, len(body))],
		})
		body = body[min(5+n, len(body)):]
	}
	return out
}

// PTS returns the presentation timestamp of the PES packet that b starts
// with, and how long its header is, so that its data follows at b[header:].
// It reports false if b does not start a PES packet with a timestamp.
func PTS(b []byte) (pts int64, header int, ok bool) {
	if len(b) < 9 || b[0] != 0 || b[1] != 0 || b[2] != 1 || b[7]&0x80 == 0 {
		return 0, 0, false
	}
	n := int(b[8])
	if n < 5 || len(b) < 9+n {
		return 0, 0, false
	}
	p := b[9:14]
	pts = int64(p[0]>>1&0x07)<<30 | int64(p[1])<<22 | int64(p[2]>>1)<<15 | int64(p[3])<<7 | int64(p[4]>>1)
	return pts, 9 + n, true
}

// Unwrapper turns 33-bit timestamps into ones that keep counting past the
// wrap, given each timestamp of a stream in turn.
type Unwrapper struct {
	last   int64
	offset int64
	seen   bool
}

// Unwrap returns raw, unwrapped.
func (u *Unwrapper) Unwrap(raw int64) int64 {
	if u.seen {
		switch d := raw - u.last; {
		case d < -timestampWrap/2:
			u.offset += timestampWrap
		case d > timestampWrap/2:
			u.offset -= timestampWrap
		}
	}
	u.last, u.seen = raw, true
	return raw + u.offset
}

// streamTypes names the stream types of a program map: kind and codec.
var streamTypes = map[byte][2]string{
	0x01: {"video", "MPEG-1 video"},
	0x02: {"video", "MPEG-2 video"},
	0x10: {"video", "MPEG-4 video"},
	0x1B: {"video", "H.264"},
	0x24: {"video", "HEVC"},
	0x33: {"video", "VVC"},
	0x03: {"audio", "MPEG audio"},
	0x04: {"audio", "MPEG audio"},
	0x0F: {"audio", "AAC"},
	0x11: {"audio", "AAC (LATM)"},
	0x81: {"audio", "AC-3"},
	0x87: {"audio", "E-AC-3"},
	0x15: {"data", "metadata"},
	0x86: {"data", "SCTE-35 cues"},
}

// Describe names an elementary stream by its type and descriptors: its
// kind (video, audio, subtitles or data), its codec, and its language if a
// descriptor gives one.
func (e Elementary) Describe() (kind, codec, language string) {
	kind, codec = "data", fmt.Sprintf("stream type 0x%02X", e.Type)
	if t, ok := streamTypes[e.Type]; ok {
		kind, codec = t[0], t[1]
	}
	for d := e.Descriptors; len(d) >= 2 && 2+int(d[1]) <= len(d); d = d[2+int(d[1]):] {
		tag, body := d[0], d[2:2+int(d[1])]
		switch {
		case tag == 0x0A && len(body) >= 3:
			language = string(body[:3])
		case e.Type != 0x06:
		case tag == 0x6A:
			kind, codec = "audio", "AC-3"
		case tag == 0x7A:
			kind, codec = "audio", "E-AC-3"
		case tag == 0x7B:
			kind, codec = "audio", "DTS"
		case tag == 0x56:
			kind, codec = "subtitles", "teletext"
		case tag == 0x59:
			kind, codec = "subtitles", "DVB subtitles"
		case tag == 0x05 && len(body) >= 4:
			switch string(body[:4]) {
			case "AC-3":
				kind, codec = "audio", "AC-3"
			case "EAC3":
				kind, codec = "audio", "E-AC-3"
			case "Opus":
				kind, codec = "audio", "Opus"
			}
		}
	}
	return kind, codec, language
}

// Clock returns the stream whose timestamps make a program's clock: its
// first video stream, or its first audio stream if it has no video. It
// reports false if it has neither.
func Clock(streams []Elementary) (Elementary, bool) {
	for _, want := range []string{"video", "audio"} {
		for _, e := range streams {
			if kind, _, _ := e.Describe(); kind == want {
				return e, true
			}
		}
	}
	return Elementary{}, false
}

// FingerprintBytes is how much of a picture's data identifies it.
const FingerprintBytes = 2048

// Fingerprint identifies a picture by the first FingerprintBytes of its
// data, after the PES header.
func Fingerprint(data []byte) uint64 {
	h := fnv.New64a()
	h.Write(data[:min(len(data), FingerprintBytes)])
	return h.Sum64()
}
