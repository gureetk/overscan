// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tmdb reads the details of films and series from The Movie
// Database (TMDB), with the administrator's own key (D88), as
// docs/protocol/tmdb-notes.md describes its API. It never puts the key in
// an error or a log line.
package tmdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DefaultBase is the address of TMDB's API, version 3.
const DefaultBase = "https://api.themoviedb.org/3"

// imageBase is where TMDB's pictures are, before a size and a path.
const imageBase = "https://image.tmdb.org/t/p/"

// Notice is the notice that TMDB asks applications that use its API to show.
const Notice = "This product uses the TMDB API but is not endorsed or certified by TMDB."

// Picture sizes that Overscan asks for.
const (
	PosterSize   = "w342"
	BackdropSize = "w1280"
	StillSize    = "w300"
)

var (
	// ErrKey is TMDB refusing the key.
	ErrKey = errors.New("TMDB refused the key")
	// ErrNotFound is TMDB knowing no title of that ID.
	ErrNotFound = errors.New("TMDB has no title of that ID")
	// ErrBusy is TMDB asking for fewer requests.
	ErrBusy = errors.New("TMDB asked for fewer requests")
)

// maxBody is the most of an answer that is read.
const maxBody = 4 << 20

// Client asks TMDB's API with one key, in one language.
type Client struct {
	key      string
	language string
	base     string
	agent    string
	http     *http.Client
}

// New returns a client with the administrator's key: an API key, sent as
// the api_key parameter, or an API read access token, sent as a bearer
// token. language, such as en-GB, may be empty for TMDB's own default.
func New(key, language, userAgent string) *Client {
	return &Client{key: strings.TrimSpace(key), language: language, base: DefaultBase, agent: userAgent,
		http: &http.Client{Timeout: 20 * time.Second}}
}

// WithBase returns the client asking another address, such as a test's.
func (c *Client) WithBase(base string) *Client {
	n := *c
	n.base = strings.TrimSuffix(base, "/")
	return &n
}

// bearer says whether the key is a read access token, which has the form of
// a JSON web token, rather than an API key.
func (c *Client) bearer() bool {
	return strings.HasPrefix(c.key, "eyJ") && strings.Count(c.key, ".") == 2
}

// ValidLanguage says whether s is a language TMDB takes: two letters, and
// an optional region, such as en or en-GB.
func ValidLanguage(s string) bool {
	return languageForm.MatchString(s)
}

var languageForm = regexp.MustCompile(`^[a-z]{2}(-[A-Z]{2})?$`)

// get asks TMDB for path with the query, and decodes its answer into v.
func (c *Client) get(ctx context.Context, path string, q url.Values, v any) error {
	if q == nil {
		q = url.Values{}
	}
	if c.language != "" {
		q.Set("language", c.language)
	}
	if !c.bearer() {
		q.Set("api_key", c.key)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return errors.New("cannot make a request to TMDB")
	}
	req.Header.Set("Accept", "application/json")
	if c.agent != "" {
		req.Header.Set("User-Agent", c.agent)
	}
	if c.bearer() {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error would name the address, with the key in it.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("cannot reach TMDB: %s", strings.ReplaceAll(err.Error(), c.key, "…"))
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return ErrKey
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusTooManyRequests:
		return ErrBusy
	default:
		return fmt.Errorf("TMDB answered %d", resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(v); err != nil {
		return errors.New("TMDB's answer could not be read")
	}
	return nil
}

// Check asks TMDB for its configuration, which needs a key that it takes.
func (c *Client) Check(ctx context.Context) error {
	var v map[string]any
	return c.get(ctx, "/configuration", nil, &v)
}

// Named is a genre.
type Named struct {
	Name string `json:"name"`
}

// Credits are a title's cast and crew.
type Credits struct {
	Cast []struct {
		Name      string `json:"name"`
		Character string `json:"character"`
		Order     int    `json:"order"`
	} `json:"cast"`
	Crew []struct {
		Name string `json:"name"`
		Job  string `json:"job"`
	} `json:"crew"`
}

// Movie is a film's details, with its credits.
type Movie struct {
	ID            int64   `json:"id"`
	Title         string  `json:"title"`
	OriginalTitle string  `json:"original_title"`
	Overview      string  `json:"overview"`
	ReleaseDate   string  `json:"release_date"`
	Runtime       int     `json:"runtime"` // minutes
	Genres        []Named `json:"genres"`
	PosterPath    string  `json:"poster_path"`
	BackdropPath  string  `json:"backdrop_path"`
	VoteAverage   float64 `json:"vote_average"` // out of 10
	Credits       Credits `json:"credits"`
}

// Show is a series' details, with its credits and its seasons.
type Show struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	OriginalName string   `json:"original_name"`
	Overview     string   `json:"overview"`
	FirstAirDate string   `json:"first_air_date"`
	Genres       []Named  `json:"genres"`
	PosterPath   string   `json:"poster_path"`
	BackdropPath string   `json:"backdrop_path"`
	VoteAverage  float64  `json:"vote_average"`
	Credits      Credits  `json:"credits"`
	Seasons      []Season `json:"seasons"`
}

// Season is a season of a series; a season's details hold its episodes.
type Season struct {
	SeasonNumber int       `json:"season_number"`
	Name         string    `json:"name"`
	Overview     string    `json:"overview"`
	PosterPath   string    `json:"poster_path"`
	Episodes     []Episode `json:"episodes"`
}

// Episode is an episode of a season.
type Episode struct {
	EpisodeNumber int    `json:"episode_number"`
	Name          string `json:"name"`
	Overview      string `json:"overview"`
	StillPath     string `json:"still_path"`
	AirDate       string `json:"air_date"`
	Runtime       int    `json:"runtime"` // minutes
}

// Result is a film or a series that a search found.
type Result struct {
	ID            int64  `json:"id"`
	Title         string `json:"title"`
	OriginalTitle string `json:"original_title"`
	ReleaseDate   string `json:"release_date"`
	Name          string `json:"name"`
	OriginalName  string `json:"original_name"`
	FirstAirDate  string `json:"first_air_date"`
}

type results struct {
	Results []Result `json:"results"`
}

// SearchMovie searches the films whose title holds query, of the year if it
// is not 0, leaving out those for adults.
func (c *Client) SearchMovie(ctx context.Context, query string, year int) ([]Result, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("year", strconv.Itoa(year))
	}
	var r results
	err := c.get(ctx, "/search/movie", q, &r)
	return r.Results, err
}

