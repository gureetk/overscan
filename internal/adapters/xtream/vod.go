// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Movies and series (docs/protocol/xtream-api-notes.md, section 7). A
// server lists its films and its series one list each, which can hold tens
// of thousands of entries, so a list is decoded an entry at a time as it
// arrives. A title's details, and a series' episodes, come from a call of
// their own, which Overscan makes only when a device opens the title (D63).

// DefaultMaxListBytes is the most that a list of films or series may hold,
// unless a client sets another limit.
const DefaultMaxListBytes = 512 << 20

// Limits on what is kept of a title's text.
const (
	maxNameLen  = 200
	maxShortLen = 300  // a genre, the cast, a director, a country
	maxPlotLen  = 2000 // a plot
	maxURLLen   = 4096
)

// Movie is a film as the server lists it.
type Movie struct {
	ID         int64  // the server's stream ID, which goes into the film's address
	Name       string // cleaned
	CategoryID string // "" if none
	Poster     string // a picture's address, "" if none
	Rating     int    // out of 100, 0 if none
	Year       int    // 0 if not known
	Added      time.Time
	Container  string // the file's extension, such as mkv; "" if not known
	Runtime    int    // minutes, 0 if not known
	// Some servers add these to the list; "" where they do not.
	Plot     string
	Genre    string
	Cast     string
	Director string
}

// Show is a series as the server lists it.
type Show struct {
	ID         int64 // the server's series ID
	Name       string
	CategoryID string
	Poster     string
	Backdrop   string    // a wide picture's address, "" if none
	Rating     int       // out of 100
	Year       int       // 0 if not known
	Modified   time.Time // when the server last changed it, zero if not known
	Runtime    int       // an episode's minutes, 0 if not known
	Plot       string
	Genre      string
	Cast       string
	Director   string
}

// MovieInfo is what the server says about one film.
type MovieInfo struct {
	Name      string
	Poster    string
	Backdrop  string
	Plot      string
	Genre     string
	Cast      string
	Director  string
	Country   string
	Released  string // as the server writes it, such as 2026-03-01
	AgeRating string
	Year      int
	Rating    int           // out of 100
	Duration  time.Duration // 0 if not known
	Container string        // the file's extension, "" if not known
}

// ShowInfo is what the server says about one series, with its episodes.
type ShowInfo struct {
	Name     string
	Poster   string
	Backdrop string
	Plot     string
	Genre    string
	Cast     string
	Director string
	Released string
	Year     int
	Rating   int
	Seasons  []Season  // the seasons that the server describes, by number
	Episodes []Episode // by season, then number
}

// Season is a season of a series, as the server describes it.
type Season struct {
	Number int
	Name   string
	Poster string
	Plot   string
}

// Episode is an episode of a series.
type Episode struct {
	ID        string // the server's episode ID, which goes into its address
	Season    int
	Number    int
	Name      string
	Container string
	Plot      string
	Picture   string
	Duration  time.Duration
	Added     time.Time
}

// MovieCategories returns the categories of films, in the server's order.
func (c *Client) MovieCategories(ctx context.Context) ([]Category, error) {
	return c.categories(ctx, "get_vod_categories")
}

// SeriesCategories returns the categories of series, in the server's order.
func (c *Client) SeriesCategories(ctx context.Context) ([]Category, error) {
	return c.categories(ctx, "get_series_categories")
}

func (c *Client) categories(ctx context.Context, action string) ([]Category, error) {
	raw, _, err := getList[struct {
		ID   text `json:"category_id"`
		Name text `json:"category_name"`
	}](ctx, c, c.api(action, nil))
	if err != nil {
		return nil, err
	}
	out := make([]Category, 0, len(raw))
	for _, r := range raw {
		if id := strings.TrimSpace(string(r.ID)); id != "" {
			out = append(out, Category{ID: id, Name: clean(string(r.Name), maxNameLen)})
		}
	}
	return out, nil
}

// movieEntry is a film in get_vod_streams.
type movieEntry struct {
	ID          number   `json:"stream_id"`
	Name        text     `json:"name"`
	Title       text     `json:"title"`
	Category    text     `json:"category_id"`
	Icon        text     `json:"stream_icon"`
	Rating      decimal  `json:"rating"`
	Rating5     decimal  `json:"rating_5based"`
	Year        text     `json:"year"`
	Released    text     `json:"release_date"`
	Added       number   `json:"added"`
	Container   text     `json:"container_extension"`
	Plot        text     `json:"plot"`
	Genre       text     `json:"genre"`
	Cast        text     `json:"cast"`
	Director    text     `json:"director"`
	RunTime     number   `json:"episode_run_time"`
	StreamType  text     `json:"stream_type"`
	CategoryIDs textList `json:"category_ids"`
}

