// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"overscan.gureet.ca/internal/store"
)

// The films and series that devices see (D79): the administrator hides,
// renames, locks (D85) and orders their shelves, and hides and locks
// titles, on the Movies and Series pages and in the API. What the API and
// the pages do goes through the same functions here.

const (
	// pageTitles is how many titles a page of the Movies or Series page
	// lists, and maxPageTitles the most the API gives at once.
	pageTitles    = 100
	maxPageTitles = 1000
)

// libraryKind returns the store's kind for a kind in a path, movies or
// series.
func libraryKind(path string) (string, bool) {
	switch path {
	case "movies":
		return store.KindMovie, true
	case "series":
		return store.KindSeries, true
	}
	return "", false
}

// libraryShelf returns a shelf that the admin lists, or an edit error if
// there is none.
func (a *Admin) libraryShelf(ctx context.Context, kind string, id int64) (store.LibraryShelf, error) {
	sh, err := a.store.LibraryShelf(ctx, kind, id)
	if errors.Is(err, store.ErrNotFound) {
		return sh, badEdit(http.StatusNotFound, "not_found", "There is no such shelf.")
	}
	return sh, err
}

// hideShelf hides a shelf from devices, or shows it again.
func (a *Admin) hideShelf(ctx context.Context, kind string, sh store.LibraryShelf, hidden bool) error {
	if err := a.store.SetShelfHidden(ctx, kind, sh.Key, hidden); err != nil {
		return err
	}
	a.log.Info("a shelf hidden or shown in the admin", "kind", kind, "shelf", sh.Own, "hidden", hidden)
	return nil
}

// lockShelf locks a shelf, and every title on it, so that devices need the
// parental PIN to play them (D85), or unlocks it.
func (a *Admin) lockShelf(ctx context.Context, kind string, sh store.LibraryShelf, locked bool) error {
	if err := a.store.SetShelfLocked(ctx, kind, sh.Key, locked); err != nil {
		return err
	}
	a.log.Info("a shelf locked or unlocked in the admin", "kind", kind, "shelf", sh.Own, "locked", locked)
	return nil
}

// renameShelf gives a shelf the name devices see it by; "" gives it back
// its own.
func (a *Admin) renameShelf(ctx context.Context, kind string, sh store.LibraryShelf, name string) error {
	err := a.store.RenameShelf(ctx, kind, sh.Key, name)
	if errors.Is(err, store.ErrBadName) {
		return badEdit(http.StatusBadRequest, "bad_name", "A shelf's name is 1 to %d characters.", store.MaxShelfName)
	}
	if err != nil {
		return err
	}
	a.log.Info("a shelf renamed in the admin", "kind", kind, "shelf", sh.Own, "name", name)
	return nil
}

// moveShelf moves a shelf by delta places among the shelves, which then
// keep the order the administrator gave them.
func (a *Admin) moveShelf(ctx context.Context, kind string, id int64, delta int) error {
	shelves, err := a.store.LibraryShelves(ctx, kind)
	if err != nil {
		return err
	}
	at := slices.IndexFunc(shelves, func(sh store.LibraryShelf) bool { return sh.ID == id })
	if at < 0 {
		return badEdit(http.StatusNotFound, "not_found", "There is no such shelf.")
	}
	to := min(max(at+delta, 0), len(shelves)-1)
	moved := shelves[at]
	shelves = slices.Insert(slices.Delete(shelves, at, at+1), to, moved)
	keys := make([]string, len(shelves))
	for i, sh := range shelves {
		keys[i] = sh.Key
	}
	if err := a.store.PlaceShelves(ctx, kind, keys); err != nil {
		return err
	}
	a.log.Info("a shelf moved in the admin", "kind", kind, "shelf", moved.Own, "place", to+1)
	return nil
}

// orderShelves puts the shelves with the given IDs first, in that order,
// and the others after them in their sources' order; none gives every shelf
// back its sources' order.
func (a *Admin) orderShelves(ctx context.Context, kind string, ids []int64) error {
	shelves, err := a.store.LibraryShelves(ctx, kind)
	if err != nil {
		return err
	}
	keys := make([]string, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		i := slices.IndexFunc(shelves, func(sh store.LibraryShelf) bool { return sh.ID == id })
		if i < 0 || seen[id] {
			return badEdit(http.StatusBadRequest, "bad_shelves", "Give each shelf's ID once, of shelves that are listed.")
		}
		seen[id] = true
		keys = append(keys, shelves[i].Key)
	}
	if err := a.store.PlaceShelves(ctx, kind, keys); err != nil {
		return err
	}
	a.log.Info("the order of the shelves set in the admin", "kind", kind, "placed", len(keys))
	return nil
}

