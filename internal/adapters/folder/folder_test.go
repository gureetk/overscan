// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// newTree makes a fictional folder of films and series, and returns its
// path. A folder beside it holds what the tree's links point out to.
func newTree(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	root := filepath.Join(top, "Films and Shows")
	outside := filepath.Join(top, "outside")
	files := map[string]string{
		"The Long Field (2024)/The.Long.Field.2024.1080p.mkv":          "video",
		"The Long Field (2024)/poster.jpg":                             "poster",
		"The Long Field (2024)/fanart.png":                             "fanart",
		"The Long Field (2024)/movie.nfo":                              "<movie><title>The Long Field</title><plot>A field, again.</plot><genre>Drama</genre></movie>",
		"The Long Field (2024)/The.Long.Field-trailer.mkv":             "video",
		"The Long Field (2024)/Extras/Making of.mkv":                   "video",
		"Comedy/Bright.Harbour.2019.720p.mp4":                          "video",
		"Comedy/Bright.Harbour.2019.720p-poster.jpg":                   "poster",
		"Comedy/Calm Water (2021).avi":                                 "video",
		"loose film.mkv":                                               "video",
		"Coastline (2019)/tvshow.nfo":                                  "<tvshow><title>Coastline</title><plot>Waves.</plot><year>2019</year></tvshow>",
		"Coastline (2019)/Poster.JPG":                                  "poster",
		"Coastline (2019)/Season 1/Coastline S01E01 - Rocks.mkv":       "video",
		"Coastline (2019)/Season 1/Coastline S01E01 - Rocks.nfo":       "<episodedetails><title>Rocks!</title><plot>Rocks, mostly.</plot><runtime>40</runtime></episodedetails>",
		"Coastline (2019)/Season 1/Coastline S01E01 - Rocks-thumb.jpg": "thumb",
		"Coastline (2019)/Season 1/02 - Sand.mp4":                      "video",
		"Coastline (2019)/Specials/Coastline S00E01.mkv":               "video",
		"Downloads/Night.Shift.S01E02.1080p.mkv":                       "video",
		"Downloads/Night.Shift.S01E01.1080p.mkv":                       "video",
		"Downloads/notes.txt":                                          "text",
		".hidden/Secret.mkv":                                           "video",
		"@eaDir/thumbs.mkv":                                            "video",
		"Deep/1/2/3/4/5/6/7/8/9/10/11/12/Too Deep.mkv":                 "video",
		"../outside/elsewhere.mkv":                                     "video",
		"../outside/elsewhere.jpg":                                     "poster",
		"../outside/Hidden Show/Hidden Show S01E01.mkv":                "video",
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for link, target := range map[string]string{
		"out.mkv":        filepath.Join(outside, "elsewhere.mkv"),
		"Comedy/out.jpg": filepath.Join(outside, "elsewhere.jpg"),
		"Linked Show":    filepath.Join(outside, "Hidden Show"),
		"alias.mkv":      "loose film.mkv",
	} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestFilms(t *testing.T) {
	root := newTree(t)
	f, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.Name() != "Films and Shows" || f.Check() != nil {
		t.Errorf("name %q, check %v", f.Name(), f.Check())
	}
	films, skipped, err := f.Films(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, film := range films {
		names = append(names, film.Name)
	}
	if want := []string{"alias", "Bright Harbour", "Calm Water", "loose film", "The Long Field"}; !reflect.DeepEqual(names, want) {
		t.Errorf("films %q", names)
	}
	if skipped != 1 { // the link out
		t.Errorf("skipped %d", skipped)
	}
	long := films[4]
	if long.Key != "The Long Field (2024)/The.Long.Field.2024.1080p.mkv" || long.Year != 2024 || long.Container != "mkv" ||
		long.Info.Plot != "A field, again." || !strings.HasSuffix(long.Poster, ":The Long Field (2024)/poster.jpg") ||
		!strings.HasSuffix(long.Backdrop, ":The Long Field (2024)/fanart.png") || long.Added.IsZero() {
		t.Errorf("the long field %+v", long)
	}
	bright := films[1]
	if bright.Year != 2019 || !strings.HasSuffix(bright.Poster, ":Comedy/Bright.Harbour.2019.720p-poster.jpg") || bright.Backdrop != "" {
		t.Errorf("bright harbour %+v", bright)
	}
	// In a folder with another film, the folder's pictures are not a film's.
	if calm := films[2]; calm.Year != 2021 || calm.Poster != "" {
		t.Errorf("calm water %+v", calm)
	}

	again, err := f.Film(context.Background(), long.Key)
	if err != nil || !reflect.DeepEqual(again, long) {
		t.Errorf("the film again %+v, %v", again, err)
	}
	for _, key := range []string{"missing.mkv", "../outside/elsewhere.mkv", "out.mkv", "/etc/passwd", ".", "Comedy/out.jpg",
		"Coastline (2019)/Season 1/02 - Sand.mp4"} {
		if _, err := f.Film(context.Background(), key); !errors.Is(err, ErrNoTitle) {
			t.Errorf("Film(%q): %v", key, err)
		}
	}
}

func TestShows(t *testing.T) {
	root := newTree(t)
	f, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	shows, _, err := f.Shows(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if films, series, skipped, err := f.Count(context.Background()); err != nil || films != 5 || series != 2 || skipped != 1 {
		t.Errorf("count %d films, %d series, %d skipped, %v", films, series, skipped, err)
	}
	if len(shows) != 2 {
		t.Fatalf("shows %+v", shows)
	}
	coast, night := shows[0], shows[1]
	if coast.Key != "dir:Coastline (2019)" || coast.Name != "Coastline" || coast.Year != 2019 || coast.Info.Plot != "Waves." ||
		!strings.HasSuffix(coast.Poster, ":Coastline (2019)/Poster.JPG") || coast.Episodes != nil || coast.Added.IsZero() {
		t.Errorf("coastline %+v", coast)
	}
	if night.Key != "name:nightshift" || night.Name != "Night Shift" || night.Poster != "" {
		t.Errorf("night shift %+v", night)
	}

	full, err := f.Show(context.Background(), coast.Key)
	if err != nil {
		t.Fatal(err)
	}
	type ep struct {
		season, number  int
		name, container string
	}
	var got []ep
	for _, e := range full.Episodes {
		got = append(got, ep{e.Season, e.Number, e.Name, e.Container})
	}
	want := []ep{{0, 1, "", "mkv"}, {1, 1, "Rocks!", "mkv"}, {1, 2, "Sand", "mp4"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("episodes %+v", got)
	}
	if rocks := full.Episodes[1]; rocks.Plot != "Rocks, mostly." || rocks.Runtime != 40*time.Minute ||
		!strings.HasSuffix(rocks.Picture, ":Coastline (2019)/Season 1/Coastline S01E01 - Rocks-thumb.jpg") {
		t.Errorf("rocks %+v", rocks)
	}
	if full.Name != "Coastline" || full.Poster != coast.Poster {
		t.Errorf("the series again %+v", full)
	}
	ns, err := f.Show(context.Background(), night.Key)
	if err != nil || len(ns.Episodes) != 2 || ns.Episodes[0].Number != 1 {
		t.Errorf("night shift %+v, %v", ns, err)
	}
	for _, key := range []string{"dir:Gone", "dir:../outside/Hidden Show", "dir:Linked Show", "name:nothing", "Coastline"} {
		if _, err := f.Show(context.Background(), key); !errors.Is(err, ErrNoTitle) {
			t.Errorf("Show(%q): %v", key, err)
		}
	}
}

// A folder that is one series' own: its seasons sit at its top.
func TestOneSeriesFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Coastline")
	for _, name := range []string{"Season 1/E01 Rocks.mkv", "Season 2/S02E01.mkv", "S03E01 Late.mkv", "Coastline.S04E01.mkv"} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	show, err := f.Show(context.Background(), "dir:.")
	if err != nil || show.Name != "Coastline" || len(show.Episodes) != 4 || show.Episodes[0].Name != "Rocks" {
		t.Errorf("show %+v, %v", show, err)
	}
	films, _, _ := f.Films(context.Background())
	if len(films) != 0 {
		t.Errorf("films %+v", films)
	}
}

func TestPicturesAndVideos(t *testing.T) {
	root := newTree(t)
	f, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	films, _, err := f.Films(context.Background())
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	long := films[4]
	if !IsPicture(long.Poster) {
		t.Errorf("poster %q", long.Poster)
	}
	data, err := ReadPicture(root, long.Poster)
	if err != nil || string(data) != "poster" {
		t.Errorf("poster %q, %v", data, err)
	}
	for _, address := range []string{"local:1.2:../outside/elsewhere.jpg", "local:1.2:Comedy/out.jpg", "local:1.2:/etc/passwd",
		"local:1.2:The Long Field (2024)/movie.nfo", "http://example.com/a.jpg", "local:Comedy/x.jpg"} {
		if _, err := ReadPicture(root, address); err == nil {
			t.Errorf("ReadPicture(%q) worked", address)
		}
	}
	video, err := OpenVideo(root, long.Key)
	if err != nil {
		t.Fatal(err)
	}
	data, _ = io.ReadAll(video)
	video.Close()
	if string(data) != "video" {
		t.Errorf("video %q", data)
	}
	for _, key := range []string{"out.mkv", "../outside/elsewhere.mkv", "missing.mkv", "The Long Field (2024)/movie.nfo"} {
		if v, err := OpenVideo(root, key); err == nil {
			v.Close()
			t.Errorf("OpenVideo(%q) worked", key)
		}
	}
}

func TestOpenErrors(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "a.mkv")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]error{filepath.Join(dir, "gone"): ErrMissing, file: ErrNotDir} {
		if _, err := Open(path); !errors.Is(err, want) || strings.Contains(err.Error(), dir) {
			t.Errorf("Open(%q): %v", path, err)
		}
	}
	if _, err := Open("relative/path"); err == nil {
		t.Error("a relative path")
	}
	if os.Geteuid() != 0 {
		locked := filepath.Join(dir, "locked")
		if err := os.Mkdir(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		if f, err := Open(locked); err == nil {
			if err := f.Check(); !errors.Is(err, ErrDenied) {
				t.Errorf("a locked folder: %v", err)
			}
			f.Close()
		} else if !errors.Is(err, ErrDenied) {
			t.Errorf("a locked folder: %v", err)
		}
	}
	f, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := f.Films(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("a cancelled walk: %v", err)
	}
}
