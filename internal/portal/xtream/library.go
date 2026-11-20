// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/portal/output"
	"overscan.gureet.ca/internal/store"
)

// Films and series: the library's lists, read when a player first asks for
// them as when a box does (D63), and a title's details when a player opens
// it. A Stalker portal's categories are read a page at a time as a box
// browses them (D77); a list of every film at once would read every page,
// which looks like a crawl to a provider, so they are left out.

// titlePage is how many titles a list reads from the store at a time.
const titlePage = 500

// defaultContainer is the extension of a file whose source gives none.
const defaultContainer = "mp4"

// shelves are the categories of films or of series that an account's
// players see: their IDs by name, and those of portals, left out.
type shelves struct {
	list   []category
	byName map[string]string
	portal map[int64]bool
}

// of returns the ID of a title's category, "" if it has none, and whether
// it is a portal's.
func (s shelves) of(t store.TitleListing) (string, bool) {
	if s.portal[t.CategoryID] {
		return "", true
	}
	return s.byName[t.Shelf], false
}

// ready reads the library's state for a kind, starting the reads that are
// due and waiting a while for a first one, and the shelves that the
// account's players see. It returns false if there is nothing to list.
func (o *Output) ready(ctx context.Context, acct store.Account, kind string) (shelves, bool, error) {
	out := shelves{list: []category{}, byName: map[string]string{}, portal: map[int64]bool{}}
	if o.library == nil {
		return out, false, nil
	}
	state, err := o.library.Want(ctx, kind, libraryWait)
	if err != nil || !state.Ready() {
		return out, false, err
	}
	all, err := o.store.Shelves(ctx, kind)
	if err != nil {
		return out, false, err
	}
	for _, s := range all {
		switch {
		case s.Portal:
			out.portal[s.ID] = true
		case s.Locked && acct.PINHash != "":
			// Its titles are left out with the other locked ones.
		default:
			id := strconv.FormatInt(s.ID, 10)
			out.byName[s.Name] = id
			out.list = append(out.list, category{ID: id, Name: s.Name})
		}
	}
	return out, true, nil
}

// categories answers get_vod_categories and get_series_categories: the
// shelves of films or of series, as the box's Movies and Series show them.
func (o *Output) categories(w http.ResponseWriter, r *http.Request, acct store.Account, kind string) {
	s, _, err := o.ready(r.Context(), acct, kind)
	if err != nil {
		o.libraryFailed(w, r, "look at the library", err)
		return
	}
	writeJSON(w, s.list)
}

// libraryFailed answers a call whose read of the library failed. A player
// that went away needs no answer.
func (o *Output) libraryFailed(w http.ResponseWriter, r *http.Request, what string, err error) {
	if r.Context().Err() != nil {
		return
	}
	o.failed(w, what, err)
}

// details returns what a title's details say, before what its list says.
type details struct {
	plot, genre, cast, director, released, country, age string
	poster, backdrop                                    string // the source's addresses
	year, rating, runtime, duration                     int    // rating out of 100, runtime in minutes, duration in seconds
}

func detailsOf(t store.TitleRow, summary store.TitleSummary, d *store.TitleDetails) details {
	if d == nil {
		d = &store.TitleDetails{}
	}
	out := details{plot: first(d.Plot, summary.Plot), genre: first(d.Genre, summary.Genre),
		cast: first(d.Cast, summary.Cast), director: first(d.Director, summary.Director), released: d.Released,
		country: d.Country, age: d.AgeRating, poster: first(d.Poster, t.Poster),
		backdrop: first(d.Backdrop, summary.Backdrop), year: t.Year, rating: t.Rating, duration: d.Duration}
	if d.Year > 0 {
		out.year = d.Year
	}
	if d.Rating > 0 {
		out.rating = d.Rating
	}
	out.runtime = first(d.Runtime, summary.Runtime)
	if d.Duration > 0 {
		out.runtime = (d.Duration + 30) / 60
	}
	return out
}

// first returns the first of its arguments that is not the zero value.
func first[T comparable](v ...T) T {
	var zero T
	for _, x := range v {
		if x != zero {
			return x
		}
	}
	return zero
}

