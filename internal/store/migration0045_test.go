// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0045OwnGroups upgrades a database of schema 44 with a source,
// the household's lineup with edits, and a second lineup with its own
// group, and checks that every lineup still takes every category
// afterwards (D136): what devices see is the same, and so are the edits,
// after the next read too.
func TestMigration0045OwnGroups(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:44])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 44 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 1, '2026-12-04T03:00:00Z', '');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1), (31, 3, 'live', 'Sport', 2);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at)
			VALUES (40, 3, 'url:1', 'Alpha', 30, 'http://streams.example/1', 1, '2026-12-04T03:00:00Z'),
			       (41, 3, 'url:2', 'Bravo', 31, 'http://streams.example/2', 2, '2026-12-04T03:00:00Z'),
			       (42, 3, 'url:3', 'Charlie', NULL, 'http://streams.example/3', 3, '2026-12-04T03:00:00Z');
		INSERT INTO lineups (id, name) VALUES (2, 'Kitchen');
		INSERT INTO lineup_groups (id, lineup_id, name, hidden) VALUES (50, 1, 'News', 0), (51, 1, 'Sport', 1),
			(52, 2, 'Everything', 0);
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50), (1, 3, 'Sport', 51),
			(2, 3, 'News', 52);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, name, pinned) VALUES (60, 1, 40, 7, 'Alpha One', 1);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number) VALUES (61, 1, 41, 8), (62, 1, 42, 9);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, group_id) VALUES (63, 2, 40, 1, NULL), (64, 2, 41, 2, 52),
			(65, 2, 42, 3, NULL);
		INSERT INTO source_categories (source_id, kind, source_key, name, position, items, ticked, listed_at)
			VALUES (3, 'live', 'News', 'News', 1, 1, 1, '2026-12-04T03:00:00Z'), (3, 'live', 'Sport', 'Sport', 2, 1, 1, '2026-12-04T03:00:00Z');`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 45 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	lineups, err := s.Lineups(ctx)
	if err != nil || len(lineups) != 2 || lineups[0].OwnGroups || lineups[1].OwnGroups || lineups[1].Name != "Kitchen" {
		t.Fatalf("lineups %+v, %v", lineups, err)
	}

	household := map[string][2]any{"Alpha One": {7, "News"}, "Charlie": {9, ""}}
	kitchen := map[string][2]any{"Alpha": {1, "Everything"}, "Bravo": {2, "Everything"}, "Charlie": {3, ""}}
	check := func(when string) {
		t.Helper()
		if got := lineupShown(t, s, DefaultLineupID); !reflect.DeepEqual(got, household) {
			t.Errorf("%s: the household's lineup %v", when, got)
		}
		if got := lineupShown(t, s, 2); !reflect.DeepEqual(got, kitchen) {
			t.Errorf("%s: the second lineup %v", when, got)
		}
		if entries, total, err := s.LineupEntries(ctx, DefaultLineupID, EntryQuery{}); err != nil || total != 3 || len(entries) != 3 {
			t.Errorf("%s: the household's entries %d, %v", when, total, err)
		}
	}
	check("after the upgrade")
	if _, err := s.ApplyCatalog(ctx, 3, []CatalogChannel{
		{IdentityKey: "url:1", Name: "Alpha", Category: "News", StreamURL: "http://streams.example/1"},
		{IdentityKey: "url:2", Name: "Bravo", Category: "Sport", StreamURL: "http://streams.example/2"},
		{IdentityKey: "url:3", Name: "Charlie", StreamURL: "http://streams.example/3"},
	}, time.Date(2026, 12, 4, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	check("after the next read")

	// The column takes only 0 and 1.
	if _, err := s.db.Exec("UPDATE lineups SET own_groups = 2 WHERE id = 2"); err == nil {
		t.Error("own_groups took 2")
	}
}
