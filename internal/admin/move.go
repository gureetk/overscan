// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/store"
)

// Moving a source's channels to other sources (D144): the page lists which
// of the other sources' channels stands for each of the source's, matched
// by guide ID and then by name as guides are (D139), for the
// administrator to confirm; nothing moves until they do, and the old
// source stays until they remove it.

// maxMoveFormBytes bounds the form, or the API's body, that confirms a
// move: a pair of channel IDs for each of 40,000 channels.
const maxMoveFormBytes = 2 << 20

// moveRow is a channel that moves, and where to.
type moveRow struct {
	From     store.MovableChannel
	To       store.MovableChannel
	ToSource string
	ByName   bool // matched by name, not by guide ID
}

// Value is what the confirming form sends for the row.
func (r moveRow) Value() string { return fmt.Sprintf("%d:%d", r.From.ID, r.To.ID) }

// moveMiss is a channel that finds no match.
type moveMiss struct {
	Channel store.MovableChannel
	// Taken names the channel that the same match went to, "" if it
	// found none.
	Taken string
}

// movePlan is what moving a source's channels to others would do.
type movePlan struct {
	From      store.Source
	Targets   []store.Source // the sources moved to, in the order that settles ties
	Others    []store.Source // the sources that can be moved to
	Rows      []moveRow
	Unmatched []moveMiss
	Moved     int // the source's channels that moved before
}

// ByID counts the matches by guide ID.
func (p movePlan) ByID() int {
	n := 0
	for _, r := range p.Rows {
		if !r.ByName {
			n++
		}
	}
	return n
}

// Chosen reports whether a source is among the targets.
func (p movePlan) Chosen(id int64) bool {
	return slices.ContainsFunc(p.Targets, func(s store.Source) bool { return s.ID == id })
}

// TargetNames names the targets, for the page.
func (p movePlan) TargetNames() string {
	names := make([]string, len(p.Targets))
	for i, s := range p.Targets {
		names[i] = s.Name
	}
	return andList(names)
}

// livePlaylists reports whether a source has channels to move or to move
// to: one that is not only films and series.
func livePlaylists(s store.Source) bool { return s.Settings().HasChannels() }

// planMove works out where a source's channels would move among the
// targets, which must be other enabled sources of channels.
func (a *Admin) planMove(ctx context.Context, from store.Source, targets []int64) (movePlan, error) {
	plan := movePlan{From: from}
	srcs, err := a.store.Sources(ctx)
	if err != nil {
		return plan, err
	}
	byID := map[int64]store.Source{}
	for _, s := range srcs {
		if s.ID != from.ID && s.Enabled && livePlaylists(s) {
			plan.Others = append(plan.Others, s)
			byID[s.ID] = s
		}
	}
	if plan.Moved, err = a.store.MovedChannels(ctx, from.ID); err != nil {
		return plan, err
	}
	for _, id := range targets {
		s, ok := byID[id]
		if !ok {
			return plan, badEdit(http.StatusBadRequest, "bad_target", "Move to other sources of channels that are on, not to this one.")
		}
		if !plan.Chosen(id) {
			plan.Targets = append(plan.Targets, s)
		}
	}
	if len(plan.Targets) == 0 {
		return plan, nil
	}
	old, err := a.store.MovableChannels(ctx, from.ID)
	if err != nil {
		return plan, err
	}
	candidates := make([]store.MatchCandidate, len(old))
	for i, c := range old {
		candidates[i] = store.MatchCandidate{ID: c.ID, XMLTVID: c.XMLTVID, Name: c.Name, Category: c.Category}
	}
	var listed []store.ListedChannel
	others := map[int64]store.MovableChannel{}
	for i, t := range plan.Targets {
		cs, err := a.store.MovableChannels(ctx, t.ID)
		if err != nil {
			return plan, err
		}
		for _, c := range cs {
			others[c.ID] = c
			listed = append(listed, store.ListedChannel{ID: c.ID, GuideID: int64(i + 1), XMLTVID: c.XMLTVID, Names: []string{c.Name}})
		}
	}
	matched := map[int64]store.GuideMatch{}
	for _, m := range guide.MatchChannels(candidates, listed) {
		matched[m.ChannelID] = m
	}
	// A channel of the other sources stands for one of this source's at
	// most: the first, in this source's order, takes it.
	takenBy := map[int64]string{}
	for _, c := range old {
		m, ok := matched[c.ID]
		if !ok {
			plan.Unmatched = append(plan.Unmatched, moveMiss{Channel: c})
			continue
		}
		if name, taken := takenBy[m.GuideChannelID]; taken {
			plan.Unmatched = append(plan.Unmatched, moveMiss{Channel: c, Taken: name})
			continue
		}
		takenBy[m.GuideChannelID] = c.Name
		to := others[m.GuideChannelID]
		plan.Rows = append(plan.Rows, moveRow{From: c, To: to, ToSource: byID[to.SourceID].Name, ByName: m.ByName})
	}
	return plan, nil
}

