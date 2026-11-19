// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
)

// testSource is a source's settings for a test: its name, how many streams
// it allows at once, and its numbering ("" for the default).
func testSource(name string, maxConnections int, numbers string) source.Settings {
	return source.Settings{Name: name, MaxConnections: maxConnections, Numbers: numbers}
}

// addSources adds sources for a test, at made-up addresses where they name
// none, and returns their IDs by name.
func addSources(t *testing.T, s *Store, specs ...source.Settings) map[string]int64 {
	t.Helper()
	ids := map[string]int64{}
	for _, spec := range specs {
		if spec.URL == "" {
			spec.URL = "http://provider.example/" + spec.Name + ".m3u"
		}
		id, err := s.AddSource(context.Background(), spec)
		if err != nil {
			t.Fatal(err)
		}
		ids[spec.Name] = id
	}
	return ids
}

func TestAddAndUpdateSources(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	const secret = "s3cret"
	id, err := s.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example:8080/get.php?username=u&password=" + secret,
		UserAgent: "TestPlayer/1.0", MaxConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.Source(ctx, id)
	if err != nil || src.Name != "main" || src.Type != source.TypeM3U || src.Host != "provider.example:8080" || src.UserAgent != "TestPlayer/1.0" ||
		src.MaxConnections != 2 || src.Refresh != source.DefaultRefresh || src.Numbers != NumbersPlaylist || !src.Enabled ||
		src.Address != AddressSaved {
		t.Fatalf("source %+v, %v", src, err)
	}
	settings, err := s.SourceSettings(ctx, id)
	if err != nil || settings.URL != "http://provider.example:8080/get.php?username=u&password="+secret || settings.Name != "main" {
		t.Fatalf("settings %+v, %v", settings, err)
	}
	var sealed []byte
	if err := s.db.QueryRow("SELECT secret FROM source_secrets WHERE source_id = ?", id).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte(secret)) {
		t.Errorf("stored secret: %d bytes, %v", len(sealed), err)
	}

	// Mistakes.
	if _, err := s.AddSource(ctx, source.Settings{Name: "main", URL: "/srv/main.m3u"}); !errors.Is(err, ErrSourceExists) {
		t.Errorf("a taken name: %v", err)
	}
	var bad *source.Error
	if _, err := s.AddSource(ctx, source.Settings{Name: "Bad Name", URL: "/srv/x.m3u"}); !errors.As(err, &bad) || bad.Field != "name" {
		t.Errorf("a bad name: %v", err)
	}
	if _, err := s.AddSource(ctx, source.Settings{Name: "x", URL: "ftp://provider.example/" + secret}); !errors.As(err, &bad) || bad.Field != "url" || strings.Contains(err.Error(), secret) {
		t.Errorf("a bad address: %v", err)
	}

	// An update without an address keeps the saved one; with one, the host
	// follows.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "renamed", MaxConnections: 0, Refresh: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if settings, err = s.SourceSettings(ctx, id); err != nil || settings.Name != "renamed" || settings.MaxConnections != 0 ||
		settings.Refresh != time.Hour || settings.UserAgent != "" || !strings.Contains(settings.URL, secret) {
		t.Errorf("after an update: %+v, %v", settings, err)
	}
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "renamed", URL: "/srv/playlists/main.m3u"}); err != nil {
		t.Fatal(err)
	}
	if src, _ = s.Source(ctx, id); src.Host != "file main.m3u" {
		t.Errorf("host %q", src.Host)
	}
	other := addSources(t, s, testSource("other", 1, ""))
	if err := s.UpdateSource(ctx, other["other"], source.Settings{Name: "renamed"}); !errors.Is(err, ErrSourceExists) {
		t.Errorf("a taken name: %v", err)
	}
	if err := s.UpdateSource(ctx, 9999, source.Settings{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown source: %v", err)
	}
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "renamed", Refresh: time.Minute}); !errors.As(err, &bad) || bad.Field != "refresh" {
		t.Errorf("a short refresh: %v", err)
	}
	if err := s.SetSourceEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if src, _ = s.Source(ctx, id); src.Enabled {
		t.Error("still enabled")
	}
	if err := s.SetSourceEnabled(ctx, 9999, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("enabling an unknown source: %v", err)
	}
}

