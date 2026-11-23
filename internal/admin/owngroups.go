// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"html/template"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/store"
)

// Lineups of the household's own groups (D136): a lineup that starts empty
// and holds what its groups take; a page that chooses the categories a
// group takes from every source's; and the accounts that see a lineup,
// with how their devices and players reach it.

// mergeRules returns the rules that a new lineup starts with for a choice
// of how to merge the copies of a channel (D113): "" for none, or the
// rules' "guide" or "name".
func mergeRules(choice string) ([]rules.Rule, error) {
	switch choice {
	case "", "none":
		return nil, nil
	case rules.ByGuide, rules.ByName:
		return []rules.Rule{{Action: rules.Merge, Copies: choice}}, nil
	}
	return nil, badEdit(http.StatusBadRequest, "bad_merge", "Merge the copies of a channel by their guide or by their names, or not at all.")
}

// newLineup makes an empty lineup of its own groups.
func (a *Admin) newLineup(ctx context.Context, name, merge string) (int64, error) {
	name, err := checkName(name, "lineup", maxLineupName)
	if err != nil {
		return 0, err
	}
	rs, err := mergeRules(merge)
	if err != nil {
		return 0, err
	}
	id, err := a.store.NewLineup(ctx, name, rs...)
	var bad *rules.Error
	if errors.As(err, &bad) {
		return 0, badEdit(http.StatusBadRequest, "bad_rule", "%s", bad.Error())
	}
	if err != nil {
		return 0, lineupError(err)
	}
	a.log.Info("a lineup made in the admin", "lineup", name, "own_groups", true, "merge", cmp.Or(merge, "none"))
	return id, nil
}

// setGroupCategories makes a group of the lineup take exactly the given
// categories.
func (a *Admin) setGroupCategories(ctx context.Context, groupID int64, cats []store.CategoryRef) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	err = a.store.SetGroupCategories(ctx, lineup, groupID, cats)
	switch {
	case errors.Is(err, store.ErrTooManyCategories):
		return badEdit(http.StatusBadRequest, "too_many_categories", "A group takes at most %d categories.", store.MaxGroupCategories)
	case errors.Is(err, store.ErrNotFound):
		return badEdit(http.StatusNotFound, "not_found", "There is no such group, or a category is not one that a source lists.")
	case err != nil:
		return err
	}
	a.log.Info("a group's categories chosen in the admin", "lineup", a.lineupName(ctx, lineup), "group", groupID, "categories", len(cats))
	return nil
}

// lineupGroup returns a group of the lineup, or ErrNotFound.
func (a *Admin) lineupGroup(ctx context.Context, groupID int64) (store.LineupGroup, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return store.LineupGroup{}, err
	}
	groups, err := a.store.LineupGroups(ctx, lineup)
	if err != nil {
		return store.LineupGroup{}, err
	}
	for _, g := range groups {
		if g.ID == groupID && g.ID != 0 {
			return g, nil
		}
	}
	return store.LineupGroup{}, store.ErrNotFound
}

// categoryKey is how a form names a source's category: its source's ID, a
// slash, and its name.
func categoryKey(c store.CategoryRef) string {
	return strconv.FormatInt(c.SourceID, 10) + "/" + c.Name
}

func parseCategoryKey(key string) (store.CategoryRef, bool) {
	id, name, ok := strings.Cut(key, "/")
	source, err := strconv.ParseInt(id, 10, 64)
	if !ok || err != nil || source <= 0 {
		return store.CategoryRef{}, false
	}
	return store.CategoryRef{SourceID: source, Name: name}, true
}

// maxPickerCategories caps how many categories the page lists; its search
// finds the others.
const maxPickerCategories = 1000

// groupCategoryRow is a category on the page that chooses a group's.
type groupCategoryRow struct {
	store.LineupCategory
	Mine bool // the group shown takes it
}

// Key names the category in the page's form.
func (c groupCategoryRow) Key() string { return categoryKey(c.CategoryRef) }

// Label names the category, or the channels without one.
func (c groupCategoryRow) Label() string {
	if c.Name == "" {
		return "Without a category"
	}
	return c.Name
}

// ItemsText says how many channels the category held at its source's last
// read.
func (c groupCategoryRow) ItemsText() string {
	return count(c.Items) + " " + plural(c.Items, "channel")
}

// groupCategoriesData is what the page that chooses a group's categories
// shows.
type groupCategoriesData struct {
	View       lineupView // the lineup and the group
	Lineup     store.LineupInfo
	Several    bool // there is more than one lineup
	Group      store.LineupGroup
	Query      string
	Categories []groupCategoryRow
	Total      int // the categories that the search finds
	Mine       int // of those listed, the ones the group takes
	More       bool
	// Elsewhere counts the categories that the group takes and the page
	// does not list, which saving keeps.
	Elsewhere int
}

