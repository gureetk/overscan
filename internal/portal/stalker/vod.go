// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/store"
)

// Films and series, as the protocol notes describe them (section 6). Films
// are in the vod module and series in a series module of their own (D74).
// The lists come from what the library keeps (D63): a source's list of
// films or of series is read when a client first asks for it, and a
// series' seasons and episodes when a client opens the series, never title
// by title as a client scrolls, which would look like a crawl to a
// provider (D22). A link plays the file as it is, through the hub, which
// passes on the player's range requests so that it can seek (D64, D75).

const (
	// libraryWait is how long a call waits for a first read of a source's
	// list, before it answers with what the library has.
	libraryWait = 8 * time.Second
	// partWait is how long a call waits for the page of a portal's shelf
	// that it reads (D77).
	partWait = 20 * time.Second
	// openWait is how long a call waits for a series' seasons and episodes
	// to be read.
	openWait = 10 * time.Second
	// seasonsPerSeries is the room a series leaves for its seasons' IDs: a
	// season's ID is its series' times this, plus its number.
	seasonsPerSeries = 1000
)

// UseLibrary gives the API the library of films and series. Without one,
// clients see none.
func (a *API) UseLibrary(l *library.Library) { a.library = l }

// vodItem is a film or a series in a list, in the shape the protocol notes
// describe (section 6), with numbers as strings. A number that is not known
// is "". A picture is an address on Overscan, which serves it from its own
// copy, and cmd names the title for create_link: it is no stream address.
type vodItem struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Cmd           string `json:"cmd"`
	CategoryID    string `json:"category_id"`
	ScreenshotURI string `json:"screenshot_uri"`
	Description   string `json:"description"`
	Actors        string `json:"actors"`
	Director      string `json:"director"`
	GenresStr     string `json:"genres_str"`
	Year          string `json:"year"`
	Time          string `json:"time"` // minutes
	RatingIMDB    string `json:"rating_imdb"`
	Age           string `json:"age"`
	Added         string `json:"added"`
	Censored      string `json:"censored"`
}

// seasonItem is a season of a series, as the series module gives it: the
// numbers its episodes are played by, with its cmd and create_link's series
// parameter, and the series' own details and poster.
type seasonItem struct {
	vodItem
	Series []int `json:"series"`
}

// titlePage is a page of a list of films, series or seasons.
type titlePage[T any] struct {
	TotalItems   int `json:"total_items"`
	MaxPageItems int `json:"max_page_items"`
	Data         []T `json:"data"`
}

// getCategories answers vod/get_categories and series/get_categories: "All"
// first, then the shelves of films or of series. It starts the reads of the
// sources' lists that are due, and waits a while for a first one. A
// library with nothing to browse yet has only "All"; one without sources of
// films and series has nothing.
func (a *API) getCategories(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	out := []genre{}
	if a.library == nil {
		writeJS(w, out)
		return
	}
	state, err := a.library.Want(r.Context(), kind, libraryWait)
	if err != nil {
		a.libraryError(w, r, "look at the library", err)
		return
	}
	if state.Sources == 0 {
		writeJS(w, out)
		return
	}
	out = append(out, genre{ID: "*", Title: "All", Censored: "0"})
	if state.Ready() {
		shelves, err := a.store.Shelves(r.Context(), kind)
		if err != nil {
			a.internalError(w, "list the shelves", err)
			return
		}
		locks, err := a.locksApply(r.Context(), d)
		if err != nil {
			a.internalError(w, "look up account", err)
			return
		}
		for _, s := range shelves {
			if s.Locked && locks {
				continue
			}
			out = append(out, genre{ID: strconv.FormatInt(s.ID, 10), Title: s.Name, Censored: "0"})
		}
	}
	writeJS(w, out)
}

// locksApply reports whether d's account has a parental PIN, so that what
// is locked is left out, since Stalker clients have no way to ask for it
// (D55, D85).
func (a *API) locksApply(ctx context.Context, d store.Device) (bool, error) {
	acct, err := a.store.Account(ctx, d.AccountID)
	return acct.PINHash != "", err
}

