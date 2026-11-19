// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"net/url"
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
//
// A Stalker portal's channel plays a past programme at the link that the
// portal gives for it (D84), which the Resolver asks for as the session
// opens and again when it lapses. The link plays the programme from its
// start, and is served as a portal's film is (D77): the player's ranges go
// to the portal, so the player seeks in it itself. One that is HLS is
// relayed as one stream instead, which plays from the programme's start.

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

// ArchiveRef returns what the Resolver gets to play a past programme of a
// channel of a source that resolves (D84): the source's own ID for the
// channel, and when the programme began.
func ArchiveRef(channel string, start time.Time) string {
	return url.Values{"channel": {channel}, "start": {strconv.FormatInt(start.Unix(), 10)}}.Encode()
}

// ParseArchiveRef reads a reference that ArchiveRef made.
func ParseArchiveRef(ref string) (channel string, start time.Time, ok bool) {
	v, err := url.ParseQuery(ref)
	if err != nil {
		return "", time.Time{}, false
	}
	at, err := strconv.ParseInt(v.Get("start"), 10, 64)
	channel = v.Get("channel")
	if err != nil || at <= 0 || channel == "" || len(channel) > 256 {
		return "", time.Time{}, false
	}
	return channel, time.Unix(at, 0).UTC(), true
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
// A portal's past programme plays from its start, whatever t says, and
// seeks with ranges.
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
		if h.source(entry.SourceID).Resolves {
			if !h.roomFor(entry.SourceID, c.deviceID) {
				h.resolveFailed(w, r, "channel", entry.Name, entry.SourceID, c.deviceID, ErrLimit)
				return
			}
			ref := ArchiveRef(entry.Portal, opening.aired)
			res, err := h.resolve(r.Context(), entry.SourceID, PlaysArchive, ref)
			if err != nil {
				h.resolveFailed(w, r, "channel", entry.Name, entry.SourceID, c.deviceID, err)
				return
			}
			opening.archive, opening.streamURL, opening.ref, opening.refKind = nil, res.Address, ref, PlaysArchive
			if res.Hold != nil {
				opening.release = res.Hold()
			}
		}
		s, err = h.admit(c, opening)
		if s != opening && opening.release != nil {
			// Refused, or another request opened the session first.
			opening.release()
		}
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
	if s.archive == nil {
		h.servePortalArchive(w, r, s)
		return
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

// servePortalArchive serves a request for a portal's past programme, as part
// of s (D84): a link that is HLS is relayed as one stream, from the
// programme's start, and asked for again for each request after the first,
// since it can lapse once played; any other is served as a portal's film
// is, ranges and all.
func (h *Hub) servePortalArchive(w http.ResponseWriter, r *http.Request, s *session) {
	if !hlsAddress(s.address()) {
		h.serveFile(w, r, s)
		return
	}
	ctx, release, ok := h.takeTurn(r.Context(), s, w)
	if !ok {
		return
	}
	defer release()
	target := s.address()
	if s.bytes.Load() > 0 {
		res, err := h.resolve(ctx, s.sourceID, s.refKind, s.ref)
		if err != nil {
			h.resolveFailed(w, r, "channel", s.channel, s.sourceID, s.deviceID, err)
			return
		}
		s.readdress.Store(&res.Address)
		target = res.Address
	}
	h.proxyIn(ctx, w, s, target, true)
}
