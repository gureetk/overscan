// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
)

// fetchFile fetches bytes 100 to 199 of what a link plays.
func fetchFile(t *testing.T, link string) []byte {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, link, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusPartialContent {
		t.Errorf("%d: %s", resp.StatusCode, body)
	}
	return body
}

func TestFilms(t *testing.T) {
	p := stalkertest.New(t)
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	cats, err := c.Categories(ctx, ModuleVOD)
	if err != nil || len(cats) != 2 || cats[0] != (Genre{ID: "1", Title: "Drama"}) {
		t.Fatalf("categories %+v, %v", cats, err)
	}
	first, err := c.Titles(ctx, ModuleVOD, "*", 1)
	if err != nil || first.Total != 20 || len(first.Items) != 14 {
		t.Fatalf("first page %+v, %v", first, err)
	}
	second, err := c.Titles(ctx, ModuleVOD, "*", 2)
	if err != nil || second.Total != 20 || len(second.Items) != 6 || second.Items[5].Name != "Example Film 20" {
		t.Fatalf("second page %+v, %v", second, err)
	}
	f := first.Items[0]
	want := Item{ID: "1001", Name: "Example Film 01", Cmd: "/media/1001.mpg", CategoryID: "2",
		Poster: p.URL + "/stalker_portal/screenshots/1001.jpg", Description: "A fictional film.", Actors: "A. Actor",
		Director: "D. Director", Genres: "Drama", Year: 2001, Minutes: 95, Rating: 74, Age: "12+",
		Added: time.Date(2026, 12, 19, 0, 0, 0, 0, time.UTC)}
	if !reflect.DeepEqual(f, want) {
		t.Errorf("film\n%+v, want\n%+v", f, want)
	}
	if drama, err := c.Titles(ctx, ModuleVOD, "1", 1); err != nil || drama.Total != 10 {
		t.Errorf("drama %+v, %v", drama, err)
	}

	// A film whose file is asked for apart.
	third := first.Items[2]
	if !third.HasFiles || third.Minutes != 101 {
		t.Fatalf("third %+v", third)
	}
	cmd, err := c.MovieFile(ctx, third.ID)
	if err != nil || cmd != "/media/file_9003.mpg" {
		t.Fatalf("file %q, %v", cmd, err)
	}
	link, err := c.VODLink(ctx, cmd, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := fetchFile(t, link); !bytes.Equal(got, stalkertest.Content("1003")[100:200]) {
		t.Errorf("film 1003: %q", got)
	}
	if _, err := c.MovieFile(ctx, "1001"); !errors.Is(err, ErrNoFile) {
		t.Errorf("a film without files apart: %v", err)
	}
	link, err = c.VODLink(ctx, f.Cmd, 0)
	if err != nil || !bytes.Equal(fetchFile(t, link), stalkertest.Content("1001")[100:200]) {
		t.Errorf("film 1001: %q, %v", link, err)
	}
	var lerr *LinkError
	if _, err := c.VODLink(ctx, "/media/nothing", 0); !errors.As(err, &lerr) || lerr.Reason != "nothing_to_play" {
		t.Errorf("nothing: %v", err)
	}
}

func TestSeriesModule(t *testing.T) {
	p := stalkertest.New(t)
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	cats, err := c.Categories(ctx, ModuleSeries)
	if err != nil || len(cats) != 1 || cats[0].Title != "Documentary" {
		t.Fatalf("categories %+v, %v", cats, err)
	}
	list, err := c.Titles(ctx, ModuleSeries, "5", 1)
	if err != nil || list.Total != 2 || list.Items[0].Name != "Example Coast" || list.Items[0].Poster != "http://img.example/2001.jpg" {
		t.Fatalf("series %+v, %v", list, err)
	}
	seasons, err := c.SeriesSeasons(ctx, "2001")
	if err != nil || len(seasons) != 2 {
		t.Fatalf("seasons %+v, %v", seasons, err)
	}
	if s := seasons[0]; s.ID != "20011" || s.Number != 1 || s.Name != "Season 1" || s.Cmd != "/media/2001_1.mpg" ||
		!reflect.DeepEqual(s.Episodes, []int{1, 2, 3}) {
		t.Errorf("season 1 %+v", s)
	}
	if s := seasons[1]; s.Number != 2 || !reflect.DeepEqual(s.Episodes, []int{1, 2}) {
		t.Errorf("season 2 %+v", s)
	}
	link, err := c.VODLink(ctx, seasons[1].Cmd, 2)
	if err != nil || !bytes.Equal(fetchFile(t, link), stalkertest.Content("2001_2e2")[100:200]) {
		t.Errorf("S2 E2: %q, %v", link, err)
	}
	// Films' lists hold no series.
	films, _ := c.Titles(ctx, ModuleVOD, "1", 1)
	for _, it := range films.Items {
		if it.IsSeries || len(it.Series) > 0 {
			t.Errorf("a series among the films: %+v", it)
		}
	}
}

func TestInlineSeries(t *testing.T) {
	p := stalkertest.New(t)
	p.Series = stalkertest.Inline
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	if _, err := c.Categories(ctx, ModuleSeries); !errors.Is(err, ErrNoAction) {
		t.Errorf("series module: %v", err)
	}
	drama, err := c.Titles(ctx, ModuleVOD, "1", 1)
	if err != nil || drama.Total != 12 || drama.Items[0].Name != "Example Coast" ||
		!reflect.DeepEqual(drama.Items[0].Series, []int{1, 2, 3, 4, 5}) || drama.Items[0].IsSeries {
		t.Fatalf("drama %+v, %v", drama, err)
	}
	link, err := c.VODLink(ctx, drama.Items[0].Cmd, 4)
	if err != nil || !bytes.Equal(fetchFile(t, link), stalkertest.Content("2001e4")[100:200]) {
		t.Errorf("episode 4: %q, %v", link, err)
	}
}

func TestSeasonsOnRequest(t *testing.T) {
	p := stalkertest.New(t)
	p.Series = stalkertest.OnRequest
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	drama, err := c.Titles(ctx, ModuleVOD, "1", 1)
	if err != nil || !drama.Items[0].IsSeries || len(drama.Items[0].Series) != 0 {
		t.Fatalf("drama %+v, %v", drama, err)
	}
	seasons, err := c.Seasons(ctx, ModuleVOD, "2001")
	if err != nil || len(seasons) != 2 || seasons[1].Number != 2 || seasons[1].Name != "Season 2" || seasons[1].Cmd != "" {
		t.Fatalf("seasons %+v, %v", seasons, err)
	}
	eps, err := c.Episodes(ctx, ModuleVOD, "2001", seasons[0].ID)
	if err != nil || len(eps) != 3 {
		t.Fatalf("episodes %+v, %v", eps, err)
	}
	if e := eps[1]; e.ID != "2001102" || e.Number != 2 || e.Name != "Episode 2" || e.Minutes != 42 ||
		e.Picture != p.URL+"/shots/2001102.jpg" || e.Cmd != "/media/2001102.mpg" {
		t.Errorf("episode 2 %+v", e)
	}
	link, err := c.VODLink(ctx, eps[1].Cmd, 0)
	if err != nil || !bytes.Equal(fetchFile(t, link), stalkertest.Content("2001102")[100:200]) {
		t.Errorf("episode 2: %q, %v", link, err)
	}
}

func TestItemFields(t *testing.T) {
	c, _ := New(Config{Portal: "http://portal.example/c/", MAC: stalkertest.MAC}, http.DefaultClient)
	for _, tc := range []struct {
		json string
		ok   bool
		want func(Item) bool
	}{
		{`{"id":5,"o_name":"Original","cmd":"/media/5.mpg","year":"2019-05-01","time":"1:05","rating_kinopoisk":"6,5"}`, true,
			func(it Item) bool {
				return it.Name == "Original" && it.Year == 2019 && it.Minutes == 65 && it.Rating == 65
			}},
		{`{"id":"6","name":"A","time":"02:00:40","rating_imdb":"N/A","year":"20190"}`, true,
			func(it Item) bool { return it.Minutes == 121 && it.Rating == 0 && it.Year == 0 }},
		{`{"id":"7","name":"B","series":["1","2","x",3],"is_series":"0","added":"1795000000"}`, true,
			func(it Item) bool {
				return reflect.DeepEqual(it.Series, []int{1, 2, 3}) && it.Added.Equal(time.Unix(1795000000, 0))
			}},
		{`{"id":"8","name":"C","series":false,"screenshot_uri":"javascript:x"}`, true,
			func(it Item) bool { return it.Series == nil && it.Poster == "" }},
		{`{"id":"","name":"No ID"}`, false, nil},
		{`{"id":"9"}`, false, nil},
	} {
		var raw itemJSON
		if err := json.Unmarshal([]byte(tc.json), &raw); err != nil {
			t.Fatal(err)
		}
		it, ok := c.item(raw)
		if ok != tc.ok || (ok && !tc.want(it)) {
			t.Errorf("%s: %+v, %v", tc.json, it, ok)
		}
	}
}

func TestMinutes(t *testing.T) {
	for s, want := range map[string]int{"95": 95, "01:35:00": 95, "1:35": 95, "00:42:31": 43, "": 0, "abc": 0,
		"1:75": 0, "-5": 0, "1:2:3:4": 0, "999999": 0} {
		if got := minutes(s); got != want {
			t.Errorf("minutes(%q) = %d, want %d", s, got, want)
		}
	}
}

func FuzzDecodePage(f *testing.F) {
	f.Add(`{"js":{"total_items":"20","max_page_items":14,"data":[{"id":"1","name":"A","series":[1,"2"],"time":"01:02:03"}]}}`)
	f.Add(`{"js":{"total_items":0,"data":false}}`)
	f.Add(`{"js":false}`)
	c, _ := New(Config{Portal: "http://portal.example/c/", MAC: stalkertest.MAC}, http.DefaultClient)
	f.Fuzz(func(t *testing.T, body string) {
		js, err := jsMember([]byte(body))
		if err != nil {
			return
		}
		var page pageJSON[itemJSON]
		if decode(js, &page) != nil {
			return
		}
		for _, raw := range page.Data {
			if it, ok := c.item(raw); ok && (it.ID == "" || it.Name == "" || it.Minutes < 0 || it.Rating > 100 ||
				strings.HasPrefix(it.Poster, "javascript")) {
				t.Fatalf("item %+v", it)
			}
		}
	})
}
