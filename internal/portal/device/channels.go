// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/lineup"
	"overscan.gureet.ca/internal/store"
)

// group is a group of channels.
type group struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// channel is a channel in the device's lineup. Its ID is the lineup entry's,
// which stays the same across refreshes; Group is 0 for a channel in no
// group. Logo is the path on Overscan of its logo, if it has one (D54).
// Locked is set when playing it needs the account's parental PIN (D55).
// Archive is how many days back its past programmes play from its source's
// archive (D81), 0 or left out for none.
type channel struct {
	ID      int64  `json:"id"`
	Number  int    `json:"number"`
	Name    string `json:"name"`
	Group   int64  `json:"group"`
	Logo    string `json:"logo,omitempty"`
	Locked  bool   `json:"locked,omitempty"`
	Archive int    `json:"archive,omitempty"`
}

// channelsResponse is the device's whole lineup, in one answer, with the
// account's favourites and what the device watched last (D53). Version
// changes when the lineup does, so the page can tell whether to load it again.
type channelsResponse struct {
	Version    string    `json:"version"`
	Groups     []group   `json:"groups"`
	Channels   []channel `json:"channels"`
	Favourites []int64   `json:"favourites"`
	Recent     []int64   `json:"recent"`
}

// lineupOf returns the device's lineup as the API shows it, with its version.
func (a *API) lineupOf(ctx context.Context, d store.Device) (channelsResponse, store.Account, error) {
	acct, err := a.store.Account(ctx, d.AccountID)
	if err != nil {
		return channelsResponse{}, store.Account{}, err
	}
	entries, err := a.store.LineupChannels(ctx, acct.LineupID)
	if err != nil {
		return channelsResponse{}, store.Account{}, err
	}
	groups := lineup.Groups(entries)
	resp := channelsResponse{Groups: make([]group, len(groups)), Channels: make([]channel, len(entries))}
	for i, g := range groups {
		resp.Groups[i] = group{ID: g.ID, Name: g.Name}
	}
	for i, c := range entries {
		resp.Channels[i] = channel{ID: c.EntryID, Number: c.Number, Name: c.Name, Group: c.GroupID,
			Logo: artwork.LogoPath(c.EntryID, c.Logo), Locked: c.Locked && acct.PINHash != "", Archive: c.ArchiveDays}
	}
	sum := sha256.New()
	_ = json.NewEncoder(sum).Encode([]any{resp.Groups, resp.Channels})
	resp.Version = hex.EncodeToString(sum.Sum(nil)[:8])
	return resp, acct, nil
}

// channels answers GET channels: every channel in the device's lineup, by
// number, and the groups they are in, in the order of their first channel;
// the account's favourites in the order they were marked; and the channels
// the device watched, the latest first. Both lists hold only channels that
// the lineup shows.
func (a *API) channels(w http.ResponseWriter, r *http.Request, d store.Device) {
	ctx := r.Context()
	resp, acct, err := a.lineupOf(ctx, d)
	if err != nil {
		a.internalError(w, "list channels", err)
		return
	}
	shown := make(map[int64]bool, len(resp.Channels))
	for _, c := range resp.Channels {
		shown[c.ID] = true
	}
	keep := func(ids []int64) []int64 {
		out := []int64{}
		for _, id := range ids {
			if shown[id] {
				out = append(out, id)
			}
		}
		return out
	}
	favourites, err := a.store.Favourites(ctx, acct.ID)
	if err != nil {
		a.internalError(w, "list favourites", err)
		return
	}
	watched, err := a.store.Watched(ctx, d.ID)
	if err != nil {
		a.internalError(w, "list what the device watched", err)
		return
	}
	resp.Favourites, resp.Recent = keep(favourites), keep(watched)
	writeJSON(w, http.StatusOK, resp)
}

// channelsVersion answers GET channels/version with the lineup's version
// alone, which the page asks for now and then to notice a changed lineup.
func (a *API) channelsVersion(w http.ResponseWriter, r *http.Request, d store.Device) {
	resp, _, err := a.lineupOf(r.Context(), d)
	if err != nil {
		a.internalError(w, "list channels", err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Version string `json:"version"`
	}{resp.Version})
}

// playRequest is the body of a play call, which may be empty: the parental
// PIN, for a locked channel (D55).
type playRequest struct {
	PIN string `json:"pin"`
}

// playResponse says how the box plays a channel: gSTB.Play(solution + " " +
// url).
type playResponse struct {
	URL      string `json:"url"`
	Solution string `json:"solution"`
}

// play answers POST channels/{id}/play with a link that plays the channel
// through the hub. A locked channel needs the account's PIN in the body.
func (a *API) play(w http.ResponseWriter, r *http.Request, d store.Device) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var req playRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	entry, err := a.store.PlayableEntry(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up channel", err)
		return
	}
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if entry.LineupID != acct.LineupID {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if entry.Locked && acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
		return
	}
	if !a.hub.Available(entry.SourceID, d.ID) {
		writeError(w, http.StatusServiceUnavailable, errLimit)
		return
	}
	link := a.hub.Link(d.ID, entry)
	if err := a.store.RecordWatch(r.Context(), d.ID, entry.EntryID, a.now()); err != nil {
		a.log.Warn("cannot record what a device watched", "mac", d.MAC, "error", err)
	}
	writeJSON(w, http.StatusOK, playResponse{URL: "http://" + r.Host + link, Solution: solution(link)})
}

