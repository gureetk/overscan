// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/store"
)

// Numbers and order (D137): a group's first number, sorting a group's
// channels by name, and find and replace in the names of groups and
// shelves.

// setNumbersFrom gives a group of the lineup a first number, or takes it
// away with 0.
func (a *Admin) setNumbersFrom(ctx context.Context, id int64, from int) error {
	if from < 0 || from > store.MaxNumber {
		return lineupStoreError(store.ErrBadNumber, "")
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	if err := a.store.SetGroupNumbersFrom(ctx, lineup, id, from); err != nil {
		return lineupStoreError(err, "group")
	}
	a.log.Info("a group's first number set in the admin", "lineup", a.lineupName(ctx, lineup), "group", id, "from", from)
	return nil
}

// sortGroup sorts a group's channels by name, and returns how many moved.
func (a *Admin) sortGroup(ctx context.Context, id int64) (int, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return 0, err
	}
	n, err := a.store.SortGroupByName(ctx, lineup, id)
	if err != nil {
		return 0, lineupStoreError(err, "group")
	}
	a.log.Info("a group sorted by name in the admin", "lineup", a.lineupName(ctx, lineup), "group", id, "moved", n)
	return n, nil
}

// renamed is a name that a find and replace changed.
type renamed struct {
	ID   int64  `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
}

// renamedText says what a find and replace renamed, the first few by name.
func renamedText(what string, done []renamed) string {
	if len(done) == 0 {
		return "No " + what + "'s name holds that text: nothing changed."
	}
	var names []string
	for i, d := range done {
		if i == 5 {
			names = append(names, fmt.Sprintf("%d more", len(done)-5))
			break
		}
		names = append(names, d.From+" → "+d.To)
	}
	return fmt.Sprintf("Renamed %d %s: %s.", len(done), plural(len(done), what), andList(names))
}

// replacer compiles a find and replace, as a rename rule's (D107).
func replacer(find, with string) (func(string) string, error) {
	change, err := rules.CompileReplace(find, with)
	var bad *rules.Error
	if errors.As(err, &bad) {
		return nil, badEdit(http.StatusBadRequest, "bad_replace", "%s", bad.Error())
	}
	return change, err
}

// replaceGroupNames finds and replaces text in the names of the lineup's
// groups, all at once or not at all.
func (a *Admin) replaceGroupNames(ctx context.Context, find, with string) ([]renamed, error) {
	change, err := replacer(find, with)
	if err != nil {
		return nil, err
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := a.store.LineupGroups(ctx, lineup)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	byName := map[string]string{}
	var done []renamed
	for _, g := range groups {
		if g.ID == 0 {
			continue
		}
		to := strings.TrimSpace(change(g.Name))
		if to != g.Name {
			if _, err := checkName(to, "group", maxGroupName); err != nil {
				var bad *editError
				errors.As(err, &bad)
				return nil, badEdit(http.StatusBadRequest, "bad_name", "%s would be renamed %q: %s", g.Name, to, bad.text)
			}
			names[g.ID] = to
			done = append(done, renamed{ID: g.ID, From: g.Name, To: to})
		}
		if other, ok := byName[to]; ok {
			return nil, badEdit(http.StatusConflict, "name_taken", "%s and %s would both be named %s.", other, g.Name, to)
		}
		byName[to] = g.Name
	}
	if len(done) == 0 {
		return done, nil
	}
	if err := a.store.SetLineupGroupNames(ctx, lineup, names); err != nil {
		return nil, lineupStoreError(err, "group")
	}
	a.log.Info("groups renamed in the admin", "lineup", a.lineupName(ctx, lineup), "groups", len(done))
	return done, nil
}

// replaceShelfNames finds and replaces text in the names of the shelves of
// films or of series, all at once or not at all.
func (a *Admin) replaceShelfNames(ctx context.Context, kind, find, with string) ([]renamed, error) {
	change, err := replacer(find, with)
	if err != nil {
		return nil, err
	}
	shelves, err := a.store.LibraryShelves(ctx, kind)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	var done []renamed
	for _, sh := range shelves {
		to := strings.TrimSpace(change(sh.Name))
		if to == sh.Name {
			continue
		}
		if to == "" || utf8.RuneCountInString(to) > store.MaxShelfName {
			return nil, badEdit(http.StatusBadRequest, "bad_name", "%s would be renamed %q: a shelf's name is 1 to %d characters.",
				sh.Name, to, store.MaxShelfName)
		}
		names[sh.Key] = to
		done = append(done, renamed{ID: sh.ID, From: sh.Name, To: to})
	}
	if len(done) == 0 {
		return done, nil
	}
	err = a.store.RenameShelves(ctx, kind, names)
	if errors.Is(err, store.ErrBadName) {
		return nil, badEdit(http.StatusBadRequest, "bad_name", "A shelf's name is 1 to %d characters, without control characters.", store.MaxShelfName)
	}
	if err != nil {
		return nil, err
	}
	a.log.Info("shelves renamed in the admin", "kind", kind, "shelves", len(done))
	return done, nil
}

// replaceGroupsForm finds and replaces text in the names of the lineup's
// groups, from the Live TV page.
func (a *Admin) replaceGroupsForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	lv := parseLineupView(backValues(r))
	done, err := a.replaceGroupNames(r.Context(), r.PostFormValue("find"), r.PostFormValue("with"))
	a.lineupDoneAt(w, r, s, lv, err, renamedText("group", done), 0)
}

// replaceShelvesForm finds and replaces text in the names of the shelves,
// from the Movies or Series page.
func (a *Admin) replaceShelvesForm(kind string) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		lv := parseLibraryView(kind, backValues(r))
		done, err := a.replaceShelfNames(r.Context(), kind, r.PostFormValue("find"), r.PostFormValue("with"))
		a.libraryDone(w, r, s, lv, err, renamedText("shelf", done))
	}
}

// numbersFromForm reads a group's first number from a form: "" takes it
// away.
func numbersFromForm(r *http.Request) (int, error) {
	text := strings.TrimSpace(r.PostFormValue("from"))
	if text == "" || r.PostFormValue("stop") == "1" {
		return 0, nil
	}
	from, err := strconv.Atoi(text)
	if err != nil || from < 1 || from > store.MaxNumber {
		return 0, lineupStoreError(store.ErrBadNumber, "")
	}
	return from, nil
}

// API.

func (a *Admin) apiSortGroup(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	n, err := a.sortGroup(r.Context(), id)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Sorted int `json:"sorted"`
	}{n})
}

// apiReplace reads a find and replace from the API.
func apiReplace(w http.ResponseWriter, r *http.Request) (find, with string, ok bool) {
	var body struct {
		Find    string `json:"find"`
		Replace string `json:"replace"`
	}
	if !readJSON(w, r, &body) {
		return "", "", false
	}
	return body.Find, body.Replace, true
}

func (a *Admin) apiReplaceGroupNames(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	find, with, ok := apiReplace(w, r)
	if !ok {
		return
	}
	done, err := a.replaceGroupNames(r.Context(), find, with)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Renamed []renamed `json:"renamed"`
	}{append([]renamed{}, done...)})
}

func (a *Admin) apiReplaceShelfNames(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	kind, ok := apiKind(w, r)
	if !ok {
		return
	}
	find, with, ok := apiReplace(w, r)
	if !ok {
		return
	}
	done, err := a.replaceShelfNames(r.Context(), kind, find, with)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Renamed []renamed `json:"renamed"`
	}{append([]renamed{}, done...)})
}