// rating10 is a rating out of 100 as one out of 10, such as "7.4", or ""
// for none; rating5 the same out of 5, as a number.
func rating10(r int) string {
	if r <= 0 {
		return ""
	}
	return strconv.Itoa(r/10) + "." + strconv.Itoa(r%10)
}

func rating5(r int) float64 { return float64(r/2) / 10 }

// year is a year as a string, "" if not known.
func year(n int) string {
	if n <= 0 {
		return ""
	}
	return strconv.Itoa(n)
}

// unix is a time in Unix seconds as a string, "" if not known.
func unix(t store.TitleRow) string {
	if t.AddedAt.IsZero() {
		return ""
	}
	return strconv.FormatInt(t.AddedAt.Unix(), 10)
}

// picture is the address on Overscan of a title's or an episode's picture,
// "" if it has none.
func picture(base string, kind artwork.Kind, id int64, address string) string {
	if path := artwork.TitlePath(kind, id, address); path != "" {
		return base + path
	}
	return ""
}

// pictures is a list of the address of a picture, empty for none.
func pictures(address string) []string {
	if address == "" {
		return []string{}
	}
	return []string{address}
}

// hhmmss is a duration in seconds as HH:MM:SS, "" if not known.
func hhmmss(seconds int) string {
	if seconds <= 0 {
		return ""
	}
	return fmt.Sprintf("%02d:%02d:%02d", seconds/3600, seconds/60%60, seconds%60)
}

// categoryOf is a title's category_id and category_ids.
func categoryOf(id string) (*string, []int64) {
	if id == "" {
		return nil, []int64{}
	}
	n, _ := strconv.ParseInt(id, 10, 64)
	return &id, []int64{n}
}

// film is a film as get_vod_streams lists it (notes, section 7).
type film struct {
	Num                int     `json:"num"`
	Name               string  `json:"name"`
	Title              string  `json:"title"`
	Year               string  `json:"year"`
	StreamType         string  `json:"stream_type"`
	StreamID           int64   `json:"stream_id"`
	StreamIcon         string  `json:"stream_icon"`
	Rating             string  `json:"rating"`
	Rating5            float64 `json:"rating_5based"`
	Added              string  `json:"added"`
	CategoryID         *string `json:"category_id"`
	CategoryIDs        []int64 `json:"category_ids"`
	ContainerExtension string  `json:"container_extension"`
	CustomSID          string  `json:"custom_sid"`
	DirectSource       string  `json:"direct_source"`
	Plot               string  `json:"plot"`
	Genre              string  `json:"genre"`
	Cast               string  `json:"cast"`
	Director           string  `json:"director"`
	ReleaseDate        string  `json:"release_date"`
	EpisodeRunTime     string  `json:"episode_run_time"`
}

// series is a series as get_series lists it, and as get_series_info
// describes it (notes, section 7).
type series struct {
	Num            int      `json:"num"`
	Name           string   `json:"name"`
	Title          string   `json:"title"`
	Year           string   `json:"year"`
	StreamType     string   `json:"stream_type"`
	SeriesID       int64    `json:"series_id"`
	Cover          string   `json:"cover"`
	Plot           string   `json:"plot"`
	Cast           string   `json:"cast"`
	Director       string   `json:"director"`
	Genre          string   `json:"genre"`
	ReleaseDateCC  string   `json:"releaseDate"`
	ReleaseDate    string   `json:"release_date"`
	LastModified   string   `json:"last_modified"`
	Rating         string   `json:"rating"`
	Rating5        float64  `json:"rating_5based"`
	BackdropPath   []string `json:"backdrop_path"`
	YouTubeTrailer string   `json:"youtube_trailer"`
	EpisodeRunTime string   `json:"episode_run_time"`
	CategoryID     *string  `json:"category_id"`
	CategoryIDs    []int64  `json:"category_ids"`
}