// Movies reads the server's whole list of films, in one request, and calls
// each with every film that has a usable ID, in the server's order. It
// returns how many entries it left out. An error from each stops the read
// and is returned.
func (c *Client) Movies(ctx context.Context, each func(Movie) error) (int, error) {
	return eachInList(ctx, c, c.api("get_vod_streams", nil), func(e movieEntry) (bool, error) {
		m, ok := e.movie()
		if !ok {
			return false, nil
		}
		return true, each(m)
	})
}

// movie returns the film that e lists, and false for an entry without a
// usable ID or name.
func (e movieEntry) movie() (Movie, bool) {
	if e.ID <= 0 {
		return Movie{}, false
	}
	name := clean(string(e.Name), maxNameLen)
	if name == "" {
		name = clean(string(e.Title), maxNameLen)
	}
	if name == "" {
		return Movie{}, false
	}
	m := Movie{
		ID:         int64(e.ID),
		Name:       name,
		CategoryID: strings.TrimSpace(string(e.Category)),
		Poster:     address(string(e.Icon)),
		Rating:     rating(e.Rating, e.Rating5),
		Year:       year(string(e.Year), string(e.Released)),
		Added:      unixTime(int64(e.Added)),
		Container:  container(string(e.Container)),
		Plot:       clean(string(e.Plot), maxPlotLen),
		Genre:      clean(string(e.Genre), maxShortLen),
		Cast:       clean(string(e.Cast), maxShortLen),
		Director:   clean(string(e.Director), maxShortLen),
		Runtime:    minutes(int64(e.RunTime)),
	}
	if m.CategoryID == "" && len(e.CategoryIDs) > 0 {
		m.CategoryID = strings.TrimSpace(e.CategoryIDs[0])
	}
	return m, true
}

// showEntry is a series in get_series, and the info of get_series_info.
type showEntry struct {
	ID          number   `json:"series_id"`
	Name        text     `json:"name"`
	Title       text     `json:"title"`
	Category    text     `json:"category_id"`
	Cover       text     `json:"cover"`
	Backdrops   textList `json:"backdrop_path"`
	Rating      decimal  `json:"rating"`
	Rating5     decimal  `json:"rating_5based"`
	Year        text     `json:"year"`
	ReleaseDate text     `json:"releaseDate"`
	Released    text     `json:"release_date"`
	Modified    number   `json:"last_modified"`
	Plot        text     `json:"plot"`
	Genre       text     `json:"genre"`
	Cast        text     `json:"cast"`
	Director    text     `json:"director"`
	RunTime     text     `json:"episode_run_time"`
	CategoryIDs textList `json:"category_ids"`
}

func (e showEntry) released() string {
	if s := clean(string(e.ReleaseDate), 40); s != "" {
		return s
	}
	return clean(string(e.Released), 40)
}

// Series reads the server's whole list of series, in one request, and calls
// each with every series that has a usable ID, in the server's order. It
// returns how many entries it left out.
func (c *Client) Series(ctx context.Context, each func(Show) error) (int, error) {
	return eachInList(ctx, c, c.api("get_series", nil), func(e showEntry) (bool, error) {
		sh, ok := e.show()
		if !ok {
			return false, nil
		}
		return true, each(sh)
	})
}

// show returns the series that e lists, and false for an entry without a
// usable ID or name.
func (e showEntry) show() (Show, bool) {
	if e.ID <= 0 {
		return Show{}, false
	}
	name := clean(string(e.Name), maxNameLen)
	if name == "" {
		name = clean(string(e.Title), maxNameLen)
	}
	if name == "" {
		return Show{}, false
	}
	runtime, _ := strconv.Atoi(strings.TrimSpace(string(e.RunTime)))
	sh := Show{
		ID:         int64(e.ID),
		Name:       name,
		CategoryID: strings.TrimSpace(string(e.Category)),
		Poster:     address(string(e.Cover)),
		Backdrop:   firstAddress(e.Backdrops),
		Rating:     rating(e.Rating, e.Rating5),
		Year:       year(string(e.Year), e.released()),
		Modified:   unixTime(int64(e.Modified)),
		Plot:       clean(string(e.Plot), maxPlotLen),
		Genre:      clean(string(e.Genre), maxShortLen),
		Cast:       clean(string(e.Cast), maxShortLen),
		Director:   clean(string(e.Director), maxShortLen),
		Runtime:    minutes(int64(runtime)),
	}
	if sh.CategoryID == "" && len(e.CategoryIDs) > 0 {
		sh.CategoryID = strings.TrimSpace(e.CategoryIDs[0])
	}
	return sh, true
}

