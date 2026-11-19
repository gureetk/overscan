// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/store"
)

// The Movies and Series pages (D79): the shelves on one side, hidden ones
// too, in the order devices see them in; the titles of the chosen shelf, or
// of all, on the other. Every form carries the view it came from, so that
// the page comes back to it after a change, as the Live TV page's do.

// libraryView is the part of the films or series that the page shows.
type libraryView struct {
	Kind  string // store.KindMovie or store.KindSeries
	Shelf int64  // 0 for every title
	Query string // a part of the names shown
	Page  int    // from 1
}

func parseLibraryView(kind string, v url.Values) libraryView {
	lv := libraryView{Kind: kind, Query: strings.TrimSpace(v.Get("q")), Page: 1}
	if id, err := strconv.ParseInt(v.Get("shelf"), 10, 64); err == nil && id > 0 {
		lv.Shelf = id
	}
	if p, err := strconv.Atoi(v.Get("page")); err == nil && p > 1 {
		lv.Page = p
	}
	return lv
}

// Path is the page's path: /admin/movies or /admin/series.
func (lv libraryView) Path() string {
	if lv.Kind == store.KindSeries {
		return "/admin/series"
	}
	return "/admin/movies"
}

// values encodes the view, with another page if page is not 0.
func (lv libraryView) values(page int) url.Values {
	v := url.Values{}
	if lv.Shelf != 0 {
		v.Set("shelf", strconv.FormatInt(lv.Shelf, 10))
	}
	if lv.Query != "" {
		v.Set("q", lv.Query)
	}
	if page == 0 {
		page = lv.Page
	}
	if page > 1 {
		v.Set("page", strconv.Itoa(page))
	}
	return v
}

// Back is the view as a query, for the forms to send back.
func (lv libraryView) Back() string { return lv.values(0).Encode() }

// URL returns the page's address for the view, on another page if page is
// not 0.
func (lv libraryView) URL(page int) template.URL {
	if q := lv.values(page).Encode(); q != "" {
		return template.URL(lv.Path() + "?" + q)
	}
	return template.URL(lv.Path())
}

// ShelfURL returns the address of a shelf's titles; 0 for all.
func (lv libraryView) ShelfURL(id int64) template.URL {
	if id == 0 {
		return template.URL(lv.Path())
	}
	return template.URL(lv.Path() + "?shelf=" + strconv.FormatInt(id, 10))
}

// IsShelf reports whether the view shows the shelf with the given ID.
func (lv libraryView) IsShelf(id int64) bool { return lv.Shelf == id }

// libraryData is what the Movies and Series pages show.
type libraryData struct {
	View     libraryView
	Heading  string // Movies or Series
	Noun     string // film or series, for counts
	Shelves  []store.LibraryShelf
	Selected *store.LibraryShelf // the shelf shown, nil for every title
	Place    int                 // the selected shelf's place among the shelves, from 1
	Titles   []store.AdminTitle
	Total    int // titles in the view
	All      int // titles the admin lists
	Shown    int // of them, those devices see
	First    int
	Last     int
	Prev     int // the previous page, 0 if none
	Next     int // the next page, 0 if none
	Ordered  bool
	// PINSet says that the household has a parental PIN, without which
	// locks do nothing (D55); Locks counts the shelves and titles locked.
	PINSet bool
	Locks  int
	// Adult is what sources' marks for adults do (D86).
	Adult string
}

// AdultNote says what a title's mark for adults does, for its badge.
func (d libraryData) AdultNote() string {
	switch d.Adult {
	case store.AdultLock:
		return "Its source marks it for adults, which Settings locks"
	case store.AdultHide:
		return "Its source marks it for adults, which Settings hides from devices"
	}
	return "Its source marks it for adults; Settings can lock or hide such titles"
}

// Count says how many titles of the page's kind, such as "3 films".
func (d libraryData) Count(n int) string {
	if d.Noun == "series" {
		return count(n) + " series"
	}
	return count(n) + " " + plural(n, d.Noun)
}

// libraryPage shows the Movies or the Series page.
func (a *Admin) libraryPage(kind string) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		a.showLibrary(w, r, s, http.StatusOK, parseLibraryView(kind, r.URL.Query()), view{})
	}
}

