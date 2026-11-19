// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Live TV (section 4) and the guide (section 5).

const (
	// MaxChannels caps the channels read from a portal.
	MaxChannels = 50_000
	// MaxGuideBytes caps a guide read as it comes.
	MaxGuideBytes = 1 << 30
	// pageSize is how many items a page of a portal's list holds (section
	// 1).
	pageSize = 14
	// pagePause is the wait between the pages of a list read one after the
	// other, so that a list of channels read a page at a time does not
	// look like a crawl (D22).
	pagePause = 500 * time.Millisecond
)

// Genre is a genre of channels, or a category of films or series, that a
// portal lists.
type Genre struct {
	ID    string
	Title string
}

// genreJSON is a genre or a category as a portal sends it.
type genreJSON struct {
	ID    text `json:"id"`
	Title text `json:"title"`
}

func genres(items list[genreJSON]) []Genre {
	out := []Genre{}
	for _, g := range items {
		id, title := strings.TrimSpace(string(g.ID)), strings.TrimSpace(string(g.Title))
		if id == "" || id == "*" || title == "" {
			continue
		}
		out = append(out, Genre{ID: id, Title: title})
	}
	return out
}

// Genres returns the portal's genres of channels, without "All" (section
// 4).
func (c *Client) Genres(ctx context.Context) ([]Genre, error) {
	var items list[genreJSON]
	if err := c.call(ctx, "itv", "get_genres", nil, &items); err != nil {
		return nil, err
	}
	return genres(items), nil
}

// Channel is a channel that a portal lists (section 4).
type Channel struct {
	ID      string // the portal's ID for it
	Name    string
	Number  int    // 0 if the portal gives none
	Cmd     string // what create_link takes to play it
	Logo    string // an absolute address, "" if none
	GenreID string
	XMLTVID string
	// Censored is a channel the portal marks for adults.
	Censored bool
	// ArchiveHours is how many hours of catch-up the portal keeps of it, 0
	// for none.
	ArchiveHours int
}

// channelJSON is a channel as a portal sends it.
type channelJSON struct {
	ID              text   `json:"id"`
	Name            text   `json:"name"`
	Number          number `json:"number"`
	Cmd             text   `json:"cmd"`
	Logo            text   `json:"logo"`
	GenreID         text   `json:"tv_genre_id"`
	XMLTVID         text   `json:"xmltv_id"`
	Censored        flag   `json:"censored"`
	TVArchive       flag   `json:"tv_archive"`
	EnableTVArchive flag   `json:"enable_tv_archive"`
	Archive         flag   `json:"archive"`
	ArchiveDuration number `json:"tv_archive_duration"`
}

func (c *Client) channel(ch channelJSON) (Channel, bool) {
	out := Channel{
		ID:      strings.TrimSpace(string(ch.ID)),
		Name:    strings.TrimSpace(string(ch.Name)),
		Cmd:     strings.TrimSpace(string(ch.Cmd)),
		Logo:    c.resolve(string(ch.Logo)),
		GenreID: strings.TrimSpace(string(ch.GenreID)),
		XMLTVID: strings.TrimSpace(string(ch.XMLTVID)),
	}
	if out.ID == "" || out.Cmd == "" || len(out.Cmd) > 4096 {
		return Channel{}, false
	}
	if ch.Number > 0 && ch.Number < 1_000_000 {
		out.Number = int(ch.Number)
	}
	out.Censored = bool(ch.Censored)
	if (ch.TVArchive || ch.EnableTVArchive || ch.Archive) && ch.ArchiveDuration > 0 && ch.ArchiveDuration < 24*365 {
		out.ArchiveHours = int(ch.ArchiveDuration)
	}
	return out, true
}

// Channels returns the portal's channels, and how many it listed that
// cannot be read: itv/get_all_channels, or where the portal lacks it,
// itv/get_ordered_list a page at a time (section 4).
func (c *Client) Channels(ctx context.Context) ([]Channel, int, error) {
	var all struct {
		Data list[channelJSON] `json:"data"`
	}
	err := c.call(ctx, "itv", "get_all_channels", nil, &all)
	if errors.Is(err, ErrNoAction) {
		return c.orderedChannels(ctx)
	}
	if err != nil {
		return nil, 0, err
	}
	return c.keep(all.Data)
}