// ErrNoTitle is returned for a title that the server does not know.
var ErrNoTitle = errors.New("the server does not know the title")

// MovieInfo returns what the server says about a film: get_vod_info. A
// server that knows nothing about it may answer an empty info, which gives
// a MovieInfo with only what the film's own entry says.
func (c *Client) MovieInfo(ctx context.Context, id int64) (MovieInfo, error) {
	var answer struct {
		Info object[struct {
			Name        text     `json:"name"`
			OName       text     `json:"o_name"`
			CoverBig    text     `json:"cover_big"`
			MovieImage  text     `json:"movie_image"`
			Backdrops   textList `json:"backdrop_path"`
			Plot        text     `json:"plot"`
			Description text     `json:"description"`
			Genre       text     `json:"genre"`
			Cast        text     `json:"cast"`
			Actors      text     `json:"actors"`
			Director    text     `json:"director"`
			Country     text     `json:"country"`
			ReleaseDate text     `json:"releasedate"`
			Released    text     `json:"release_date"`
			Age         text     `json:"age"`
			MPAA        text     `json:"mpaa_rating"`
			Rating      decimal  `json:"rating"`
			Rating5     decimal  `json:"rating_5based"`
			DurationSec number   `json:"duration_secs"`
			Duration    text     `json:"duration"`
			RunTime     number   `json:"episode_run_time"`
		}] `json:"info"`
		MovieData object[struct {
			ID        number `json:"stream_id"`
			Name      text   `json:"name"`
			Container text   `json:"container_extension"`
		}] `json:"movie_data"`
	}
	if err := c.getTitle(ctx, c.api("get_vod_info", url.Values{"vod_id": {strconv.FormatInt(id, 10)}}), &answer); err != nil {
		return MovieInfo{}, err
	}
	in, data := answer.Info.v, answer.MovieData.v
	if !answer.Info.ok && !answer.MovieData.ok {
		return MovieInfo{}, ErrNoTitle
	}
	released := clean(string(in.ReleaseDate), 40)
	if released == "" {
		released = clean(string(in.Released), 40)
	}
	m := MovieInfo{
		Name:      clean(string(data.Name), maxNameLen),
		Poster:    address(string(in.CoverBig)),
		Backdrop:  firstAddress(in.Backdrops),
		Plot:      clean(string(in.Plot), maxPlotLen),
		Genre:     clean(string(in.Genre), maxShortLen),
		Cast:      clean(string(in.Cast), maxShortLen),
		Director:  clean(string(in.Director), maxShortLen),
		Country:   clean(string(in.Country), maxShortLen),
		Released:  released,
		AgeRating: clean(string(in.Age), 40),
		Year:      year("", released),
		Rating:    rating(in.Rating, in.Rating5),
		Duration:  duration(int64(in.DurationSec), string(in.Duration), int64(in.RunTime)),
		Container: container(string(data.Container)),
	}
	if m.Name == "" {
		m.Name = clean(string(in.Name), maxNameLen)
	}
	if m.Poster == "" {
		m.Poster = address(string(in.MovieImage))
	}
	if m.Plot == "" {
		m.Plot = clean(string(in.Description), maxPlotLen)
	}
	if m.Cast == "" {
		m.Cast = clean(string(in.Actors), maxShortLen)
	}
	if m.AgeRating == "" {
		m.AgeRating = clean(string(in.MPAA), 40)
	}
	return m, nil
}

