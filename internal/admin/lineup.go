// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"overscan.gureet.ca/internal/store"
)

// The lineup (D47): its groups, which the administrator renames, hides,
// merges and numbers, and its channels, which they rename, regroup, hide or
// show, and number. The admin edits the household account's lineup.

// Limits on what the administrator types.
const (
	maxGroupName   = 100 // characters
	maxChannelName = 200 // characters
	maxPageEntries = 1000
	pageEntries    = 100
)

// editError is a mistake in a request to change the lineup, which its sender
// can put right.
type editError struct {
	status int
	code   string
	text   string
}

func (e *editError) Error() string { return e.text }

func badEdit(status int, code, format string, args ...any) error {
	return &editError{status, code, fmt.Sprintf(format, args...)}
}

// lineupID returns the household account's lineup.
func (a *Admin) lineupID(ctx context.Context) (int64, error) {
	acct, err := a.store.Account(ctx, householdAccount)
	if err != nil {
		return 0, err
	}
	return acct.LineupID, nil
}

// checkName returns the name, trimmed, or why it cannot be one.
func checkName(name, what string, limit int) (string, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", badEdit(http.StatusBadRequest, "bad_name", "A %s needs a name.", what)
	case !utf8.ValidString(name):
		return "", badEdit(http.StatusBadRequest, "bad_name", "A %s's name must be text.", what)
	case utf8.RuneCountInString(name) > limit:
		return "", badEdit(http.StatusBadRequest, "bad_name", "A %s's name can have at most %d characters.", what, limit)
	case strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }):
		return "", badEdit(http.StatusBadRequest, "bad_name", "A %s's name cannot contain control characters.", what)
	}
	return name, nil
}

// lineupStoreError turns the store's errors about the lineup into mistakes
// that the request can put right.
func lineupStoreError(err error, what string) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such %s.", what)
	case errors.Is(err, store.ErrGroupExists):
		return badEdit(http.StatusConflict, "name_taken", "Another group has that name; to put their channels together, merge the groups.")
	case errors.Is(err, store.ErrGroupNotEmpty):
		return badEdit(http.StatusConflict, "group_not_empty", "Only an empty group can be deleted; merge it into another instead.")
	case errors.Is(err, store.ErrBadNumber):
		return badEdit(http.StatusBadRequest, "bad_number", "A number runs from 1 to %d.", store.MaxNumber)
	}
	return err
}

// Groups.

func (a *Admin) createGroup(ctx context.Context, name string) (int64, error) {
	name, err := checkName(name, "group", maxGroupName)
	if err != nil {
		return 0, err
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return 0, err
	}
	id, err := a.store.CreateLineupGroup(ctx, lineup, name)
	if err != nil {
		return 0, lineupStoreError(err, "group")
	}
	a.log.Info("lineup group made", "group", id)
	return id, nil
}

func (a *Admin) renameGroup(ctx context.Context, id int64, name string) error {
	name, err := checkName(name, "group", maxGroupName)
	if err != nil {
		return err
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	return lineupStoreError(a.store.RenameLineupGroup(ctx, lineup, id, name), "group")
}

func (a *Admin) hideGroup(ctx context.Context, id int64, hidden bool) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	return lineupStoreError(a.store.SetLineupGroupHidden(ctx, lineup, id, hidden), "group")
}

func (a *Admin) lockGroup(ctx context.Context, id int64, locked bool) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	return lineupStoreError(a.store.SetLineupGroupLocked(ctx, lineup, id, locked), "group")
}

func (a *Admin) mergeGroup(ctx context.Context, from, into int64) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	if err := a.store.MergeLineupGroup(ctx, lineup, from, into); err != nil {
		return lineupStoreError(err, "group")
	}
	a.log.Info("lineup groups merged", "group", from, "into", into)
	return nil
}

func (a *Admin) deleteGroup(ctx context.Context, id int64) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	return lineupStoreError(a.store.DeleteLineupGroup(ctx, lineup, id), "group")
}

func (a *Admin) numberGroup(ctx context.Context, id int64, from int) (int, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return 0, err
	}
	n, err := a.store.NumberGroupFrom(ctx, lineup, id, from)
	if err != nil {
		return 0, lineupStoreError(err, "group")
	}
	a.log.Info("lineup group numbered", "group", id, "from", from, "channels", n)
	return n, nil
}

func (a *Admin) moveCategory(ctx context.Context, category, group int64) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	return lineupStoreError(a.store.MoveCategory(ctx, lineup, category, group), "category or group")
}

// Channels.

