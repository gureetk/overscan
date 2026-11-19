// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
)

// xtreamSource is a fictional Xtream source.
func xtreamSource(name string) source.Settings {
	return source.Settings{Name: name, Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "pw/" + name}
}

// The films of a fictional source.
var (
	filmCategories = []LibraryCategory{{Key: "21", Name: "Drama"}, {Key: "22", Name: "Comedy"}, {Key: "23", Name: "Empty"}}
	films          = []LibraryTitle{
		{Key: "5001", Category: "21", Name: "The Long Field", Year: 2024, Rating: 74, Poster: "http://img.example/5001.jpg",
			Container: "mkv", AddedAt: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Summary: TitleSummary{Genre: "Drama"}},
		{Key: "5002", Category: "22", Name: "quiet harbour", Year: 2019, AddedAt: time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC)},
		{Key: "5003", Category: "21", Name: "Around 100% of_it", AddedAt: time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)},
		{Key: "5004", Category: "nowhere", Name: "Lost Reel"},
		{Key: "5001", Category: "22", Name: "Twice"},
	}
)

// titleIDs returns the library's title IDs by name.
func titleIDs(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	rows, err := s.db.Query("SELECT name, id FROM titles")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			t.Fatal(err)
		}
		out[name] = id
	}
	return out
}

func names(rows []TitleRow) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

