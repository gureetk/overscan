// SPDX-License-Identifier: AGPL-3.0-or-later

// Package jellyfin reads a Jellyfin server: its libraries, their films and
// series, a series' episodes, and the addresses of their pictures. The
// address that plays a file is the source's (source.Settings.StreamURL).
// What it expects of a server is written up in
// docs/protocol/jellyfin-api-notes.md.
//
// The API key goes into every request and into the addresses that play
// files, so no error from this package quotes an address.
package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/source"
)

// DefaultMaxBytes is the most that one answer may hold, unless a client
// sets another limit. A page of titles is well under it.
const DefaultMaxBytes = 64 << 20

// pageSize is how many titles one request asks for.
const pageSize = 500

// maxPages bounds the pages of one list, so that a server that never stops
// paging cannot keep a read going.
const maxPages = 2000

// Client reads one server with one API key, as one of its users or as the
// whole server.
type Client struct {
	http      *http.Client
	server    string // the server's address, without a slash at the end
	key       string
	user      string // a user's name, "" for every library on the server
	userID    string // the user's ID, once found
	userAgent string
	version   string
	// MaxBytes is the most that one answer may hold.
	MaxBytes int64
}

// New returns a client for a Jellyfin source's settings: the server's
// address, the API key as the password, and as the username, the user
// whose libraries to read, or none for all of them. It sends userAgent
// unless the settings name another, and tells the server the version that
// userAgent names, as in Overscan/0.2.0.
func New(settings source.Settings, client *http.Client, userAgent string) *Client {
	version := "0"
	if v, ok := strings.CutPrefix(userAgent, "Overscan/"); ok && v != "" {
		version = v
	}
	if settings.UserAgent != "" {
		userAgent = settings.UserAgent
	}
	return &Client{
		http:      client,
		server:    strings.TrimRight(settings.URL, "/"),
		key:       settings.Password,
		user:      strings.TrimSpace(settings.Username),
		userAgent: userAgent,
		version:   version,
		MaxBytes:  DefaultMaxBytes,
	}
}

// Errors that say why a server cannot be read.
var (
	// ErrKey is returned when the server refuses the API key.
	ErrKey = errors.New("the server refused the API key")
	// ErrNotJSON is returned for an answer that is not JSON, such as a web
	// page: often an address that is not a Jellyfin server's.
	ErrNotJSON = errors.New("the server did not answer with JSON; is it a Jellyfin server's address?")
	// ErrNoTitle is returned for a title that the server does not know.
	ErrNoTitle = errors.New("the server has no such title")

	errShape = errors.New("the server's answer is not in the shape that Jellyfin gives")
)

// UserError is returned when the server has no user of the name given.
type UserError struct {
	Name string
}

func (e *UserError) Error() string { return fmt.Sprintf("the server has no user named %q", e.Name) }

// Info is what a server says about itself.
type Info struct {
	Name    string // the server's name, as its owner set it
	Version string // such as 10.11.2
	ID      string
}

// Library is one of a server's libraries.
type Library struct {
	ID   string
	Name string
	// Type is what it holds, as the server says: movies, tvshows,
	// homevideos, or "" for a library of films and shows mixed.
	Type string
}

// FilmType returns the type of item that the library's films are: Video in
// a library of home videos, Movie in one of films or of both, and "" in a
// library of shows, which has none.
func (l Library) FilmType() string {
	switch l.Type {
	case "tvshows":
		return ""
	case "homevideos":
		return TypeVideo
	}
	return TypeMovie
}

// HasSeries reports whether the library may hold series: one of shows, or
// of both.
func (l Library) HasSeries() bool { return l.Type == "tvshows" || l.Type == "" || l.Type == "unknown" }

// Item is a film, a series or an episode.
type Item struct {
	ID        string
	Name      string
	Type      string // Movie, Series or Episode
	Year      int    // 0 if not known
	Rating    int    // the community rating out of 100, 0 if none
	AgeRating string // such as PG-13, "" if none
	Plot      string
	Genres    []string
	Cast      []string // actors, in the server's order
	Directors []string
	Duration  time.Duration // 0 if not known
	Added     time.Time     // when the server added it, zero if not known
	// Updated is when the server last added an episode to a series, zero
	// if not known.
	Updated   time.Time
	Premiere  time.Time // zero if not known
	Countries []string  // where it was made
	Container string    // the file's extension, such as mkv; "" if not known
	Poster    string    // the primary image's tag, "" if none
	Backdrop  string    // the first backdrop's tag, "" if none
	// Season and Number place an episode; 0 when the server does not say.
	Season     int
	Number     int
	SeasonName string
	// Missing is true for an episode that the server knows of but has no
	// file for.
	Missing bool

	extra bool // one of a title's extras, such as a trailer
}

