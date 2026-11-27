// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0054TitleTranscoding upgrades a database of schema 53 with an
// Xtream source's films and series on shelves, one of them hidden and
// renamed, a profile of the administrator's that live TV and a channel use,
// and checks that nothing changes: every title and shelf, and films and
// series as a whole, transcode as needed, the shelves keep what was set of
// them, and the profile keeps its uses (D150).
func TestMigration0054TitleTranscoding(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	old, err := open(ctx, path, quiet(), migrations[:53])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 53 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	for _, q := range []string{`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'xtream', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '')`, `
		INSERT INTO library_categories (id, source_id, kind, source_key, name, position, generation)
			VALUES (20, 3, 'movie', '1', 'Drama', 1, 1), (21, 3, 'series', '2', 'Comedy', 1, 1)`, `
		INSERT INTO titles (id, source_id, kind, source_key, category_id, name, position, generation, locked)
			VALUES (30, 3, 'movie', '100', 20, 'Quiet Harbour', 1, 1, 1),
			       (31, 3, 'series', '200', 21, 'Coastline', 1, 1, 0)`, `
		INSERT INTO shelf_settings (kind, shelf, hidden, name) VALUES ('movie', 'name:Drama', 1, 'Dramas')`, `
		INSERT INTO transcode_profiles (name, codec, max_lines, quality, max_kbps, frame_rate, deinterlace, tone_map, sound, made_at)
			VALUES ('kitchen', 'hevc', 720, 'low', 3000, '30', 1, 0, 'ac3', '2026-12-04T03:00:00Z')`, `
		INSERT INTO settings (key, value) VALUES ('live_transcode', 'kitchen')`, `
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (40, 3, 'live', 'News', 1)`, `
		INSERT INTO channels (id, source_id, identity_key, name, category_id, xmltv_id, stream_url, position, listed_at, transcode)
			VALUES (50, 3, 'a', 'Alpha', 40, '', 'http://streams.example/a', 1, '2026-12-04T03:00:00Z', 'never')`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 54 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	for _, id := range []int64{30, 31} {
		if tc, by, err := s.TitleTranscode(ctx, id); err != nil || !tc.AsNeeded() || by != "" {
			t.Errorf("title %d: %+v by %q, %v", id, tc, by, err)
		}
		a, err := s.AdminTitleByID(ctx, id)
		if err != nil || !a.TranscodeOwn.AsNeeded() || !a.Transcode.AsNeeded() || a.Own != (id == 30) {
			t.Errorf("title %d for the admin: %+v, %v", id, a, err)
		}
	}
	if vod, err := s.VODTranscode(ctx); err != nil || !vod.AsNeeded() {
		t.Errorf("films and series: %+v, %v", vod, err)
	}
	shelves, err := s.LibraryShelves(ctx, KindMovie)
	if err != nil || len(shelves) != 1 || !shelves[0].Hidden || shelves[0].Name != "Dramas" || !shelves[0].Transcode.AsNeeded() {
		t.Errorf("the films' shelves: %+v, %v", shelves, err)
	}
	if live, err := s.LiveTranscode(ctx); err != nil || live.Profile != "kitchen" {
		t.Errorf("live TV: %+v, %v", live, err)
	}
	if u, err := s.TranscodeProfileUses(ctx, "kitchen"); err != nil ||
		u != (ProfileUses{LiveTV: true}) {
		t.Errorf("the profile's uses: %+v, %v", u, err)
	}
}