// getTitles answers vod/get_ordered_list and series/get_ordered_list: a page
// of the films or series in a category ("*" for all of them), the latest
// added first, or by name with sortby=name, and only those whose names hold
// search if it is given. With movie_id, the series module lists a series'
// seasons; films have no files listed apart, so vod lists nothing. The
// library keeps no favourite films or series, so fav=1 lists nothing.
func (a *API) getTitles(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	ctx := r.Context()
	p, err := strconv.Atoi(r.Form.Get("p"))
	if err != nil || p < 1 {
		p = 1
	}
	locks, err := a.locksApply(ctx, d)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	empty := titlePage[vodItem]{MaxPageItems: pageSize, Data: []vodItem{}}
	if id := r.Form.Get("movie_id"); id != "" && id != "0" {
		if kind == store.KindSeries {
			a.getSeasons(w, r, id, p, locks)
			return
		}
		writeJS(w, empty)
		return
	}
	if a.library == nil || r.Form.Get("fav") == "1" {
		writeJS(w, empty)
		return
	}
	var shelf int64
	if c := r.Form.Get("category"); c != "" && c != "*" {
		if shelf, err = strconv.ParseInt(c, 10, 64); err != nil || shelf <= 0 {
			writeJS(w, empty)
			return
		}
	}
	order := store.ByAdded
	if r.Form.Get("sortby") == "name" {
		order = store.ByName
	}
	if _, err := a.library.Want(ctx, kind, libraryWait); err != nil {
		a.libraryError(w, r, "look at the library", err)
		return
	}
	search := r.Form.Get("search")
	if shelf != 0 {
		cat, err := a.store.PortalCategory(ctx, kind, shelf)
		switch {
		case err == nil && search == "":
			a.getPortalTitles(w, r, kind, cat, p, locks)
			return
		case err == nil:
			// A portal is not searched: what was read of it is, with all
			// the rest (D77).
			shelf = 0
		case !errors.Is(err, store.ErrNotFound):
			a.internalError(w, "look up a shelf", err)
			return
		}
	}
	titles, total, err := a.store.TitlePage(ctx, kind, shelf, search, order, (p-1)*pageSize, pageSize, locks)
	if errors.Is(err, store.ErrNotFound) {
		writeJS(w, empty)
		return
	}
	if err != nil {
		a.internalError(w, "list titles", err)
		return
	}
	shelves, err := a.shelfIDs(ctx, kind)
	if err != nil {
		a.internalError(w, "list the shelves", err)
		return
	}
	out := titlePage[vodItem]{TotalItems: total, MaxPageItems: pageSize, Data: make([]vodItem, 0, len(titles))}
	for _, t := range titles {
		out.Data = append(out.Data, listedItem(r, t, shelves.of(t)))
	}
	writeJS(w, out)
}

// getPortalTitles answers for page p of a Stalker portal's shelf, which
// the library reads from the portal as clients ask for it (D77): its films,
// or its series, in the portal's order, whatever order was asked for. A
// portal without a series module lists its series among its films, and
// they are left out of its film pages, which can then hold fewer than 14;
// so are locked titles, with locks.
func (a *API) getPortalTitles(w http.ResponseWriter, r *http.Request, kind string, cat store.PortalCategory, p int,
	locks bool) {
	ctx, cancel := context.WithTimeout(r.Context(), partWait)
	defer cancel()
	part, err := a.library.PortalPart(ctx, cat, (p-1)*pageSize, pageSize)
	if err != nil {
		if r.Context().Err() == nil {
			a.log.Warn("cannot list a page of a portal's shelf", "shelf", cat.Name, "page", p, "error", err)
			writeJS(w, titlePage[vodItem]{MaxPageItems: pageSize, Data: []vodItem{}})
		}
		return
	}
	id := strconv.FormatInt(cat.ID, 10)
	out := titlePage[vodItem]{TotalItems: max(part.Total, 0), MaxPageItems: pageSize, Data: make([]vodItem, 0, len(part.Titles))}
	for _, t := range part.Titles {
		if t.Kind == kind && !(t.Locked && locks) {
			out.Data = append(out.Data, listedItem(r, t, id))
		}
	}
	writeJS(w, out)
}

