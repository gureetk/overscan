// SPDX-License-Identifier: AGPL-3.0-or-later

package hlscut

import (
	"bytes"
	"testing"
	"time"

	"overscan.gureet.ca/internal/mpegts"
)

const (
	pmtPID   = 0x1000
	videoPID = 0x0100
	audioPID = 0x0101
)

// stream builds a fictional MPEG-TS stream, packet by packet.
type stream struct {
	b  bytes.Buffer
	cc map[uint16]byte
}

func newStream() *stream { return &stream{cc: map[uint16]byte{}} }

// packet writes a packet of pid: an adaptation field with flags if af is
// not -1, then payload, padded with 0xFF.
func (s *stream) packet(pid uint16, start bool, af int, payload []byte) {
	p := make([]byte, mpegts.PacketSize)
	p[0] = mpegts.SyncByte
	p[1] = byte(pid >> 8 & 0x1F)
	if start {
		p[1] |= 0x40
	}
	p[2] = byte(pid)
	p[3] = 0x10 | s.cc[pid]&0x0F
	s.cc[pid]++
	i := 4
	if af >= 0 {
		p[3] |= 0x20
		p[4], p[5] = 1, byte(af)
		i = 6
	}
	n := copy(p[i:], payload)
	for j := i + n; j < len(p); j++ {
		p[j] = 0xFF
	}
	s.b.Write(p)
}

// tables writes a PAT, and a PMT of one program with an H.264 video
// stream and an AAC one, or AAC alone.
func (s *stream) tables(video bool) {
	pat := []byte{0, 0x00, 0xB0, 13, 0x00, 0x01, 0xC1, 0, 0, 0x00, 0x01, 0xE0 | pmtPID>>8, pmtPID & 0xFF, 1, 2, 3, 4}
	s.packet(0, true, -1, pat)
	body := []byte{0x02, 0xB0, 0, 0x00, 0x01, 0xC1, 0, 0, 0xE0 | videoPID>>8, videoPID & 0xFF, 0xF0, 0}
	if video {
		body = append(body, 0x1B, 0xE0|videoPID>>8, videoPID&0xFF, 0xF0, 0)
	}
	body = append(body, 0x0F, 0xE0|audioPID>>8, audioPID&0xFF, 0xF0, 0, 1, 2, 3, 4)
	body[2] = byte(len(body) - 3)
	s.packet(pmtPID, true, -1, append([]byte{0}, body...))
}

// pes writes the first packet of a PES packet of pid with a timestamp of
// pts, marked as a place to start from if rai, holding data.
func (s *stream) pes(pid uint16, pts int64, rai bool, data []byte) {
	af := 0
	if rai {
		af = 0x40
	}
	h := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 5,
		byte(0x21 | pts>>29&0x0E), byte(pts >> 22), byte(pts>>14&0xFE | 1), byte(pts >> 7), byte(pts<<1&0xFE | 1)}
	s.packet(pid, true, af, append(h, data...))
	// The rest of the picture.
	s.packet(pid, false, -1, []byte{1, 2, 3})
}

var (
	idr    = []byte{0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x67, 0x64, 0, 0, 1, 0x65}
	nonIDR = []byte{0, 0, 0, 1, 0x09, 0xF0, 0, 0, 0, 1, 0x41}
)

// video writes seconds of 25 pictures a second, a keyframe every gop
// pictures, with tables every second, from pts; rai says whether keyframes
// are marked in the adaptation field too.
func (s *stream) video(pts int64, seconds, gop int, rai bool) int64 {
	for i := range seconds * 25 {
		if i%25 == 0 {
			s.tables(true)
		}
		key := i%gop == 0
		data := nonIDR
		if key {
			data = idr
		}
		s.pes(videoPID, pts, key && rai, data)
		s.pes(audioPID, pts, false, []byte{0xFF, 0xF1})
		pts += clockRate / 25
	}
	return pts
}

// cutAll feeds b to a cutter in pieces of the given sizes, in turn, and
// returns the segments that it cut.
func cutAll(b []byte, min, max time.Duration, sizes ...int) []Segment {
	var got []Segment
	c := New(min, max, func(s Segment) { got = append(got, s) })
	if len(sizes) == 0 {
		sizes = []int{len(b)}
	}
	for at, k := 0, 0; at < len(b); k++ {
		n := min64(sizes[k%len(sizes)], len(b)-at)
		c.Write(int64(at), b[at:at+n])
		at += n
	}
	return got
}

