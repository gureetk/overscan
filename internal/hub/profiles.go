// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"

	"overscan.gureet.ca/internal/streamprofile"
)

// Output profiles (D110): an account's devices and players, or a device of
// its own, can get live channels made H.264 by the hub's ffmpeg, at up to
// a size, where they would otherwise get them as their sources send them.
// Such a transcode counts against the number at a time that the
// administrator lets the hub transcode, as a film's does (D73), and a
// stream that cannot have one plays as its source sends it.

// outputProfile returns the output profile through which a device or a
// player plays live channels: a player's account's, or a device's own, or
// else its account's. Recordings and the debug playlist get their sources'
// streams.
func (h *Hub) outputProfile(ctx context.Context, deviceID int64) streamprofile.Profile {
	var p streamprofile.Profile
	var err error
	switch {
	case deviceID <= 0:
		return streamprofile.Profile{}
	case IsPlayer(deviceID):
		player, ok := h.PlayerOf(deviceID)
		if !ok {
			return streamprofile.Profile{}
		}
		p, err = h.store.AccountOutputProfile(ctx, player.AccountID)
	default:
		p, err = h.store.OutputProfileOf(ctx, deviceID)
	}
	if err != nil {
		h.log.Error("cannot look up an output profile; the stream plays as its source sends it", "device", deviceID, "error", err)
		return streamprofile.Profile{}
	}
	return p
}

// transcodeJob returns the job of transcoding s's stream to its output
// profile, and whether there is one: the profile transcodes, the hub's
// ffmpeg makes H.264, and the administrator lets the hub transcode. When
// the profile transcodes and the hub may not, the log says why, and the
// stream plays as its source sends it.
func (h *Hub) transcodeJob(ctx context.Context, s *session) (ffmpegJob, bool) {
	if !s.profile.Transcodes() {
		return ffmpegJob{}, false
	}
	about := []any{"channel", s.channel, "source", h.source(s.sourceID).Name, "device", s.deviceID, "profile", s.profile.Name}
	if h.ffmpeg == "" || !h.encoder.H264 {
		h.log.Warn("a stream's output profile transcodes it, but this server's ffmpeg cannot make H.264; it plays as its source sends it",
			about...)
		return ffmpegJob{}, false
	}
	limit := h.transcodeCap(ctx)
	if limit == 0 {
		h.log.Warn("a stream's output profile transcodes it, but transcoding is off; it plays as its source sends it", about...)
		return ffmpegJob{}, false
	}
	return ffmpegJob{args: s.profile.Args(), doing: "transcoding", does: "transcode", about: []any{"profile", s.profile.Name},
		take: func(about []any) bool {
			if !h.takeEncode(s.deviceID, limit) {
				h.log.Warn("the server transcodes as many streams as it may; this one plays as its source sends it",
					append(about, "most", limit)...)
				return false
			}
			s.encoding.Add(1)
			return true
		},
		give: func() {
			h.endEncode(s.deviceID)
			s.encoding.Add(-1)
		},
	}, true
}

// transcodesLive reports whether the hub transcodes a device's or player's
// live channels to its output profile now, so that a channel whose source
// sends HLS gets a continuous link, which the relay makes one stream for
// ffmpeg (D61).
func (h *Hub) transcodesLive(deviceID int64) bool {
	ctx := context.Background()
	return h.ffmpeg != "" && h.encoder.H264 && h.outputProfile(ctx, deviceID).Transcodes() && h.transcodeCap(ctx) > 0
}
