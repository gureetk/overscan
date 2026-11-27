// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"path/filepath"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/store"
)

// The shared tuner (D93). While it is on, a channel's continuous stream
// comes from its source once, however many screens of the household play
// it. A tuner reads the stream into a buffer on disk, after the repeat
// filter and repair, as a timeshift buffer's tuner does (D92), and every
// box, Stalker client and recording that plays the channel reads the
// buffer, from its end. The tuner holds one of its source's connections
// for as long as any of them plays: it is a session of its own, and the
// screens' sessions hold none.

// sharedKeep is how much of a shared channel its buffer keeps while
// timeshift is off: enough for a screen that reads slowly to catch up.
const sharedKeep = 30 * time.Second

// errNotShared says that a channel cannot play from a shared tuner, whose
// buffer could not be made: it plays on a connection of its own.
var errNotShared = errors.New("the channel cannot be shared")

// sharable reports whether a channel's continuous stream may come from a
// shared tuner: the hub has a folder for buffers, the channel is not a
// radio station, and its address is not HLS, which players fetch a
// playlist at a time. A source that gives an address for each stream as it
// starts can give one that is HLS: the tuner relays it as one stream.
func (h *Hub) sharable(entry store.PlayableEntry) bool {
	return h.shiftDir != "" && !entry.Radio && (h.source(entry.SourceID).Resolves || !hlsAddress(entry.StreamURL))
}

// sharing reports whether the shared tuner is on.
func (h *Hub) sharing(ctx context.Context) bool {
	on, err := h.store.SharedTuner(ctx)
	if err != nil {
		h.log.Error("cannot read the shared tuner's setting", "error", err)
		return false
	}
	return on
}

// AvailableChannel reports, as Available does, whether a channel's source,
// or the source of one of its backups (D109), can take a new stream for a
// device. While the shared tuner is on, a channel that a tuner plays
// always can: one more screen on it takes no new connection (D93).
func (h *Hub) AvailableChannel(ctx context.Context, entry store.PlayableEntry, deviceID int64) bool {
	sharing := h.sharing(ctx)
	for _, try := range append([]store.PlayableEntry{entry}, entry.Backups...) {
		if sharing && h.sharable(try) {
			h.mu.Lock()
			t := h.shared[try.ChannelID]
			h.mu.Unlock()
			if t != nil {
				return true
			}
		}
		if h.Available(try.SourceID, deviceID) {
			return true
		}
	}
	return false
}

// openShared returns the session that c starts for a channel that plays
// from a shared tuner: the channel's tuner if one runs, or else a new one,
// which takes one of its source's connections. On a source at its limit,
// the device's own streams there end to make room if they can. It fails
// with ErrLimit, ErrNoLink with the Resolver's reason, or errNotShared.
func (h *Hub) openShared(ctx context.Context, c claims, token string, entry store.PlayableEntry) (*session, error) {
	screen := &session{token: token, channelID: entry.ChannelID, sourceID: entry.SourceID, channel: entry.Name,
		streamURL: entry.StreamURL, repair: entry.Repair, repairHLS: entry.RepairHLS, canShift: c.shift, cut: c.cut}
	screen.account, screen.accountLimit = h.viewer(ctx, c.deviceID)
	if s, ok, err := h.joinTuner(c, screen); ok || err != nil {
		return s, err
	}

	t := &session{channelID: entry.ChannelID, sourceID: entry.SourceID, channel: entry.Name,
		streamURL: entry.StreamURL, repair: entry.Repair, repairHLS: entry.RepairHLS, shared: true, nonce: mathrand.Uint64()}
	keep := sharedKeep
	if minutes, err := h.store.Timeshift(ctx); err == nil && minutes > 0 {
		// A box that plays the channel can pause it and go back.
		keep = time.Duration(minutes) * time.Minute
	}
	b, err := newShift(filepath.Join(h.shiftDir, strconv.FormatUint(t.nonce, 16)), keep, h.now)
	if err != nil {
		h.log.Warn("cannot make a shared channel's buffer, so the channel plays on a connection of its own",
			"channel", entry.Name, "error", err)
		return nil, errNotShared
	}
	t.shift = b
	if h.source(entry.SourceID).Resolves {
		if !h.roomFor(entry.SourceID, c.deviceID) {
			b.close()
			return nil, ErrLimit
		}
		res, err := h.resolve(ctx, entry.SourceID, PlaysChannel, entry.StreamURL)
		if err != nil {
			b.close()
			if errors.Is(err, ErrLimit) {
				return nil, err
			}
			return nil, fmt.Errorf("%w: %w", ErrNoLink, err)
		}
		t.streamURL, t.ref, t.refKind = res.Address, entry.StreamURL, PlaysChannel
		if res.Hold != nil {
			t.release = res.Hold()
		}
	}
	return h.startTuner(c, screen, t)
}