// entryChange is a change to one or more lineup entries. A nil field changes
// nothing.
type entryChange struct {
	Name       *string // "" gives the channel its source's name again
	Group      *int64  // 0 puts it back in its category's group
	Visibility *store.Visibility
	Number     *int  // 0 returns it to automatic numbering
	Locked     *bool // needs the parental PIN (D55)
	// Repair is its channel's own repair switch (D57), when RepairSet:
	// repaired or not, or, nil, as the default (D58).
	Repair    *bool
	RepairSet bool
}

// changeEntries applies a change to entries, checking it all before changing
// anything. Name and number apply to one entry only.
func (a *Admin) changeEntries(ctx context.Context, ids []int64, ch entryChange) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return badEdit(http.StatusBadRequest, "no_channels", "Choose at least one channel.")
	}
	if len(ids) > 1 && (ch.Name != nil || ch.Number != nil) {
		return badEdit(http.StatusBadRequest, "one_at_a_time", "Names and numbers are set one channel at a time.")
	}
	if ch.Name != nil && *ch.Name != "" {
		name, err := checkName(*ch.Name, "channel", maxChannelName)
		if err != nil {
			return err
		}
		ch.Name = &name
	}
	if ch.Number != nil && (*ch.Number < 0 || *ch.Number > store.MaxNumber) {
		return lineupStoreError(store.ErrBadNumber, "")
	}
	for _, id := range ids {
		if _, err := a.store.LineupEntry(ctx, lineup, id); err != nil {
			return lineupStoreError(err, "channel")
		}
	}
	if ch.Name != nil {
		if err := a.store.RenameEntry(ctx, lineup, ids[0], *ch.Name); err != nil {
			return lineupStoreError(err, "channel")
		}
	}
	if ch.Group != nil {
		if err := a.store.SetEntriesGroup(ctx, lineup, ids, *ch.Group); err != nil {
			return lineupStoreError(err, "group")
		}
	}
	if ch.Visibility != nil {
		if err := a.store.SetEntriesVisibility(ctx, lineup, ids, *ch.Visibility); err != nil {
			return lineupStoreError(err, "channel")
		}
	}
	if ch.Locked != nil {
		if err := a.store.SetEntriesLocked(ctx, lineup, ids, *ch.Locked); err != nil {
			return lineupStoreError(err, "channel")
		}
	}
	if ch.RepairSet {
		if err := a.store.SetEntriesRepair(ctx, lineup, ids, ch.Repair); err != nil {
			return lineupStoreError(err, "channel")
		}
	}
	if ch.Number != nil {
		if *ch.Number == 0 {
			err = a.store.ResetEntryNumber(ctx, lineup, ids[0])
		} else {
			err = a.store.PinEntryNumber(ctx, lineup, ids[0], *ch.Number)
		}
		if err != nil {
			return lineupStoreError(err, "channel")
		}
	}
	return nil
}

// visibilityNames are the visibilities as the API and the pages name them.
var visibilityNames = map[store.Visibility]string{store.AsGroup: "group", store.Shown: "shown", store.Hidden: "hidden"}

func parseVisibility(s string) (store.Visibility, error) {
	for v, name := range visibilityNames {
		if name == s {
			return v, nil
		}
	}
	return 0, badEdit(http.StatusBadRequest, "bad_visibility", `Visibility is "group", "shown" or "hidden".`)
}

// The API.

type apiGroupCategory struct {
	ID       *int64 `json:"id"`
	SourceID int64  `json:"source_id"`
	Source   string `json:"source"`
	Name     string `json:"name"`
}

type apiGroup struct {
	ID          int64              `json:"id"`
	Name        string             `json:"name"`
	Hidden      bool               `json:"hidden"`
	Locked      bool               `json:"locked"`
	Edited      bool               `json:"edited"`
	Channels    int                `json:"channels"`
	Shown       int                `json:"shown"`
	FirstNumber *int               `json:"first_number"`
	Categories  []apiGroupCategory `json:"categories"`
}

func groupBody(g store.LineupGroup) apiGroup {
	out := apiGroup{ID: g.ID, Name: g.Name, Hidden: g.Hidden, Locked: g.Locked, Edited: g.Edited, Channels: g.Channels,
		Shown: g.Shown, Categories: []apiGroupCategory{}}
	if g.FirstNumber > 0 {
		out.FirstNumber = &g.FirstNumber
	}
	for _, c := range g.Categories {
		ac := apiGroupCategory{SourceID: c.SourceID, Source: c.Source, Name: c.Name}
		if c.ID != 0 {
			ac.ID = &c.ID
		}
		out.Categories = append(out.Categories, ac)
	}
	return out
}

