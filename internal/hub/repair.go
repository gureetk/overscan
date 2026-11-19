// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"overscan.gureet.ca/internal/mpegts"
	"overscan.gureet.ca/internal/sniff"
)

// Stream repair (D57). The stream of a channel set to be repaired goes
// through ffmpeg, which copies its video and sound into a new MPEG-TS
// stream without re-encoding them. Where the source's clock jumps back, or
// ahead by more than ten seconds, the new stream's clock runs on; its
// program clock references and continuity counters are ffmpeg's own. The
// repeat filter (D43) comes first, so that what a source sends again is
// still left out rather than re-timed.
//
// ffmpeg reads the stream from a pipe and writes to another: it never sees
// the source's address, and it may open nothing but pipes. It first reads
// about two seconds of the stream to find out what is in it. If it stops
// before it sends anything, as it does when a sound track that the stream
// lists carries nothing yet, the player gets the stream as it is, from the
// start, and the log says why.

const (
	// maxRepairs caps the streams that go through ffmpeg at once.
	maxRepairs = 8

	// repairKeep caps what is kept of a stream until ffmpeg sends
	// something, to play the stream as it is if ffmpeg stops.
	repairKeep = 8 << 20

	// repairDrain is how long ffmpeg may take to pass on the end of a
	// stream once the source has ended it.
	repairDrain = 5 * time.Second

	// repairMessages is how many of ffmpeg's last messages are kept, to log
	// when it stops, and maxMessage caps the length of each.
	repairMessages = 5
	maxMessage     = 300
)

// repairArgs are ffmpeg's arguments: MPEG-TS from standard input, with
// pipes the only protocol allowed, its video and sound copied into MPEG-TS
// on standard output. Its first two seconds tell ffmpeg what the stream
// holds. A stream that pauses holds back the others for at most two
// seconds, where the default would be ten.
var repairArgs = []string{
	"-hide_banner", "-nostdin", "-nostats", "-loglevel", "warning",
	"-protocol_whitelist", "pipe",
	"-fflags", "+genpts",
	"-analyzeduration", "2000000", "-probesize", "4000000",
	"-f", "mpegts", "-i", "pipe:0",
	"-map", "0:v?", "-map", "0:a?", "-c", "copy",
	"-max_interleave_delta", "2000000",
	"-f", "mpegts", "pipe:1",
}

// Repair says which streams the hub repairs.
type Repair int

const (
	// RepairSet repairs the streams of the channels set to be repaired.
	RepairSet Repair = iota
	// RepairAll repairs every continuous stream, as a channel check with
	// repair on does.
	RepairAll
	// RepairNone repairs none.
	RepairNone
)

// UseFFmpeg gives the hub the ffmpeg program, path, that repairs streams,
// and says which streams it repairs. Without ffmpeg, the hub repairs none:
// the channels set to be repaired play as they are. It is called before
// the hub serves anything.
func (h *Hub) UseFFmpeg(path string, which Repair) {
	h.ffmpeg = path
	h.repair = which
}

// CanRepair reports whether the hub has ffmpeg to repair streams with.
func (h *Hub) CanRepair() bool { return h.ffmpeg != "" && h.repair != RepairNone }

// repairs reports whether the hub repairs the continuous streams of s.
func (h *Hub) repairs(s *session) bool { return h.repairsChannel(s.repair) }

// repairsChannel reports whether the hub repairs the continuous streams of
// a channel that is set to be repaired, or not.
func (h *Hub) repairsChannel(set bool) bool {
	return h.ffmpeg != "" && (h.repair == RepairAll || (h.repair == RepairSet && set))
}

// A sink is where the hub sends a continuous stream: to the player, or
// through ffmpeg to the player.
type sink interface {
	io.Writer
	// flush passes on to the player what has been written, as far as it
	// can.
	flush()
	// finish passes on the rest at the end of the stream.
	finish()
	// close stops what the sink runs, at the end of the request.
	close()
}

// sink returns where a stream for s goes, given that the response's header
// has been written: through ffmpeg if it is continuous and the hub repairs
// it, and to the player otherwise.
func (h *Hub) sink(ctx context.Context, w http.ResponseWriter, s *session, continuous bool) sink {
	rc := http.NewResponseController(w)
	if continuous && h.repairs(s) {
		return &repairSink{h: h, s: s, ctx: ctx, w: w, rc: rc}
	}
	return directSink{w: w, rc: rc}
}

// directSink sends a stream to the player as it is.
type directSink struct {
	w  io.Writer
	rc *http.ResponseController
}

func (d directSink) Write(p []byte) (int, error) { return d.w.Write(p) }
func (d directSink) flush()                      { _ = d.rc.Flush() }
func (d directSink) finish()                     { _ = d.rc.Flush() }
func (d directSink) close()                      {}