// filmOf turns a film in a list into its Xtream shape.
func filmOf(base string, t store.TitleListing, categoryID string) film {
	d := detailsOf(t.TitleRow, t.Summary, t.Details)
	f := film{Name: t.Name, Title: t.Name, Year: year(d.year), StreamType: "movie", StreamID: t.ID,
		StreamIcon: picture(base, artwork.PosterKind, t.ID, d.poster), Rating: rating10(d.rating),
		Rating5: rating5(d.rating), Added: unix(t.TitleRow), ContainerExtension: first(t.Container, defaultContainer),
		Plot: d.plot, Genre: d.genre, Cast: d.cast, Director: d.director, ReleaseDate: d.released}
	if d.runtime > 0 {
		f.EpisodeRunTime = strconv.Itoa(d.runtime)
	}
	f.CategoryID, f.CategoryIDs = categoryOf(categoryID)
	return f
}

// seriesOf turns a series into its Xtream shape.
func seriesOf(base string, t store.TitleListing, categoryID string) series {
	d := detailsOf(t.TitleRow, t.Summary, t.Details)
	s := series{Name: t.Name, Title: t.Name, Year: year(d.year), StreamType: "series", SeriesID: t.ID,
		Cover: picture(base, artwork.PosterKind, t.ID, d.poster), Plot: d.plot, Cast: d.cast, Director: d.director,
		Genre: d.genre, ReleaseDateCC: d.released, ReleaseDate: d.released, LastModified: unix(t.TitleRow),
		Rating: rating10(d.rating), Rating5: rating5(d.rating),
		BackdropPath: pictures(picture(base, artwork.BackdropKind, t.ID, d.backdrop))}
	if d.runtime > 0 {
		s.EpisodeRunTime = strconv.Itoa(d.runtime)
	}
	s.CategoryID, s.CategoryIDs = categoryOf(categoryID)
	return s
}

// films answers get_vod_streams, of every film or of one category's.
func (o *Output) films(w http.ResponseWriter, r *http.Request, acct store.Account) {
	o.titles(w, r, acct, store.KindMovie, func(base string, t store.TitleListing, num int, categoryID string) any {
		f := filmOf(base, t, categoryID)
		f.Num = num
		return f
	})
}

// seriesList answers get_series, of every series or of one category's.
func (o *Output) seriesList(w http.ResponseWriter, r *http.Request, acct store.Account) {
	o.titles(w, r, acct, store.KindSeries, func(base string, t store.TitleListing, num int, categoryID string) any {
		s := seriesOf(base, t, categoryID)
		s.Num = num
		return s
	})
}

// titles answers a list of films or of series, the latest added first, as
// the box's lists show them, a page of the store at a time. With a
// category_id, only that category's; without the locked ones once the
// account has a parental PIN, since a player cannot ask for it (D85).
func (o *Output) titles(w http.ResponseWriter, r *http.Request, acct store.Account, kind string,
	shape func(base string, t store.TitleListing, num int, categoryID string) any) {
	ctx := r.Context()
	s, ok, err := o.ready(ctx, acct, kind)
	if err != nil {
		o.libraryFailed(w, r, "look at the library", err)
		return
	}
	var shelfID int64
	if only := r.Form.Get("category_id"); only != "" && only != "*" && only != "0" {
		found := false
		for _, c := range s.list {
			if c.ID == only {
				found = true
			}
		}
		if !found {
			writeJSON(w, []any{})
			return
		}
		shelfID, _ = strconv.ParseInt(only, 10, 64)
	}
	if !ok {
		writeJSON(w, []any{})
		return
	}
	// The first page is read before anything is sent, so that a failure
	// can still be answered as one.
	locks := acct.PINHash != ""
	page, _, err := o.store.TitlePage(ctx, kind, shelfID, "", store.ByAdded, 0, titlePage, locks)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, []any{})
		return
	}
	if err != nil {
		o.libraryFailed(w, r, "list the library", err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	base := output.Base(r)
	enc := json.NewEncoder(w)
	_, _ = w.Write([]byte("["))
	num := 0
	for offset := 0; ; offset += titlePage {
		if offset > 0 {
			if page, _, err = o.store.TitlePage(ctx, kind, shelfID, "", store.ByAdded, offset, titlePage, locks); err != nil {
				if ctx.Err() == nil {
					// The answer has begun: all that can be done is to
					// stop it.
					o.log.Warn("the Xtream output's list stopped part way", "kind", kind, "error", err)
				}
				return
			}
		}
		for _, t := range page {
			id, isPortal := s.of(t)
			if isPortal {
				continue
			}
			if num > 0 {
				_, _ = w.Write([]byte(","))
			}
			num++
			if err := enc.Encode(shape(base, t, num, id)); err != nil {
				return
			}
		}
		if len(page) < titlePage {
			break
		}
	}
	_, _ = w.Write([]byte("]"))
}

// open returns a title that a player opened, of a kind, with its details,
// and a series with its episodes, read from its source if Overscan has none
// or they are old, as when a box opens it (D63), or what Overscan has if
// the source takes longer than openWait. It returns store.ErrNotFound for
// a title that is not of the kind, that players do not see, or that is
// locked once the account has a PIN.
func (o *Output) open(ctx context.Context, acct store.Account, idText, kind string) (library.Opened, error) {
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 || o.library == nil {
		return library.Opened{}, store.ErrNotFound
	}
	t, err := o.store.Title(ctx, id)
	if err != nil {
		return library.Opened{}, err
	}
	if t.Kind != kind || t.Locked && acct.PINHash != "" {
		return library.Opened{}, store.ErrNotFound
	}
	readCtx, cancel := context.WithTimeout(ctx, openWait)
	defer cancel()
	opened, err := o.library.Open(readCtx, id)
	if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		opened = library.Opened{Title: t}
		if kind == store.KindSeries {
			opened.Episodes, err = o.store.Episodes(ctx, id)
		} else {
			err = nil
		}
	}
	if err == nil && opened.Kind != kind {
		err = store.ErrNotFound
	}
	return opened, err
}