// shelfIDs are the IDs of the shelves of a kind, as get_categories gives
// them.
type shelfIDs struct {
	byName map[string]string // of the shelves that merge sources' categories of the same name
	portal map[int64]bool    // of the shelves that are portals' categories (D77)
}

// of returns the ID of a title's shelf, "" if it has none.
func (s shelfIDs) of(t store.TitleListing) string {
	if s.portal[t.CategoryID] {
		return strconv.FormatInt(t.CategoryID, 10)
	}
	return s.byName[t.Shelf]
}

// shelfIDs returns the IDs of the shelves of a kind.
func (a *API) shelfIDs(ctx context.Context, kind string) (shelfIDs, error) {
	shelves, err := a.store.Shelves(ctx, kind)
	if err != nil {
		return shelfIDs{}, err
	}
	out := shelfIDs{byName: map[string]string{}, portal: map[int64]bool{}}
	for _, s := range shelves {
		if s.Portal {
			out.portal[s.ID] = true
		} else {
			out.byName[s.Name] = strconv.FormatInt(s.ID, 10)
		}
	}
	return out, nil
}

// listedItem turns a title in a list into its Stalker shape, with what its
// details say, once a device has opened it, before what its source's list
// says.
func listedItem(r *http.Request, t store.TitleListing, category string) vodItem {
	d := t.Details
	if d == nil {
		d = &store.TitleDetails{}
	}
	runtime := d.Runtime
	if d.Duration > 0 {
		runtime = (d.Duration + 30) / 60
	}
	if runtime == 0 {
		runtime = t.Summary.Runtime
	}
	year, rating := t.Year, t.Rating
	if d.Year > 0 {
		year = d.Year
	}
	if d.Rating > 0 {
		rating = d.Rating
	}
	added := ""
	if !t.AddedAt.IsZero() {
		added = t.AddedAt.UTC().Format(stalkerDate)
	}
	return vodItem{
		ID:            strconv.FormatInt(t.ID, 10),
		Name:          t.Name,
		Cmd:           titleCmd(t.ID),
		CategoryID:    category,
		ScreenshotURI: pictureURL(r, artwork.PosterKind, t.ID, first(d.Poster, t.Poster)),
		Description:   first(d.Plot, t.Summary.Plot),
		Actors:        first(d.Cast, t.Summary.Cast),
		Director:      first(d.Director, t.Summary.Director),
		GenresStr:     first(d.Genre, t.Summary.Genre),
		Year:          number(year),
		Time:          number(runtime),
		RatingIMDB:    rating10(rating),
		Age:           d.AgeRating,
		Added:         added,
		Censored:      "0",
	}
}

// getSeasons answers series/get_ordered_list with movie_id: a page of the
// series' seasons, each with the numbers of its episodes. Overscan reads
// the series' details and episodes from its source, if it has none or they
// are old, as when a box opens it (D63); if the source is slow, it lists
// what it has.
func (a *API) getSeasons(w http.ResponseWriter, r *http.Request, movieID string, p int, locks bool) {
	empty := titlePage[seasonItem]{MaxPageItems: pageSize, Data: []seasonItem{}}
	id, err := strconv.ParseInt(movieID, 10, 64)
	if err != nil || id <= 0 || a.library == nil {
		writeJS(w, empty)
		return
	}
	o, err := a.openSeries(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || err == nil && o.Locked && locks {
		writeJS(w, empty)
		return
	}
	if err != nil {
		a.libraryError(w, r, "open a series", err)
		return
	}
	shelves, err := a.shelfIDs(r.Context(), store.KindSeries)
	if err != nil {
		a.internalError(w, "list the shelves", err)
		return
	}
	listing := store.TitleListing{TitleRow: o.TitleRow, Shelf: o.Shelf, CategoryID: o.CategoryID, Summary: o.Summary,
		Details: o.Details}
	series := listedItem(r, listing, shelves.of(listing))
	described := map[int]store.SeasonDetails{}
	if o.Details != nil {
		for _, s := range o.Details.Seasons {
			described[s.Number] = s
		}
	}
	seasons := numberedSeasons(o.Episodes)
	out := titlePage[seasonItem]{TotalItems: len(seasons), MaxPageItems: pageSize, Data: []seasonItem{}}
	start := min((p-1)*pageSize, len(seasons))
	for _, s := range seasons[start:min(start+pageSize, len(seasons))] {
		item := seasonItem{vodItem: series, Series: s.Numbers}
		item.ID = strconv.FormatInt(id*seasonsPerSeries+int64(s.Number), 10)
		item.Cmd = seasonCmd(id, s.Number)
		item.Name = seasonName(s.Number, described[s.Number].Name)
		item.Description = first(described[s.Number].Plot, series.Description)
		out.Data = append(out.Data, item)
	}
	writeJS(w, out)
}

// openSeries returns a series with its episodes, read from its source if
// Overscan has none or they are old, or what Overscan has if the source
// takes longer than openWait. It returns store.ErrNotFound for a title that
// is not a series that devices see, without reading anything of it.
func (a *API) openSeries(ctx context.Context, id int64) (library.Opened, error) {
	t, err := a.store.Title(ctx, id)
	if err != nil {
		return library.Opened{}, err
	}
	if t.Kind != store.KindSeries {
		return library.Opened{}, store.ErrNotFound
	}
	readCtx, cancel := context.WithTimeout(ctx, openWait)
	defer cancel()
	o, err := a.library.Open(readCtx, id)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		o = library.Opened{Title: t}
		o.Episodes, err = a.store.Episodes(ctx, id)
	}
	if err == nil && o.Kind != store.KindSeries {
		err = store.ErrNotFound
	}
	return o, err
}

