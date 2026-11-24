// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

// pictureFilms are a fictional Xtream server's films: one with a poster,
// one without, one whose poster will not load, and SharedPicture that
// share one, a placeholder.
func pictureFilms(shared int) []LibraryTitle {
	out := []LibraryTitle{
		{Key: "1", Name: "Alpha", Year: 2020, Poster: "http://img.example/alpha.jpg"},
		{Key: "2", Name: "Bravo", Year: 2021},
		{Key: "3", Name: "Charlie", Year: 2022, Poster: "http://img.example/charlie.jpg",
			Summary: TitleSummary{Backdrop: "http://img.example/charlie-wide.jpg"}},
	}
	for i := range shared {
		out = append(out, LibraryTitle{Key: fmt.Sprint(10 + i), Name: fmt.Sprintf("Shared %d", i), Poster: "http://img.example/none.jpg"})
	}
	return out
}

func wantNames(ws []PictureWant) []string {
	var out []string
	for _, w := range ws {
		out = append(out, w.Name)
	}
	return out
}

// tmdbFound is what TMDB says of a film it found, with its pictures.
func tmdbFound(id int64, name string, at time.Time) TMDBTitle {
	return TMDBTitle{ID: id, CheckedAt: at, Details: TitleDetails{Poster: "https://image.tmdb.org/t/p/w342/" + name + ".jpg",
		TitleSummary: TitleSummary{Backdrop: "https://image.tmdb.org/t/p/w1280/" + name + ".jpg"}}}
}

