// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(context.Background(), path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	s.UseKey(secrets.NewRandomBox())
	return s
}

func newPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), FileName)
}

func TestOpenCreatesDatabase(t *testing.T) {
	path := newPath(t)
	s := openTest(t, path)
	ctx := context.Background()

	v, err := s.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != len(migrations) {
		t.Errorf("schema version %d, want %d", v, len(migrations))
	}

	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Errorf("journal mode %q, want wal", mode)
	}
	var fk int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
		t.Fatal(err)
	}
	if fk != 1 {
		t.Errorf("foreign keys %d, want 1", fk)
	}
}

func TestFilesAreOwnerOnly(t *testing.T) {
	path := newPath(t)
	openTest(t, path) // migrations write, so the WAL file exists while it is open
	for _, p := range []string{path, path + "-wal"} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has mode %o, want 600", filepath.Base(p), perm)
		}
	}
}

func TestHouseholdAccountAndLineup(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()

	a, err := s.Account(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "Household" || a.LineupID != 1 || !a.ExpiresAt.IsZero() {
		t.Errorf("account %+v", a)
	}
	if time.Since(a.CreatedAt) > time.Hour || a.CreatedAt.Location() != time.UTC {
		t.Errorf("account created at %v", a.CreatedAt)
	}

	l, err := s.Lineup(ctx, a.LineupID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "Default" {
		t.Errorf("lineup %+v", l)
	}
}

func TestAccountExpiry(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	want := time.Date(2027, 1, 31, 23, 0, 0, 0, time.UTC)
	if err := s.SetAccountExpiry(ctx, 1, want.In(time.FixedZone("UTC-5", -5*3600))); err != nil {
		t.Fatal(err)
	}
	a, err := s.Account(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !a.ExpiresAt.Equal(want) {
		t.Errorf("expires at %v, want %v", a.ExpiresAt, want)
	}

	if err := s.SetAccountExpiry(ctx, 1, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if a, err = s.Account(ctx, 1); err != nil || !a.ExpiresAt.IsZero() {
		t.Errorf("after clearing: %v, %v", a.ExpiresAt, err)
	}
	if err := s.SetAccountExpiry(ctx, 42, want); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing account: %v", err)
	}
}

func TestNotFound(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.Account(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("account: %v, want ErrNotFound", err)
	}
	if _, err := s.Lineup(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("lineup: %v, want ErrNotFound", err)
	}
}

func TestReopenKeepsData(t *testing.T) {
	path := newPath(t)
	ctx := context.Background()
	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "INSERT INTO lineups (name) VALUES ('Kids')"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s = openTest(t, path)
	var lineups, accounts int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM lineups").Scan(&lineups); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if lineups != 2 || accounts != 1 {
		t.Errorf("%d lineups and %d accounts after reopening, want 2 and 1", lineups, accounts)
	}
}

func TestForeignKeysAreEnforced(t *testing.T) {
	s := openTest(t, newPath(t))
	_, err := s.db.ExecContext(context.Background(), "INSERT INTO accounts (name, lineup_id) VALUES ('Guests', 42)")
	if err == nil {
		t.Error("an account with a missing lineup was accepted")
	}
}

func TestNewerSchemaIsRefused(t *testing.T) {
	path := newPath(t)
	ctx := context.Background()
	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "PRAGMA user_version = 99"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(ctx, path, quiet())
	if err == nil {
		_ = s.Close()
		t.Fatal("opened a database with a newer schema")
	}
	if !strings.Contains(err.Error(), "newer") {
		t.Errorf("error %q does not say the schema is newer", err)
	}
}

func TestFailedMigrationLeavesDatabaseAsItWas(t *testing.T) {
	path := newPath(t)
	ctx := context.Background()
	ms := []migration{
		{version: 1, name: "0001_one.sql", sql: "CREATE TABLE one (x INTEGER) STRICT;"},
		{version: 2, name: "0002_two.sql", sql: "CREATE TABLE two (x INTEGER) STRICT; INSERT INTO missing VALUES (1);"},
	}
	if s, err := open(ctx, path, quiet(), ms); err == nil {
		_ = s.Close()
		t.Fatal("a failing migration did not fail Open")
	} else if !strings.Contains(err.Error(), "0002_two.sql") {
		t.Errorf("error %q does not name the migration", err)
	}

	s, err := open(ctx, path, quiet(), ms[:1])
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v != 1 {
		t.Errorf("schema version %d, want 1", v)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_schema WHERE name = 'two'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Error("the failed migration's table exists")
	}
}

func TestConcurrentOpensMigrateOnce(t *testing.T) {
	path := newPath(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Go(func() {
			s, err := Open(ctx, path, quiet())
			if err == nil {
				err = s.Close()
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	s := openTest(t, path)
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM accounts").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("%d accounts, want 1", n)
	}
}

func TestEmbeddedMigrationsLoad(t *testing.T) {
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for i, m := range ms {
		if m.version != i+1 || strings.TrimSpace(m.sql) == "" {
			t.Errorf("migration %d: %+v", i, m)
		}
	}
}

func TestLoadMigrationsRejectsBadSets(t *testing.T) {
	file := &fstest.MapFile{Data: []byte("SELECT 1;")}
	for name, files := range map[string]fstest.MapFS{
		"gap":       {"migrations/0001_a.sql": file, "migrations/0003_c.sql": file},
		"duplicate": {"migrations/0001_a.sql": file, "migrations/0001_b.sql": file},
		"no number": {"migrations/first.sql": file},
		"short":     {"migrations/1_a.sql": file},
		"not sql":   {"migrations/0001_a.txt": file},
		"dot file":  {"migrations/.keep": file},
		"empty":     {"migrations": &fstest.MapFile{Mode: fs.ModeDir}},
	} {
		if _, err := loadMigrations(files); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestAccounts(t *testing.T) {
	s := openTest(t, newPath(t))
	accounts, err := s.Accounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].ID != 1 || accounts[0].Name != "Household" {
		t.Errorf("got %+v, want the household account", accounts)
	}
}

func TestRebuildingAReferredTable(t *testing.T) {
	ctx := context.Background()
	parent := migration{version: 1, name: "0001_parent.sql", sql: `
		CREATE TABLE parent (id INTEGER PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('a'))) STRICT;
		CREATE TABLE child (id INTEGER PRIMARY KEY, parent_id INTEGER NOT NULL REFERENCES parent (id) ON DELETE CASCADE) STRICT;
		INSERT INTO parent VALUES (1, 'a');
		INSERT INTO child VALUES (10, 1);`}
	rebuild := func(extra string) migration {
		return migration{version: 2, name: "0002_rebuild.sql", sql: `-- A new check on kind.
` + rebuildMarker + `
CREATE TABLE parent_new (id INTEGER PRIMARY KEY, kind TEXT NOT NULL CHECK (kind IN ('a', 'b'))) STRICT;
INSERT INTO parent_new SELECT id, kind FROM parent ` + extra + `;
DROP TABLE parent;
ALTER TABLE parent_new RENAME TO parent;
`}
	}
	count := func(s *Store, table string) int {
		var n int
		if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// With the marker, the rows that refer to the rebuilt table stay, and
	// the pool's connections still check foreign keys afterwards.
	path := newPath(t)
	s, err := open(ctx, path, quiet(), []migration{parent, rebuild("")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if count(s, "child") != 1 {
		t.Error("the rebuild lost the rows that refer to the table")
	}
	for range maxOpenConns + 1 {
		if _, err := s.db.ExecContext(ctx, "INSERT INTO child VALUES (11, 99)"); err == nil {
			t.Fatal("a row that refers to nothing was taken after the migration")
		}
	}

	// A rebuild that leaves rows referring to nothing is not committed.
	path = newPath(t)
	old, err := open(ctx, path, quiet(), []migration{parent})
	if err != nil {
		t.Fatal(err)
	}
	old.Close()
	if _, err := open(ctx, path, quiet(), []migration{parent, rebuild("WHERE id <> 1")}); err == nil || !strings.Contains(err.Error(), "refer to rows that do not exist") {
		t.Fatalf("a broken rebuild: %v", err)
	}
	s, err = open(ctx, path, quiet(), []migration{parent})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if v, _ := s.SchemaVersion(ctx); v != 1 || count(s, "parent") != 1 || count(s, "child") != 1 {
		t.Errorf("after a broken rebuild: schema %d", v)
	}
}
