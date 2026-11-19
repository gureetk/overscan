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

const apiKey = "0123456789abcdef-secret"

// jellyfinServer is a fictional Jellyfin server with a library of films,
// one of shows, one of home videos and one of both, and a user who sees
// only the films. It counts the requests for each path.
type jellyfinServer struct {
	mu    sync.Mutex
	calls map[string]int
}

func (j *jellyfinServer) count(path string) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls[path]
}

func newJellyfin(t *testing.T) (*jellyfinServer, *httptest.Server) {
	t.Helper()
	j := &jellyfinServer{calls: map[string]int{}}
	field := map[string]any{"Id": "a1", "Name": "Another Field", "Type": "Movie", "Container": "matroska,webm",
		"ProductionYear": 2024, "CommunityRating": 7.4, "OfficialRating": "PG-13", "Overview": "A field, again.",
		"Genres": []string{"Drama", "Mystery"}, "ProductionLocations": []string{"Wales"},
		"People":       []map[string]any{{"Name": "A. Actor", "Type": "Actor"}, {"Name": "D. Director", "Type": "Director"}},
		"RunTimeTicks": int64(5400) * 10_000_000, "DateCreated": "2026-12-04T10:30:00.0000000Z",
		"PremiereDate": "2024-05-01T00:00:00.0000000Z",
		"ImageTags":    map[string]string{"Primary": "pa1"}, "BackdropImageTags": []string{"ba1"},
		"MediaSources": []map[string]any{{"Id": "ms1", "MediaStreams": []map[string]any{
			{"Type": "Subtitle", "Index": 2, "Codec": "subrip", "Language": "eng", "IsTextSubtitleStream": true},
			{"Type": "Subtitle", "Index": 3, "Codec": "PGSSUB", "Language": "fre", "IsTextSubtitleStream": false}}}}}
	coastline := map[string]any{"Id": "s9", "Name": "Coastline", "Type": "Series", "Overview": "Waves.",
		"DateCreated": "2025-01-01T00:00:00.0000000Z", "DateLastMediaAdded": "2026-12-01T08:00:00.0000000Z",
		"ImageTags": map[string]string{"Primary": "ps9"}}
	lists := map[string][]map[string]any{
		"lib1 Movie": {field, {"Id": "a2", "Name": "Bright Harbour", "Type": "Movie", "Container": "mov,mp4,m4a,3gp,3g2,mj2"}},
		"lib3 Video": {{"Id": "v1", "Name": "Birthday", "Type": "Video", "Container": "mp4"},
			{"Id": "v2", "Name": "Birthday: the making of", "Type": "Video", "ExtraType": "BehindTheScenes"}},
		"lib4 Movie":  {field, {"Id": "a5", "Name": "Calm Water", "Type": "Movie"}},
		"lib2 Series": {coastline},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		j.mu.Lock()
		j.calls[r.URL.Path]++
		j.mu.Unlock()
		if !strings.Contains(r.Header.Get("Authorization"), `Token="`+apiKey+`"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		var answer any
		switch r.URL.Path {
		case "/Users":
			answer = []map[string]any{{"Name": "Anna", "Id": "u1"}}
		case "/Library/MediaFolders":
			answer = map[string]any{"Items": []map[string]any{
				{"Id": "lib1", "Name": "Films", "CollectionType": "movies"},
				{"Id": "lib2", "Name": "Shows", "CollectionType": "tvshows"},
				{"Id": "lib3", "Name": "Home", "CollectionType": "homevideos"},
				{"Id": "lib4", "Name": "Everything"},
				{"Id": "lib5", "Name": "Music", "CollectionType": "music"},
			}}
		case "/UserViews":
			answer = map[string]any{"Items": []map[string]any{{"Id": "lib1", "Name": "Films", "CollectionType": "movies"}}}
		case "/Items":
			if id := q.Get("Ids"); id != "" {
				var found []map[string]any
				for _, item := range []map[string]any{field, coastline} {
					if item["Id"] == id {
						found = append(found, item)
					}
				}
				answer = map[string]any{"Items": found, "TotalRecordCount": len(found)}
				break
			}
			items := lists[q.Get("ParentId")+" "+q.Get("IncludeItemTypes")]
			answer = map[string]any{"Items": items, "TotalRecordCount": len(items)}
		case "/Shows/s9/Episodes":
			answer = map[string]any{"Items": []map[string]any{
				{"Id": "e1", "Name": "Rocks", "Type": "Episode", "IndexNumber": 1, "ParentIndexNumber": 1,
					"SeasonName": "Season 1", "Container": "mkv", "RunTimeTicks": int64(2400) * 10_000_000,
					"Overview": "Rocks, mostly.", "ImageTags": map[string]string{"Primary": "t1"}},
				{"Id": "e2", "Name": "Sand", "Type": "Episode", "IndexNumber": 2, "ParentIndexNumber": 1, "LocationType": "Virtual"},
				{"Id": "e3", "Name": "Tides", "Type": "Episode", "IndexNumber": 1, "ParentIndexNumber": 2,
					"SeasonName": "The Second", "Container": "mov,mp4,m4a,3gp,3g2,mj2"},
			}}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return j, srv
}

// newJellyfinLibrary returns a library over a store with one Jellyfin
// source, which reads as user if not "".
func newJellyfinLibrary(t *testing.T, srv *httptest.Server, user string) (*Library, *store.Store, *syncBuffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.AddSource(context.Background(), source.Settings{Name: "den", Type: source.TypeJellyfin, URL: srv.URL,
		Username: user, Password: apiKey}); err != nil {
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

// shelfTitles returns the names of the titles on each shelf of a kind.
func shelfTitles(t *testing.T, st *store.Store, kind string) map[string][]string {
	t.Helper()
	ctx := context.Background()
	shelves, err := st.Shelves(ctx, kind)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for _, sh := range shelves {
		rows, err := st.ShelfTitles(ctx, kind, sh.ID, store.ByName)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range rows {
			out[sh.Name] = append(out[sh.Name], r.Name)
		}
	}
	return out
}

func TestJellyfinFilms(t *testing.T) {
	j, srv := newJellyfin(t)
	l, st, logs := newJellyfinLibrary(t, srv, "")
	ctx := context.Background()
	state, err := l.Want(ctx, store.KindMovie, 5*time.Second)
	if err != nil || !state.Ready() || state.Sources != 1 {
		t.Fatalf("state %+v, %v", state, err)
	}
	// Each library is a shelf; a film in two is on the first's; extras
	// are left out.
	want := map[string][]string{"Films": {"Another Field", "Bright Harbour"}, "Home": {"Birthday"}, "Everything": {"Calm Water"}}
	if got := shelfTitles(t, st, store.KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %v", got)
	}
	recent, err := st.RecentTitles(ctx, store.KindMovie, 1)
	if err != nil || len(recent) != 1 || recent[0].Name != "Another Field" ||
		recent[0].Poster != srv.URL+"/Items/a1/Images/Primary?maxWidth=480&tag=pa1" {
		t.Fatalf("recent %+v, %v", recent, err)
	}
	title, err := st.Title(ctx, recent[0].ID)
	if err != nil || title.Container != "mkv" || title.Summary.Genre != "Drama, Mystery" || title.Summary.Cast != "A. Actor" ||
		title.Summary.Director != "D. Director" || title.Summary.Runtime != 90 ||
		title.Summary.Backdrop != srv.URL+"/Items/a1/Images/Backdrop/0?maxWidth=1280&tag=ba1" {
		t.Errorf("title %+v, %v", title, err)
	}

	// Opening a film reads it again.
	o, err := l.Open(ctx, recent[0].ID)
	if err != nil || o.ReadError != "" || o.Details == nil {
		t.Fatalf("opened %+v, %v", o, err)
	}
	if d := o.Details; d.Duration != 5400 || d.Released != "2024-05-01" || d.Country != "Wales" || d.AgeRating != "PG-13" ||
		d.Rating != 74 || d.Year != 2024 || d.Plot != "A field, again." {
		t.Errorf("details %+v", d)
	}
	p, err := st.PlayableMovie(ctx, recent[0].ID)
	if err != nil || p.StreamURL != srv.URL+"/Videos/a1/stream.mkv?ApiKey="+apiKey+"&static=true" {
		t.Errorf("playable %+v, %v", p, err)
	}
	if strings.Contains(logs.String(), apiKey) {
		t.Errorf("the key in the log:\n%s", logs)
	}
	subs := l.Subtitles(ctx, p)
	if len(subs) != 1 || subs[0].Language != "eng" || subs[0].Format != "srt" ||
		subs[0].URL != srv.URL+"/Videos/a1/ms1/Subtitles/2/Stream.srt?ApiKey="+apiKey {
		t.Errorf("subtitles %+v", subs)
	}

	// A quarter of an hour on, the list is read again.
	before := j.count("/Library/MediaFolders")
	later := time.Now().Add(OwnTTL + time.Minute)
	l.now = func() time.Time { return later }
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return j.count("/Library/MediaFolders") == before+1 })
}

func TestJellyfinSeries(t *testing.T) {
	_, srv := newJellyfin(t)
	l, st, _ := newJellyfinLibrary(t, srv, "")
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindSeries, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Shows, and the library of both, which has no series.
	if got := shelfTitles(t, st, store.KindSeries); !reflect.DeepEqual(got, map[string][]string{"Shows": {"Coastline"}}) {
		t.Errorf("shelves %v", got)
	}
	recent, err := st.RecentTitles(ctx, store.KindSeries, 1)
	if err != nil || len(recent) != 1 || !recent[0].AddedAt.Equal(time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)) {
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
		e.Picture != srv.URL+"/Items/e1/Images/Primary?maxWidth=640&tag=t1" {
		t.Errorf("first episode %+v", e)
	}
	p, err := st.PlayableEpisode(ctx, o.Episodes[1].ID)
	if err != nil || p.Container != "mp4" || p.StreamURL != srv.URL+"/Videos/e3/stream.mp4?ApiKey="+apiKey+"&static=true" {
		t.Errorf("playable %+v, %v", p, err)
	}
}

func TestJellyfinUser(t *testing.T) {
	j, srv := newJellyfin(t)
	l, st, _ := newJellyfinLibrary(t, srv, "anna")
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := shelfTitles(t, st, store.KindMovie); !reflect.DeepEqual(got, map[string][]string{"Films": {"Another Field", "Bright Harbour"}}) {
		t.Errorf("Anna's shelves %v", got)
	}
	if j.count("/Library/MediaFolders") != 0 || j.count("/UserViews") != 1 {
		t.Errorf("calls %v", j.calls)
	}
}

func TestJellyfinWrongKey(t *testing.T) {
	_, srv := newJellyfin(t)
	l, st, _ := newJellyfinLibrary(t, srv, "")
	ctx := context.Background()
	src, err := st.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSource(ctx, src[0].ID, source.Settings{Name: "den", Type: source.TypeJellyfin, Password: "wrong"}); err != nil {
		t.Fatal(err)
	}
	state, err := l.Want(ctx, store.KindMovie, 5*time.Second)
	if err != nil || state.Ready() || !strings.Contains(state.Error, "refused the API key") {
		t.Errorf("state %+v, %v", state, err)
	}
}
