// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// Schema 31 gives each account a first profile, named after it, which
// takes the account's favourites and progress (D94).
func TestProfilesMigration(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	old, err := open(ctx, path, quiet(), ms[:30])
	if err != nil {
		t.Fatal(err)
	}
	old.UseKey(secrets.NewRandomBox())
	ids := addSources(t, old, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	// A channel and its entry, as schema 30 kept them: the store's code
	// of today writes columns that it did not have.
	if _, err := old.db.ExecContext(ctx, `
		INSERT INTO channels (source_id, identity_key, name, stream_url, position, listed_at)
		VALUES (?, 'a', 'Alpha', '', 1, '2026-12-04T10:00:00Z')`, ids["main"]); err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, `
		INSERT INTO lineup_entries (lineup_id, channel_id, number) SELECT 1, id, 1 FROM channels`); err != nil {
		t.Fatal(err)
	}
	// A film, as schema 30 kept it.
	if _, err := old.db.ExecContext(ctx, `
		INSERT INTO titles (source_id, kind, source_key, name, position, generation, added_at)
		VALUES (?, 'movie', '5001', 'The Long Field', 1, 1, ?)`, ids["main"], at.Unix()); err != nil {
		t.Fatal(err)
	}
	var entry, title int64
	if err := old.db.QueryRowContext(ctx, `SELECT id FROM lineup_entries`).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	if err := old.db.QueryRowContext(ctx, `SELECT id FROM titles ORDER BY id LIMIT 1`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO favourites (account_id, entry_id, added_at) VALUES (1, ?, '2026-12-04T10:00:00Z')`,
		`INSERT INTO progress (account_id, title_id, episode_id, position, duration, finished, watched_at) VALUES (1, ?, 0, 600, 6000, 0, 1796378400)`,
		`INSERT INTO devices (mac, state, account_id) VALUES ('00:1A:79:00:00:01', 'approved', 1) RETURNING ?`,
	} {
		arg := entry
		if strings.Contains(q, "progress") {
			arg = title
		}
		if _, err := old.db.ExecContext(ctx, q, arg); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s := openTest(t, path)
	profiles, err := s.Profiles(ctx, householdID)
	if err != nil || len(profiles) != 1 || profiles[0].Name != "Household" || !profiles[0].First || profiles[0].Devices != 1 ||
		profiles[0].PINHash != "" {
		t.Fatalf("profiles %+v, %v", profiles, err)
	}
	first := profiles[0].ID
	if got, err := s.Favourites(ctx, first); err != nil || !reflect.DeepEqual(got, []int64{entry}) {
		t.Errorf("favourites %v, %v", got, err)
	}
	if got, err := s.TitleProgress(ctx, first, title); err != nil || len(got) != 1 || got[0].Position != 600 {
		t.Errorf("progress %+v, %v", got, err)
	}
	d, err := s.Device(ctx, "00:1A:79:00:00:01")
	if err != nil || d.ProfileID != first {
		t.Errorf("device %+v, %v", d, err)
	}
}

func TestProfiles(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	profiles, err := s.Profiles(ctx, householdID)
	if err != nil || len(profiles) != 1 {
		t.Fatalf("profiles %+v, %v", profiles, err)
	}
	first := profiles[0]

	alex, err := s.AddProfile(ctx, householdID, "  Alex ")
	if err != nil || alex.Name != "Alex" || alex.First || alex.AccountID != householdID {
		t.Fatalf("add: %+v, %v", alex, err)
	}
	for _, name := range []string{"", "   ", "alex", "ALEX", strings.Repeat("x", MaxProfileName+1), "Tab\there"} {
		if _, err := s.AddProfile(ctx, householdID, name); !errors.Is(err, ErrProfileName) {
			t.Errorf("add %q: %v", name, err)
		}
	}
	sam, err := s.AddProfile(ctx, householdID, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RenameProfile(ctx, sam.ID, "alex"); !errors.Is(err, ErrProfileName) {
		t.Errorf("rename to another's name: %v", err)
	}
	if err := s.RenameProfile(ctx, sam.ID, "Sam B"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameProfile(ctx, 999, "Nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename a missing profile: %v", err)
	}
	if err := s.SetProfilePIN(ctx, alex.ID, "hash"); err != nil {
		t.Fatal(err)
	}
	profiles, err = s.Profiles(ctx, householdID)
	if err != nil || len(profiles) != 3 || profiles[0].ID != first.ID || profiles[1].PINHash != "hash" ||
		profiles[2].Name != "Sam B" {
		t.Fatalf("profiles %+v, %v", profiles, err)
	}

	// Each profile keeps its own favourites.
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCatalog(ctx, ids["main"], []CatalogChannel{{IdentityKey: "a", Name: "Alpha", StreamURL: "http://provider.example/a"},
		{IdentityKey: "b", Name: "Bravo", StreamURL: "http://provider.example/b"}}, at); err != nil {
		t.Fatal(err)
	}
	entries, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(entries) != 2 {
		t.Fatal(entries, err)
	}
	if err := s.SetFavourite(ctx, first.ID, entries[0].EntryID, true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFavourite(ctx, alex.ID, entries[1].EntryID, true, at); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Favourites(ctx, alex.ID); !reflect.DeepEqual(got, []int64{entries[1].EntryID}) {
		t.Errorf("Alex's favourites %v", got)
	}

	// A device plays as its account's first profile until it has one.
	d, err := s.SeeDevice(ctx, "00:1A:79:00:00:02", "MAG322", at)
	if err != nil {
		t.Fatal(err)
	}
	if d.ProfileID != 0 {
		t.Errorf("a pending device plays as %d", d.ProfileID)
	}
	if err := s.SetDeviceProfile(ctx, d.ID, alex.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a pending device given a profile: %v", err)
	}
	if d, err = s.ApproveDevice(ctx, d.MAC, householdID, "", at); err != nil || d.ProfileID != first.ID {
		t.Fatalf("approved: %+v, %v", d, err)
	}
	if err := s.SetDeviceProfile(ctx, d.ID, alex.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.Device(ctx, d.MAC); d.ProfileID != alex.ID {
		t.Errorf("device plays as %d, want %d", d.ProfileID, alex.ID)
	}
	if p, _ := s.Profile(ctx, alex.ID); p.Devices != 1 {
		t.Errorf("Alex's devices: %d", p.Devices)
	}
	if err := s.SetDeviceProfile(ctx, d.ID, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing profile: %v", err)
	}

	// Removing a profile takes its favourites, and its devices play as the
	// first again; the last one stays.
	if err := s.RemoveProfile(ctx, alex.ID); err != nil {
		t.Fatal(err)
	}
	if d, _ = s.Device(ctx, d.MAC); d.ProfileID != first.ID {
		t.Errorf("after removing its profile, the device plays as %d", d.ProfileID)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM favourites WHERE profile_id = ?`, alex.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d favourites left, %v", n, err)
	}
	if err := s.RemoveProfile(ctx, sam.ID); err != nil {
		t.Fatal(err)
	}
	// A profile added after another was removed never takes its ID.
	if kim, err := s.AddProfile(ctx, householdID, "Kim"); err != nil || kim.ID <= sam.ID {
		t.Errorf("a new profile has ID %d (%v), after %d", kim.ID, err, sam.ID)
	} else if err := s.RemoveProfile(ctx, kim.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveProfile(ctx, first.ID); !errors.Is(err, ErrLastProfile) {
		t.Errorf("remove the last profile: %v", err)
	}
	if err := s.RemoveProfile(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("remove a missing profile: %v", err)
	}
	if got, _ := s.Favourites(ctx, first.ID); !reflect.DeepEqual(got, []int64{entries[0].EntryID}) {
		t.Errorf("the first profile's favourites %v", got)
	}
}

func TestTooManyProfiles(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	for i := 1; i < MaxProfiles; i++ {
		if _, err := s.AddProfile(ctx, householdID, "P"+string(rune('a'+i))); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := s.AddProfile(ctx, householdID, "One more"); !errors.Is(err, ErrTooManyProfiles) {
		t.Errorf("one too many: %v", err)
	}
}

// A new account gets its first profile at once.
func TestNewAccountProfile(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	var id int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO accounts (name, lineup_id) VALUES ('Guests', 1) RETURNING id`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.Profiles(ctx, id)
	if err != nil || len(profiles) != 1 || profiles[0].Name != "Guests" || !profiles[0].First {
		t.Fatalf("profiles %+v, %v", profiles, err)
	}

	// A device moved to another account leaves its profile behind.
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	alex, err := s.AddProfile(ctx, householdID, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.ApproveDevice(ctx, "00:1A:79:00:00:03", householdID, "", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceProfile(ctx, d.ID, alex.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceProfile(ctx, d.ID, profiles[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a profile of another account: %v", err)
	}
	if d, err = s.ApproveDevice(ctx, d.MAC, id, "", at); err != nil || d.ProfileID != profiles[0].ID {
		t.Errorf("moved to Guests, it plays as %d (%v), want %d", d.ProfileID, err, profiles[0].ID)
	}
	if p, _ := s.Profile(ctx, alex.ID); p.Devices != 0 {
		t.Errorf("Alex still has %d devices", p.Devices)
	}
}

func TestCleanProfileName(t *testing.T) {
	for in, want := range map[string]string{" Alex ": "Alex", "Sam": "Sam", "Ünïcödé": "Ünïcödé"} {
		if got, err := CleanProfileName(in); err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", " ", "a\x00b", "line\nbreak", "\xff", strings.Repeat("é", MaxProfileName+1)} {
		if _, err := CleanProfileName(in); !errors.Is(err, ErrProfileName) {
			t.Errorf("%q: %v", in, err)
		}
	}
}
