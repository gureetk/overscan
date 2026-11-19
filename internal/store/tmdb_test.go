// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

// TMDB's key is kept sealed, and removing it forgets what TMDB said and the
// posters that folders' titles had from it, not other sources' (D88).
func TestTMDB(t *testing.T) {
	path := newPath(t)
	s := openTest(t, path)
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"), source.Settings{Name: "one", Type: source.TypeFolder, URL: "/srv/one"})
	if key, err := s.TMDBKey(ctx); err != nil || key != "" {
		t.Fatalf("no key: %q, %v", key, err)
	}
	if err := s.SetTMDBKey(ctx, "made-up-key"); err != nil {
		t.Fatal(err)
	}
	if key, err := s.TMDBKey(ctx); err != nil || key != "made-up-key" {
		t.Errorf("key %q, %v", key, err)
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT secret FROM sealed_settings`).Scan(&sealed); err != nil ||
		strings.Contains(string(sealed), "made-up-key") {
		t.Errorf("the key is not sealed: %v", err)
	}

	poster := "https://image.tmdb.org/t/p/w342/made-up.jpg"
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	for name, id := range ids {
		if _, err := s.ApplyLibrary(ctx, id, KindMovie, []LibraryCategory{{Key: "c", Name: name}},
			[]LibraryTitle{{Key: "k", Category: "c", Name: "Quiet Harbour " + name, Poster: poster}}, at); err != nil {
			t.Fatal(err)
		}
	}
	found := TMDBTitle{ID: 7, Details: TitleDetails{Poster: poster, TitleSummary: TitleSummary{Plot: "A harbour."}},
		Episodes: []TMDBEpisode{{Season: 1, Number: 2, Name: "Two"}}, CheckedAt: at}
	if err := s.SetTMDBTitle(ctx, ids["one"], KindMovie, "k", found); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTMDBTitle(ctx, ids["one"], KindMovie, "other", TMDBTitle{CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	got, err := s.TMDBTitles(ctx, ids["one"], KindMovie)
	if err != nil || len(got) != 2 || got["k"].ID != 7 || got["k"].Details.Plot != "A harbour." ||
		len(got["k"].Episodes) != 1 || !got["k"].CheckedAt.Equal(at) {
		t.Errorf("titles %+v, %v", got, err)
	}
	if one, ok, err := s.TMDBTitleOf(ctx, ids["one"], KindMovie, "other"); err != nil || !ok || one.ID != 0 {
		t.Errorf("no match %+v %v %v", one, ok, err)
	}
	if _, ok, err := s.TMDBTitleOf(ctx, ids["one"], KindSeries, "k"); err != nil || ok {
		t.Errorf("never asked: %v %v", ok, err)
	}
	if f, m, err := s.TMDBCounts(ctx); err != nil || f != 1 || m != 1 {
		t.Errorf("counts %d %d, %v", f, m, err)
	}
	// A new language makes every title due again, keeping what TMDB said;
	// the same language again changes nothing.
	if err := s.SetTMDBLanguage(ctx, "fr-FR"); err != nil {
		t.Fatal(err)
	}
	if one, _, _ := s.TMDBTitleOf(ctx, ids["one"], KindMovie, "k"); one.ID != 7 || !one.CheckedAt.Equal(time.Unix(0, 0)) {
		t.Errorf("after a new language %+v", one)
	}
	if err := s.SetTMDBTitle(ctx, ids["one"], KindMovie, "k", found); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTMDBLanguage(ctx, "fr-FR"); err != nil {
		t.Fatal(err)
	}
	if one, _, _ := s.TMDBTitleOf(ctx, ids["one"], KindMovie, "k"); !one.CheckedAt.Equal(at) {
		t.Errorf("after the same language %+v", one)
	}

	if err := s.SetTMDBKey(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if key, _ := s.TMDBKey(ctx); key != "" {
		t.Errorf("key %q after removing it", key)
	}
	if f, m, _ := s.TMDBCounts(ctx); f != 0 || m != 0 {
		t.Errorf("counts after removing the key %d %d", f, m)
	}
	posters := map[string]string{}
	rows, err := s.db.QueryContext(ctx, `SELECT name, poster FROM titles`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name, p string
		if err := rows.Scan(&name, &p); err != nil {
			t.Fatal(err)
		}
		posters[name] = p
	}
	rows.Close()
	if posters["Quiet Harbour one"] != "" || posters["Quiet Harbour main"] != poster {
		t.Errorf("posters %v", posters)
	}
	if err := s.SetTMDBLanguage(ctx, "en-GB"); err != nil {
		t.Fatal(err)
	}
	if l, err := s.TMDBLanguage(ctx); err != nil || l != "en-GB" {
		t.Errorf("language %q, %v", l, err)
	}
}
