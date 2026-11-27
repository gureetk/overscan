// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// Output profiles (D110): an account's devices and players, or a device of
// its own, can get live channels made anew by the hub's ffmpeg, at up to a
// size, where they would otherwise get them as their sources send them.
// Such a transcode counts against the number at a time that the
// administrator lets the hub transcode, as a film's does (D73), and a
// stream that cannot have one plays as its source sends it.
//
// What is transcoded (D147): live TV as a whole, a lineup's group and a
// channel each transcode as needed, which leaves it to the device's
// profile; always, with a profile, for a device whose own profile sends
// channels as their sources send them; or never, whatever the device's
// profile. The store says which wins for a stream (PlayableEntry.Transcode).

// Who set the profile that a stream goes through, for the log.
const setByDevice = "device or account"

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

// streamProfile returns the profile through which a device or a player
// plays a lineup entry's stream, and who set it: its device or account,
// or, when the entry's channel, group or live TV says always or never, that
// one (D147). Recordings and the debug playlist get their sources' streams,
// whatever is set.
func (h *Hub) streamProfile(ctx context.Context, deviceID int64, entry store.PlayableEntry) (streamprofile.Profile, string) {
	if deviceID <= 0 {
		return streamprofile.Profile{}, ""
	}
	own := h.outputProfile(ctx, deviceID)
	switch t := entry.Transcode; {
	case t.Never:
		return streamprofile.Profile{}, entry.TranscodeBy
	case t.Profile != "" && !own.Transcodes():
		p, err := h.store.TranscodeProfile(ctx, t.Profile)
		if err != nil {
			h.log.Error("cannot look up the profile that a channel is transcoded with; it plays as its device's profile says",
				"channel", entry.Name, "profile", t.Profile, "error", err)
			return own, setByDevice
		}
		return p, entry.TranscodeBy
	}
	return own, setByDevice
}

// noteNever logs, when a device's profile would transcode a stream that its
// channel, group or live TV says is never transcoded, that it plays as its
// source sends it.
func (h *Hub) noteNever(ctx context.Context, deviceID int64, entry store.PlayableEntry) {
	if !entry.Transcode.Never || deviceID <= 0 {
		return
	}
	if own := h.outputProfile(ctx, deviceID); own.Transcodes() {
		h.log.Info("a stream plays as its source sends it, since it is never transcoded", "channel", entry.Name,
			"source", h.source(entry.SourceID).Name, "device", deviceID, "profile", own.Name, "set_by", entry.TranscodeBy)
	}
}

// transcodeJob returns the job of transcoding s's stream to its profile,
// and whether there is one: the profile transcodes, the hub's ffmpeg has
// the encoder that it needs, and the administrator lets the hub transcode.
// When the profile transcodes and the hub may not, the log says why, and
// the stream plays as its source sends it.
func (h *Hub) transcodeJob(ctx context.Context, s *session) (ffmpegJob, bool) {
	if !s.profile.Transcodes() {
		return ffmpegJob{}, false
	}
	about := []any{"channel", s.channel, "source", h.source(s.sourceID).Name, "device", s.deviceID, "profile", s.profile.Name}
	if s.profileBy != "" {
		about = append(about, "set_by", s.profileBy)
	}
	if h.ffmpeg == "" || !h.encoder.Has(s.profile.Encoder()) {
		h.log.Warn("a stream's output profile transcodes it, but this server's ffmpeg lacks the encoder that it needs; it plays as its source sends it",
			append(about, "needs", s.profile.Encoder())...)
		return ffmpegJob{}, false
	}
	limit := h.transcodeCap(ctx)
	if limit == 0 {
		h.log.Warn("a stream's output profile transcodes it, but transcoding is off; it plays as its source sends it", about...)
		return ffmpegJob{}, false
	}
	return ffmpegJob{args: s.profile.Args(), doing: "transcoding", does: "transcode", about: about[6:], watch: true,
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
// stream of an entry now, so that a channel whose source sends HLS gets a
// continuous link, which the relay makes one stream for ffmpeg (D61).
func (h *Hub) transcodesLive(deviceID int64, entry store.PlayableEntry) bool {
	ctx := context.Background()
	p, _ := h.streamProfile(ctx, deviceID, entry)
	return h.ffmpeg != "" && h.encoder.Has(p.Encoder()) && p.Transcodes() && h.transcodeCap(ctx) > 0
}
