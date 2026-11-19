// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

const plexToken = "fictional-plex-secret"

// newPlex returns a fictional Plex server with a library of films, one of
// shows and one of music. It counts the requests for each path.
func newPlex(t *testing.T) (func(string) int, *httptest.Server) {
	t.Helper()
	var mu sync.Mutex
	calls := map[string]int{}
	part := func(key, ext string) []map[string]any {
		return []map[string]any{{"container": ext, "Part": []map[string]any{
			{"key": "/library/parts/" + key + "/1700000000/file." + ext, "container": ext}}}}
	}
	field := map[string]any{"ratingKey": "101", "type": "movie", "title": "Another Field", "year": 2024,
		"audienceRating": 7.4, "contentRating": "PG-13", "summary": "A field, again.", "duration": 5_400_000,
		"addedAt": 1796380200, "originallyAvailableAt": "2024-05-01",
		"thumb": "/library/metadata/101/thumb/1", "art": "/library/metadata/101/art/1",
		"Genre": []map[string]any{{"tag": "Drama"}, {"tag": "Mystery"}}, "Media": part("9101", "mkv")}
	details := map[string]any{}
	for k, v := range field {
		details[k] = v
	}
	details["Role"] = []map[string]any{{"tag": "A. Actor"}}
	details["Director"] = []map[string]any{{"tag": "D. Director"}}
	details["Country"] = []map[string]any{{"tag": "Wales"}}
	details["Media"] = []map[string]any{{"container": "mkv", "Part": []map[string]any{{
		"key": "/library/parts/9101/1700000000/file.mkv", "container": "mkv", "Stream": []map[string]any{
			{"id": 3, "streamType": 3, "index": 2, "codec": "srt", "languageCode": "eng"},
			{"id": 6, "streamType": 3, "codec": "srt", "languageCode": "fre", "key": "/library/streams/6"},
			{"id": 7, "streamType": 3, "codec": "vobsub", "languageCode": "spa", "key": "/library/streams/7"},
		}}}}}
	lists := map[string][]map[string]any{
		"1": {field, {"ratingKey": "102", "type": "movie", "title": "Bright Harbour", "Media": part("9102", "mp4")},
			{"ratingKey": "103", "type": "movie", "title": "Not Yet There"}},
		"2": {{"ratingKey": "300", "type": "show", "title": "Coastline", "summary": "Waves.", "addedAt": 1796127600,
			"thumb": "/library/metadata/300/thumb/1"}},
		"4": {field, {"ratingKey": "105", "type": "movie", "title": "Calm Water", "Media": part("9105", "avi")}},
	}
	episodes := []map[string]any{
		{"ratingKey": "301", "type": "episode", "title": "Rocks", "index": 1, "parentIndex": 1, "parentTitle": "Season 1",
			"duration": 2_400_000, "summary": "Rocks, mostly.", "thumb": "/library/metadata/301/thumb/1",
			"Media": part("3011", "mkv")},
		{"ratingKey": "302", "type": "episode", "title": "Sand", "index": 2, "parentIndex": 1, "parentTitle": "Season 1"},
		{"ratingKey": "303", "type": "episode", "title": "Tides", "index": 1, "parentIndex": 2, "parentTitle": "The Second",
			"Media": part("3031", "mp4")},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls[r.URL.Path]++
		mu.Unlock()
		if r.Header.Get("X-Plex-Token") != plexToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var answer map[string]any
		switch r.URL.Path {
		case "/library/sections/all":
			answer = map[string]any{"Directory": []map[string]any{
				{"key": "1", "title": "Films", "type": "movie"},
				{"key": "2", "title": "Shows", "type": "show"},
				{"key": "3", "title": "Music", "type": "artist"},
				{"key": "4", "title": "More Films", "type": "movie"},
			}}
		case "/library/sections/1/all", "/library/sections/2/all", "/library/sections/4/all":
			items := lists[strings.Split(r.URL.Path, "/")[3]]
			answer = map[string]any{"size": len(items), "totalSize": len(items), "Metadata": items}
		case "/library/metadata/101":
			answer = map[string]any{"Metadata": []map[string]any{details}}
		case "/library/metadata/300":
			answer = map[string]any{"Metadata": lists["2"]}
		case "/library/metadata/300/allLeaves":
			answer = map[string]any{"size": len(episodes), "Metadata": episodes}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": answer})
	}))
	t.Cleanup(srv.Close)
	return func(path string) int {
		mu.Lock()
		defer mu.Unlock()
		return calls[path]
	}, srv
}

// newPlexLibrary returns a library over a store with one Plex source.
func newPlexLibrary(t *testing.T, srv *httptest.Server) (*Library, *store.Store, *syncBuffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.AddSource(context.Background(), source.Settings{Name: "lounge", Type: source.TypePlex, URL: srv.URL,
		Password: plexToken}); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	l := New(st, slog.New(slog.NewTextHandler(logs, nil)), "test")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return l, st, logs
}

