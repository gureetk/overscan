// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Several lineups (D106): the Live TV page edits one lineup at a time, the
// household account's unless a request names another, and makes new ones
// as copies of the one it shows. Each account sees one, which its card on
// the Accounts page chooses.

// maxLineupName caps a lineup's name, in characters.
const maxLineupName = 50

// lineupKey keys the lineup that a request edits in its context.
type lineupKey struct{}

// inLineup returns ctx with the lineup that the admin edits.
func inLineup(ctx context.Context, id int64) context.Context {
	return context.WithValue(ctx, lineupKey{}, id)
}

// lineupID returns the lineup that the admin edits: the one that the
// request chose, or else the household account's.
func (a *Admin) lineupID(ctx context.Context) (int64, error) {
	if id, ok := ctx.Value(lineupKey{}).(int64); ok && id > 0 {
		return id, nil
	}
	acct, err := a.store.Account(ctx, householdAccount)
	if err != nil {
		return 0, err
	}
	return acct.LineupID, nil
}

// chosenLineup returns the lineup that a request names: in its "lineup"
// parameter, or in the view that a form came from; 0 if it names none. ok is
// false for a lineup that does not exist.
func (a *Admin) chosenLineup(r *http.Request) (id int64, ok bool, err error) {
	text := r.URL.Query().Get("lineup")
	if text == "" && !safeMethod(r.Method) {
		if back, perr := url.ParseQuery(r.PostFormValue("back")); perr == nil {
			text = back.Get("lineup")
		}
	}
	if text == "" {
		return 0, true, nil
	}
	id, perr := strconv.ParseInt(text, 10, 64)
	if perr != nil || id <= 0 {
		return 0, false, nil
	}
	if _, err := a.store.Lineup(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		return 0, false, nil
	} else if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// lineupRoute serves fn with the lineup that the request chose.
func (a *Admin) lineupRoute(fn signedInFn) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		id, ok, err := a.chosenLineup(r)
		switch {
		case err != nil:
			a.log.Error("cannot look up a lineup", "error", err)
			a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
			return
		case !ok:
			a.notFound(w, r)
			return
		case id != 0:
			r = r.WithContext(inLineup(r.Context(), id))
		}
		fn(w, r, s)
	}
}

// apiLineupRoute is lineupRoute for the API, which names the lineup in its
// lineup parameter.
func (a *Admin) apiLineupRoute(fn signedInFn) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		id, ok, err := a.chosenLineup(r)
		switch {
		case a.apiFailed(w, err):
			return
		case !ok:
			apiError(w, http.StatusNotFound, "not_found", "There is no such lineup.")
			return
		case id != 0:
			r = r.WithContext(inLineup(r.Context(), id))
		}
		fn(w, r, s)
	}
}

// lineupError turns the store's errors about lineups into mistakes that
// the request can put right.
func lineupError(err error) error {
	switch {
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such lineup.")
	case errors.Is(err, store.ErrLineupExists):
		return badEdit(http.StatusConflict, "name_taken", "Another lineup has that name.")
	case errors.Is(err, store.ErrLineupInUse):
		return badEdit(http.StatusConflict, "lineup_in_use", "An account sees this lineup. Give it another one on the Accounts page first.")
	case errors.Is(err, store.ErrTooManyLineups):
		return badEdit(http.StatusConflict, "too_many_lineups", "A server has at most %d lineups.", store.MaxLineups)
	}
	return err
}

// lineupName returns the lineup's name, or "lineup N" for one that is gone.
func (a *Admin) lineupName(ctx context.Context, id int64) string {
	if l, err := a.store.Lineup(ctx, id); err == nil {
		return l.Name
	}
	return "lineup " + strconv.FormatInt(id, 10)
}

// copyLineup makes a lineup as a copy of another.
func (a *Admin) copyLineup(ctx context.Context, from int64, name string) (int64, error) {
	name, err := checkName(name, "lineup", maxLineupName)
	if err != nil {
		return 0, err
	}
	id, err := a.store.CopyLineup(ctx, from, name)
	if err != nil {
		return 0, lineupError(err)
	}
	a.log.Info("a lineup made in the admin", "lineup", name, "copy_of", a.lineupName(ctx, from))
	return id, nil
}

// renameLineup gives a lineup another name.
func (a *Admin) renameLineup(ctx context.Context, id int64, name string) error {
	name, err := checkName(name, "lineup", maxLineupName)
	if err != nil {
		return err
	}
	old := a.lineupName(ctx, id)
	if err := a.store.RenameLineup(ctx, id, name); err != nil {
		return lineupError(err)
	}
	if old != name {
		a.log.Info("a lineup renamed in the admin", "lineup", name, "was", old)
	}
	return nil
}