// Info asks the server about itself, which also checks the API key.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var answer struct {
		ServerName string `json:"ServerName"`
		Version    string `json:"Version"`
		ID         string `json:"Id"`
	}
	if err := c.get(ctx, "/System/Info", nil, &answer); err != nil {
		return Info{}, err
	}
	if answer.Version == "" && answer.ID == "" {
		return Info{}, errShape
	}
	return Info{Name: clean(answer.ServerName, 80), Version: clean(answer.Version, 40), ID: clean(answer.ID, 64)}, nil
}

// findUser looks up the ID of the user that the client reads as, once.
func (c *Client) findUser(ctx context.Context) error {
	if c.user == "" || c.userID != "" {
		return nil
	}
	var users []struct {
		Name string `json:"Name"`
		ID   string `json:"Id"`
	}
	if err := c.get(ctx, "/Users", nil, &users); err != nil {
		return fmt.Errorf("users: %w", err)
	}
	for _, u := range users {
		if strings.EqualFold(strings.TrimSpace(u.Name), c.user) && u.ID != "" {
			c.userID = u.ID
			return nil
		}
	}
	return &UserError{Name: c.user}
}

// query returns q with the user's ID, if the client reads as a user.
func (c *Client) query(ctx context.Context, q url.Values) (url.Values, error) {
	if err := c.findUser(ctx); err != nil {
		return nil, err
	}
	if q == nil {
		q = url.Values{}
	}
	if c.userID != "" {
		q.Set("userId", c.userID)
	}
	return q, nil
}

// Libraries returns the libraries that may hold films or series: the user's,
// or with no user, every library on the server.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	q, err := c.query(ctx, nil)
	if err != nil {
		return nil, err
	}
	path := "/Library/MediaFolders"
	if c.userID != "" {
		path = "/UserViews"
	}
	var answer struct {
		Items []struct {
			ID             string `json:"Id"`
			Name           string `json:"Name"`
			CollectionType string `json:"CollectionType"`
		} `json:"Items"`
	}
	if err := c.get(ctx, path, q, &answer); err != nil {
		return nil, fmt.Errorf("libraries: %w", err)
	}
	var out []Library
	for _, l := range answer.Items {
		if l.ID == "" || !holdsVideo(l.CollectionType) {
			continue
		}
		out = append(out, Library{ID: l.ID, Name: clean(l.Name, 120), Type: l.CollectionType})
	}
	return out, nil
}

// holdsVideo reports whether a library of the given type may hold films or
// series. The view of folders that a user can turn on holds what the other
// libraries do, so it is left out.
func holdsVideo(collectionType string) bool {
	switch collectionType {
	case "", "unknown", "movies", "tvshows", "homevideos":
		return true
	}
	return false
}

// Item types to ask for.
const (
	TypeMovie   = "Movie"
	TypeVideo   = "Video" // a home video
	TypeSeries  = "Series"
	TypeEpisode = "Episode"
)

// fields are the details to ask for with every title.
const fields = "Overview,Genres,People,DateCreated,DateLastMediaAdded,ProductionLocations"

// Items calls each with the films, home videos or series (typ) of a
// library, a page at a time, by name. It stops at the first error that each
// returns. Entries that cannot be read are left out and counted; a title's
// extras, such as its trailers, are left out.
func (c *Client) Items(ctx context.Context, libraryID, typ string, each func(Item) error) (skipped int, err error) {
	q, err := c.query(ctx, url.Values{
		"ParentId":         {libraryID},
		"Recursive":        {"true"},
		"IncludeItemTypes": {typ},
		"Fields":           {fields},
		"SortBy":           {"SortName"},
		"SortOrder":        {"Ascending"},
		"EnableImageTypes": {"Primary,Backdrop"},
		"ImageTypeLimit":   {"1"},
		"Limit":            {strconv.Itoa(pageSize)},
	})
	if err != nil {
		return 0, err
	}
	for page, start := 0, 0; page < maxPages; page++ {
		q.Set("StartIndex", strconv.Itoa(start))
		var answer struct {
			Items []json.RawMessage `json:"Items"`
			Total int               `json:"TotalRecordCount"`
		}
		if err := c.get(ctx, "/Items", q, &answer); err != nil {
			return skipped, err
		}
		for _, raw := range answer.Items {
			it, ok := decodeItem(raw)
			switch {
			case !ok || it.Type != typ:
				skipped++
				continue
			case it.extra:
				continue
			}
			if err := each(it); err != nil {
				return skipped, err
			}
		}
		start += len(answer.Items)
		if len(answer.Items) == 0 || start >= answer.Total {
			return skipped, nil
		}
	}
	return skipped, fmt.Errorf("the server lists more than %d pages", maxPages)
}

