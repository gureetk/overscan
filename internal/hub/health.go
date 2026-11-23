// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/streaminfo"
)

// Channel health, from what happens as people watch (D134, D138): the hub
// notes in the store when a channel's stream fails to start, when one
// starts, and what a stream holds as it passes through, read from its first
// megabytes. It never opens a channel to find out.

// noteTimeout bounds a note's write to the store.
const noteTimeout = 10 * time.Second

// note writes to the store in the background, so that a stream never waits
// for it; what fails is logged.
func (h *Hub) note(what string, write func(context.Context) error) {
	if h.store == nil {
		return
	}
	h.notes.Go(func() {
		ctx, cancel := context.WithTimeout(context.Background(), noteTimeout)
		defer cancel()
		if err := write(ctx); err != nil {
			h.log.Warn("cannot note "+what, "error", err)
		}
	})
}

// noteFailed notes that a live channel's stream failed to start.
func (h *Hub) noteFailed(s *session) {
	if s.kind != kindEntry || s.channelID == 0 {
		return
	}
	id, at := s.channelID, h.now()
	h.note("that a channel failed to start", func(ctx context.Context) error {
		return h.store.NoteStreamFailed(ctx, id, at)
	})
}

// passed is told the data of a live channel's stream as it reaches its
// player: the first notes that the channel plays, and the stream's details
// are read from it until they are known.
func (h *Hub) passed(s *session, b []byte) {
	if s.kind != kindEntry || s.channelID == 0 || len(b) == 0 {
		return
	}
	if s.played.CompareAndSwap(false, true) {
		id, at, name, src := s.channelID, h.now(), s.channel, h.source(s.sourceID).Name
		h.note("that a channel played", func(ctx context.Context) error {
			was, err := h.store.NoteStreamPlayed(ctx, id, at)
			if was {
				h.log.Info("a channel hidden until it played again plays: it shows again", "channel", name, "source", src)
			}
			return err
		})
	}
	s.detMu.Lock()
	defer s.detMu.Unlock()
	if s.detected {
		return
	}
	if s.detector == nil {
		s.detector = streaminfo.New()
	}
	s.detector.Write(b)
	if !s.detector.Done() {
		return
	}
	s.detected = true
	d, ok := s.detector.Details()
	s.detector = nil
	if !ok {
		return
	}
	s.details.Store(&d)
	id, at := s.channelID, h.now()
	h.note("what a channel's stream holds", func(ctx context.Context) error {
		return h.store.NoteStreamDetails(ctx, id, d, at)
	})
}

// streamID is how the admin knows a stream: its session's nonce.
func streamID(s *session) string { return strconv.FormatUint(s.nonce, 16) }

// StopStream ends the stream with the given ID, as Stream.ID gives it, for
// the reason given, which the log says; for a screen of a shared tuner, the
// screen alone (D93). It reports whether there was such a stream.
func (h *Hub) StopStream(id, reason string) bool {
	nonce, err := strconv.ParseUint(id, 16, 64)
	if err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[nonce]
	if !ok || s.ended || s.shared {
		return false
	}
	h.end(s, reason)
	return true
}
