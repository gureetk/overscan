// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0051TitlePictures upgrades a database of schema 50 with an
// Xtream server's films, among them some without a poster and ten that
// share one, a film whose details were read, and what TMDB said of a
// folder's film (D88), and checks that every title keeps its pictures:
// TMDB's pictures for providers' titles are off (D143), and nothing is
// known not to load.
func TestMigration0051TitlePictures(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:50])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 50 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	queries := []string{`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'xtream', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', ''),
			       (4, 'films', 'folder', '', 1, 0, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '')`, `
		INSERT INTO titles (id, source_id, kind, source_key, name, year, poster, position, generation, details)
			VALUES (100, 3, 'movie', '1', 'Alpha', 2020, 'http://pictures.example/alpha.jpg', 1, 1, NULL),
			       (101, 3, 'movie', '2', 'Bravo', 2021, '', 2, 1, NULL),
			       (102, 3, 'movie', '3', 'Charlie', 2022, 'http://pictures.example/charlie.jpg', 3, 1,
			        '{"poster":"http://pictures.example/charlie-big.jpg","plot":"A made-up film."}'),
			       (103, 4, 'movie', 'delta', 'Delta', 2019, 'https://image.tmdb.org/t/p/w342/delta.jpg', 1, 1, NULL)`, `
		INSERT INTO tmdb_titles (source_id, kind, key, tmdb_id, details, checked_at)
			VALUES (4, 'movie', 'delta', 77, '{"details":{"poster":"https://image.tmdb.org/t/p/w342/delta.jpg"}}', 1796000000),
			       (3, 'movie', '2', 78, '{"details":{"poster":"https://image.tmdb.org/t/p/w342/bravo.jpg"}}', 1796000000)`,
	}
	for i := range 10 {
		queries = append(queries, `INSERT INTO titles (source_id, kind, source_key, name, poster, position, generation)
			VALUES (3, 'movie', 'shared`+string(rune('a'+i))+`', 'Shared', 'http://pictures.example/none.jpg', `+
			string(rune('4'+i%5))+`, 1)`)
	}
	for _, q := range queries {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 51 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	if on, err := s.TMDBPictures(ctx); on || err != nil {
		t.Errorf("TMDB's pictures: %v, %v", on, err)
	}
	for id, want := range map[int64]string{100: "http://pictures.example/alpha.jpg", 101: "",
		102: "http://pictures.example/charlie-big.jpg", 103: "https://image.tmdb.org/t/p/w342/delta.jpg"} {
		ti, err := s.Title(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		poster := ti.Poster
		if ti.Details != nil && ti.Details.Poster != "" {
			poster = ti.Details.Poster
		}
		if poster != want || ti.Summary.Backdrop != "" {
			t.Errorf("title %d: poster %q, backdrop %q; want %q", id, poster, ti.Summary.Backdrop, want)
		}
	}
	var fills, checks int
	if err := s.db.QueryRow(`SELECT (SELECT count(*) FROM titles WHERE poster_fill <> '' OR backdrop_fill <> ''),
		(SELECT count(*) FROM picture_checks)`).Scan(&fills, &checks); err != nil || fills != 0 || checks != 0 {
		t.Errorf("fills %d, checks %d, %v", fills, checks, err)
	}
	if found, missed, err := s.TMDBCounts(ctx); found != 2 || missed != 0 || err != nil {
		t.Errorf("TMDB's titles: %d, %d, %v", found, missed, err)
	}
}
