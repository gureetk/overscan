// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/subtitles"
)

// Films and series: what a box browses and opens (D63), the links that play
// them (D64), and how far the box's account got into them (D65).

// What the library can show, in the state member of GET movies and GET
// series.
const (
	libraryNone    = "none"    // no source has films or series
	libraryLoading = "loading" // a first read is under way: ask again in a moment
	libraryReady   = "ready"
	libraryFailed  = "failed" // the first read failed; error says why
)

// Shelves that are not a source's categories.
const (
	shelfContinue = "continue" // what the account is part way through
	shelfRecent   = "recent"   // what the sources added last
)

const (
	recentTitles   = 100
	continueTitles = 30
	maxShelfPage   = 1000
	// libraryWait is how long a call waits for a first read of a list,
	// before it answers that the list is loading.
	libraryWait = 6 * time.Second
	// partWait is how long a call waits for the pages of a portal's shelf
	// that it reads (D77).
	partWait = 20 * time.Second
	// openWait is how long a call waits for a title's details to be read.
	openWait = 10 * time.Second
	// notStarted is how far into a film or an episode a box must get for
	// the account to be part way through it.
	notStarted = 30
	// maxSeconds caps a position or a duration.
	maxSeconds = 100 * 3600
	// fileSolution is the box player's solution for a file through HTTP.
	fileSolution = "ffmpeg"
)

// UseLibrary gives the API the library of films and series. Without one, the
// box sees none.
func (a *API) UseLibrary(l *library.Library) { a.library = l }

// shelf is a shelf of films or series.
type shelf struct {
	ID    string `json:"id"` // a category's ID, as digits, or continue or recent
	Name  string `json:"name"`
	Count int    `json:"count"` // -1 while not known: a portal's shelf until a part of it is read (D77)
}

// libraryResponse says what the box can browse of films or of series.
type libraryResponse struct {
	State   string  `json:"state"`
	Error   string  `json:"error,omitempty"`
	Shelves []shelf `json:"shelves"`
}

// titleRow is a film or a series on a shelf, or found by a search.
type titleRow struct {
	ID int64 `json:"id"`
	// Kind is movie or series. A portal without a series module lists its
	// series among its films, so a shelf of films can hold series (D77).
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	Year     int    `json:"year,omitempty"`
	Rating   int    `json:"rating,omitempty"` // out of 100
	Poster   string `json:"poster,omitempty"`
	Progress int    `json:"progress,omitempty"` // percent watched, of a film or episode part way through
	Watched  bool   `json:"watched,omitempty"`  // a film that the account finished
	Episode  string `json:"episode,omitempty"`  // on the continue shelf, the series' episode, such as S1 E3
	// Locked is set when playing it needs the account's parental PIN
	// (D85).
	Locked bool `json:"locked,omitempty"`
}

// shelfResponse is a part of a shelf: titles from, at most count of them.
type shelfResponse struct {
	ID     string     `json:"id"`
	Name   string     `json:"name"`
	Total  int        `json:"total"`
	From   int        `json:"from"`
	Titles []titleRow `json:"titles"`
	// Next is the from of the next part, -1 after the last. A part of a
	// portal's shelf ends where the pages read for it do, and its places
	// are the portal's (D77).
	Next int `json:"next"`
}

// row is a title as a list shows it; locks says that the account has a
// parental PIN, so that a locked title is marked.
func row(t store.TitleRow, locks bool) titleRow {
	return titleRow{ID: t.ID, Kind: t.Kind, Name: t.Name, Year: t.Year, Rating: t.Rating,
		Poster: artwork.TitlePath(artwork.PosterKind, t.ID, t.Poster), Locked: t.Locked && locks}
}

// locksApply reports whether d's account has a parental PIN, without which
// locks do nothing (D55, D85).
func (a *API) locksApply(ctx context.Context, d store.Device) (bool, error) {
	acct, err := a.store.Account(ctx, d.AccountID)
	return acct.PINHash != "", err
}