// episodeEntry is an episode in get_series_info.
type episodeEntry struct {
	ID        text   `json:"id"`
	Number    number `json:"episode_num"`
	Title     text   `json:"title"`
	Container text   `json:"container_extension"`
	Season    number `json:"season"`
	Added     number `json:"added"`
	Info      object[struct {
		Plot        text   `json:"plot"`
		MovieImage  text   `json:"movie_image"`
		CoverBig    text   `json:"cover_big"`
		DurationSec number `json:"duration_secs"`
		Duration    text   `json:"duration"`
		Name        text   `json:"name"`
	}] `json:"info"`
}

// seasonEntry is a season in get_series_info.
type seasonEntry struct {
	Number   number `json:"season_number"`
	Name     text   `json:"name"`
	Cover    text   `json:"cover"`
	CoverBig text   `json:"cover_big"`
	Overview text   `json:"overview"`
}

// maxEpisodes caps the episodes kept of one series.
const maxEpisodes = 5000

// SeriesInfo returns what the server says about a series, with its
// episodes: get_series_info. Servers key episodes by season number, or list
// them by season, or in one list.
func (c *Client) SeriesInfo(ctx context.Context, id int64) (ShowInfo, error) {
	var answer seriesAnswer
	if err := c.getTitle(ctx, c.api("get_series_info", url.Values{"series_id": {strconv.FormatInt(id, 10)}}), &answer); err != nil {
		return ShowInfo{}, err
	}
	return answer.show()
}

// seriesAnswer is the answer of get_series_info.
type seriesAnswer struct {
	Info     object[showEntry] `json:"info"`
	Seasons  json.RawMessage   `json:"seasons"`
	Episodes episodeLists      `json:"episodes"`
}

// show returns what a get_series_info answer says.
func (answer seriesAnswer) show() (ShowInfo, error) {
	if !answer.Info.ok && len(answer.Episodes) == 0 {
		return ShowInfo{}, ErrNoTitle
	}
	in := answer.Info.v
	s := ShowInfo{
		Name:     clean(string(in.Name), maxNameLen),
		Poster:   address(string(in.Cover)),
		Backdrop: firstAddress(in.Backdrops),
		Plot:     clean(string(in.Plot), maxPlotLen),
		Genre:    clean(string(in.Genre), maxShortLen),
		Cast:     clean(string(in.Cast), maxShortLen),
		Director: clean(string(in.Director), maxShortLen),
		Released: in.released(),
		Rating:   rating(in.Rating, in.Rating5),
	}
	s.Year = year(string(in.Year), s.Released)
	if s.Name == "" {
		s.Name = clean(string(in.Title), maxNameLen)
	}

	var seasons []json.RawMessage
	_ = json.Unmarshal(answer.Seasons, &seasons) // an object, null or false: no seasons
	described := map[int]bool{}
	for _, raw := range seasons {
		var e seasonEntry
		if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &e) != nil {
			continue
		}
		n := int(e.Number)
		if n < 0 || n > 10_000 || described[n] {
			continue
		}
		described[n] = true
		poster := address(string(e.CoverBig))
		if poster == "" {
			poster = address(string(e.Cover))
		}
		s.Seasons = append(s.Seasons, Season{Number: n, Name: clean(string(e.Name), maxNameLen),
			Poster: poster, Plot: clean(string(e.Overview), maxPlotLen)})
	}
	sort.SliceStable(s.Seasons, func(i, j int) bool { return s.Seasons[i].Number < s.Seasons[j].Number })

	seen := map[string]bool{}
	for _, l := range answer.Episodes {
		for _, e := range l.episodes {
			id := strings.TrimSpace(string(e.ID))
			if id == "" || len(id) > 18 || strings.Trim(id, "0123456789") != "" || seen[id] {
				continue
			}
			if len(s.Episodes) == maxEpisodes {
				break
			}
			seen[id] = true
			season := int(e.Season)
			if season <= 0 {
				season = l.season
			}
			ep := Episode{
				ID:        id,
				Season:    max(0, min(season, 10_000)),
				Number:    int(max(0, min(e.Number, 100_000))),
				Name:      clean(string(e.Title), maxNameLen),
				Container: container(string(e.Container)),
				Plot:      clean(string(e.Info.v.Plot), maxPlotLen),
				Picture:   address(string(e.Info.v.MovieImage)),
				Duration:  duration(int64(e.Info.v.DurationSec), string(e.Info.v.Duration), 0),
				Added:     unixTime(int64(e.Added)),
			}
			if ep.Name == "" {
				ep.Name = clean(string(e.Info.v.Name), maxNameLen)
			}
			if ep.Picture == "" {
				ep.Picture = address(string(e.Info.v.CoverBig))
			}
			s.Episodes = append(s.Episodes, ep)
		}
	}
	sort.SliceStable(s.Episodes, func(i, j int) bool {
		a, b := s.Episodes[i], s.Episodes[j]
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		return a.Number < b.Number
	})
	return s, nil
}

