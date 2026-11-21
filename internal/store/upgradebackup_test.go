// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// oldDatabase makes a database at path with the schema of the first n
// migrations and one row of its own, and closes it.
func oldDatabase(t *testing.T, path string, n int) {
	t.Helper()
	ctx := context.Background()
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	old, err := open(ctx, path, quiet(), ms[:n])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.ExecContext(ctx, `UPDATE accounts SET name = 'Kept' WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
}

func backupNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestOpenBacksUpBeforeUpgrading(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	oldDatabase(t, path, 41)

	s := openTest(t, path)
	if v, err := s.SchemaVersion(ctx); err != nil || v != len(migrations) {
		t.Fatalf("schema %d, %v", v, err)
	}
	dir := filepath.Join(filepath.Dir(path), BackupsFolder)
	names := backupNames(t, dir)
	if len(names) != 1 || !strings.HasPrefix(names[0], upgradeBackupPrefix) || !strings.HasSuffix(names[0], "-schema41.db") {
		t.Fatalf("backups %v, want one copy at schema 41", names)
	}
	copyPath := filepath.Join(dir, names[0])
	for p, want := range map[string]os.FileMode{dir: 0o700, copyPath: 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", p, fi.Mode().Perm(), err, want)
		}
	}
	copied, err := sql.Open("sqlite3", "file:"+copyPath+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer copied.Close()
	var version int
	var name string
	if err := copied.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil || version != 41 {
		t.Errorf("the copy's schema is %d, %v; want 41", version, err)
	}
	if err := copied.QueryRowContext(ctx, "SELECT name FROM accounts WHERE id = 1").Scan(&name); err != nil || name != "Kept" {
		t.Errorf("the copy's account is %q, %v", name, err)
	}

	// Opening it again changes nothing, so it makes no copy.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	openTest(t, path)
	if got := backupNames(t, dir); !slices.Equal(got, names) {
		t.Errorf("backups after opening again: %v", got)
	}
}

func TestNewDatabaseIsNotBackedUp(t *testing.T) {
	path := newPath(t)
	openTest(t, path)
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), BackupsFolder)); !os.IsNotExist(err) {
		t.Errorf("a new database made a backups folder: %v", err)
	}
}

func TestFailedBackupLeavesDatabaseAsItWas(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	oldDatabase(t, path, 41)
	// A file where the folder would go stops the backup.
	if err := os.WriteFile(filepath.Join(filepath.Dir(path), BackupsFolder), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, path, quiet())
	if err == nil {
		_ = s.Close()
		t.Fatal("Open upgraded a database it could not back up")
	}
	if !strings.Contains(err.Error(), "backup failed") || !strings.Contains(err.Error(), "schema 41") {
		t.Errorf("error %q does not say that the backup of schema 41 failed", err)
	}
	ms, err := loadMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	old, err := open(ctx, path, quiet(), ms[:41])
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if v, err := old.SchemaVersion(ctx); err != nil || v != 41 {
		t.Errorf("schema %d, %v; want 41", v, err)
	}
}

func TestOldUpgradeBackupsAreRemoved(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	oldDatabase(t, path, 41)
	dir := filepath.Join(filepath.Dir(path), BackupsFolder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	kept := []string{
		"overscan-20261201-100000.db", // made by "overscan backup"
		"before-upgrade-notes.txt",
	}
	for _, name := range append([]string{
		upgradeBackupName(at.Add(-72*time.Hour), 30),
		upgradeBackupName(at.Add(-48*time.Hour), 35),
		upgradeBackupName(at.Add(-24*time.Hour), 40),
	}, kept...) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// Copies are removed only once they have settled.
		hourAgo := time.Now().Add(-time.Hour)
		if err := os.Chtimes(filepath.Join(dir, name), hourAgo, hourAgo); err != nil {
			t.Fatal(err)
		}
	}

	dest, err := backupBeforeUpgrade(ctx, path, 41, at, quiet())
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{
		upgradeBackupName(at.Add(-48*time.Hour), 35),
		upgradeBackupName(at.Add(-24*time.Hour), 40),
		filepath.Base(dest),
	}, kept...)
	slices.Sort(want)
	if got := backupNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("backups %v, want %v", got, want)
	}

	// A second copy in the same second gets a name of its own, and a copy
	// written in the last minute is never removed, since another process
	// may be writing it.
	again, err := backupBeforeUpgrade(ctx, path, 41, at, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if again == dest || !strings.HasSuffix(again, "-schema41-2.db") {
		t.Errorf("second copy %s, first %s", again, dest)
	}
	want = append(slices.DeleteFunc(want, func(n string) bool { return n == upgradeBackupName(at.Add(-48*time.Hour), 35) }), filepath.Base(again))
	slices.Sort(want)
	if got := backupNames(t, dir); !slices.Equal(got, want) {
		t.Errorf("backups %v, want %v", got, want)
	}
}

// Processes that upgrade one database at once each make a copy of their
// own, even in the same second.
func TestConcurrentUpgradeBackups(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	oldDatabase(t, path, 41)
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	errs := make([]error, 4)
	dests := make([]string, 4)
	var wg sync.WaitGroup
	for i := range errs {
		wg.Go(func() { dests[i], errs[i] = backupBeforeUpgrade(ctx, path, 41, at, quiet()) })
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("copy %d: %v", i, err)
		}
	}
	slices.Sort(dests)
	if len(slices.Compact(dests)) != len(errs) {
		t.Errorf("copies %v share names", dests)
	}
	// None was removed, though they are more than the three kept: each was
	// written in the last minute, maybe while another process wrote its own.
	for _, d := range dests {
		if _, err := os.Stat(d); err != nil {
			t.Error(err)
		}
	}
}
