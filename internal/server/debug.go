// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/store"
)

// debugDevice is the device ID that the hub sees for the debug playlist's
// streams: no real device has it.
const debugDevice = 0

// householdAccount is the account whose lineup the debug playlist shows.
const householdAccount = 1

// handleDebugLineup serves the household lineup as an M3U playlist, for
// testing the stream hub in a player such as VLC before the MAG page exists
// (plan, section 14). It is for the administrator only: the admin token must
// be in the token parameter, and every address in it carries the token too.
// It is not the per-account playlist output that the hub stage brings.
func (s *Server) handleDebugLineup(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	token := r.URL.Query().Get("token")
	if !admin.Valid(s.services.AdminToken, token) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	acct, err := s.services.Store.Account(r.Context(), householdAccount)
	if err != nil {
		s.debugError(w, "look up the household account", err)
		return
	}
	channels, err := s.services.Store.LineupChannels(r.Context(), acct.LineupID)
	if err != nil {
		s.debugError(w, "list the lineup", err)
		return
	}

	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	q := url.Values{"token": {token}}.Encode()
	for _, c := range channels {
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"%s\" tvg-chno=\"%d\" group-title=\"%s\",%s\n",
			attr(c.XMLTVID), c.Number, attr(c.Group), c.Name)
		fmt.Fprintf(&b, "http://%s/debug/play/%d?%s\n", r.Host, c.EntryID, q)
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="overscan-debug.m3u"`)
	_, _ = w.Write([]byte(b.String()))
}

// handleDebugPlay sends a player from the debug playlist to a new hub link
// for the entry, so the playlist's addresses never expire.
func (s *Server) handleDebugPlay(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !admin.Valid(s.services.AdminToken, r.URL.Query().Get("token")) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("entry"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	entry, err := s.services.Store.PlayableEntry(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.debugError(w, "look up the channel", err)
		return
	}
	acct, err := s.services.Store.Account(r.Context(), householdAccount)
	if err != nil {
		s.debugError(w, "look up the household account", err)
		return
	}
	if entry.LineupID != acct.LineupID {
		http.NotFound(w, r)
		return
	}
	if !s.services.Hub.Available(entry.SourceID, debugDevice) {
		http.Error(w, "the source has no free connection", http.StatusServiceUnavailable)
		return
	}
	http.Redirect(w, r, s.services.Hub.Link(debugDevice, entry), http.StatusFound)
}

func (s *Server) debugError(w http.ResponseWriter, what string, err error) {
	s.log.Error("debug playlist failed", "while", what, "error", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// attr makes s safe inside a double-quoted M3U attribute.
func attr(s string) string {
	return strings.ReplaceAll(s, `"`, "'")
}