// joinTuner opens screen, the session that c starts, on its channel's
// shared tuner if one runs, and reports whether it did. If c's session is
// open already, it returns that. It fails with ErrAccountLimit while the
// screen's account plays as many streams as it may (D102).
func (h *Hub) joinTuner(c claims, screen *session) (*session, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.sweep(now)
	if open, ok := h.sessions[c.nonce]; ok && !open.shared {
		return open, true, nil
	}
	t := h.shared[screen.channelID]
	if t == nil {
		return nil, false, nil
	}
	if !h.accountRoom(screen.account, screen.accountLimit, c.deviceID, now) {
		return nil, false, ErrAccountLimit
	}
	h.attach(c, screen, t, now)
	return screen, true, nil
}

// startTuner starts t, a new shared tuner, with screen, the session that c
// starts, on it, once its source has room for it; or, if another request
// started the channel's tuner meanwhile, puts screen on that one.
func (h *Hub) startTuner(c claims, screen, t *session) (*session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	drop := func() {
		t.shift.close()
		if t.release != nil {
			t.release()
		}
	}
	now := h.now()
	h.sweep(now)
	if open, ok := h.sessions[c.nonce]; ok && !open.shared {
		drop()
		return open, nil
	}
	if !h.accountRoom(screen.account, screen.accountLimit, c.deviceID, now) {
		drop()
		return nil, ErrAccountLimit
	}
	if other := h.shared[t.channelID]; other != nil {
		drop()
		h.attach(c, screen, other, now)
		return screen, nil
	}
	if _, taken := h.sessions[t.nonce]; taken {
		drop()
		return nil, errNotShared
	}
	if !h.makeRoom(t.sourceID, c.deviceID, now) {
		drop()
		return nil, ErrLimit
	}
	t.ctx, t.cancel = context.WithCancel(context.Background())
	t.kind, t.entryID = kindEntry, c.entryID
	t.started, t.lastActive = now, now
	t.resources, t.indexes = map[int]string{}, map[string]int{}
	h.sessions[t.nonce] = t
	h.shared[t.channelID] = t
	h.attach(c, screen, t, now)
	h.tuners.Go(func() { h.tune(t) })
	return screen, nil
}

// attach opens screen, the session that c starts, on the shared tuner t.
// It needs h.mu.
func (h *Hub) attach(c claims, screen, t *session, now time.Time) {
	screen.ctx, screen.cancel = context.WithCancel(context.Background())
	screen.nonce, screen.kind, screen.deviceID, screen.entryID = c.nonce, c.kind, c.deviceID, c.entryID
	screen.started, screen.lastActive = now, now
	screen.resources, screen.indexes = map[int]string{}, map[string]int{}
	screen.tuner = t
	t.screens = append(t.screens, screen)
	h.sessions[c.nonce] = screen
	h.log.Info("stream started", "channel", screen.channel, "source", h.source(screen.sourceID).Name,
		"device", screen.deviceID, "shared_by", len(t.screens))
}

// inUse reports whether a shared tuner has a screen that plays from it now.
// It needs h.mu.
func (s *session) inUse(now time.Time) bool {
	for _, v := range s.screens {
		if v.active(now) {
			return true
		}
	}
	return false
}

// holding reports whether s holds one of its source's connections now. A
// screen on a shared tuner never does: its tuner does, while a screen
// plays from it. It needs h.mu.
func (h *Hub) holding(s *session, now time.Time) bool {
	switch {
	case s.ended:
		return false
	case s.tuner != nil:
		return false
	case s.shared:
		return s.inUse(now)
	}
	return s.holds(now)
}

// owns reports whether s is the device's own stream: its own session, or a
// shared tuner whose screens that play now are all the device's. It needs
// h.mu.
func (h *Hub) owns(s *session, deviceID int64, now time.Time) bool {
	if !s.shared {
		return s.deviceID == deviceID
	}
	mine := false
	for _, v := range s.screens {
		if !v.active(now) {
			continue
		}
		if v.deviceID != deviceID {
			return false
		}
		mine = true
	}
	return mine
}

// endIdleTuners ends a source's shared tuners that no screen plays from
// now, which hold a connection for no one. It needs h.mu.
func (h *Hub) endIdleTuners(sourceID int64, now time.Time) {
	for _, t := range h.shared {
		if t.sourceID == sourceID && !t.inUse(now) {
			h.end(t, "no screen plays it any more")
		}
	}
}