type apiEntry struct {
	ID          int64   `json:"id"`
	ChannelID   int64   `json:"channel_id"`
	SourceID    int64   `json:"source_id"`
	Source      string  `json:"source"`
	Number      *int    `json:"number"`
	Pinned      bool    `json:"pinned"`
	Name        string  `json:"name"`
	ChannelName string  `json:"channel_name"`
	Renamed     bool    `json:"renamed"`
	GroupID     *int64  `json:"group_id"`
	Group       *string `json:"group"`
	Moved       bool    `json:"moved"`
	Category    *string `json:"category"`
	Visibility  string  `json:"visibility"`
	Hidden      bool    `json:"hidden"`
	Missing     bool    `json:"missing"`
	Locked      bool    `json:"locked"`
	GroupLocked bool    `json:"group_locked"`
	Repair      bool    `json:"repair"`
	RepairOwn   *bool   `json:"repair_own"`
	Adult       bool    `json:"adult"` // its source marks it for adults (D86)
}

func entryBody(e store.LineupEntry) apiEntry {
	out := apiEntry{ID: e.ID, ChannelID: e.ChannelID, SourceID: e.SourceID, Source: e.Source, Pinned: e.Pinned,
		Name: e.Name, ChannelName: e.ChannelName, Renamed: e.Renamed, Moved: e.Moved,
		Visibility: visibilityNames[e.Visibility], Hidden: e.Hidden, Missing: e.Missing, Locked: e.Locked,
		GroupLocked: e.GroupLocked, Repair: e.Repair, RepairOwn: e.RepairOwn, Adult: e.Adult}
	if e.Number > 0 {
		out.Number = &e.Number
	}
	if e.GroupID != 0 {
		out.GroupID, out.Group = &e.GroupID, &e.Group
	}
	if e.Category != "" {
		out.Category = &e.Category
	}
	return out
}

// editAnswer answers with what went wrong, if anything, and reports whether
// it did.
func (a *Admin) editAnswer(w http.ResponseWriter, err error) bool {
	var bad *editError
	switch {
	case err == nil:
		return false
	case errors.As(err, &bad):
		apiError(w, bad.status, bad.code, bad.text)
	default:
		a.apiInternal(w, "cannot change the lineup", err)
	}
	return true
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 0 {
		return 0, badEdit(http.StatusNotFound, "not_found", "There is no such item.")
	}
	return id, nil
}

func (a *Admin) apiGroups(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	lineup, err := a.lineupID(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	gs, err := a.store.LineupGroups(r.Context(), lineup)
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiGroup, len(gs))
	for i, g := range gs {
		out[i] = groupBody(g)
	}
	writeJSON(w, http.StatusOK, struct {
		Groups []apiGroup `json:"groups"`
	}{out})
}

// writeGroup answers with the group with the given ID.
func (a *Admin) writeGroup(w http.ResponseWriter, r *http.Request, status int, id int64) {
	lineup, err := a.lineupID(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	gs, err := a.store.LineupGroups(r.Context(), lineup)
	if a.editAnswer(w, err) {
		return
	}
	for _, g := range gs {
		if g.ID == id {
			writeJSON(w, status, groupBody(g))
			return
		}
	}
	a.editAnswer(w, badEdit(http.StatusNotFound, "not_found", "There is no such group."))
}

func (a *Admin) apiCreateGroup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id, err := a.createGroup(r.Context(), body.Name)
	if a.editAnswer(w, err) {
		return
	}
	a.writeGroup(w, r, http.StatusCreated, id)
}

func (a *Admin) apiChangeGroup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		Name   *string `json:"name"`
		Hidden *bool   `json:"hidden"`
		Locked *bool   `json:"locked"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name == nil && body.Hidden == nil && body.Locked == nil {
		apiError(w, http.StatusBadRequest, "bad_json", "Say what to change: name, hidden or locked.")
		return
	}
	if body.Name != nil {
		if a.editAnswer(w, a.renameGroup(r.Context(), id, *body.Name)) {
			return
		}
	}
	if body.Hidden != nil {
		if a.editAnswer(w, a.hideGroup(r.Context(), id, *body.Hidden)) {
			return
		}
	}
	if body.Locked != nil {
		if a.editAnswer(w, a.lockGroup(r.Context(), id, *body.Locked)) {
			return
		}
	}
	a.writeGroup(w, r, http.StatusOK, id)
}

func (a *Admin) apiMergeGroup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		Into int64 `json:"into"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if a.editAnswer(w, a.mergeGroup(r.Context(), id, body.Into)) {
		return
	}
	a.writeGroup(w, r, http.StatusOK, body.Into)
}

