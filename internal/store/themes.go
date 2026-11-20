// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Themes (D97, schema 33): the theme files that the administrator
// installed, which the theme package checked. An account may have one for
// its boxes and a profile one of its own; a box shows its profile's, else
// its account's, else Overscan's own.

// Theme is an installed theme.
type Theme struct {
	ID        int64
	Name      string
	Data      []byte // the theme file, as the theme package keeps it
	CreatedAt time.Time
	// Uses names what has it: its accounts' names and its profiles' names,
	// for the admin to say before removing it.
	Uses []string
}

const themeColumns = `t.id, t.name, t.data, t.created_at,
	COALESCE((SELECT group_concat(name, char(31)) FROM (
		SELECT a.name AS name FROM accounts a WHERE a.theme_id = t.id
		UNION ALL SELECT p.name FROM profiles p WHERE p.theme_id = t.id ORDER BY 1)), '')`

func scanTheme(row rowScanner) (Theme, error) {
	var th Theme
	var data string
	var created int64
	var uses string
	if err := row.Scan(&th.ID, &th.Name, &data, &created, &uses); err != nil {
		return Theme{}, err
	}
	th.Data, th.CreatedAt = []byte(data), time.Unix(created, 0).UTC()
	if uses != "" {
		th.Uses = splitUnit(uses)
	}
	return th, nil
}

// splitUnit splits a list that group_concat joined with the unit separator.
func splitUnit(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == 31 {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// InstallTheme keeps a theme, or with the name of one installed already,
// whatever its case, replaces that one's file, keeping what uses it. It
// says which it did.
func (s *Store) InstallTheme(ctx context.Context, name string, data []byte, at time.Time) (Theme, bool, error) {
	var id int64
	replaced := false
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `SELECT id FROM themes WHERE name = ? COLLATE NOCASE`, name).Scan(&id)
		switch {
		case err == nil:
			replaced = true
			_, err = tx.ExecContext(ctx, `UPDATE themes SET name = ?, data = ? WHERE id = ?`, name, string(data), id)
			return err
		case errors.Is(err, sql.ErrNoRows):
			return tx.QueryRowContext(ctx, `INSERT INTO themes (name, data, created_at) VALUES (?, ?, ?) RETURNING id`,
				name, string(data), at.Unix()).Scan(&id)
		}
		return err
	})
	if err != nil {
		return Theme{}, false, fmt.Errorf("store: install theme %q: %w", name, err)
	}
	th, err := s.Theme(ctx, id)
	return th, replaced, err
}

// Themes returns the installed themes, by name.
func (s *Store) Themes(ctx context.Context) ([]Theme, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+themeColumns+` FROM themes t ORDER BY t.name COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("store: themes: %w", err)
	}
	defer rows.Close()
	var out []Theme
	for rows.Next() {
		th, err := scanTheme(rows)
		if err != nil {
			return nil, fmt.Errorf("store: themes: %w", err)
		}
		out = append(out, th)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: themes: %w", err)
	}
	return out, nil
}

// Theme returns an installed theme, or ErrNotFound.
func (s *Store) Theme(ctx context.Context, id int64) (Theme, error) {
	th, err := scanTheme(s.db.QueryRowContext(ctx, `SELECT `+themeColumns+` FROM themes t WHERE t.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Theme{}, ErrNotFound
	}
	if err != nil {
		return Theme{}, fmt.Errorf("store: theme %d: %w", id, err)
	}
	return th, nil
}

// RemoveTheme removes an installed theme; what used it shows its account's
// theme, or Overscan's own. It fails with ErrNotFound.
func (s *Store) RemoveTheme(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM themes WHERE id = ?`, id)
	return oneRow(res, err, "remove theme", id)
}

// SetAccountTheme gives an account's boxes a theme, or with 0 Overscan's
// own. It fails with ErrNotFound for a theme or an account that is not
// there.
func (s *Store) SetAccountTheme(ctx context.Context, accountID, themeID int64) error {
	return s.setTheme(ctx, "accounts", accountID, themeID)
}

// SetProfileTheme gives a profile a theme of its own, or with 0 its
// account's. It fails with ErrNotFound for a theme or a profile that is not
// there.
func (s *Store) SetProfileTheme(ctx context.Context, profileID, themeID int64) error {
	return s.setTheme(ctx, "profiles", profileID, themeID)
}

func (s *Store) setTheme(ctx context.Context, table string, id, themeID int64) error {
	var value any
	if themeID != 0 {
		value = themeID
		var one int
		if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM themes WHERE id = ?`, themeID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		} else if err != nil {
			return fmt.Errorf("store: theme %d: %w", themeID, err)
		}
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+table+` SET theme_id = ? WHERE id = ?`, value, id)
	return oneRow(res, err, "set the theme of "+table[:len(table)-1], id)
}

// ProfileTheme returns the theme that a box playing as a profile shows: the
// profile's own, else its account's. It reports false for Overscan's own.
func (s *Store) ProfileTheme(ctx context.Context, profileID int64) (Theme, bool, error) {
	var id sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(p.theme_id, a.theme_id) FROM profiles p JOIN accounts a ON a.id = p.account_id
		WHERE p.id = ?`, profileID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !id.Valid {
		return Theme{}, false, nil
	}
	if err != nil {
		return Theme{}, false, fmt.Errorf("store: theme of profile %d: %w", profileID, err)
	}
	th, err := s.Theme(ctx, id.Int64)
	if errors.Is(err, ErrNotFound) {
		return Theme{}, false, nil
	}
	return th, err == nil, err
}