func TestApplyLibrary(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	ch, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at)
	if err != nil {
		t.Fatal(err)
	}
	if ch != (LibraryChanges{Added: 4}) {
		t.Errorf("changes %+v", ch)
	}
	shelves, err := s.Shelves(ctx, KindMovie)
	if err != nil {
		t.Fatal(err)
	}
	if len(shelves) != 2 || shelves[0].Name != "Drama" || shelves[0].Count != 2 || shelves[1].Name != "Comedy" || shelves[1].Count != 1 {
		t.Fatalf("shelves %+v", shelves)
	}
	drama, err := s.ShelfTitles(ctx, KindMovie, shelves[0].ID, ByAdded)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(drama); !reflect.DeepEqual(got, []string{"Around 100% of_it", "The Long Field"}) {
		t.Errorf("drama by date %q", got)
	}
	if drama[1].Year != 2024 || drama[1].Rating != 74 || drama[1].Poster != "http://img.example/5001.jpg" ||
		!drama[1].AddedAt.Equal(films[0].AddedAt) || drama[1].Kind != KindMovie {
		t.Errorf("row %+v", drama[1])
	}
	byName, err := s.ShelfTitles(ctx, KindMovie, shelves[0].ID, ByName)
	if err != nil || !reflect.DeepEqual(names(byName), []string{"Around 100% of_it", "The Long Field"}) {
		t.Errorf("drama by name %q, %v", names(byName), err)
	}
	if _, err := s.ShelfTitles(ctx, KindSeries, shelves[0].ID, ByAdded); !errors.Is(err, ErrNotFound) {
		t.Errorf("a film shelf as series: %v", err)
	}
	recent, err := s.RecentTitles(ctx, KindMovie, 2)
	if err != nil || !reflect.DeepEqual(names(recent), []string{"quiet harbour", "Around 100% of_it"}) {
		t.Errorf("recent %q, %v", names(recent), err)
	}
	reads, err := s.LibraryReads(ctx)
	if err != nil || len(reads) != 1 || reads[0].Titles != 4 || !reads[0].ReadAt.Equal(at) || reads[0].Error != "" {
		t.Errorf("reads %+v, %v", reads, err)
	}

	// The next read keeps IDs and details, and marks what it does not list.
	before := titleIDs(t, s)
	if err := s.SetMovieDetails(ctx, before["The Long Field"], TitleDetails{Duration: 6730}, "avi", at); err != nil {
		t.Fatal(err)
	}
	later := at.Add(24 * time.Hour)
	again := []LibraryTitle{{Key: "5001", Category: "22", Name: "The Long Field", Year: 2024}, films[1]}
	if ch, err = s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories[1:2], again, later); err != nil {
		t.Fatal(err)
	}
	if ch != (LibraryChanges{Updated: 2, Missing: 2}) {
		t.Errorf("changes %+v", ch)
	}
	if got := titleIDs(t, s); got["The Long Field"] != before["The Long Field"] {
		t.Errorf("IDs %v, before %v", got, before)
	}
	title, err := s.Title(ctx, before["The Long Field"])
	if err != nil {
		t.Fatal(err)
	}
	// The list gives no picture or container this time: the old ones stay.
	if title.Shelf != "Comedy" || title.Container != "avi" || title.Poster != "http://img.example/5001.jpg" ||
		title.Details == nil || title.Details.Duration != 6730 || !title.DetailsAt.Equal(at) {
		t.Errorf("title %+v, details %+v", title, title.Details)
	}
	if _, err := s.Title(ctx, before["Lost Reel"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing title: %v", err)
	}
	shelves, _ = s.Shelves(ctx, KindMovie)
	if len(shelves) != 1 || shelves[0].Name != "Comedy" || shelves[0].Count != 2 {
		t.Errorf("shelves %+v", shelves)
	}

	// A week later, what is still missing goes.
	if ch, err = s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories[1:2], again, later.Add(LibraryGrace+time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ch.Purged != 2 || len(titleIDs(t, s)) != 2 {
		t.Errorf("changes %+v, titles %v", ch, titleIDs(t, s))
	}
}

func TestLibraryFailedRead(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindSeries, nil, []LibraryTitle{{Key: "1", Name: "Coastline"}}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLibraryFailed(ctx, ids["main"], KindSeries, at.Add(time.Hour), "the server answered 503"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLibraryFailed(ctx, ids["main"], KindMovie, at.Add(time.Hour), "refused"); err != nil {
		t.Fatal(err)
	}
	reads, err := s.LibraryReads(ctx)
	if err != nil || len(reads) != 2 {
		t.Fatalf("reads %+v, %v", reads, err)
	}
	if r := reads[0]; r.Kind != KindMovie || !r.ReadAt.IsZero() || r.Error != "refused" {
		t.Errorf("films %+v", r)
	}
	if r := reads[1]; r.Kind != KindSeries || !r.ReadAt.Equal(at) || !r.TriedAt.Equal(at.Add(time.Hour)) || r.Titles != 1 ||
		r.Error != "the server answered 503" {
		t.Errorf("series %+v", r)
	}
	if err := s.SetLibraryFailed(ctx, ids["main"], "radio", at, "no"); err == nil {
		t.Error("a kind that does not exist")
	}
}

func TestShelvesAcrossSources(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("one"), xtreamSource("two"), testSource("three", 1, NumbersPlaylist))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	for _, name := range []string{"one", "two", "three"} {
		if _, err := s.ApplyLibrary(ctx, ids[name], KindMovie, []LibraryCategory{{Key: "9", Name: "Drama"}},
			[]LibraryTitle{{Key: "1", Category: "9", Name: "Film from " + name}}, at); err != nil {
			t.Fatal(err)
		}
	}
	shelves, err := s.Shelves(ctx, KindMovie)
	if err != nil || len(shelves) != 1 || shelves[0].Count != 2 {
		t.Fatalf("shelves %+v, %v", shelves, err)
	}
	rows, err := s.ShelfTitles(ctx, KindMovie, shelves[0].ID, ByName)
	if err != nil || !reflect.DeepEqual(names(rows), []string{"Film from one", "Film from two"}) {
		t.Errorf("titles %q, %v (an M3U source has no library)", names(rows), err)
	}
	if err := s.SetSourceEnabled(ctx, ids["one"], false); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.ShelfTitles(ctx, KindMovie, shelves[0].ID, ByName)
	if !reflect.DeepEqual(names(rows), []string{"Film from two"}) {
		t.Errorf("with one disabled: %q", names(rows))
	}
	if _, err := s.PlayableMovie(ctx, titleIDs(t, s)["Film from one"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("a disabled source's film plays: %v", err)
	}
	if err := s.DeleteSource(ctx, ids["two"]); err != nil {
		t.Fatal(err)
	}
	if shelves, _ := s.Shelves(ctx, KindMovie); len(shelves) != 0 {
		t.Errorf("shelves %+v", shelves)
	}
}

func TestSearchTitles(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindSeries, nil, []LibraryTitle{{Key: "1", Name: "Field Notes"},
		{Key: "2", Name: "Harbour Lights"}}, at); err != nil {
		t.Fatal(err)
	}
	for q, want := range map[string][]string{
		"HARBOUR": {"quiet harbour", "Harbour Lights"},
		"field":   {"The Long Field", "Field Notes"},
		"100%":    {"Around 100% of_it"},
		"f_i":     {"Around 100% of_it"},
		"%":       {"Around 100% of_it"},
		"  ":      {},
		"nothing": {},
	} {
		got, err := s.SearchTitles(ctx, q, 10)
		if err != nil || !reflect.DeepEqual(names(got), want) {
			t.Errorf("search %q: %q, %v; want %q", q, names(got), err, want)
		}
	}
	got, _ := s.SearchTitles(ctx, "o", 1)
	if len(got) != 2 || got[0].Kind != KindMovie || got[1].Kind != KindSeries {
		t.Errorf("one of each kind: %+v", got)
	}
}

func TestTitlePage(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"), xtreamSource("two"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyLibrary(ctx, ids["two"], KindMovie, []LibraryCategory{{Key: "1", Name: "Drama"}},
		[]LibraryTitle{{Key: "1", Category: "1", Name: "Field of Two", AddedAt: time.Date(2026, 12, 5, 0, 0, 0, 0, time.UTC),
			Summary: TitleSummary{Plot: "Two fields."}}}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMovieDetails(ctx, titleIDs(t, s)["The Long Field"], TitleDetails{Duration: 6000, AgeRating: "12"}, "", at); err != nil {
		t.Fatal(err)
	}
	listed := func(ls []TitleListing) []string {
		out := []string{}
		for _, l := range ls {
			out = append(out, l.Name)
		}
		return out
	}
	page := func(shelf int64, text, order string, offset, limit int) ([]TitleListing, int) {
		t.Helper()
		got, total, err := s.TitlePage(ctx, KindMovie, shelf, text, order, offset, limit, false)
		if err != nil {
			t.Fatal(err)
		}
		return got, total
	}

	// Every film, the latest added first, and those without a time last.
	got, total := page(0, "", ByAdded, 0, 10)
	if total != 5 || !reflect.DeepEqual(listed(got), []string{"Field of Two", "quiet harbour", "Around 100% of_it",
		"The Long Field", "Lost Reel"}) {
		t.Errorf("all: %d %q", total, listed(got))
	}
	if got[0].Shelf != "Drama" || got[0].Summary.Plot != "Two fields." || got[0].Details != nil || got[4].Shelf != "" {
		t.Errorf("listings %+v", got)
	}
	if d := got[3].Details; d == nil || d.Duration != 6000 || d.AgeRating != "12" || got[3].Year != 2024 || got[3].Rating != 74 {
		t.Errorf("details %+v of %+v", d, got[3])
	}
	// A page of them, by name.
	if got, total = page(0, "", ByName, 1, 2); total != 5 || !reflect.DeepEqual(listed(got), []string{"Field of Two", "Lost Reel"}) {
		t.Errorf("by name, from 1: %d %q", total, listed(got))
	}
	if got, total = page(0, "", ByName, 5, 2); total != 5 || len(got) != 0 {
		t.Errorf("past the end: %d %q", total, listed(got))
	}
	// A shelf holds the categories of its name from every source.
	shelves, err := s.Shelves(ctx, KindMovie)
	if err != nil || len(shelves) != 2 || shelves[0].Name != "Drama" {
		t.Fatalf("shelves %+v, %v", shelves, err)
	}
	if got, total = page(shelves[0].ID, "", ByAdded, 0, 10); total != 3 ||
		!reflect.DeepEqual(listed(got), []string{"Field of Two", "Around 100% of_it", "The Long Field"}) {
		t.Errorf("drama: %d %q", total, listed(got))
	}
	// Names that hold some text, on a shelf or on all of them.
	if got, total = page(shelves[0].ID, "FIELD", ByName, 0, 10); total != 2 ||
		!reflect.DeepEqual(listed(got), []string{"Field of Two", "The Long Field"}) {
		t.Errorf("drama, field: %d %q", total, listed(got))
	}
	if got, total = page(0, "_", ByName, 0, 10); total != 1 || listed(got)[0] != "Around 100% of_it" {
		t.Errorf("an underscore: %d %q", total, listed(got))
	}
	if _, _, err := s.TitlePage(ctx, KindMovie, 999999, "", ByAdded, 0, 10, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such shelf: %v", err)
	}
	if _, _, err := s.TitlePage(ctx, KindSeries, shelves[0].ID, "", ByAdded, 0, 10, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("a film shelf as series: %v", err)
	}
	if got, total, err := s.TitlePage(ctx, KindSeries, 0, "", ByAdded, 0, 10, false); err != nil || total != 0 || len(got) != 0 {
		t.Errorf("no series: %d %+v %v", total, got, err)
	}
	// A disabled source's titles are not listed.
	if err := s.SetSourceEnabled(ctx, ids["two"], false); err != nil {
		t.Fatal(err)
	}
	if got, total = page(0, "", ByAdded, 0, 10); total != 4 || listed(got)[0] != "quiet harbour" {
		t.Errorf("with two disabled: %d %q", total, listed(got))
	}
}

func TestSeriesDetailsAndEpisodes(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindSeries, nil, []LibraryTitle{{Key: "7001", Name: "Coastline",
		Summary: TitleSummary{Plot: "Waves."}}}, at); err != nil {
		t.Fatal(err)
	}
	id := titleIDs(t, s)["Coastline"]
	if err := s.SetSeriesDetails(ctx, 99999, TitleDetails{}, nil, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown series: %v", err)
	}
	details := TitleDetails{TitleSummary: TitleSummary{Plot: "Waves, closer."},
		Seasons: []SeasonDetails{{Number: 1, Name: "Season 1"}}}
	episodes := []LibraryEpisode{
		{Key: "9002", Season: 1, Number: 2, Name: "Sand", Container: "mkv", Duration: 2700},
		{Key: "9001", Season: 1, Number: 1, Name: "Rocks", Container: "mkv"},
		{Key: "9003", Season: 2, Number: 1, Name: "Tides", Container: "mp4", Picture: "http://img.example/9003.jpg"},
	}
	if err := s.SetSeriesDetails(ctx, id, details, episodes, at); err != nil {
		t.Fatal(err)
	}
	title, err := s.Title(ctx, id)
	if err != nil || title.Details == nil || title.Details.Plot != "Waves, closer." || title.Summary.Plot != "Waves." ||
		len(title.Details.Seasons) != 1 {
		t.Fatalf("title %+v, %v", title, err)
	}
	eps, err := s.Episodes(ctx, id)
	if err != nil || len(eps) != 3 || eps[0].Name != "Rocks" || eps[1].Name != "Sand" || eps[1].Duration != 2700 ||
		eps[2].Season != 2 || eps[2].Picture != "http://img.example/9003.jpg" {
		t.Fatalf("episodes %+v, %v", eps, err)
	}
	sand := eps[1].ID

	p, err := s.PlayableEpisode(ctx, sand)
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindEpisode || p.TitleID != id || p.Name != "Coastline S1 E2" || p.Container != "mkv" ||
		p.StreamURL != "http://provider.example:8080/series/viewer/pw%2Fmain/9002.mkv" {
		t.Errorf("playable %+v", p)
	}
	account := int64(1)
	if err := s.SetProgress(ctx, account, Progress{TitleID: id, EpisodeID: sand, Position: 600, Duration: 2700,
		WatchedAt: at}); err != nil {
		t.Fatal(err)
	}

	// A new read drops Sand: it is missing, then gone with its progress.
	if err := s.SetSeriesDetails(ctx, id, details, []LibraryEpisode{episodes[1], episodes[2]}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if eps, _ = s.Episodes(ctx, id); len(eps) != 2 {
		t.Errorf("episodes %+v", eps)
	}
	if _, err := s.PlayableEpisode(ctx, sand); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing episode plays: %v", err)
	}
	if err := s.SetSeriesDetails(ctx, id, details, []LibraryEpisode{episodes[1], episodes[2]},
		at.Add(LibraryGrace+2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.TitleProgress(ctx, account, id); len(got) != 0 {
		t.Errorf("progress in a purged episode %+v", got)
	}
	if err := s.SetSeriesDetails(ctx, id, details, episodes, at.Add(LibraryGrace+3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if eps, _ = s.Episodes(ctx, id); len(eps) != 3 || eps[1].ID == sand {
		t.Errorf("Sand came back as %+v", eps)
	}
}

func TestPlayableMovie(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	byName := titleIDs(t, s)
	p, err := s.PlayableMovie(ctx, byName["The Long Field"])
	if err != nil {
		t.Fatal(err)
	}
	if p.Kind != KindMovie || p.ID != byName["The Long Field"] || p.SourceID != ids["main"] || p.Container != "mkv" ||
		p.StreamURL != "http://provider.example:8080/movie/viewer/pw%2Fmain/5001.mkv" {
		t.Errorf("playable %+v", p)
	}
	if p, err = s.PlayableMovie(ctx, byName["quiet harbour"]); err != nil || p.Container != "mp4" ||
		!strings.HasSuffix(p.StreamURL, "/5002.mp4") {
		t.Errorf("without a container %+v, %v", p, err)
	}
	if _, err := s.PlayableMovie(ctx, 4242); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown film: %v", err)
	}
	if _, err := s.PlayableEpisode(ctx, byName["The Long Field"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("a film as an episode: %v", err)
	}
}

func TestContinueWatching(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindSeries, nil, []LibraryTitle{{Key: "1", Name: "Coastline"},
		{Key: "2", Name: "Night Shift"}, {Key: "3", Name: "Done"}}, at); err != nil {
		t.Fatal(err)
	}
	byName := titleIDs(t, s)
	for _, series := range []string{"Coastline", "Night Shift", "Done"} {
		eps := []LibraryEpisode{{Key: series + "1", Season: 1, Number: 1}, {Key: series + "2", Season: 1, Number: 2}}
		if err := s.SetSeriesDetails(ctx, byName[series], TitleDetails{}, eps, at); err != nil {
			t.Fatal(err)
		}
	}
	episode := func(series string, number int) int64 {
		eps, err := s.Episodes(ctx, byName[series])
		if err != nil {
			t.Fatal(err)
		}
		return eps[number-1].ID
	}
	const account = 1
	for i, p := range []Progress{
		{TitleID: byName["The Long Field"], Position: 1200, Duration: 6000},
		{TitleID: byName["quiet harbour"], Duration: 5000, Finished: true},
		{TitleID: byName["Around 100% of_it"], Position: 300, Duration: 6000},
		// Coastline: episode 1 finished, so episode 2 is next.
		{TitleID: byName["Coastline"], EpisodeID: episode("Coastline", 1), Duration: 2000, Finished: true},
		// Night Shift: part way through episode 2, after finishing 1.
		{TitleID: byName["Night Shift"], EpisodeID: episode("Night Shift", 1), Duration: 2000, Finished: true},
		{TitleID: byName["Night Shift"], EpisodeID: episode("Night Shift", 2), Position: 900, Duration: 2000},
		// Done: the last episode finished, so nothing to continue.
		{TitleID: byName["Done"], EpisodeID: episode("Done", 2), Duration: 2000, Finished: true},
	} {
		p.WatchedAt = at.Add(time.Duration(i) * time.Minute)
		if err := s.SetProgress(ctx, account, p); err != nil {
			t.Fatal(err)
		}
	}
	movies, err := s.ContinueWatching(ctx, account, KindMovie, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(movies) != 2 || movies[0].Title.Name != "Around 100% of_it" || movies[1].Progress.Position != 1200 ||
		movies[0].Episode != nil {
		t.Errorf("films %+v", movies)
	}
	series, err := s.ContinueWatching(ctx, account, KindSeries, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 2 || series[0].Title.Name != "Night Shift" || series[0].Episode == nil ||
		series[0].Episode.Number != 2 || series[0].Progress.Position != 900 || series[1].Title.Name != "Coastline" ||
		!series[1].Progress.Finished || series[1].Episode.Number != 1 || series[0].Next != nil ||
		series[1].Next == nil || series[1].Next.Number != 2 || series[1].Next.ID != episode("Coastline", 2) {
		t.Errorf("series %+v", series)
	}
	got, err := s.TitleProgress(ctx, account, byName["Night Shift"])
	if err != nil || len(got) != 2 || got[0].Position != 900 || !got[1].Finished || got[1].Position != 0 {
		t.Errorf("progress %+v, %v", got, err)
	}
	if got, _ := s.ContinueWatching(ctx, 2, KindMovie, 10); len(got) != 0 {
		t.Errorf("another account's %+v", got)
	}
}

func TestJellyfinSource(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	const key = "0123456789abcdef"
	id, err := s.AddSource(ctx, source.Settings{Name: "den", Type: source.TypeJellyfin, URL: "http://jellyfin.example:8096",
		Username: "Anna", Password: key})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, id, KindMovie, []LibraryCategory{{Key: "lib1", Name: "Films"}}, []LibraryTitle{
		{Key: "4f2c00aa", Category: "lib1", Name: "Calm Water", Container: "mkv"},
		{Key: "4f2c00bb", Category: "lib1", Name: "Bright Harbour"},
	}, at); err != nil {
		t.Fatal(err)
	}
	byName := titleIDs(t, s)
	p, err := s.PlayableMovie(ctx, byName["Calm Water"])
	if err != nil || p.Container != "mkv" ||
		p.StreamURL != "http://jellyfin.example:8096/Videos/4f2c00aa/stream.mkv?ApiKey="+key+"&static=true" ||
		p.TranscodeURL != "http://jellyfin.example:8096/Videos/4f2c00aa/stream.ts?ApiKey="+key {
		t.Errorf("playable %+v, %v", p, err)
	}
	// Without a container, the file plays as it is; the box still gets an
	// extension.
	if p, err = s.PlayableMovie(ctx, byName["Bright Harbour"]); err != nil || p.Container != "mp4" ||
		p.StreamURL != "http://jellyfin.example:8096/Videos/4f2c00bb/stream?ApiKey="+key+"&static=true" {
		t.Errorf("without a container %+v, %v", p, err)
	}

	readAt := func() time.Time {
		t.Helper()
		reads, err := s.LibraryReads(ctx)
		if err != nil || len(reads) != 1 {
			t.Fatalf("reads %+v, %v", reads, err)
		}
		return reads[0].ReadAt
	}
	// The same user, with the address and key left out: all stays.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "den", Type: source.TypeJellyfin, Username: "Anna", MaxConnections: 2}); err != nil {
		t.Fatal(err)
	}
	if settings, err := s.SourceSettings(ctx, id); err != nil || settings.Password != key || settings.Username != "Anna" ||
		settings.MaxConnections != 2 {
		t.Errorf("kept %+v, %v", settings, err)
	}
	if readAt().IsZero() {
		t.Error("an unchanged login made the films due")
	}
	// No user is every library: the films are read again.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "den", Type: source.TypeJellyfin}); err != nil {
		t.Fatal(err)
	}
	if settings, err := s.SourceSettings(ctx, id); err != nil || settings.Password != key || settings.Username != "" ||
		settings.URL != "http://jellyfin.example:8096" {
		t.Errorf("without a user %+v, %v", settings, err)
	}
	if !readAt().IsZero() {
		t.Error("a new user left the films as read")
	}
	if _, err := s.ApplyLibrary(ctx, id, KindMovie, nil, nil, at); err != nil {
		t.Fatal(err)
	}
	// A new key, with the address kept.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "den", Type: source.TypeJellyfin, Password: "fedcba"}); err != nil {
		t.Fatal(err)
	}
	if settings, _ := s.SourceSettings(ctx, id); settings.Password != "fedcba" || settings.URL != "http://jellyfin.example:8096" {
		t.Errorf("a new key %+v", settings)
	}
	if !readAt().IsZero() {
		t.Error("a new key left the films as read")
	}

	// A playlist that becomes a Jellyfin server needs its address and key.
	ids := addSources(t, s, testSource("list", 1, ""))
	var bad *source.Error
	if err := s.UpdateSource(ctx, ids["list"], source.Settings{Name: "list", Type: source.TypeJellyfin, Password: key}); !errors.As(err, &bad) || bad.Field != "url" {
		t.Errorf("without its address: %v", err)
	}
	if err := s.UpdateSource(ctx, ids["list"], source.Settings{Name: "list", Type: source.TypeJellyfin,
		URL: "https://media.example", Password: key}); err != nil {
		t.Error(err)
	}
}

func TestPlexSource(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	id, err := s.AddSource(ctx, source.Settings{Name: "lounge", Type: source.TypePlex, URL: "http://plex.example:32400",
		Password: "t&k"})
	if err != nil {
		t.Fatal(err)
	}
	if src, err := s.Source(ctx, id); err != nil || src.MaxConnections != 0 && src.MaxConnections != 1 {
		t.Errorf("source %+v, %v", src, err)
	}
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, id, KindMovie, []LibraryCategory{{Key: "1", Name: "Films"}}, []LibraryTitle{
		{Key: "101", Category: "1", Name: "Calm Water", Container: "mkv", File: "/library/parts/12/1700000000/file.mkv"},
		{Key: "102", Category: "1", Name: "No File"},
	}, at); err != nil {
		t.Fatal(err)
	}
	byName := titleIDs(t, s)
	p, err := s.PlayableMovie(ctx, byName["Calm Water"])
	if err != nil || p.Container != "mkv" || p.Key != "101" || p.TranscodeURL != "" ||
		p.StreamURL != "http://plex.example:32400/library/parts/12/1700000000/file.mkv?X-Plex-Token=t%26k" {
		t.Errorf("playable %+v, %v", p, err)
	}
	if _, err := s.PlayableMovie(ctx, byName["No File"]); err == nil {
		t.Error("a film without a file plays")
	}
	// A list that gives no file keeps the one before; a new one replaces it.
	if _, err := s.ApplyLibrary(ctx, id, KindMovie, nil, []LibraryTitle{{Key: "101", Name: "Calm Water"}}, at); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PlayableMovie(ctx, byName["Calm Water"]); !strings.Contains(p.StreamURL, "/parts/12/") || p.Container != "mkv" {
		t.Errorf("kept %+v", p)
	}
	if _, err := s.ApplyLibrary(ctx, id, KindMovie, nil, []LibraryTitle{{Key: "101", Name: "Calm Water",
		File: "/library/parts/13/1700000009/file.mp4", Container: "mp4"}}, at); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PlayableMovie(ctx, byName["Calm Water"]); !strings.Contains(p.StreamURL, "/parts/13/1700000009/file.mp4?") {
		t.Errorf("replaced %+v", p)
	}

	// An episode plays from its own file.
	if _, err := s.ApplyLibrary(ctx, id, KindSeries, nil, []LibraryTitle{{Key: "300", Name: "Coastline"}}, at); err != nil {
		t.Fatal(err)
	}
	series := titleIDs(t, s)["Coastline"]
	if err := s.SetSeriesDetails(ctx, series, TitleDetails{Seasons: []SeasonDetails{{Number: 1}}}, []LibraryEpisode{
		{Key: "301", Season: 1, Number: 1, Name: "Arrival", Container: "mkv", File: "/library/parts/3011/1700000000/file.mkv"},
	}, at); err != nil {
		t.Fatal(err)
	}
	opened, err := s.Title(ctx, series)
	if err != nil {
		t.Fatal(err)
	}
	eps, err := s.Episodes(ctx, opened.ID)
	if err != nil || len(eps) != 1 {
		t.Fatalf("episodes %+v, %v", eps, err)
	}
	if p, err := s.PlayableEpisode(ctx, eps[0].ID); err != nil || p.Key != "301" ||
		p.StreamURL != "http://plex.example:32400/library/parts/3011/1700000000/file.mkv?X-Plex-Token=t%26k" {
		t.Errorf("episode %+v, %v", p, err)
	}

	// The token stays when it is left out; the address alone changes.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "lounge", Type: source.TypePlex,
		URL: "https://plex.example:32400"}); err != nil {
		t.Fatal(err)
	}
	if settings, err := s.SourceSettings(ctx, id); err != nil || settings.Password != "t&k" ||
		settings.URL != "https://plex.example:32400" || settings.Username != "" {
		t.Errorf("kept %+v, %v", settings, err)
	}
	// A Jellyfin server that becomes a Plex server needs its address and token.
	den, err := s.AddSource(ctx, source.Settings{Name: "den", Type: source.TypeJellyfin, URL: "http://jellyfin.example",
		Password: "k"})
	if err != nil {
		t.Fatal(err)
	}
	var bad *source.Error
	if err := s.UpdateSource(ctx, den, source.Settings{Name: "den", Type: source.TypePlex}); !errors.As(err, &bad) ||
		bad.Field != "url" || !strings.Contains(bad.Text, "token") {
		t.Errorf("without its address and token: %v", err)
	}
}