// pinRequest is the body of a PIN call.
type pinRequest struct {
	PIN string `json:"pin"`
}

// checkPIN answers POST pin, which checks the account's parental PIN before
// the box shows what is locked (D85): {"ok": true} when it is right, or when
// the account has none; otherwise as a locked channel's play call does,
// wrong PINs counting alike.
func (a *API) checkPIN(w http.ResponseWriter, r *http.Request, d store.Device) {
	var req pinRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		OK bool `json:"ok"`
	}{true})
}

// pinOK checks the PIN that a box gives for something locked, and answers
// the call itself when the box may not play it.
func (a *API) pinOK(w http.ResponseWriter, r *http.Request, d store.Device, acct store.Account, pin string) bool {
	if wait := a.pins.wait(d.ID, a.now()); wait > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, errPINLocked)
		return false
	}
	if pin == "" {
		writeError(w, http.StatusForbidden, errPINRequired)
		return false
	}
	ok, err := admin.CheckPassword(r.Context(), acct.PINHash, pin)
	if err != nil {
		a.internalError(w, "check the PIN", err)
		return false
	}
	if !ok {
		a.pins.fail(d.ID, a.now())
		a.log.Warn("wrong parental PIN", "mac", d.MAC, "label", d.Label)
		writeError(w, http.StatusForbidden, errPINWrong)
		return false
	}
	a.pins.reset(d.ID)
	return true
}

// archiveRequest is the body of an archive call: when the programme began,
// in Unix seconds, and the parental PIN, for a locked channel (D55).
type archiveRequest struct {
	Start int64  `json:"start"`
	PIN   string `json:"pin"`
}

// archiveResponse says how the box plays a past programme (D81): its
// stream; how the page seeks in it, which Seek says as it does for a film:
// "restart", by asking for it from a time, t seconds into the programme,
// after asking Start where the stream from that time starts, or "player",
// for a Stalker portal's past programme, which the player seeks in itself
// (D84); the programme's length, in seconds; and the programme.
type archiveResponse struct {
	URL       string    `json:"url"`
	Solution  string    `json:"solution"`
	Seek      string    `json:"seek"`
	Start     string    `json:"start,omitempty"`
	Duration  int64     `json:"duration"`
	Programme programme `json:"programme"`
}

// playArchive answers POST channels/{id}/archive with a link that plays a
// past programme of the channel from its source's archive (D81): the one
// that began at the time given, if it has ended and the archive reaches
// back to it. A locked channel needs the account's PIN in the body.
func (a *API) playArchive(w http.ResponseWriter, r *http.Request, d store.Device) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var req archiveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Start <= 0 || req.Start > 1<<40 {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	ctx := r.Context()
	entry, err := a.store.PlayableArchive(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, errNotFound)
		return
	case errors.Is(err, store.ErrNoArchive):
		writeError(w, http.StatusNotFound, errNoArchive)
		return
	case err != nil:
		a.internalError(w, "look up a channel's archive", err)
		return
	}
	acct, err := a.store.Account(ctx, d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if entry.LineupID != acct.LineupID {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	p, err := a.store.ProgrammeAt(ctx, acct.LineupID, id, time.Unix(req.Start, 0))
	if errors.Is(err, store.ErrNotFound) || err == nil && !store.InArchive(p, entry.Days, a.now()) {
		writeError(w, http.StatusNotFound, errNoArchive)
		return
	}
	if err != nil {
		a.internalError(w, "look up a programme", err)
		return
	}
	if entry.Locked && acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
		return
	}
	if !a.hub.Available(entry.SourceID, d.ID) {
		writeError(w, http.StatusServiceUnavailable, errLimit)
		return
	}
	link, start := a.hub.ArchiveLink(d.ID, entry.PlayableEntry, p.Start, p.Stop)
	answer := archiveResponse{URL: "http://" + r.Host + link, Solution: "ffrt", Seek: "restart",
		Start: "http://" + r.Host + start, Duration: int64(p.Stop.Sub(p.Start) / time.Second), Programme: fullProgramme(p)}
	if entry.Portal != "" {
		answer.Solution, answer.Seek, answer.Start = fileSolution, "player", ""
	}
	writeJSON(w, http.StatusOK, answer)
}

// solution returns the box player's solution for a hub link: ffrt, for
// MPEG-TS streamed live over HTTP, unless the link is HLS, which goes to
// ffmpeg.
func solution(link string) string {
	if strings.HasSuffix(link, ".m3u8") {
		return "ffmpeg"
	}
	return "ffrt"
}
