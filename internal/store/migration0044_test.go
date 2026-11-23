// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0044SourceCategories upgrades a database of schema 43 with a
// source, its channels in two categories with an edit in the lineup, a
// hidden group, and films on a shelf, and checks that every source brings
// every category afterwards, as before (D135): what devices see is the
// same, and the next read keeps every channel and title.
func TestMigration0044SourceCategories(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:43])
	if err != nil {
		t.Fatal(err)
	}
	old.UseKey(box)
	if v, err := old.SchemaVersion(ctx); err != nil || v != 43 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'xtream', 'provider.example', 1, 2, 43200, 'playlist', 0, 1, '2026-12-04T03:00:00Z', '');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1), (31, 3, 'live', 'Sport', 2);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at)
			VALUES (40, 3, 'xtream:1', 'Alpha', 30, 'live/1.ts', 1, '2026-12-04T03:00:00Z'),
			       (41, 3, 'xtream:2', 'Bravo', 31, 'live/2.ts', 2, '2026-12-04T03:00:00Z');
		INSERT INTO lineup_groups (id, lineup_id, name, hidden) VALUES (50, 1, 'News', 0), (51, 1, 'Sport', 1);
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50), (1, 3, 'Sport', 51);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, name, pinned) VALUES (60, 1, 40, 7, 'Alpha One', 1);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number) VALUES (61, 1, 41, 8);
		INSERT INTO library_reads (source_id, kind, generation, read_at, tried_at, read_error, titles)
			VALUES (3, 'movie', 1, '2026-12-04T03:00:00Z', '2026-12-04T03:00:00Z', '', 1);
		INSERT INTO library_categories (id, source_id, kind, source_key, name, position, generation)
			VALUES (70, 3, 'movie', '21', 'Drama', 1, 1);
		INSERT INTO titles (id, source_id, kind, source_key, category_id, name, position, generation)
			VALUES (80, 3, 'movie', '5001', 70, 'The Long Field', 1, 1);`); err != nil {
		t.Fatal(err)
	}
	sealed, err := old.sealSource(3, sourceSecret{URL: "http://provider.example:8080", Username: "alex", Password: "correct-horse"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec("INSERT INTO source_secrets (source_id, secret) VALUES (3, ?)", sealed); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 44 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	copies, err := filepath.Glob(filepath.Join(filepath.Dir(path), BackupsFolder, "before-upgrade-*-schema43.db"))
	if err != nil || len(copies) != 1 {
		t.Errorf("the copy before the upgrade: %v, %v", copies, err)
	}

	// Every source brings every category, and nothing is listed yet.
	for _, kind := range []string{KindLive, KindMovie, KindSeries} {
		if c, err := s.CategoryChoice(ctx, 3, kind); err != nil || c != (CategoryChoice{}) {
			t.Errorf("%s: %+v, %v", kind, c, err)
		}
		if cats, total, err := s.SourceCategories(ctx, 3, kind, ""); err != nil || total != 0 || len(cats) != 0 {
			t.Errorf("%s categories %+v, %d, %v", kind, cats, total, err)
		}
	}
	src, err := s.Source(ctx, 3)
	if err != nil || src.Name != "main" || !src.PublicOnly || src.Channels != 2 || src.Refresh != 12*time.Hour {
		t.Errorf("main %+v, %v", src, err)
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(cs) != 1 || cs[0].EntryID != 60 || cs[0].Name != "Alpha One" || cs[0].Number != 7 {
		t.Errorf("lineup %+v, %v", cs, err)
	}

	// The next read keeps every channel, the edits and the hidden group,
	// and records the categories, ticked.
	at := time.Date(2026, 12, 5, 3, 0, 0, 0, time.UTC)
	ch, err := s.ApplyCatalog(ctx, 3, []CatalogChannel{
		{IdentityKey: "xtream:1", Name: "Alpha", Category: "News", StreamURL: "live/1.ts"},
		{IdentityKey: "xtream:2", Name: "Bravo", Category: "Sport", StreamURL: "live/2.ts"},
	}, at)
	if err != nil || ch.Updated != 2 || ch.Added != 0 || ch.Missing != 0 || ch.LeftOut != 0 {
		t.Errorf("the next read %+v, %v", ch, err)
	}
	cs, err = s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(cs) != 1 || cs[0].EntryID != 60 || cs[0].Name != "Alpha One" || cs[0].Number != 7 {
		t.Errorf("lineup after a read %+v, %v", cs, err)
	}
	if got := lineupNumbers(t, s); got["Alpha"] != 7 || got["Bravo"] != 8 {
		t.Errorf("numbers %v", got)
	}
	if cats, _, err := s.SourceCategories(ctx, 3, KindLive, ""); err != nil || len(cats) != 2 || !cats[0].Ticked || !cats[1].Ticked ||
		cats[0].Items != 1 {
		t.Errorf("live categories %+v, %v", cats, err)
	}
	lc, err := s.ApplyLibrary(ctx, 3, KindMovie, []LibraryCategory{{Key: "21", Name: "Drama"}},
		[]LibraryTitle{{Key: "5001", Category: "21", Name: "The Long Field"}}, at)
	if err != nil || lc.Updated != 1 || lc.LeftOut != 0 || lc.Missing != 0 {
		t.Errorf("films after the upgrade %+v, %v", lc, err)
	}
	if ids := titleIDs(t, s); ids["The Long Field"] != 80 {
		t.Errorf("titles %v", ids)
	}

	// The tables hold only what they may.
	for _, bad := range []string{
		"INSERT INTO source_categories (source_id, kind, source_key, name, position, ticked, listed_at) VALUES (3, 'radio', 'x', 'x', 1, 1, '2026-12-05T03:00:00Z')",
		"INSERT INTO source_categories (source_id, kind, source_key, name, position, ticked, listed_at) VALUES (3, 'live', 'x', 'x', 1, 2, '2026-12-05T03:00:00Z')",
		"INSERT INTO source_category_choices (source_id, kind, only_ticked, tick_new, changed_at) VALUES (3, 'episode', 1, 0, '2026-12-05T03:00:00Z')",
		"INSERT INTO source_category_choices (source_id, kind, only_ticked, tick_new, changed_at) VALUES (3, 'live', 2, 0, '2026-12-05T03:00:00Z')",
	} {
		if _, err := s.db.Exec(bad); err == nil || !strings.Contains(err.Error(), "CHECK") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err := s.db.Exec(`INSERT INTO source_category_choices (source_id, kind, only_ticked, tick_new, changed_at)
		VALUES (9, 'live', 1, 0, '2026-12-05T03:00:00Z')`); err == nil {
		t.Error("a choice for a source that does not exist")
	}
}
