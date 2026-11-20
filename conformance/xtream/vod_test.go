// SPDX-License-Identifier: Apache-2.0

package xtream_test

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// Films and series (notes, section 7): categories, lists, a title's
// details, and the addresses that play a film or an episode with the
// ranges a player asks for.
//
// The fixture is a fictional library of sixteen films and one series with
// two seasons, of three and two episodes, in a folder. Each file holds its
// own name, over and over, so that a test can tell which one plays.

// libraryName is the name of the fixture library's folder, which a server
// may show as a category.
const libraryName = "Example Library"

// fixtureFilms is how many films the fixture library holds.
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

// categoryID returns the ID of a category of films or of series by name.
func categoryID(t *testing.T, action, name string) string {
	t.Helper()
	var cats []category
	api(t, &cats, "action", action)
	for _, c := range cats {
		if c.Name == name {
			return string(c.ID)
		}
	}
	t.Fatalf("%s has no %q: %+v", action, name, cats)
	return ""
}

type film struct {
	Num        flexInt    `json:"num"`
	Name       string     `json:"name"`
	StreamID   flexInt    `json:"stream_id"`
	StreamType flexString `json:"stream_type"`
	CategoryID flexString `json:"category_id"`
	Container  flexString `json:"container_extension"`
}

// playRange fetches bytes 100 to 199 of what a stream address plays, as a
// player that seeks does.
func playRange(t *testing.T, path string) (int, []byte) {
	t.Helper()
	resp, body := get(t, path, "Range", "bytes=100-199")
	return resp.StatusCode, body
}

func TestFilms(t *testing.T) {
	category := categoryID(t, "get_vod_categories", libraryName)
	var films []film
	api(t, &films, "action", "get_vod_streams")
	if len(films) != fixtureFilms {
		t.Fatalf("%d films, want %d", len(films), fixtureFilms)
	}
	var names []string
	ids := map[flexInt]bool{}
	for _, f := range films {
		names = append(names, f.Name)
		if f.StreamID <= 0 || ids[f.StreamID] || f.StreamType != "movie" || string(f.CategoryID) != category || f.Container != "mkv" {
			t.Errorf("film %+v", f)
		}
		ids[f.StreamID] = true
	}
	for i := 1; i <= fixtureFilms; i++ {
		if !slices.Contains(names, filmName(i)) {
			t.Errorf("no film %q in %v", filmName(i), names)
		}
	}
	var inCategory []film
	api(t, &inCategory, "action", "get_vod_streams", "category_id", category)
	if len(inCategory) != fixtureFilms {
		t.Errorf("%d films in %q", len(inCategory), libraryName)
	}

	// A film's details, and the address that plays it.
	var first film
	for _, f := range films {
		if f.Name == filmName(1) {
			first = f
		}
	}
	var info struct {
		Info map[string]any `json:"info"`
		Data struct {
			StreamID  flexInt    `json:"stream_id"`
			Container flexString `json:"container_extension"`
		} `json:"movie_data"`
	}
	api(t, &info, "action", "get_vod_info", "vod_id", fmt.Sprint(first.StreamID))
	if info.Data.StreamID != first.StreamID || info.Data.Container != "mkv" || info.Info == nil {
		t.Errorf("get_vod_info: %+v", info)
	}
	status, body := playRange(t, streamPath("movie", fmt.Sprintf("%d.%s", first.StreamID, first.Container)))
	if want := fixtureFile(filmName(1))[100:200]; status != http.StatusPartialContent || !bytes.Equal(body, want) {
		t.Errorf("playing %s: status %d, %q; want 206, %q", filmName(1), status, body, want)
	}
	if resp, _ := get(t, streamPath("movie", "999999999.mkv")); resp.StatusCode < 400 {
		t.Errorf("a film that is not there: %s", resp.Status)
	}
}

func TestSeries(t *testing.T) {
	category := categoryID(t, "get_series_categories", libraryName)
	var list []struct {
		Name       string     `json:"name"`
		SeriesID   flexInt    `json:"series_id"`
		CategoryID flexString `json:"category_id"`
	}
	api(t, &list, "action", "get_series")
	if len(list) != 1 || list[0].Name != "Example Series" || list[0].SeriesID <= 0 || string(list[0].CategoryID) != category {
		t.Fatalf("series: %+v", list)
	}
	var info struct {
		Seasons []struct {
			Number flexInt `json:"season_number"`
		} `json:"seasons"`
		Episodes map[string][]struct {
			ID        flexString `json:"id"`
			Num       flexInt    `json:"episode_num"`
			Season    flexInt    `json:"season"`
			Container flexString `json:"container_extension"`
		} `json:"episodes"`
	}
	api(t, &info, "action", "get_series_info", "series_id", fmt.Sprint(list[0].SeriesID))
	if len(info.Seasons) != len(fixtureEpisodes) || len(info.Episodes) != len(fixtureEpisodes) {
		t.Fatalf("seasons: %+v", info)
	}
	for season, count := range fixtureEpisodes {
		episodes := info.Episodes[strconv.Itoa(season)]
		if len(episodes) != count {
			t.Errorf("season %d: %d episodes, want %d", season, len(episodes), count)
			continue
		}
		for i, e := range episodes {
			if e.ID == "" || int(e.Num) != i+1 || int(e.Season) != season || e.Container != "mkv" {
				t.Errorf("season %d, episode %d: %+v", season, i+1, e)
			}
		}
	}
	e := info.Episodes["2"][1]
	status, body := playRange(t, streamPath("series", fmt.Sprintf("%s.%s", e.ID, e.Container)))
	if want := fixtureFile("Example Series S02E02")[100:200]; status != http.StatusPartialContent || !bytes.Equal(body, want) {
		t.Errorf("playing S02E02: status %d, %q; want 206, %q", status, body, want)
	}
}
