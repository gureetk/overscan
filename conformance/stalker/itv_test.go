// SPDX-License-Identifier: Apache-2.0

package stalker_test

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// genre and channel are what clients read from itv answers (protocol notes,
// section 4).
type genre struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type channel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Number    string `json:"number"`
	Cmd       string `json:"cmd"`
	TVGenreID string `json:"tv_genre_id"`
	XMLTVID   string `json:"xmltv_id"`
}

type page struct {
	TotalItems   flexInt   `json:"total_items"`
	MaxPageItems flexInt   `json:"max_page_items"`
	Data         []channel `json:"data"`
}

// The fixture lineup: three channels in two genres, by number.
var fixture = []struct{ name, genre, xmltvID string }{
	{"Example News", "News", "news.example"},
	{"Example Sports", "Sports", "sports.example"},
	{"Example Weather", "News", "weather.example"},
}

func signedIn(t *testing.T) *client {
	c := approvedClient(t)
	c.signIn()
	return c
}

// genres returns the genres by title, checking that All comes first.
func (c *client) genres() map[string]string {
	c.t.Helper()
	var gs []genre
	c.js(&gs, "itv", "get_genres")
	if len(gs) == 0 || gs[0].ID != "*" {
		c.t.Fatalf("genres %+v: All (*) is not first", gs)
	}
	byTitle := map[string]string{}
	for _, g := range gs[1:] {
		byTitle[g.Title] = g.ID
	}
	return byTitle
}

func (c *client) allChannels() []channel {
	c.t.Helper()
	var all struct{ Data []channel }
	c.js(&all, "itv", "get_all_channels")
	return all.Data
}

func TestGenres(t *testing.T) {
	c := signedIn(t)
	var gs []genre
	c.js(&gs, "itv", "get_genres")
	var titles []string
	for _, g := range gs {
		titles = append(titles, g.Title)
	}
	if got := strings.Join(titles, ", "); gs[0].ID != "*" || got != "All, News, Sports" {
		t.Errorf("genres %+v, want All (*), News, Sports", gs)
	}
}

func TestAllChannels(t *testing.T) {
	c := signedIn(t)
	genres := c.genres()
	channels := c.allChannels()
	if len(channels) != len(fixture) {
		t.Fatalf("%d channels, want %d: %+v", len(channels), len(fixture), channels)
	}
	for i, want := range fixture {
		ch := channels[i]
		if ch.ID == "" || ch.Name != want.name || ch.Number == "" || ch.Cmd == "" ||
			ch.TVGenreID != genres[want.genre] || ch.XMLTVID != want.xmltvID {
			t.Errorf("channel %d: %+v, want %s in %s", i, ch, want.name, want.genre)
		}
	}
}

func TestOrderedList(t *testing.T) {
	c := signedIn(t)
	var p page
	c.js(&p, "itv", "get_ordered_list", "genre", "*", "p", "1", "sortby", "number", "fav", "0", "hd", "0")
	if p.TotalItems != 3 || p.MaxPageItems <= 0 || len(p.Data) != 3 {
		t.Errorf("all channels: total %d, page size %d, %d on the page", p.TotalItems, p.MaxPageItems, len(p.Data))
	}
	c.js(&p, "itv", "get_ordered_list", "genre", c.genres()["News"], "p", "1", "sortby", "number", "fav", "0", "hd", "0")
	if p.TotalItems != 2 || len(p.Data) != 2 || p.Data[0].Name != "Example News" || p.Data[1].Name != "Example Weather" {
		t.Errorf("News: total %d, %+v", p.TotalItems, p.Data)
	}
	c.js(&p, "itv", "get_ordered_list", "genre", "*", "p", "2", "sortby", "number", "fav", "0", "hd", "0")
	if p.TotalItems != 3 || len(p.Data) != 0 {
		t.Errorf("past the last page: total %d, %+v", p.TotalItems, p.Data)
	}
}

// link is create_link's answer (protocol notes, section 4).
type link struct {
	Cmd   string `json:"cmd"`
	Error string `json:"error"`
}

func (c *client) createLink(cmd string) link {
	c.t.Helper()
	var l link
	c.js(&l, "itv", "create_link", "cmd", cmd, "series", "", "forced_storage", "undefined", "disable_ad", "0", "download", "0", "force_ch_link_check", "")
	return l
}

// play opens the stream a link names, as a player does: the last word of
// its cmd.
func play(t *testing.T, l link) *http.Response {
	t.Helper()
	fields := strings.Fields(l.Cmd)
	if len(fields) == 0 {
		t.Fatalf("link %+v has no address", l)
	}
	resp, err := http.Get(fields[len(fields)-1])
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// Links are made at play time and play the channel (protocol notes,
// section 4).
func TestCreateLink(t *testing.T) {
	c := signedIn(t)
	ch := c.allChannels()[0]
	l := c.createLink(ch.Cmd)
	if l.Error != "" || !hasPrefix(l.Cmd, "ffmpeg ", "ffrt ", "auto ") {
		t.Fatalf("link %+v", l)
	}
	resp := play(t, l)
	defer resp.Body.Close()
	packet := make([]byte, 188)
	if _, err := io.ReadFull(resp.Body, packet); resp.StatusCode != http.StatusOK || err != nil || packet[0] != 0x47 {
		t.Errorf("playing the link: status %d, %v, first byte %#x", resp.StatusCode, err, packet[0])
	}
}

func hasPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// A request for a link can fail with a reason instead (protocol notes,
// section 4): nothing_to_play for a channel there is none of, limit when
// the source has no free connection.
func TestNoLinkReasons(t *testing.T) {
	a, b := signedIn(t), signedIn(t)
	channels := a.allChannels()

	for _, cmd := range []string{"ffrt http://example.invalid/ch/999999", "", "garbage"} {
		if l := a.createLink(cmd); l.Error != "nothing_to_play" || l.Cmd != "" {
			t.Errorf("cmd %q: %+v, want nothing_to_play", cmd, l)
		}
	}

	// The fixture's source allows one connection. While box A watches, box
	// B is refused; a stream that another test closed may take a moment to
	// let go of it.
	var l link
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if l = a.createLink(channels[0].Cmd); l.Error != "limit" || time.Now().After(deadline) {
			break
		}
	}
	if l.Error != "" {
		t.Fatalf("box A: %+v", l)
	}
	resp := play(t, l)
	defer resp.Body.Close()
	if _, err := io.ReadFull(resp.Body, make([]byte, 188)); err != nil {
		t.Fatal(err)
	}
	if l := b.createLink(channels[1].Cmd); l.Error != "limit" || l.Cmd != "" {
		t.Errorf("box B while A watches: %+v, want limit", l)
	}
}
