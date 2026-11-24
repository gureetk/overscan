// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/store"
)

// The lineup editor's pages (D47): the groups on one side, the channels of
// the chosen group, or of all, on the other. Every form carries the view it
// came from, so that the page comes back to it after a change.

// lineupView is the part of the lineup that the page shows.
type lineupView struct {
	// Lineup is the lineup shown, 0 for the household account's (D106).
	Lineup int64
	Group  *int64 // nil for every channel
	Query  string // a part of the names shown
	Page   int    // from 1
	Add    string // a part of the names of channels in other groups, to add to Group
}

func parseLineupView(v url.Values) lineupView {
	lv := lineupView{Query: strings.TrimSpace(v.Get("q")), Page: 1}
	if id, err := strconv.ParseInt(v.Get("lineup"), 10, 64); err == nil && id > 0 {
		lv.Lineup = id
	}
	if id, err := strconv.ParseInt(v.Get("group"), 10, 64); err == nil && id >= 0 {
		lv.Group = &id
		if id > 0 {
			lv.Add = strings.TrimSpace(v.Get("add"))
		}
	}
	if p, err := strconv.Atoi(v.Get("page")); err == nil && p > 1 {
		lv.Page = p
	}
	return lv
}

// values encodes the view, with another page if page is not 0.
func (lv lineupView) values(page int) url.Values {
	v := url.Values{}
	if lv.Lineup != 0 {
		v.Set("lineup", strconv.FormatInt(lv.Lineup, 10))
	}
	if lv.Group != nil {
		v.Set("group", strconv.FormatInt(*lv.Group, 10))
	}
	if lv.Query != "" {
		v.Set("q", lv.Query)
	}
	if lv.Add != "" {
		v.Set("add", lv.Add)
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
func (lv lineupView) Back() string { return lv.values(0).Encode() }

// URL returns the page's address for the view, on another page if page is
// not 0.
func (lv lineupView) URL(page int) template.URL {
	if q := lv.values(page).Encode(); q != "" {
		return template.URL("/admin/lineup?" + q)
	}
	return "/admin/lineup"
}

// RulesURL returns the address of the lineup's rules (D107).
func (lv lineupView) RulesURL() template.URL {
	if lv.Lineup != 0 {
		return template.URL("/admin/lineup/rules?lineup=" + strconv.FormatInt(lv.Lineup, 10))
	}
	return "/admin/lineup/rules"
}

// GroupURL returns the address of a group's channels; -1 for all.
func (lv lineupView) GroupURL(id int64) template.URL {
	to := lineupView{Lineup: lv.Lineup, Page: 1}
	if id >= 0 {
		to.Group = &id
	}
	return to.URL(0)
}

// In returns a view of the start of the lineup, in the lineup it shows.
func (lv lineupView) In() lineupView { return lineupView{Lineup: lv.Lineup, Page: 1} }

// PanelURL returns the address of an entry's panel, for htmx to put beside
// the channels.
func (lv lineupView) PanelURL(id int64) template.URL {
	return template.URL(fmt.Sprintf("/admin/lineup/channels/%d/edit?%s", id, lv.Back()))
}

// EditURL returns the page's address with an entry open in the panel.
func (lv lineupView) EditURL(id int64) template.URL {
	v := lv.values(0)
	v.Set("edit", strconv.FormatInt(id, 10))
	return template.URL("/admin/lineup?" + v.Encode())
}

// CategoriesURL returns the address of the page that chooses the
// categories a group takes (D136).
func (lv lineupView) CategoriesURL(id int64) template.URL {
	u := "/admin/lineup/groups/" + strconv.FormatInt(id, 10) + "/categories"
	if lv.Lineup != 0 {
		u += "?lineup=" + strconv.FormatInt(lv.Lineup, 10)
	}
	return template.URL(u)
}

// IsGroup reports whether the view shows the group.
func (lv lineupView) IsGroup(id int64) bool { return lv.Group != nil && *lv.Group == id }

// lineupData is what the lineup page shows.
type lineupData struct {
	View      lineupView
	Groups    []store.LineupGroup // in the order devices see them
	ByName    []store.LineupGroup // the same, by name, for choosing one
	Selected  *store.LineupGroup  // the group shown, nil for all
	All       int                 // every channel
	Entries   []store.LineupEntry
	Total     int // the channels the view picks
	Pages     int
	Edit      int64     // the entry open in the panel
	Open      *entryRow // that entry, nil if none or not found
	CSRF      string
	homeGroup map[string]string // a source's category, "source/category": its group's name

	PINSet bool // the household has a parental PIN (D55)
	Locks  int  // locked groups, and channels locked one by one

	CanRepair     bool // channels set to be repaired go through ffmpeg (D57)
	Now           time.Time
	RepairDefault bool // channels are repaired unless they say otherwise (D58)

	// Channels in other groups whose names hold View.Add, to add to the
	// group shown: the first maxCandidates of Found.
	Candidates []store.LineupEntry
	Found      int

	// Lineups are every lineup, and Current the one shown (D106), which
	// the accounts of lineupSeen see.
	Lineups []store.LineupInfo
	Current store.LineupInfo
	lineupSeen
}

// maxCandidates is how many channels the search for channels to add lists.
const maxCandidates = 50

// Row returns what an entry's row, or its panel, needs.
func (d lineupData) Row(e store.LineupEntry) entryRow {
	return entryRow{E: e, View: d.View, CSRF: d.CSRF, Groups: d.ByName, Home: d.homeGroup[fmt.Sprint(e.SourceID, "/", e.Category)],
		Open: e.ID == d.Edit, InGroup: d.View.Group != nil, CanRepair: d.CanRepair, RepairDefault: d.RepairDefault, Now: d.Now}
}

// Shown counts the channels that devices see.
func (d lineupData) Shown() int {
	n := 0
	for _, g := range d.Groups {
		n += g.Shown
	}
	return n
}

// Prev and Next are the neighbouring pages, 0 if there is none.
func (d lineupData) Prev() int {
	if d.View.Page > 1 {
		return d.View.Page - 1
	}
	return 0
}

func (d lineupData) Next() int {
	if d.View.Page < d.Pages {
		return d.View.Page + 1
	}
	return 0
}

// First is the place of the first entry on the page, counting from 1.
func (d lineupData) First() int { return (d.View.Page-1)*pageEntries + 1 }

// Last is the place of the last entry on the page.
func (d lineupData) Last() int { return d.First() + len(d.Entries) - 1 }

// Deletable reports whether a group can be deleted: nothing is in it.
func (d lineupData) Deletable(g store.LineupGroup) bool {
	if g.Channels > 0 {
		return false
	}
	for _, c := range g.Categories {
		if c.ID != 0 {
			return false
		}
	}
	return true
}

// entryRow is what a channel's row, or its panel, shows.
type entryRow struct {
	E       store.LineupEntry
	View    lineupView
	CSRF    string
	Groups  []store.LineupGroup // by name
	Home    string              // the group of the entry's category, "" if none
	Open    bool                // the entry is open in the panel
	InGroup bool                // the view shows one group, or the channels in none

	CanRepair     bool
	RepairDefault bool
	Now           time.Time

	// Backups are, for the entry open in the panel, the channels whose
	// streams play when its own cannot start (D109), and RuleBackups those
	// that merge rules give it (D113).
	Backups, RuleBackups []store.EntryBackup

	// Guide is, for the entry open in the panel, where its programmes come
	// from (D139, D140), and Programmes what devices see on now and next.
	Guide      *store.ChannelGuide
	Programmes []store.Programme
	// Logo is, for the entry open in the panel, where its logo comes from
	// (D141).
	Logo *store.EntryLogoInfo
}

// LogoPath is where Overscan serves the entry's logo, "" if it has none.
func (r entryRow) LogoPath() string { return artwork.LogoPath(r.E.ID, r.E.Logo) }

// LogoChoice is how the entry's logo is set, as the edit form names it:
// "auto", "none" or "address".
func (r entryRow) LogoChoice() string {
	switch {
	case r.E.LogoByHand == nil:
		return "auto"
	case *r.E.LogoByHand == "":
		return "none"
	}
	return "address"
}

// LogoAddress is the address of the logo set by hand, "" if none is.
func (r entryRow) LogoAddress() string {
	if r.E.LogoByHand == nil {
		return ""
	}
	return *r.E.LogoByHand
}

// LogoText says where the entry's logo comes from.
func (r entryRow) LogoText() string {
	l := r.Logo
	if l == nil {
		return ""
	}
	fails := ""
	if l.OwnFails {
		fails = ", since its source's does not load"
	}
	switch l.From {
	case store.LogoFromHand:
		return "Set by hand, from its address."
	case store.LogoFromNone:
		return "None, set by hand: devices show its name."
	case store.LogoFromRule:
		return "From a rule."
	case store.LogoFromSource:
		return "From its source."
	case store.LogoFromGuide:
		return "From the guide " + l.Guide + fails + "."
	case store.LogoFromSourceGuide:
		return "From its source's guide" + fails + "."
	}
	if l.OwnFails {
		return "None: its source's does not load, and no guide has one. Devices show its name."
	}
	return "None: its source gives none, and no guide has one. Devices show its name."
}

// GuideText says where the entry's programmes come from, with what rules
// do to them; "" if the panel does not know.
func (r entryRow) GuideText() string {
	g := r.Guide
	if g == nil {
		return ""
	}
	text := guideFromText(*g)
	if r.E.RuleGuide != "" {
		if g.Chosen {
			text += " It wins over the rule that gives it the guide of " + r.E.RuleGuide + "."
		} else {
			text = "The guide of " + r.E.RuleGuide + ", which a rule gives it."
		}
	}
	if r.E.RuleShift != 0 {
		text += " A rule shows it " + shiftText(int(r.E.RuleShift/time.Minute)) + "."
	}
	return text
}

// GuideURL is the address of the page that chooses the entry's guide,
// which leads back to this panel.
func (r entryRow) GuideURL() string {
	return "/admin/guides/channels/" + strconv.FormatInt(r.E.ChannelID, 10) + "?" +
		url.Values{"back": {string(r.View.EditURL(r.E.ID))}}.Encode()
}

// FailedText says when the entry's stream last failed to start (D138).
func (r entryRow) FailedText() string {
	h := r.E.Health
	out := "Its stream failed to start " + ago(h.FailedAt, r.Now)
	if h.Failures > 1 {
		out += fmt.Sprintf(", %d times in a row", h.Failures)
	}
	return out
}

// StreamText says what the entry's stream held when it last played, as the
// hub read it (D138); "" if not known.
func (r entryRow) StreamText() string {
	h := r.E.Health
	out := h.Details.Summary()
	if out == "" {
		return ""
	}
	if fps := h.Details.FrameRate; fps > 0 {
		out += "; " + strconv.FormatFloat(fps, 'f', -1, 64) + " frames a second"
	}
	return out + "; seen " + ago(h.SeenAt, r.Now)
}

// MaxBackups is how many backups a channel can have.
func (entryRow) MaxBackups() int { return store.MaxBackups }

// AllBackups counts the channels that play when the entry's stream cannot
// start: those picked by hand and those that merge rules give it.
func (r entryRow) AllBackups() int { return r.E.Backups + r.E.RuleBackups }

// RepairChoice is the entry's own repair switch, as the edit form names
// it: "on", "off", or "" when it follows the default.
func (r entryRow) RepairChoice() string {
	switch {
	case r.E.RepairOwn == nil:
		return ""
	case *r.E.RepairOwn:
		return "on"
	}
	return "off"
}

// Visibility names the entry's visibility as the forms do.
func (r entryRow) Visibility() string { return visibilityNames[r.E.Visibility] }

// RuleText says what the lineup's rules do to the entry (D107), leaving out
// what the administrator's own edits undo; "" if nothing.
func (r entryRow) RuleText() string {
	e := r.E
	var did []string
	switch {
	case e.Visibility != store.AsGroup:
		if e.MergedInto != "" {
			did = append(did, "makes it a backup of "+e.MergedInto)
		}
	case e.RuleHidden && e.MergedInto != "":
		did = append(did, "hides it, as a copy of "+e.MergedInto+", which plays it when its own stream cannot start")
	case e.RuleHidden:
		did = append(did, "hides it")
	case e.RuleShown:
		did = append(did, "shows it, whatever its group")
	}
	switch {
	case e.RuleBackups == 1:
		did = append(did, "merges a copy of it into it, as its backup")
	case e.RuleBackups > 1:
		did = append(did, fmt.Sprintf("merges %d copies of it into it, as its backups", e.RuleBackups))
	}
	if e.RuleRenamed && !e.Renamed {
		did = append(did, "names it")
	}
	if e.RuleMoved && !e.Moved {
		did = append(did, "moves it to its group")
	}
	if e.RuleNumbered {
		did = append(did, "numbers it")
	}
	if e.RuleLogo {
		did = append(did, "gives it its logo")
	}
	if e.RuleGuide != "" {
		did = append(did, "gives it the guide of "+e.RuleGuide)
	}
	if e.RuleShift != 0 {
		did = append(did, "shows its guide "+shiftText(int(e.RuleShift/time.Minute)))
	}
	if len(did) == 0 {
		return ""
	}
	return "A rule " + andList(did) + "."
}

// lineupData reads what the page shows; with rows false, only what a row
// needs.
func (a *Admin) lineupData(ctx context.Context, lv lineupView, csrf string, rows bool) (lineupData, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return lineupData{}, err
	}
	groups, err := a.store.LineupGroups(ctx, lineup)
	if err != nil {
		return lineupData{}, err
	}
	d := lineupData{View: lv, Groups: groups, CSRF: csrf, homeGroup: map[string]string{}, CanRepair: a.canRepair(), Now: a.now()}
	if d.RepairDefault, err = a.store.RepairDefault(ctx); err != nil {
		return lineupData{}, err
	}
	if d.Lineups, err = a.store.Lineups(ctx); err != nil {
		return lineupData{}, err
	}
	for _, l := range d.Lineups {
		if l.ID == lineup {
			d.Current = l
		}
	}
	if d.lineupSeen, err = a.lineupSeen(ctx, lineup); err != nil {
		return lineupData{}, err
	}
	for i, g := range groups {
		if lv.IsGroup(g.ID) {
			d.Selected = &groups[i]
		}
		if g.ID == 0 {
			continue
		}
		d.ByName = append(d.ByName, g)
		for _, c := range g.Categories {
			d.homeGroup[fmt.Sprint(c.SourceID, "/", c.Name)] = g.Name
		}
	}
	slices.SortFunc(d.ByName, func(x, y store.LineupGroup) int {
		return strings.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
	})
	if !rows {
		return d, nil
	}
	if d.PINSet, err = a.pinSet(ctx); err != nil {
		return lineupData{}, err
	}
	if d.Locks, err = a.lockCount(ctx); err != nil {
		return lineupData{}, err
	}
	if _, d.All, err = a.store.LineupEntries(ctx, lineup, store.EntryQuery{Limit: 1}); err != nil {
		return lineupData{}, err
	}
	if lv.Add != "" && d.Selected != nil && d.Selected.ID > 0 {
		// In a lineup of its own groups, those it does not hold too (D136).
		q := store.EntryQuery{NotGroup: lv.Group, Text: lv.Add, Limit: maxCandidates, Untaken: true}
		if d.Candidates, d.Found, err = a.store.LineupEntries(ctx, lineup, q); err != nil {
			return lineupData{}, err
		}
	}
	q := store.EntryQuery{Group: lv.Group, Text: lv.Query, Offset: (lv.Page - 1) * pageEntries, Limit: pageEntries}
	if d.Entries, d.Total, err = a.store.LineupEntries(ctx, lineup, q); err != nil {
		return lineupData{}, err
	}
	d.Pages = max(1, (d.Total+pageEntries-1)/pageEntries)
	if lv.Page > d.Pages {
		// Past the end, which a change can leave the view: the last page.
		d.View.Page = d.Pages
		q.Offset = (d.Pages - 1) * pageEntries
		if d.Entries, d.Total, err = a.store.LineupEntries(ctx, lineup, q); err != nil {
			return lineupData{}, err
		}
	}
	return d, nil
}

// lineupPage shows the lineup.
func (a *Admin) lineupPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	lv := parseLineupView(r.URL.Query())
	edit, _ := strconv.ParseInt(r.URL.Query().Get("edit"), 10, 64)
	a.showLineup(w, r, s, http.StatusOK, lv, edit, view{})
}

