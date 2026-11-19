// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tmdb"
)

// TMDB's details for the titles in folders (D88): with the administrator's
// key, a folder's film or series that lacks a poster or a plot gets them,
// and what else its .nfo file and pictures leave out, from TMDB. What the
// folder has always comes first.

const (
	// tmdbFoundTTL and tmdbMissTTL are how long what TMDB said of a title
	// is kept before TMDB is asked again: a match, and none.
	tmdbFoundTTL = 30 * 24 * time.Hour
	tmdbMissTTL  = 7 * 24 * time.Hour
	// tmdbBatch is the most titles that one run in the background looks
	// up; the next read of the folder goes on with the rest.
	tmdbBatch = 500
	// tmdbSeasons is the most seasons of a series whose episodes are read.
	tmdbSeasons = 30
)

// tmdbPace is the least time between two requests to TMDB.
var tmdbPace = 300 * time.Millisecond

// tmdbWant is a folder's title to look up on TMDB.
type tmdbWant struct {
	key  string
	name string
	year int
	id   int64 // the TMDB ID that the folder gives, 0 if none
}

// tmdbClient returns a client with the administrator's key, or nil when
// there is none.
func (l *Library) tmdbClient(ctx context.Context) (*tmdb.Client, error) {
	key, err := l.store.TMDBKey(ctx)
	if err != nil || key == "" {
		return nil, err
	}
	language, err := l.store.TMDBLanguage(ctx)
	if err != nil {
		return nil, err
	}
	return tmdb.New(key, language, l.userAgent).WithBase(l.tmdbBase), nil
}

// tmdbDue says whether TMDB is to be asked again about a title.
func (l *Library) tmdbDue(t store.TMDBTitle, asked bool) bool {
	if !asked {
		return true
	}
	ttl := tmdbFoundTTL
	if t.ID == 0 {
		ttl = tmdbMissTTL
	}
	return l.now().Sub(t.CheckedAt) >= ttl
}

// startTMDB looks titles up on TMDB in the background, one request at a
// time, unless a run for the source's films or series is under way. When
// TMDB found any, the folder is read again, which adds what it found.
func (l *Library) startTMDB(src store.Source, kind string, wants []tmdbWant) {
	key := listKey{src.ID, kind}
	l.mu.Lock()
	if l.tmdbRuns[key] || l.base.Err() != nil {
		l.mu.Unlock()
		return
	}
	l.tmdbRuns[key] = true
	l.running.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.running.Done()
		found := l.lookUpTMDB(src, kind, wants)
		l.mu.Lock()
		delete(l.tmdbRuns, key)
		l.mu.Unlock()
		if found == 0 {
			return
		}
		// The read that started this run may not have ended yet: a new read
		// is what adds what TMDB found.
		if p := l.startList(src, kind, false); p != nil {
			select {
			case <-p.done:
			case <-l.base.Done():
				return
			}
		}
		l.startList(src, kind, true)
	}()
}

// lookUpTMDB looks titles up on TMDB and keeps what it says, and returns
// how many it found. It stops at the first sign that TMDB will not answer.
func (l *Library) lookUpTMDB(src store.Source, kind string, wants []tmdbWant) int {
	ctx := l.base
	c, err := l.tmdbClient(ctx)
	if err != nil || c == nil {
		return 0
	}
	if len(wants) > tmdbBatch {
		wants = wants[:tmdbBatch]
	}
	what := map[string]string{store.KindMovie: "films", store.KindSeries: "series"}[kind]
	start := l.now()
	found, asked := 0, 0
	for _, w := range wants {
		t, err := l.findTMDB(ctx, c, kind, w.name, w.year, w.id)
		if err != nil {
			if ctx.Err() == nil {
				l.log.Warn("cannot look a source's "+what+" up on TMDB; the next read of the folder tries again",
					"source", src.Name, "title", w.name, "error", err)
			}
			break
		}
		if err := l.store.SetTMDBTitle(ctx, src.ID, kind, w.key, t); err != nil {
			l.log.Error("cannot keep what TMDB says of a title", "source", src.Name, "title", w.name, "error", err)
			break
		}
		asked++
		if t.ID != 0 {
			found++
		}
	}
	if asked > 0 {
		l.log.Info("looked a source's "+what+" up on TMDB", "source", src.Name, "titles", asked, "found", found,
			"left", len(wants)-asked, "took", l.now().Sub(start).Round(time.Millisecond))
	}
	return found
}

