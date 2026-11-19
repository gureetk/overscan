// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Films and series (section 6). A portal's lists come 14 items a page; a
// series' seasons and episodes come in one of three shapes: inline, with the
// episodes' numbers on the series' item; on request, with movie_id,
// season_id and episode_id; or in a series module of its own.

// The modules that hold films and series.
const (
	ModuleVOD    = "vod"
	ModuleSeries = "series"
)

// maxSeasonPages caps the pages read of a series' seasons or a season's
// episodes.
const maxSeasonPages = 20

// Categories returns a module's categories, vod or series, without "All".
// ErrNoAction says that the portal lacks the module.
func (c *Client) Categories(ctx context.Context, module string) ([]Genre, error) {
	var items list[genreJSON]
	if err := c.call(ctx, module, "get_categories", nil, &items); err != nil {
		return nil, err
	}
	return genres(items), nil
}

// Item is a film or a series in a list (section 6).
type Item struct {
	ID          string
	Name        string
	Cmd         string // what create_link takes to play it
	CategoryID  string
	Poster      string // an absolute address, "" if none
	Description string
	Actors      string
	Director    string
	Genres      string
	Year        int // 0 if not known
	Minutes     int // its length, 0 if not known
	Rating      int // out of 100, 0 if not known
	Age         string
	Added       time.Time // zero if not known
	Censored    bool
	// HasFiles says that the film's file is asked for apart (MovieFile).
	HasFiles bool
	// IsSeries says that the item is a series whose seasons come on
	// request (Seasons).
	IsSeries bool
	// Series are the numbers of an inline series' episodes, which its cmd
	// plays with create_link's series parameter.
	Series []int
}

// itemJSON is a film, a series, a season or an episode as a portal sends
// them.
type itemJSON struct {
	ID            text    `json:"id"`
	Name          text    `json:"name"`
	OName         text    `json:"o_name"`
	Cmd           text    `json:"cmd"`
	CategoryID    text    `json:"category_id"`
	Screenshot    text    `json:"screenshot_uri"`
	Description   text    `json:"description"`
	Actors        text    `json:"actors"`
	Director      text    `json:"director"`
	Genres        text    `json:"genres_str"`
	Year          text    `json:"year"`
	Time          text    `json:"time"`
	RatingIMDB    decimal `json:"rating_imdb"`
	RatingKP      decimal `json:"rating_kinopoisk"`
	Age           text    `json:"age"`
	Added         text    `json:"added"`
	Censored      flag    `json:"censored"`
	HasFiles      flag    `json:"has_files"`
	IsSeries      flag    `json:"is_series"`
	Series        numbers `json:"series"`
	IsSeason      flag    `json:"is_season"`
	SeasonNumber  number  `json:"season_number"`
	IsEpisode     flag    `json:"is_episode"`
	EpisodeNumber number  `json:"series_number"`
}

func (c *Client) item(it itemJSON) (Item, bool) {
	out := Item{
		ID:          strings.TrimSpace(string(it.ID)),
		Name:        strings.TrimSpace(string(it.Name)),
		Cmd:         strings.TrimSpace(string(it.Cmd)),
		CategoryID:  strings.TrimSpace(string(it.CategoryID)),
		Poster:      c.resolve(string(it.Screenshot)),
		Description: strings.TrimSpace(string(it.Description)),
		Actors:      strings.TrimSpace(string(it.Actors)),
		Director:    strings.TrimSpace(string(it.Director)),
		Genres:      strings.TrimSpace(string(it.Genres)),
		Year:        year(string(it.Year)),
		Minutes:     minutes(string(it.Time)),
		Rating:      rating(it.RatingIMDB, it.RatingKP),
		Age:         strings.TrimSpace(string(it.Age)),
		Censored:    bool(it.Censored),
		HasFiles:    bool(it.HasFiles),
		IsSeries:    bool(it.IsSeries),
		Series:      it.Series,
	}
	if out.Name == "" {
		out.Name = strings.TrimSpace(string(it.OName))
	}
	out.Added, _ = ParseDate(string(it.Added))
	if out.ID == "" || out.Name == "" || len(out.Cmd) > 4096 {
		return Item{}, false
	}
	return out, true
}

