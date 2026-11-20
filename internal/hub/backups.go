// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"time"

	"overscan.gureet.ca/internal/store"
)

// A lineup channel's backups (D109): when a channel's own stream cannot
// start, the hub tries its backups' streams in turn, as the administrator
// ordered them. A stream is passed over when its source has no room for
// the device, gives no address for it, or, on a connection of its own,
// does not answer with it before the player has had anything. A stream
// that failed lately is tried last, so that a player that asks again gets
// a backup at once.

// failingFor is how long a channel whose stream failed to start goes after
// the others among a channel's streams.
const failingFor = 10 * time.Minute

// markFailing remembers that a channel's stream failed to start.
func (h *Hub) markFailing(channelID int64) {
	h.mu.Lock()
	h.failing[channelID] = h.now().Add(failingFor)
	h.mu.Unlock()
}

// candidates returns the streams that play a lineup entry, in the order to
// try them: its own, then its backups, with those that failed lately last.
func (h *Hub) candidates(entry store.PlayableEntry) []store.PlayableEntry {
	all := append([]store.PlayableEntry{entry}, entry.Backups...)
	if len(all) == 1 {
		return all
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	failing := func(p store.PlayableEntry) bool {
		until, found := h.failing[p.ChannelID]
		if found && !now.Before(until) {
			delete(h.failing, p.ChannelID)
			return false
		}
		return found
	}
	slices.SortStableFunc(all, func(a, b store.PlayableEntry) int {
		switch fa, fb := failing(a), failing(b); {
		case fa && !fb:
			return 1
		case fb && !fa:
			return -1
		}
		return 0
	})
	return all
}

// openChannel opens the session that c starts for a lineup entry, and
// plays it, from the first of its streams that can start (D109). Without
// backups it does what it always did.
func (h *Hub) openChannel(w http.ResponseWriter, r *http.Request, c claims, token string, entry store.PlayableEntry, continuous bool) {
	tries := h.candidates(entry)
	for i, try := range tries {
		more := i < len(tries)-1
		passOver := func(why string) {
			h.log.Warn("a channel's stream cannot start; the hub tries its next stream", "channel", entry.Name,
				"source", h.source(try.SourceID).Name, "device", c.deviceID, "reason", why)
		}
		playing := func() {
			if try.ChannelID != entry.ChannelID {
				h.log.Info("a channel plays from a backup", "channel", entry.Name, "source", h.source(try.SourceID).Name,
					"device", c.deviceID)
			}
		}
		// While the shared tuner is on, a continuous stream comes from
		// the channel's tuner, which every screen on it shares (D93).
		if continuous && h.sharable(try) && h.sharing(r.Context()) {
			s, err := h.openShared(r.Context(), c, token, try)
			switch {
			case err == nil:
				playing()
				h.serveShift(w, r, s)
				return
			case errors.Is(err, errNotShared):
				// It plays on a connection of its own, below.
			case more && !errors.Is(err, ErrAccountLimit):
				passOver(err.Error())
				continue
			default:
				h.resolveFailed(w, r, "channel", entry.Name, try.SourceID, c.deviceID, err)
				return
			}
		}
		opening := &session{token: token, channelID: try.ChannelID, sourceID: try.SourceID,
			channel: entry.Name, streamURL: try.StreamURL, repair: try.Repair, repairHLS: try.RepairHLS}
		opening.account, opening.accountLimit = h.viewer(r.Context(), c.deviceID)
		if h.source(try.SourceID).Resolves {
			if !h.roomFor(try.SourceID, c.deviceID) {
				if more {
					passOver(ErrLimit.Error())
					continue
				}
				h.resolveFailed(w, r, "channel", entry.Name, try.SourceID, c.deviceID, ErrLimit)
				return
			}
			res, err := h.resolve(r.Context(), try.SourceID, PlaysChannel, try.StreamURL)
			if err != nil {
				if more {
					passOver(err.Error())
					continue
				}
				h.resolveFailed(w, r, "channel", entry.Name, try.SourceID, c.deviceID, err)
				return
			}
			opening.streamURL, opening.ref, opening.refKind = res.Address, try.StreamURL, PlaysChannel
			if res.Hold != nil {
				opening.release = res.Hold()
			}
		}
		if c.shift && continuous {
			opening.shift = h.openShift(r.Context(), c, entry.Name)
		}
		s, err := h.admit(c, opening)
		if s != opening {
			// Refused, or another request opened the session first.
			if opening.release != nil {
				opening.release()
			}
			if opening.shift != nil {
				opening.shift.close()
			}
		}
		if err != nil {
			if more && errors.Is(err, ErrLimit) {
				passOver(err.Error())
				continue
			}
			h.refused(w, err, "channel", entry.Name, try.SourceID, c.deviceID)
			return
		}
		if s.shift != nil {
			if s == opening {
				h.tuners.Go(func() { h.tune(s) })
				playing()
			}
			h.serveShift(w, r, s)
			return
		}
		if s != opening {
			// Another request of the same link opened it first.
			h.proxy(w, r, s, s.streamURL, continuous)
			return
		}
		if h.proxyFirst(w, r, s, s.streamURL, continuous, !more, playing) {
			return
		}
		h.mu.Lock()
		h.end(s, "its source did not send it, so the channel's next stream plays")
		h.mu.Unlock()
		passOver("its source did not send it")
	}
}

// proxyFirst is proxy for the stream of a channel that may have another
// to try after it (D109). If its source does not answer with it, it
// answers the player nothing and returns false, so that the next stream
// can, unless this is the last, which the player is told about as proxy
// would. Otherwise it calls started once the source answers, plays the
// stream, and returns true.
func (h *Hub) proxyFirst(w http.ResponseWriter, r *http.Request, s *session, target string, continuous, last bool, started func()) bool {
	h.begin(s)
	defer h.finish(s)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	resp, err := h.get(ctx, s, target)
	switch {
	case err == nil:
		started()
		h.serve(ctx, w, s, target, continuous, resp)
		return true
	case ctx.Err() != nil:
		return true // the player went away: there is no one to play to
	case last:
		h.getFailed(w, s, err)
		return true
	}
	h.getFailed(nil, s, err)
	return false
}
