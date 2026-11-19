// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestBackup(t *testing.T) {
	path := newPath(t)
	s := openTest(t, path)
	src := addSources(t, s, testSource("main", 1, NumbersPlaylist))["main"]
	at := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sports"))
	before, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}

	// The server's store stays open while the copy is made.
	dir := t.TempDir()
	dest := filepath.Join(dir, "copy.db")
	version, err := Backup(ctx, path, dest)
	if err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Errorf("schema %d, want %d", version, len(migrations))
	}
	fi, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode())
	}
	if names := dirNames(t, dir); !slices.Equal(names, []string{"copy.db"}) {
		t.Errorf("the backup's folder holds %v", names)
	}

	restored, err := Open(ctx, dest, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	after, err := restored.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) || after[0].Name != before[0].Name || after[2].Number != before[2].Number {
		t.Errorf("restored %+v, want %+v", after, before)
	}

	// A backup never overwrites a file, and needs a database.
	if _, err := Backup(ctx, path, dest); err == nil {
		t.Error("a backup overwrote a file")
	}
	if _, err := Backup(ctx, filepath.Join(dir, "absent.db"), filepath.Join(dir, "other.db")); err == nil {
		t.Error("a backup of no database")
	}
	if _, err := os.Stat(filepath.Join(dir, "absent.db")); err == nil {
		t.Error("a backup made the database it was to copy")
	}
}

func dirNames(t *testing.T, dir string) []string {
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
