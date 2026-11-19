// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/mpegts"
	"overscan.gureet.ca/internal/tsinfo"
	"overscan.gureet.ca/internal/tsinfo/tstest"
)

// filtered runs data through a repeat filter in pieces of random size, as a
// network would deliver it, and returns what comes out and the repeats
// reported.
func filtered(t *testing.T, data []byte) ([]byte, []time.Duration) {
	t.Helper()
	out, backs, _ := filteredSkipping(t, data)
	return out, backs
}

// filteredSkipping is filtered, and returns too how many bytes the filter
// reported leaving out before the first whole packet.
func filteredSkipping(t *testing.T, data []byte) ([]byte, []time.Duration, int) {
	t.Helper()
	var out bytes.Buffer
	var backs []time.Duration
	skipped := 0
	f := newRepeatFilter(&out, func(back time.Duration, bytes int64) {
		if bytes <= 0 {
			t.Errorf("a repeat of %d bytes", bytes)
		}
		backs = append(backs, back)
	})
	f.skipped = func(n int) { skipped += n }
	rng := rand.New(rand.NewPCG(3, 4))
	for len(data) > 0 {
		n := min(len(data), 1+rng.IntN(3000))
		if _, err := f.Write(data[:n]); err != nil {
			t.Fatal(err)
		}
		data = data[n:]
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes(), backs, skipped
}

// timestamps returns the timestamps of the PES packets on pid, in order,
// in frames.
func timestamps(data []byte, pid uint16) []int {
	var out []int
	for ; len(data) >= mpegts.PacketSize; data = data[mpegts.PacketSize:] {
		p := mpegts.Packet(data[:mpegts.PacketSize])
		if p.PID() != pid || !p.Start() {
			continue
		}
		if pts, _, ok := mpegts.PTS(p.Payload()); ok {
			out = append(out, int(pts/tstest.Frame))
		}
	}
	return out
}

func frames(first, last int) []int {
	var out []int
	for i := first; i <= last; i++ {
		out = append(out, i)
	}
	return out
}

func report(data []byte) tsinfo.Report {
	a := tsinfo.New(func() time.Duration { return 0 })
	a.Write(data)
	return a.Report()
}

// restart is a source's stream that restarts after old: a new muxer, whose
// continuity counters start again.
func restart(old *tstest.Muxer) *tstest.Muxer {
	m := tstest.Bare()
	for _, pid := range []uint16{0, tstest.PMTPID, tstest.VideoPID, tstest.AudioPID} {
		m.SetCounter(pid, old.Counter(pid)+7)
	}
	m.Tables()
	return m
}

func TestRepeatFilterPassesAStreamAsItIs(t *testing.T) {
	m := tstest.New()
	m.Program(0, 250, "")
	out, backs := filtered(t, m.Bytes())
	if !bytes.Equal(out, m.Bytes()) || len(backs) != 0 {
		t.Errorf("%d bytes in, %d out, repeats %v", len(m.Bytes()), len(out), backs)
	}
}

// restarting is a source's stream of ten seconds after which the source
// restarts, cutting its last packet short, sends the last five seconds
// again, and carries on for two more.
func restarting() []byte {
	a := tstest.New()
	a.Program(0, 250, "")
	b := restart(a)
	b.Program(125, 300, "")
	return append(bytes.Clone(a.Bytes()[:len(a.Bytes())-100]), b.Bytes()...)
}

func TestRepeatFilterLeavesOutARepeat(t *testing.T) {
	out, backs := filtered(t, restarting())
	if len(backs) != 1 || backs[0] != 5*time.Second {
		t.Errorf("repeats %v", backs)
	}
	// Every picture and every sound once, though the cut packet leaves the
	// last sound before the restart short.
	if got := timestamps(out, tstest.VideoPID); !reflect.DeepEqual(got, frames(0, 300)) {
		t.Errorf("pictures %v", got)
	}
	if got := timestamps(out, tstest.AudioPID); !reflect.DeepEqual(got, frames(0, 300)) {
		t.Errorf("sound %v", got)
	}
	r := report(out)
	if len(r.Clock.Jumps) != 0 || r.Clock.Repeated != 0 || r.Lost != 0 || r.SyncLosses != 0 {
		t.Errorf("clock %+v, lost %d, out of step %d", r.Clock, r.Lost, r.SyncLosses)
	}
}

func TestRepeatFilterPassesNewPicturesWithAnOldClock(t *testing.T) {
	// The clock goes back five seconds on new pictures, which pass on; then
	// the source repeats two seconds of those, which do not.
	a := tstest.New()
	a.Program(0, 250, "")
	b := restart(a)
	b.Program(125, 250, "new")
	c := restart(b)
	c.Program(200, 275, "new")
	in := append(append(bytes.Clone(a.Bytes()), b.Bytes()...), c.Bytes()...)

	out, backs := filtered(t, in)
	if len(backs) != 1 || backs[0] != 2*time.Second {
		t.Errorf("repeats %v", backs)
	}
	want := append(append(frames(0, 250), frames(125, 250)...), frames(251, 275)...)
	if got := timestamps(out, tstest.VideoPID); !reflect.DeepEqual(got, want) {
		t.Errorf("pictures %v", got)
	}
	if !bytes.Equal(out[:len(a.Bytes())+len(b.Bytes())], in[:len(a.Bytes())+len(b.Bytes())]) {
		t.Error("the new pictures did not pass on as they were")
	}
}

func TestRepeatFilterPassesWhatIsNotTS(t *testing.T) {
	for _, in := range [][]byte{
		bytes.Repeat([]byte("#EXTM3U\nnot a stream\n"), 500),
		append([]byte{0x47}, bytes.Repeat([]byte{1}, 1000)...), // one sync byte, then none in step
		{0x47, 1, 2},
		{},
	} {
		out, backs := filtered(t, in)
		if !bytes.Equal(out, in) || len(backs) != 0 {
			t.Errorf("%q came out as %q", in[:min(len(in), 20)], out[:min(len(out), 20)])
		}
	}
}

func TestRepeatFilterStartsAtTheFirstWholePacket(t *testing.T) {
	m := tstest.New()
	m.Program(0, 50, "")
	junk := []byte("ICY 200 OK\r\n\r\n")
	for _, c := range []struct {
		name     string
		in, want []byte
		skipped  int
	}{
		{"part way through a packet", m.Bytes()[100:], m.Bytes()[mpegts.PacketSize:], mpegts.PacketSize - 100},
		{"after bytes that are not packets", append(bytes.Clone(junk), m.Bytes()...), m.Bytes(), len(junk)},
	} {
		out, backs, skipped := filteredSkipping(t, c.in)
		if !bytes.Equal(out, c.want) || len(backs) != 0 || skipped != c.skipped {
			t.Errorf("%s: %d bytes in, %d out, want %d; %d left out at the start, want %d; repeats %v",
				c.name, len(c.in), len(out), len(c.want), skipped, c.skipped, backs)
		}
	}
}

func TestRepeatFilterLeavesOutARepeatInAStreamThatStartsPartWay(t *testing.T) {
	// As restarting, but with the tables twice at the start, as a source
	// sends them over and over, and starting part way through the first.
	a := tstest.New()
	a.Tables()
	a.Program(0, 250, "")
	b := restart(a)
	b.Program(125, 300, "")
	in := append(bytes.Clone(a.Bytes()[100:len(a.Bytes())-100]), b.Bytes()...)
	out, backs, skipped := filteredSkipping(t, in)
	if len(backs) != 1 || backs[0] != 5*time.Second || skipped != mpegts.PacketSize-100 {
		t.Errorf("repeats %v, %d left out at the start", backs, skipped)
	}
	if r := report(out); len(r.Clock.Jumps) != 0 || r.Clock.Repeated != 0 || r.SyncLosses != 0 {
		t.Errorf("clock %+v, out of step %d", r.Clock, r.SyncLosses)
	}
}

func TestRepeatFilterLeavesOutJunkBetweenPackets(t *testing.T) {
	m := tstest.New()
	m.Program(0, 50, "")
	cut := 40 * mpegts.PacketSize
	in := append(append(bytes.Clone(m.Bytes()[:cut]), "junk\x47between"...), m.Bytes()[cut:]...)
	out, _ := filtered(t, in)
	if r := report(out); r.SyncLosses != 0 || len(out)%mpegts.PacketSize != 0 {
		t.Errorf("out of step %d times, %d bytes", r.SyncLosses, len(out))
	}
}

func TestRepeatFilterForgetsOldPictures(t *testing.T) {
	// A repeat of pictures older than the filter remembers passes on.
	m := tstest.New()
	m.Pictures(0, repeatHistory+100, "")
	m.Pictures(10, 60, "")
	out, backs := filtered(t, m.Bytes())
	if len(backs) != 0 || !bytes.Equal(out, m.Bytes()) {
		t.Errorf("repeats %v", backs)
	}
}

func TestRepeatsAreLeftOut(t *testing.T) {
	h := newHarness(t, 1)
	resp := get(t, h.link(7, "restarting"))
	out := []byte(body(t, resp))
	h.waitIdle()
	if r := report(out); r.Clock == nil || len(r.Clock.Jumps) != 0 || r.Lost != 0 {
		t.Errorf("report %+v", r)
	}
	if logs := h.logs.String(); !strings.Contains(logs, `msg="the source sent a stretch again; the hub left it out" channel=restarting source=main back=5s bytes=`) {
		t.Errorf("logs: %s", logs)
	}
}

func FuzzRepeatFilter(f *testing.F) {
	// Streams of small pictures, so that the fuzzer runs many: two
	// seconds that restart more than a second back, and one as it is.
	small := func(m *tstest.Muxer, first, last int) {
		for i := first; i <= last; i++ {
			m.Picture(int64(i)*tstest.Frame, []byte(fmt.Sprintf("picture %d", i)))
			m.AudioFrame(int64(i)*tstest.Frame, []byte(fmt.Sprintf("sound %d", i)))
		}
	}
	a := tstest.New()
	small(a, 0, 50)
	b := restart(a)
	small(b, 15, 60)
	f.Add(append(bytes.Clone(a.Bytes()[:len(a.Bytes())-100]), b.Bytes()...), uint16(1500))
	m := tstest.New()
	small(m, 0, 25)
	f.Add(m.Bytes(), uint16(188))
	f.Add([]byte("#EXTM3U\nnot a stream"), uint16(5))
	f.Fuzz(func(t *testing.T, data []byte, piece uint16) {
		var out bytes.Buffer
		filter := newRepeatFilter(&out, nil)
		size := int(piece%4000) + 1
		for rest := data; len(rest) > 0; {
			n := min(len(rest), size)
			if _, err := filter.Write(rest[:n]); err != nil {
				t.Fatal(err)
			}
			rest = rest[n:]
		}
		if err := filter.Close(); err != nil {
			t.Fatal(err)
		}
		// The filter leaves things out; it never adds any.
		if out.Len() > len(data) {
			t.Errorf("%d bytes out of %d", out.Len(), len(data))
		}
	})
}