// Action is where the page's form goes.
func (d groupCategoriesData) Action() string {
	return "/admin/lineup/groups/" + strconv.FormatInt(d.Group.ID, 10) + "/categories"
}

// SearchURL is the page's address, for its search to come back to.
func (d groupCategoriesData) SearchURL() template.URL {
	if d.View.Lineup != 0 {
		return template.URL(d.Action() + "?lineup=" + strconv.FormatInt(d.View.Lineup, 10))
	}
	return template.URL(d.Action())
}

func (a *Admin) groupCategoriesData(ctx context.Context, lv lineupView, groupID int64, query string) (groupCategoriesData, error) {
	g, err := a.lineupGroup(ctx, groupID)
	if err != nil {
		return groupCategoriesData{}, err
	}
	lv.Group, lv.Page, lv.Query, lv.Add = &g.ID, 1, "", ""
	d := groupCategoriesData{View: lv, Group: g, Query: query}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return d, err
	}
	all, err := a.store.Lineups(ctx)
	if err != nil {
		return d, err
	}
	d.Several = len(all) > 1
	for _, l := range all {
		if l.ID == lineup {
			d.Lineup = l
		}
	}
	cats, total, err := a.store.LineupCategories(ctx, lineup, query)
	if err != nil {
		return d, err
	}
	if len(cats) > maxPickerCategories {
		cats = cats[:maxPickerCategories]
	}
	d.Total, d.More = total, total > len(cats)
	listed := map[store.CategoryRef]bool{}
	for _, c := range cats {
		row := groupCategoryRow{LineupCategory: c, Mine: c.GroupID == g.ID}
		d.Categories = append(d.Categories, row)
		listed[c.CategoryRef] = true
		if row.Mine {
			d.Mine++
		}
	}
	for _, c := range g.Categories {
		if !listed[store.CategoryRef{SourceID: c.SourceID, Name: c.Name}] {
			d.Elsewhere++
		}
	}
	return d, nil
}

// groupCategoriesPage shows the categories of every source, to choose the
// group's.
func (a *Admin) groupCategoriesPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showGroupCategories(w, r, s, http.StatusOK, strings.TrimSpace(r.URL.Query().Get("q")), view{})
}

func (a *Admin) showGroupCategories(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, query string, v view) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		a.notFound(w, r)
		return
	}
	lv := parseLineupView(r.URL.Query())
	if r.Method != http.MethodGet {
		lv = parseLineupView(backValues(r))
	}
	data, err := a.groupCategoriesData(ctx, lv, id, query)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	if err != nil {
		a.log.Error("cannot show a group's categories", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "What "+data.Group.Name+" takes · Live TV", "lineup", &s, data
	a.render(w, r, status, "group-categories", v)
}

// groupCategoriesForm saves the categories ticked on the page: those it
// listed and left unticked leave the group, and those it did not list stay
// as they were.
func (a *Admin) groupCategoriesForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		a.notFound(w, r)
		return
	}
	g, err := a.lineupGroup(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	query := strings.TrimSpace(r.PostFormValue("q"))
	if err != nil {
		a.log.Error("cannot change a group's categories", "error", err)
		a.showGroupCategories(w, r, s, http.StatusInternalServerError, query, view{Error: "Something went wrong; the server's log says what."})
		return
	}
	set := map[store.CategoryRef]bool{}
	for _, c := range g.Categories {
		set[store.CategoryRef{SourceID: c.SourceID, Name: c.Name}] = true
	}
	for _, key := range r.PostForm["listed"] {
		if c, ok := parseCategoryKey(key); ok {
			delete(set, c)
		}
	}
	for _, key := range r.PostForm["tick"] {
		if c, ok := parseCategoryKey(key); ok {
			set[c] = true
		}
	}
	cats := slices.SortedFunc(maps.Keys(set), func(x, y store.CategoryRef) int {
		return cmp.Or(cmp.Compare(x.SourceID, y.SourceID), strings.Compare(x.Name, y.Name))
	})
	err = a.setGroupCategories(ctx, id, cats)
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showGroupCategories(w, r, s, bad.status, query, view{Error: bad.text})
		return
	case err != nil:
		a.log.Error("cannot change a group's categories", "error", err)
		a.showGroupCategories(w, r, s, http.StatusInternalServerError, query, view{Error: "Something went wrong; the server's log says what."})
		return
	}
	lv := parseLineupView(backValues(r))
	lv.Group, lv.Page, lv.Query, lv.Add = &g.ID, 1, "", ""
	a.lineupDoneAt(w, r, s, lv, nil, fmt.Sprintf("%s takes %s %s.", g.Name, count(len(cats)), plural(len(cats), "category")), 0)
}