// pace waits between two requests to TMDB.
func pace(ctx context.Context) error {
	if tmdbPace <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(tmdbPace)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// findTMDB finds a film or a series on TMDB, by the ID that its folder
// gives or else by its name and year, and returns what TMDB says of it: a
// title with ID 0 when TMDB has no match.
func (l *Library) findTMDB(ctx context.Context, c *tmdb.Client, kind, name string, year int, id int64) (store.TMDBTitle, error) {
	miss := store.TMDBTitle{CheckedAt: l.now()}
	if id == 0 {
		var rs []tmdb.Result
		var err error
		if kind == store.KindMovie {
			rs, err = c.SearchMovie(ctx, name, year)
		} else {
			rs, err = c.SearchShow(ctx, name, year)
		}
		if err != nil {
			return miss, err
		}
		var ok bool
		if id, ok = tmdb.Pick(rs, name, year); !ok {
			return miss, pace(ctx)
		}
		if err := pace(ctx); err != nil {
			return miss, err
		}
	}
	var t store.TMDBTitle
	if kind == store.KindMovie {
		m, err := c.Movie(ctx, id)
		if errors.Is(err, tmdb.ErrNotFound) {
			return miss, pace(ctx)
		}
		if err != nil {
			return miss, err
		}
		t = store.TMDBTitle{ID: id, Details: movieDetails(m), CheckedAt: l.now()}
	} else {
		s, err := c.Show(ctx, id)
		if errors.Is(err, tmdb.ErrNotFound) {
			return miss, pace(ctx)
		}
		if err != nil {
			return miss, err
		}
		t = store.TMDBTitle{ID: id, Details: showDetails(s), CheckedAt: l.now()}
	}
	return t, pace(ctx)
}

// tmdbEpisodes reads from TMDB the episodes of a series' seasons that a
// folder has, at most tmdbSeasons of them.
func (l *Library) tmdbEpisodes(ctx context.Context, c *tmdb.Client, id int64, seasons []int) ([]store.TMDBEpisode, error) {
	var out []store.TMDBEpisode
	for i, n := range seasons {
		if i == tmdbSeasons {
			break
		}
		s, err := c.Season(ctx, id, n)
		if errors.Is(err, tmdb.ErrNotFound) {
			continue
		}
		if err != nil {
			return out, err
		}
		for _, e := range s.Episodes {
			out = append(out, store.TMDBEpisode{Season: n, Number: e.EpisodeNumber, Name: clip(e.Name, maxName),
				Plot: clip(e.Overview, maxPlot), Picture: tmdb.Image(e.StillPath, tmdb.StillSize), Runtime: e.Runtime})
		}
		if err := pace(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}

// Limits on what is kept of TMDB's text.
const (
	maxName   = 200
	maxPlot   = 4000
	maxPeople = 8
)

// clip trims a text from TMDB, leaves out control characters, and cuts it
// to at most n bytes.
func clip(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// people returns the names of a title's first actors, and its directors.
func people(cr tmdb.Credits) (cast, directors string) {
	var names []string
	for _, a := range cr.Cast {
		if len(names) == maxPeople {
			break
		}
		if n := clip(a.Name, maxName); n != "" {
			names = append(names, n)
		}
	}
	var dirs []string
	for _, p := range cr.Crew {
		if p.Job == "Director" && len(dirs) < 4 {
			if n := clip(p.Name, maxName); n != "" {
				dirs = append(dirs, n)
			}
		}
	}
	return strings.Join(names, ", "), strings.Join(dirs, ", ")
}

func genres(gs []tmdb.Named) string {
	var out []string
	for _, g := range gs {
		if n := clip(g.Name, maxName); n != "" && len(out) < 10 {
			out = append(out, n)
		}
	}
	return strings.Join(out, ", ")
}

// rating turns TMDB's mark out of 10 into one out of 100.
func rating(v float64) int {
	return int(math.Round(math.Max(0, math.Min(10, v)) * 10))
}

// date keeps a date of the form 2024-05-01, and nothing else.
func date(s string) string {
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		return ""
	}
	return s
}

// movieDetails returns what Overscan keeps of a film's details on TMDB.
func movieDetails(m tmdb.Movie) store.TitleDetails {
	cast, directors := people(m.Credits)
	return store.TitleDetails{
		TitleSummary: store.TitleSummary{Plot: clip(m.Overview, maxPlot), Genre: genres(m.Genres), Cast: cast,
			Director: directors, Backdrop: tmdb.Image(m.BackdropPath, tmdb.BackdropSize), Runtime: max(0, m.Runtime)},
		Poster:   tmdb.Image(m.PosterPath, tmdb.PosterSize),
		Released: date(m.ReleaseDate),
		Year:     tmdb.Year(m.ReleaseDate),
		Rating:   rating(m.VoteAverage),
		Duration: max(0, m.Runtime) * 60,
	}
}

// showDetails returns what Overscan keeps of a series' details on TMDB,
// with its seasons.
func showDetails(s tmdb.Show) store.TitleDetails {
	cast, directors := people(s.Credits)
	d := store.TitleDetails{
		TitleSummary: store.TitleSummary{Plot: clip(s.Overview, maxPlot), Genre: genres(s.Genres), Cast: cast,
			Director: directors, Backdrop: tmdb.Image(s.BackdropPath, tmdb.BackdropSize)},
		Poster:   tmdb.Image(s.PosterPath, tmdb.PosterSize),
		Released: date(s.FirstAirDate),
		Year:     tmdb.Year(s.FirstAirDate),
		Rating:   rating(s.VoteAverage),
	}
	for _, se := range s.Seasons {
		d.Seasons = append(d.Seasons, store.SeasonDetails{Number: se.SeasonNumber, Name: clip(se.Name, maxName),
			Plot: clip(se.Overview, maxPlot), Poster: tmdb.Image(se.PosterPath, tmdb.PosterSize)})
	}
	return d
}

// fillSummary fills what a summary lacks from another.
func fillSummary(s *store.TitleSummary, from store.TitleSummary) {
	s.Plot = cmpOr(s.Plot, from.Plot)
	s.Genre = cmpOr(s.Genre, from.Genre)
	s.Cast = cmpOr(s.Cast, from.Cast)
	s.Director = cmpOr(s.Director, from.Director)
	s.Backdrop = cmpOr(s.Backdrop, from.Backdrop)
	if s.Runtime == 0 {
		s.Runtime = from.Runtime
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// withTMDB fills what a folder's title lacks on a shelf from TMDB.
func withTMDB(t *store.LibraryTitle, d store.TitleDetails) {
	t.Poster = cmpOr(t.Poster, d.Poster)
	if t.Year == 0 {
		t.Year = d.Year
	}
	if t.Rating == 0 {
		t.Rating = d.Rating
	}
	fillSummary(&t.Summary, d.TitleSummary)
}

// fillDetails fills what a folder's title lacks when it is opened from
// TMDB, its seasons included.
func fillDetails(d *store.TitleDetails, from store.TitleDetails) {
	fillSummary(&d.TitleSummary, from.TitleSummary)
	d.Poster = cmpOr(d.Poster, from.Poster)
	d.Released = cmpOr(d.Released, from.Released)
	if d.Year == 0 {
		d.Year = from.Year
	}
	if d.Rating == 0 {
		d.Rating = from.Rating
	}
	if d.Duration == 0 {
		d.Duration = from.Duration
	}
	for i := range d.Seasons {
		for _, s := range from.Seasons {
			if s.Number == d.Seasons[i].Number {
				d.Seasons[i].Name = cmpOr(d.Seasons[i].Name, s.Name)
				d.Seasons[i].Plot = cmpOr(d.Seasons[i].Plot, s.Plot)
				d.Seasons[i].Poster = cmpOr(d.Seasons[i].Poster, s.Poster)
			}
		}
	}
}

// fillEpisodes fills what a folder's episodes lack from TMDB.
func fillEpisodes(eps []store.LibraryEpisode, from []store.TMDBEpisode) {
	type at struct{ season, number int }
	byNumber := map[at]store.TMDBEpisode{}
	for _, e := range from {
		byNumber[at{e.Season, e.Number}] = e
	}
	for i := range eps {
		e, ok := byNumber[at{eps[i].Season, eps[i].Number}]
		if !ok {
			continue
		}
		eps[i].Name = cmpOr(eps[i].Name, e.Name)
		eps[i].Plot = cmpOr(eps[i].Plot, e.Plot)
		eps[i].Picture = cmpOr(eps[i].Picture, e.Picture)
		if eps[i].Duration == 0 {
			eps[i].Duration = e.Runtime * 60
		}
	}
}
