// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Films and series of a fictional panel, with the mixed types that panels
// send.
const (
	movieCategories = `[{"category_id":"21","category_name":"Drama","parent_id":0},
		{"category_id":22,"category_name":" Comedy\n","parent_id":0},{"category_id":null,"category_name":"None"}]`

	movies = "\xef\xbb\xbf" + `[
		{"num":1,"name":"The Long Field","stream_type":"movie","stream_id":5001,"stream_icon":"http://img.example/5001.jpg",
		 "rating":"7.4","rating_5based":3.7,"added":"1767225600","category_id":"21","container_extension":"MKV",
		 "custom_sid":"","direct_source":"","year":"2024"},
		{"num":2,"name":"","title":"Quiet Harbour","stream_id":"5002","stream_icon":"not an address","rating":"",
		 "rating_5based":4,"added":1767312000,"category_id":null,"category_ids":[22],"container_extension":"mp4",
		 "release_date":"2019-05-03","plot":"A harbour, quietly.","genre":"Drama","cast":"A. Actor","director":"D. Rector",
		 "episode_run_time":96},
		{"num":3,"name":"No ID","stream_id":0},
		"not an object",
		{"num":4,"name":"Odd","stream_id":5004,"rating":"6,5","added":"soon","container_extension":"../x","year":"twenty",
		 "is_adult":"1"},
		{"num":5,"name":"   ","stream_id":5005}
	]`

	seriesList = `[
		{"num":1,"name":"Coastline","series_id":7001,"cover":"http://img.example/7001.jpg","plot":"Waves.",
		 "cast":"B. Actor","director":"","genre":"Documentary","releaseDate":"2021-02-03","last_modified":"1767225600",
		 "rating":"8","rating_5based":4,"backdrop_path":["http://img.example/7001-wide.jpg"],"youtube_trailer":"",
		 "episode_run_time":"45","category_id":"31"},
		{"num":2,"name":"Night Shift","series_id":"7002","cover":"","backdrop_path":"http://img.example/7002-wide.jpg",
		 "category_id":null,"release_date":"2018","rating":"","episode_run_time":"abc"},
		{"num":3,"name":"Nameless","series_id":-1}
	]`

	movieInfo = `{"info":{"name":"The Long Field (2024)","o_name":"","cover_big":"http://img.example/5001-big.jpg",
		"movie_image":"http://img.example/5001-still.jpg","releasedate":"2024-09-12","director":"C. Director","actors":"D. Actor",
		"cast":"","description":"A long field.","plot":"","age":"","mpaa_rating":"PG","country":"Nowhere","genre":"Drama",
		"backdrop_path":["","http://img.example/5001-wide.jpg"],"duration_secs":0,"duration":"01:52:10","rating":"7.4"},
		"movie_data":{"stream_id":5001,"name":"The Long Field","added":"1767225600","category_id":"21","container_extension":"mkv"}}`

	seriesInfo = `{"seasons":[{"air_date":"2021-02-03","episode_count":2,"id":11,"name":"Season 1","overview":"The first.",
		"season_number":1,"cover":"http://img.example/s1.jpg","cover_big":""},"not an object",
		{"season_number":"2","name":"Season 2","cover_big":"http://img.example/s2.jpg"},{"season_number":1,"name":"Again"}],
		"info":{"name":"Coastline","cover":"http://img.example/7001.jpg","plot":"Waves.","genre":"Documentary",
		"releaseDate":"2021-02-03","rating":"8","backdrop_path":["http://img.example/7001-wide.jpg"]},
		"episodes":{"2":[{"id":"9003","episode_num":1,"title":"Tides","container_extension":"mp4","season":2,
			"info":{"duration_secs":2700,"plot":"Up and down.","movie_image":"http://img.example/9003.jpg"}}],
		"1":[{"id":"9002","episode_num":"2","title":"Sand","container_extension":"mkv","season":"1","added":"1767225600",
			"info":[]},
			{"id":"9001","episode_num":1,"title":"","container_extension":"mkv","info":{"name":"Rocks","duration":"00:44:30"}},
			{"id":"x9","episode_num":3,"title":"Bad ID"},{"id":"9001","episode_num":9,"title":"Twice"}]}}`
)