func TestStalkerPortalSource(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	box := source.Device{MAC: "00:1A:79:00:00:51", Serial: "FICTIONAL0001", DeviceID: "made-up-1", Signature: "made-up-3"}
	id, err := s.AddSource(ctx, source.Settings{Name: "portal", Type: source.TypeStalker,
		URL: "http://portal.example/stalker_portal/c/", Device: box, Username: "viewer", Password: "s3cret", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	src, _ := s.Source(ctx, id)
	settings, err := s.SourceSettings(ctx, id)
	if err != nil || src.Host != "portal.example" || settings.Device != box || settings.Username != "viewer" ||
		settings.Password != "s3cret" || settings.URL != "http://portal.example/stalker_portal/c/" {
		t.Fatalf("settings %+v, %v; host %q", settings, err, src.Host)
	}
	var sealed []byte
	if err := s.db.QueryRow("SELECT secret FROM source_secrets WHERE source_id = ?", id).Scan(&sealed); err != nil ||
		bytes.Contains(sealed, []byte("00:1A:79")) || bytes.Contains(sealed, []byte("FICTIONAL")) {
		t.Errorf("stored secret: %d bytes, %v", len(sealed), err)
	}

	// What a change leaves out stays as saved, each on its own.
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "portal", Type: source.TypeStalker,
		Device: source.Device{Model: "MAG322"}, Refresh: time.Hour}); err != nil {
		t.Fatal(err)
	}
	want := box
	want.Model = "MAG322"
	if settings, err = s.SourceSettings(ctx, id); err != nil || settings.Device != want || settings.Password != "s3cret" ||
		settings.Refresh != time.Hour {
		t.Errorf("after a change: %+v, %v", settings, err)
	}
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "portal", Type: source.TypeStalker}); err != nil {
		t.Fatal(err)
	}
	if settings, _ = s.SourceSettings(ctx, id); settings.Device != want {
		t.Errorf("after a change of nothing: %+v", settings.Device)
	}
	var bad *source.Error
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "portal", Type: source.TypeStalker,
		Device: source.Device{MAC: "not a MAC"}}); !errors.As(err, &bad) || bad.Field != "mac" {
		t.Errorf("a bad MAC address: %v", err)
	}
	// A MAC address given describes the box again: what it leaves out is
	// none.
	again := source.Device{MAC: "00:1a:79:00:00:51", DeviceID: "made-up-4"}
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "portal", Type: source.TypeStalker, Device: again,
		Refresh: time.Hour}); err != nil {
		t.Fatal(err)
	}
	if settings, _ = s.SourceSettings(ctx, id); settings.Device != again || settings.Username != "viewer" {
		t.Errorf("after the box was entered again: %+v", settings)
	}

	// Another type of source becomes a portal with its address and MAC
	// address, and a portal another type with its own.
	ids := addSources(t, s, xtreamSource("main"))
	if err := s.UpdateSource(ctx, ids["main"], source.Settings{Name: "main", Type: source.TypeStalker}); !errors.As(err, &bad) {
		t.Errorf("a portal without its address: %v", err)
	}
	if err := s.UpdateSource(ctx, ids["main"], source.Settings{Name: "main", Type: source.TypeStalker,
		URL: "http://portal.example/c/", Device: source.Device{MAC: "00:1A:79:00:00:52"}}); err != nil {
		t.Errorf("an Xtream source made a portal: %v", err)
	}
	if err := s.UpdateSource(ctx, id, source.Settings{Name: "portal", Type: source.TypeM3U, URL: "/srv/portal.m3u"}); err != nil {
		t.Fatal(err)
	}
	if settings, _ = s.SourceSettings(ctx, id); settings.Device != (source.Device{}) || settings.Username != "" {
		t.Errorf("a portal made a playlist keeps %+v", settings)
	}
}

