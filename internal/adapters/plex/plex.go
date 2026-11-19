// SPDX-License-Identifier: AGPL-3.0-or-later

// Package plex reads a Plex Media Server: its libraries, their films and
// series, a series' episodes, and the addresses of their pictures and of
// the subtitle files beside their videos. The address that plays a file is
// the source's (source.Settings.StreamURL). What it expects of a server is
// written up in docs/protocol/plex-api-notes.md.
//
// The token goes into every request and into the addresses of pictures and
// subtitles, so no error from this package quotes an address.
package plex

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

// Client reads one server with one token, as the account that the token
// belongs to.
type Client struct {
	http      *http.Client
	server    string // the server's address, without a slash at the end
	token     string
	userAgent string
	version   string
	// MaxBytes is the most that one answer may hold.
	MaxBytes int64
}

// New returns a client for a Plex source's settings: the server's address,
// and the token as the password. It sends userAgent unless the settings
// name another, and tells the server the version that userAgent names, as
// in Overscan/0.2.0.
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
		token:     settings.Password,
		userAgent: userAgent,
		version:   version,
		MaxBytes:  DefaultMaxBytes,
	}
}

// Errors that say why a server cannot be read.
var (
	// ErrToken is returned when the server refuses the token.
	ErrToken = errors.New("the server refused the token")
	// ErrNotJSON is returned for an answer that is not JSON, such as a web
	// page: often an address that is not a Plex server's.
	ErrNotJSON = errors.New("the server did not answer with JSON; is it a Plex server's address?")
	// ErrNoTitle is returned for a title that the server does not know.
	ErrNoTitle = errors.New("the server has no such title")

	errShape = errors.New("the server's answer is not in the shape that Plex gives")
)

// Info is what a server says about itself.
type Info struct {
	Name    string // the server's name, as its owner set it
	Version string // such as 1.42.2.10156-f737b826c
	ID      string // the server's machine identifier
}

// Info asks the server about itself, which also checks the token.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var answer struct {
		MediaContainer struct {
			FriendlyName      text `json:"friendlyName"`
			Version           text `json:"version"`
			MachineIdentifier text `json:"machineIdentifier"`
		} `json:"MediaContainer"`
	}
	if err := c.get(ctx, "/", nil, &answer); err != nil {
		return Info{}, err
	}
	m := answer.MediaContainer
	if m.MachineIdentifier == "" && m.Version == "" {
		return Info{}, errShape
	}
	return Info{Name: clean(string(m.FriendlyName), 120), Version: clean(string(m.Version), 60),
		ID: clean(string(m.MachineIdentifier), 80)}, nil
}

// Library is one of a server's libraries of films or of shows.
type Library struct {
	Key  string // the server's key for it, such as 3
	Name string
	// Type is what it holds, as the server says: movie or show.
	Type string
}

// Library types, as the server names them.
const (
	TypeMovie   = "movie"
	TypeShow    = "show"
	TypeEpisode = "episode"
)

// Libraries returns the server's libraries of films and of shows, in the
// server's order. Libraries of music and of photos are left out.
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var answer struct {
		MediaContainer struct {
			Directory []struct {
				Key   text `json:"key"`
				Title text `json:"title"`
				Type  text `json:"type"`
			} `json:"Directory"`
		} `json:"MediaContainer"`
	}
	err := c.get(ctx, "/library/sections/all", nil, &answer)
	var status *statusError
	if errors.As(err, &status) && (status.code == http.StatusNotFound || status.code == http.StatusBadRequest) {
		// A server older than the documented list, or an account that is
		// not the owner's, has the list here.
		err = c.get(ctx, "/library/sections", nil, &answer)
	}
	if err != nil {
		return nil, err
	}
	var out []Library
	for _, d := range answer.MediaContainer.Directory {
		typ := strings.ToLower(string(d.Type))
		key := string(d.Key)
		if (typ != TypeMovie && typ != TypeShow) || !validKey(key) {
			continue
		}
		name := clean(string(d.Title), 120)
		if name == "" {
			name = "Library " + key
		}
		out = append(out, Library{Key: key, Name: name, Type: typ})
	}
	return out, nil
}

