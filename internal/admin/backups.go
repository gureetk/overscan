// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"errors"
	"net/http"
	"slices"
	"strconv"

	"overscan.gureet.ca/internal/store"
)

// A lineup channel's backups (D109), on its panel and in the API: other
// channels whose streams the hub tries in turn when its own cannot start.

// backupError turns the store's errors about backups into mistakes that the
// request can put right.
func backupError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such channel in the lineup.")
	case errors.Is(err, store.ErrTooManyBackups):
		return badEdit(http.StatusConflict, "too_many_backups", "A channel has at most %d backups.", store.MaxBackups)
	case errors.Is(err, store.ErrBadBackup):
		return badEdit(http.StatusBadRequest, "bad_backup", "A channel cannot be its own backup, or a backup twice.")
	}
	return err
}

// backupsAction serves a channel's panel's forms about its backups: one
// added by its number or name, and one removed, or moved up or down.
func (a *Admin) backupsAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	lv := parseLineupView(backValues(r))
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		a.notFound(w, r)
		return
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		a.backupsDone(w, r, s, lv, id, err, "")
		return
	}
	list, err := a.store.EntryBackups(ctx, lineup, id)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	if err != nil {
		a.backupsDone(w, r, s, lv, id, err, "")
		return
	}
	ids := make([]int64, len(list))
	for i, b := range list {
		ids[i] = b.EntryID
	}
	which, _ := strconv.ParseInt(r.PostFormValue("backup"), 10, 64)
	at := slices.Index(ids, which)
	var done string
	switch op := r.PostFormValue("op"); {
	case op == "add":
		found, err := a.store.FindEntry(ctx, lineup, r.PostFormValue("backup"))
		switch {
		case errors.Is(err, store.ErrNotFound):
			err = badEdit(http.StatusBadRequest, "no_such_channel", "No channel of this lineup has that number or name.")
		case errors.Is(err, store.ErrSeveral):
			err = badEdit(http.StatusBadRequest, "several_channels", "Several channels have that name: give its number instead.")
		}
		if err != nil {
			a.backupsDone(w, r, s, lv, id, err, "")
			return
		}
		ids, done = append(ids, found), "Added the backup."
	case at < 0:
		a.backupsDone(w, r, s, lv, id, badEdit(http.StatusNotFound, "not_found", "That channel is not a backup of this one."), "")
		return
	case op == "remove":
		ids, done = slices.Delete(ids, at, at+1), "Removed the backup."
	case op == "up" && at > 0:
		ids[at-1], ids[at] = ids[at], ids[at-1]
		done = "Moved the backup up."
	case op == "down" && at < len(ids)-1:
		ids[at+1], ids[at] = ids[at], ids[at+1]
		done = "Moved the backup down."
	default:
		a.notFound(w, r)
		return
	}
	err = backupError(a.store.SetEntryBackups(ctx, lineup, id, ids))
	if err == nil {
		a.log.Info("a channel's backups set in the admin", "lineup", a.lineupName(ctx, lineup), "entry", id, "backups", len(ids))
	}
	a.backupsDone(w, r, s, lv, id, err, done)
}

// backupsDone finishes a change of a channel's backups, as lineupDone does,
// with the channel's panel still open.
func (a *Admin) backupsDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, lv lineupView, id int64, err error, notice string) {
	switch {
	case err != nil:
		a.lineupDoneAt(w, r, s, lv, err, "", id)
	case r.Header.Get("HX-Request") == "true":
		a.showLineup(w, r, s, http.StatusOK, lv, id, view{Notice: notice})
	default:
		http.Redirect(w, r, string(lv.EditURL(id)), http.StatusSeeOther)
	}
}

// apiBackup is a backup as the API shows it.
type apiBackup struct {
	EntryID   int64  `json:"entry_id"`
	ChannelID int64  `json:"channel_id"`
	Name      string `json:"name"`
	Source    string `json:"source"`
	Number    *int   `json:"number"`
	Missing   bool   `json:"missing"`
}

// writeBackups answers with a channel's backups.
func (a *Admin) writeBackups(w http.ResponseWriter, r *http.Request, lineup, id int64) {
	list, err := a.store.EntryBackups(r.Context(), lineup, id)
	if a.editAnswer(w, backupError(err)) {
		return
	}
	out := make([]apiBackup, len(list))
	for i, b := range list {
		out[i] = apiBackup{EntryID: b.EntryID, ChannelID: b.ChannelID, Name: b.Name, Source: b.Source, Missing: b.Missing}
		if b.Number > 0 {
			out[i].Number = &list[i].Number
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Backups []apiBackup `json:"backups"`
	}{out})
}

func (a *Admin) apiBackups(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	lineup, err := a.lineupID(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	a.writeBackups(w, r, lineup, id)
}

func (a *Admin) apiSetBackups(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		Entries []int64 `json:"entries"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	lineup, err := a.lineupID(ctx)
	if a.editAnswer(w, err) {
		return
	}
	if a.editAnswer(w, backupError(a.store.SetEntryBackups(ctx, lineup, id, body.Entries))) {
		return
	}
	a.log.Info("a channel's backups set in the admin", "lineup", a.lineupName(ctx, lineup), "entry", id, "backups", len(body.Entries))
	a.writeBackups(w, r, lineup, id)
}
