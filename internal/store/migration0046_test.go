// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0046GroupNumbers upgrades a database of schema 45 with a
// source, the household's lineup with a number pinned by hand and a hidden
// channel, and a lineup of its own groups, and checks that no group has a
// first number afterwards and that every channel keeps its number, after
// the next read too (D137).
func TestMigration0046GroupNumbers(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:45])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 45 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 1, '2026-12-04T03:00:00Z', '');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1), (31, 3, 'live', 'Sport', 2);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at)
			VALUES (40, 3, 'url:1', 'Alpha', 30, 'http://streams.example/1', 1, '2026-12-04T03:00:00Z'),
			       (41, 3, 'url:2', 'Bravo', 30, 'http://streams.example/2', 2, '2026-12-04T03:00:00Z'),
			       (42, 3, 'url:3', 'Charlie', 31, 'http://streams.example/3', 3, '2026-12-04T03:00:00Z');
		INSERT INTO lineups (id, name, own_groups) VALUES (2, 'Ours', 1);
		INSERT INTO lineup_groups (id, lineup_id, name, edited) VALUES (50, 1, 'News', 1), (51, 1, 'Sport', 0), (52, 2, 'Mine', 1);
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50), (1, 3, 'Sport', 51),
			(2, 3, 'News', 52);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, pinned, hidden) VALUES (60, 1, 40, 70, 1, NULL),
			(61, 1, 41, 2, 0, 1), (62, 1, 42, 3, 0, NULL), (63, 2, 40, 1, 0, NULL), (64, 2, 41, 2, 0, NULL), (65, 2, 42, NULL, 0, NULL);
		INSERT INTO source_categories (source_id, kind, source_key, name, position, items, ticked, listed_at)
			VALUES (3, 'live', 'News', 'News', 1, 2, 1, '2026-12-04T03:00:00Z'), (3, 'live', 'Sport', 'Sport', 2, 1, 1, '2026-12-04T03:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 46 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	numbers := func() map[int64]int {
		t.Helper()
		rows, err := s.db.Query("SELECT id, COALESCE(number, 0) FROM lineup_entries ORDER BY id")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int64]int{}
		for rows.Next() {
			var id int64
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatal(err)
			}
			out[id] = n
		}
		return out
	}
	want := map[int64]int{60: 70, 61: 2, 62: 3, 63: 1, 64: 2, 65: 0}
	check := func(when string) {
		t.Helper()
		if got := numbers(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: numbers %v, want %v", when, got, want)
		}
		for _, l := range []int64{1, 2} {
			groups, err := s.LineupGroups(ctx, l)
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range groups {
				if g.NumbersFrom != 0 {
					t.Errorf("%s: %s has a first number, %d", when, g.Name, g.NumbersFrom)
				}
			}
		}
		var in int
		if err := s.db.QueryRow("SELECT count(*) FROM lineup_entries WHERE numbered_in IS NOT NULL").Scan(&in); err != nil || in != 0 {
			t.Errorf("%s: %d entries numbered by a group, %v", when, in, err)
		}
	}
	check("after the upgrade")
	if _, err := s.ApplyCatalog(ctx, 3, []CatalogChannel{
		{IdentityKey: "url:1", Name: "Alpha", Category: "News", StreamURL: "http://streams.example/1"},
		{IdentityKey: "url:2", Name: "Bravo", Category: "News", StreamURL: "http://streams.example/2"},
		{IdentityKey: "url:3", Name: "Charlie", Category: "Sport", StreamURL: "http://streams.example/3"},
	}, time.Date(2026, 12, 4, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	check("after the next read")

	if _, err := s.db.Exec("UPDATE lineup_groups SET numbers_from = 0 WHERE id = 50"); err == nil {
		t.Error("numbers_from took 0")
	}
}
