// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Before Open changes the schema of a database that already holds a
// household's setup, it backs the database up (D117): a copy in the backups
// folder beside it, named for the time and for the schema it had, so that
// going back to an older version is a restore away (docs/upgrading.md). The
// newest few such copies are kept; copies that "overscan backup" makes are
// never touched.

// BackupsFolder is the folder beside the database that the copies made
// before upgrades go to. "overscan backup" writes to it too, when it is not
// given a file.
const BackupsFolder = "backups"

// KeepUpgradeBackups is how many of the copies made before upgrades are
// kept. Once a new copy is made, older ones beyond it are removed.
const KeepUpgradeBackups = 3

// upgradeBackupPrefix starts the name of every copy made before an upgrade.
// Names sort in the order the copies were made, since the time follows the
// prefix in a fixed width.
const upgradeBackupPrefix = "before-upgrade-"

// upgradeBackupName returns the name of the copy made at t of a database
// whose schema version is v.
func upgradeBackupName(t time.Time, v int) string {
	return fmt.Sprintf("%s%s-schema%d.db", upgradeBackupPrefix, t.UTC().Format("20060102-150405"), v)
}

// backupBeforeUpgrade copies the database at path, whose schema version is
// from, into the backups folder beside it, and removes the copies made
// before earlier upgrades beyond the newest KeepUpgradeBackups. It returns
// the copy's path. Nothing is removed unless the copy was made.
func backupBeforeUpgrade(ctx context.Context, path string, from int, now time.Time, log *slog.Logger) (string, error) {
	dir := filepath.Join(filepath.Dir(path), BackupsFolder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	name := upgradeBackupName(now, from)
	dest := filepath.Join(dir, name)
	// Processes upgrading the same database in the same second each keep a
	// copy of their own: Backup makes its file only if it is not there.
	for i := 2; ; i++ {
		_, err := Backup(ctx, path, dest)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || i > 100 {
			return "", err
		}
		dest = filepath.Join(dir, strings.TrimSuffix(name, ".db")+fmt.Sprintf("-%d.db", i))
	}
	for _, old := range oldUpgradeBackups(dir, KeepUpgradeBackups) {
		if err := os.Remove(filepath.Join(dir, old)); err != nil {
			log.Warn("cannot remove an old copy of the database made before an upgrade", "file", old, "error", err)
			continue
		}
		log.Info("removed an old copy of the database made before an upgrade", "file", old)
	}
	return dest, nil
}

// settle is how long a copy is left alone after it was last written: one
// that another process is still writing is never removed.
const settle = time.Minute

// oldUpgradeBackups returns the names of the copies made before upgrades in
// dir beyond the newest keep, oldest first, leaving out those written in
// the last minute.
func oldUpgradeBackups(dir string, keep int) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(name, upgradeBackupPrefix) && strings.HasSuffix(name, ".db") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) <= keep {
		return nil
	}
	var old []string
	for _, name := range names[:len(names)-keep] {
		if fi, err := os.Stat(filepath.Join(dir, name)); err == nil && time.Since(fi.ModTime()) >= settle {
			old = append(old, name)
		}
	}
	return old
}