// TestTitlePictures checks which titles' pictures want replacing, how
// TMDB's take their place and give it back, and how what does not load is
// recorded (D143).
func TestTitlePictures(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	src := ids["main"]
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, src, KindMovie, nil, pictureFilms(SharedPicture), at); err != nil {
		t.Fatal(err)
	}
	titles := titleIDs(t, s)
	poster := func(name string) (string, string) {
		t.Helper()
		ti, err := s.Title(ctx, titles[name])
		if err != nil {
			t.Fatal(err)
		}
		return ti.Poster, ti.Summary.Backdrop
	}

	// What wants replacing: no poster, and the shared one.
	ws, err := s.PictureWants(ctx, src, KindMovie, at, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := wantNames(ws); len(got) != 1+SharedPicture || got[0] != "Bravo" || got[1] != "Shared 0" {
		t.Fatalf("wants: %v", got)
	}
	// One fewer sharing it, and it is no placeholder.
	if _, err := s.ApplyLibrary(ctx, src, KindMovie, nil, pictureFilms(SharedPicture-1), at); err != nil {
		t.Fatal(err)
	}
	if ws, _ = s.PictureWants(ctx, src, KindMovie, at, 100); !reflect.DeepEqual(wantNames(ws), []string{"Bravo"}) {
		t.Fatalf("wants with fewer sharing: %v", wantNames(ws))
	}
	if _, err := s.ApplyLibrary(ctx, src, KindMovie, nil, pictureFilms(SharedPicture), at); err != nil {
		t.Fatal(err)
	}
	// Charlie's poster does not load.
	if err := s.PictureFailed(ctx, src, "http://img.example/charlie.jpg", at); err != nil {
		t.Fatal(err)
	}
	if ws, _ = s.PictureWants(ctx, src, KindMovie, at, 3); !reflect.DeepEqual(wantNames(ws), []string{"Bravo", "Charlie", "Shared 0"}) {
		t.Fatalf("wants with Charlie's failing: %v", wantNames(ws))
	}

	// TMDB found Bravo and Charlie, and not Shared 0.
	for name, tt := range map[string]TMDBTitle{"2": tmdbFound(41, "bravo", at), "3": tmdbFound(42, "charlie", at),
		"10": {CheckedAt: at}} {
		if err := s.SetTMDBTitle(ctx, src, KindMovie, name, tt); err != nil {
			t.Fatal(err)
		}
	}
	if ws, _ = s.PictureWants(ctx, src, KindMovie, at, 100); len(ws) != SharedPicture-1 || slices.Contains(wantNames(ws), "Shared 0") {
		t.Errorf("wants once asked: %v", wantNames(ws))
	}
	if ws, _ = s.PictureWants(ctx, src, KindMovie, at.Add(time.Second), 100); len(ws) != SharedPicture {
		t.Errorf("wants once a miss is due again: %v", wantNames(ws))
	}
	restored, filled, err := s.SettlePictures(ctx, src, KindMovie)
	if err != nil || restored != 0 || filled != 2 {
		t.Fatalf("settle: %d, %d, %v", restored, filled, err)
	}
	if p, b := poster("Bravo"); p != "https://image.tmdb.org/t/p/w342/bravo.jpg" || b != "https://image.tmdb.org/t/p/w1280/bravo.jpg" {
		t.Errorf("Bravo: %q, %q", p, b)
	}
	// Charlie keeps its own wide picture, and TMDB's poster comes before
	// the one its details give.
	if err := s.SetMovieDetails(ctx, titles["Charlie"], TitleDetails{Poster: "http://img.example/charlie-big.jpg"}, "", at); err != nil {
		t.Fatal(err)
	}
	ti, err := s.Title(ctx, titles["Charlie"])
	if err != nil || ti.Poster != "https://image.tmdb.org/t/p/w342/charlie.jpg" || ti.Details.Poster != ti.Poster ||
		ti.Summary.Backdrop != "http://img.example/charlie-wide.jpg" {
		t.Errorf("Charlie: %+v, %v", ti, err)
	}
	page, _, err := s.TitlePage(ctx, KindMovie, 0, "Charlie", ByName, 0, 10, false, Sources{})
	if err != nil || len(page) != 1 || page[0].Details == nil || page[0].Details.Poster != "https://image.tmdb.org/t/p/w342/charlie.jpg" {
		t.Errorf("Charlie's listing: %+v, %v", page, err)
	}
	page, _, _ = s.TitlePage(ctx, KindMovie, 0, "Bravo", ByName, 0, 10, false, Sources{})
	if len(page) != 1 || page[0].Summary.Backdrop != "https://image.tmdb.org/t/p/w1280/bravo.jpg" {
		t.Errorf("Bravo's listing: %+v", page)
	}
	posters, backdrops, _, err := s.TitleArt(ctx, titles["Bravo"])
	if err != nil || len(posters) != 1 || posters[0] != "https://image.tmdb.org/t/p/w342/bravo.jpg" || len(backdrops) != 1 {
		t.Errorf("Bravo's pictures: %v, %v, %v", posters, backdrops, err)
	}
	rows, err := s.SearchTitles(ctx, "Bravo", 10, Sources{})
	if err != nil || len(rows) != 1 || rows[0].Poster != "https://image.tmdb.org/t/p/w342/bravo.jpg" {
		t.Errorf("search: %+v, %v", rows, err)
	}
	if filled, failing, err := s.PictureCounts(ctx); filled != 2 || failing != 1 || err != nil {
		t.Errorf("counts: %d, %d, %v", filled, failing, err)
	}

	// A shared poster takes TMDB's as the run gives it.
	if err := s.SetPictureFill(ctx, titles["Shared 1"], "https://image.tmdb.org/t/p/w342/shared.jpg", ""); err != nil {
		t.Fatal(err)
	}
	// Charlie's poster loads again: its own comes back.
	due, err := s.PicturesToCheck(ctx, at.Add(time.Second), 10)
	if err != nil || len(due) != 1 || due[0].Address != "http://img.example/charlie.jpg" || due[0].SourceID != src {
		t.Fatalf("to check: %+v, %v", due, err)
	}
	if due, _ := s.PicturesToCheck(ctx, at, 10); len(due) != 0 {
		t.Errorf("checked too soon: %+v", due)
	}
	if err := s.PictureLoads(ctx, src, "http://img.example/charlie.jpg"); err != nil {
		t.Fatal(err)
	}
	if p, _ := poster("Charlie"); p != "http://img.example/charlie.jpg" {
		t.Errorf("Charlie once its poster loads: %q", p)
	}
	// A read in which Bravo has a poster gives it its own; and with fewer
	// sharing the placeholder, the next settle gives theirs back too.
	again := pictureFilms(SharedPicture - 1)
	again[1].Poster = "http://img.example/bravo.jpg"
	if _, err := s.ApplyLibrary(ctx, src, KindMovie, nil, again, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p, b := poster("Bravo"); p != "http://img.example/bravo.jpg" || b != "" {
		t.Errorf("Bravo with a poster of its own: %q, %q", p, b)
	}
	if p, _ := poster("Shared 1"); p != "https://image.tmdb.org/t/p/w342/shared.jpg" {
		t.Errorf("Shared 1 before settling: %q", p)
	}
	if restored, filled, err := s.SettlePictures(ctx, src, KindMovie); restored != 1 || filled != 0 || err != nil {
		t.Errorf("settle again: %d, %d, %v", restored, filled, err)
	}
	if p, _ := poster("Shared 1"); p != "http://img.example/none.jpg" {
		t.Errorf("Shared 1 after settling: %q", p)
	}

	// Off, or without TMDB's key, every title shows its own.
	if err := s.PictureFailed(ctx, src, "http://img.example/bravo.jpg", at); err != nil {
		t.Fatal(err)
	}
	if _, filled, _ := s.SettlePictures(ctx, src, KindMovie); filled != 1 {
		t.Errorf("Bravo's failing poster: %d filled", filled)
	}
	if err := s.SetTMDBPictures(ctx, true); err != nil {
		t.Fatal(err)
	}
	if on, err := s.TMDBPictures(ctx); !on || err != nil {
		t.Errorf("on: %v, %v", on, err)
	}
	if err := s.SetTMDBPictures(ctx, false); err != nil {
		t.Fatal(err)
	}
	if p, _ := poster("Bravo"); p != "http://img.example/bravo.jpg" {
		t.Errorf("Bravo, off: %q", p)
	}
	if _, filled, _ := s.SettlePictures(ctx, src, KindMovie); filled != 1 {
		t.Errorf("Bravo again: %d filled", filled)
	}
	if err := s.SetTMDBKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := poster("Bravo"); p != "http://img.example/bravo.jpg" {
		t.Errorf("Bravo, without a key: %q", p)
	}
	// Checks of pictures that no title shows are forgotten.
	if err := s.PictureFailed(ctx, src, "http://img.example/gone.jpg", at); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetPictureChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if _, failing, _ := s.PictureCounts(ctx); failing != 1 {
		t.Errorf("%d checks kept, want Bravo's", failing)
	}
}
