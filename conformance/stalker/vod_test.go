// SPDX-License-Identifier: Apache-2.0

package stalker_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Films and series (protocol notes, section 6): categories, lists a page of
// 14 at a time, a series' seasons in the series module, and links that play
// a file with the ranges a player asks for.
//
// The fixture is a fictional library of sixteen films and one series with
// two seasons, of three and two episodes. Each file holds its own name, over
// and over, so that a test can tell which one plays.

// libraryName is the name of the fixture library's folder, which a server
// may show as a category.
const libraryName = "Example Library"

// fixtureFilms is how many films the fixture library holds: more than a
// page.
const fixtureFilms = 16

// fixtureEpisodes are the fixture series' episodes by season.
var fixtureEpisodes = map[int]int{1: 3, 2: 2}

func filmName(i int) string { return fmt.Sprintf("Example Film %02d", i) }

// fixtureFile is what a fixture file holds.
func fixtureFile(name string) []byte { return bytes.Repeat([]byte(name+"\n"), 200) }

// writeLibrary writes the fixture library into dir, laid out as media
// servers expect, and returns its path.
func writeLibrary(dir string) (string, error) {
	root := filepath.Join(dir, libraryName)
	files := map[string]string{}
	for i := 1; i <= fixtureFilms; i++ {
		name := fmt.Sprintf("%s (%d)", filmName(i), 2000+i)
		files[filepath.Join(root, name, name+".mkv")] = filmName(i)
	}
	for s, count := range fixtureEpisodes {
		for e := 1; e <= count; e++ {
			name := fmt.Sprintf("Example Series S%02dE%02d", s, e)
			files[filepath.Join(root, "Example Series (2020)", fmt.Sprintf("Season %d", s), name+".mkv")] = name
		}
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(path, fixtureFile(content), 0o644); err != nil {
			return "", err
		}
	}
	return root, nil
}

// vodItem is what clients read of a film or a series in a list, and
// season of a series' season (protocol notes, section 6).
type vodItem struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Cmd        string `json:"cmd"`
	CategoryID string `json:"category_id"`
}

type season struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Cmd    string    `json:"cmd"`
	Series []flexInt `json:"series"`
}

type vodPage struct {
	TotalItems   flexInt   `json:"total_items"`
	MaxPageItems flexInt   `json:"max_page_items"`
	Data         []vodItem `json:"data"`
}

// categories returns a module's categories by title, checking that All
// comes first.
func (c *client) categories(module string) map[string]string {
	c.t.Helper()
	var gs []genre
	c.js(&gs, module, "get_categories")
	if len(gs) == 0 || gs[0].ID != "*" {
		c.t.Fatalf("%s categories %+v: All (*) is not first", module, gs)
	}
	byTitle := map[string]string{}
	for _, g := range gs[1:] {
		byTitle[g.Title] = g.ID
	}
	return byTitle
}

func (c *client) titles(module string, params ...string) vodPage {
	c.t.Helper()
	var p vodPage
	c.js(&p, module, "get_ordered_list", params...)
	return p
}

func names(items []vodItem) []string {
	out := []string{}
	for _, it := range items {
		out = append(out, it.Name)
	}
	return out
}

func (c *client) vodLink(module, cmd, series string) link {
	c.t.Helper()
	var l link
	c.js(&l, module, "create_link", "cmd", cmd, "series", series, "forced_storage", "undefined", "disable_ad", "0",
		"download", "0")
	return l
}