// lineupSeen is who sees the lineup shown, for the top of its pages.
type lineupSeen struct {
	Viewers   []store.LineupViewer
	OutputsOn bool // the server's outputs for players are on (D103, D104)
}

// seenLine is an account that sees a lineup, and how.
type seenLine struct {
	AccountID int64
	Account   string
	Ways      string // its boxes, Stalker clients, playlist and login
}

// SeenLines says, for each account that sees the lineup, how its devices
// and players reach it: its boxes, its Stalker clients, its playlist and
// guide and its Xtream login, those last two only while the server's
// outputs are on.
func (s lineupSeen) SeenLines() []seenLine {
	var out []seenLine
	for _, v := range s.Viewers {
		var ways []string
		switch {
		case v.Boxes == 1:
			ways = append(ways, "1 box")
		case v.Boxes > 1:
			ways = append(ways, count(v.Boxes)+" boxes")
		}
		if v.Clients > 0 {
			ways = append(ways, count(v.Clients)+" Stalker "+plural(v.Clients, "client"))
		}
		if s.OutputsOn && v.Playlist {
			ways = append(ways, "its playlist and guide")
		}
		if s.OutputsOn && v.Login {
			ways = append(ways, "its Xtream login")
		}
		line := seenLine{AccountID: v.AccountID, Account: v.Account, Ways: andList(ways)}
		if line.Ways == "" {
			line.Ways = "nothing yet"
		}
		out = append(out, line)
	}
	return out
}

// lineupSeen reads who sees a lineup.
func (a *Admin) lineupSeen(ctx context.Context, lineupID int64) (lineupSeen, error) {
	var s lineupSeen
	var err error
	if s.Viewers, err = a.store.LineupViewers(ctx, lineupID); err != nil {
		return s, err
	}
	s.OutputsOn, err = a.store.Outputs(ctx)
	return s, err
}

// apiViewer is an account that sees a lineup, as the API shows it.
type apiViewer struct {
	AccountID      int64  `json:"account_id"`
	Account        string `json:"account"`
	Boxes          int    `json:"boxes"`
	StalkerClients int    `json:"stalker_clients"`
	Playlist       bool   `json:"playlist"`
	XtreamLogin    bool   `json:"xtream_login"`
}

func viewerBody(v store.LineupViewer) apiViewer {
	return apiViewer{AccountID: v.AccountID, Account: v.Account, Boxes: v.Boxes, StalkerClients: v.Clients,
		Playlist: v.Playlist, XtreamLogin: v.Login}
}

// apiLineupCategory is a source's category as a lineup's groups take it.
type apiLineupCategory struct {
	SourceID int64  `json:"source_id"`
	Source   string `json:"source"`
	Name     string `json:"name"`
	Channels int    `json:"channels"`
	GroupID  *int64 `json:"group_id"`
	Group    string `json:"group"`
}

// apiLineupCategories lists the live categories of every source, with the
// group of the lineup that takes each.
func (a *Admin) apiLineupCategories(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	lineup, err := a.lineupID(r.Context())
	if a.apiFailed(w, err) {
		return
	}
	cats, total, err := a.store.LineupCategories(r.Context(), lineup, r.URL.Query().Get("q"))
	if a.apiFailed(w, err) {
		return
	}
	out := make([]apiLineupCategory, len(cats))
	for i, c := range cats {
		out[i] = apiLineupCategory{SourceID: c.SourceID, Source: c.Source, Name: c.Name, Channels: c.Items, Group: c.Group}
		if c.GroupID != 0 {
			out[i].GroupID = &cats[i].GroupID
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Categories []apiLineupCategory `json:"categories"`
		Total      int                 `json:"total"`
	}{out, total})
}

// apiSetGroupCategories makes a group take exactly the categories it is
// sent.
func (a *Admin) apiSetGroupCategories(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "not_found", "There is no such group.")
		return
	}
	var body struct {
		Categories []struct {
			SourceID int64  `json:"source_id"`
			Name     string `json:"name"`
		} `json:"categories"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if len(body.Categories) > store.MaxGroupCategories {
		apiError(w, http.StatusBadRequest, "too_many_categories", fmt.Sprintf("A group takes at most %d categories.", store.MaxGroupCategories))
		return
	}
	cats := make([]store.CategoryRef, len(body.Categories))
	for i, c := range body.Categories {
		cats[i] = store.CategoryRef{SourceID: c.SourceID, Name: c.Name}
	}
	if a.editAnswer(w, a.setGroupCategories(r.Context(), id, cats)) {
		return
	}
	a.writeGroup(w, r, http.StatusOK, id)
}