// keep returns the channels that can be read, each ID once.
func (c *Client) keep(items []channelJSON) ([]Channel, int, error) {
	out := []Channel{}
	seen := map[string]bool{}
	skipped := 0
	for _, it := range items {
		ch, ok := c.channel(it)
		if !ok || seen[ch.ID] {
			skipped++
			continue
		}
		if len(out) == MaxChannels {
			return nil, 0, fmt.Errorf("the portal lists more than %d channels", MaxChannels)
		}
		seen[ch.ID] = true
		out = append(out, ch)
	}
	return out, skipped, nil
}

// pageJSON is a page of a list (section 1).
type pageJSON[T any] struct {
	TotalItems   number  `json:"total_items"`
	MaxPageItems number  `json:"max_page_items"`
	Data         list[T] `json:"data"`
}

// orderedChannels reads the channels a page at a time, by number, with a
// pause between pages.
func (c *Client) orderedChannels(ctx context.Context) ([]Channel, int, error) {
	var items []channelJSON
	for p := 1; ; p++ {
		var page pageJSON[channelJSON]
		err := c.call(ctx, "itv", "get_ordered_list", url.Values{"genre": {"*"}, "p": {strconv.Itoa(p)},
			"sortby": {"number"}, "fav": {"0"}, "hd": {"0"}}, &page)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, page.Data...)
		if len(page.Data) == 0 || int64(len(items)) >= int64(page.TotalItems) || len(items) > MaxChannels {
			break
		}
		select {
		case <-ctx.Done():
			return nil, 0, ctx.Err()
		case <-time.After(pagePause):
		}
	}
	return c.keep(items)
}

// linkJSON is create_link's answer (section 4).
type linkJSON struct {
	Cmd   text `json:"cmd"`
	Error text `json:"error"`
}

// link returns the address in create_link's answer: the last word of its
// cmd, which may be a path on the portal (section 6), or the portal's
// reason for giving none.
func (c *Client) link(answer linkJSON) (string, error) {
	if reason := strings.TrimSpace(string(answer.Error)); reason != "" {
		return "", &LinkError{Reason: clean(reason, 60)}
	}
	fields := strings.Fields(string(answer.Cmd))
	if len(fields) == 0 {
		return "", &LinkError{Reason: "nothing_to_play"}
	}
	address := c.resolve(fields[len(fields)-1])
	if address == "" {
		return "", errors.New("the portal's link is not an http or https address")
	}
	return address, nil
}

// ChannelLink asks the portal for the address that plays a channel now:
// itv/create_link with the channel's cmd (section 4). A *LinkError says why
// the portal gave none.
func (c *Client) ChannelLink(ctx context.Context, cmd string) (string, error) {
	var answer linkJSON
	err := c.call(ctx, "itv", "create_link", url.Values{"cmd": {cmd}, "series": {""}, "forced_storage": {"undefined"},
		"disable_ad": {"0"}, "download": {"0"}, "force_ch_link_check": {""}}, &answer)
	if err != nil {
		return "", err
	}
	return c.link(answer)
}

// Programme is a programme in a portal's guide (section 5).
type Programme struct {
	ID          string
	Title       string
	Description string
	Category    string
	Start, Stop time.Time
	Archived    bool // mark_archive: it can be played from the archive
}

// programmeJSON is a programme as a portal sends it.
type programmeJSON struct {
	ID          text   `json:"id"`
	Name        text   `json:"name"`
	Descr       text   `json:"descr"`
	Category    text   `json:"category"`
	Start       number `json:"start_timestamp"`
	Stop        number `json:"stop_timestamp"`
	MarkArchive flag   `json:"mark_archive"`
}