// States of a repairSink.
const (
	repairSniffing  = iota // waiting to see whether the stream is MPEG-TS
	repairRunning          // ffmpeg repairs it
	repairBypassing        // it goes to the player as it is
)

// errRepairStopped is why a repaired stream ends when ffmpeg stops after
// it has sent something: the player then asks for the stream again.
var errRepairStopped = errors.New("ffmpeg stopped")

// repairSink sends a stream through ffmpeg to the player. It starts ffmpeg
// once the stream's first bytes show MPEG-TS, and feeds it from the first
// whole packet; anything else goes to the player as it is. Its methods
// other than the output's copy run on the request's goroutine; the copy
// from ffmpeg to the player runs on its own, and alone writes to the
// player while ffmpeg runs.
type repairSink struct {
	h   *Hub
	s   *session
	ctx context.Context // the request's
	w   io.Writer       // the player
	rc  *http.ResponseController

	state   int
	sniffed []byte // the stream's first bytes, until they tell

	cmd      *exec.Cmd
	stop     context.CancelFunc // stops ffmpeg
	stdin    io.WriteCloser
	messages *messageTail
	slot     bool // holds one of the hub's repair slots

	// Until ffmpeg sends something, what it was given is kept, unless
	// there was too much of it.
	kept    []byte
	keeping bool

	sent    atomic.Bool // ffmpeg has sent something to the player
	output  chan error  // the copy to the player's end: nil, or why
	outErr  error
	outDone bool
}

// Write takes in the next piece of the stream.
func (r *repairSink) Write(p []byte) (int, error) {
	switch r.state {
	case repairBypassing:
		return r.w.Write(p)
	case repairRunning:
		return len(p), r.feed(p)
	}
	r.sniffed = append(r.sniffed, p...)
	at, ts, known := mpegts.Start(r.sniffed)
	if !known {
		return len(p), nil
	}
	data := r.sniffed
	r.sniffed = nil
	if !ts {
		r.notTS(data)
	} else if r.start() {
		r.state = repairRunning
		return len(p), r.feed(data[at:])
	}
	r.state = repairBypassing
	_, err := r.w.Write(data)
	return len(p), err
}

// notTS logs that the stream goes as it is, and what it starts with.
func (r *repairSink) notTS(data []byte) {
	r.h.log.Info("not repairing a stream that is not MPEG-TS; it plays as it is",
		append(r.about(), "data", sniff.Describe(data))...)
}

func (r *repairSink) flush() {
	if r.state == repairBypassing {
		_ = r.rc.Flush()
	}
}

// finish passes on the rest of the stream: ffmpeg is told that the stream
// has ended and given a little time to send what it holds.
func (r *repairSink) finish() {
	switch r.state {
	case repairSniffing:
		// The stream ended before its first bytes could tell.
		r.state = repairBypassing
		if len(r.sniffed) > 0 {
			r.notTS(r.sniffed)
		}
		_, _ = r.w.Write(r.sniffed)
		r.sniffed = nil
	case repairRunning:
		r.stdin.Close()
		stop := time.AfterFunc(repairDrain, r.stop)
		err := r.wait()
		stop.Stop()
		switch {
		case r.ctx.Err() != nil:
		case !r.sent.Load() && r.keeping:
			r.bypass(err)
		case err != nil:
			r.h.log.Warn("ffmpeg stopped repairing a stream", append(r.about(), "error", err, "ffmpeg", r.messages.String())...)
		}
	}
	_ = r.rc.Flush()
}

// close stops ffmpeg, if it still runs, and gives back its slot.
func (r *repairSink) close() {
	if r.cmd != nil {
		r.stop()
		r.stdin.Close()
		r.wait()
	}
	if r.slot {
		r.slot = false
		r.h.repairsRunning.Add(-1)
		r.s.repairing.Add(-1)
	}
}

// about returns the attributes that name the stream in the log.
func (r *repairSink) about() []any {
	return []any{"channel", r.s.channel, "source", r.h.source(r.s.sourceID).Name, "device", r.s.deviceID}
}