// percent returns how much of a duration a position is, 1 to 99, or 0.
func percent(position, duration int) int {
	if duration <= 0 || position <= 0 {
		return 0
	}
	return min(99, max(1, position*100/duration))
}

// handler is a call's handler, for a device with a session.
type handler = func(http.ResponseWriter, *http.Request, store.Device)

// libraryState answers GET movies and GET series: whether the box can
// browse them, and the shelves: what the account is part way through, what
// the sources added last, and the sources' categories.
func (a *API) libraryState(kind string) handler {
	return func(w http.ResponseWriter, r *http.Request, d store.Device) { a.listShelves(w, r, d, kind) }
}

func (a *API) listShelves(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	resp := libraryResponse{State: libraryNone, Shelves: []shelf{}}
	if a.library == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	ctx := r.Context()
	state, err := a.library.Want(ctx, kind, a.libraryWait)
	if err != nil {
		a.internalError(w, "look at the library", err)
		return
	}
	switch {
	case state.Ready():
		resp.State = libraryReady
	case state.Loading:
		resp.State = libraryLoading
	case state.Error != "":
		resp.State, resp.Error = libraryFailed, state.Error
	}
	if resp.State != libraryReady {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	watching, err := a.store.ContinueWatching(ctx, d.ProfileID, kind, continueTitles)
	if err != nil {
		a.internalError(w, "list what the account is watching", err)
		return
	}
	if len(watching) > 0 {
		resp.Shelves = append(resp.Shelves, shelf{ID: shelfContinue, Name: "Continue Watching", Count: len(watching)})
	}
	recent, err := a.store.RecentTitles(ctx, kind, recentTitles)
	if err != nil {
		a.internalError(w, "list what was added last", err)
		return
	}
	if len(recent) > 0 {
		resp.Shelves = append(resp.Shelves, shelf{ID: shelfRecent, Name: "Recently Added", Count: len(recent)})
	}
	shelves, err := a.store.Shelves(ctx, kind)
	if err != nil {
		a.internalError(w, "list the shelves", err)
		return
	}
	for _, s := range shelves {
		resp.Shelves = append(resp.Shelves, shelf{ID: strconv.FormatInt(s.ID, 10), Name: s.Name, Count: s.Count})
	}
	writeJSON(w, http.StatusOK, resp)
}

// shelfTitles answers GET movies/shelves/{id} and GET series/shelves/{id}:
// the titles on a shelf, from from, at most count of them (200 if not
// given). A source's category is in the order its source added them, the
// latest first, or by name with order=name.
func (a *API) shelfTitles(kind string) handler {
	return func(w http.ResponseWriter, r *http.Request, d store.Device) { a.listShelf(w, r, d, kind) }
}

func (a *API) listShelf(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	ctx := r.Context()
	q := r.URL.Query()
	from, count := 0, 200
	if v := q.Get("from"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, errBadRequest)
			return
		}
		from = n
	}
	if v := q.Get("count"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxShelfPage {
			writeError(w, http.StatusBadRequest, errBadRequest)
			return
		}
		count = n
	}
	order := store.ByAdded
	switch q.Get("order") {
	case "", store.ByAdded:
	case store.ByName:
		order = store.ByName
	default:
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	locks, err := a.locksApply(ctx, d)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	id := r.PathValue("id")
	resp := shelfResponse{ID: id, From: from, Titles: []titleRow{}}
	var rows []titleRow
	switch id {
	case shelfContinue:
		resp.Name = "Continue Watching"
		watching, err := a.store.ContinueWatching(ctx, d.ProfileID, kind, continueTitles)
		if err != nil {
			a.internalError(w, "list what the account is watching", err)
			return
		}
		for _, w := range watching {
			t := row(w.Title, locks)
			t.Progress = percent(w.Progress.Position, w.Progress.Duration)
			if e := w.Episode; e != nil {
				if w.Next != nil { // finished: the one after it plays next
					e, t.Progress = w.Next, 0
				}
				t.Episode = "S" + strconv.Itoa(e.Season) + " E" + strconv.Itoa(e.Number)
			}
			rows = append(rows, t)
		}
	default:
		var titles []store.TitleRow
		var err error
		if id == shelfRecent {
			resp.Name = "Recently Added"
			titles, err = a.store.RecentTitles(ctx, kind, recentTitles)
		} else {
			n, perr := strconv.ParseInt(id, 10, 64)
			if perr != nil || n <= 0 {
				writeError(w, http.StatusNotFound, errNotFound)
				return
			}
			if cat, cerr := a.store.PortalCategory(ctx, kind, n); cerr == nil && a.library != nil {
				a.listPortalShelf(w, r, d, kind, cat, from, count, locks)
				return
			} else if cerr != nil && !errors.Is(cerr, store.ErrNotFound) {
				a.internalError(w, "look up a shelf", cerr)
				return
			}
			titles, err = a.store.ShelfTitles(ctx, kind, n, order)
			if len(titles) == 0 && err == nil {
				err = store.ErrNotFound
			}
		}
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		if err != nil {
			a.internalError(w, "list a shelf", err)
			return
		}
		var watched map[int64]store.Progress
		if kind == store.KindMovie {
			if watched, err = a.store.MovieProgress(ctx, d.ProfileID); err != nil {
				a.internalError(w, "list what the account watched", err)
				return
			}
		}
		for _, t := range titles {
			r := row(t, locks)
			if p, ok := watched[t.ID]; ok {
				r.Progress, r.Watched = percent(p.Position, p.Duration), p.Finished
			}
			rows = append(rows, r)
		}
		if resp.Name == "" && len(titles) > 0 {
			if t, err := a.store.Title(ctx, titles[0].ID); err == nil {
				resp.Name = t.Shelf
			}
		}
	}
	resp.Total, resp.Next = len(rows), -1
	if from < len(rows) {
		resp.Titles = rows[from:min(len(rows), from+count)]
		if end := from + len(resp.Titles); end < len(rows) {
			resp.Next = end
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// listPortalShelf answers for a part of a Stalker portal's shelf, whose
// pages the library reads as devices browse it (D77): films and series
// alike, in the portal's order whatever order was asked for, from the
// place from in it, up to where the pages read for it end.
func (a *API) listPortalShelf(w http.ResponseWriter, r *http.Request, d store.Device, kind string,
	cat store.PortalCategory, from, count int, locks bool) {
	ctx, cancel := context.WithTimeout(r.Context(), partWait)
	defer cancel()
	part, err := a.library.PortalPart(ctx, cat, from, count)
	if err != nil {
		if r.Context().Err() == nil {
			writeError(w, http.StatusBadGateway, errSource)
		}
		return
	}
	watched, err := a.store.MovieProgress(ctx, d.ProfileID)
	if err != nil {
		a.internalError(w, "list what the account watched", err)
		return
	}
	resp := shelfResponse{ID: strconv.FormatInt(cat.ID, 10), Name: cat.Name, Total: max(part.Total, 0), From: from,
		Titles: make([]titleRow, 0, len(part.Titles)), Next: part.Next}
	for _, t := range part.Titles {
		r := row(t.TitleRow, locks)
		if p, ok := watched[t.ID]; ok && t.Kind == store.KindMovie {
			r.Progress, r.Watched = percent(p.Position, p.Duration), p.Finished
		}
		resp.Titles = append(resp.Titles, r)
	}
	writeJSON(w, http.StatusOK, resp)
}

// progress is how far the account got into a film or an episode.
type progress struct {
	Position  int   `json:"position"` // seconds; 0 once finished
	Duration  int   `json:"duration"` // seconds, 0 if not known
	Finished  bool  `json:"finished,omitempty"`
	WatchedAt int64 `json:"watched_at"` // Unix seconds
}

func progressOf(p store.Progress) *progress {
	return &progress{Position: p.Position, Duration: p.Duration, Finished: p.Finished, WatchedAt: p.WatchedAt.Unix()}
}

// titleResponse is a film or a series that a box opened.
type titleResponse struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Year      int    `json:"year,omitempty"`
	Rating    int    `json:"rating,omitempty"`
	Poster    string `json:"poster,omitempty"`
	Backdrop  string `json:"backdrop,omitempty"`
	Shelf     string `json:"shelf,omitempty"`
	Plot      string `json:"plot,omitempty"`
	Genre     string `json:"genre,omitempty"`
	Cast      string `json:"cast,omitempty"`
	Director  string `json:"director,omitempty"`
	Country   string `json:"country,omitempty"`
	Released  string `json:"released,omitempty"`
	AgeRating string `json:"age_rating,omitempty"`
	Runtime   int    `json:"runtime,omitempty"` // minutes: a film's, or a series' episodes'
	// Details is false while the source's details have not been read: the
	// title then shows what its source's list said. DetailsError says why
	// they could not be read just now.
	Details      bool      `json:"details"`
	DetailsError string    `json:"details_error,omitempty"`
	Progress     *progress `json:"progress,omitempty"` // a film's
	Seasons      []season  `json:"seasons,omitempty"`  // a series'
	Next         *next     `json:"next,omitempty"`     // a series' episode to play next
	// Locked is set when playing it, or a series' episodes, needs the
	// account's parental PIN (D85).
	Locked bool `json:"locked,omitempty"`
}

// season is a season of a series, with its episodes.
type season struct {
	Number   int       `json:"number"`
	Name     string    `json:"name"`
	Episodes []episode `json:"episodes"`
}

// episode is an episode of a series.
type episode struct {
	ID       int64     `json:"id"`
	Number   int       `json:"number"`
	Name     string    `json:"name,omitempty"`
	Duration int       `json:"duration,omitempty"` // seconds
	Plot     string    `json:"plot,omitempty"`
	Picture  string    `json:"picture,omitempty"`
	Progress *progress `json:"progress,omitempty"`
}

// next is the episode of a series that plays next, and where.
type next struct {
	Episode  int64 `json:"episode"`
	Position int   `json:"position"` // seconds
}

// openTitle answers GET movies/{id} and GET series/{id}: the title with its
// details, read from its source if Overscan has none or they are old, how
// far the account got, and a series' episodes and the one to play next.
func (a *API) openTitle(kind string) handler {
	return func(w http.ResponseWriter, r *http.Request, d store.Device) { a.open(w, r, d, kind) }
}

func (a *API) open(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 || a.library == nil {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), openWait)
	defer cancel()
	o, err := a.library.Open(ctx, id)
	if errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil {
		// The source is slow: show what Overscan has, and the details next
		// time.
		var t store.Title
		if t, err = a.store.Title(r.Context(), id); err == nil {
			o = library.Opened{Title: t, ReadError: "the source is taking long to answer"}
			if t.Kind == store.KindSeries {
				o.Episodes, err = a.store.Episodes(r.Context(), id)
			}
		}
	}
	if errors.Is(err, store.ErrNotFound) || (err == nil && o.Kind != kind) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "open a title", err)
		return
	}
	locks, err := a.locksApply(r.Context(), d)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	resp := titleResponse{ID: o.ID, Kind: o.Kind, Name: o.Name, Year: o.Year, Rating: o.Rating, Shelf: o.Shelf,
		Plot: o.Summary.Plot, Genre: o.Summary.Genre, Cast: o.Summary.Cast, Director: o.Summary.Director,
		Runtime: o.Summary.Runtime, Details: o.Details != nil, DetailsError: o.ReadError, Locked: o.Locked && locks}
	poster, backdrop := o.Poster, o.Summary.Backdrop
	if dt := o.Details; dt != nil {
		resp.Plot = first(dt.Plot, resp.Plot)
		resp.Genre = first(dt.Genre, resp.Genre)
		resp.Cast = first(dt.Cast, resp.Cast)
		resp.Director = first(dt.Director, resp.Director)
		resp.Country, resp.Released, resp.AgeRating = dt.Country, dt.Released, dt.AgeRating
		if dt.Year > 0 {
			resp.Year = dt.Year
		}
		if dt.Rating > 0 {
			resp.Rating = dt.Rating
		}
		if dt.Runtime > 0 {
			resp.Runtime = dt.Runtime
		}
		poster, backdrop = first(dt.Poster, poster), first(dt.Backdrop, backdrop)
	}
	resp.Poster = artwork.TitlePath(artwork.PosterKind, o.ID, poster)
	resp.Backdrop = artwork.TitlePath(artwork.BackdropKind, o.ID, backdrop)
	made, err := a.store.TitleProgress(r.Context(), d.ProfileID, o.ID)
	if err != nil {
		a.internalError(w, "look up progress", err)
		return
	}
	if o.Kind == store.KindMovie {
		if len(made) > 0 {
			resp.Progress = progressOf(made[0])
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Seasons, resp.Next = seasons(o, made)
	writeJSON(w, http.StatusOK, resp)
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

// seasons returns a series' seasons with their episodes and the account's
// progress in each, and the episode to play next: the one the account
// played last if it did not finish it (from where it stopped, or from the
// start if it barely started), else the one after it, else the first.
func seasons(o library.Opened, made []store.Progress) ([]season, *next) {
	byEpisode := map[int64]store.Progress{}
	for _, p := range made {
		byEpisode[p.EpisodeID] = p
	}
	names := map[int]string{}
	if o.Details != nil {
		for _, s := range o.Details.Seasons {
			names[s.Number] = s.Name
		}
	}
	out := []season{}
	for _, e := range o.Episodes {
		if len(out) == 0 || out[len(out)-1].Number != e.Season {
			name := names[e.Season]
			if name == "" {
				name = "Season " + strconv.Itoa(e.Season)
				if e.Season == 0 {
					name = "Specials"
				}
			}
			out = append(out, season{Number: e.Season, Name: name, Episodes: []episode{}})
		}
		ep := episode{ID: e.ID, Number: e.Number, Name: e.Name, Duration: e.Duration, Plot: e.Plot,
			Picture: artwork.TitlePath(artwork.StillKind, e.ID, e.Picture)}
		if p, ok := byEpisode[e.ID]; ok {
			ep.Progress = progressOf(p)
		}
		s := &out[len(out)-1]
		s.Episodes = append(s.Episodes, ep)
	}
	if len(o.Episodes) == 0 {
		return out, nil
	}
	nx := &next{Episode: o.Episodes[0].ID}
	for _, p := range made { // the latest first
		at := -1
		for i, e := range o.Episodes {
			if e.ID == p.EpisodeID {
				at = i
				break
			}
		}
		if at < 0 {
			continue // an episode that its source no longer lists
		}
		switch {
		case !p.Finished:
			nx = &next{Episode: p.EpisodeID, Position: p.Position}
		case at+1 < len(o.Episodes):
			nx = &next{Episode: o.Episodes[at+1].ID}
		}
		break
	}
	return out, nx
}

// playItem answers POST movies/{id}/play and POST episodes/{id}/play with
// how the box plays the film or episode through the hub: a link, the box
// player's solution for it, and how the box seeks in it (D64, D68). Only a
// page that says it seeks by asking for a stream from a time gets a stream
// through ffmpeg; others get the file as it is. kind is store.KindMovie or
// store.KindEpisode.
func (a *API) playItem(kind string) handler {
	return func(w http.ResponseWriter, r *http.Request, d store.Device) { a.playTitle(w, r, d, kind) }
}

// itemPlayRequest is the body of a call to play a film or an episode.
type itemPlayRequest struct {
	// Restart says that the page can play a stream that ffmpeg makes,
	// seeking in it by asking for it from a time (D68).
	Restart bool `json:"restart"`
	// PIN is the parental PIN, for a locked film or series (D85).
	PIN string `json:"pin"`
}

// itemPlayResponse is how the box plays a film or an episode.
type itemPlayResponse struct {
	URL      string `json:"url"`
	Solution string `json:"solution"`
	// Seek is "player" when the box's player seeks in the file itself, and
	// "restart" when it plays a stream that ffmpeg makes of it: the page
	// then asks Start where the stream from a time truly starts, and plays
	// URL from there with ?t=.
	Seek     string       `json:"seek"`
	Start    string       `json:"start,omitempty"`
	Duration int          `json:"duration"`        // seconds, 0 if not known
	Audio    []audioTrack `json:"audio,omitempty"` // a converted stream's sound tracks, in its order
	// AudioSwitch is "restart" for a stream that the source's server
	// transcodes, with one sound track at a time (D71): the page picks
	// another by asking for URL again with ?a= and its place in Audio.
	AudioSwitch string `json:"audio_switch,omitempty"`
	// Subtitles are the subtitle tracks that the page may load (D69).
	Subtitles []subtitleTrack `json:"subtitles,omitempty"`
}

// subtitleTrack is a subtitle track that the page may load: SRT at URL,
// with ?t= for a stream through ffmpeg that starts part way through.
type subtitleTrack struct {
	Language string `json:"language,omitempty"` // as its name or the server tags it, such as eng
	Title    string `json:"title,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	Hearing  bool   `json:"hearing,omitempty"` // for the deaf and hard of hearing
	Default  bool   `json:"default,omitempty"`
	URL      string `json:"url"`
}

// audioTrack is a sound track of a converted stream.
type audioTrack struct {
	Language  string `json:"language,omitempty"` // as the file tags it, such as eng
	Title     string `json:"title,omitempty"`
	Codec     string `json:"codec"`              // the file's, as ffmpeg names it
	Channels  int    `json:"channels,omitempty"` // the file's
	Converted bool   `json:"converted,omitempty"`
}

func (a *API) playTitle(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var req itemPlayRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	var item store.PlayableItem
	if kind == store.KindEpisode {
		item, err = a.store.PlayableEpisode(r.Context(), id)
	} else {
		item, err = a.store.PlayableMovie(r.Context(), id)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a title to play", err)
		return
	}
	if item.Locked {
		acct, err := a.store.Account(r.Context(), d.AccountID)
		if err != nil {
			a.internalError(w, "look up account", err)
			return
		}
		if acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
			return
		}
	}
	if !a.room(w, a.hub.Room(r.Context(), item.SourceID, d.ID)) {
		return
	}
	if !req.Restart {
		writeJSON(w, http.StatusOK, itemPlayResponse{URL: "http://" + r.Host + a.hub.ItemLink(d.ID, item),
			Solution: fileSolution, Seek: "player"})
		return
	}
	var subs []subtitles.Track
	if a.library != nil {
		subs = a.library.Subtitles(r.Context(), item)
	}
	pb, err := a.hub.Prepare(r.Context(), d.ID, item, a.profile(r.Context(), d), subs)
	if errors.Is(err, hub.ErrLimit) || errors.Is(err, hub.ErrAccountLimit) {
		a.room(w, err)
		return
	}
	if errors.Is(err, hub.ErrBusy) {
		writeError(w, http.StatusServiceUnavailable, errBusy)
		return
	}
	if errors.Is(err, hub.ErrNoLink) {
		writeError(w, http.StatusBadGateway, errSource)
		return
	}
	if err != nil {
		a.internalError(w, "prepare a title to play", err)
		return
	}
	writeJSON(w, http.StatusOK, itemResponse(r.Host, pb))
}

// itemResponse is how a box plays what pb says, from the server at host.
func itemResponse(host string, pb hub.Playback) itemPlayResponse {
	out := itemPlayResponse{URL: "http://" + host + pb.Path, Solution: fileSolution, Seek: "player",
		Duration: int(pb.Duration + 0.5)}
	for _, sub := range pb.Subtitles {
		t := sub.Track
		out.Subtitles = append(out.Subtitles, subtitleTrack{Language: t.Language, Title: t.Title, Forced: t.Forced,
			Hearing: t.Hearing, Default: t.Default, URL: "http://" + host + sub.Path})
	}
	if pb.Converted {
		out.Seek, out.Start = "restart", "http://"+host+pb.StartPath
		if pb.Transcoded {
			out.AudioSwitch = "restart"
		}
		for _, t := range pb.Tracks {
			out.Audio = append(out.Audio, audioTrack{Language: t.Language, Title: t.Title, Codec: t.Codec,
				Channels: t.Channels, Converted: t.Convert})
		}
	}
	return out
}

// progressRequest is the body of a progress call.
type progressRequest struct {
	Position int  `json:"position"` // seconds into it
	Duration int  `json:"duration"` // seconds, 0 if the box does not know
	Finished bool `json:"finished"` // it played to its end
}

// putProgress answers PUT movies/{id}/progress and PUT
// episodes/{id}/progress: it keeps how far the account got (D65), and
// answers with what it kept. Near the end, the film or episode counts as
// finished; at the very start, as not started. kind is store.KindMovie or
// store.KindEpisode.
func (a *API) putProgress(kind string) handler {
	return func(w http.ResponseWriter, r *http.Request, d store.Device) { a.keepProgress(w, r, d, kind) }
}

func (a *API) keepProgress(w http.ResponseWriter, r *http.Request, d store.Device, kind string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	var req progressRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	if req.Position < 0 || req.Duration < 0 || req.Position > maxSeconds || req.Duration > maxSeconds {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	ctx := r.Context()
	p := store.Progress{Position: req.Position, Duration: req.Duration, WatchedAt: a.now()}
	if kind == store.KindEpisode {
		p.EpisodeID = id
		p.TitleID, err = a.store.EpisodeSeries(ctx, id)
	} else {
		var t store.Title
		if t, err = a.store.Title(ctx, id); err == nil && t.Kind != store.KindMovie {
			err = store.ErrNotFound
		}
		p.TitleID = id
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a title", err)
		return
	}
	p.Finished = req.Finished || nearEnd(p.Position, p.Duration)
	if !p.Finished && p.Position < notStarted {
		p.Position = 0
	}
	if err := a.store.SetProgress(ctx, d.ProfileID, p); err != nil {
		a.internalError(w, "keep progress", err)
		return
	}
	if p.Finished {
		p.Position = 0
	}
	writeJSON(w, http.StatusOK, progressOf(p))
}

// nearEnd reports whether a position is close enough to the end to count
// as the end: within the last twentieth, and at most the last ten minutes,
// which credits mostly take.
func nearEnd(position, duration int) bool {
	return duration > 0 && position >= duration-min(duration/20, 600)
}

// searchTitles returns the films and series whose names hold q, at most
// maxSearchTitles of each, for d.
func (a *API) searchTitles(ctx context.Context, d store.Device, q string) ([]titleRow, []titleRow, error) {
	movies, series := []titleRow{}, []titleRow{}
	if a.library == nil {
		return movies, series, nil
	}
	found, err := a.store.SearchTitles(ctx, q, maxSearchTitles)
	if err != nil {
		return nil, nil, err
	}
	locks, err := a.locksApply(ctx, d)
	if err != nil {
		return nil, nil, err
	}
	for _, t := range found {
		if t.Kind == store.KindMovie {
			movies = append(movies, row(t, locks))
		} else {
			series = append(series, row(t, locks))
		}
	}
	return movies, series, nil
}