// moveChannels moves the channels, and says what it did in the log.
func (a *Admin) moveChannels(ctx context.Context, from store.Source, moves []store.ChannelMove) (store.MoveResult, error) {
	if len(moves) == 0 {
		return store.MoveResult{}, badEdit(http.StatusBadRequest, "nothing_to_move", "Tick the channels to move.")
	}
	old, err := a.store.MovableChannels(ctx, from.ID)
	if err != nil {
		return store.MoveResult{}, err
	}
	ours := map[int64]bool{}
	for _, c := range old {
		ours[c.ID] = true
	}
	for _, m := range moves {
		if !ours[m.From] {
			return store.MoveResult{}, badEdit(http.StatusBadRequest, "bad_move", "Only %s's channels that have not moved can move.", from.Name)
		}
	}
	res, err := a.store.MoveChannels(ctx, moves)
	if errors.Is(err, store.ErrBadMove) {
		return res, badEdit(http.StatusBadRequest, "bad_move",
			"Each channel moves to another source's channel that no other channel moves to, and that has not moved itself.")
	}
	if err != nil {
		return res, err
	}
	a.log.Info("channels moved to other sources in the admin", "source", from.Name, "channels", res.Channels,
		"entries", res.Entries, "merged", res.Merged)
	// Guides chosen by hand went with them.
	a.guidesChanged()
	return res, nil
}

// targetIDs reads the sources to move to: IDs, as several values or one
// list separated by commas.
func targetIDs(values []string) ([]int64, bool) {
	var out []int64
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part == "" {
				continue
			}
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				return nil, false
			}
			out = append(out, id)
		}
	}
	return out, true
}

// moveData is what the page that moves a source's channels shows.
type moveData struct {
	movePlan
	CSRF string
}

// ToQuery is the query that finds the plan's matches again.
func (d moveData) ToQuery() string {
	q := url.Values{}
	for _, t := range d.Targets {
		q.Add("to", strconv.FormatInt(t.ID, 10))
	}
	return q.Encode()
}

// sourceFor returns the source that the path names, or answers that there
// is none.
func (a *Admin) sourceFor(w http.ResponseWriter, r *http.Request) (store.Source, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return store.Source{}, false
	}
	src, err := a.store.Source(r.Context(), id)
	if err != nil || !livePlaylists(src) {
		a.notFound(w, r)
		return store.Source{}, false
	}
	return src, true
}

// showMove shows the page that moves a source's channels.
func (a *Admin) showMove(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, from store.Source, targets []int64, v view) {
	plan, err := a.planMove(r.Context(), from, targets)
	var bad *editError
	switch {
	case errors.As(err, &bad):
		status, v.Error = bad.status, bad.text
	case err != nil:
		a.log.Error("cannot work out a move of channels", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session = "Move "+from.Name+"'s channels · Sources", "sources", &s
	v.Data = moveData{movePlan: plan, CSRF: s.CSRFToken}
	a.render(w, r, status, "move", v)
}

// movePage shows the page that moves a source's channels: the sources to
// move them to, and, once they are chosen, the matches to confirm.
func (a *Admin) movePage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	from, ok := a.sourceFor(w, r)
	if !ok {
		return
	}
	targets, ok := targetIDs(r.URL.Query()["to"])
	if !ok {
		targets = nil
	}
	a.showMove(w, r, s, http.StatusOK, from, targets, view{})
}

// moveForm moves the ticked channels.
func (a *Admin) moveForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	from, ok := a.sourceFor(w, r)
	if !ok {
		return
	}
	// htmx sends the CSRF token as a header, so nothing has read the form
	// yet.
	if err := r.ParseForm(); err != nil {
		a.showMove(w, r, s, http.StatusRequestEntityTooLarge, from, nil, view{Error: "The form is too large to read."})
		return
	}
	var moves []store.ChannelMove
	for _, v := range r.PostForm["move"] {
		f, t, found := strings.Cut(v, ":")
		fromID, err1 := strconv.ParseInt(f, 10, 64)
		toID, err2 := strconv.ParseInt(t, 10, 64)
		if !found || err1 != nil || err2 != nil {
			continue
		}
		moves = append(moves, store.ChannelMove{From: fromID, To: toID})
	}
	res, err := a.moveChannels(r.Context(), from, moves)
	if err != nil {
		targets, _ := targetIDs(r.PostForm["to"])
		var bad *editError
		if !errors.As(err, &bad) {
			a.log.Error("cannot move channels", "error", err)
			bad = &editError{status: http.StatusInternalServerError, text: "Something went wrong; the server's log says what."}
		}
		a.showMove(w, r, s, bad.status, from, targets, view{Error: bad.text})
		return
	}
	a.sourceDone(w, r, s, nil, moveNotice(from.Name, res), newSourceForm(), false)
}