// episodeLists reads a series' episodes, which servers send as an object
// keyed by season number, as a list of lists, one a season, or as one list.
type episodeLists []episodeList

type episodeList struct {
	season   int // from the key or the place in the list, 0 if not known
	episodes []episodeEntry
}

func (l *episodeLists) UnmarshalJSON(b []byte) error {
	*l = nil
	b = bytes.TrimSpace(b)
	switch {
	case len(b) > 0 && b[0] == '{':
		var bySeason map[string]json.RawMessage
		if json.Unmarshal(b, &bySeason) != nil {
			return nil
		}
		for key, raw := range bySeason {
			n, _ := strconv.Atoi(strings.TrimSpace(key))
			*l = append(*l, episodeList{season: n, episodes: entries[episodeEntry](raw)})
		}
		sort.Slice(*l, func(i, j int) bool { return (*l)[i].season < (*l)[j].season })
	case len(b) > 0 && b[0] == '[':
		var items []json.RawMessage
		if json.Unmarshal(b, &items) != nil {
			return nil
		}
		var flat []episodeEntry
		for i, raw := range items {
			t := bytes.TrimSpace(raw)
			switch {
			case len(t) > 0 && t[0] == '[':
				*l = append(*l, episodeList{season: i + 1, episodes: entries[episodeEntry](t)})
			case len(t) > 0 && t[0] == '{':
				var e episodeEntry
				if json.Unmarshal(t, &e) == nil {
					flat = append(flat, e)
				}
			}
		}
		if len(flat) > 0 {
			*l = append(*l, episodeList{episodes: flat})
		}
	}
	return nil
}

// entries decodes the objects in a JSON list, leaving out anything else.
func entries[T any](raw []byte) []T {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	out := make([]T, 0, len(items))
	for _, it := range items {
		var v T
		if t := bytes.TrimSpace(it); len(t) > 0 && t[0] == '{' && json.Unmarshal(t, &v) == nil {
			out = append(out, v)
		}
	}
	return out
}

// getTitle fetches what a server says about one title into into. An answer
// that is not an object, such as the empty list or null that some servers
// send for a title they do not know, is ErrNoTitle.
func (c *Client) getTitle(ctx context.Context, address string, into any) error {
	var raw json.RawMessage
	if err := c.get(ctx, address, &raw); err != nil {
		return err
	}
	if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' {
		return ErrNoTitle
	}
	if err := json.Unmarshal(raw, into); err != nil {
		return errShape
	}
	return nil
}

// object reads a JSON object into v; anything else, such as the empty list
// that some servers send for a title without details, leaves ok false.
type object[T any] struct {
	v  T
	ok bool
}

func (o *object[T]) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '{' && json.Unmarshal(b, &o.v) == nil {
		o.ok = true
	}
	return nil
}

// eachInList fetches a list and decodes it an entry at a time as it
// arrives, so that a list of tens of thousands of entries never sits whole
// in memory. It calls each with every entry that is an object; each says
// whether it kept the entry. It returns how many entries were left out.
func eachInList[T any](ctx context.Context, c *Client, address string, each func(T) (bool, error)) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return 0, errors.New("the server's address is not valid")
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("the server answered %s", clean(resp.Status, 60))
	}
	limit := c.MaxListBytes
	if limit <= 0 {
		limit = DefaultMaxListBytes
	}
	return decodeList(resp.Body, limit, each)
}