func min64(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// checkSegments checks that segments follow one another, start with a
// keyframe where key says, and carry the tables.
func checkSegments(t *testing.T, b []byte, segs []Segment) {
	t.Helper()
	for i, s := range segs {
		if i > 0 && segs[i-1].Off+segs[i-1].Len != s.Off {
			t.Errorf("segment %d starts at %d, after one that ends at %d", i, s.Off, segs[i-1].Off+segs[i-1].Len)
		}
		if s.Off%mpegts.PacketSize != 0 || s.Len%mpegts.PacketSize != 0 {
			t.Errorf("segment %d is not whole packets: %d+%d", i, s.Off, s.Len)
		}
		p := mpegts.Packet(b[s.Off : s.Off+mpegts.PacketSize])
		if p.PID() != videoPID || !p.Start() {
			t.Errorf("segment %d starts with PID %#x, start %v", i, p.PID(), p.Start())
		}
		if len(s.Tables) != 2*mpegts.PacketSize || mpegts.Packet(s.Tables).PID() != 0 ||
			mpegts.Packet(s.Tables[mpegts.PacketSize:]).PID() != pmtPID {
			t.Errorf("segment %d's tables: %d bytes", i, len(s.Tables))
		}
	}
}

func durations(segs []Segment) []time.Duration {
	var d []time.Duration
	for _, s := range segs {
		d = append(d, s.Duration)
	}
	return d
}

func TestCutsAtKeyframes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		gop      int // pictures between keyframes
		min, max time.Duration
		want     time.Duration // each segment's length
		key      bool
	}{
		{"a keyframe every 2 s", 50, 4 * time.Second, 10 * time.Second, 4 * time.Second, true},
		{"every second", 25, 4 * time.Second, 10 * time.Second, 4 * time.Second, true},
		{"every 3 s", 75, 4 * time.Second, 10 * time.Second, 6 * time.Second, true},
		{"every 5 s", 125, 4 * time.Second, 10 * time.Second, 5 * time.Second, true},
		{"every 12 s, cut at 6 s", 300, 4 * time.Second, 6 * time.Second, 6 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newStream()
			s.video(900_000, 60, tc.gop, true)
			b := s.b.Bytes()
			segs := cutAll(b, tc.min, tc.max)
			if len(segs) < 4 {
				t.Fatalf("%d segments: %v", len(segs), durations(segs))
			}
			checkSegments(t, b, segs)
			for i, seg := range segs {
				if seg.Duration != tc.want {
					t.Errorf("segment %d lasts %v, not %v", i, seg.Duration, tc.want)
				}
				if seg.Discontinuity {
					t.Errorf("segment %d follows a discontinuity", i)
				}
				if tc.key && !seg.Key {
					t.Errorf("segment %d does not start at a keyframe", i)
				}
				if seg.Duration > tc.max {
					t.Errorf("segment %d lasts %v, over %v", i, seg.Duration, tc.max)
				}
			}
			if !segs[0].Key {
				t.Error("the first segment does not start at a keyframe")
			}
			if !tc.key && segs[1].Key {
				t.Error("a segment cut at the longest starts at a keyframe")
			}
		})
	}
}

// TestKeyframesByTheirPictures cuts a stream that does not mark its
// keyframes in the adaptation field, as some sources do not.
func TestKeyframesByTheirPictures(t *testing.T) {
	s := newStream()
	s.video(0, 30, 50, false)
	b := s.b.Bytes()
	segs := cutAll(b, 4*time.Second, 10*time.Second)
	// Seven end at keyframes; the last 2 s wait for the next.
	if len(segs) != 7 {
		t.Fatalf("%d segments: %v", len(segs), durations(segs))
	}
	checkSegments(t, b, segs)
	for i, seg := range segs {
		if !seg.Key || seg.Duration != 4*time.Second {
			t.Errorf("segment %d: %+v", i, seg)
		}
	}
}

// TestCutsInAnyPieces gives the cutter the same stream in pieces of many
// sizes, as a buffer hands it on, and expects the same segments.
func TestCutsInAnyPieces(t *testing.T) {
	s := newStream()
	s.video(0, 30, 50, true)
	b := s.b.Bytes()
	want := cutAll(b, 4*time.Second, 10*time.Second)
	for _, sizes := range [][]int{{1}, {7, 300, 188}, {187}, {189}, {32 << 10}} {
		got := cutAll(b, 4*time.Second, 10*time.Second, sizes...)
		if len(got) != len(want) {
			t.Fatalf("in pieces of %v: %d segments, not %d", sizes, len(got), len(want))
		}
		for i := range got {
			if got[i].Off != want[i].Off || got[i].Len != want[i].Len || got[i].Duration != want[i].Duration {
				t.Errorf("in pieces of %v, segment %d: %+v, not %+v", sizes, i, got[i], want[i])
			}
		}
	}
}

