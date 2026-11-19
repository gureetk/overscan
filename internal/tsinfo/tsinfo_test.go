// SPDX-License-Identifier: AGPL-3.0-or-later

package tsinfo

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"overscan.gureet.ca/internal/tsinfo/tstest"
)

const (
	videoPID = tstest.VideoPID
	audioPID = tstest.AudioPID
	frame    = tstest.Frame
)

// analyse writes data to a new Analyzer in pieces of random size, as a
// network would deliver it, with the clock moving on a millisecond a piece.
func analyse(t *testing.T, data []byte) Report {
	t.Helper()
	var now time.Duration
	a := New(func() time.Duration { return now })
	rng := rand.New(rand.NewPCG(1, 2))
	for len(data) > 0 {
		n := min(len(data), 1+rng.IntN(1500))
		if _, err := a.Write(data[:n]); err != nil {
			t.Fatal(err)
		}
		data = data[n:]
		now += time.Millisecond
	}
	return a.Report()
}

func near(got, want time.Duration) bool {
	return got > want-50*time.Millisecond && got < want+50*time.Millisecond
}

func TestSmoothStream(t *testing.T) {
	m := tstest.New()
	m.Pictures(0, 250, "")
	r := analyse(t, m.Bytes())
	if !r.TS || r.Programs != 1 || r.SyncLosses != 0 || r.Lost != 0 || r.Scrambled != 0 || r.Discontinuities != 0 {
		t.Errorf("report %+v", r)
	}
	want := []Stream{{PID: videoPID, Kind: "video", Codec: "H.264"}, {PID: audioPID, Kind: "audio", Codec: "AAC", Language: "eng"}}
	if fmt.Sprint(r.Streams) != fmt.Sprint(want) {
		t.Errorf("streams %+v", r.Streams)
	}
	c := r.Clock
	if c == nil || c.PID != videoPID || c.Kind != "video" || !near(c.Covered, 10*time.Second) || c.Repeated != 0 || len(c.Jumps) != 0 {
		t.Errorf("clock %+v", c)
	}
}

func TestRepeat(t *testing.T) {
	// Ten seconds, the last five again, then two more seconds.
	m := tstest.New()
	m.Pictures(0, 250, "")
	m.Pictures(125, 300, "")
	c := analyse(t, m.Bytes()).Clock
	if len(c.Jumps) != 1 || !c.Jumps[0].Repeat || !near(c.Jumps[0].By, -5*time.Second) {
		t.Fatalf("jumps %+v", c.Jumps)
	}
	if !near(c.Repeated, 5*time.Second) || !near(c.Covered, 12*time.Second) {
		t.Errorf("covered %s, repeated %s", c.Covered, c.Repeated)
	}
	if c.Jumps[0].At == 0 {
		t.Error("the jump has no place in the reading")
	}
}

func TestOldClockOnNewPictures(t *testing.T) {
	m := tstest.New()
	m.Pictures(0, 250, "")
	m.Pictures(125, 250, "new")
	c := analyse(t, m.Bytes()).Clock
	if len(c.Jumps) != 1 || c.Jumps[0].Repeat || !near(c.Jumps[0].By, -5*time.Second) {
		t.Fatalf("jumps %+v", c.Jumps)
	}
	if c.Repeated != 0 || !near(c.Covered, 15*time.Second) {
		t.Errorf("covered %s, repeated %s", c.Covered, c.Repeated)
	}
}

func TestGap(t *testing.T) {
	m := tstest.New()
	m.Pictures(0, 50, "")
	m.Pictures(300, 350, "")
	c := analyse(t, m.Bytes()).Clock
	if len(c.Jumps) != 1 || c.Jumps[0].Repeat || !near(c.Jumps[0].By, 10*time.Second) || !near(c.Covered, 4*time.Second) {
		t.Errorf("clock %+v", c)
	}
}