// hideTitles hides films or series from devices, or shows them again, and
// returns how many there were.
func (a *Admin) hideTitles(ctx context.Context, ids []int64, hidden bool) (int, error) {
	if len(ids) == 0 {
		return 0, badEdit(http.StatusBadRequest, "no_titles", "Tick the titles first.")
	}
	n, err := a.store.SetTitlesHidden(ctx, ids, hidden)
	if err != nil {
		return 0, err
	}
	a.log.Info("titles hidden or shown in the admin", "titles", n, "hidden", hidden)
	return n, nil
}

// lockTitles locks films or series, or unlocks them, and returns how many
// there were.
func (a *Admin) lockTitles(ctx context.Context, ids []int64, locked bool) (int, error) {
	if len(ids) == 0 {
		return 0, badEdit(http.StatusBadRequest, "no_titles", "Tick the titles first.")
	}
	n, err := a.store.SetTitlesLocked(ctx, ids, locked)
	if err != nil {
		return 0, err
	}
	a.log.Info("titles locked or unlocked in the admin", "titles", n, "locked", locked)
	return n, nil
}

// apiShelf is a shelf in the API.
type apiShelf struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"` // the name devices see it by
	Own          string   `json:"own"`  // the name its sources give it
	Sources      []string `json:"sources"`
	Titles       int      `json:"titles"`
	HiddenTitles int      `json:"hidden_titles"`
	Portal       bool     `json:"portal"`
	Hidden       bool     `json:"hidden"`
	Locked       bool     `json:"locked"`
	Placed       bool     `json:"placed"`
}

func shelfOut(sh store.LibraryShelf) apiShelf {
	out := apiShelf{ID: sh.ID, Name: sh.Name, Own: sh.Own, Sources: sh.Sources, Titles: sh.Titles,
		HiddenTitles: sh.HiddenTitles, Portal: sh.Portal, Hidden: sh.Hidden, Locked: sh.Locked, Placed: sh.Placed}
	if out.Sources == nil {
		out.Sources = []string{}
	}
	return out
}

// apiTitle is a film or a series in the API.
type apiTitle struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Year   int    `json:"year,omitempty"`
	Source string `json:"source"`
	Shelf  string `json:"shelf,omitempty"` // its category's name, as its source gives it
	Hidden bool   `json:"hidden"`
	Locked bool   `json:"locked"`    // the administrator locked it
	Adult  bool   `json:"adult"`     // its source marks it for adults
	PIN    bool   `json:"needs_pin"` // devices need the PIN for it once there is one: it, its shelf or its mark
}

func titleOut(t store.AdminTitle) apiTitle {
	return apiTitle{ID: t.ID, Kind: t.Kind, Name: t.Name, Year: t.Year, Source: t.Source, Shelf: t.Shelf, Hidden: t.Hidden,
		Locked: t.Own, Adult: t.Adult, PIN: t.Locked}
}

// apiKind reads the kind in a request's path; false if it answered that
// there is no such kind.
func apiKind(w http.ResponseWriter, r *http.Request) (string, bool) {
	kind, ok := libraryKind(r.PathValue("kind"))
	if !ok {
		apiError(w, http.StatusNotFound, "not_found", "There is no such path in the API.")
	}
	return kind, ok
}

func (a *Admin) apiShelves(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	kind, ok := apiKind(w, r)
	if !ok {
		return
	}
	shelves, err := a.store.LibraryShelves(r.Context(), kind)
	if err != nil {
		a.apiInternal(w, "cannot list the shelves", err)
		return
	}
	out := make([]apiShelf, 0, len(shelves))
	for _, sh := range shelves {
		out = append(out, shelfOut(sh))
	}
	writeJSON(w, http.StatusOK, map[string]any{"shelves": out})
}

