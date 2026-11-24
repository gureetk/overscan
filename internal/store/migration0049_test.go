// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0049GuideChoices upgrades a database of schema 48 with a
// source, its channels in the household's lineup, its guide's programmes on
// one of them, a rule that shows that channel's guide on another (D108),
// and a guide of the household's own that fills a third by name (D139),
// and checks that the upgrade chooses no channel's guide: the matches stay
// as they were, none by hand, and so does every programme that devices see,
// after the next read of the source too (D140).
func TestMigration0049GuideChoices(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:48])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 48 {
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
			VALUES (60, 1, 40, 1, 0, NULL, NULL, NULL), (61, 1, 41, 2, 0, NULL, 40, NULL), (62, 1, 42, 3, 0, NULL, NULL, NULL);
		INSERT INTO lineup_rules (lineup_id, position, spec)
			VALUES (1, 1, '{"match":{"name":"Bravo"},"action":"guide","channel":"Alpha"}');
		INSERT INTO source_guides (source_id, generation, read_at, read_from, channels, programmes)
			VALUES (3, 2, '2026-12-04T03:00:00Z', 'abc', 1, 2);
		INSERT INTO guides (id, name, address, host, position, added_at, generation, read_at, read_from, listed, channels, programmes)
			VALUES (7, 'extra', x'00', 'guide.example', 1, '2026-12-04T03:00:00Z', 4, '2026-12-04T03:00:00Z', 'def', 2, 1, 1);
		INSERT INTO guide_channels (id, guide_id, xmltv_id, names, listed_at, kept)
			VALUES (70, 7, 'charlie.example', 'Charlie', '2026-12-04T03:00:00Z', 1),
			       (71, 7, 'delta.example', 'Delta', '2026-12-04T03:00:00Z', 0);
		INSERT INTO channel_guide_matches (channel_id, guide_channel_id, by_name) VALUES (42, 70, 1);`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO programmes (source_id, generation, channel_id, start, stop, title)
			VALUES (3, 2, 40, ?, ?, 'Morning'), (3, 2, 40, ?, ?, 'Noon')`,
		day.Add(9*time.Hour).Unix(), day.Add(12*time.Hour).Unix(), day.Add(12*time.Hour).Unix(), day.Add(13*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO guide_programmes (guide_id, generation, channel_id, start, stop, title) VALUES (7, 4, 70, ?, ?, 'Talk')`,
		day.Add(10*time.Hour).Unix(), day.Add(11*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 49 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	want := map[string][2]any{"Alpha": {1, "News"}, "Bravo": {2, "News"}, "Charlie": {3, "News"}}
	wantGuide := map[int64][]string{60: {"Morning 09:00", "Noon 12:00"}, 61: {"Morning 09:00", "Noon 12:00"}, 62: {"Talk 10:00"}}
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
		if p, err := s.ProgrammeAt(ctx, DefaultLineupID, 62, day.Add(10*time.Hour)); err != nil || p.Title != "Talk" || p.ID >= 0 {
			t.Errorf("%s: Charlie's programme %+v, %v", when, p, err)
		}
		if ms, err := s.GuideMatches(ctx); err != nil || !reflect.DeepEqual(ms, []GuideMatch{{ChannelID: 42, GuideChannelID: 70, ByName: true}}) {
			t.Errorf("%s: matches %+v, %v", when, ms, err)
		}
		if hand, err := s.HandMatches(ctx); err != nil || len(hand) != 0 {
			t.Errorf("%s: hand matches %+v, %v", when, hand, err)
		}
		for _, id := range []int64{40, 41, 42} {
			if _, ok, err := s.GuideChoiceOf(ctx, id); ok || err != nil {
				t.Errorf("%s: channel %d has a choice, %v", when, id, err)
			}
		}
		if counts, err := s.ChannelGuideCounts(ctx); err != nil ||
			!reflect.DeepEqual(counts, map[string]int{ShowWithoutGuide: 1, ShowByName: 1, ShowByHand: 0}) {
			t.Errorf("%s: counts %v, %v", when, counts, err)
		}
		if gs, err := s.Guides(ctx); err != nil || len(gs) != 1 || gs[0].Matched != 1 || gs[0].ByName != 1 || gs[0].ByHand != 0 {
			t.Errorf("%s: guides %+v, %v", when, gs, err)
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
	for _, bad := range []string{
		"INSERT INTO channel_guide_choices (channel_id, use_channel, guide_channel_id, chosen_at) VALUES (41, 40, 70, 'x')",
		"INSERT INTO channel_guide_choices (channel_id, use_channel, chosen_at) VALUES (41, 41, 'x')",
		"UPDATE channel_guide_matches SET by_hand = 2",
	} {
		if _, err := s.db.Exec(bad); err == nil {
			t.Errorf("allowed: %s", bad)
		}
	}
}