func TestClockWraps(t *testing.T) {
	m := tstest.New()
	start := int64((1<<33 - 2*clockRate) / frame)
	for i := int64(0); i <= 100; i++ {
		m.Picture((start+i)*frame%(1<<33), tstest.Content(int(i), ""))
	}
	c := analyse(t, m.Bytes()).Clock
	if len(c.Jumps) != 0 || !near(c.Covered, 4*time.Second) {
		t.Errorf("clock %+v", c)
	}
}

func TestLostScrambledAndDiscontinuities(t *testing.T) {
	m := tstest.New()
	m.Pictures(0, 10, "")
	data := m.Bytes()

	// Drop two video packets, and mark another as scrambled.
	var out []byte
	video := 0
	for p := data; len(p) > 0; p = p[packetSize:] {
		pkt := bytes.Clone(p[:packetSize])
		if uint16(pkt[1]&0x1F)<<8|uint16(pkt[2]) == videoPID {
			video++
			if video == 20 || video == 21 {
				continue
			}
			if video == 40 {
				pkt[3] |= 0x80
			}
		}
		out = append(out, pkt...)
	}
	// A packet that announces a discontinuity, with its counter out of step.
	m2 := tstest.Bare()
	m2.SetCounter(videoPID, 9)
	m2.Packet(videoPID, false, []byte{1, 2, 3}, 0x80)
	out = append(out, m2.Bytes()...)

	r := analyse(t, out)
	if r.Lost != 2 || r.Scrambled != 1 || r.Discontinuities != 1 {
		t.Errorf("lost %d, scrambled %d, discontinuities %d", r.Lost, r.Scrambled, r.Discontinuities)
	}
}

func TestSyncLoss(t *testing.T) {
	m := tstest.New()
	m.Pictures(0, 25, "")
	data := m.Bytes()
	cut := 100 * packetSize
	junk := append(append(bytes.Clone(data[:cut]), []byte("some junk\x47 in between")...), data[cut:]...)
	r := analyse(t, junk)
	if r.SyncLosses != 1 || r.Lost != 0 || !near(r.Clock.Covered, time.Second) {
		t.Errorf("sync losses %d, lost %d, clock %+v", r.SyncLosses, r.Lost, r.Clock)
	}
}

func TestNotTS(t *testing.T) {
	text := bytes.Repeat([]byte("#EXTM3U\n#EXTINF:-1,Example\nhttp://192.0.2.1/x\n"), 2000)
	r := analyse(t, text)
	if r.TS || string(r.Start) != "#EXTM3U\n#EXTINF:" || r.Clock != nil || r.Bytes != int64(len(text)) {
		t.Errorf("report %+v", r)
	}
}

func TestLongestGap(t *testing.T) {
	var now time.Duration
	a := New(func() time.Duration { return now })
	m := tstest.New()
	m.Pictures(0, 5, "")
	data := m.Bytes()
	for i, gap := range []time.Duration{0, time.Second, 3 * time.Second, 2 * time.Second} {
		now += gap
		a.Write(data[i*packetSize : (i+1)*packetSize])
	}
	if r := a.Report(); r.LongestGap != 3*time.Second || r.Elapsed != 6*time.Second {
		t.Errorf("longest gap %s, elapsed %s", r.LongestGap, r.Elapsed)
	}
}

func FuzzAnalyzer(f *testing.F) {
	m := tstest.New()
	m.Program(0, 30, "")
	f.Add(m.Bytes(), uint16(1000))
	f.Add(append([]byte{0x47, 0x40}, m.Bytes()[2:400]...), uint16(7))
	f.Add([]byte("not a stream at all"), uint16(3))
	f.Fuzz(func(t *testing.T, data []byte, piece uint16) {
		a := New(func() time.Duration { return 0 })
		size := int(piece%4000) + 1
		for rest := data; len(rest) > 0; {
			n := min(len(rest), size)
			if _, err := a.Write(rest[:n]); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		r := a.Report()
		if r.Bytes != int64(len(data)) || r.Lost < 0 || r.SyncLosses < 0 {
			t.Errorf("report %+v for %d bytes", r, len(data))
		}
	})
}
