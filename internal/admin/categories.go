// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// What a source brings (D135): each source's page of categories, of its
// live channels, its films and its series, and the API's
// sources/{id}/categories/{kind}. A source brings every category, as
// before, or only those ticked. Saving reads the source again, so that what
// it brings changes at once; the API's PUT only saves, and
// sources/{id}/categories/{kind}/read reads.

// Lists builds the library's lists again; the library does.
type Lists interface {
	// ReadList reads a source's list of films or of series now, and waits
	// for it until ctx is done.
	ReadList(ctx context.Context, sourceID int64, kind string) error
}

// UseLists gives the admin what reads the sources' lists of films and of
// series, which a change to the categories that a source brings needs.
func (a *Admin) UseLists(l Lists) { a.lists = l }

// maxCategoriesFormBytes bounds the form that saves a source's ticks: a
// thousand categories, each with a name of up to 200 bytes, sent twice.
const maxCategoriesFormBytes = 1 << 20

// listWait is how long the admin waits for a read of a source's films or
// series before it says that the read goes on.
var listWait = 20 * time.Second

// categoryKind is a kind of category, as the API's paths and the page's
// addresses name it.
type categoryKind struct {
	Path   string // live, movies or series
	Kind   string // the store's kind
	Tab    string // as the page's tabs name it
	Noun   string // what its categories hold, one
	Nouns  string // and several
	Screen string // the box's screen that shows them
}

var categoryKinds = []categoryKind{
	{Path: "live", Kind: store.KindLive, Tab: "Live TV", Noun: "channel", Nouns: "channels", Screen: "Live TV"},
	{Path: "movies", Kind: store.KindMovie, Tab: "Films", Noun: "film", Nouns: "films", Screen: "Movies"},
	{Path: "series", Kind: store.KindSeries, Tab: "Series", Noun: "series", Nouns: "series", Screen: "Series"},
}

// sourceKinds returns the kinds of category that a source has: live
// channels for one that lists them, films and series for one that has
// them; none for a folder, which is one category of its own.
func sourceKinds(src store.Source) []categoryKind {
	settings := src.Settings()
	if settings.Type == source.TypeFolder {
		return nil
	}
	var out []categoryKind
	for _, k := range categoryKinds {
		if k.Kind == store.KindLive && settings.HasChannels() || k.Kind != store.KindLive && settings.HasLibrary() {
			out = append(out, k)
		}
	}
	return out
}

// kindOf returns the kind of category that a source has by its path.
func kindOf(src store.Source, path string) (categoryKind, bool) {
	for _, k := range sourceKinds(src) {
		if k.Path == path {
			return k, true
		}
	}
	return categoryKind{}, false
}

var errNoCategories = badEdit(http.StatusNotFound, "not_found", "The source has no such categories.")

// bringSummary sums up what a source brings of a kind of category.
type bringSummary struct {
	Kind       categoryKind
	OnlyTicked bool
	Listed     int // the categories that its reads listed
	Ticked     int
}

// Text says it, as the source's card does.
func (b bringSummary) Text() string {
	switch {
	case !b.OnlyTicked:
		return b.Kind.Tab + ": every category"
	case b.Listed == 0:
		return b.Kind.Tab + ": the ticked categories, once a read lists them"
	}
	word := "categories"
	if b.Listed == 1 {
		word = "category"
	}
	return fmt.Sprintf("%s: %s of %s %s", b.Kind.Tab, count(b.Ticked), count(b.Listed), word)
}

// sourceBrings returns what each source brings, by source, for the kinds
// of category that it has.
func (a *Admin) sourceBrings(ctx context.Context, srcs []store.Source) (map[int64][]bringSummary, error) {
	choices, err := a.store.CategoryChoices(ctx)
	if err != nil {
		return nil, err
	}
	counts, err := a.store.CategoryCounts(ctx)
	if err != nil {
		return nil, err
	}
	out := map[int64][]bringSummary{}
	for _, src := range srcs {
		for _, k := range sourceKinds(src) {
			n := counts[src.ID][k.Kind]
			out[src.ID] = append(out[src.ID], bringSummary{Kind: k, OnlyTicked: choices[src.ID][k.Kind].OnlyTicked,
				Listed: n.Listed, Ticked: n.Ticked})
		}
	}
	return out, nil
}

// The API.