func (a *Admin) showLineup(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, lv lineupView, edit int64, v view) {
	lv.Page = max(1, lv.Page)
	data, err := a.lineupData(r.Context(), lv, s.CSRFToken, true)
	if err != nil {
		a.log.Error("cannot show the lineup", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	data.Edit = edit
	if edit != 0 {
		if err := a.openEntry(r.Context(), &data); err != nil {
			a.log.Error("cannot show the lineup", "error", err)
			a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
			return
		}
	}
	if r.Method == http.MethodPost && r.Header.Get("HX-Request") == "true" && status == http.StatusOK {
		// After a change, the address follows the view, which the change
		// may have moved, without adding to the browser's history.
		w.Header().Set("HX-Replace-Url", string(data.View.URL(0)))
	}
	title := "Live TV"
	if data.Selected != nil {
		title = data.Selected.Name + " · Live TV"
	}
	v.Title, v.Section, v.Session, v.Data = title, "lineup", &s, data
	a.render(w, r, status, "lineup", v)
}

// openEntry puts the entry d.Edit in the panel, if the lineup has it.
func (a *Admin) openEntry(ctx context.Context, d *lineupData) error {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return err
	}
	e, err := a.store.LineupEntry(ctx, lineup, d.Edit)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	row := d.Row(e)
	if row.Backups, err = a.store.EntryBackups(ctx, lineup, e.ID); err != nil {
		return err
	}
	if row.RuleBackups, err = a.store.EntryRuleBackups(ctx, lineup, e.ID); err != nil {
		return err
	}
	g, err := a.store.ChannelGuideOf(ctx, e.ChannelID)
	if err != nil {
		return err
	}
	row.Guide = &g
	logo, err := a.store.EntryLogoInfo(ctx, lineup, e.ID)
	if err != nil {
		return err
	}
	row.Logo = &logo
	if row.Programmes, err = firstTwo(a.store.EntryProgrammes(ctx, lineup, e.ID, d.Now, d.Now.Add(programmesAhead))); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return err
	}
	d.Open = &row
	return nil
}

