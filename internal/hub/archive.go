// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Past programmes (D81). A channel whose source keeps an archive plays a
// past programme from it: the source makes a stream of it from a time, at
// an address that the store's catchup.Archive gives, which can hold the
// source's login and so stays on the server. The hub proxies that stream as
// it does a channel's, through the repeat filter and, for a channel that is
// repaired, through ffmpeg; HLS is relayed as one continuous stream. The
// stream has no byte positions, so a player seeks by asking again from a
// time: a session makes one request to its source at a time, and a new one
// ends the one before, as a film's does (D64). Archives take times to the
// minute, so a stream from a time starts at the whole minute at or before
// it, which /archive/<token>/start says.

// ArchiveLink returns the path of a new link that plays a lineup entry's
// past programme, which began at start and ended at stop, for a device,
// and the path at which the hub says where its stream from a time starts.
func (h *Hub) ArchiveLink(deviceID int64, entry store.PlayableEntry, start, stop time.Time) (link, startPath string) {
	c := claims{
		kind:     kindArchive,
		entryID:  entry.EntryID,
		deviceID: deviceID,
		expires:  h.now().Add(linkTTL).Unix(),
		nonce:    rand.Uint64(),
		start:    start.Unix(),
		stop:     stop.Unix(),
	}
	token := h.sign(c)
	return "/archive/" + token + ".ts", "/archive/" + token + "/start"
}

// archiveFrom returns when a past programme's stream from at seconds into
// it starts: the whole minute at or before that time.
func archiveFrom(c claims, at float64) time.Time {
	t := c.start + int64(math.Floor(at))
	return time.Unix(t-((t%60)+60)%60, 0).UTC()
}

// archiveOffset reads the t parameter of a request for a past programme:
// how far into it, in seconds, from 0 to its length; 0 if not given.
func archiveOffset(r *http.Request, c claims) (float64, bool) {
	v := r.URL.Query().Get("t")
	if v == "" {
		return 0, true
	}
	t, err := strconv.ParseFloat(v, 64)
	return t, err == nil && t >= 0 && t < float64(c.stop-c.start) && !math.IsNaN(t)
}

// serveArchive serves /archive/<token>.ts?t=<seconds>, the stream of a past
// programme from that far into it, which starts or goes on with its
// session, and /archive/<token>/start?t=<seconds>, where that stream
// starts, in seconds from the programme's start, which can be less than 0.
func (h *Hub) serveArchive(w http.ResponseWriter, r *http.Request, rest string) {
	first, sub, hasSub := strings.Cut(rest, "/")
	token := strings.TrimSuffix(first, ".ts")
	c, ok := h.verify(token)
	if !ok || c.kind != kindArchive || hasSub && sub != "start" {
		http.Error(w, "invalid link", http.StatusForbidden)
		return
	}
	at, ok := archiveOffset(r, c)
	if !ok {
		http.Error(w, "t must be a time in seconds within the programme", http.StatusBadRequest)
		return
	}
	from := archiveFrom(c, at)
	if hasSub {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			T int64 `json:"t"`
		}{T: from.Unix() - c.start})
		return
	}
	s := h.lookup(c.nonce)
	if s == nil {
		if h.now().Unix() > c.expires {
			http.Error(w, "this link has expired; ask for a new one", http.StatusForbidden)
			return
		}
		entry, err := h.store.PlayableArchive(r.Context(), c.entryID)
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrNoArchive) {
			http.Error(w, "this channel's archive is not available", http.StatusNotFound)
			return
		}
		if err != nil {
			h.log.Error("cannot look up a channel's archive", "entry", c.entryID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		opening := &session{token: token, channelID: entry.ChannelID, sourceID: entry.SourceID, channel: entry.Name,
			repair: entry.Repair, repairHLS: entry.RepairHLS, archive: &entry.Archive,
			aired: time.Unix(c.start, 0).UTC()}
		if p, err := h.store.ProgrammeAt(r.Context(), entry.LineupID, entry.EntryID, opening.aired); err == nil {
			opening.programme = p.Title
		}
		s, err = h.admit(c, opening)
		if err != nil {
			src := h.source(entry.SourceID)
			h.log.Warn("stream refused: the source is at its connection limit",
				"channel", entry.Name, "source", src.Name, "limit", src.MaxConnections, "device", c.deviceID)
			http.Error(w, "the source has no free connection", http.StatusServiceUnavailable)
			return
		}
		if s == opening {
			h.log.Info("a past programme plays from its source's archive", "channel", s.channel, "programme", s.programme,
				"aired", s.aired.Format(time.RFC3339), "length", time.Duration(c.stop-c.start)*time.Second,
				"source", h.source(s.sourceID).Name, "device", s.deviceID)
		}
	}
	ctx, release, ok := h.takeTurn(r.Context(), s, w)
	if !ok {
		return
	}
	defer release()
	length := time.Unix(c.stop, 0).Sub(from)
	address, err := s.archive.Address(from, length, h.now())
	if err != nil {
		h.log.Warn("cannot make the address of a past programme", "channel", s.channel, "programme", s.programme,
			"source", h.source(s.sourceID).Name, "error", err)
		http.Error(w, "the source's archive cannot be played", http.StatusBadGateway)
		return
	}
	h.log.Debug("a player asked for a past programme from a time", "channel", s.channel, "device", s.deviceID,
		"from", from.Format(time.RFC3339), "length", length)
	h.proxyIn(ctx, w, s, address, true)
}