// year reads a year, which may start a date.
func year(s string) int {
	s = strings.TrimSpace(s)
	if len(s) < 4 {
		return 0
	}
	n, err := strconv.Atoi(s[:4])
	if err != nil || n < 1870 || n > 2100 || len(s) > 4 && unicode.IsDigit(rune(s[4])) {
		return 0
	}
	return n
}

// minutes reads a length in minutes, or as HH:MM:SS (or HH:MM).
func minutes(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if !strings.Contains(s, ":") {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 || n > 100*60 {
			return 0
		}
		return n
	}
	parts := strings.Split(s, ":")
	if len(parts) > 3 {
		return 0
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || (i > 0 && n > 59) {
			return 0
		}
		v[i] = n
	}
	m := v[0]*60 + v[1]
	if len(parts) == 3 && v[2] >= 30 {
		m++
	}
	if m > 100*60 {
		return 0
	}
	return m
}

// rating is a rating out of 10 as one out of 100: IMDb's, else
// Kinopoisk's.
func rating(ratings ...decimal) int {
	for _, r := range ratings {
		if r > 0 && r <= 10 {
			return int(float64(r)*10 + 0.5)
		}
	}
	return 0
}

// Page is a page of a list of films or series.
type Page struct {
	Total int // the items in the whole list
	Items []Item
	// Skipped counts the items on the page that could not be read.
	Skipped int
}

// Titles reads page p, from 1, of a module's list in a category ("*" for
// all), the latest added first (section 6).
func (c *Client) Titles(ctx context.Context, module, category string, p int) (Page, error) {
	var page pageJSON[itemJSON]
	err := c.call(ctx, module, "get_ordered_list", url.Values{"category": {category}, "genre": {"*"},
		"sortby": {"added"}, "fav": {"0"}, "p": {strconv.Itoa(max(1, p))}}, &page)
	if err != nil {
		return Page{}, err
	}
	out := Page{Total: max(0, int(page.TotalItems))}
	for _, it := range page.Data {
		if item, ok := c.item(it); ok {
			out.Items = append(out.Items, item)
		} else {
			out.Skipped++
		}
	}
	return out, nil
}

// Season is a season of a series.
type Season struct {
	ID     string
	Number int // from the portal, else from its name, else its place
	Name   string
	// Cmd and Episodes are a season of the series module: its cmd, and the
	// numbers of its episodes, which the cmd plays with create_link's series
	// parameter. A season on request has neither: Episodes lists them.
	Cmd      string
	Episodes []int
}