func (a *Admin) apiNumberGroup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		From int `json:"from"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	n, err := a.numberGroup(r.Context(), id, body.From)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Numbered int `json:"numbered"`
	}{n})
}

func (a *Admin) apiDeleteGroup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) || a.editAnswer(w, a.deleteGroup(r.Context(), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *Admin) apiMoveCategory(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		GroupID int64 `json:"group_id"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if a.editAnswer(w, a.moveCategory(r.Context(), id, body.GroupID)) {
		return
	}
	a.writeGroup(w, r, http.StatusOK, body.GroupID)
}

func (a *Admin) apiEntries(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	lineup, err := a.lineupID(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	q := store.EntryQuery{Text: r.URL.Query().Get("q"), Limit: pageEntries}
	if v := r.URL.Query().Get("group"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 0 {
			apiError(w, http.StatusBadRequest, "bad_query", "group is a group's ID, or 0 for the channels in none.")
			return
		}
		q.Group = &id
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			apiError(w, http.StatusBadRequest, "bad_query", "offset is 0 or more.")
			return
		}
		q.Offset = n
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageEntries {
			apiError(w, http.StatusBadRequest, "bad_query", fmt.Sprintf("limit runs from 1 to %d.", maxPageEntries))
			return
		}
		q.Limit = n
	}
	es, total, err := a.store.LineupEntries(r.Context(), lineup, q)
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiEntry, len(es))
	for i, e := range es {
		out[i] = entryBody(e)
	}
	writeJSON(w, http.StatusOK, struct {
		Total    int        `json:"total"`
		Channels []apiEntry `json:"channels"`
	}{total, out})
}

// readEntryChange decodes a change to lineup entries, in which null resets a
// field to what Overscan would choose, and an absent field stays as it is.
func readEntryChange(w http.ResponseWriter, r *http.Request, withIDs bool) ([]int64, entryChange, bool) {
	var raw map[string]json.RawMessage
	if !readJSON(w, r, &raw) {
		return nil, entryChange{}, false
	}
	var ids []int64
	var ch entryChange
	bad := func(text string) ([]int64, entryChange, bool) {
		apiError(w, http.StatusBadRequest, "bad_json", text)
		return nil, entryChange{}, false
	}
	isNull := func(v json.RawMessage) bool { return string(v) == "null" }
	for k, v := range raw {
		switch {
		case k == "ids" && withIDs:
			if err := json.Unmarshal(v, &ids); err != nil {
				return bad("ids is a list of channel IDs.")
			}
		case k == "name" && !withIDs:
			name := ""
			if !isNull(v) && json.Unmarshal(v, &name) != nil {
				return bad("name is a string, or null for the source's name.")
			}
			ch.Name = &name
		case k == "group_id":
			var id int64
			if !isNull(v) && (json.Unmarshal(v, &id) != nil || id <= 0) {
				return bad("group_id is a group's ID, or null for the category's group.")
			}
			ch.Group = &id
		case k == "visibility":
			var s string
			if json.Unmarshal(v, &s) != nil {
				return bad(`visibility is "group", "shown" or "hidden".`)
			}
			vis, err := parseVisibility(s)
			if err != nil {
				return bad(`visibility is "group", "shown" or "hidden".`)
			}
			ch.Visibility = &vis
		case k == "locked":
			var locked bool
			if json.Unmarshal(v, &locked) != nil {
				return bad("locked is true or false.")
			}
			ch.Locked = &locked
		case k == "repair":
			var repair *bool
			if json.Unmarshal(v, &repair) != nil {
				return bad("repair is true, false, or null for the default.")
			}
			ch.Repair, ch.RepairSet = repair, true
		case k == "number" && !withIDs:
			n := 0
			if !isNull(v) && (json.Unmarshal(v, &n) != nil || n <= 0) {
				return bad("number is a channel number, or null for automatic numbering.")
			}
			ch.Number = &n
		default:
			return bad(fmt.Sprintf("%q is not something to change here.", k))
		}
	}
	if ch == (entryChange{}) {
		return bad("Say what to change.")
	}
	return ids, ch, true
}

func (a *Admin) apiChangeEntry(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	_, ch, ok := readEntryChange(w, r, false)
	if !ok || a.editAnswer(w, a.changeEntries(r.Context(), []int64{id}, ch)) {
		return
	}
	lineup, err := a.lineupID(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	e, err := a.store.LineupEntry(r.Context(), lineup, id)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, entryBody(e))
}

func (a *Admin) apiChangeEntries(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ids, ch, ok := readEntryChange(w, r, true)
	if !ok || a.editAnswer(w, a.changeEntries(r.Context(), ids, ch)) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Changed int `json:"changed"`
	}{len(ids)})
}