// Guide reads the programmes of every channel over the next period hours,
// with itv/get_epg_info (section 5), and calls each with a channel's ID and
// one of its programmes, as the answer comes, so that a large guide is
// never held whole. Programmes without a name or a time are left out, and
// counted in what it returns.
func (c *Client) Guide(ctx context.Context, hours int, each func(channelID string, p Programme) error) (int, error) {
	var body io.ReadCloser
	err := c.withSession(ctx, func(s session) error {
		var err error
		body, err = c.openGuide(ctx, s, hours)
		return err
	})
	if err != nil {
		return 0, err
	}
	defer body.Close()
	skipped, err := readGuide(body, each)
	if errors.Is(err, errEmptyGuide) {
		err = nil
	}
	return skipped, err
}

// openGuide asks for the guide, and returns its answer to be read as it
// comes, or errAuth if the portal refused the token.
func (c *Client) openGuide(ctx context.Context, s session, hours int) (io.ReadCloser, error) {
	body, err := c.fetch(ctx, s.api, "itv", "get_epg_info", url.Values{"period": {strconv.Itoa(hours)}}, s.token)
	if err != nil {
		return nil, err
	}
	r := bufio.NewReader(&capReader{r: body, left: MaxGuideBytes})
	start, _ := r.Peek(32)
	if bytes.HasPrefix(bytes.TrimSpace(start), []byte("Authorization failed")) {
		body.Close()
		return nil, errAuth
	}
	return struct {
		io.Reader
		io.Closer
	}{r, body}, nil
}

// readGuide reads {"js":{"data":{"<channel>":[programme, ...], ...}}} a
// programme at a time.
func readGuide(r io.Reader, each func(string, Programme) error) (int, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return 0, fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
	}
	if err := enter(dec, "js"); err != nil {
		return 0, err
	}
	if err := enter(dec, "data"); err != nil {
		return 0, err
	}
	skipped := 0
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return skipped, fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
		}
		channelID, _ := key.(string)
		tok, err := dec.Token()
		if err != nil {
			return skipped, fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
		}
		if d, ok := tok.(json.Delim); !ok || d != '[' {
			if d == '{' {
				if err := skipRest(dec); err != nil {
					return skipped, err
				}
			}
			continue
		}
		for dec.More() {
			var p programmeJSON
			if err := dec.Decode(&p); err != nil {
				return skipped, fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
			}
			name := strings.TrimSpace(string(p.Name))
			if name == "" || p.Start <= 0 || p.Stop <= p.Start || p.Stop-p.Start > 7*24*3600 {
				skipped++
				continue
			}
			err := each(channelID, Programme{ID: strings.TrimSpace(string(p.ID)), Title: name,
				Description: strings.TrimSpace(string(p.Descr)), Category: strings.TrimSpace(string(p.Category)),
				Start: time.Unix(int64(p.Start), 0).UTC(), Stop: time.Unix(int64(p.Stop), 0).UTC(),
				Archived: bool(p.MarkArchive)})
			if err != nil {
				return skipped, err
			}
		}
		if _, err := dec.Token(); err != nil { // the closing ]
			return skipped, fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
		}
	}
	return skipped, nil
}

// enter moves dec, which is inside an object, into the object that is its
// member name. A member that is not an object, such as an empty list or
// false, or no such member, gives errEmptyGuide.
func enter(dec *json.Decoder, name string) error {
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
		}
		if key == name {
			tok, err := dec.Token()
			if err != nil {
				return fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
			}
			if d, ok := tok.(json.Delim); ok && d == '{' {
				return nil
			}
			if d, ok := tok.(json.Delim); ok && d == '[' {
				if err := skipRest(dec); err != nil {
					return err
				}
			}
			return errEmptyGuide
		}
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
		}
	}
	return errEmptyGuide
}

// errEmptyGuide is a guide whose data is not an object: an empty list,
// false or missing.
var errEmptyGuide = errors.New("the portal's guide is empty")

// skipRest reads the rest of the array or object that dec has entered.
func skipRest(dec *json.Decoder) error {
	for depth := 1; depth > 0; {
		tok, err := dec.Token()
		if err != nil {
			return fmt.Errorf("itv/get_epg_info: %w", ErrNotJSON)
		}
		if d, ok := tok.(json.Delim); ok {
			if d == '[' || d == '{' {
				depth++
			} else {
				depth--
			}
		}
	}
	return nil
}

// capReader fails once more than left bytes have been read, and drops the
// address from read errors.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		err = withoutAddress(err)
	}
	return n, err
}
