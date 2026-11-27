// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/hlscut"
	"overscan.gureet.ca/internal/store"
)

// HLS that the hub cuts (D153). A player that asks for a channel as HLS gets
// a playlist of one rendition, which points to a session of its own: a
// screen on the channel's shared tuner (D93), whose buffer on disk holds
// the channel's one stream, after the repeat filter and repair. The tuner's
// feed cuts that stream into segments at its keyframes, without ffmpeg,
// and lists them; the player's session serves the media playlist and the
// segments, each a stretch of the buffer with the stream's tables before
// it. Every HLS player of the channel shares the tuner, whether or not the
// shared tuner is on for screens. A session is active while its player
// asks for its playlist or its segments, and ends idleTimeout after it
// stops; the tuner ends with its last screen, as before.
//
// Written from RFC 8216, which says what a live playlist lists and how long
// a segment that leaves it stays.

const (
	// hlsMin is how long a segment lasts before it ends at a keyframe, and
	// hlsMaxTarget the longest target duration: a segment ends at the first
	// picture after it, keyframe or not, until the feed sets its own from
	// its first segments.
	hlsMin       = 4 * time.Second
	hlsMaxTarget = 10 * time.Second
	// hlsOver is how far past the target duration a segment that finds no
	// keyframe may go: RFC 8216 rounds its length to the nearest second.
	hlsOver = 350 * time.Millisecond
	// hlsWindow is the fewest segments that a media playlist lists, which
	// last at least three target durations once it has that many.
	hlsWindow = 6
	// hlsKept is how many segments a feed remembers: those that its
	// playlist lists, and those that left it, which players may still ask
	// for.
	hlsKept = 4 * hlsWindow
	// hlsFirst is how many segments a player's first playlist waits for.
	hlsFirst = 2
	// hlsKeep is how much of the stream a tuner's buffer keeps for its
	// feed: every segment that it remembers.
	hlsKeep = 2 * time.Minute
	// hlsBehind is how far back in its tuner's buffer a feed starts, so
	// that a player that joins a channel that plays already gets a full
	// playlist at once.
	hlsBehind = 30 * time.Second
)

// hlsFirstWait is how long a player's first playlist waits for the
// channel's first segments. A variable so that tests can shorten it.
var hlsFirstWait = 30 * time.Second

// playerEntry names a player's HLS of a lineup entry.
type playerEntry struct {
	player int64
	entry  int64
}

// hlsFeed cuts a shared tuner's buffer into segments and lists them.
type hlsFeed struct {
	b      *shift
	cutter *hlscut.Cutter // used by run's goroutine alone

	mu   sync.Mutex
	cond *sync.Cond
	segs []hlsSegment // oldest first, at most hlsKept
	next int64        // the sequence number of the next segment
	// discs counts the discontinuities so far; target is the playlist's
	// target duration in seconds, 0 until the first segments set it.
	discs  int64
	target int
	// status is what the source answered the tuner, while it gives no
	// stream and no segment has come; ended says that the feed stopped.
	status int
	ended  bool
}

// hlsSegment is a segment that a feed lists, with its sequence number and
// how many discontinuities came up to and with it.
type hlsSegment struct {
	hlscut.Segment
	seq   int64
	discs int64
}

// feedOf returns t's feed, starting one for the shared tuner t if it has
// none. It needs h.mu.
func (h *Hub) feedOf(t *session) *hlsFeed {
	if t.feed != nil {
		return t.feed
	}
	f := &hlsFeed{b: t.shift}
	f.cond = sync.NewCond(&f.mu)
	f.cutter = hlscut.New(hlsMin, hlsMaxTarget, f.add)
	t.feed = f
	t.shift.keepAtLeast(hlsKeep)
	h.log.Info("a channel is cut into HLS segments for players, from its shared tuner", "channel", t.channel,
		"source", h.source(t.sourceID).Name)
	h.tuners.Go(func() { h.runFeed(t, f) })
	return f
}

