// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/subtitles"
)

// newFolderLibrary returns a library over a store with one folder source,
// a fictional folder of films and series, and the folder's path.
func newFolderLibrary(t *testing.T) (*Library, *store.Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Home Films")
	for name, content := range map[string]string{
		"The Long Field (2024)/The.Long.Field.2024.1080p.mkv": "video",
		"The Long Field (2024)/poster.jpg":                    "poster",
		"The Long Field (2024)/movie.nfo": `<movie><title>The Long Field</title><plot>A field, again.</plot>
			<genre>Drama</genre><country>Wales</country><runtime>96</runtime><mpaa>PG</mpaa><premiered>2024-05-01</premiered>
			<rating>7.4</rating><actor><name>A. Actor</name></actor></movie>`,
		"The Long Field (2024)/The.Long.Field.2024.1080p.en.forced.srt": "subtitles",
		"Calm Water (2021).mp4":                                "video",
		"Coastline (2019)/tvshow.nfo":                          "<tvshow><title>Coastline</title><plot>Waves.</plot></tvshow>",
		"Coastline (2019)/Season 1/Coastline S01E01 Rocks.mkv": "video",
		"Coastline (2019)/Season 1/Coastline S01E02.mkv":       "video",
		"Coastline (2019)/Season 2/01 - Tides.mkv":             "video",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.AddSource(context.Background(), source.Settings{Name: "films", Type: source.TypeFolder, URL: dir}); err != nil {
		t.Fatal(err)
	}
	l := New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
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
	return l, st, dir
}

func TestFolderFilms(t *testing.T) {
	l, st, dir := newFolderLibrary(t)
	ctx := context.Background()
	state, err := l.Want(ctx, store.KindMovie, 5*time.Second)
	if err != nil || !state.Ready() || state.Sources != 1 {
		t.Fatalf("state %+v, %v", state, err)
	}
	want := map[string][]string{"Home Films": {"Calm Water", "The Long Field"}}
	if got := shelfTitles(t, st, store.KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %v", got)
	}
	recent, err := st.RecentTitles(ctx, store.KindMovie, 5, store.EverySource)
	if err != nil || len(recent) != 2 {
		t.Fatalf("recent %+v, %v", recent, err)
	}
	var long store.TitleRow
	for _, r := range recent {
		if r.Name == "The Long Field" {
			long = r
		}
	}
	if long.Year != 2024 || long.Rating != 74 || !strings.HasSuffix(long.Poster, ":The Long Field (2024)/poster.jpg") {
		t.Errorf("the long field %+v", long)
	}
	o, err := l.Open(ctx, long.ID)
	if err != nil || o.ReadError != "" || o.Details == nil {
		t.Fatalf("opened %+v, %v", o, err)
	}
	if d := o.Details; d.Plot != "A field, again." || d.Country != "Wales" || d.Duration != 96*60 || d.AgeRating != "PG" ||
		d.Released != "2024-05-01" || d.Cast != "A. Actor" || d.Genre != "Drama" {
		t.Errorf("details %+v", d)
	}
	p, err := st.PlayableMovie(ctx, long.ID)
	if err != nil || p.Folder != dir || p.File != "The Long Field (2024)/The.Long.Field.2024.1080p.mkv" || p.Container != "mkv" {
		t.Errorf("playable %+v, %v", p, err)
	}
	subs := l.Subtitles(ctx, p)
	if want := []subtitles.Track{{Language: "eng", Forced: true, Format: "srt",
		File: "The Long Field (2024)/The.Long.Field.2024.1080p.en.forced.srt"}}; !reflect.DeepEqual(subs, want) {
		t.Errorf("subtitles %+v", subs)
	}

	// A film that leaves the folder is gone at the next read; one opened
	// meanwhile says why it has no new details.
	if err := os.Remove(filepath.Join(dir, "Calm Water (2021).mp4")); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(OwnTTL + time.Minute)
	l.now = func() time.Time { return later }
	var calm store.TitleRow
	for _, r := range recent {
		if r.Name == "Calm Water" {
			calm = r
		}
	}
	if o, err := l.Open(ctx, calm.ID); err != nil || o.ReadError != errNoDetails.Error() {
		t.Errorf("a film gone: %+v, %v", o, err)
	}
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		return reflect.DeepEqual(shelfTitles(t, st, store.KindMovie), map[string][]string{"Home Films": {"The Long Field"}})
	})
}

func TestFolderSeries(t *testing.T) {
	l, st, dir := newFolderLibrary(t)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindSeries, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if got := shelfTitles(t, st, store.KindSeries); !reflect.DeepEqual(got, map[string][]string{"Home Films": {"Coastline"}}) {
		t.Errorf("shelves %v", got)
	}
	recent, err := st.RecentTitles(ctx, store.KindSeries, 1, store.EverySource)
	if err != nil || len(recent) != 1 {
		t.Fatalf("recent %+v, %v", recent, err)
	}
	o, err := l.Open(ctx, recent[0].ID)
	if err != nil || o.ReadError != "" || o.Details == nil || o.Details.Plot != "Waves." {
		t.Fatalf("opened %+v, %v", o, err)
	}
	if !reflect.DeepEqual(o.Details.Seasons, []store.SeasonDetails{{Number: 1}, {Number: 2}}) || len(o.Episodes) != 3 {
		t.Fatalf("seasons %+v, episodes %+v", o.Details.Seasons, o.Episodes)
	}
	if e := o.Episodes[0]; e.Name != "Rocks" || e.Season != 1 || e.Number != 1 {
		t.Errorf("first episode %+v", e)
	}
	if e := o.Episodes[2]; e.Name != "Tides" || e.Season != 2 || e.Number != 1 {
		t.Errorf("last episode %+v", e)
	}
	p, err := st.PlayableEpisode(ctx, o.Episodes[2].ID)
	if err != nil || p.Folder != dir || p.File != "Coastline (2019)/Season 2/01 - Tides.mkv" {
		t.Errorf("playable %+v, %v", p, err)
	}
}

func TestFolderGone(t *testing.T) {
	l, _, dir := newFolderLibrary(t)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	state, err := l.Want(context.Background(), store.KindMovie, 5*time.Second)
	if err != nil || state.Ready() || state.Error != "the folder does not exist" {
		t.Errorf("state %+v, %v", state, err)
	}
}
