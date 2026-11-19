// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
)

// Backup copies the database at path to dest, a new file readable by its
// owner only, and returns the copy's schema version. The copy is a
// consistent snapshot, which a running server may go on writing to
// meanwhile, compacted as VACUUM compacts. The database is not migrated:
// the copy has the schema that the database has.
func Backup(ctx context.Context, path, dest string) (int, error) {
	fail := func(err error) (int, error) { return 0, fmt.Errorf("store: back up %s: %w", path, err) }
	if _, err := os.Stat(path); err != nil {
		return fail(err)
	}
	// VACUUM INTO writes into a file that does not exist or is empty; one
	// made here first has the right mode.
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		return fail(err)
	}
	version, err := vacuumInto(ctx, path, dest)
	if err != nil {
		return fail(errors.Join(err, os.Remove(dest)))
	}
	return version, nil
}

func vacuumInto(ctx context.Context, path, dest string) (int, error) {
	db, err := sql.Open("sqlite3", dataSourceName(path))
	if err != nil {
		return 0, err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", dest); err != nil {
		return 0, err
	}
	// The copy's own version, which a server starting meanwhile could have
	// changed in the database.
	copied, err := sql.Open("sqlite3", "file:"+dest+"?mode=ro")
	if err != nil {
		return 0, err
	}
	defer copied.Close()
	var version int
	err = copied.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version)
	return version, err
}