// decodeList decodes a JSON list from r an entry at a time, as eachInList
// says, reading at most limit bytes.
func decodeList[T any](r io.Reader, limit int64, each func(T) (bool, error)) (int, error) {
	capped := &limitedReader{r: r, left: limit}
	br := bufio.NewReaderSize(capped, 64<<10)
	if bom, _ := br.Peek(3); bytes.Equal(bom, []byte("\xef\xbb\xbf")) {
		_, _ = br.Discard(3)
	}
	broken := func(err error) error {
		switch {
		case capped.over:
			return fmt.Errorf("the server's answer is larger than %d MiB", limit>>20)
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			return err
		}
		var uerr *url.Error
		if errors.As(err, &uerr) {
			return fmt.Errorf("fetch: %w", uerr.Err)
		}
		return errBroken
	}
	if first := firstByte(br); first != '[' && first != '{' && first != 'n' && first != 'f' {
		if first == 0 {
			return 0, broken(io.ErrUnexpectedEOF)
		}
		return 0, ErrNotJSON
	}
	dec := json.NewDecoder(br)
	tok, err := dec.Token()
	if err != nil {
		return 0, broken(err)
	}
	switch tok {
	case json.Delim('['):
	case nil, false:
		return 0, nil // nothing to list
	case json.Delim('{'):
		// An empty object for an empty list; anything else is not a list.
		if end, err := dec.Token(); err == nil && end == json.Delim('}') {
			return 0, nil
		}
		return 0, errShape
	default:
		return 0, errShape
	}
	skipped := 0
	for dec.More() {
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return skipped, broken(err)
		}
		var v T
		if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '{' || json.Unmarshal(t, &v) != nil {
			skipped++
			continue
		}
		kept, err := each(v)
		if err != nil {
			return skipped, err
		}
		if !kept {
			skipped++
		}
	}
	if _, err := dec.Token(); err != nil {
		return skipped, broken(err)
	}
	return skipped, nil
}

// firstByte returns the first byte of br that is not white space, without
// reading past it, or 0 at the end.
func firstByte(br *bufio.Reader) byte {
	for {
		b, err := br.Peek(1)
		if err != nil {
			return 0
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = br.Discard(1)
		default:
			return b[0]
		}
	}
}

// limitedReader fails once more than left bytes have been read.
type limitedReader struct {
	r    io.Reader
	left int64
	over bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.over {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.left+1 {
		p = p[:l.left+1]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	if l.left < 0 {
		l.over = true
		return n, errTooLarge
	}
	return n, err
}

var errTooLarge = errors.New("the answer is too large")

// address returns s if it is an http or https address that is not too long,
// and "" otherwise.
func address(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > maxURLLen {
		return ""
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return s
	}
	return ""
}

// firstAddress returns the first usable address in a list.
func firstAddress(l textList) string {
	for _, s := range l {
		if a := address(s); a != "" {
			return a
		}
	}
	return ""
}

// rating returns a rating out of 100 from one out of 10, or else one out
// of 5.
func rating(ten, five decimal) int {
	switch {
	case ten > 0 && ten <= 10:
		return int(float64(ten)*10 + 0.5)
	case five > 0 && five <= 5:
		return int(float64(five)*20 + 0.5)
	}
	return 0
}

// year returns the year in y, or else at the start of a date such as
// 2026-03-01; 0 if neither has one.
func year(y, date string) int {
	for _, s := range []string{strings.TrimSpace(y), strings.TrimSpace(date)} {
		if len(s) >= 4 {
			if n, err := strconv.Atoi(s[:4]); err == nil && n >= 1870 && n <= 2200 && (len(s) == 4 || s[4] < '0' || s[4] > '9') {
				return n
			}
		}
	}
	return 0
}

// unixTime returns Unix seconds as a time, zero for none or for one that
// cannot be right.
func unixTime(n int64) time.Time {
	if n <= 0 || n >= 1<<40 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// minutes returns a running time in minutes, 0 for one that cannot be
// right.
func minutes(n int64) int {
	if n <= 0 || n > 1000 {
		return 0
	}
	return int(n)
}

// duration returns a running time from seconds, or else from a time such as
// 01:52:10, or else from minutes.
func duration(secs int64, hms string, mins int64) time.Duration {
	if secs > 0 && secs < 100*3600 {
		return time.Duration(secs) * time.Second
	}
	if parts := strings.Split(strings.TrimSpace(hms), ":"); len(parts) == 3 {
		total := 0
		for _, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 || n > 99 {
				total = -1
				break
			}
			total = total*60 + n
		}
		if total > 0 {
			return time.Duration(total) * time.Second
		}
	}
	if m := minutes(mins); m > 0 {
		return time.Duration(m) * time.Minute
	}
	return 0
}

// container returns a file's extension, lower-cased, if it is a plain one
// of at most five letters and digits, such as mkv or mp4; "" otherwise.
func container(s string) string {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "."))
	if s == "" || len(s) > 5 {
		return ""
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return s
}