// entryPanel serves a channel's panel, for htmx to put beside the
// channels.
func (a *Admin) entryPanel(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	data, err := a.lineupData(r.Context(), parseLineupView(r.URL.Query()), s.CSRFToken, false)
	if err == nil {
		data.Edit = id
		err = a.openEntry(r.Context(), &data)
	}
	if err != nil {
		a.log.Error("cannot show a channel of the lineup", "error", err)
		http.Error(w, "Something went wrong; the server's log says what.", http.StatusInternalServerError)
		return
	}
	if data.Open == nil {
		a.notFound(w, r)
		return
	}
	a.fragment(w, r, http.StatusOK, "lineup", "entry-panel-fragment", view{Session: &s, Data: *data.Open})
}

// lineupDone finishes a change from the lineup page: a browser without htmx
// goes back to the view; with htmx, the page comes back at once with what
// was done, or what was wrong.
func (a *Admin) lineupDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string, edit int64) {
	a.lineupDoneAt(w, r, s, parseLineupView(backValues(r)), err, notice, edit)
}

// lineupDoneAt is lineupDone for a change that moves the page to another
// view.
func (a *Admin) lineupDoneAt(w http.ResponseWriter, r *http.Request, s store.AdminSession, lv lineupView, err error, notice string, edit int64) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showLineup(w, r, s, bad.status, lv, edit, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change the lineup", "error", err)
		a.showLineup(w, r, s, http.StatusInternalServerError, lv, edit, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showLineup(w, r, s, http.StatusOK, lv, 0, view{Notice: notice})
	default:
		http.Redirect(w, r, string(lv.URL(0)), http.StatusSeeOther)
	}
}