// seasons reads every page of a series' seasons.
func (c *Client) seasons(ctx context.Context, module string, params url.Values) ([]Season, error) {
	var out []Season
	seen := map[string]bool{}
	for p := 1; p <= maxSeasonPages; p++ {
		params.Set("p", strconv.Itoa(p))
		var page pageJSON[itemJSON]
		if err := c.call(ctx, module, "get_ordered_list", params, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Data {
			id := strings.TrimSpace(string(it.ID))
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			s := Season{ID: id, Name: strings.TrimSpace(string(it.Name)), Cmd: strings.TrimSpace(string(it.Cmd)),
				Episodes: it.Series}
			switch {
			case it.SeasonNumber > 0 && it.SeasonNumber < 1000:
				s.Number = int(it.SeasonNumber)
			case firstNumber(s.Name) > 0:
				s.Number = firstNumber(s.Name)
			default:
				s.Number = len(out) + 1
			}
			out = append(out, s)
		}
		if len(page.Data) == 0 || int64(len(out)) >= int64(page.TotalItems) {
			break
		}
	}
	return out, nil
}

// firstNumber returns the first whole number in s below 1000, such as 2 in
// "Season 2", or 0.
func firstNumber(s string) int {
	start := strings.IndexFunc(s, unicode.IsDigit)
	if start < 0 {
		return 0
	}
	end := start
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	n, err := strconv.Atoi(s[start:end])
	if err != nil || n >= 1000 {
		return 0
	}
	return n
}

// SeriesSeasons returns a series' seasons as a series module gives them
// (section 6): series/get_ordered_list with movie_id, each season with its
// cmd and its episodes' numbers.
func (c *Client) SeriesSeasons(ctx context.Context, seriesID string) ([]Season, error) {
	return c.seasons(ctx, ModuleSeries, url.Values{"movie_id": {seriesID}})
}

// Seasons returns the seasons of a series whose seasons come on request
// (is_series): module/get_ordered_list with movie_id, season_id=0 and
// episode_id=0 (section 6).
func (c *Client) Seasons(ctx context.Context, module, seriesID string) ([]Season, error) {
	return c.seasons(ctx, module, url.Values{"movie_id": {seriesID}, "season_id": {"0"}, "episode_id": {"0"}})
}

// Episode is an episode of a season on request.
type Episode struct {
	ID          string
	Number      int // series_number, else its place
	Name        string
	Minutes     int
	Picture     string // an absolute address, "" if none
	Description string
	Cmd         string // what create_link takes to play it
}

// Episodes returns a season's episodes, for a series whose seasons come on
// request: module/get_ordered_list with movie_id and season_id (section 6).
func (c *Client) Episodes(ctx context.Context, module, seriesID, seasonID string) ([]Episode, error) {
	var out []Episode
	seen := map[string]bool{}
	params := url.Values{"movie_id": {seriesID}, "season_id": {seasonID}, "episode_id": {"0"}}
	for p := 1; p <= maxSeasonPages; p++ {
		params.Set("p", strconv.Itoa(p))
		var page pageJSON[itemJSON]
		if err := c.call(ctx, module, "get_ordered_list", params, &page); err != nil {
			return nil, err
		}
		for _, it := range page.Data {
			id, cmd := strings.TrimSpace(string(it.ID)), strings.TrimSpace(string(it.Cmd))
			if id == "" || seen[id] || len(cmd) > 4096 {
				continue
			}
			seen[id] = true
			e := Episode{ID: id, Name: strings.TrimSpace(string(it.Name)), Minutes: minutes(string(it.Time)),
				Picture: c.resolve(string(it.Screenshot)), Description: strings.TrimSpace(string(it.Description)), Cmd: cmd}
			e.Number = len(out) + 1
			if it.EpisodeNumber > 0 && it.EpisodeNumber < 100_000 {
				e.Number = int(it.EpisodeNumber)
			}
			out = append(out, e)
		}
		if len(page.Data) == 0 || int64(len(out)) >= int64(page.TotalItems) {
			break
		}
	}
	return out, nil
}

// ErrNoFile is returned for a film whose files the portal lists none of.
var ErrNoFile = errors.New("the portal lists no file for the film")

// MovieFile returns the cmd that plays a film whose file is asked for apart
// (has_files): vod/get_ordered_list with movie_id gives its first file's
// ID, and the cmd is /media/file_<id>.mpg (section 6).
func (c *Client) MovieFile(ctx context.Context, movieID string) (string, error) {
	var page pageJSON[itemJSON]
	err := c.call(ctx, ModuleVOD, "get_ordered_list", url.Values{"movie_id": {movieID}, "p": {"1"}}, &page)
	if err != nil {
		return "", err
	}
	for _, it := range page.Data {
		id := strings.TrimSpace(string(it.ID))
		if id != "" && !strings.ContainsAny(id, "/?#. ") && len(id) <= 64 {
			return "/media/file_" + id + ".mpg", nil
		}
	}
	return "", ErrNoFile
}

// VODLink asks the portal for the address that plays a film or an episode
// now: vod/create_link with its cmd, and series=<n> for episode n of an
// inline series or of a season of the series module (section 6). A
// *LinkError says why the portal gave none.
func (c *Client) VODLink(ctx context.Context, cmd string, episode int) (string, error) {
	series := ""
	if episode > 0 {
		series = strconv.Itoa(episode)
	}
	var answer linkJSON
	err := c.call(ctx, ModuleVOD, "create_link", url.Values{"cmd": {cmd}, "series": {series},
		"forced_storage": {"undefined"}, "disable_ad": {"0"}, "download": {"0"}}, &answer)
	if err != nil {
		return "", err
	}
	return c.link(answer)
}