// fetchRange fetches bytes 100 to 199 of what a link plays, as a player
// that seeks does: the last word of its cmd, which may be a path on the
// portal.
func (c *client) fetchRange(l link) (int, []byte) {
	c.t.Helper()
	fields := strings.Fields(l.Cmd)
	if l.Error != "" || len(fields) == 0 {
		c.t.Fatalf("link %+v has no address", l)
	}
	base, _ := url.Parse(c.base)
	u, err := base.Parse(fields[len(fields)-1])
	if err != nil {
		c.t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	return resp.StatusCode, body
}

func TestFilmCategories(t *testing.T) {
	c := signedIn(t)
	if _, ok := c.categories("vod")[libraryName]; !ok {
		t.Errorf("no category %q", libraryName)
	}
}

// Lists come a page of 14 at a time, the latest added first unless sortby
// says otherwise.
func TestFilmPages(t *testing.T) {
	c := signedIn(t)
	category := c.categories("vod")[libraryName]
	first := c.titles("vod", "category", category, "genre", "*", "sortby", "name", "fav", "0", "p", "1")
	if first.TotalItems != fixtureFilms || first.MaxPageItems != 14 || len(first.Data) != 14 {
		t.Fatalf("first page: total %d, page size %d, %d on it", first.TotalItems, first.MaxPageItems, len(first.Data))
	}
	second := c.titles("vod", "category", category, "genre", "*", "sortby", "name", "fav", "0", "p", "2")
	var want []string
	for i := 1; i <= fixtureFilms; i++ {
		want = append(want, filmName(i))
	}
	if got := append(names(first.Data), names(second.Data)...); !reflect.DeepEqual(got, want) {
		t.Errorf("films by name %q, want %q", got, want)
	}
	for _, it := range first.Data {
		if it.ID == "" || it.Cmd == "" || it.CategoryID != category {
			t.Errorf("film %+v", it)
		}
	}
	all := c.titles("vod", "category", "*", "genre", "*", "sortby", "added", "fav", "0", "p", "1")
	if all.TotalItems != fixtureFilms || len(all.Data) != 14 {
		t.Errorf("all films: total %d, %d on the first page", all.TotalItems, len(all.Data))
	}
	if past := c.titles("vod", "category", category, "sortby", "name", "p", "3"); past.TotalItems != fixtureFilms || len(past.Data) != 0 {
		t.Errorf("past the last page: total %d, %q", past.TotalItems, names(past.Data))
	}
}

// A film's link plays its file, from the part a player asks for.
func TestFilmLink(t *testing.T) {
	c := signedIn(t)
	films := c.titles("vod", "category", c.categories("vod")[libraryName], "sortby", "name", "p", "1")
	if len(films.Data) == 0 {
		t.Fatal("no films")
	}
	l := c.vodLink("vod", films.Data[0].Cmd, "0")
	status, body := c.fetchRange(l)
	if want := fixtureFile(filmName(1))[100:200]; status != http.StatusPartialContent || !bytes.Equal(body, want) {
		t.Errorf("playing %+v: status %d, %q; want 206, %q", l, status, body, want)
	}
	for _, cmd := range []string{"", "garbage", "/media/999999999.mpg"} {
		if l := c.vodLink("vod", cmd, ""); l.Error != "nothing_to_play" || l.Cmd != "" {
			t.Errorf("cmd %q: %+v, want nothing_to_play", cmd, l)
		}
	}
}

// Series come from the series module. A series' seasons come with movie_id,
// each with its episodes' numbers, and a season's cmd with series=<n> plays
// episode n.
func TestSeriesModule(t *testing.T) {
	c := signedIn(t)
	list := c.titles("series", "category", c.categories("series")[libraryName], "sortby", "added", "p", "1")
	if list.TotalItems != 1 || len(list.Data) != 1 || list.Data[0].Name != "Example Series" {
		t.Fatalf("series %+v", list)
	}
	var seasons struct {
		TotalItems flexInt  `json:"total_items"`
		Data       []season `json:"data"`
	}
	c.js(&seasons, "series", "get_ordered_list", "movie_id", list.Data[0].ID, "p", "1")
	if seasons.TotalItems != 2 || len(seasons.Data) != 2 {
		t.Fatalf("seasons %+v", seasons)
	}
	for i, s := range seasons.Data {
		want := fixtureEpisodes[i+1]
		if s.ID == "" || s.Name == "" || s.Cmd == "" || len(s.Series) != want {
			t.Errorf("season %d: %+v, want %d episodes", i+1, s, want)
		}
	}
	s2 := seasons.Data[1]
	l := c.vodLink("vod", s2.Cmd, fmt.Sprint(int(s2.Series[1])))
	status, body := c.fetchRange(l)
	if want := fixtureFile("Example Series S02E02")[100:200]; status != http.StatusPartialContent || !bytes.Equal(body, want) {
		t.Errorf("playing %+v: status %d, %q; want 206, %q", l, status, body, want)
	}
	if l := c.vodLink("vod", s2.Cmd, "99"); l.Error != "nothing_to_play" || l.Cmd != "" {
		t.Errorf("an episode the season lacks: %+v, want nothing_to_play", l)
	}
}