// backValues reads the view that a form came from.
func backValues(r *http.Request) url.Values {
	v, err := url.ParseQuery(r.PostFormValue("back"))
	if err != nil {
		return url.Values{}
	}
	return v
}

// formInt reads a whole number from a form field; ok is false if the field
// is there but is not one.
func formInt(r *http.Request, name string) (n int64, present, ok bool) {
	v := strings.TrimSpace(r.PostFormValue(name))
	if v == "" {
		return 0, false, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, true, err == nil
}

// saveEntry saves a channel's edit row.
func (a *Admin) saveEntry(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		a.lineupDone(w, r, s, err, "", id)
		return
	}
	e, err := a.store.LineupEntry(ctx, lineup, id)
	if err != nil {
		a.lineupDone(w, r, s, lineupStoreError(err, "channel"), "", 0)
		return
	}

	var ch entryChange
	name := strings.TrimSpace(r.PostFormValue("name"))
	if name == e.ChannelName {
		name = ""
	}
	if (name == "" && e.Renamed) || (name != "" && name != e.Name) {
		ch.Name = &name
	}
	group, present, ok := formInt(r, "group")
	if !ok || group < 0 {
		a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_group", "Choose a group."), "", id)
		return
	}
	if !present {
		group = 0
	}
	if (group == 0 && e.Moved) || (group != 0 && (!e.Moved || group != e.GroupID)) {
		ch.Group = &group
	}
	if v := r.PostFormValue("visibility"); v != "" {
		vis, err := parseVisibility(v)
		if err != nil {
			a.lineupDone(w, r, s, err, "", id)
			return
		}
		if vis != e.Visibility {
			ch.Visibility = &vis
		}
	}
	if locked := r.PostFormValue("locked") != ""; locked != e.Locked {
		ch.Locked = &locked
	}
	if repair, ok := repairChoice(r.PostFormValue("repair")); !ok {
		a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_repair", "Choose how its stream is repaired."), "", id)
		return
	} else if !sameChoice(repair, e.RepairOwn) {
		ch.Repair, ch.RepairSet = repair, true
	}
	number, present, ok := formInt(r, "number")
	pin := r.PostFormValue("pinned") != ""
	switch {
	case !ok || number < 0 || number > store.MaxNumber:
		a.lineupDone(w, r, s, lineupStoreError(store.ErrBadNumber, ""), "", id)
		return
	case !present || number == 0:
		if e.Pinned {
			reset := 0
			ch.Number = &reset
		}
	case int(number) != e.Number || (pin && !e.Pinned):
		n := int(number)
		ch.Number = &n
	case !pin && e.Pinned:
		reset := 0
		ch.Number = &reset
	}
	switch r.PostFormValue("logo_choice") {
	case "": // a form without the logo leaves it as it is
	case "auto":
		ch.LogoSet = e.LogoByHand != nil
	case "none":
		if e.LogoByHand == nil || *e.LogoByHand != "" {
			none := ""
			ch.Logo, ch.LogoSet = &none, true
		}
	case "address":
		logo := strings.TrimSpace(r.PostFormValue("logo"))
		if logo == "" {
			a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_logo", "Enter the logo's address, or choose another logo."), "", id)
			return
		}
		if e.LogoByHand == nil || *e.LogoByHand != logo {
			ch.Logo, ch.LogoSet = &logo, true
		}
	default:
		a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_logo", "Choose its logo."), "", id)
		return
	}

	if ch == (entryChange{}) {
		a.lineupDone(w, r, s, nil, "Nothing to change.", 0)
		return
	}
	err = a.changeEntries(ctx, []int64{id}, ch)
	a.lineupDone(w, r, s, err, "Saved "+e.ChannelName+".", id)
}