// SearchShow searches the series whose name holds query, first shown in the
// year if it is not 0, leaving out those for adults.
func (c *Client) SearchShow(ctx context.Context, query string, year int) ([]Result, error) {
	q := url.Values{"query": {query}, "include_adult": {"false"}}
	if year > 0 {
		q.Set("first_air_date_year", strconv.Itoa(year))
	}
	var r results
	err := c.get(ctx, "/search/tv", q, &r)
	return r.Results, err
}

// Movie returns a film's details and credits.
func (c *Client) Movie(ctx context.Context, id int64) (Movie, error) {
	var m Movie
	err := c.get(ctx, "/movie/"+strconv.FormatInt(id, 10), url.Values{"append_to_response": {"credits"}}, &m)
	return m, err
}

// Show returns a series' details, credits and seasons.
func (c *Client) Show(ctx context.Context, id int64) (Show, error) {
	var s Show
	err := c.get(ctx, "/tv/"+strconv.FormatInt(id, 10), url.Values{"append_to_response": {"credits"}}, &s)
	return s, err
}

// Season returns a season of a series with its episodes.
func (c *Client) Season(ctx context.Context, id int64, number int) (Season, error) {
	var s Season
	err := c.get(ctx, "/tv/"+strconv.FormatInt(id, 10)+"/season/"+strconv.Itoa(number), nil, &s)
	return s, err
}

// Image returns the address of a picture of the given size, or "" for a
// path that is not one.
func Image(path, size string) string {
	if !imagePath.MatchString(path) {
		return ""
	}
	return imageBase + size + path
}

var imagePath = regexp.MustCompile(`^/[A-Za-z0-9_-]{1,64}\.(?:jpg|jpeg|png|svg)$`)

// Year returns the year of a date such as 2024-05-01, or 0.
func Year(date string) int {
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y < 1870 || y > 2200 {
		return 0
	}
	return y
}

// Pick returns the result whose title, or original title, is name, ignoring
// case and punctuation, and whose year is within one of year when year is
// not 0. Where none has that title, a search that found a single title of
// that year picks it. A wrong match would show another title's poster, so
// anything else picks none.
func Pick(rs []Result, name string, year int) (int64, bool) {
	want := fold(name)
	near := func(date string) bool {
		y := Year(date)
		return year == 0 || (y != 0 && y >= year-1 && y <= year+1)
	}
	for _, r := range rs {
		title, original, date := r.Title, r.OriginalTitle, r.ReleaseDate
		if title == "" {
			title, original, date = r.Name, r.OriginalName, r.FirstAirDate
		}
		if want != "" && (fold(title) == want || fold(original) == want) && near(date) {
			return r.ID, true
		}
	}
	if len(rs) == 1 && year != 0 {
		date := rs[0].ReleaseDate
		if date == "" {
			date = rs[0].FirstAirDate
		}
		if Year(date) == year {
			return rs[0].ID, true
		}
	}
	return 0, false
}

// fold lowers a name and keeps its letters and digits, with single spaces
// between words.
func fold(s string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		case r == '&':
			if b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteString("and")
			space = true
		default:
			space = true
		}
	}
	return b.String()
}
