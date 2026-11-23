// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0047ChannelHealth upgrades a database of schema 46 with a
// source, its channels in the household's lineup, one hidden by hand and
// one pinned, and checks that no channel has a health record afterwards:
// devices see the same channels, with the same numbers, after the next
// read too (D138).
func TestMigration0047ChannelHealth(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:46])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 46 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 1, '2026-12-04T03:00:00Z', '');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at)
			VALUES (40, 3, 'url:1', 'Alpha', 30, 'http://streams.example/1', 1, '2026-12-04T03:00:00Z'),
			       (41, 3, 'url:2', 'Bravo', 30, 'http://streams.example/2', 2, '2026-12-04T03:00:00Z'),
			       (42, 3, 'url:3', 'Charlie', 30, 'http://streams.example/3', 3, '2026-12-04T03:00:00Z');
		INSERT INTO lineup_groups (id, lineup_id, name, numbers_from) VALUES (50, 1, 'News', 100);
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, pinned, hidden, numbered_in)
			VALUES (60, 1, 40, 100, 0, NULL, 50), (61, 1, 41, NULL, 0, 1, NULL), (62, 1, 42, 7, 1, NULL, NULL);`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 47 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	want := map[string][2]any{"Alpha": {100, "News"}, "Charlie": {7, "News"}}
	check := func(when string) {
		t.Helper()
		if got := lineupShown(t, s, DefaultLineupID); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", when, got, want)
		}
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM channel_health").Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d health records, %v", when, n, err)
		}
		if failed, total, err := s.FailedChannels(ctx); err != nil || total != 0 || len(failed) != 0 {
			t.Errorf("%s: failed channels %+v, %v", when, failed, err)
		}
	}
	check("after the upgrade")
	if _, err := s.ApplyCatalog(ctx, 3, []CatalogChannel{
		{IdentityKey: "url:1", Name: "Alpha", Category: "News", StreamURL: "http://streams.example/1"},
		{IdentityKey: "url:2", Name: "Bravo", Category: "News", StreamURL: "http://streams.example/2"},
		{IdentityKey: "url:3", Name: "Charlie", Category: "News", StreamURL: "http://streams.example/3"},
	}, time.Date(2026, 12, 4, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	check("after the next read")
	if _, err := s.db.Exec("INSERT INTO channel_health (channel_id, held) VALUES (40, 2)"); err == nil {
		t.Error("held took 2")
	}
}