// runFeed feeds the tuner's buffer to the cutter while the tuner lasts,
// from hlsBehind before its end, or its start. When the buffer stalls, as
// when the source broke off and the tuner asks again, it goes on from where
// it was.
func (h *Hub) runFeed(t *session, f *hlsFeed) {
	defer f.end()
	off := int64(-1)
	for t.ctx.Err() == nil {
		if off < 0 {
			status, _, at := f.b.start(t.ctx, hlsBehind)
			if status != http.StatusOK {
				if t.ctx.Err() != nil || status == http.StatusServiceUnavailable {
					return
				}
				f.refused(status)
				if sleepContext(t.ctx, shiftRetry) != nil {
					return
				}
				continue
			}
			off = at
		}
		f.b.follow(t.ctx, off, func(at int64, p []byte) error {
			f.cutter.Write(at, p)
			off = at + int64(len(p))
			return nil
		})
		if sleepContext(t.ctx, shiftRetry) != nil {
			return
		}
	}
}

// add lists a segment that the cutter cut. With the first segments, it
// sets the target duration: their longest, rounded, between hlsMin and
// hlsMaxTarget; later segments that find no keyframe end a little after
// it.
func (f *hlsFeed) add(s hlscut.Segment) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s.Discontinuity {
		f.discs++
	}
	f.segs = append(f.segs, hlsSegment{Segment: s, seq: f.next, discs: f.discs})
	f.next++
	if n := len(f.segs) - hlsKept; n > 0 {
		f.segs = slices.Delete(f.segs, 0, n)
	}
	f.status = 0
	if f.target == 0 && len(f.segs) >= hlsFirst {
		longest := 0.0
		for _, x := range f.segs {
			longest = max(longest, x.Duration.Seconds())
		}
		f.target = min(max(int(math.Round(longest)), int(hlsMin/time.Second)), int(hlsMaxTarget/time.Second))
		f.cutter.SetLimits(hlsMin, time.Duration(f.target)*time.Second+hlsOver)
	}
	f.cond.Broadcast()
}

// refused notes what the source answered the tuner while it gives no
// stream.
func (f *hlsFeed) refused(status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.segs) == 0 {
		f.status = status
	}
	f.cond.Broadcast()
}

// end says that the feed has stopped.
func (f *hlsFeed) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ended = true
	f.cond.Broadcast()
}

// ready waits, up to hlsFirstWait, until the feed lists its first segments,
// and reports whether it does. If not, it returns the status to answer
// with.
func (f *hlsFeed) ready(ctx context.Context) (bool, int) {
	ctx, cancel := context.WithTimeout(ctx, hlsFirstWait)
	defer cancel()
	stop := context.AfterFunc(ctx, func() {
		f.mu.Lock()
		f.cond.Broadcast()
		f.mu.Unlock()
	})
	defer stop()
	f.mu.Lock()
	defer f.mu.Unlock()
	for f.target == 0 && !f.ended && ctx.Err() == nil {
		f.cond.Wait()
	}
	switch {
	case f.target != 0:
		return true, 0
	case f.ended:
		return false, http.StatusServiceUnavailable
	case f.status != 0:
		return false, f.status
	}
	return false, http.StatusGatewayTimeout
}

// window returns the segments that the media playlist lists: the newest,
// at least hlsWindow of them, lasting at least three target durations. It
// needs f.mu. Since a new segment only adds to the end, the first never
// moves back.
func (f *hlsFeed) window() []hlsSegment {
	var total time.Duration
	least := 3 * time.Duration(f.target) * time.Second
	i := len(f.segs)
	for i > 0 && (len(f.segs)-i < hlsWindow || total < least) {
		i--
		total += f.segs[i].Duration
	}
	return f.segs[i:]
}

// mediaPlaylist returns the feed's live playlist, whose segments are
// named by their sequence numbers, beside it.
func (f *hlsFeed) mediaPlaylist() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	segs := f.window()
	var b strings.Builder
	b.WriteString("#EXTM3U\n#EXT-X-VERSION:3\n")
	fmt.Fprintf(&b, "#EXT-X-TARGETDURATION:%d\n", f.target)
	if len(segs) > 0 {
		fmt.Fprintf(&b, "#EXT-X-MEDIA-SEQUENCE:%d\n", segs[0].seq)
		// The discontinuities before the first segment that the playlist
		// lists, whose tags it no longer shows.
		before := segs[0].discs
		if segs[0].Discontinuity {
			before--
		}
		if before > 0 {
			fmt.Fprintf(&b, "#EXT-X-DISCONTINUITY-SEQUENCE:%d\n", before)
		}
	}
	for _, s := range segs {
		if s.Discontinuity {
			b.WriteString("#EXT-X-DISCONTINUITY\n")
		}
		fmt.Fprintf(&b, "#EXTINF:%.3f,\n%d.ts\n", s.Duration.Seconds(), s.seq)
	}
	return b.String()
}