func TestMovieCategories(t *testing.T) {
	srv, _ := panel(t, map[string]string{"get_vod_categories": movieCategories, "get_series_categories": `null`})
	c := client(srv, pass)
	got, err := c.MovieCategories(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Category{{ID: "21", Name: "Drama"}, {ID: "22", Name: "Comedy"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got, err := c.SeriesCategories(context.Background()); err != nil || len(got) != 0 {
		t.Errorf("series categories %+v, %v", got, err)
	}
}

func TestMovies(t *testing.T) {
	srv, seen := panel(t, map[string]string{"get_vod_streams": movies})
	var got []Movie
	skipped, err := client(srv, pass).Movies(context.Background(), func(m Movie) error {
		got = append(got, m)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 3 {
		t.Errorf("skipped %d, want 3", skipped)
	}
	want := []Movie{
		{ID: 5001, Name: "The Long Field", CategoryID: "21", Poster: "http://img.example/5001.jpg", Rating: 74, Year: 2024,
			Added: time.Unix(1767225600, 0).UTC(), Container: "mkv"},
		{ID: 5002, Name: "Quiet Harbour", CategoryID: "22", Rating: 80, Year: 2019, Added: time.Unix(1767312000, 0).UTC(),
			Container: "mp4", Runtime: 96, Plot: "A harbour, quietly.", Genre: "Drama", Cast: "A. Actor", Director: "D. Rector"},
		{ID: 5004, Name: "Odd", Rating: 65, Adult: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if q := (*seen)[0].URL.Query(); q.Get("action") != "get_vod_streams" || q.Has("category_id") {
		t.Errorf("asked %v", q)
	}
}

func TestMoviesStopsWhenTold(t *testing.T) {
	srv, _ := panel(t, map[string]string{"get_vod_streams": movies})
	stop := errors.New("stop")
	n := 0
	_, err := client(srv, pass).Movies(context.Background(), func(Movie) error {
		n++
		return stop
	})
	if !errors.Is(err, stop) || n != 1 {
		t.Errorf("err %v after %d", err, n)
	}
}

func TestListAnswers(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       error // nil: an empty list
	}{
		{"null", "null", nil},
		{"false", " false", nil},
		{"empty object", "{}", nil},
		{"empty list", "[]", nil},
		{"an object", `{"error":"no"}`, errShape},
		{"a web page", "<html>busy</html>", ErrNotJSON},
		{"cut short", `[{"stream_id":1,"name":"A"},{"stream_id":2,`, errBroken},
		{"nothing", "", errBroken},
		{"a number", "42", ErrNotJSON},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := panel(t, map[string]string{"get_vod_streams": tc.body})
			_, err := client(srv, pass).Movies(context.Background(), func(Movie) error { return nil })
			if !errors.Is(err, tc.want) || (tc.want == nil && err != nil) {
				t.Errorf("err %v, want %v", err, tc.want)
			}
		})
	}
}

func TestListTooLarge(t *testing.T) {
	srv, _ := panel(t, map[string]string{"get_vod_streams": movies})
	c := client(srv, pass)
	c.MaxListBytes = 300
	_, err := c.Movies(context.Background(), func(Movie) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("err %v", err)
	}
}

func TestListRefused(t *testing.T) {
	srv, _ := panel(t, map[string]string{"get_series": "503 busy"})
	_, err := client(srv, pass).Series(context.Background(), func(Show) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "503") || strings.Contains(err.Error(), srv.URL) {
		t.Errorf("err %v", err)
	}
}

func TestSeries(t *testing.T) {
	srv, _ := panel(t, map[string]string{"get_series": seriesList})
	var got []Show
	skipped, err := client(srv, pass).Series(context.Background(), func(s Show) error {
		got = append(got, s)
		return nil
	})
	if err != nil || skipped != 1 {
		t.Fatalf("skipped %d, err %v", skipped, err)
	}
	want := []Show{
		{ID: 7001, Name: "Coastline", CategoryID: "31", Poster: "http://img.example/7001.jpg",
			Backdrop: "http://img.example/7001-wide.jpg", Rating: 80, Year: 2021, Modified: time.Unix(1767225600, 0).UTC(),
			Runtime: 45, Plot: "Waves.", Genre: "Documentary", Cast: "B. Actor"},
		{ID: 7002, Name: "Night Shift", Backdrop: "http://img.example/7002-wide.jpg", Year: 2018},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestMovieInfo(t *testing.T) {
	srv, seen := panel(t, map[string]string{"get_vod_info": movieInfo})
	got, err := client(srv, pass).MovieInfo(context.Background(), 5001)
	if err != nil {
		t.Fatal(err)
	}
	want := MovieInfo{Name: "The Long Field", Poster: "http://img.example/5001-big.jpg",
		Backdrop: "http://img.example/5001-wide.jpg", Plot: "A long field.", Genre: "Drama", Cast: "D. Actor",
		Director: "C. Director", Country: "Nowhere", Released: "2024-09-12", AgeRating: "PG", Year: 2024, Rating: 74,
		Duration: time.Hour + 52*time.Minute + 10*time.Second, Container: "mkv"}
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
	if q := (*seen)[0].URL.Query(); q.Get("vod_id") != "5001" {
		t.Errorf("asked %v", q)
	}
}

func TestMovieInfoWithoutDetails(t *testing.T) {
	for _, tc := range []struct {
		body string
		want MovieInfo
		err  error
	}{
		{`{"info":[],"movie_data":{"stream_id":5002,"name":"Quiet Harbour","container_extension":"avi"}}`,
			MovieInfo{Name: "Quiet Harbour", Container: "avi"}, nil},
		{`{"info":{"duration":"1:30","episode_run_time":"95"},"movie_data":[]}`, MovieInfo{Duration: 95 * time.Minute}, nil},
		{`[]`, MovieInfo{}, ErrNoTitle},
		{`{"info":[],"movie_data":[]}`, MovieInfo{}, ErrNoTitle},
		{`null`, MovieInfo{}, ErrNoTitle},
		{`<p>gone</p>`, MovieInfo{}, ErrNotJSON},
	} {
		srv, _ := panel(t, map[string]string{"get_vod_info": tc.body})
		got, err := client(srv, pass).MovieInfo(context.Background(), 5002)
		if got != tc.want || !errors.Is(err, tc.err) || (tc.err == nil && err != nil) {
			t.Errorf("%s: got %+v, %v; want %+v, %v", tc.body, got, err, tc.want, tc.err)
		}
	}
}

func TestSeriesInfo(t *testing.T) {
	srv, seen := panel(t, map[string]string{"get_series_info": seriesInfo})
	got, err := client(srv, pass).SeriesInfo(context.Background(), 7001)
	if err != nil {
		t.Fatal(err)
	}
	if q := (*seen)[0].URL.Query(); q.Get("series_id") != "7001" {
		t.Errorf("asked %v", q)
	}
	if got.Name != "Coastline" || got.Year != 2021 || got.Rating != 80 || got.Backdrop != "http://img.example/7001-wide.jpg" {
		t.Errorf("info %+v", got)
	}
	wantSeasons := []Season{{Number: 1, Name: "Season 1", Poster: "http://img.example/s1.jpg", Plot: "The first."},
		{Number: 2, Name: "Season 2", Poster: "http://img.example/s2.jpg"}}
	if !reflect.DeepEqual(got.Seasons, wantSeasons) {
		t.Errorf("seasons %+v", got.Seasons)
	}
	wantEpisodes := []Episode{
		{ID: "9001", Season: 1, Number: 1, Name: "Rocks", Container: "mkv", Duration: 44*time.Minute + 30*time.Second},
		{ID: "9002", Season: 1, Number: 2, Name: "Sand", Container: "mkv", Added: time.Unix(1767225600, 0).UTC()},
		{ID: "9003", Season: 2, Number: 1, Name: "Tides", Container: "mp4", Plot: "Up and down.",
			Picture: "http://img.example/9003.jpg", Duration: 45 * time.Minute},
	}
	if !reflect.DeepEqual(got.Episodes, wantEpisodes) {
		t.Errorf("episodes\n got  %+v\n want %+v", got.Episodes, wantEpisodes)
	}
}

func TestSeriesInfoEpisodeShapes(t *testing.T) {
	for _, tc := range []struct{ name, episodes string }{
		{"list of seasons", `[[{"id":"1","episode_num":1}],[{"id":"2","episode_num":1}]]`},
		{"one list", `[{"id":"1","episode_num":1,"season":1},{"id":"2","episode_num":1,"season":2}]`},
		{"keyed", `{"1":[{"id":"1","episode_num":1}],"2":[{"id":"2","episode_num":1}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := panel(t, map[string]string{"get_series_info": `{"info":[],"episodes":` + tc.episodes + `}`})
			got, err := client(srv, pass).SeriesInfo(context.Background(), 7001)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Episodes) != 2 || got.Episodes[0].Season != 1 || got.Episodes[1].Season != 2 {
				t.Errorf("episodes %+v", got.Episodes)
			}
		})
	}
	srv, _ := panel(t, map[string]string{"get_series_info": `{"info":[],"episodes":[]}`})
	if _, err := client(srv, pass).SeriesInfo(context.Background(), 7001); !errors.Is(err, ErrNoTitle) {
		t.Errorf("an empty series: %v", err)
	}
}

func TestParsers(t *testing.T) {
	for _, tc := range []struct {
		y, date string
		want    int
	}{{"2024", "", 2024}, {"", "2019-05-03", 2019}, {"x", "1999", 1999}, {"", "20190503", 0}, {"1700", "", 0}, {"", "", 0}} {
		if got := year(tc.y, tc.date); got != tc.want {
			t.Errorf("year(%q, %q) = %d, want %d", tc.y, tc.date, got, tc.want)
		}
	}
	for _, tc := range []struct {
		ten, five decimal
		want      int
	}{{7.44, 0, 74}, {0, 3.7, 74}, {11, 4, 80}, {0, 0, 0}, {-1, 6, 0}} {
		if got := rating(tc.ten, tc.five); got != tc.want {
			t.Errorf("rating(%v, %v) = %d, want %d", tc.ten, tc.five, got, tc.want)
		}
	}
	for _, tc := range []struct {
		secs int64
		hms  string
		mins int64
		want time.Duration
	}{{5400, "", 0, 90 * time.Minute}, {0, "02:00:01", 0, 2*time.Hour + time.Second}, {0, "1:30", 95, 95 * time.Minute},
		{0, "aa:bb:cc", 0, 0}, {-5, "", 2000, 0}} {
		if got := duration(tc.secs, tc.hms, tc.mins); got != tc.want {
			t.Errorf("duration(%d, %q, %d) = %v, want %v", tc.secs, tc.hms, tc.mins, got, tc.want)
		}
	}
	for in, want := range map[string]string{"MKV": "mkv", ".mp4": "mp4", "m2ts": "m2ts", "../ts": "", "toolong": "", "": ""} {
		if got := container(in); got != want {
			t.Errorf("container(%q) = %q, want %q", in, got, want)
		}
	}
}

// FuzzMovies feeds a film list to the decoder, which must never fail on
// what a server sends, and never keep a film without an ID and a name.
func FuzzMovies(f *testing.F) {
	f.Add([]byte(movies))
	f.Add([]byte(`[{"stream_id":"1","name":"A","rating":"1e400","added":"-1"},[],{"stream_id":2}]`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		_, _ = decodeList(bytes.NewReader(body), 1<<16, func(e movieEntry) (bool, error) {
			m, kept := e.movie()
			if kept && (m.ID <= 0 || m.Name == "" || m.Rating < 0 || m.Rating > 100 || m.Year < 0) {
				t.Errorf("kept %+v", m)
			}
			return kept, nil
		})
	})
}

// FuzzSeriesInfo feeds a series' details to the parser.
func FuzzSeriesInfo(f *testing.F) {
	f.Add([]byte(seriesInfo))
	f.Add([]byte(`{"episodes":[[{"id":"1"}],{"id":"2"},null],"seasons":{"a":1}}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		var answer seriesAnswer
		if json.Unmarshal(body, &answer) != nil {
			return
		}
		got, err := answer.show()
		if err != nil {
			return
		}
		for _, e := range got.Episodes {
			if e.ID == "" || strings.Trim(e.ID, "0123456789") != "" || e.Season < 0 || e.Number < 0 {
				t.Errorf("kept %+v", e)
			}
		}
		if len(got.Episodes) > maxEpisodes || strings.Contains(got.Name, "\n") {
			t.Errorf("kept %d episodes, name %q", len(got.Episodes), got.Name)
		}
	})
}
