// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Account is what a household's devices and profiles belong to. It holds
// their entitlements: the lineup they see and an optional expiry.
type Account struct {
	ID        int64
	Name      string
	LineupID  int64
	ExpiresAt time.Time // zero if the account never expires
	CreatedAt time.Time
	PINHash   string // the parental PIN's argon2id hash, "" if none (D55)
	ThemeID   int64  // the theme its boxes show, 0 for Overscan's own (D97)
}

// Ended reports whether the account's end date has passed by now (D95):
// its devices then play nothing.
func (a Account) Ended(now time.Time) bool {
	return !a.ExpiresAt.IsZero() && !now.Before(a.ExpiresAt)
}

// Lineup is the set of channels an account sees.
type Lineup struct {
	ID        int64
	Name      string
	CreatedAt time.Time
}

// Account returns the account with the given ID, or ErrNotFound.
func (s *Store) Account(ctx context.Context, id int64) (Account, error) {
	var a Account
	var expires sql.NullString
	var created string
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, lineup_id, expires_at, created_at, COALESCE(pin_hash, ''), COALESCE(theme_id, 0) FROM accounts WHERE id = ?", id,
	).Scan(&a.ID, &a.Name, &a.LineupID, &expires, &created, &a.PINHash, &a.ThemeID)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account %d: %w", id, err)
	}
	if a.CreatedAt, err = parseTime(created); err != nil {
		return Account{}, fmt.Errorf("store: account %d: %w", id, err)
	}
	if expires.Valid {
		if a.ExpiresAt, err = parseTime(expires.String); err != nil {
			return Account{}, fmt.Errorf("store: account %d: %w", id, err)
		}
	}
	return a, nil
}

// Accounts returns every account, by ID.
func (s *Store) Accounts(ctx context.Context) ([]Account, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM accounts ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("store: accounts: %w", err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("store: accounts: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: accounts: %w", err)
	}
	out := make([]Account, 0, len(ids))
	for _, id := range ids {
		a, err := s.Account(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// SetAccountExpiry sets when an account expires; the zero time means never.
// It returns ErrNotFound if there is no such account.
func (s *Store) SetAccountExpiry(ctx context.Context, id int64, expiresAt time.Time) error {
	var value any
	if !expiresAt.IsZero() {
		value = formatTime(expiresAt)
	}
	res, err := s.db.ExecContext(ctx, "UPDATE accounts SET expires_at = ? WHERE id = ?", value, id)
	if err != nil {
		return fmt.Errorf("store: account %d: %w", id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: account %d: %w", id, err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Lineup returns the lineup with the given ID, or ErrNotFound.
func (s *Store) Lineup(ctx context.Context, id int64) (Lineup, error) {
	var l Lineup
	var created string
	err := s.db.QueryRowContext(ctx,
		"SELECT id, name, created_at FROM lineups WHERE id = ?", id,
	).Scan(&l.ID, &l.Name, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Lineup{}, ErrNotFound
	}
	if err != nil {
		return Lineup{}, fmt.Errorf("store: lineup %d: %w", id, err)
	}
	if l.CreatedAt, err = parseTime(created); err != nil {
		return Lineup{}, fmt.Errorf("store: lineup %d: %w", id, err)
	}
	return l, nil
}

// parseTime parses a time stored in the database: UTC, in RFC 3339 form.
func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored time %q: %w", s, err)
	}
	return t.UTC(), nil
}

// SetAccountPIN keeps the hash of an account's parental PIN, or with "" takes
// the PIN away (D55).
func (s *Store) SetAccountPIN(ctx context.Context, accountID int64, hash string) error {
	var value any = hash
	if hash == "" {
		value = nil
	}
	res, err := s.db.ExecContext(ctx, "UPDATE accounts SET pin_hash = ? WHERE id = ?", value, accountID)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil {
		return fmt.Errorf("store: PIN of account %d: %w", accountID, err)
	}
	return nil
}