// bandwidth returns the highest bit rate of the segments that the feed
// remembers, as a playlist of renditions gives it: bits a second, with
// their tables.
func (f *hlsFeed) bandwidth() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var most float64
	for _, s := range f.segs {
		if d := s.Duration.Seconds(); d > 0 {
			most = max(most, float64(s.Len+int64(len(s.Tables)))*8/d)
		}
	}
	return int64(math.Ceil(most))
}

// segment returns the segment with a sequence number, if the feed still
// remembers it.
func (f *hlsFeed) segment(seq int64) (hlsSegment, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.segs) == 0 || seq < f.segs[0].seq || seq >= f.next {
		return hlsSegment{}, false
	}
	return f.segs[seq-f.segs[0].seq], true
}

// CutLink returns the path of a link that plays a lineup entry for a player
// as HLS that the hub cuts (D153): the same one while the player's session
// of it lasts, so that a player that asks for the channel's playlist again
// goes on with it. It returns "" for a channel that the hub cannot cut for
// the player: a radio station, one whose source sends HLS, which the shared
// tuner does not carry, or one that the hub transcodes for the player.
func (h *Hub) CutLink(playerID int64, entry store.PlayableEntry) string {
	if !h.sharable(entry) || h.transcodesLive(playerID, entry) {
		return ""
	}
	key := playerEntry{playerID, entry.EntryID}
	now := h.now()
	h.mu.Lock()
	defer h.mu.Unlock()
	if token, ok := h.cutLinks[key]; ok {
		if c, ok := h.verify(token); ok {
			if s := h.sessions[c.nonce]; (s != nil && !s.ended) || now.Unix() <= c.expires {
				return "/live/" + token + ".m3u8"
			}
		}
	}
	for k, token := range h.cutLinks {
		c, ok := h.verify(token)
		if s := h.sessions[c.nonce]; !ok || ((s == nil || s.ended) && now.Unix() > c.expires) {
			delete(h.cutLinks, k)
		}
	}
	token := h.sign(claims{kind: kindEntry, entryID: entry.EntryID, deviceID: playerID, expires: now.Add(linkTTL).Unix(),
		nonce: mathrand.Uint64(), cut: true})
	h.cutLinks[key] = token
	return "/live/" + token + ".m3u8"
}