// Item returns a film or a series with its details, or ErrNoTitle.
func (c *Client) Item(ctx context.Context, id string) (Item, error) {
	q, err := c.query(ctx, url.Values{
		"Ids":              {id},
		"Fields":           {fields},
		"EnableImageTypes": {"Primary,Backdrop"},
		"ImageTypeLimit":   {"1"},
	})
	if err != nil {
		return Item{}, err
	}
	var answer struct {
		Items []json.RawMessage `json:"Items"`
	}
	if err := c.get(ctx, "/Items", q, &answer); err != nil {
		return Item{}, err
	}
	for _, raw := range answer.Items {
		if it, ok := decodeItem(raw); ok && strings.EqualFold(it.ID, id) {
			return it, nil
		}
	}
	return Item{}, ErrNoTitle
}

// Episodes returns a series' episodes that have a file, in the server's
// order, or ErrNoTitle for a series that the server does not know.
func (c *Client) Episodes(ctx context.Context, seriesID string) ([]Item, error) {
	q, err := c.query(ctx, url.Values{
		"Fields":           {"Overview"},
		"IsMissing":        {"false"},
		"EnableImageTypes": {"Primary"},
		"ImageTypeLimit":   {"1"},
	})
	if err != nil {
		return nil, err
	}
	var answer struct {
		Items []json.RawMessage `json:"Items"`
	}
	err = c.get(ctx, "/Shows/"+url.PathEscape(seriesID)+"/Episodes", q, &answer)
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil, ErrNoTitle
	}
	if err != nil {
		return nil, err
	}
	var out []Item
	for _, raw := range answer.Items {
		if it, ok := decodeItem(raw); ok && it.Type == TypeEpisode && !it.Missing {
			out = append(out, it)
		}
	}
	return out, nil
}

// ImageURL returns the address of an item's image of the given type
// (Primary or Backdrop) with the given tag, at most width pixels wide.
// Jellyfin serves images without a key, so the address holds none.
func (c *Client) ImageURL(itemID, imageType, tag string, width int) string {
	if itemID == "" || tag == "" {
		return ""
	}
	path := "/Items/" + url.PathEscape(itemID) + "/Images/" + imageType
	if imageType == "Backdrop" {
		path += "/0"
	}
	q := url.Values{"tag": {tag}}
	if width > 0 {
		q.Set("maxWidth", strconv.Itoa(width))
	}
	return c.server + path + "?" + q.Encode()
}

// statusError is an answer other than 200.
type statusError struct {
	code   int
	status string
}

func (e *statusError) Error() string { return "the server answered " + e.status }

// get asks the server for path with query q, and decodes its JSON answer.
func (c *Client) get(ctx context.Context, path string, q url.Values, into any) error {
	address := c.server + path
	if len(q) > 0 {
		address += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return errors.New("the server's address is not valid")
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", c.authorization())
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrKey
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("the server does not let the API key read this; use an API key made by an administrator")
	case resp.StatusCode != http.StatusOK:
		return &statusError{code: resp.StatusCode, status: clean(resp.Status, 60)}
	}
	limit := c.MaxBytes
	if limit <= 0 {
		limit = DefaultMaxBytes
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	if int64(len(data)) > limit {
		return fmt.Errorf("the server's answer is larger than %d MiB", limit>>20)
	}
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if !json.Valid(data) {
		return ErrNotJSON
	}
	if err := json.Unmarshal(data, into); err != nil {
		return errShape
	}
	return nil
}

// authorization returns the Authorization header that carries the API key,
// in the form that Jellyfin reads.
func (c *Client) authorization() string {
	return `MediaBrowser Client="Overscan", Device="Overscan", DeviceId="overscan", Version="` +
		url.QueryEscape(c.version) + `", Token="` + url.QueryEscape(c.key) + `"`
}

// withoutAddress drops the address that a url.Error quotes.
func withoutAddress(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

// clean returns s on one line, without control characters, cut to n bytes.
func clean(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f || r == ' ' }), " ")
	if len(s) > n {
		s = strings.ToValidUTF8(s[:n], "")
	}
	return s
}

