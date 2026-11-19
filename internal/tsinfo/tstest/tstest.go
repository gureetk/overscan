// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tstest writes fictional MPEG-TS streams for tests: a PAT, a PMT
// with H.264 video and English AAC audio, and video pictures whose data
// names them.
package tstest

import (
	"bytes"
	"fmt"
)

const (
	PMTPID   = 0x1000
	VideoPID = 0x100
	AudioPID = 0x101

	// ClockRate is the rate of timestamps, and Frame a picture's length at
	// 25 frames a second.
	ClockRate = 90000
	Frame     = ClockRate / 25

	// PacketSize is the size of a transport stream packet.
	PacketSize = 188
)

// Muxer writes a stream.
type Muxer struct {
	out bytes.Buffer
	cc  map[uint16]byte
}

// New returns a Muxer that has written the PAT and the PMT.
func New() *Muxer {
	m := Bare()
	m.Tables()
	return m
}

// Bare returns a Muxer that has written nothing.
func Bare() *Muxer {
	return &Muxer{cc: map[uint16]byte{}}
}

// Bytes returns the stream written so far.
func (m *Muxer) Bytes() []byte { return m.out.Bytes() }

// SetCounter sets the continuity counter of the next packet on pid.
func (m *Muxer) SetCounter(pid uint16, cc byte) { m.cc[pid] = cc & 0x0F }

// Packet writes one packet, filled out with an adaptation field whose flags
// are flags, such as 0x80 for a discontinuity.
func (m *Muxer) Packet(pid uint16, start bool, payload []byte, flags byte) {
	if len(payload) > 184 {
		panic("tstest: payload too long")
	}
	b := []byte{0x47, byte(pid >> 8 & 0x1F), byte(pid), 0x10 | m.cc[pid]}
	if start {
		b[1] |= 0x40
	}
	if n := 183 - len(payload); n >= 0 {
		b[3] |= 0x20
		b = append(b, byte(n))
		if n > 0 {
			b = append(b, flags)
			b = append(b, bytes.Repeat([]byte{0xFF}, n-1)...)
		}
	}
	b = append(b, payload...)
	m.cc[pid] = (m.cc[pid] + 1) & 0x0F
	m.out.Write(b)
}

// Tables writes the PAT and the PMT.
func (m *Muxer) Tables() {
	pat := []byte{0x00, 0xB0, 13, 0x00, 0x01, 0xC1, 0, 0, 0x00, 0x01, 0xE0 | PMTPID>>8, PMTPID & 0xFF, 0, 0, 0, 0}
	m.Packet(0, true, append([]byte{0}, pat...), 0)
	streams := []byte{
		0x1B, 0xE0 | VideoPID>>8, VideoPID & 0xFF, 0xF0, 0,
		0x0F, 0xE0 | AudioPID>>8, AudioPID & 0xFF, 0xF0, 6, 0x0A, 4, 'e', 'n', 'g', 0,
	}
	pmt := append([]byte{0x02, 0xB0, byte(9 + len(streams) + 4), 0x00, 0x01, 0xC1, 0, 0, 0xE0 | VideoPID>>8, VideoPID & 0xFF, 0xF0, 0}, streams...)
	m.Packet(PMTPID, true, append([]byte{0}, append(pmt, 0, 0, 0, 0)...), 0)
}

// Picture writes a video PES packet with timestamp pts that holds data.
func (m *Muxer) Picture(pts int64, data []byte) {
	m.pes(VideoPID, 0xE0, pts, data)
}

// pes writes a PES packet with the given stream ID and timestamp on pid.
func (m *Muxer) pes(pid uint16, streamID byte, pts int64, data []byte) {
	pes := append([]byte{0, 0, 1, streamID, 0, 0, 0x80, 0x80, 5,
		0x21 | byte(pts>>29)&0x0E, byte(pts >> 22), 0x01 | byte(pts>>14)&0xFE, byte(pts >> 7), 0x01 | byte(pts<<1)&0xFE}, data...)
	for start := true; len(pes) > 0; start = false {
		n := min(len(pes), 184)
		m.Packet(pid, start, pes[:n], 0)
		pes = pes[n:]
	}
}

// Pictures writes pictures first to last, at 25 a second from the start of
// the clock, each holding Content(i, salt).
func (m *Muxer) Pictures(first, last int, salt string) {
	for i := first; i <= last; i++ {
		m.Picture(int64(i)*Frame, Content(i, salt))
	}
}

// Content is the data of picture i: 3000 bytes that differ from picture to
// picture, and with the salt.
func Content(i int, salt string) []byte {
	unit := []byte(fmt.Sprintf("picture %d %s;", i, salt))
	return bytes.Repeat(unit, 3000/len(unit)+1)[:3000]
}

// Counter returns the continuity counter of the next packet on pid.
func (m *Muxer) Counter(pid uint16) byte { return m.cc[pid] }

// AudioFrame writes an audio PES packet with timestamp pts that holds data.
func (m *Muxer) AudioFrame(pts int64, data []byte) {
	m.pes(AudioPID, 0xC0, pts, data)
}

// Program writes pictures first to last as Pictures does, each followed by
// an audio frame with the same timestamp, holding AudioContent(i, salt).
func (m *Muxer) Program(first, last int, salt string) {
	for i := first; i <= last; i++ {
		m.Picture(int64(i)*Frame, Content(i, salt))
		m.AudioFrame(int64(i)*Frame, AudioContent(i, salt))
	}
}

// AudioContent is the data of audio frame i: 300 bytes that name it.
func AudioContent(i int, salt string) []byte {
	unit := []byte(fmt.Sprintf("sound %d %s;", i, salt))
	return bytes.Repeat(unit, 300/len(unit)+1)[:300]
}