// start starts ffmpeg, if a slot is free. It reports false if the stream
// must go to the player as it is.
func (r *repairSink) start() bool {
	if r.h.repairsRunning.Add(1) > maxRepairs {
		r.h.repairsRunning.Add(-1)
		r.h.log.Warn("too many streams are being repaired; this one plays as it is", append(r.about(), "most", maxRepairs)...)
		return false
	}
	ctx, stop := context.WithCancel(r.ctx)
	cmd := exec.CommandContext(ctx, r.h.ffmpeg, repairArgs...)
	cmd.Env = []string{} // nothing from Overscan's environment
	cmd.Dir = "/"
	cmd.WaitDelay = repairDrain
	r.messages = &messageTail{log: r.h.log, about: r.about()}
	cmd.Stderr = r.messages
	keepChild(cmd)
	stdin, err := cmd.StdinPipe()
	var stdout io.ReadCloser
	if err == nil {
		stdout, err = cmd.StdoutPipe()
	}
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		stop()
		r.h.repairsRunning.Add(-1)
		r.h.log.Warn("cannot start ffmpeg to repair a stream; it plays as it is", append(r.about(), "error", err)...)
		return false
	}
	r.cmd, r.stop, r.stdin, r.slot, r.keeping = cmd, stop, stdin, true, true
	r.s.repairing.Add(1)
	r.output = make(chan error, 1)
	go r.copyOut(stdout)
	r.h.log.Info("repairing a stream with ffmpeg", r.about()...)
	return true
}

// copyOut sends what ffmpeg writes to the player until ffmpeg stops or the
// player goes away, which stops ffmpeg.
func (r *repairSink) copyOut(stdout io.Reader) {
	buf := make([]byte, copyBufferBytes)
	var err error
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 {
			r.sent.Store(true)
			if _, werr := r.w.Write(buf[:n]); werr != nil {
				err = werr
				r.stop()
				break
			}
			_ = r.rc.Flush()
		}
		if rerr != nil {
			break
		}
	}
	if werr := r.cmd.Wait(); err == nil {
		err = werr
	}
	r.output <- err
}

// wait waits for the copy to the player to end, and returns why it did.
func (r *repairSink) wait() error {
	if !r.outDone {
		r.outErr = <-r.output
		r.outDone = true
	}
	return r.outErr
}

// feed gives ffmpeg the next piece of the stream.
func (r *repairSink) feed(p []byte) error {
	if r.keeping {
		if r.sent.Load() || len(r.kept)+len(p) > repairKeep {
			r.kept, r.keeping = nil, false
		} else {
			r.kept = append(r.kept, p...)
		}
	}
	if _, err := r.stdin.Write(p); err != nil {
		return r.stopped()
	}
	return nil
}

// stopped deals with ffmpeg having stopped while the stream goes on. If it
// sent nothing, the player gets the stream as it is; otherwise the stream
// ends here, and the player asks for it again.
func (r *repairSink) stopped() error {
	err := r.wait()
	if r.ctx.Err() != nil {
		return r.ctx.Err()
	}
	if r.sent.Load() || !r.keeping {
		r.h.log.Warn("ffmpeg stopped repairing a stream", append(r.about(), "error", err, "ffmpeg", r.messages.String())...)
		return errRepairStopped
	}
	return r.bypass(err)
}

// bypass sends the player what ffmpeg was given, which ffmpeg failed to
// repair, and from then on the stream as it is.
func (r *repairSink) bypass(err error) error {
	r.h.log.Warn("ffmpeg could not repair a stream, so it plays as it is",
		append(r.about(), "error", err, "ffmpeg", r.messages.String())...)
	r.state = repairBypassing
	kept := r.kept
	r.kept, r.keeping = nil, false
	_, werr := r.w.Write(kept)
	_ = r.rc.Flush()
	return werr
}

// messageTail takes in what ffmpeg writes to standard error: it logs each
// line at the debug level and keeps the last few.
type messageTail struct {
	log   interface{ Debug(string, ...any) }
	about []any

	mu      sync.Mutex
	partial []byte
	last    []string
}

func (m *messageTail) Write(p []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.partial = append(m.partial, p...)
	for {
		i := bytes.IndexByte(m.partial, '\n')
		if i < 0 {
			break
		}
		m.add(string(m.partial[:i]))
		m.partial = m.partial[i+1:]
	}
	if len(m.partial) > maxMessage {
		m.add(string(m.partial))
		m.partial = m.partial[:0]
	}
	return len(p), nil
}

// add takes in one line. It needs m.mu.
func (m *messageTail) add(line string) {
	line = strings.TrimSpace(strings.ToValidUTF8(line, "?"))
	if line == "" {
		return
	}
	if len(line) > maxMessage {
		line = line[:maxMessage] + "…"
	}
	m.log.Debug("ffmpeg says", append(m.about[:len(m.about):len(m.about)], "message", line)...)
	if len(m.last) == repairMessages {
		m.last = append(m.last[:0], m.last[1:]...)
	}
	m.last = append(m.last, line)
}

// String returns the last lines, joined.
func (m *messageTail) String() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	last := m.last
	if len(m.partial) > 0 {
		last = append(last[:len(last):len(last)], strings.TrimSpace(string(m.partial)))
	}
	return strings.Join(last, " | ")
}
