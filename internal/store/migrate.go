// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"strconv"
	"strings"
	"time"
)

// The migrations directory holds one SQL file per schema version, named
// NNNN_description.sql and numbered from 0001 without gaps. A migration that
// has been released is never edited; changes go in a new one.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

var migrations = mustLoadMigrations(migrationFiles)

// A migration takes the schema from version-1 to version.
type migration struct {
	version int
	name    string // file name, for messages
	sql     string
}

func mustLoadMigrations(fsys fs.FS) []migration {
	ms, err := loadMigrations(fsys)
	if err != nil {
		panic(err)
	}
	return ms
}

// loadMigrations reads the migrations directory of fsys and checks that the
// files are numbered 1, 2, 3 and so on.
func loadMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, err
	}
	var ms []migration
	for _, e := range entries { // sorted by name
		name := e.Name()
		num, desc, ok := strings.Cut(strings.TrimSuffix(name, ".sql"), "_")
		version, err := strconv.Atoi(num)
		if e.IsDir() || !strings.HasSuffix(name, ".sql") || !ok || desc == "" || len(num) != 4 || err != nil {
			return nil, fmt.Errorf("store: migration %q is not named NNNN_description.sql", name)
		}
		if want := len(ms) + 1; version != want {
			return nil, fmt.Errorf("store: migration %q has version %d, want %d", name, version, want)
		}
		data, err := fs.ReadFile(fsys, path.Join("migrations", name))
		if err != nil {
			return nil, err
		}
		ms = append(ms, migration{version: version, name: name, sql: string(data)})
	}
	if len(ms) == 0 {
		return nil, fmt.Errorf("store: no migrations")
	}
	return ms, nil
}

// migrate applies the migrations the database at path lacks, each in its
// own transaction together with the new schema version, so a failed
// migration leaves the database as it was. The transactions take the write
// lock first, so two processes opening the same new database apply each
// migration once. A database that already has a schema is backed up first
// (backupBeforeUpgrade), and is left as it is if the backup fails.
func (s *Store) migrate(ctx context.Context, log *slog.Logger, path string, ms []migration) error {
	latest := ms[len(ms)-1].version
	v, err := s.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	if v < 0 {
		return fmt.Errorf("store: the database's schema version %d is not valid", v)
	}
	if v > 0 && v < latest {
		dest, err := backupBeforeUpgrade(ctx, path, v, time.Now(), log)
		if err != nil {
			return fmt.Errorf("store: the database has schema %d, which this build upgrades to %d once it has backed the database up, "+
				"but the backup failed, so the database is as it was: %w", v, latest, err)
		}
		log.Info("backed up the database before upgrading its schema", "backup", dest, "schema", v, "to", latest)
	}
	if v < latest {
		for _, m := range ms[v:] {
			applied, err := s.apply(ctx, m)
			if err != nil {
				return err
			}
			if applied {
				log.Info("applied database migration", "version", m.version, "file", m.name)
			}
		}
		if v, err = s.SchemaVersion(ctx); err != nil {
			return err
		}
	}
	if v > latest {
		return fmt.Errorf("store: the database's schema version is %d, newer than the %d this build knows; run a newer Overscan or restore a backup", v, latest)
	}
	return nil
}

// rebuildMarker, on a line of its own in a migration, makes the migration
// run with foreign key checks off, as SQLite's procedure for rebuilding a
// table that other tables refer to requires: otherwise dropping the old table
// deletes the rows that refer to it. Before the migration is committed, every
// foreign key is checked.
const rebuildMarker = "-- overscan: foreign keys off"

// apply applies m unless the database already has it.
func (s *Store) apply(ctx context.Context, m migration) (applied bool, err error) {
	rebuild := strings.Contains(m.sql, "\n"+rebuildMarker+"\n")
	var tx *sql.Tx
	if rebuild {
		// The pragma does nothing inside a transaction, so it goes on a
		// connection of its own, which is then closed rather than going
		// back to the pool with the checks off.
		var conn *sql.Conn
		if conn, err = s.db.Conn(ctx); err != nil {
			return false, fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		defer func() {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}()
		if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys = OFF"); err != nil {
			return false, fmt.Errorf("store: migration %s: %w", m.name, err)
		}
		tx, err = conn.BeginTx(ctx, nil)
	} else {
		// The driver begins read-write transactions with BEGIN IMMEDIATE.
		tx, err = s.db.BeginTx(ctx, nil)
	}
	if err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	defer func() {
		if !applied {
			_ = tx.Rollback()
		}
	}()

	var v int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return false, fmt.Errorf("store: migration %s: read schema version: %w", m.name, err)
	}
	if v >= m.version {
		return false, nil
	}
	if v != m.version-1 {
		return false, fmt.Errorf("store: migration %s needs schema version %d, the database has %d", m.name, m.version-1, v)
	}
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	if rebuild {
		rows, err := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if err != nil {
			return false, fmt.Errorf("store: migration %s: check foreign keys: %w", m.name, err)
		}
		broken := rows.Next()
		rows.Close()
		if broken {
			return false, fmt.Errorf("store: migration %s leaves rows that refer to rows that do not exist", m.name)
		}
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("store: migration %s: check foreign keys: %w", m.name, err)
		}
	}
	// PRAGMA takes no bound parameters; the version is an int.
	if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
		return false, fmt.Errorf("store: migration %s: set schema version: %w", m.name, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("store: migration %s: %w", m.name, err)
	}
	return true, nil
}