// TestRebuiltSourcesKeepSources upgrades a database of schema 17 through
// schemas 18 and 19, which rebuild sources, and checks that its sources
// keep every setting, and what refers to them.
func TestRebuiltSourcesKeepSources(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	old, err := open(ctx, path, quiet(), migrations[:17])
	if err != nil {
		t.Fatal(err)
	}
	old.UseKey(secrets.NewRandomBox())
	ids := addSources(t, old, xtreamSource("main"))
	if _, err := old.db.ExecContext(ctx, `
		UPDATE sources SET enabled = 0, max_connections = 3, user_agent = 'TestPlayer/1.0', refresh_seconds = 3600,
			numbers = 'provider', new_categories_hidden = 1, refreshed_at = '2026-12-04T10:00:00Z', refresh_error = 'slow',
			account_status = 'Active', account_expires = '2027-01-01T00:00:00Z', account_connections = 2,
			account_time_zone = 'Europe/London'
		WHERE id = ?`, ids["main"]); err != nil {
		t.Fatal(err)
	}
	// Titles as schema 17 kept them, which today's code would not write.
	for i, f := range films[:4] {
		if _, err := old.db.ExecContext(ctx, `INSERT INTO titles (source_id, kind, source_key, name, position, generation)
			VALUES (?, ?, ?, ?, ?, 1)`, ids["main"], KindMovie, f.Key, f.Name, i+1); err != nil {
			t.Fatal(err)
		}
	}
	const columns = `name, type, host, enabled, max_connections, user_agent, refresh_seconds, numbers, new_categories_hidden,
		refreshed_at, refresh_error, created_at, account_status, account_expires, account_connections, account_time_zone`
	row := func(s *Store) []any {
		out := make([]any, 16)
		ptrs := make([]any, len(out))
		for i := range out {
			ptrs[i] = &out[i]
		}
		if err := s.db.QueryRowContext(ctx, "SELECT "+columns+" FROM sources WHERE id = ?", ids["main"]).Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := row(old)
	old.Close()

	s := openTest(t, path)
	if v, _ := s.SchemaVersion(ctx); v != len(migrations) || v < 19 {
		t.Fatalf("schema %d", v)
	}
	if after := row(s); !reflect.DeepEqual(after, before) {
		t.Errorf("the source changed:\n%v\n%v", after, before)
	}
	var sealed, titles int
	if err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM source_secrets WHERE source_id = ?),
		(SELECT count(*) FROM titles WHERE source_id = ?)`, ids["main"], ids["main"]).Scan(&sealed, &titles); err != nil ||
		sealed != 1 || titles != 4 {
		t.Errorf("%d secrets, %d titles, %v", sealed, titles, err)
	}
	if _, err := s.AddSource(ctx, source.Settings{Name: "den", Type: source.TypeJellyfin, URL: "http://jellyfin.example",
		Password: "k"}); err != nil {
		t.Error(err)
	}
	if _, err := s.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeFolder, URL: "/srv/media/Films"}); err != nil {
		t.Error(err)
	}
	if _, err := s.AddSource(ctx, source.Settings{Name: "lounge", Type: source.TypePlex, URL: "http://plex.example:32400",
		Password: "t"}); err != nil {
		t.Error(err)
	}
}

func TestFolderSource(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	id, err := s.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeFolder, URL: "/srv/media/Films"})
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.Source(ctx, id)
	if err != nil || src.Host != "Films" || src.MaxConnections != 0 {
		t.Errorf("source %+v, %v", src, err)
	}
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, id, KindMovie, []LibraryCategory{{Key: "folder", Name: "Films"}}, []LibraryTitle{
		{Key: "Calm Water (2021)/Calm Water (2021).mkv", Category: "folder", Name: "Calm Water", Container: "mkv",
			Poster: "local:1.2:Calm Water (2021)/poster.jpg"},
	}, at); err != nil {
		t.Fatal(err)
	}
	byName := titleIDs(t, s)
	p, err := s.PlayableMovie(ctx, byName["Calm Water"])
	if err != nil || p.StreamURL != "" || p.Folder != "/srv/media/Films" || p.File != "Calm Water (2021)/Calm Water (2021).mkv" ||
		p.Container != "mkv" {
		t.Errorf("playable %+v, %v", p, err)
	}
	posters, _, art, err := s.TitleArt(ctx, byName["Calm Water"])
	if err != nil || len(posters) != 1 || art != (ArtSource{ID: id, Type: source.TypeFolder}) {
		t.Errorf("art %v, %+v, %v", posters, art, err)
	}
	// A change that leaves the path out keeps it; a new path makes the
	// films due.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "films", Type: source.TypeFolder, MaxConnections: 2}); err != nil {
		t.Fatal(err)
	}
	if settings, _ := s.SourceSettings(ctx, id); settings.URL != "/srv/media/Films" || settings.MaxConnections != 2 {
		t.Errorf("kept %+v", settings)
	}
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "films", Type: source.TypeFolder, URL: "/srv/media/Kids"}); err != nil {
		t.Fatal(err)
	}
	if src, _ := s.Source(ctx, id); src.Host != "Kids" {
		t.Errorf("host %q", src.Host)
	}
	reads, _ := s.LibraryReads(ctx)
	if len(reads) != 1 || !reads[0].ReadAt.IsZero() {
		t.Errorf("reads %+v", reads)
	}
	var bad *source.Error
	other := addSources(t, s, testSource("list", 1, ""))
	if err := s.UpdateSource(ctx, other["list"], source.Settings{Name: "list", Type: source.TypeFolder}); !errors.As(err, &bad) ||
		bad.Text != "Enter the folder's path." {
		t.Errorf("a playlist without a path: %v", err)
	}
}