func TestPlexFilms(t *testing.T) {
	count, srv := newPlex(t)
	l, st, logs := newPlexLibrary(t, srv)
	ctx := context.Background()
	state, err := l.Want(ctx, store.KindMovie, 5*time.Second)
	if err != nil || !state.Ready() || state.Sources != 1 {
		t.Fatalf("state %+v, %v", state, err)
	}
	// Each library is a shelf; a film in two is on the first's; a film
	// without a file is left out.
	want := map[string][]string{"Films": {"Another Field", "Bright Harbour"}, "More Films": {"Calm Water"}}
	if got := shelfTitles(t, st, store.KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %v", got)
	}
	recent, err := st.RecentTitles(ctx, store.KindMovie, 1)
	if err != nil || len(recent) != 1 || recent[0].Name != "Another Field" ||
		recent[0].Poster != "plex:480x720/library/metadata/101/thumb/1" {
		t.Fatalf("recent %+v, %v", recent, err)
	}
	title, err := st.Title(ctx, recent[0].ID)
	if err != nil || title.Container != "mkv" || title.Summary.Genre != "Drama, Mystery" || title.Summary.Runtime != 90 ||
		title.Summary.Backdrop != "plex:1280x720/library/metadata/101/art/1" {
		t.Errorf("title %+v, %v", title, err)
	}

	o, err := l.Open(ctx, recent[0].ID)
	if err != nil || o.ReadError != "" || o.Details == nil {
		t.Fatalf("opened %+v, %v", o, err)
	}
	if d := o.Details; d.Duration != 5400 || d.Released != "2024-05-01" || d.Country != "Wales" || d.AgeRating != "PG-13" ||
		d.Rating != 74 || d.Year != 2024 || d.Cast != "A. Actor" || d.Director != "D. Director" {
		t.Errorf("details %+v", d)
	}
	p, err := st.PlayableMovie(ctx, recent[0].ID)
	if err != nil || p.Key != "101" ||
		p.StreamURL != srv.URL+"/library/parts/9101/1700000000/file.mkv?X-Plex-Token="+plexToken {
		t.Errorf("playable %+v, %v", p, err)
	}
	// The file beside the film; the one in it stays with the box's player,
	// and pictures are left out.
	subs := l.Subtitles(ctx, p)
	if len(subs) != 1 || subs[0].Language != "fre" || subs[0].Format != "srt" ||
		subs[0].URL != srv.URL+"/library/streams/6.srt?X-Plex-Token="+plexToken+"&encoding=utf-8&format=srt" {
		t.Errorf("subtitles %+v", subs)
	}
	if strings.Contains(logs.String(), plexToken) {
		t.Errorf("the token in the log:\n%s", logs)
	}

	// A quarter of an hour on, the list is read again.
	before := count("/library/sections/all")
	later := time.Now().Add(OwnTTL + time.Minute)
	l.now = func() time.Time { return later }
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return count("/library/sections/all") == before+1 })
}

func TestPlexSeries(t *testing.T) {
	_, srv := newPlex(t)
	l, st, _ := newPlexLibrary(t, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindSeries, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := shelfTitles(t, st, store.KindSeries); !reflect.DeepEqual(got, map[string][]string{"Shows": {"Coastline"}}) {
		t.Errorf("shelves %v", got)
	}
	recent, err := st.RecentTitles(ctx, store.KindSeries, 1)
	if err != nil || len(recent) != 1 || !recent[0].AddedAt.Equal(time.Unix(1796127600, 0)) {
		t.Fatalf("recent %+v, %v", recent, err)
	}
	o, err := l.Open(ctx, recent[0].ID)
	if err != nil || o.ReadError != "" || o.Details == nil {
		t.Fatalf("opened %+v, %v", o, err)
	}
	wantSeasons := []store.SeasonDetails{{Number: 1, Name: "Season 1"}, {Number: 2, Name: "The Second"}}
	if !reflect.DeepEqual(o.Details.Seasons, wantSeasons) || len(o.Episodes) != 2 {
		t.Fatalf("seasons %+v, episodes %+v", o.Details.Seasons, o.Episodes)
	}
	if e := o.Episodes[0]; e.Name != "Rocks" || e.Duration != 2400 || e.Plot != "Rocks, mostly." ||
		e.Picture != "plex:640x360/library/metadata/301/thumb/1" {
		t.Errorf("first episode %+v", e)
	}
	p, err := st.PlayableEpisode(ctx, o.Episodes[1].ID)
	if err != nil || p.Container != "mp4" || p.Key != "303" ||
		p.StreamURL != srv.URL+"/library/parts/3031/1700000000/file.mp4?X-Plex-Token="+plexToken {
		t.Errorf("playable %+v, %v", p, err)
	}
}