// removeLineup removes a lineup that no account sees.
func (a *Admin) removeLineup(ctx context.Context, id int64) error {
	name := a.lineupName(ctx, id)
	if err := a.store.RemoveLineup(ctx, id); err != nil {
		return lineupError(err)
	}
	a.log.Info("a lineup removed in the admin", "lineup", name)
	return nil
}

// setAccountLineup lets an account see another lineup.
func (a *Admin) setAccountLineup(ctx context.Context, id, lineupID int64) (store.Account, error) {
	acct, err := a.account(ctx, id)
	if err != nil {
		return store.Account{}, err
	}
	if acct.LineupID == lineupID {
		return acct, nil
	}
	if err := a.store.SetAccountLineup(ctx, id, lineupID); err != nil {
		return store.Account{}, lineupError(err)
	}
	a.log.Info("an account's lineup chosen in the admin", "account", acct.Name, "lineup", a.lineupName(ctx, lineupID))
	acct.LineupID = lineupID
	return acct, nil
}

// lineupsAction serves the Live TV page's forms about lineups: a copy of
// the lineup shown, and renaming or removing it.
func (a *Admin) lineupsAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	lv := parseLineupView(backValues(r)).In()
	shown, err := a.lineupID(ctx)
	if err != nil {
		a.lineupDoneAt(w, r, s, lv, err, "", 0)
		return
	}
	var done string
	switch r.PathValue("action") {
	case "copy":
		var id int64
		if id, err = a.copyLineup(ctx, shown, r.PostFormValue("name")); err == nil {
			lv = lineupView{Lineup: id, Page: 1}
			r = r.WithContext(inLineup(ctx, id))
			done = "Made the lineup " + strings.TrimSpace(r.PostFormValue("name")) +
				", a copy of the one before. No account sees it yet: choose it for one on the Accounts page."
		}
	case "rename":
		name := strings.TrimSpace(r.PostFormValue("name"))
		if err = a.renameLineup(ctx, shown, name); err == nil {
			done = "Renamed the lineup " + name + "."
		}
	case "remove":
		name := a.lineupName(ctx, shown)
		if err = a.removeLineup(ctx, shown); err == nil {
			lv = lineupView{Page: 1}
			r = r.WithContext(inLineup(ctx, 0))
			done = "Removed the lineup " + name + "."
		}
	default:
		a.notFound(w, r)
		return
	}
	a.lineupDoneAt(w, r, s, lv, err, done, 0)
}

// apiLineup is a lineup as the API shows it.
type apiLineup struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	Accounts  []string  `json:"accounts"`
	Shown     int       `json:"shown"`
}

func lineupBody(l store.LineupInfo) apiLineup {
	return apiLineup{ID: l.ID, Name: l.Name, CreatedAt: l.CreatedAt.UTC(), Accounts: l.Accounts, Shown: l.Shown}
}

func (a *Admin) apiLineups(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	all, err := a.store.Lineups(r.Context())
	if a.apiFailed(w, err) {
		return
	}
	out := make([]apiLineup, len(all))
	for i, l := range all {
		out[i] = lineupBody(l)
	}
	writeJSON(w, http.StatusOK, struct {
		Lineups []apiLineup `json:"lineups"`
	}{out})
}

// writeLineup answers with a lineup.
func (a *Admin) writeLineup(w http.ResponseWriter, r *http.Request, status int, id int64) {
	all, err := a.store.Lineups(r.Context())
	if a.apiFailed(w, err) {
		return
	}
	for _, l := range all {
		if l.ID == id {
			writeJSON(w, status, lineupBody(l))
			return
		}
	}
	apiError(w, http.StatusNotFound, "not_found", "There is no such lineup.")
}

func (a *Admin) apiCopyLineup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		Name   string `json:"name"`
		CopyOf int64  `json:"copy_of"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	from := body.CopyOf
	if from == 0 {
		var err error
		if from, err = a.lineupID(r.Context()); a.editAnswer(w, err) {
			return
		}
	}
	id, err := a.copyLineup(r.Context(), from, body.Name)
	if a.editAnswer(w, err) {
		return
	}
	a.writeLineup(w, r, http.StatusCreated, id)
}

// pathLineup reads the lineup's ID from the path.
func pathLineup(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		apiError(w, http.StatusNotFound, "not_found", "There is no such lineup.")
		return 0, false
	}
	return id, true
}

func (a *Admin) apiChangeLineup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathLineup(w, r)
	if !ok {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if a.editAnswer(w, a.renameLineup(r.Context(), id, body.Name)) {
		return
	}
	a.writeLineup(w, r, http.StatusOK, id)
}

func (a *Admin) apiRemoveLineup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, ok := pathLineup(w, r)
	if !ok {
		return
	}
	if a.editAnswer(w, a.removeLineup(r.Context(), id)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