// changeSome serves the buttons for the ticked channels, and the quick
// buttons of a row.
func (a *Admin) changeSome(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	if err := r.ParseForm(); err != nil {
		a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_form", "The form could not be read."), "", 0)
		return
	}
	var ids []int64
	for _, v := range r.PostForm["ids"] {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_form", "The form could not be read."), "", 0)
			return
		}
		ids = append(ids, id)
	}
	var ch entryChange
	var done string
	n := fmt.Sprintf("%d %s", len(ids), plural(len(ids), "channel"))
	switch r.PostFormValue("action") {
	case "hide", "show", "group":
		vis, _ := parseVisibility(map[string]string{"hide": "hidden", "show": "shown", "group": "group"}[r.PostFormValue("action")])
		ch.Visibility = &vis
		done = map[store.Visibility]string{store.Hidden: "Hid ", store.Shown: "Showed ", store.AsGroup: "Set as their group: "}[vis] + n + "."
	case "lock", "unlock":
		locked := r.PostFormValue("action") == "lock"
		ch.Locked = &locked
		done = "Locked " + n + "."
		if !locked {
			done = "Unlocked " + n + ". Those in a locked group still need the PIN."
		}
	case "repair", "norepair", "repairdefault":
		repair, _ := repairChoice(map[string]string{"repair": "on", "norepair": "off"}[r.PostFormValue("action")])
		ch.Repair, ch.RepairSet = repair, true
		switch {
		case repair == nil:
			done = "Set " + n + " to be repaired as the default says."
		case *repair:
			done = "Set " + n + " to be repaired."
		default:
			done = "Set " + n + " to play as they are."
		}
	case "move":
		// The bar's list starts on "Move to…", which sends no group.
		group, present, ok := formInt(r, "group")
		if !ok || !present || group < 0 {
			a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_group", "Choose a group."), "", 0)
			return
		}
		ch.Group = &group
		done = "Moved " + n + "."
		if group == 0 {
			done = "Put " + n + " back in their categories' groups."
		}
	default:
		a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_form", "Choose what to do with the ticked channels."), "", 0)
		return
	}
	a.lineupDone(w, r, s, a.changeEntries(r.Context(), ids, ch), done, 0)
}

