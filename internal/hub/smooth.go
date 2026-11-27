// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"math"
	"slices"
	"sync/atomic"
	"time"
)

// Smoothness (D147). A live transcode that cannot keep up with its stream
// stutters on the screen for as long as it plays, so the hub watches how
// fast ffmpeg goes, as it does a film's (D73), and says when it falls
// behind.
//
// ffmpeg says how much video it has made (encodeProgress). The hub counts
// the time it waited for ffmpeg's output once the first came, which leaves
// out the time a slow player took to take it, and the time its own writes
// of the stream to ffmpeg waited for ffmpeg to take them. A transcode is
// behind when, over the last half minute, it made less than 95% of a
// second of video in each second that the hub waited for it, and the
// stream waited a fifth of that time or more to go into ffmpeg: ffmpeg
// held up the stream, not the source. A source that sends slowly does not
// make the stream wait to go in, and a player that takes it slowly leaves
// the hub no time to wait for ffmpeg, so neither counts.

const (
	// watchEvery is how often the hub notes how far a live transcode has
	// got, and watchWindow how far back it looks to judge it.
	watchEvery  = 5 * time.Second
	watchWindow = 30 * time.Second
	// watchSettle is how long ffmpeg works before the hub judges it: it
	// first reads two seconds of the stream, and starts faster than it
	// goes on.
	watchSettle = 15 * time.Second
	// behindSpeed is the speed below which a transcode is behind, and
	// caughtUpSpeed the one at or above which it has caught up again.
	behindSpeed   = 0.95
	caughtUpSpeed = 0.98
	// heldShare is the share of the window that the stream must have
	// waited to go into ffmpeg for a slow transcode to be behind.
	heldShare = 0.2
)

// liveWatch follows a live transcode's speed.
type liveWatch struct {
	progress *encodeProgress
	// held is how long the hub's writes of the stream to ffmpeg waited
	// for ffmpeg to take them, in nanoseconds; it is written by the
	// request's goroutine, which feeds ffmpeg, and read by the one that
	// copies ffmpeg's output.
	held atomic.Int64

	// The copy's own, by the goroutine that copies ffmpeg's output.
	started time.Time
	notes   []watchNote
	behind  bool
	speed   float64 // over the last window, 0 until known
}

// watchNote is how far a transcode had got at a time.
type watchNote struct {
	at     time.Time
	made   int64         // microseconds of video
	waited time.Duration // the hub's wait for ffmpeg's output
	held   time.Duration // the stream's wait to go into ffmpeg
}

func newLiveWatch(p *encodeProgress, now time.Time) *liveWatch {
	return &liveWatch{progress: p, started: now}
}

// feeding counts a write of the stream to ffmpeg that took d.
func (w *liveWatch) feeding(d time.Duration) {
	if d > time.Millisecond {
		w.held.Add(int64(d))
	}
}

// note notes how far the transcode has got, at most every watchEvery, and
// judges it. It reports whether it went behind (+1) or caught up again
// (-1), 0 if neither.
func (w *liveWatch) note(now time.Time) int {
	if n := len(w.notes); n > 0 && now.Sub(w.notes[n-1].at) < watchEvery {
		return 0
	}
	w.notes = append(w.notes, watchNote{at: now, made: w.progress.made.Load(), waited: w.progress.waited,
		held: time.Duration(w.held.Load())})
	// Keep the notes of the window and one before it.
	if i := slices.IndexFunc(w.notes, func(n watchNote) bool { return now.Sub(n.at) <= watchWindow }); i > 1 {
		w.notes = slices.Delete(w.notes, 0, i-1)
	}
	if now.Sub(w.started) < watchSettle+watchWindow || len(w.notes) < 2 {
		return 0
	}
	first, last := w.notes[0], w.notes[len(w.notes)-1]
	span, waited := last.at.Sub(first.at), last.waited-first.waited
	// A player that took the stream slowly left the hub too little time
	// to wait for ffmpeg to tell.
	if span < watchWindow/2 || waited < span/2 {
		return 0
	}
	w.speed = float64(last.made-first.made) / float64(waited.Microseconds())
	held := float64(last.held-first.held) / float64(span)
	switch {
	case !w.behind && w.speed < behindSpeed && held >= heldShare:
		w.behind = true
		return 1
	case w.behind && w.speed >= caughtUpSpeed:
		w.behind = false
		return -1
	}
	return 0
}

// speedBits returns the speed over the last window as float64 bits, for a
// session's speed.
func (w *liveWatch) speedBits() uint64 { return math.Float64bits(w.speed) }