// categoryID returns the category_id of an opened title, "" if it has none
// or it is on a shelf that the account's players do not see.
func (o *Output) categoryID(ctx context.Context, acct store.Account, t store.Title) string {
	s, _, err := o.ready(ctx, acct, t.Kind)
	if err != nil {
		return ""
	}
	id, _ := s.of(store.TitleListing{Shelf: t.Shelf, CategoryID: t.CategoryID})
	return id
}

// filmInfo answers get_vod_info: a film's details, read from its source as
// when a box opens it, and what plays it.
func (o *Output) filmInfo(w http.ResponseWriter, r *http.Request, acct store.Account) {
	ctx := r.Context()
	opened, err := o.open(ctx, acct, r.Form.Get("vod_id"), store.KindMovie)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, map[string]any{"info": []any{}, "movie_data": []any{}})
		return
	}
	if err != nil {
		o.libraryFailed(w, r, "open a film", err)
		return
	}
	base := output.Base(r)
	t := opened.Title
	d := detailsOf(t.TitleRow, t.Summary, t.Details)
	poster := picture(base, artwork.PosterKind, t.ID, d.poster)
	duration := d.duration
	if duration == 0 && d.runtime > 0 {
		duration = d.runtime * 60
	}
	category := o.categoryID(ctx, acct, t)
	writeJSON(w, map[string]any{
		"info": map[string]any{
			"name":          t.Name,
			"o_name":        t.Name,
			"cover_big":     poster,
			"movie_image":   poster,
			"releasedate":   d.released,
			"director":      d.director,
			"actors":        d.cast,
			"cast":          d.cast,
			"description":   d.plot,
			"plot":          d.plot,
			"age":           d.age,
			"country":       d.country,
			"genre":         d.genre,
			"backdrop_path": pictures(picture(base, artwork.BackdropKind, t.ID, d.backdrop)),
			"duration_secs": duration,
			"duration":      hhmmss(duration),
			"rating":        rating10(d.rating),
		},
		"movie_data": map[string]any{
			"stream_id":           t.ID,
			"name":                t.Name,
			"added":               unix(t.TitleRow),
			"category_id":         category,
			"container_extension": first(t.Container, defaultContainer),
			"custom_sid":          "",
			"direct_source":       "",
		},
	})
}

