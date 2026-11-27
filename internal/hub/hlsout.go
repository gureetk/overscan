// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	// playlist at once. A transcode for players starts only hlsTranscodeBehind
	// back, since it transcodes what it starts with faster than it plays.
	hlsBehind          = 30 * time.Second
	hlsTranscodeBehind = 2 * hlsMin
)

// hlsFirstWait is how long a player's first playlist waits for the
// channel's first segments. A variable so that tests can shorten it.
var hlsFirstWait = 30 * time.Second

// playerEntry names a player's HLS of a lineup entry.
type playerEntry struct {
	player int64
	entry  int64
}

// hlsFeed cuts a stream into segments and lists them: a shared tuner's
// buffer, as the source sends the channel, or the buffer of a transcode of
// it with a profile, which the feed's own stand-in session x runs for every
// player that gets that profile (D155).
type hlsFeed struct {
	key    string   // the profile's name, "" for as the source sends it
	x      *session // the transcode's, nil for as the source sends it
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

// feedOf returns t's feed of the channel as its source sends it, starting
// it if t has none. It needs h.mu.
func (h *Hub) feedOf(t *session) *hlsFeed {
	if f := t.feeds[""]; f != nil {
		return f
	}
	f := newFeed("", nil, t.shift)
	if t.feeds == nil {
		t.feeds = map[string]*hlsFeed{}
	}
	t.feeds[""] = f
	t.shift.keepAtLeast(hlsKeep)
	h.log.Info("a channel is cut into HLS segments for players, from its shared tuner", "channel", t.channel,
		"source", h.source(t.sourceID).Name)
	h.tuners.Go(func() { h.runFeed(t.ctx, f) })
	return f
}

// newFeed returns a feed of b.
func newFeed(key string, x *session, b *shift) *hlsFeed {
	f := &hlsFeed{key: key, x: x, b: b}
	f.cond = sync.NewCond(&f.mu)
	f.cutter = hlscut.New(hlsMin, hlsMaxTarget, f.add)
	return f
}

// hlsFeedDevices numbers the stand-in sessions of transcodes for players,
// below every device's and recording's ID, so that each counts against
// the numbers of transcodes at a time on its own (D155).
var hlsFeedDevices atomic.Int64

const hlsFeedDeviceBase int64 = -(1 << 41)

// cutFeed returns the feed that a player's HLS session plays: the one that
// it has played, or, the first time, the feed of its profile on its
// tuner, which it starts if none runs. A profile that this server cannot
// transcode with now gives the channel as its source sends it, as the log
// says (D155).
func (h *Hub) cutFeed(ctx context.Context, s *session) *hlsFeed {
	h.mu.Lock()
	t := s.tuner
	if f := s.cutTo; f != nil {
		h.mu.Unlock()
		return f
	}
	key := ""
	if s.profile.Transcodes() {
		key = s.profile.Name
	}
	if f := t.feeds[key]; f != nil {
		s.cutTo = f
		h.mu.Unlock()
		return f
	}
	h.mu.Unlock()

	var x *session
	var jobs []ffmpegJob
	if key != "" {
		x = &session{nonce: mathrand.Uint64(), kind: kindEntry, deviceID: hlsFeedDeviceBase - hlsFeedDevices.Add(1),
			channelID: t.channelID, sourceID: t.sourceID, channel: t.channel, profile: s.profile, profileBy: s.profileBy}
		// What the stream holds, for whether the graphics decode it.
		x.details.Store(t.details.Load())
		if jobs = h.transcodeJobs(ctx, x); len(jobs) == 0 {
			key, x = "", nil
		}
	}
	var b *shift
	if x != nil {
		var err error
		if b, err = newShift(filepath.Join(h.shiftDir, strconv.FormatUint(x.nonce, 16)), hlsKeep, h.now); err != nil {
			h.log.Warn("cannot make a buffer for a channel's transcode for HLS players, so they get it as its source sends it",
				"channel", t.channel, "profile", s.profile.Name, "error", err)
			key, x, b = "", nil, nil
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if s.cutTo != nil {
		// Another request of the session found it meanwhile.
		if b != nil {
			b.close()
		}
		return s.cutTo
	}
	if f := t.feeds[key]; f != nil || key == "" {
		if b != nil {
			b.close()
		}
		if f == nil {
			f = h.feedOf(t)
		}
		s.cutTo = f
		return f
	}
	f := newFeed(key, x, b)
	x.ctx, x.cancel = context.WithCancel(t.ctx)
	x.started, x.lastActive = h.now(), h.now()
	if t.feeds == nil {
		t.feeds = map[string]*hlsFeed{}
	}
	t.feeds[key] = f
	s.cutTo = f
	t.shift.keepAtLeast(hlsKeep)
	h.log.Info("a channel is transcoded once for its HLS players, which share it", "channel", t.channel,
		"source", h.source(t.sourceID).Name, "profile", x.profile.Name, "set_by", x.profileBy)
	h.tuners.Go(func() { h.runCutTranscode(t, f, jobs) })
	h.tuners.Go(func() { h.runFeed(x.ctx, f) })
	return f
}

// dropFeed stops a feed that transcodes for players once no screen of its
// tuner plays it any more, which gives back its transcode's slot; the feed
// of the channel as its source sends it lasts as long as the tuner. It
// needs h.mu.
func (h *Hub) dropFeed(t *session, f *hlsFeed) {
	if f == nil || f.x == nil || t.feeds[f.key] != f {
		return
	}
	for _, v := range t.screens {
		if v.cutTo == f {
			return
		}
	}
	delete(t.feeds, f.key)
	f.x.cancel()
	h.log.Info("a channel's transcode for HLS players ended", "channel", t.channel, "source", h.source(t.sourceID).Name,
		"profile", f.key, "reason", "no player plays it any more")
}

// runCutTranscode transcodes the tuner's stream into the feed's own
// buffer while the feed lasts, with the first of jobs that can, through
// ffmpeg as a screen's transcode goes (D147, D148), from a little before
// the tuner's end. When ffmpeg stops, or the stream stalls, it starts
// again where it was, with the jobs that the hub would give now; with
// none, the stream goes into the buffer as its source sends it.
func (h *Hub) runCutTranscode(t *session, f *hlsFeed, jobs []ffmpegJob) {
	x := f.x
	defer f.b.close()
	off := int64(-1)
	for x.ctx.Err() == nil {
		if off < 0 {
			status, _, at := t.shift.start(x.ctx, hlsTranscodeBehind)
			if status != http.StatusOK {
				if x.ctx.Err() != nil || status == http.StatusServiceUnavailable {
					return
				}
				f.refused(status)
				if sleepContext(x.ctx, shiftRetry) != nil {
					return
				}
				continue
			}
			off = at
		}
		var dst sink = directSink{w: f.b, rc: http.NewResponseController(f.b)}
		if len(jobs) > 0 {
			dst = newRepairSink(h, x, x.ctx, f.b, http.NewResponseController(f.b), jobs)
		}
		t.shift.follow(x.ctx, off, func(at int64, p []byte) error {
			off = at + int64(len(p))
			_, err := dst.Write(p)
			return err
		})
		dst.finish()
		dst.close()
		if sleepContext(x.ctx, shiftRetry) != nil {
			return
		}
		jobs = h.transcodeJobs(x.ctx, x)
	}
}

// runFeed feeds the feed's buffer to the cutter until ctx ends, from
// hlsBehind before its end, or its start. When the buffer stalls, as when
// the source broke off and the tuner asks again, it goes on from where it
// was.
func (h *Hub) runFeed(ctx context.Context, f *hlsFeed) {
	defer f.end()
	off := int64(-1)
	for ctx.Err() == nil {
		if off < 0 {
			status, _, at := f.b.start(ctx, hlsBehind)
			if status != http.StatusOK {
				if ctx.Err() != nil || status == http.StatusServiceUnavailable {
					return
				}
				f.refused(status)
				if sleepContext(ctx, shiftRetry) != nil {
					return
				}
				continue
			}
			off = at
		}
		f.b.follow(ctx, off, func(at int64, p []byte) error {
			f.cutter.Write(at, p)
			off = at + int64(len(p))
			return nil
		})
		if sleepContext(ctx, shiftRetry) != nil {
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
// goes on with it. It returns "" for a channel that the hub cannot cut: a
// radio station, or one whose source sends HLS, which the shared tuner does
// not carry. A channel that the hub transcodes for the player is cut from
// the transcode (D155).
func (h *Hub) CutLink(playerID int64, entry store.PlayableEntry) string {
	if !h.sharable(entry) {
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
		f := h.cutFeed(r.Context(), s)
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
	f := h.cutFeed(r.Context(), s)
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
		// Each stream's own channel says what is transcoded of it (D147).
		profile, profileBy := h.streamProfile(r.Context(), c.deviceID, try)
		h.noteNever(r.Context(), c.deviceID, try)
		s, err := h.openShared(r.Context(), c, token, try)
		if err == nil {
			h.mu.Lock()
			s.profile, s.profileBy = profile, profileBy
			h.mu.Unlock()
			about := []any{"channel", entry.Name, "source", h.source(try.SourceID).Name, "device", c.deviceID}
			if profile.Transcodes() {
				about = append(about, "profile", profile.Name, "set_by", profileBy)
			}
			h.log.Info("a player plays a channel as HLS that the hub cuts from its shared tuner", about...)
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