// season is a season of a series as a Stalker client sees it: its
// episodes, and the number that create_link's series parameter names each
// by.
type season struct {
	Number   int
	Episodes []store.Episode
	Numbers  []int
}

// numberedSeasons groups a series' episodes, which come by season and
// number, into seasons. An episode goes by its own number, unless its
// season has an episode without one or two with the same one: the season's
// episodes then go by their places in it, from 1.
func numberedSeasons(episodes []store.Episode) []season {
	var out []season
	for _, e := range episodes {
		if len(out) == 0 || out[len(out)-1].Number != e.Season {
			out = append(out, season{Number: e.Season})
		}
		s := &out[len(out)-1]
		s.Episodes = append(s.Episodes, e)
	}
	for i := range out {
		s := &out[i]
		seen := map[int]bool{}
		own := true
		for _, e := range s.Episodes {
			if e.Number <= 0 || seen[e.Number] {
				own = false
				break
			}
			seen[e.Number] = true
		}
		s.Numbers = make([]int, len(s.Episodes))
		for j, e := range s.Episodes {
			s.Numbers[j] = j + 1
			if own {
				s.Numbers[j] = e.Number
			}
		}
	}
	return out
}

// seasonName is a season's name: its source's, or else "Season N", and
// "Specials" for season 0.
func seasonName(number int, name string) string {
	switch {
	case name != "":
		return name
	case number == 0:
		return "Specials"
	}
	return "Season " + strconv.Itoa(number)
}

// createTitleLink answers vod/create_link and series/create_link with a link
// that plays the film, or the episode of a season, that cmd names, through
// the hub, as it is (D75): a season's episode is the one that the series
// parameter names, or its first. The answer is an address with no player
// prefix, which clients that keep a cmd's last word read the same way. The
// error is nothing_to_play for a title that devices do not see, one that is
// locked once the account has a PIN (D85), a series without a season, or an
// episode that the season lacks, and limit when the title's source has no
// free connection.
func (a *API) createTitleLink(w http.ResponseWriter, r *http.Request, d store.Device) {
	reply := func(cmd, errText string) {
		writeJS(w, struct {
			Cmd   string `json:"cmd"`
			Error string `json:"error"`
		}{cmd, errText})
	}
	ctx := r.Context()
	id, seasonNumber, ok := mediaFromCmd(r.Form.Get("cmd"))
	if !ok || a.library == nil {
		reply("", "nothing_to_play")
		return
	}
	var item store.PlayableItem
	var err error
	if seasonNumber < 0 {
		item, err = a.store.PlayableMovie(ctx, id)
	} else {
		n, _ := strconv.Atoi(r.Form.Get("series"))
		item, err = a.episodeOf(ctx, id, seasonNumber, n)
	}
	if errors.Is(err, store.ErrNotFound) {
		reply("", "nothing_to_play")
		return
	}
	if err != nil {
		a.libraryError(w, r, "look up a title to play", err)
		return
	}
	if item.Locked {
		locks, err := a.locksApply(ctx, d)
		if err != nil {
			a.internalError(w, "look up account", err)
			return
		}
		if locks {
			reply("", "nothing_to_play")
			return
		}
	}
	if a.hub.Room(r.Context(), item.SourceID, d.ID) != nil {
		reply("", "limit")
		return
	}
	reply("http://"+r.Host+a.hub.ItemLink(d.ID, item), "")
}

