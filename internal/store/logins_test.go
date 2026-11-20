// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// Schema 36 leaves the household's account as it was: no login, no token,
// no limit and no output (D102).
func TestLoginsMigration(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	old, err := open(ctx, path, quiet(), ms[:35])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, `UPDATE accounts SET pin_hash = 'kept', expires_at = '2026-12-04T00:00:00Z' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	s := openTest(t, path)
	a, err := s.Account(ctx, HouseholdAccount)
	if err != nil {
		t.Fatal(err)
	}
	if a.Username != "" || a.PasswordHash != "" || a.HasToken || a.MaxStreams != 0 || a.Outputs != (Outputs{}) {
		t.Errorf("the household's account after the migration: %+v", a)
	}
	if a.PINHash != "kept" || a.ExpiresAt.IsZero() || a.Name != "Household" || a.LineupID != 1 {
		t.Errorf("the household's account lost what it had: %+v", a)
	}
}

func TestAddAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	if err := s.SetAccountPIN(ctx, HouseholdAccount, "pin-hash"); err != nil {
		t.Fatal(err)
	}
	alex, err := s.AddAccount(ctx, "  Alex ")
	if err != nil {
		t.Fatal(err)
	}
	if alex.Name != "Alex" || alex.LineupID != 1 || alex.PINHash != "pin-hash" || alex.Username != "" || alex.HasToken {
		t.Errorf("new account: %+v", alex)
	}
	// It has a first profile, named after it.
	ps, err := s.Profiles(ctx, alex.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].Name != "Alex" || !ps[0].First {
		t.Errorf("its profiles: %+v", ps)
	}
	for _, name := range []string{"alex", "", "   ", "a\x07b", string(make([]rune, MaxAccountName+1))} {
		if _, err := s.AddAccount(ctx, name); !errors.Is(err, ErrAccountName) {
			t.Errorf("AddAccount(%q): %v, want ErrAccountName", name, err)
		}
	}
	if err := s.RenameAccount(ctx, alex.ID, "household"); !errors.Is(err, ErrAccountName) {
		t.Errorf("renamed to another's name: %v", err)
	}
	if err := s.RenameAccount(ctx, alex.ID, "Alex and Sam"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameAccount(ctx, 99, "Nobody"); !errors.Is(err, ErrNotFound) {
		t.Errorf("renamed a missing account: %v", err)
	}
	for i := 2; i < MaxAccounts; i++ {
		if _, err := s.AddAccount(ctx, "Account "+string(rune('A'+i%26))+string(rune('a'+i/26))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddAccount(ctx, "One too many"); !errors.Is(err, ErrTooManyAccounts) {
		t.Errorf("the account after the last: %v", err)
	}
}

func TestRemoveAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	sam, err := s.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApproveDevice(ctx, "00:1A:79:00:00:01", sam.ID, "Den", at); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAccount(ctx, HouseholdAccount); !errors.Is(err, ErrHousehold) {
		t.Errorf("removed the household's: %v", err)
	}
	if err := s.RemoveAccount(ctx, sam.ID); !errors.Is(err, ErrAccountInUse) {
		t.Errorf("removed an account with a device: %v", err)
	}
	if _, err := s.BlockDevice(ctx, "00:1A:79:00:00:01"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveAccount(ctx, sam.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Account(ctx, sam.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the account is still there: %v", err)
	}
	d, err := s.Device(ctx, "00:1A:79:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	if d.AccountID != 0 || d.State != DeviceBlocked {
		t.Errorf("the blocked device: %+v", d)
	}
	if err := s.RemoveAccount(ctx, sam.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removed it twice: %v", err)
	}
}

func TestAccountLogin(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	alex, err := s.AddAccount(ctx, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLogin(ctx, alex.ID, "alex", ""); !errors.Is(err, ErrNoPassword) {
		t.Errorf("a login without a password: %v", err)
	}
	for _, u := range []string{"", "a b", "a/b", "a%20", "ä", "a?b", string(make([]byte, MaxUsername+1))} {
		if err := s.SetAccountLogin(ctx, alex.ID, u, "hash"); !errors.Is(err, ErrUsername) {
			t.Errorf("username %q: %v, want ErrUsername", u, err)
		}
	}
	if err := s.SetAccountLogin(ctx, alex.ID, " alex.b-2@home_1 ", "hash-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLogin(ctx, HouseholdAccount, "ALEX.B-2@HOME_1", "hash-2"); !errors.Is(err, ErrUsernameTaken) {
		t.Errorf("another account's username: %v", err)
	}
	got, err := s.AccountByUsername(ctx, "ALEX.b-2@home_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != alex.ID || got.Username != "alex.b-2@home_1" || got.PasswordHash != "hash-1" {
		t.Errorf("by username: %+v", got)
	}
	// A new username keeps the password.
	if err := s.SetAccountLogin(ctx, alex.ID, "alex", ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Account(ctx, alex.ID); got.Username != "alex" || got.PasswordHash != "hash-1" {
		t.Errorf("after a new username: %+v", got)
	}
	if err := s.RemoveAccountLogin(ctx, alex.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountByUsername(ctx, "alex"); !errors.Is(err, ErrNotFound) {
		t.Errorf("found by a removed login: %v", err)
	}
	if got, _ := s.Account(ctx, alex.ID); got.Username != "" || got.PasswordHash != "" {
		t.Errorf("after removing the login: %+v", got)
	}
	if err := s.SetAccountLogin(ctx, 99, "nobody", "hash"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing account's login: %v", err)
	}
}

func TestAccountToken(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	if _, err := s.AccountToken(ctx, HouseholdAccount); !errors.Is(err, ErrNotFound) {
		t.Errorf("a token before any: %v", err)
	}
	first, err := s.NewAccountToken(ctx, HouseholdAccount)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 43 {
		t.Errorf("token %q: %d characters, want 43", first, len(first))
	}
	if got, err := s.AccountToken(ctx, HouseholdAccount); err != nil || got != first {
		t.Errorf("AccountToken = %q, %v; want %q", got, err, first)
	}
	if a, err := s.AccountByToken(ctx, first); err != nil || a.ID != HouseholdAccount || !a.HasToken {
		t.Errorf("by token: %+v, %v", a, err)
	}
	second, err := s.NewAccountToken(ctx, HouseholdAccount)
	if err != nil || second == first {
		t.Fatalf("a new token: %q, %v", second, err)
	}
	if _, err := s.AccountByToken(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old token still works: %v", err)
	}
	if _, err := s.AccountByToken(ctx, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("an empty token: %v", err)
	}
	// What the database keeps is neither the token nor anything that finds
	// it without the key.
	var hash string
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, `SELECT token_hash, token_sealed FROM accounts WHERE id = 1`).Scan(&hash, &sealed); err != nil {
		t.Fatal(err)
	}
	if hash == second || string(sealed) == second {
		t.Error("the database keeps the token as it is")
	}
	if err := s.RemoveAccountToken(ctx, HouseholdAccount); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AccountByToken(ctx, second); !errors.Is(err, ErrNotFound) {
		t.Errorf("a removed token still works: %v", err)
	}
	// Without the key, no token is made.
	bare, err := Open(ctx, newPath(t), quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer bare.Close()
	if _, err := bare.NewAccountToken(ctx, HouseholdAccount); !errors.Is(err, ErrNoKey) {
		t.Errorf("a token without a key: %v", err)
	}
	// Another key cannot open it.
	other := openTest(t, newPath(t))
	if _, err := other.NewAccountToken(ctx, HouseholdAccount); err != nil {
		t.Fatal(err)
	}
	other.UseKey(secrets.NewRandomBox())
	if _, err := other.AccountToken(ctx, HouseholdAccount); !errors.Is(err, ErrUnreadable) {
		t.Errorf("a token under another key: %v", err)
	}
}

func TestAccountStreamsAndOutputs(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	for _, n := range []int{-1, MaxAccountStreams + 1} {
		if err := s.SetAccountStreams(ctx, HouseholdAccount, n); !errors.Is(err, ErrAccountStreams) {
			t.Errorf("SetAccountStreams(%d): %v", n, err)
		}
	}
	if err := s.SetAccountStreams(ctx, HouseholdAccount, 2); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountOutputs(ctx, HouseholdAccount, Outputs{M3U: true}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Account(ctx, HouseholdAccount)
	if err != nil {
		t.Fatal(err)
	}
	if a.MaxStreams != 2 || !a.Outputs.M3U || a.Outputs.Xtream {
		t.Errorf("account: %+v", a)
	}
	if n, err := s.AccountStreams(ctx, HouseholdAccount); err != nil || n != 2 {
		t.Errorf("AccountStreams = %d, %v", n, err)
	}
	if err := s.SetAccountStreams(ctx, 99, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing account's limit: %v", err)
	}
	if err := s.SetAccountOutputs(ctx, 99, Outputs{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing account's outputs: %v", err)
	}
}

func TestDeviceAccount(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	sam, err := s.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountStreams(ctx, sam.ID, 3); err != nil {
		t.Fatal(err)
	}
	guest, err := s.AddProfile(ctx, HouseholdAccount, "Guest")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.ApproveDevice(ctx, "00:1A:79:00:00:02", HouseholdAccount, "", at)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDeviceProfile(ctx, d.ID, guest.ID); err != nil {
		t.Fatal(err)
	}
	if acct, limit, err := s.StreamsOf(ctx, d.ID); err != nil || acct != HouseholdAccount || limit != 0 {
		t.Errorf("StreamsOf = %d, %d, %v", acct, limit, err)
	}
	moved, err := s.SetDeviceAccount(ctx, "00:1a:79:00:00:02", sam.ID)
	if err != nil {
		t.Fatal(err)
	}
	ps, _ := s.Profiles(ctx, sam.ID)
	if moved.AccountID != sam.ID || moved.ProfileID != ps[0].ID {
		t.Errorf("moved device: %+v, Sam's profiles %+v", moved, ps)
	}
	if acct, limit, err := s.StreamsOf(ctx, d.ID); err != nil || acct != sam.ID || limit != 3 {
		t.Errorf("StreamsOf after the move = %d, %d, %v", acct, limit, err)
	}
	if _, err := s.SetDeviceAccount(ctx, "00:1A:79:00:00:02", 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("moved to a missing account: %v", err)
	}
	if _, err := s.SeeDevice(ctx, "00:1A:79:00:00:03", "MAG322", at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDeviceAccount(ctx, "00:1A:79:00:00:03", sam.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("moved a pending device: %v", err)
	}
	pending, _ := s.Device(ctx, "00:1A:79:00:00:03")
	if acct, limit, err := s.StreamsOf(ctx, pending.ID); err != nil || acct != 0 || limit != 0 {
		t.Errorf("a pending device's StreamsOf = %d, %d, %v", acct, limit, err)
	}
}