func TestSourceAddresses(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("main", 1, ""))

	// Saved with another key: it must be entered again.
	s.UseKey(secrets.NewRandomBox())
	if src, _ := s.Source(ctx, ids["main"]); src.Address != AddressLocked {
		t.Errorf("another key: %v", src.Address)
	}
	if _, err := s.SourceSettings(ctx, ids["main"]); !errors.Is(err, ErrUnreadable) {
		t.Errorf("another key: %v", err)
	}
	if err := s.UpdateSource(ctx, ids["main"], source.Settings{Name: "main", URL: "/srv/main.m3u"}); err != nil {
		t.Fatal(err)
	}
	if settings, err := s.SourceSettings(ctx, ids["main"]); err != nil || settings.URL != "/srv/main.m3u" {
		t.Errorf("entered again: %+v, %v", settings, err)
	}

	// Older versions kept none.
	if _, err := s.db.Exec("INSERT INTO sources (id, name, type) VALUES (50, 'old', 'm3u')"); err != nil {
		t.Fatal(err)
	}
	if src, _ := s.Source(ctx, 50); src.Address != AddressMissing {
		t.Errorf("none saved: %v", src.Address)
	}
	if _, err := s.SourceSettings(ctx, 50); !errors.Is(err, ErrNoAddress) {
		t.Errorf("none saved: %v", err)
	}
	if _, err := s.SourceSettings(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown source: %v", err)
	}
}

func TestDeleteSource(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	ids := addSources(t, s, testSource("more", 1, ""))
	apply(t, s, src, at, grouped("a/News", "b/Sports"))
	apply(t, s, ids["more"], at, grouped("c/News", "d/Films"))
	groups := groupsByName(t, s)
	if err := s.RenameLineupGroup(ctx, DefaultLineupID, groups["Films"].ID, "Cinema"); err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteSource(ctx, ids["more"]); err != nil {
		t.Fatal(err)
	}
	if got := lineupNumbers(t, s); len(got) != 2 || got["a"] == 0 || got["b"] == 0 {
		t.Errorf("lineup after the delete: %v", got)
	}
	groups = groupsByName(t, s)
	if _, ok := groups["Cinema"]; !ok {
		t.Error("the delete removed a group that the administrator changed")
	}
	if len(groups) != 3 {
		t.Errorf("groups after the delete: %v", groups)
	}
	for _, table := range []string{"source_secrets", "categories", "channels", "lineup_categories"} {
		var n int
		if err := s.db.QueryRow("SELECT count(*) FROM "+table+" WHERE source_id = ?", ids["more"]).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s keeps %d rows of the deleted source, %v", table, n, err)
		}
	}
	if err := s.DeleteSource(ctx, ids["more"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting again: %v", err)
	}
}

