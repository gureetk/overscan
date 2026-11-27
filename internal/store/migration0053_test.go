// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0053Transcoding upgrades a database of schema 52 with a
// source's channels in the household's lineup, an account whose output
// profile transcodes and a device of its own, and checks that nothing
// changes: every channel, group and live TV transcode as needed, there are
// no profiles of the administrator's, and the output profiles are as they
// were (D147).
func TestMigration0053Transcoding(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	old, err := open(ctx, path, quiet(), migrations[:52])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 52 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	for _, q := range []string{`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '')`, `
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1)`, `
		INSERT INTO channels (id, source_id, identity_key, name, category_id, xmltv_id, stream_url, position, listed_at, repair)
			VALUES (40, 3, 'a', 'Alpha', 30, '', 'http://streams.example/a', 1, '2026-12-04T03:00:00Z', 1),
			       (41, 3, 'b', 'Bravo', 30, '', 'http://streams.example/b', 2, '2026-12-04T03:00:00Z', NULL)`, `
		INSERT INTO lineup_groups (id, lineup_id, name) VALUES (50, 1, 'News')`, `
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50)`, `
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, pinned) VALUES (60, 1, 40, 1, 0), (61, 1, 41, 2, 0)`, `
		UPDATE accounts SET output_profile = 'h264-720p' WHERE id = 1`, `
		INSERT INTO devices (id, mac, account_id, state, output_profile, created_at)
			VALUES (70, '00:1A:79:00:00:01', 1, 'approved', 'h264-1080p', '2026-12-04T03:00:00Z')`,
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 53 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	before, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(before) != 2 {
		t.Fatalf("the lineup %+v, %v", before, err)
	}
	for _, id := range []int64{60, 61} {
		p, err := s.PlayableEntry(ctx, id)
		if err != nil || !p.Transcode.AsNeeded() || p.TranscodeBy != "" || p.Repair != (id == 60) {
			t.Errorf("entry %d: %+v, %v", id, p, err)
		}
	}
	if live, err := s.LiveTranscode(ctx); err != nil || !live.AsNeeded() {
		t.Errorf("live TV: %+v, %v", live, err)
	}
	if list, err := s.TranscodeProfiles(ctx); err != nil || len(list) != 0 {
		t.Errorf("profiles: %+v, %v", list, err)
	}
	if p, err := s.AccountOutputProfile(ctx, HouseholdAccount); err != nil || p.Name != "h264-720p" || p.Made {
		t.Errorf("the account's profile: %+v, %v", p, err)
	}
	if p, err := s.OutputProfileOf(ctx, 70); err != nil || p.Name != "h264-1080p" {
		t.Errorf("the device's profile: %+v, %v", p, err)
	}
	groups, err := s.LineupGroups(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if !g.Transcode.AsNeeded() {
			t.Errorf("group %s: %+v", g.Name, g.Transcode)
		}
	}
	apply(t, s, 3, time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC), []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", Category: "News", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Bravo", Category: "News", StreamURL: "http://streams.example/b"},
	})
	after, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Errorf("after the read:\n%+v\nbefore\n%+v, %v", after, before, err)
	}
}