// TestBreaksOff gives the cutter a stream with a gap in it, as when a
// buffer drops what a reader had not read: the segment open at the gap is
// dropped, and the next one follows a discontinuity.
func TestBreaksOff(t *testing.T) {
	s := newStream()
	pts := s.video(0, 10, 50, true)
	first := s.b.Len()
	s.video(pts+5*clockRate, 10, 50, true)
	b := s.b.Bytes()
	var got []Segment
	c := New(4*time.Second, 10*time.Second, func(s Segment) { got = append(got, s) })
	c.Write(0, b[:first-50*mpegts.PacketSize])
	// The rest comes at its own offset, after a gap.
	c.Write(int64(first), b[first:])
	if len(got) < 3 {
		t.Fatalf("%d segments: %v", len(got), durations(got))
	}
	for i, seg := range got {
		after := seg.Off >= int64(first)
		if seg.Off < int64(first) && seg.Off+seg.Len > int64(first-50*mpegts.PacketSize) {
			t.Errorf("segment %d spans the gap: %+v", i, seg)
		}
		if seg.Discontinuity != (after && (i == 0 || got[i-1].Off < int64(first))) {
			t.Errorf("segment %d: discontinuity %v", i, seg.Discontinuity)
		}
	}
}

// TestClockJumps cuts a stream whose timestamps jump ahead, as when a
// source restarts its encoder: the segment before the jump keeps its
// length, and the one after follows a discontinuity.
func TestClockJumps(t *testing.T) {
	s := newStream()
	pts := s.video(0, 8, 50, true)
	s.video(pts+3600*clockRate, 12, 50, true)
	b := s.b.Bytes()
	segs := cutAll(b, 4*time.Second, 10*time.Second)
	checkSegments(t, b, segs)
	disc := 0
	for i, seg := range segs {
		if seg.Duration > 10*time.Second {
			t.Errorf("segment %d lasts %v", i, seg.Duration)
		}
		if seg.Discontinuity {
			disc++
		}
	}
	if disc != 1 || len(segs) < 4 {
		t.Errorf("%d discontinuities in %v", disc, durations(segs))
	}
}

// TestSoundAlone cuts a stream without pictures by its frames of sound.
func TestSoundAlone(t *testing.T) {
	s := newStream()
	var pts int64
	for i := range 10 * 25 {
		if i%25 == 0 {
			s.tables(false)
		}
		s.pes(audioPID, pts, false, []byte{0xFF, 0xF1})
		pts += clockRate / 25
	}
	segs := cutAll(s.b.Bytes(), 4*time.Second, 10*time.Second)
	if len(segs) != 2 || segs[0].Duration != 4*time.Second || !segs[0].Key {
		t.Errorf("segments: %+v", segs)
	}
}

// TestNotTS gives the cutter bytes that are not MPEG-TS: it cuts nothing.
func TestNotTS(t *testing.T) {
	b := bytes.Repeat([]byte("not a transport stream "), 10000)
	if segs := cutAll(b, 4*time.Second, 10*time.Second, 1000); len(segs) != 0 {
		t.Errorf("segments of text: %+v", segs)
	}
}

func FuzzCutter(f *testing.F) {
	s := newStream()
	s.video(0, 6, 25, true)
	f.Add(s.b.Bytes()[:40*mpegts.PacketSize], uint16(100))
	f.Add([]byte{mpegts.SyncByte, 0x40, 0, 0x10, 0}, uint16(1))
	f.Fuzz(func(t *testing.T, b []byte, step uint16) {
		n := int(step%2000) + 1
		c := New(time.Second, 2*time.Second, func(seg Segment) {
			if seg.Len <= 0 || seg.Off < 0 || seg.Off+seg.Len > int64(len(b)) || seg.Duration < 0 {
				t.Fatalf("segment %+v of %d bytes", seg, len(b))
			}
		})
		for at := 0; at < len(b); at += n {
			c.Write(int64(at), b[at:min64(at+n, len(b))])
		}
	})
}