func TestImportSources(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	// A source that older versions recorded from the configuration, with a
	// channel, numbered by the playlist's own numbers.
	if _, err := s.db.Exec(`
		INSERT INTO sources (id, name, type, numbers) VALUES (7, 'old', 'm3u', 'provider');
		INSERT INTO channels (id, source_id, identity_key, name, number, stream_url, position, listed_at)
			VALUES (70, 7, 'a', 'Alpha', 500, 'http://provider.example/a', 1, '2026-11-14T03:00:00Z');
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number) VALUES (700, 1, 70, 500);`); err != nil {
		t.Fatal(err)
	}
	addSources(t, s, testSource("taken", 1, ""))
	specs := []source.Settings{
		{Name: "old", URL: "http://provider.example/old.m3u", MaxConnections: 3},
		{Name: "new", URL: "/srv/new.m3u", MaxConnections: 1, Numbers: NumbersProvider},
		{Name: "taken", URL: "/srv/taken.m3u", MaxConnections: 1},
	}
	got, err := s.ImportSources(ctx, specs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != (Import{"old", 7, Imported}) || got[1].Outcome != Imported || got[1].ID == 0 ||
		got[2] != (Import{"taken", 0, NameTaken}) {
		t.Fatalf("imports %+v", got)
	}
	old, err := s.SourceSettings(ctx, 7)
	if err != nil || old.URL != "http://provider.example/old.m3u" || old.MaxConnections != 3 || old.Numbers != NumbersPlaylist {
		t.Errorf("old %+v, %v", old, err)
	}
	// Its numbering changed, so its channel was numbered again, keeping its
	// entry.
	if got := lineupNumbers(t, s); got["Alpha"] != 1 {
		t.Errorf("numbers %v", got)
	}

	// Each is imported once, even after the admin deletes it.
	if err := s.DeleteSource(ctx, got[1].ID); err != nil {
		t.Fatal(err)
	}
	specs[0].URL = "http://provider.example/changed.m3u"
	again, err := s.ImportSources(ctx, specs)
	if err != nil || len(again) != 3 || again[0].Outcome != ImportedBefore || again[1].Outcome != ImportedBefore || again[2].Outcome != NameTaken {
		t.Fatalf("again %+v, %v", again, err)
	}
	if old, _ = s.SourceSettings(ctx, 7); old.URL != "http://provider.example/old.m3u" {
		t.Errorf("the second import changed the address: %+v", old)
	}
	if srcs, _ := s.Sources(ctx); len(srcs) != 2 {
		t.Errorf("sources %+v", srcs)
	}
	if _, err := s.ImportSources(ctx, []source.Settings{{Name: "bad name", URL: "/srv/x.m3u"}}); err == nil {
		t.Error("a bad source was imported")
	}
}

func TestUpgradeToSourcesInTheDatabase(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	old, err := open(ctx, path, quiet(), migrations[:9])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type, enabled, max_connections, numbers, new_categories_hidden, refreshed_at, refresh_error)
			VALUES (3, 'one', 'm3u', 1, 2, 'provider', 1, '2026-11-14T03:00:00Z', ''),
			       (4, 'gone', 'm3u', 0, 1, 'playlist', 0, NULL, 'connection refused');
		INSERT INTO categories (id, source_id, kind, name, position) VALUES (30, 3, 'live', 'News', 1);
		INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at)
			VALUES (31, 3, 'a', 'Alpha', 30, 'http://provider.example/a', 1, '2026-11-14T03:00:00Z');
		INSERT INTO lineup_groups (id, lineup_id, name) VALUES (30, 1, 'News');
		INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (1, 3, 'News', 30);
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number) VALUES (32, 1, 31, 1);`); err != nil {
		t.Fatal(err)
	}
	old.Close()

	s := openTest(t, path)
	srcs, err := s.Sources(ctx)
	if err != nil || len(srcs) != 2 {
		t.Fatalf("sources %+v, %v", srcs, err)
	}
	if one := srcs[1]; one.ID != 3 || one.Name != "one" || !one.Enabled || one.MaxConnections != 2 || one.Numbers != NumbersProvider ||
		!one.NewCategoriesHidden || one.RefreshedAt.IsZero() || one.Refresh != source.DefaultRefresh || one.Address != AddressMissing {
		t.Errorf("one %+v", one)
	}
	if gone := srcs[0]; gone.ID != 4 || gone.Enabled || gone.RefreshError != "connection refused" {
		t.Errorf("gone %+v", gone)
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(cs) != 1 || cs[0].EntryID != 32 || cs[0].Group != "News" {
		t.Errorf("lineup %+v, %v", cs, err)
	}
	// Foreign keys hold, and are checked again.
	rows, err := s.db.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("rows refer to rows that do not exist")
	}
	rows.Close()
	if _, err := s.db.Exec("INSERT INTO categories (source_id, kind, name, position) VALUES (99, 'live', 'X', 1)"); err == nil {
		t.Error("a category of no source was taken")
	}
}

// groupsByName returns the default lineup's groups by name.
func groupsByName(t *testing.T, s *Store) map[string]LineupGroup {
	t.Helper()
	gs, err := s.LineupGroups(context.Background(), DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]LineupGroup{}
	for _, g := range gs {
		out[g.Name] = g
	}
	return out
}