// season is a season of a series as get_series_info gives it.
type season struct {
	Number   int    `json:"season_number"`
	Name     string `json:"name"`
	Overview string `json:"overview"`
	Cover    string `json:"cover"`
	CoverBig string `json:"cover_big"`
	Episodes int    `json:"episode_count"`
}

// episode is an episode as get_series_info gives it.
type episode struct {
	ID                 string      `json:"id"`
	Num                int         `json:"episode_num"`
	Title              string      `json:"title"`
	ContainerExtension string      `json:"container_extension"`
	Season             int         `json:"season"`
	Added              string      `json:"added"`
	CustomSID          string      `json:"custom_sid"`
	DirectSource       string      `json:"direct_source"`
	Info               episodeInfo `json:"info"`
}

type episodeInfo struct {
	Plot         string `json:"plot"`
	MovieImage   string `json:"movie_image"`
	DurationSecs int    `json:"duration_secs"`
	Duration     string `json:"duration"`
}

// seriesInfo answers get_series_info: a series' details and its episodes
// by season, read from its source as when a box opens it.
func (o *Output) seriesInfo(w http.ResponseWriter, r *http.Request, acct store.Account) {
	ctx := r.Context()
	opened, err := o.open(ctx, acct, r.Form.Get("series_id"), store.KindSeries)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, map[string]any{"seasons": []any{}, "info": []any{}, "episodes": []any{}})
		return
	}
	if err != nil {
		o.libraryFailed(w, r, "open a series", err)
		return
	}
	base := output.Base(r)
	t := opened.Title
	listing := store.TitleListing{TitleRow: t.TitleRow, Shelf: t.Shelf, CategoryID: t.CategoryID, Summary: t.Summary,
		Details: t.Details}
	info := seriesOf(base, listing, o.categoryID(ctx, acct, t))
	described := map[int]store.SeasonDetails{}
	if t.Details != nil {
		for _, s := range t.Details.Seasons {
			described[s.Number] = s
		}
	}
	seasons := []season{}
	episodes := map[string][]episode{}
	for _, s := range numbered(opened.Episodes) {
		key := strconv.Itoa(s.number)
		seasons = append(seasons, season{Number: s.number, Name: seasonName(s.number, described[s.number].Name),
			Overview: described[s.number].Plot, Cover: info.Cover, CoverBig: info.Cover, Episodes: len(s.episodes)})
		list := make([]episode, 0, len(s.episodes))
		for i, e := range s.episodes {
			list = append(list, episode{ID: strconv.FormatInt(e.ID, 10), Num: s.numbers[i],
				Title: first(e.Name, "Episode "+strconv.Itoa(s.numbers[i])), Season: s.number,
				ContainerExtension: first(e.Container, defaultContainer), Added: info.LastModified,
				Info: episodeInfo{Plot: e.Plot, MovieImage: picture(base, artwork.StillKind, e.ID, e.Picture),
					DurationSecs: e.Duration, Duration: hhmmss(e.Duration)}})
		}
		episodes[key] = list
	}
	writeJSON(w, map[string]any{"seasons": seasons, "info": info, "episodes": episodes})
}

// numberedSeason is a season's episodes, and the number each goes by.
type numberedSeason struct {
	number   int
	episodes []store.Episode
	numbers  []int
}

// numbered groups a series' episodes, which come by season and number,
// into seasons. An episode goes by its own number, unless its season has
// an episode without one or two with the same one: the season's episodes
// then go by their places in it, from 1, as for Stalker clients.
func numbered(episodes []store.Episode) []numberedSeason {
	var out []numberedSeason
	for _, e := range episodes {
		if len(out) == 0 || out[len(out)-1].number != e.Season {
			out = append(out, numberedSeason{number: e.Season})
		}
		s := &out[len(out)-1]
		s.episodes = append(s.episodes, e)
	}
	for i := range out {
		s := &out[i]
		seen := map[int]bool{}
		own := true
		for _, e := range s.episodes {
			if e.Number <= 0 || seen[e.Number] {
				own = false
				break
			}
			seen[e.Number] = true
		}
		s.numbers = make([]int, len(s.episodes))
		for j, e := range s.episodes {
			s.numbers[j] = j + 1
			if own {
				s.numbers[j] = e.Number
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