// moveNotice says what a move did.
func moveNotice(from string, res store.MoveResult) string {
	return fmt.Sprintf("Moved %s %s of %s: their entries in every lineup play from the other sources now, with their numbers, names, groups and favourites. "+
		"%s's other channels stay on it until you delete it.", count(res.Channels), plural(res.Channels, "channel"), from, from)
}

// apiMoveChannel is a channel in a plan, as the API gives it.
type apiMoveChannel struct {
	ID       int64  `json:"id"`
	SourceID int64  `json:"source_id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	XMLTVID  string `json:"xmltv_id"`
}

func moveChannelBody(c store.MovableChannel) apiMoveChannel {
	return apiMoveChannel{ID: c.ID, SourceID: c.SourceID, Name: c.Name, Category: c.Category, XMLTVID: c.XMLTVID}
}

// apiMoveMatch is a channel that would move, and where to.
type apiMoveMatch struct {
	Channel apiMoveChannel `json:"channel"`
	To      apiMoveChannel `json:"to"`
	By      string         `json:"by"` // "guide_id" or "name"
}

// apiMoveMiss is a channel that would not move.
type apiMoveMiss struct {
	Channel apiMoveChannel `json:"channel"`
	Reason  string         `json:"reason"` // "no_match", or "taken" when another channel takes its match
}

type apiMovePlan struct {
	Source    int64          `json:"source"`
	To        []int64        `json:"to"`
	Moved     int            `json:"moved"`
	Matches   []apiMoveMatch `json:"matches"`
	Unmatched []apiMoveMiss  `json:"unmatched"`
}

// apiMovePlan answers GET sources/{id}/moves?to=: where the source's
// channels would move among the sources that to names.
func (a *Admin) apiMovePlan(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	from, err := a.apiMoveSource(r)
	if a.editAnswer(w, err) {
		return
	}
	targets, ok := targetIDs(r.URL.Query()["to"])
	if !ok || len(targets) == 0 {
		apiError(w, http.StatusBadRequest, "bad_target", "to names the sources to move to, by ID, separated by commas.")
		return
	}
	plan, err := a.planMove(r.Context(), from, targets)
	if a.editAnswer(w, err) {
		return
	}
	body := apiMovePlan{Source: from.ID, Moved: plan.Moved, Matches: []apiMoveMatch{}, Unmatched: []apiMoveMiss{}}
	for _, t := range plan.Targets {
		body.To = append(body.To, t.ID)
	}
	for _, row := range plan.Rows {
		by := "guide_id"
		if row.ByName {
			by = "name"
		}
		body.Matches = append(body.Matches, apiMoveMatch{Channel: moveChannelBody(row.From), To: moveChannelBody(row.To), By: by})
	}
	for _, m := range plan.Unmatched {
		reason := "no_match"
		if m.Taken != "" {
			reason = "taken"
		}
		body.Unmatched = append(body.Unmatched, apiMoveMiss{Channel: moveChannelBody(m.Channel), Reason: reason})
	}
	writeJSON(w, http.StatusOK, body)
}

// apiMoveSource returns the source that the path names, if it has
// channels.
func (a *Admin) apiMoveSource(r *http.Request) (store.Source, error) {
	id, err := pathID(r)
	if err != nil {
		return store.Source{}, err
	}
	src, err := a.store.Source(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || err == nil && !livePlaylists(src) {
		return store.Source{}, badEdit(http.StatusNotFound, "not_found", "There is no such source of channels.")
	}
	return src, err
}

// apiMoves answers POST sources/{id}/moves: it moves the source's channels
// that the body names to the channels it names.
func (a *Admin) apiMoves(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	from, err := a.apiMoveSource(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		Moves []struct {
			Channel int64 `json:"channel"`
			To      int64 `json:"to"`
		} `json:"moves"`
	}
	if !readJSONWithin(w, r, &body, maxMoveFormBytes) {
		return
	}
	moves := make([]store.ChannelMove, len(body.Moves))
	for i, m := range body.Moves {
		moves[i] = store.ChannelMove{From: m.Channel, To: m.To}
	}
	res, err := a.moveChannels(r.Context(), from, moves)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"channels": res.Channels, "entries": res.Entries, "merged": res.Merged})
}
