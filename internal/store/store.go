// SPDX-License-Identifier: AGPL-3.0-or-later

// Package store keeps Overscan's state in an SQLite database: one file in the
// data directory, in WAL mode, with its schema brought up to date by the
// migrations embedded in the binary.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"sync/atomic"
	"time"

	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/driver" // registers the "sqlite3" database/sql driver

	"overscan.gureet.ca/internal/secrets"
)

// FileName is the database file's name in the data directory.
const FileName = "overscan.db"

// maxOpenConns caps the connection pool. SQLite lets one connection write at
// a time while others read, and every connection carries its own copy of the
// SQLite engine's memory, so a household server needs only a few.
const maxOpenConns = 4

// busyTimeoutMS is how long a statement waits for a lock that another
// connection holds before it fails.
const busyTimeoutMS = 10_000

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("store: not found")

var (
	// ErrNoKey is returned when a secret must be sealed or opened and the
	// store has no key (UseKey).
	ErrNoKey = errors.New("store: no key to seal or open secrets with")
	// ErrUnreadable is returned for a secret that the store's key cannot
	// open: it was sealed with another key file, or it is damaged.
	ErrUnreadable = errors.New("store: a secret that this key file cannot open")
)

// Store is Overscan's database. It is safe for concurrent use.
type Store struct {
	db  *sql.DB
	box atomic.Pointer[secrets.Box] // seals and opens secrets (D46)
	// zone is the time zone whose midnights placeholder programmes'
	// blocks start from (D142): the server's own, unless a test sets it.
	zone *time.Location
}

// UseKey makes the store seal secrets with box, and open them. Without it,
// the store reads secrets that older versions kept unsealed, and refuses
// everything else that touches a secret with ErrNoKey.
func (s *Store) UseKey(box *secrets.Box) { s.box.Store(box) }

// seal seals a secret for its context (secrets.Box.Seal).
func (s *Store) seal(context, value string) ([]byte, error) {
	box := s.box.Load()
	if box == nil {
		return nil, ErrNoKey
	}
	return box.Seal(context, []byte(value)), nil
}

// open opens a secret that seal sealed for the same context.
func (s *Store) open(context string, sealed []byte) (string, error) {
	box := s.box.Load()
	if box == nil {
		return "", ErrNoKey
	}
	plain, err := box.Open(context, sealed)
	if err != nil {
		return "", fmt.Errorf("%w (%w)", ErrUnreadable, err)
	}
	return string(plain), nil
}

// Open opens the database file at path, creating it if it does not exist,
// and applies the migrations it lacks, once it has backed up a database that
// had a schema already into the backups folder beside it (BackupsFolder). A
// new file is readable and writable by its owner only, and so are its
// journal and WAL files. Open refuses a database whose schema is newer than
// this build knows.
func Open(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	return open(ctx, path, log, migrations)
}

func open(ctx context.Context, path string, log *slog.Logger, ms []migration) (*Store, error) {
	// SQLite would create the file according to the umask, usually readable
	// by everyone. An empty file is a valid empty database.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}

	db, err := sql.Open("sqlite3", dataSourceName(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(maxOpenConns)
	db.SetMaxIdleConns(maxOpenConns)

	s := &Store{db: db}
	if err := setWAL(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(ctx, log, path, ms); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// setWAL puts the database in WAL mode, which is kept in the file, so it is
// set once rather than on every connection. Switching a new database needs
// an exclusive lock, and SQLite refuses rather than waits when another
// process holds the database, as when two processes open a new database at
// once, so setWAL tries again until the busy timeout.
func setWAL(ctx context.Context, db *sql.DB) error {
	deadline := time.Now().Add(busyTimeoutMS * time.Millisecond)
	for {
		var mode string
		err := db.QueryRowContext(ctx, "PRAGMA journal_mode = wal").Scan(&mode)
		switch {
		case err == nil && mode == "wal":
			return nil
		case err == nil:
			return fmt.Errorf("store: the database stays in %s mode instead of wal", mode)
		case !errors.Is(err, sqlite3.BUSY) || time.Now().After(deadline):
			return fmt.Errorf("store: set WAL mode: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

// dataSourceName returns the driver's URI for the database at path. Every new
// connection sets the busy timeout first, then NORMAL synchronous writes (safe
// in WAL mode; a power cut can lose the last commits but not corrupt the
// file) and foreign key checks. modeof makes journal and WAL files take the
// database file's permissions.
func dataSourceName(path string) string {
	q := url.Values{}
	q.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMS))
	q.Add("_pragma", "synchronous(normal)")
	q.Add("_pragma", "foreign_keys(on)")
	q.Set("modeof", path)
	u := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	return u.String()
}

// Close closes the database.
func (s *Store) Close() error {
	return s.db.Close()
}

// SchemaVersion returns the version of the database's schema: the number of
// the last migration applied.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var v int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&v); err != nil {
		return 0, fmt.Errorf("store: read schema version: %w", err)
	}
	return v, nil
}