// apiCategory is a category that a source's reads listed.
type apiCategory struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	// Items is how many channels or titles it held at the last read that
	// listed it; null if that is not known.
	Items    *int   `json:"items"`
	Ticked   bool   `json:"ticked"`
	ListedAt string `json:"listed_at"`
}

// apiCategories is what a source brings of a kind of category.
type apiCategories struct {
	Kind       string  `json:"kind"`
	OnlyTicked bool    `json:"only_ticked"`
	TickNew    bool    `json:"tick_new"`
	ChangedAt  *string `json:"changed_at"`
	// ReadAt is when a read of the kind last listed its categories, and
	// Pending whether the choice or a tick changed since then.
	ReadAt     *string       `json:"read_at"`
	Pending    bool          `json:"pending"`
	Total      int           `json:"total"`  // the categories that match, those beyond the list's end too
	Ticked     int           `json:"ticked"` // of every category, the ticked ones
	Categories []apiCategory `json:"categories"`
}

// categoriesOf returns what a source brings of a kind, its categories
// whose names hold text.
func (a *Admin) categoriesOf(ctx context.Context, src store.Source, k categoryKind, text string) (apiCategories, error) {
	out := apiCategories{Kind: k.Path, Categories: []apiCategory{}}
	choice, err := a.store.CategoryChoice(ctx, src.ID, k.Kind)
	if err != nil {
		return out, err
	}
	counts, err := a.store.CategoryCounts(ctx)
	if err != nil {
		return out, err
	}
	cats, total, err := a.store.SourceCategories(ctx, src.ID, k.Kind, text)
	if err != nil {
		return out, err
	}
	n := counts[src.ID][k.Kind]
	out.OnlyTicked, out.TickNew, out.Total, out.Ticked = choice.OnlyTicked, choice.TickNew, total, n.Ticked
	out.ChangedAt, out.ReadAt = timeText(choice.ChangedAt), timeText(n.LastRead)
	out.Pending = !n.LastRead.IsZero() && choice.ChangedAt.After(n.LastRead)
	for _, c := range cats {
		ac := apiCategory{Key: c.Key, Name: c.Name, Ticked: c.Ticked, ListedAt: c.ListedAt.UTC().Format("2006-01-02T15:04:05Z")}
		if c.Items >= 0 {
			items := c.Items
			ac.Items = &items
		}
		out.Categories = append(out.Categories, ac)
	}
	return out, nil
}

// timeText is a time as the API gives it, nil for the zero time.
func timeText(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format("2006-01-02T15:04:05Z")
	return &s
}

// sourceKind returns the source and the kind of category that the path
// names.
func (a *Admin) sourceKind(r *http.Request) (store.Source, categoryKind, error) {
	id, err := pathID(r)
	if err != nil {
		return store.Source{}, categoryKind{}, err
	}
	src, err := a.store.Source(r.Context(), id)
	if err != nil {
		return store.Source{}, categoryKind{}, sourceError(err)
	}
	k, ok := kindOf(src, r.PathValue("kind"))
	if !ok {
		return store.Source{}, categoryKind{}, errNoCategories
	}
	return src, k, nil
}

