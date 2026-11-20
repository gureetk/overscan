// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"net/http"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Each account may have a limit on the streams that its devices and
// players play at once (D102). The hub counts them as it counts a source's
// connections (D36): a device that starts a new stream while its account is
// at its limit replaces its own oldest stream, and another device's stream
// is never cut off, so the new one is refused. A screen on a shared tuner
// counts as a stream of its own, though it takes no connection of its own
// (D93): the limit is on what the account's people watch, not on what the
// source sends. Recordings show their box's account but count against no
// account's limit, and the debug playlist plays as no account.

// viewer returns the account that a device's streams count against, and
// that account's limit of streams at once, 0 for none.
func (h *Hub) viewer(ctx context.Context, deviceID int64) (account int64, limit int) {
	switch {
	case deviceID == 0:
		return 0, 0 // the debug playlist
	case IsPlayer(deviceID):
		p, ok := h.PlayerOf(deviceID)
		if !ok {
			return 0, 0
		}
		limit, err := h.store.AccountStreams(ctx, p.AccountID)
		if err != nil {
			h.log.Error("cannot look up an account's limit of streams", "account", p.AccountID, "error", err)
		}
		return p.AccountID, limit
	case deviceID < 0:
		// A recording plays as a device of its own (D89).
		account, err := h.store.RecordingAccount(ctx, -deviceID)
		if err != nil {
			h.log.Error("cannot look up a recording's account", "recording", -deviceID, "error", err)
		}
		return account, 0
	}
	account, limit, err := h.store.StreamsOf(ctx, deviceID)
	if err != nil {
		h.log.Error("cannot look up a device's account; its streams count against no account's limit",
			"device", deviceID, "error", err)
		return 0, 0
	}
	return account, limit
}

// counts reports whether s is one of an account's streams now, against the
// account's limit. It needs h.mu.
func (s *session) counts(account int64, now time.Time) bool {
	return account != 0 && s.account == account && s.deviceID > 0 && !s.shared && s.active(now)
}

// accountRoom reports whether an account at the given limit can take a new
// stream for a device, ending the device's own oldest streams of the
// account if it is at its limit. It needs h.mu.
func (h *Hub) accountRoom(account int64, limit int, deviceID int64, now time.Time) bool {
	if account == 0 || limit == 0 || deviceID <= 0 {
		return true
	}
	for {
		count := 0
		var own *session
		for _, o := range h.sessions {
			if !o.counts(account, now) {
				continue
			}
			count++
			if o.deviceID == deviceID && (own == nil || o.started.Before(own.started)) {
				own = o
			}
		}
		if count < limit {
			return true
		}
		if own == nil {
			return false
		}
		h.end(own, "replaced by a new stream from the same device")
	}
}

// accountFull reports whether a device's account plays as many streams as
// it may, besides the device's own, which a new stream of the device would
// replace.
func (h *Hub) accountFull(ctx context.Context, deviceID int64) bool {
	account, limit := h.viewer(ctx, deviceID)
	if account == 0 || limit == 0 || deviceID <= 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.sweep(now)
	others := 0
	for _, o := range h.sessions {
		if o.counts(account, now) && o.deviceID != deviceID {
			others++
		}
	}
	return others >= limit
}

// AccountStreams returns how many streams an account's devices and players
// play now, as its limit counts them.
func (h *Hub) AccountStreams(accountID int64) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.sweep(now)
	n := 0
	for _, s := range h.sessions {
		if s.counts(accountID, now) {
			n++
		}
	}
	return n
}

// Room says whether a device can start a stream from a source: nil if it
// can, ErrAccountLimit while the device's account plays as many streams as
// it may, or ErrLimit while the source does, and none of the device's own
// streams would make room.
func (h *Hub) Room(ctx context.Context, sourceID, deviceID int64) error {
	if h.accountFull(ctx, deviceID) {
		return ErrAccountLimit
	}
	if !h.Available(sourceID, deviceID) {
		return ErrLimit
	}
	return nil
}

// RoomForChannel says, as Room does, whether a device can start a channel.
// While the shared tuner is on, a channel that a tuner plays takes no new
// connection of its source (D93), though it still counts against the
// device's account.
func (h *Hub) RoomForChannel(ctx context.Context, entry store.PlayableEntry, deviceID int64) error {
	if h.accountFull(ctx, deviceID) {
		return ErrAccountLimit
	}
	if !h.AvailableChannel(ctx, entry, deviceID) {
		return ErrLimit
	}
	return nil
}

// refused answers a request whose stream could not start, err saying why:
// ErrAccountLimit, ErrLimit, or the Resolver's reason.
func (h *Hub) refused(w http.ResponseWriter, err error, what, name string, sourceID, deviceID int64) {
	src := h.source(sourceID)
	switch {
	case errors.Is(err, ErrAccountLimit):
		h.log.Warn("stream refused: the account plays as many streams as it may", what, name, "source", src.Name,
			"device", deviceID)
		http.Error(w, "this account plays as many streams as it may", http.StatusServiceUnavailable)
	default:
		h.log.Warn("stream refused: the source is at its connection limit", what, name, "source", src.Name,
			"limit", src.MaxConnections, "device", deviceID)
		http.Error(w, "the source has no free connection", http.StatusServiceUnavailable)
	}
}
