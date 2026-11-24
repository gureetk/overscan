// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0048Guides upgrades a database of schema 47 with a source,
// its channels in the household's lineup, its guide's programmes on one of
// them, and rules that show that channel's guide on another an hour later
// (D108), and checks
// that the upgrade adds no guide of the household's own and no match: the
// lineup and every programme that devices see stay as they were, after the
// next read of the source too (D139).
func TestMigration0048Guides(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:47])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 47 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	day := time.Date(2026, 12, 4, 0, 0, 0, 0, time.UTC)
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, xmltv_id, stream_url, position, listed_at)
			VALUES (40, 3, 'url:1', 'Alpha', 30, 'alpha.example', 'http://streams.example/1', 1, '2026-12-04T03:00:00Z'),
			       (41, 3, 'url:2', 'Bravo', 30, '', 'http://streams.example/2', 2, '2026-12-04T03:00:00Z'),
			       (42, 3, 'url:3', 'Charlie', 30, '', 'http://streams.example/3', 3, '2026-12-04T03:00:00Z');
		INSERT INTO lineup_groups (id, lineup_id, name) VALUES (50, 1, 'News');
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, pinned, hidden, rule_guide_id, rule_guide_shift)
			VALUES (60, 1, 40, 1, 0, NULL, NULL, NULL), (61, 1, 41, 2, 0, NULL, 40, 3600), (62, 1, 42, 3, 0, NULL, NULL, NULL);
		INSERT INTO lineup_rules (lineup_id, position, spec)
			VALUES (1, 1, '{"match":{"name":"Bravo"},"action":"guide","channel":"Alpha"}'),
			       (1, 2, '{"match":{"name":"Bravo"},"action":"timeshift","minutes":60}');
		INSERT INTO source_guides (source_id, generation, read_at, read_from, channels, programmes)
			VALUES (3, 2, '2026-12-04T03:00:00Z', 'abc', 1, 2);`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO programmes (source_id, generation, channel_id, start, stop, title)
			VALUES (3, 2, 40, ?, ?, 'Morning'), (3, 2, 40, ?, ?, 'Noon')`,
		day.Add(9*time.Hour).Unix(), day.Add(12*time.Hour).Unix(), day.Add(12*time.Hour).Unix(), day.Add(13*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 48 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	want := map[string][2]any{"Alpha": {1, "News"}, "Bravo": {2, "News"}, "Charlie": {3, "News"}}
	wantGuide := map[int64][]string{60: {"Morning 09:00", "Noon 12:00"}, 61: {"Morning 10:00", "Noon 13:00"}}
	check := func(when string) {
		t.Helper()
		if got := lineupShown(t, s, DefaultLineupID); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", when, got, want)
		}
		got := map[int64][]string{}
		if err := s.EachLineupProgramme(ctx, DefaultLineupID, day, day.Add(24*time.Hour), func(entry int64, p Programme) error {
			got[entry] = append(got[entry], p.Title+" "+p.Start.Format("15:04"))
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, wantGuide) {
			t.Errorf("%s: the guide %v, want %v", when, got, wantGuide)
		}
		if ps, err := s.EntryProgrammes(ctx, DefaultLineupID, 61, day, day.Add(24*time.Hour)); err != nil || len(ps) != 2 ||
			ps[0].Title != "Morning" || ps[0].Start != day.Add(10*time.Hour) {
			t.Errorf("%s: Bravo's programmes %+v, %v", when, ps, err)
		}
		if p, err := s.ProgrammeAt(ctx, DefaultLineupID, 60, day.Add(12*time.Hour)); err != nil || p.Title != "Noon" {
			t.Errorf("%s: the programme at noon %+v, %v", when, p, err)
		}
		gs, err := s.Guides(ctx)
		if err != nil || len(gs) != 0 {
			t.Errorf("%s: guides %+v, %v", when, gs, err)
		}
		if ms, err := s.GuideMatches(ctx); err != nil || len(ms) != 0 {
			t.Errorf("%s: matches %+v, %v", when, ms, err)
		}
		// Bravo shows Alpha's guide through its rule, but has none of its
		// own, and neither has Charlie.
		if n, err := s.ChannelsWithoutGuide(ctx); err != nil || n != 2 {
			t.Errorf("%s: %d channels without a guide, %v", when, n, err)
		}
	}
	check("after the upgrade")
	if _, err := s.ApplyCatalog(ctx, 3, []CatalogChannel{
		{IdentityKey: "url:1", Name: "Alpha", Category: "News", XMLTVID: "alpha.example", StreamURL: "http://streams.example/1"},
		{IdentityKey: "url:2", Name: "Bravo", Category: "News", StreamURL: "http://streams.example/2"},
		{IdentityKey: "url:3", Name: "Charlie", Category: "News", StreamURL: "http://streams.example/3"},
	}, day.Add(15*time.Hour)); err != nil {
		t.Fatal(err)
	}
	check("after the next read")
	if _, err := s.db.Exec("INSERT INTO guides (name, address, position, added_at, enabled) VALUES ('extra', x'00', 1, '2026-12-04T03:00:00Z', 2)"); err == nil {
		t.Error("enabled took 2")
	}
}