// Items lists a library's films or shows, as typ says (TypeMovie or
// TypeShow), page by page, and calls each for each in the server's order.
// It stops at the first error that each returns. Entries that are not
// titles of that type, or that lack a key or a name, are skipped and
// counted.
func (c *Client) Items(ctx context.Context, libraryKey, typ string, each func(Item) error) (skipped int, err error) {
	if !validKey(libraryKey) {
		return 0, errors.New("the library's key is not valid")
	}
	number := map[string]string{TypeMovie: "1", TypeShow: "2"}[typ]
	if number == "" {
		return 0, fmt.Errorf("cannot list items of type %q", typ)
	}
	return c.pages(ctx, "/library/sections/"+libraryKey+"/all", url.Values{"type": {number}}, typ, each)
}

// Item returns a film, a show or an episode with all the server says of it,
// or ErrNoTitle if the server does not know it.
func (c *Client) Item(ctx context.Context, key string) (Item, error) {
	if !validKey(key) {
		return Item{}, ErrNoTitle
	}
	var answer struct {
		MediaContainer struct {
			Metadata []json.RawMessage `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	err := c.get(ctx, "/library/metadata/"+key, nil, &answer)
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return Item{}, ErrNoTitle
	}
	if err != nil {
		return Item{}, err
	}
	for _, raw := range answer.MediaContainer.Metadata {
		if it, ok := decodeItem(raw); ok && it.Key == key {
			return it, nil
		}
	}
	return Item{}, ErrNoTitle
}

// Episodes returns a show's episodes, as the server orders them: by season,
// then number. Episodes without a file are left out.
func (c *Client) Episodes(ctx context.Context, showKey string) ([]Item, error) {
	if !validKey(showKey) {
		return nil, ErrNoTitle
	}
	var out []Item
	_, err := c.pages(ctx, "/library/metadata/"+showKey+"/allLeaves", nil, TypeEpisode, func(it Item) error {
		if it.File.Key != "" {
			out = append(out, it)
		}
		return nil
	})
	var status *statusError
	if errors.As(err, &status) && status.code == http.StatusNotFound {
		return nil, ErrNoTitle
	}
	return out, err
}

// pages asks for a list page by page, from path with query q, and calls each
// for each entry of type typ.
func (c *Client) pages(ctx context.Context, path string, q url.Values, typ string, each func(Item) error) (skipped int, err error) {
	start := 0
	for range maxPages {
		page := url.Values{}
		for k, v := range q {
			page[k] = v
		}
		page.Set("X-Plex-Container-Start", strconv.Itoa(start))
		page.Set("X-Plex-Container-Size", strconv.Itoa(pageSize))
		var answer struct {
			MediaContainer struct {
				Size      *number           `json:"size"`
				TotalSize *number           `json:"totalSize"`
				Metadata  []json.RawMessage `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := c.get(ctx, path, page, &answer); err != nil {
			return skipped, err
		}
		m := answer.MediaContainer
		for _, raw := range m.Metadata {
			it, ok := decodeItem(raw)
			if !ok || it.Type != typ {
				skipped++
				continue
			}
			if err := each(it); err != nil {
				return skipped, err
			}
		}
		start += len(m.Metadata)
		switch {
		case len(m.Metadata) == 0:
			return skipped, nil
		case m.TotalSize != nil:
			if start >= int(*m.TotalSize) {
				return skipped, nil
			}
		case len(m.Metadata) < pageSize:
			// A server that does not say how many there are gave a short
			// page: the last one.
			return skipped, nil
		}
	}
	return skipped, fmt.Errorf("the list did not end after %d pages", maxPages)
}

// PictureURL returns the address at which the server sends one of its
// pictures, by its path as an item gives it, scaled to width by height at
// least, as a JPEG. It holds the token, so it must stay on the server. ""
// for a path that is not one of the server's.
func (c *Client) PictureURL(path string, width, height int) string {
	if !validPath(path) {
		return ""
	}
	q := url.Values{
		"url":    {path},
		"width":  {strconv.Itoa(width)},
		"height": {strconv.Itoa(height)},
		// Fill width by height, cropping nothing: the box's screens crop.
		"minSize":      {"1"},
		"upscale":      {"1"},
		"X-Plex-Token": {c.token},
	}
	return c.server + "/photo/:/transcode?" + q.Encode()
}

// pictureScheme starts the address at which Overscan keeps one of the
// server's pictures: one without the token, which PictureFor turns into
// one that fetches it.
const pictureScheme = "plex:"

// PictureAddress returns the address at which Overscan keeps one of the
// server's pictures, by its path as an item gives it, to be fetched width
// by height: plex:<width>x<height><path>. It holds no token, which the
// source's settings add when the picture is fetched (PictureFor). "" for
// a path that is not one of the server's, or a size out of bounds.
func PictureAddress(path string, width, height int) string {
	if !validPath(path) || width <= 0 || height <= 0 || width > 4096 || height > 4096 {
		return ""
	}
	return pictureScheme + strconv.Itoa(width) + "x" + strconv.Itoa(height) + path
}

// IsPicture reports whether address is one that PictureAddress made.
func IsPicture(address string) bool { return strings.HasPrefix(address, pictureScheme) }

// PictureFor returns the address that fetches a picture that
// PictureAddress kept, with the token, or "" if address is not one.
func (c *Client) PictureFor(address string) string {
	size, path, ok := strings.Cut(strings.TrimPrefix(address, pictureScheme), "/")
	if !IsPicture(address) || !ok {
		return ""
	}
	w, h, ok := strings.Cut(size, "x")
	width, err1 := strconv.Atoi(w)
	height, err2 := strconv.Atoi(h)
	if !ok || err1 != nil || err2 != nil || PictureAddress("/"+path, width, height) != address {
		return ""
	}
	return c.PictureURL("/"+path, width, height)
}

// Subtitle is a text subtitle track of a film's or an episode's file
// (D69): one in the file, or a file beside it, which the server sends at
// SubtitleURL.
type Subtitle struct {
	ID       int64  // the server's ID for the stream
	Index    int    // its index among the file's streams; -1 for a file beside it
	Codec    string // as the server names it, such as srt, ass or mov_text
	Language string // a three-letter code such as eng; "" if not known
	Title    string
	Forced   bool
	Hearing  bool
	Default  bool
	External bool // a file beside the video
}

// Subtitles returns the text subtitle tracks of a film's or an episode's
// first file, or ErrNoTitle for an item that the server does not know.
// Subtitles made of pictures, which the box cannot show, are left out.
func (c *Client) Subtitles(ctx context.Context, key string) ([]Subtitle, error) {
	it, err := c.Item(ctx, key)
	if err != nil {
		return nil, err
	}
	return it.subtitles, nil
}

// SubtitleURL returns the address at which the server sends a subtitle
// file beside a video, in the format that the track's codec reads best as:
// SubStation Alpha's own, WebVTT's own, or else SubRip, in UTF-8. It holds
// the token, so it must stay on the server. "" for a track in the file,
// which the server does not send.
func (c *Client) SubtitleURL(s Subtitle) (address, format string) {
	if !s.External || s.ID <= 0 {
		return "", ""
	}
	format = "srt"
	switch s.Codec {
	case "ass", "ssa":
		format = "ass"
	case "webvtt", "vtt":
		format = "vtt"
	}
	q := url.Values{"format": {format}, "encoding": {"utf-8"}, "X-Plex-Token": {c.token}}
	return c.server + "/library/streams/" + strconv.FormatInt(s.ID, 10) + "." + format + "?" + q.Encode(), format
}

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
	req.Header.Set("X-Plex-Token", c.token)
	req.Header.Set("X-Plex-Client-Identifier", "overscan")
	req.Header.Set("X-Plex-Product", "Overscan")
	req.Header.Set("X-Plex-Version", c.version)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return ErrToken
	case resp.StatusCode == http.StatusForbidden:
		return errors.New("the server does not let the token's account read this")
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

// validKey reports whether s can be one of the server's keys for a library
// or a title: digits, as the server gives them.
func validKey(s string) bool {
	if s == "" || len(s) > 20 {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// validPath reports whether s can be the path of something on the server,
// such as a picture's or a file's: absolute, without a query, a fragment
// or a step up.
func validPath(s string) bool {
	return strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "//") && len(s) <= 512 &&
		!strings.ContainsAny(s, "?#\\") && !strings.Contains(s, "/../") && !strings.HasSuffix(s, "/..") &&
		!strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f })
}