// serveCut serves a player's HLS of a channel: at /live/<token>.m3u8, the
// playlist of one rendition, which starts the player's session or goes on
// with it; at /live/<token>/hls.m3u8, the session's media playlist; and at
// /live/<token>/<n>.ts, its segments.
func (h *Hub) serveCut(w http.ResponseWriter, r *http.Request, c claims, token, resource string, isResource bool) {
	s := h.lookup(c.nonce)
	if !isResource {
		if s == nil {
			if h.now().Unix() > c.expires {
				http.Error(w, "this link has expired; ask for a new one", http.StatusForbidden)
				return
			}
			entry, err := h.store.PlayableEntry(r.Context(), c.entryID)
			if errors.Is(err, store.ErrNotFound) {
				http.Error(w, "this channel is not available", http.StatusNotFound)
				return
			}
			if err != nil {
				h.log.Error("cannot look up a channel to play", "entry", c.entryID, "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			if s = h.openCut(w, r, c, token, entry); s == nil {
				return
			}
		}
		f := h.cutFeed(s)
		h.begin(s)
		defer h.finish(s)
		if !h.cutReady(w, r, s, f) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprintf(w, "#EXTM3U\n#EXT-X-VERSION:3\n#EXT-X-STREAM-INF:BANDWIDTH=%d\n/live/%s/hls.m3u8\n", f.bandwidth(), token)
		return
	}
	if s == nil || !s.cut {
		http.Error(w, "this stream has ended", http.StatusNotFound)
		return
	}
	f := h.cutFeed(s)
	h.begin(s)
	defer h.finish(s)
	if resource == "hls.m3u8" {
		if !h.cutReady(w, r, s, f) {
			return
		}
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		_, _ = w.Write([]byte(f.mediaPlaylist()))
		return
	}
	digits, ok := strings.CutSuffix(resource, ".ts")
	seq, err := strconv.ParseInt(digits, 10, 64)
	if !ok || err != nil || seq < 0 || strconv.FormatInt(seq, 10) != digits {
		http.NotFound(w, r)
		return
	}
	seg, ok := f.segment(seq)
	if !ok || !f.b.keeps(seg.Off, seg.Len) {
		http.Error(w, "no such segment", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Content-Length", strconv.FormatInt(int64(len(seg.Tables))+seg.Len, 10))
	if r.Method == http.MethodHead {
		return
	}
	if _, err := w.Write(seg.Tables); err != nil {
		return
	}
	if err := f.b.readRange(w, seg.Off, seg.Len); err != nil && !errors.Is(err, context.Canceled) {
		h.log.Debug("a segment of a channel's HLS was cut short", "channel", s.channel, "device", s.deviceID,
			"segment", seq, "error", err)
	}
	s.played.Store(true)
}

// cutFeed returns the feed of a player's HLS session's tuner.
func (h *Hub) cutFeed(s *session) *hlsFeed {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.feedOf(s.tuner)
}

// cutReady waits until the feed lists its first segments, and reports
// whether it does; if not, it answers why.
func (h *Hub) cutReady(w http.ResponseWriter, r *http.Request, s *session, f *hlsFeed) bool {
	ok, status := f.ready(r.Context())
	if ok || r.Context().Err() != nil {
		return ok
	}
	h.log.Warn("a channel's HLS has no segments for a player", "channel", s.channel, "source",
		h.source(s.sourceID).Name, "device", s.deviceID, "status", status)
	http.Error(w, shiftRefusal(status), status)
	return false
}

// openCut opens a player's HLS session of a channel, as a screen on the
// shared tuner of its stream, or of the first of its backups (D109) that
// can take it, and returns it; or answers why it cannot, and returns nil.
func (h *Hub) openCut(w http.ResponseWriter, r *http.Request, c claims, token string, entry store.PlayableEntry) *session {
	u, err := h.sourcesOf(r.Context(), c.deviceID)
	if err != nil {
		h.log.Error("cannot look up the sources that a device's account uses", "device", c.deviceID, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil
	}
	tries := slices.DeleteFunc(h.candidates(entry), func(t store.PlayableEntry) bool {
		return !u.Uses(t.SourceID) || !h.sharable(t)
	})
	if len(tries) == 0 {
		http.Error(w, "this channel is not available as HLS", http.StatusNotFound)
		return nil
	}
	for i, try := range tries {
		s, err := h.openShared(r.Context(), c, token, try)
		if err == nil {
			h.log.Info("a player plays a channel as HLS that the hub cuts from its shared tuner", "channel", entry.Name,
				"source", h.source(try.SourceID).Name, "device", c.deviceID)
			return s
		}
		if i < len(tries)-1 && !errors.Is(err, ErrAccountLimit) {
			h.log.Warn("a channel's stream cannot start; the hub tries its next stream", "channel", entry.Name,
				"source", h.source(try.SourceID).Name, "device", c.deviceID, "reason", err.Error())
			continue
		}
		if errors.Is(err, errNotShared) {
			http.Error(w, "this channel cannot be played as HLS now", http.StatusServiceUnavailable)
			return nil
		}
		h.resolveFailed(w, r, "channel", entry.Name, try.SourceID, c.deviceID, err)
		return nil
	}
	return nil
}

// StopCut ends the HLS that the hub cuts for an account's players (D153),
// as when the account's HLS for players is turned off, and returns how many
// sessions it ended. Their tuners end with their last screens.
func (h *Hub) StopCut(accountID int64, reason string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var stop []*session
	for _, s := range h.sessions {
		if s.cut && !s.ended && s.account == accountID {
			stop = append(stop, s)
		}
	}
	for _, s := range stop {
		h.end(s, reason)
	}
	return len(stop)
}