// groupAction serves the forms about a group.
func (a *Admin) groupAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	lv := parseLineupView(backValues(r))
	var done string
	switch r.PathValue("action") {
	case "rename":
		name := strings.TrimSpace(r.PostFormValue("name"))
		if err = a.renameGroup(ctx, id, name); err == nil {
			done = "Renamed the group " + name + "."
		}
	case "hide":
		err, done = a.hideGroup(ctx, id, true), "Hid the group. Its channels are hidden too, except those set to be shown."
	case "show":
		err, done = a.hideGroup(ctx, id, false), "Showed the group."
	case "lock":
		err, done = a.lockGroup(ctx, id, true), "Locked the group: its channels need the PIN."
	case "unlock":
		err, done = a.lockGroup(ctx, id, false), "Unlocked the group. Channels locked one by one stay locked."
	case "merge":
		into, _, ok := formInt(r, "into")
		if !ok || into <= 0 {
			err = badEdit(http.StatusBadRequest, "bad_group", "Choose the group to merge into.")
			break
		}
		if err = a.mergeGroup(ctx, id, into); err == nil {
			done = "Merged the group."
			lv = lineupView{Lineup: lv.Lineup, Group: &into, Page: 1}
		}
	case "number":
		from, _, ok := formInt(r, "from")
		if !ok || from < 1 || from > store.MaxNumber {
			err = lineupStoreError(store.ErrBadNumber, "")
			break
		}
		var n int
		if n, err = a.numberGroup(ctx, id, int(from)); err == nil {
			done = fmt.Sprintf("Numbered %d %s from %d.", n, plural(n, "channel"), from)
		}
	case "numbers-from":
		var from int
		if from, err = numbersFromForm(r); err != nil {
			break
		}
		if err = a.setNumbersFrom(ctx, id, from); err == nil {
			done = fmt.Sprintf("The group's channels are numbered from %d, and a channel that joins it takes the next free number after its last.", from)
			if from == 0 {
				done = "The group has no first number now: its channels keep their numbers, and one that joins it is numbered as the source numbers it."
			}
		}
	case "sort":
		var n int
		if n, err = a.sortGroup(ctx, id); err == nil {
			done = fmt.Sprintf("Sorted the group's channels by name: %d %s moved.", n, plural(n, "channel"))
		}
	case "delete":
		if err = a.deleteGroup(ctx, id); err == nil {
			done = "Deleted the group."
			lv = lv.In()
		}
	default:
		a.notFound(w, r)
		return
	}
	a.lineupDoneAt(w, r, s, lv, err, done, 0)
}

// createGroupForm makes a group from the lineup page, and shows it.
func (a *Admin) createGroupForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	lv := parseLineupView(backValues(r))
	id, err := a.createGroup(r.Context(), r.PostFormValue("name"))
	if err == nil {
		lv = lineupView{Lineup: lv.Lineup, Group: &id, Page: 1}
	}
	a.lineupDoneAt(w, r, s, lv, err, "Made the group. Find channels under Add channels to put them in it, or move a category here from its group.", 0)
}

// moveCategoryForm puts a source's category in another group.
func (a *Admin) moveCategoryForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	category, _, ok1 := formInt(r, "category")
	group, _, ok2 := formInt(r, "group")
	if !ok1 || !ok2 || category <= 0 || group <= 0 {
		a.lineupDone(w, r, s, badEdit(http.StatusBadRequest, "bad_group", "Choose a category and a group."), "", 0)
		return
	}
	a.lineupDone(w, r, s, a.moveCategory(r.Context(), category, group), "Moved the category, with its channels.", 0)
}