// Subtitle is a text subtitle track that the server gives of a film or an
// episode (D69): one in the file, or a file beside it. The server sends
// both at SubtitleURL.
type Subtitle struct {
	Index    int    // the server's index of the stream
	Codec    string // as the server names it, such as subrip, ass or srt
	Language string // such as eng; "" if not known
	Title    string
	Forced   bool
	Hearing  bool
	Default  bool
	External bool // a file beside the video
}

// Subtitles returns the text subtitle tracks of a film's or an episode's
// file, and the ID of the media source that SubtitleURL needs, or
// ErrNoTitle for an item that the server does not know. Subtitles made of
// pictures, which the box cannot show, are left out.
func (c *Client) Subtitles(ctx context.Context, id string) (string, []Subtitle, error) {
	q, err := c.query(ctx, url.Values{"Ids": {id}, "Fields": {"MediaSources"}})
	if err != nil {
		return "", nil, err
	}
	var answer struct {
		Items []struct {
			ID           string `json:"Id"`
			MediaSources []struct {
				ID           string `json:"Id"`
				MediaStreams []struct {
					Type              string `json:"Type"`
					Index             int    `json:"Index"`
					Codec             string `json:"Codec"`
					Language          string `json:"Language"`
					Title             string `json:"Title"`
					IsForced          bool   `json:"IsForced"`
					IsHearingImpaired bool   `json:"IsHearingImpaired"`
					IsDefault         bool   `json:"IsDefault"`
					IsExternal        bool   `json:"IsExternal"`
					IsText            *bool  `json:"IsTextSubtitleStream"`
				} `json:"MediaStreams"`
			} `json:"MediaSources"`
		} `json:"Items"`
	}
	if err := c.get(ctx, "/Items", q, &answer); err != nil {
		return "", nil, err
	}
	for _, it := range answer.Items {
		if !strings.EqualFold(it.ID, id) {
			continue
		}
		if len(it.MediaSources) == 0 {
			return "", nil, nil
		}
		src := it.MediaSources[0]
		var out []Subtitle
		for _, s := range src.MediaStreams {
			if s.Type != "Subtitle" || s.Index < 0 || len(out) == 64 {
				continue
			}
			codec := strings.ToLower(clean(s.Codec, 40))
			if s.IsText != nil && !*s.IsText || s.IsText == nil && !textCodec(codec) {
				continue
			}
			lang := clean(s.Language, 40)
			if lang == "und" {
				lang = ""
			}
			out = append(out, Subtitle{Index: s.Index, Codec: codec, Language: lang, Title: clean(s.Title, 120),
				Forced: s.IsForced, Hearing: s.IsHearingImpaired, Default: s.IsDefault, External: s.IsExternal})
		}
		return clean(src.ID, 64), out, nil
	}
	return "", nil, ErrNoTitle
}

// textCodec reports whether subtitles of a codec, as the server names it,
// are text: not pictures, as Blu-ray's, DVD's and DVB's are.
func textCodec(codec string) bool {
	for _, picture := range []string{"pgs", "dvdsub", "dvd_subtitle", "vobsub", "dvbsub", "dvb_subtitle", "xsub", "sup"} {
		if strings.Contains(codec, picture) {
			return false
		}
	}
	return codec != "" && codec != "sub"
}

// SubtitleURL returns the address at which the server sends a subtitle
// track of an item, in the format that the track's codec reads best as:
// SubStation Alpha's own, WebVTT's own, or else SubRip. It holds the API
// key, so it must stay on the server.
func (c *Client) SubtitleURL(itemID, mediaSourceID string, s Subtitle) (address, format string) {
	format = "srt"
	switch s.Codec {
	case "ass", "ssa":
		format = "ass"
	case "webvtt", "vtt":
		format = "vtt"
	}
	return c.server + "/Videos/" + url.PathEscape(itemID) + "/" + url.PathEscape(mediaSourceID) + "/Subtitles/" +
		strconv.Itoa(s.Index) + "/Stream." + format + "?ApiKey=" + url.QueryEscape(c.key), format
}