func (a *Admin) apiChangeShelf(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	kind, ok := apiKind(w, r)
	if !ok {
		return
	}
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body map[string]json.RawMessage
	if !readJSON(w, r, &body) {
		return
	}
	ctx := r.Context()
	sh, err := a.libraryShelf(ctx, kind, id)
	if a.editAnswer(w, err) {
		return
	}
	for k, v := range body {
		switch k {
		case "hidden":
			var hidden bool
			if json.Unmarshal(v, &hidden) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "hidden is true or false.")
				return
			}
			err = a.hideShelf(ctx, kind, sh, hidden)
		case "locked":
			var locked bool
			if json.Unmarshal(v, &locked) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "locked is true or false.")
				return
			}
			err = a.lockShelf(ctx, kind, sh, locked)
		case "name":
			var name *string
			if json.Unmarshal(v, &name) != nil {
				apiError(w, http.StatusBadRequest, "bad_json", "name is a string, or null for the sources' name.")
				return
			}
			given := ""
			if name != nil {
				given = *name
			}
			err = a.renameShelf(ctx, kind, sh, given)
		default:
			apiError(w, http.StatusBadRequest, "bad_json", "A shelf has no field named "+k+".")
			return
		}
		if a.editAnswer(w, err) {
			return
		}
	}
	sh, err = a.libraryShelf(ctx, kind, id)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, shelfOut(sh))
}

func (a *Admin) apiOrderShelves(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	kind, ok := apiKind(w, r)
	if !ok {
		return
	}
	var body struct {
		Shelves []int64 `json:"shelves"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if a.editAnswer(w, a.orderShelves(r.Context(), kind, body.Shelves)) {
		return
	}
	a.apiShelves(w, r, s)
}

func (a *Admin) apiResetOrder(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	kind, ok := apiKind(w, r)
	if !ok {
		return
	}
	if a.editAnswer(w, a.orderShelves(r.Context(), kind, nil)) {
		return
	}
	a.apiShelves(w, r, s)
}

func (a *Admin) apiTitles(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	kind, ok := apiKind(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	q := r.URL.Query()
	var shelf *store.LibraryShelf
	if v := q.Get("shelf"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			apiError(w, http.StatusBadRequest, "bad_query", "shelf is a shelf's ID.")
			return
		}
		sh, err := a.libraryShelf(ctx, kind, id)
		if a.editAnswer(w, err) {
			return
		}
		shelf = &sh
	}
	offset, limit := 0, pageTitles
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			apiError(w, http.StatusBadRequest, "bad_query", "offset is a whole number from 0.")
			return
		}
		offset = n
	}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPageTitles {
			apiError(w, http.StatusBadRequest, "bad_query", "limit is a whole number from 1 to 1000.")
			return
		}
		limit = n
	}
	titles, total, err := a.store.AdminTitles(ctx, kind, shelf, q.Get("q"), offset, limit)
	if err != nil {
		a.apiInternal(w, "cannot list the titles", err)
		return
	}
	out := make([]apiTitle, 0, len(titles))
	for _, t := range titles {
		out = append(out, titleOut(t))
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": total, "titles": out})
}

func (a *Admin) apiChangeTitle(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	var body struct {
		Hidden *bool `json:"hidden"`
		Locked *bool `json:"locked"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Hidden == nil && body.Locked == nil {
		apiError(w, http.StatusBadRequest, "bad_json", "Give hidden or locked, true or false.")
		return
	}
	ctx := r.Context()
	if _, err := a.store.AdminTitleByID(ctx, id); errors.Is(err, store.ErrNotFound) {
		apiError(w, http.StatusNotFound, "not_found", "There is no such title.")
		return
	} else if err != nil {
		a.apiInternal(w, "cannot look up a title", err)
		return
	}
	if body.Hidden != nil {
		if _, err := a.hideTitles(ctx, []int64{id}, *body.Hidden); a.editAnswer(w, err) {
			return
		}
	}
	if body.Locked != nil {
		if _, err := a.lockTitles(ctx, []int64{id}, *body.Locked); a.editAnswer(w, err) {
			return
		}
	}
	t, err := a.store.AdminTitleByID(ctx, id)
	if err != nil {
		a.apiInternal(w, "cannot look up a title", err)
		return
	}
	writeJSON(w, http.StatusOK, titleOut(t))
}