func (a *Admin) apiCategories(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	src, k, err := a.sourceKind(r)
	if a.editAnswer(w, err) {
		return
	}
	out, err := a.categoriesOf(r.Context(), src, k, r.URL.Query().Get("q"))
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// apiCategoriesChange changes what a source brings of a kind: what it
// leaves out stays as it is.
type apiCategoriesChange struct {
	OnlyTicked *bool           `json:"only_ticked"`
	TickNew    *bool           `json:"tick_new"`
	Ticked     map[string]bool `json:"ticked"`
}

func (a *Admin) apiSetCategories(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	src, k, err := a.sourceKind(r)
	if a.editAnswer(w, err) {
		return
	}
	var c apiCategoriesChange
	if !readJSONWithin(w, r, &c, maxCategoriesFormBytes) {
		return
	}
	if _, err := a.changeCategories(r.Context(), src, k, c); a.editAnswer(w, err) {
		return
	}
	out, err := a.categoriesOf(r.Context(), src, k, "")
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// changeCategories saves a change to what a source brings of a kind, and
// reports whether it changes what the source brings at its next read: the
// choice of every category or the ticked ones, or a tick while it brings
// the ticked ones. Whether to tick what it adds later changes nothing
// until it adds a category.
func (a *Admin) changeCategories(ctx context.Context, src store.Source, k categoryKind, c apiCategoriesChange) (bool, error) {
	now := a.now()
	choice, err := a.store.CategoryChoice(ctx, src.ID, k.Kind)
	if err != nil {
		return false, err
	}
	changed, was := false, choice.OnlyTicked
	if c.OnlyTicked != nil || c.TickNew != nil {
		if c.OnlyTicked != nil {
			choice.OnlyTicked = *c.OnlyTicked
		}
		if c.TickNew != nil {
			choice.TickNew = *c.TickNew
		}
		if _, err = a.store.SetCategoryChoice(ctx, src.ID, k.Kind, choice.OnlyTicked, choice.TickNew, now); err != nil {
			return false, sourceError(err)
		}
		changed = choice.OnlyTicked != was
	}
	if len(c.Ticked) > 0 {
		n, err := a.store.TickCategories(ctx, src.ID, k.Kind, c.Ticked, now)
		if err != nil {
			return false, sourceError(err)
		}
		// Ticks count only while it brings only the ticked categories.
		changed = changed || n > 0 && choice.OnlyTicked
	}
	if changed {
		a.log.Info("categories that a source brings changed in the admin", "source", src.Name, "kind", k.Path)
	}
	return changed, nil
}

// apiCategoriesRead is what a read of a source's categories did.
type apiCategoriesRead struct {
	// Reading is set when a read of films or series goes on after the
	// answer.
	Reading bool `json:"reading"`
	// For a read of live channels, as sources/{id}/refresh says it.
	*apiRead
}

func (a *Admin) apiReadCategories(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	src, k, err := a.sourceKind(r)
	if a.editAnswer(w, err) {
		return
	}
	ch, reading, err := a.readCategories(r.Context(), src, k)
	if a.editAnswer(w, err) {
		return
	}
	if k.Kind == store.KindLive {
		writeJSON(w, http.StatusOK, apiCategoriesRead{apiRead: readBody(ch)})
		return
	}
	status := http.StatusOK
	if reading {
		status = http.StatusAccepted
	}
	writeJSON(w, status, apiCategoriesRead{Reading: reading})
}

// readCategories reads a source's channels, films or series now, so that
// what it brings of them changes. A read of films or series that takes
// longer than listWait goes on after it returns, reading set.
func (a *Admin) readCategories(ctx context.Context, src store.Source, k categoryKind) (ch store.CatalogChanges, reading bool, err error) {
	if !src.Enabled {
		return ch, false, badEdit(http.StatusConflict, "source_off", "The source is off; turn it on to read it.")
	}
	if k.Kind == store.KindLive {
		ch, err = a.refreshSource(ctx, src.ID)
		return ch, false, err
	}
	if a.lists == nil {
		return ch, false, errNoReader
	}
	wait, cancel := context.WithTimeout(ctx, listWait)
	defer cancel()
	err = a.lists.ReadList(wait, src.ID, k.Kind)
	switch {
	case err == nil:
		return ch, false, nil
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil:
		return ch, true, nil
	}
	return ch, false, readError(err)
}

// The page.

// categoryRow is a category on the page.
type categoryRow struct {
	store.SourceCategory
	Kind categoryKind
	// Stale is set when the last read did not list it: it is kept for a
	// while, in case it comes back.
	Stale bool
}

// Label names the category, or the channels or titles without one.
func (c categoryRow) Label() string {
	if c.Key == "" {
		return "Without a category"
	}
	return c.Name
}

// ItemsText says what the category held at the last read that listed it.
func (c categoryRow) ItemsText() string {
	if c.Items < 0 {
		return ""
	}
	if c.Kind.Noun == "series" {
		return count(c.Items) + " series"
	}
	return count(c.Items) + " " + plural(c.Items, c.Kind.Noun)
}

// categoriesData is what the page of a source's categories shows.
type categoriesData struct {
	Source     sourceRow
	Kinds      []bringSummary // the kinds of category it has, as tabs
	Kind       categoryKind   // the kind shown
	Choice     store.CategoryChoice
	Categories []categoryRow
	Total      int // the categories listed
	Ticked     int // of those, the ticked ones
	Kept       int // the channels or titles of the ticked categories at the last read
	All        int // the channels or titles of every category at the last read
	Known      bool
	LastRead   time.Time
	Pending    bool
	More       bool // more categories than the page shows
	Now        time.Time
}

// URL is the page's address for a kind of category.
func (d categoriesData) URL(k categoryKind) string {
	return "/admin/sources/" + strconv.FormatInt(d.Source.ID, 10) + "/categories?kind=" + k.Path
}

// Action is where the page's forms go.
func (d categoriesData) Action() string {
	return "/admin/sources/" + strconv.FormatInt(d.Source.ID, 10) + "/categories"
}

// KeptText says what the ticked categories held at the last read.
func (d categoriesData) KeptText() string {
	if !d.Known {
		return ""
	}
	nouns := d.Kind.Nouns
	if d.All == 1 && d.Kind.Noun != "series" {
		nouns = d.Kind.Noun
	}
	if !d.Choice.OnlyTicked {
		return fmt.Sprintf("It brings all %s %s.", count(d.All), nouns)
	}
	return fmt.Sprintf("The ticked categories held %s of its %s %s at the last read.", count(d.Kept), count(d.All), nouns)
}

// categoriesPage shows the categories of a source, of the kind that the
// address names, or of its first.
func (a *Admin) categoriesPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showCategories(w, r, s, http.StatusOK, r.URL.Query().Get("kind"), view{})
}

func (a *Admin) showCategories(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, path string, v view) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	src, err := a.store.Source(ctx, id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	kinds := sourceKinds(src)
	if len(kinds) == 0 {
		a.notFound(w, r)
		return
	}
	k, ok := kindOf(src, path)
	if !ok {
		k = kinds[0]
	}
	data, err := a.categoriesData(ctx, src, k)
	if err != nil {
		a.log.Error("cannot show a source's categories", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "What "+src.Name+" brings · Sources", "sources", &s, data
	a.render(w, r, status, "categories", v)
}

func (a *Admin) categoriesData(ctx context.Context, src store.Source, k categoryKind) (categoriesData, error) {
	data := categoriesData{Source: sourceRow{Source: src, Now: a.now()}, Kind: k, Now: a.now()}
	brings, err := a.sourceBrings(ctx, []store.Source{src})
	if err != nil {
		return data, err
	}
	data.Kinds = brings[src.ID]
	if data.Choice, err = a.store.CategoryChoice(ctx, src.ID, k.Kind); err != nil {
		return data, err
	}
	cats, total, err := a.store.SourceCategories(ctx, src.ID, k.Kind, "")
	if err != nil {
		return data, err
	}
	data.Total, data.More = total, total > len(cats)
	for _, c := range cats {
		if c.ListedAt.After(data.LastRead) {
			data.LastRead = c.ListedAt
		}
	}
	data.Known = true
	for _, c := range cats {
		row := categoryRow{SourceCategory: c, Kind: k, Stale: c.ListedAt.Before(data.LastRead)}
		data.Categories = append(data.Categories, row)
		if c.Ticked {
			data.Ticked++
		}
		if row.Stale {
			continue
		}
		if c.Items < 0 {
			data.Known = false
			continue
		}
		data.All += c.Items
		if c.Ticked {
			data.Kept += c.Items
		}
	}
	data.Pending = !data.LastRead.IsZero() && data.Choice.ChangedAt.After(data.LastRead)
	return data, nil
}

// categoriesForm saves what a source brings of a kind of category from the
// page, and reads the source again so that it changes at once.
func (a *Admin) categoriesForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	src, err := a.store.Source(ctx, id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	path := r.PostFormValue("kind")
	k, ok := kindOf(src, path)
	if !ok {
		a.notFound(w, r)
		return
	}
	only := r.PostFormValue("bring") == "ticked"
	tickNew := r.PostFormValue("tick_new") == "1"
	ticks := map[string]bool{}
	for _, key := range r.PostForm["listed"] {
		ticks[key] = false
	}
	for _, key := range r.PostForm["tick"] {
		if _, ok := ticks[key]; ok {
			ticks[key] = true
		}
	}
	changed, err := a.changeCategories(ctx, src, k, apiCategoriesChange{OnlyTicked: &only, TickNew: &tickNew, Ticked: ticks})
	if err != nil {
		a.categoriesDone(w, r, s, path, err, "")
		return
	}
	if !changed {
		a.categoriesDone(w, r, s, path, nil, "Saved.")
		return
	}
	a.applyCategories(w, r, s, src, k, "Saved")
}

// applyCategories reads a source again after a change to what it brings,
// and says what came of it.
func (a *Admin) applyCategories(w http.ResponseWriter, r *http.Request, s store.AdminSession, src store.Source, k categoryKind, saved string) {
	if !src.Enabled {
		a.categoriesDone(w, r, s, k.Path, nil, saved+". It takes effect once "+src.Name+" is on and read again.")
		return
	}
	counts, err := a.store.CategoryCounts(r.Context())
	if err != nil {
		a.categoriesDone(w, r, s, k.Path, err, "")
		return
	}
	if k.Kind != store.KindLive && counts[src.ID][k.Kind].Listed == 0 {
		a.categoriesDone(w, r, s, k.Path, nil, fmt.Sprintf("%s. It takes effect when %s's %s are first read: when a box first opens %s.",
			saved, src.Name, k.Nouns, k.Screen))
		return
	}
	ch, reading, err := a.readCategories(r.Context(), src, k)
	switch {
	case err != nil && isEditError(err) && saved != "":
		var bad *editError
		errors.As(err, &bad)
		a.categoriesDone(w, r, s, k.Path, badEdit(bad.status, bad.code, "%s, but reading %s again failed, so it takes effect at its next read. %s",
			saved, src.Name, bad.text), "")
	case err != nil:
		a.categoriesDone(w, r, s, k.Path, err, "")
	case k.Kind == store.KindLive:
		n := ch.Added + ch.Updated
		done := fmt.Sprintf("%s, and read %s again: it brings %s %s", saved, src.Name, count(n), plural(n, "channel"))
		if ch.LeftOut > 0 {
			done += fmt.Sprintf(", and left out %s", count(ch.LeftOut))
		}
		if ch.Missing > 0 {
			done += fmt.Sprintf(". %s it no longer brings %s kept for a week, in case you tick their categories again",
				count(ch.Missing), map[bool]string{true: "is", false: "are"}[ch.Missing == 1])
		}
		a.categoriesDone(w, r, s, k.Path, nil, done+".")
	case reading:
		a.categoriesDone(w, r, s, k.Path, nil, fmt.Sprintf("%s. Overscan is reading %s's %s again, which takes a while; this page shows the result once it is done.",
			saved, src.Name, k.Nouns))
	default:
		a.categoriesDone(w, r, s, k.Path, nil, fmt.Sprintf("%s, and read %s's %s again.", saved, src.Name, k.Nouns))
	}
}

// categoriesRead reads a source's channels, films or series now, from the
// page.
func (a *Admin) categoriesRead(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		a.notFound(w, r)
		return
	}
	src, err := a.store.Source(r.Context(), id)
	if err != nil {
		a.notFound(w, r)
		return
	}
	k, ok := kindOf(src, r.PostFormValue("kind"))
	if !ok {
		a.notFound(w, r)
		return
	}
	if !src.Enabled {
		a.categoriesDone(w, r, s, k.Path, badEdit(http.StatusConflict, "source_off", "%s is off; turn it on to read it.", src.Name), "")
		return
	}
	ch, reading, err := a.readCategories(r.Context(), src, k)
	switch {
	case err != nil:
		a.categoriesDone(w, r, s, k.Path, err, "")
	case k.Kind == store.KindLive:
		n := ch.Added + ch.Updated
		done := fmt.Sprintf("Read %s: it brings %s %s", src.Name, count(n), plural(n, "channel"))
		if ch.LeftOut > 0 {
			done += fmt.Sprintf(", and left out %s", count(ch.LeftOut))
		}
		a.categoriesDone(w, r, s, k.Path, nil, done+".")
	case reading:
		a.categoriesDone(w, r, s, k.Path, nil, fmt.Sprintf("Overscan is reading %s's %s, which takes a while; this page shows their categories once it is done.",
			src.Name, k.Nouns))
	default:
		a.categoriesDone(w, r, s, k.Path, nil, fmt.Sprintf("Read %s's %s.", src.Name, k.Nouns))
	}
}

// categoriesDone finishes a change on the page: a browser without htmx comes
// back to it; with htmx, the page comes back with what was done.
func (a *Admin) categoriesDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, path string, err error, notice string) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showCategories(w, r, s, bad.status, path, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change a source's categories", "error", err)
		a.showCategories(w, r, s, http.StatusInternalServerError, path, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showCategories(w, r, s, http.StatusOK, path, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/sources/"+r.PathValue("id")+"/categories?kind="+path, http.StatusSeeOther)
	}
}

// formBring reads what a new source brings from the form that adds it:
// every category, or only those ticked once its first read lists them.
func formBring(r *http.Request) bool { return strings.TrimSpace(r.PostFormValue("bring")) == "ticked" }