// episodeOf returns the episode of a series that a season's number and an
// episode's number in it name (see numberedSeasons), or the season's first
// for a number below 1. A series whose episodes Overscan has not read yet
// is opened first.
func (a *API) episodeOf(ctx context.Context, seriesID int64, seasonNumber, n int) (store.PlayableItem, error) {
	episodes, err := a.store.Episodes(ctx, seriesID)
	if err != nil {
		return store.PlayableItem{}, err
	}
	if len(episodes) == 0 {
		o, err := a.openSeries(ctx, seriesID)
		if err != nil {
			return store.PlayableItem{}, err
		}
		episodes = o.Episodes
	}
	for _, s := range numberedSeasons(episodes) {
		if s.Number != seasonNumber {
			continue
		}
		for i, num := range s.Numbers {
			if num == n || n < 1 {
				return a.store.PlayableEpisode(ctx, s.Episodes[i].ID)
			}
		}
	}
	return store.PlayableItem{}, store.ErrNotFound
}

// titleCmd is the cmd of a film or a series: a path in the form that the
// protocol notes show for films (section 6). It names the title for
// create_link and is no stream address.
func titleCmd(id int64) string { return "/media/" + strconv.FormatInt(id, 10) + ".mpg" }

// seasonCmd is the cmd of a season of a series, which create_link plays an
// episode of.
func seasonCmd(seriesID int64, number int) string {
	return "/media/" + strconv.FormatInt(seriesID, 10) + "_" + strconv.Itoa(number) + ".mpg"
}

// mediaFromCmd returns the title that a cmd names, and the season of a
// series that it names, or -1 if it names none: the last word of the cmd is
// a path, or an address with a path, made by titleCmd or seasonCmd.
func mediaFromCmd(cmd string) (int64, int, bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return 0, 0, false
	}
	u, err := url.Parse(fields[len(fields)-1])
	if err != nil {
		return 0, 0, false
	}
	rest, ok := strings.CutPrefix(u.Path, "/media/")
	if !ok {
		return 0, 0, false
	}
	if rest, ok = strings.CutSuffix(rest, ".mpg"); !ok {
		return 0, 0, false
	}
	idText, seasonText, hasSeason := strings.Cut(rest, "_")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 || !digits(idText) {
		return 0, 0, false
	}
	if !hasSeason {
		return id, -1, true
	}
	n, err := strconv.Atoi(seasonText)
	if err != nil || n < 0 || n >= seasonsPerSeries || !digits(seasonText) {
		return 0, 0, false
	}
	return id, n, true
}

// digits reports whether s is made of the digits 0 to 9 alone.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// pictureURL returns the address on Overscan of a title's picture, "" if
// it has none.
func pictureURL(r *http.Request, kind artwork.Kind, id int64, address string) string {
	path := artwork.TitlePath(kind, id, address)
	if path == "" {
		return ""
	}
	return "http://" + r.Host + path
}

// number is a positive number as a string, or "" for 0.
func number(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// rating10 is a rating out of 100 as one out of 10 with a decimal, such as
// "7.4", or "" for none.
func rating10(r int) string {
	if r <= 0 {
		return ""
	}
	return strconv.Itoa(r/10) + "." + strconv.Itoa(r%10)
}

// first returns the first of its arguments that is not "".
func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// libraryError answers a call whose read of the library failed. A client
// that went away needs no answer.
func (a *API) libraryError(w http.ResponseWriter, r *http.Request, what string, err error) {
	if r.Context().Err() != nil {
		return
	}
	a.internalError(w, what, err)
}
