// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
)

// TestMigration0043SourcePublicOnly upgrades a database of schema 42 with
// sources, a lineup and an account that uses one of them, and checks that
// every source is as it was and none is kept to public addresses (D121).
func TestMigration0043SourcePublicOnly(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	box := secrets.NewRandomBox()
	old, err := open(ctx, path, quiet(), migrations[:42])
	if err != nil {
		t.Fatal(err)
	}
	old.UseKey(box)
	if v, err := old.SchemaVersion(ctx); err != nil || v != 42 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, user_agent, refresh_seconds, numbers,
				catchup_zone, new_categories_hidden, refreshed_at, refresh_error)
			VALUES (3, 'main', 'xtream', 'provider.example', 1, 2, 'TestPlayer/1.0', 43200, 'provider',
				'Europe/London', 1, '2026-12-04T03:00:00Z', ''),
			       (4, 'films', 'folder', '/srv/films', 0, 0, '', 21600, 'playlist', '', 0, NULL, 'the folder does not exist');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at)
			VALUES (31, 3, 'a', 'Alpha', 30, 'http://provider.example/live/a.ts', 1, '2026-12-04T03:00:00Z');
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number) VALUES (32, 1, 31, 7);
		INSERT INTO accounts (id, name, lineup_id) VALUES (5, 'alex', 1);
		UPDATE accounts SET sources_limited = 1 WHERE id = 5;
		INSERT INTO account_sources (account_id, source_id) VALUES (5, 3);`); err != nil {
		t.Fatal(err)
	}
	for id, secret := range map[int64]sourceSecret{
		3: {URL: "http://provider.example:8080", Username: "alex", Password: "correct-horse"},
		4: {URL: "/srv/films"},
	} {
		sealed, err := old.sealSource(id, secret)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := old.db.Exec("INSERT INTO source_secrets (source_id, secret) VALUES (?, ?)", id, sealed); err != nil {
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
	if v, err := s.SchemaVersion(ctx); err != nil || v < 43 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	copies, err := filepath.Glob(filepath.Join(filepath.Dir(path), BackupsFolder, "before-upgrade-*-schema42.db"))
	if err != nil || len(copies) != 1 {
		t.Errorf("the copy before the upgrade: %v, %v", copies, err)
	}

	srcs, err := s.Sources(ctx)
	if err != nil || len(srcs) != 2 {
		t.Fatalf("sources %+v, %v", srcs, err)
	}
	films, main := srcs[0], srcs[1]
	if main.ID != 3 || main.Type != source.TypeXtream || main.Host != "provider.example" || !main.Enabled ||
		main.MaxConnections != 2 || main.UserAgent != "TestPlayer/1.0" || main.Refresh != 12*time.Hour ||
		main.Numbers != NumbersProvider || main.CatchupZone != "Europe/London" || !main.NewCategoriesHidden ||
		main.RefreshedAt.IsZero() || main.Address != AddressSaved || main.Channels != 1 || main.PublicOnly {
		t.Errorf("main %+v", main)
	}
	if films.ID != 4 || films.Enabled || films.RefreshError != "the folder does not exist" || films.PublicOnly {
		t.Errorf("films %+v", films)
	}
	settings, err := s.SourceSettings(ctx, 3)
	if err != nil || settings.URL != "http://provider.example:8080" || settings.Username != "alex" ||
		settings.Password != "correct-horse" || settings.PublicOnly {
		t.Errorf("main's settings %+v, %v", settings, err)
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(cs) != 1 || cs[0].EntryID != 32 || cs[0].Number != 7 {
		t.Errorf("lineup %+v, %v", cs, err)
	}
	acct, err := s.Account(ctx, 5)
	if err != nil || !acct.Sources.Only || len(acct.Sources.IDs) != 1 || acct.Sources.IDs[0] != 3 {
		t.Errorf("alex %+v, %v", acct, err)
	}

	// The setting is kept, and a folder never has it.
	settings.PublicOnly = true
	if err := s.UpdateSource(ctx, 3, settings); err != nil {
		t.Fatal(err)
	}
	if got, err := s.SourceSettings(ctx, 3); err != nil || !got.PublicOnly {
		t.Errorf("after turning it on: %+v, %v", got, err)
	}
	folder, err := s.SourceSettings(ctx, 4)
	if err != nil {
		t.Fatal(err)
	}
	folder.PublicOnly = true
	if err := s.UpdateSource(ctx, 4, folder); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Source(ctx, 4); err != nil || got.PublicOnly {
		t.Errorf("a folder kept to public addresses: %+v, %v", got, err)
	}
	if _, err := s.db.Exec("UPDATE sources SET public_only = 2 WHERE id = 3"); err == nil ||
		!strings.Contains(err.Error(), "CHECK") {
		t.Errorf("public_only = 2: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}
