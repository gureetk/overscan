// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0052MovedChannels upgrades a database of schema 51 with a
// source's channels in the household's lineup, and checks that every
// channel is where it was, after the source's next read too: no channel
// has moved (D144).
func TestMigration0052MovedChannels(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	old, err := open(ctx, path, quiet(), migrations[:51])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 51 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	for _, q := range []string{`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '')`, `
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1)`, `
		INSERT INTO channels (id, source_id, identity_key, name, category_id, xmltv_id, stream_url, position, listed_at)
			VALUES (40, 3, 'a', 'Alpha', 30, '', 'http://streams.example/a', 1, '2026-12-04T03:00:00Z'),
			       (41, 3, 'b', 'Bravo', 30, '', 'http://streams.example/b', 2, '2026-12-04T03:00:00Z')`, `
		INSERT INTO lineup_groups (id, lineup_id, name) VALUES (50, 1, 'News')`, `
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50)`, `
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, pinned) VALUES (60, 1, 40, 1, 0), (61, 1, 41, 2, 0)`,
	} {
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
	s.UseKey(secrets.NewRandomBox())
	if v, err := s.SchemaVersion(ctx); err != nil || v < 52 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	before, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(before) != 2 {
		t.Fatalf("the lineup %+v, %v", before, err)
	}
	if n, err := s.MovedChannels(ctx, 3); n != 0 || err != nil {
		t.Errorf("moved %d, %v", n, err)
	}
	apply(t, s, 3, time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC), []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", Category: "News", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Bravo", Category: "News", StreamURL: "http://streams.example/b"},
	})
	after, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("after the read:\n%+v\nbefore\n%+v, %v", after, before, err)
	}
	if movable, err := s.MovableChannels(ctx, 3); err != nil || len(movable) != 2 {
		t.Errorf("movable %+v, %v", movable, err)
	}
}