// showLibrary renders the Movies or Series page at a view, with v's alerts.
func (a *Admin) showLibrary(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, lv libraryView, v view) {
	ctx := r.Context()
	d := libraryData{View: lv, Heading: "Movies", Noun: "film"}
	section := "movies"
	if lv.Kind == store.KindSeries {
		d.Heading, d.Noun, section = "Series", "series", "series"
	}
	fail := func(err error) {
		a.log.Error("cannot read the films or series for the admin", "error", err)
		a.message(w, r, http.StatusInternalServerError, d.Heading, "Something went wrong; the server's log says what.", &s)
	}
	var err error
	if d.Shelves, err = a.store.LibraryShelves(ctx, lv.Kind); err != nil {
		fail(err)
		return
	}
	for i := range d.Shelves {
		d.Ordered = d.Ordered || d.Shelves[i].Placed
		if d.Shelves[i].ID == lv.Shelf {
			d.Selected, d.Place = &d.Shelves[i], i+1
		}
	}
	if lv.Shelf != 0 && d.Selected == nil {
		lv.Shelf, d.View.Shelf = 0, 0
		if v.Error == "" {
			v.Error = "That shelf is no longer there."
		}
	}
	if _, d.All, err = a.store.AdminTitles(ctx, lv.Kind, nil, "", 0, 0); err != nil {
		fail(err)
		return
	}
	if _, d.Shown, err = a.store.TitlePage(ctx, lv.Kind, 0, "", store.ByName, 0, 0, false); err != nil {
		fail(err)
		return
	}
	if d.PINSet, err = a.pinSet(ctx); err != nil {
		fail(err)
		return
	}
	if d.Locks, err = a.store.LibraryLocks(ctx); err != nil {
		fail(err)
		return
	}
	if d.Adult, err = a.store.AdultMarks(ctx); err != nil {
		fail(err)
		return
	}
	offset := (lv.Page - 1) * pageTitles
	if d.Titles, d.Total, err = a.store.AdminTitles(ctx, lv.Kind, d.Selected, lv.Query, offset, pageTitles); err != nil {
		fail(err)
		return
	}
	if len(d.Titles) > 0 {
		d.First, d.Last = offset+1, offset+len(d.Titles)
	}
	if lv.Page > 1 {
		d.Prev = lv.Page - 1
	}
	if offset+len(d.Titles) < d.Total {
		d.Next = lv.Page + 1
	}
	v.Title, v.Section, v.Session, v.Data = d.Heading, section, &s, d
	a.render(w, r, status, "library", v)
}

// libraryDone answers a change made from the Movies or Series page: the page
// again with what happened, for htmx, or else a redirect back to the view
// the form came from.
func (a *Admin) libraryDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, lv libraryView, err error, notice string) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showLibrary(w, r, s, bad.status, lv, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change the films or series", "error", err)
		a.showLibrary(w, r, s, http.StatusInternalServerError, lv, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showLibrary(w, r, s, http.StatusOK, lv, view{Notice: notice})
	default:
		http.Redirect(w, r, string(lv.URL(0)), http.StatusSeeOther)
	}
}

// shelfAction answers a shelf's forms on the Movies or Series page: hide,
// show, lock, unlock, rename, up and down.
func (a *Admin) shelfAction(kind string) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		ctx := r.Context()
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			a.notFound(w, r)
			return
		}
		lv := parseLibraryView(kind, backValues(r))
		sh, err := a.libraryShelf(ctx, kind, id)
		if err != nil {
			a.libraryDone(w, r, s, lv, err, "")
			return
		}
		var done string
		switch r.PathValue("action") {
		case "hide":
			err, done = a.hideShelf(ctx, kind, sh, true), "Hid "+sh.Name+": devices no longer see it, nor anything on it."
		case "show":
			err, done = a.hideShelf(ctx, kind, sh, false), "Showed "+sh.Name+"."
		case "lock":
			err, done = a.lockShelf(ctx, kind, sh, true), "Locked "+sh.Name+": devices need the parental PIN for anything on it."
		case "unlock":
			err, done = a.lockShelf(ctx, kind, sh, false), "Unlocked "+sh.Name+"."
		case "rename":
			name := strings.TrimSpace(r.PostFormValue("name"))
			if err = a.renameShelf(ctx, kind, sh, name); err == nil {
				done = "Renamed the shelf " + name + "."
				if name == "" || name == sh.Own {
					done = "The shelf has its sources' name again: " + sh.Own + "."
				}
			}
		case "up":
			err, done = a.moveShelf(ctx, kind, id, -1), "Moved "+sh.Name+" up."
		case "down":
			err, done = a.moveShelf(ctx, kind, id, 1), "Moved "+sh.Name+" down."
		default:
			a.notFound(w, r)
			return
		}
		a.libraryDone(w, r, s, lv, err, done)
	}
}

// shelfOrderReset gives every shelf back its sources' order.
func (a *Admin) shelfOrderReset(kind string) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		lv := parseLibraryView(kind, backValues(r))
		a.libraryDone(w, r, s, lv, a.orderShelves(r.Context(), kind, nil), "The shelves are in their sources' order again.")
	}
}

// titlesAction hides, shows, locks or unlocks the ticked titles on the
// Movies or Series page.
func (a *Admin) titlesAction(kind string) signedInFn {
	return func(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
		lv := parseLibraryView(kind, backValues(r))
		var ids []int64
		for _, v := range r.PostForm["ids"] {
			id, err := strconv.ParseInt(v, 10, 64)
			if err != nil || id <= 0 {
				a.libraryDone(w, r, s, lv, badEdit(http.StatusBadRequest, "bad_form", "Those are not titles."), "")
				return
			}
			ids = append(ids, id)
		}
		action := r.PostFormValue("action")
		var n int
		var err error
		switch action {
		case "hide", "show":
			n, err = a.hideTitles(r.Context(), ids, action == "hide")
		case "lock", "unlock":
			n, err = a.lockTitles(r.Context(), ids, action == "lock")
		default:
			a.libraryDone(w, r, s, lv, badEdit(http.StatusBadRequest, "bad_form", "Choose Hide, Show, Lock or Unlock."), "")
			return
		}
		what := fmt.Sprintf("%d series", n)
		if kind != store.KindSeries {
			what = fmt.Sprintf("%d %s", n, plural(n, "film"))
		}
		done := map[string]string{
			"hide":   "Hid " + what + " from devices.",
			"show":   "Showed " + what + ".",
			"lock":   "Locked " + what + ": devices need the parental PIN to play them.",
			"unlock": "Unlocked " + what + ". Those on a locked shelf stay locked.",
		}[action]
		a.libraryDone(w, r, s, lv, err, done)
	}
}
