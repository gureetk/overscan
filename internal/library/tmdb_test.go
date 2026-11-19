// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// fakeTMDB is a made-up TMDB, written from docs/protocol/tmdb-notes.md,
// that knows Calm Water (2021) and the series Coastline (2019). It counts
// the requests by path.
type fakeTMDB struct {
	mu    sync.Mutex
	asked map[string]int
	key   string
}

func newFakeTMDB(t *testing.T) (*fakeTMDB, *httptest.Server) {
	t.Helper()
	f := &fakeTMDB{asked: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.asked[r.URL.Path]++
		f.key = r.URL.Query().Get("api_key")
		f.mu.Unlock()
		q := r.URL.Query()
		if q.Get("api_key") != "made-up-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/3/search/movie":
			if q.Get("query") == "Calm Water" && q.Get("year") == "2021" && q.Get("include_adult") == "false" {
				fmt.Fprint(w, `{"results":[{"id":11,"title":"Calm Water","release_date":"2021-03-03"},
					{"id":12,"title":"Calm Waters","release_date":"2021-05-05"}]}`)
				return
			}
			fmt.Fprint(w, `{"results":[]}`)
		case "/3/movie/11":
			fmt.Fprint(w, `{"id":11,"title":"Calm Water","overview":"A lake, at dawn.","release_date":"2021-03-03",
				"runtime":88,"genres":[{"name":"Drama"},{"name":"Mystery"}],"poster_path":"/calmposter.jpg",
				"backdrop_path":"/calmback.jpg","vote_average":6.8,
				"credits":{"cast":[{"name":"B. Actor","character":"Ann","order":0}],
				"crew":[{"name":"C. Director","job":"Director"},{"name":"D. Writer","job":"Writer"}]}}`)
		case "/3/search/tv":
			if q.Get("query") == "Coastline" && q.Get("first_air_date_year") == "2019" {
				fmt.Fprint(w, `{"results":[{"id":22,"name":"Coastline","first_air_date":"2019-02-02"}]}`)
				return
			}
			fmt.Fprint(w, `{"results":[]}`)
		case "/3/tv/22":
			fmt.Fprint(w, `{"id":22,"name":"Coastline","overview":"The sea.","first_air_date":"2019-02-02",
				"poster_path":"/coastposter.jpg","vote_average":7.1,
				"seasons":[{"season_number":1,"name":"Season One","overview":"Rocks and tides.","poster_path":"/s1.jpg"},
				{"season_number":2,"name":"Season Two"}]}`)
		case "/3/tv/22/season/1":
			fmt.Fprint(w, `{"season_number":1,"episodes":[{"episode_number":1,"name":"Rocks","overview":"First.","still_path":"/e1.jpg","runtime":44},
				{"episode_number":2,"name":"Sand","overview":"Second.","still_path":"/e2.jpg","runtime":45}]}`)
		case "/3/tv/22/season/2":
			fmt.Fprint(w, `{"season_number":2,"episodes":[{"episode_number":1,"name":"Tides","overview":"Third."}]}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *fakeTMDB) count(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[path]
}

// withTMDB gives a folder library TMDB's key and the fake's address.
func withTMDBKey(t *testing.T, l *Library, st *store.Store, srv *httptest.Server) {
	t.Helper()
	pace := tmdbPace
	tmdbPace = 0
	t.Cleanup(func() { tmdbPace = pace })
	l.tmdbBase = srv.URL + "/3"
	if err := st.SetTMDBKey(context.Background(), "made-up-key"); err != nil {
		t.Fatal(err)
	}
}

// rowNamed returns the shelf row of the title with the name.
func rowNamed(t *testing.T, st *store.Store, kind, name string) store.TitleRow {
	t.Helper()
	recent, err := st.RecentTitles(context.Background(), kind, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recent {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no %s named %q in %+v", kind, name, recent)
	return store.TitleRow{}
}

// TestTMDBFilms gives a folder's film that lacks a poster and a plot what
// TMDB says of it, on its shelf once the background lookup is done, and
// when it is opened; a film with its own leaves TMDB unasked (D88).
func TestTMDBFilms(t *testing.T) {
	l, st, _ := newFolderLibrary(t)
	f, srv := newFakeTMDB(t)
	withTMDBKey(t, l, st, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return rowNamed(t, st, store.KindMovie, "Calm Water").Poster == "https://image.tmdb.org/t/p/w342/calmposter.jpg"
	})
	if f.count("/3/search/movie") != 1 || f.count("/3/movie/11") != 1 {
		t.Errorf("asked %v", f.asked)
	}
	long := rowNamed(t, st, store.KindMovie, "The Long Field")
	if !strings.HasSuffix(long.Poster, "/poster.jpg") || long.Rating != 74 {
		t.Errorf("a film with its own poster %+v", long)
	}
	calm := rowNamed(t, st, store.KindMovie, "Calm Water")
	if calm.Rating != 68 || calm.Year != 2021 {
		t.Errorf("calm water on its shelf %+v", calm)
	}
	o, err := l.Open(ctx, calm.ID)
	if err != nil || o.ReadError != "" || o.Details == nil {
		t.Fatalf("opened %+v, %v", o, err)
	}
	if d := o.Details; d.Plot != "A lake, at dawn." || d.Genre != "Drama, Mystery" || d.Cast != "B. Actor" ||
		d.Director != "C. Director" || d.Duration != 88*60 || d.Released != "2021-03-03" ||
		d.Backdrop != "https://image.tmdb.org/t/p/w1280/calmback.jpg" {
		t.Errorf("details %+v", d)
	}
	// What TMDB said is kept: opening it again asks nothing.
	if f.count("/3/movie/11") != 1 {
		t.Errorf("asked again %v", f.asked)
	}
	if found, missed, err := st.TMDBCounts(ctx); err != nil || found != 1 || missed != 0 {
		t.Errorf("counts %d %d, %v", found, missed, err)
	}

	// Without the key, what TMDB said is forgotten at the next read.
	if err := st.SetTMDBKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(OwnTTL + time.Minute)
	l.now = func() time.Time { return later }
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return rowNamed(t, st, store.KindMovie, "Calm Water").Poster == "" })
}

// TestTMDBSeries looks a folder's series up on TMDB by its name and year,
// and fills its seasons and episodes when it is opened, keeping what its
// folder says first (D88).
func TestTMDBSeries(t *testing.T) {
	l, st, _ := newFolderLibrary(t)
	f, srv := newFakeTMDB(t)
	withTMDBKey(t, l, st, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindSeries, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return rowNamed(t, st, store.KindSeries, "Coastline").Poster == "https://image.tmdb.org/t/p/w342/coastposter.jpg"
	})
	o, err := l.Open(ctx, rowNamed(t, st, store.KindSeries, "Coastline").ID)
	if err != nil || o.ReadError != "" || o.Details == nil {
		t.Fatalf("opened %+v, %v", o, err)
	}
	if d := o.Details; d.Plot != "Waves." || len(d.Seasons) != 2 || d.Seasons[0].Name != "Season One" ||
		d.Seasons[0].Poster != "https://image.tmdb.org/t/p/w342/s1.jpg" {
		t.Errorf("details %+v", d)
	}
	if len(o.Episodes) != 3 {
		t.Fatalf("episodes %+v", o.Episodes)
	}
	if e := o.Episodes[0]; e.Name != "Rocks" || e.Plot != "First." || e.Picture != "https://image.tmdb.org/t/p/w300/e1.jpg" || e.Duration != 44*60 {
		t.Errorf("first episode %+v", e)
	}
	if e := o.Episodes[1]; e.Name != "Sand" || e.Plot != "Second." {
		t.Errorf("second episode %+v", e)
	}
	if e := o.Episodes[2]; e.Name != "Tides" || e.Plot != "Third." || e.Picture != "" {
		t.Errorf("third episode %+v", e)
	}
	if f.count("/3/search/tv") != 1 || f.count("/3/tv/22") != 1 || f.count("/3/tv/22/season/1") != 1 || f.count("/3/tv/22/season/2") != 1 {
		t.Errorf("asked %v", f.asked)
	}
}

// A key that TMDB refuses stops the lookups, and the titles stay as their
// folders have them.
func TestTMDBRefusedKey(t *testing.T) {
	l, st, _ := newFolderLibrary(t)
	f, srv := newFakeTMDB(t)
	withTMDBKey(t, l, st, srv)
	ctx := context.Background()
	if err := st.SetTMDBKey(ctx, "another-made-up-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return f.count("/3/search/movie") == 1 })
	time.Sleep(50 * time.Millisecond)
	if row := rowNamed(t, st, store.KindMovie, "Calm Water"); row.Poster != "" || f.count("/3/movie/11") != 0 {
		t.Errorf("calm water %+v, asked %v", row, f.asked)
	}
	if found, missed, _ := st.TMDBCounts(ctx); found != 0 || missed != 0 {
		t.Errorf("counts %d %d", found, missed)
	}
}
