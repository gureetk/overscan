// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0050Logos upgrades a database of schema 49 with a source,
// its channels in the household's lineup, some with logos, a rule that
// gives one a logo (D107), and a guide of the household's own whose
// channels, one with an icon, fill two of them (D139), and checks that
// every logo stays as it was, after the next read of the source too: no
// logo is set by hand, the source's guide's logos are not used, and
// nothing is known to fail to load. The one channel that had no logo, and
// that the guide fills with an icon, takes it, as the guide's logos are
// used where a channel has none (D141); no full release had guides of the
// household's own.
func TestMigration0050Logos(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:49])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 49 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	for _, q := range []string{`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'm3u', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '')`, `
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1)`, `
		INSERT INTO channels (id, source_id, identity_key, name, category_id, xmltv_id, stream_url, position, listed_at, logo)
			VALUES (40, 3, 'url:1', 'Alpha', 30, '', 'http://streams.example/1', 1, '2026-12-04T03:00:00Z', 'http://logos.example/alpha.png'),
			       (41, 3, 'url:2', 'Bravo', 30, '', 'http://streams.example/2', 2, '2026-12-04T03:00:00Z', 'http://logos.example/bravo.png'),
			       (42, 3, 'url:3', 'Charlie', 30, '', 'http://streams.example/3', 3, '2026-12-04T03:00:00Z', ''),
			       (43, 3, 'url:4', 'Delta', 30, '', 'http://streams.example/4', 4, '2026-12-04T03:00:00Z', 'http://logos.example/delta.png')`, `
		INSERT INTO lineup_groups (id, lineup_id, name) VALUES (50, 1, 'News')`, `
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 50)`, `
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number, pinned, hidden, rule_logo)
			VALUES (60, 1, 40, 1, 0, NULL, NULL), (61, 1, 41, 2, 0, NULL, 'http://rules.example/bravo.png'),
			       (62, 1, 42, 3, 0, NULL, NULL), (63, 1, 43, 4, 0, NULL, NULL)`, `
		INSERT INTO lineup_rules (lineup_id, position, spec)
			VALUES (1, 1, '{"match":{"name":"Bravo"},"action":"logo","logo":"http://rules.example/bravo.png"}')`, `
		INSERT INTO guides (id, name, address, host, position, added_at, generation, read_at, read_from, listed, channels, programmes)
			VALUES (7, 'extra', x'00', 'guide.example', 1, '2026-12-04T03:00:00Z', 4, '2026-12-04T03:00:00Z', 'def', 2, 0, 0)`, `
		INSERT INTO guide_channels (id, guide_id, xmltv_id, names, icon, listed_at, kept)
			VALUES (70, 7, 'charlie.example', 'Charlie', 'http://icons.example/charlie.png', '2026-12-04T03:00:00Z', 0),
			       (71, 7, 'delta.example', 'Delta', 'http://icons.example/delta.png', '2026-12-04T03:00:00Z', 0)`, `
		INSERT INTO channel_guide_matches (channel_id, guide_channel_id, by_name) VALUES (42, 70, 1), (43, 71, 1)`,
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
	s.UseKey(box)
	if v, err := s.SchemaVersion(ctx); err != nil || v < 50 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	want := map[string]string{"Alpha": "http://logos.example/alpha.png", "Bravo": "http://rules.example/bravo.png",
		"Charlie": "http://icons.example/charlie.png", "Delta": "http://logos.example/delta.png"}
	check := func(when string) {
		t.Helper()
		cs, err := s.LineupChannels(ctx, DefaultLineupID)
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, c := range cs {
			got[c.Name] = c.Logo
			e, err := s.LineupEntry(ctx, DefaultLineupID, c.EntryID)
			if err != nil || e.LogoByHand != nil {
				t.Errorf("%s: %s has a logo set by hand: %+v, %v", when, c.Name, e, err)
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: logos %v, want %v", when, got, want)
		}
		if src, err := s.Source(ctx, 3); err != nil || src.GuideLogos != GuideLogosNone {
			t.Errorf("%s: the source %+v, %v", when, src, err)
		}
		if g, err := s.Guide(ctx, 7); err != nil || g.Logos != GuideLogosFill {
			t.Errorf("%s: the guide %+v, %v", when, g, err)
		}
		if checked, _, err := s.LogoCounts(ctx); err != nil || checked != 0 {
			t.Errorf("%s: %d logos checked, %v", when, checked, err)
		}
	}
	check("after the upgrade")
	if _, err := s.ApplyCatalog(ctx, 3, []CatalogChannel{
		{IdentityKey: "url:1", Name: "Alpha", Category: "News", StreamURL: "http://streams.example/1", Logo: "http://logos.example/alpha.png"},
		{IdentityKey: "url:2", Name: "Bravo", Category: "News", StreamURL: "http://streams.example/2", Logo: "http://logos.example/bravo.png"},
		{IdentityKey: "url:3", Name: "Charlie", Category: "News", StreamURL: "http://streams.example/3"},
		{IdentityKey: "url:4", Name: "Delta", Category: "News", StreamURL: "http://streams.example/4", Logo: "http://logos.example/delta.png"},
	}, time.Date(2026, 12, 4, 15, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	check("after the next read")
	for _, bad := range []string{
		"UPDATE sources SET guide_logos = 'some'",
		"UPDATE guides SET logos = ''",
		"INSERT INTO logo_checks (address, loads, checked_at) VALUES ('x', 2, 'x')",
	} {
		if _, err := s.db.Exec(bad); err == nil {
			t.Errorf("allowed: %s", bad)
		}
	}
}
