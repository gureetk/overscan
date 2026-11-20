// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/ncruces/go-sqlite3"
)

// Profiles (D94, schema 31) are who watches on an account's devices. Each
// keeps its own favourites and how far it got into films and episodes. An
// account's first profile took what the account kept before profiles came;
// an account always has at least one. A device plays as its profile, or as
// its account's first while it has none.

// MaxProfiles is the most profiles an account may have.
const MaxProfiles = 12

// MaxProfileName is the most characters a profile's name may have.
const MaxProfileName = 32

// Profile is one of an account's profiles.
type Profile struct {
	ID        int64
	AccountID int64
	Name      string
	// PINHash is the argon2id hash of the PIN that switching to it asks
	// for, "" if it asks for none.
	PINHash string
	// First says that it is its account's first profile, which devices
	// without a profile of their own play as.
	First bool
	// Devices counts the approved devices that play as it.
	Devices int
	// ThemeID is the theme of its own, 0 for its account's (D97).
	ThemeID int64
}

var (
	// ErrProfileName is why a profile cannot have a name: it is empty,
	// longer than MaxProfileName, holds a control character, or is another
	// of the account's profiles'.
	ErrProfileName = errors.New("store: a profile's name is empty, too long, or another profile's")
	// ErrLastProfile is why an account's only profile cannot be removed.
	ErrLastProfile = errors.New("store: an account's only profile cannot be removed")
	// ErrTooManyProfiles is why an account cannot have another profile.
	ErrTooManyProfiles = errors.New("store: the account has as many profiles as it may")
)

// deviceProfile returns SQL for the profile that the row d of devices
// plays as: its own, while that is one of its account's, or its account's
// first; 0 for a device of no account.
func deviceProfile(d string) string {
	return `COALESCE((SELECT o.id FROM profiles o WHERE o.id = ` + d + `.profile_id AND o.account_id = ` + d + `.account_id), ` +
		`(SELECT min(f.id) FROM profiles f WHERE f.account_id = ` + d + `.account_id), 0)`
}

// CleanProfileName returns name as a profile keeps it, without spaces at
// either end, or ErrProfileName if it cannot be a profile's.
func CleanProfileName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || utf8.RuneCountInString(name) > MaxProfileName || !utf8.ValidString(name) {
		return "", ErrProfileName
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return "", ErrProfileName
		}
	}
	return name, nil
}

var profileColumns = `p.id, p.account_id, p.name, p.pin_hash,
	p.id = (SELECT min(f.id) FROM profiles f WHERE f.account_id = p.account_id),
	(SELECT count(*) FROM devices d WHERE d.account_id = p.account_id AND d.state = 'approved' AND ` + deviceProfile("d") + ` = p.id),
	COALESCE(p.theme_id, 0)`

func scanProfile(row rowScanner) (Profile, error) {
	var p Profile
	err := row.Scan(&p.ID, &p.AccountID, &p.Name, &p.PINHash, &p.First, &p.Devices, &p.ThemeID)
	return p, err
}

// Profiles returns an account's profiles, the first first, then in the
// order they were added.
func (s *Store) Profiles(ctx context.Context, accountID int64) ([]Profile, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+profileColumns+` FROM profiles p WHERE p.account_id = ? ORDER BY p.id`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: profiles of account %d: %w", accountID, err)
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("store: profiles of account %d: %w", accountID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profiles of account %d: %w", accountID, err)
	}
	return out, nil
}

// Profile returns the profile with the given ID, or ErrNotFound.
func (s *Store) Profile(ctx context.Context, id int64) (Profile, error) {
	p, err := scanProfile(s.db.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM profiles p WHERE p.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Profile{}, ErrNotFound
	}
	if err != nil {
		return Profile{}, fmt.Errorf("store: profile %d: %w", id, err)
	}
	return p, nil
}

// AddProfile adds a profile to an account, with no favourites, no progress
// and no PIN. It fails with ErrProfileName or ErrTooManyProfiles.
func (s *Store) AddProfile(ctx context.Context, accountID int64, name string) (Profile, error) {
	name, err := CleanProfileName(name)
	if err != nil {
		return Profile{}, err
	}
	var id int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM profiles WHERE account_id = ?`, accountID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxProfiles {
			return ErrTooManyProfiles
		}
		return tx.QueryRowContext(ctx, `INSERT INTO profiles (account_id, name) VALUES (?, ?) RETURNING id`, accountID, name).Scan(&id)
	})
	switch {
	case errors.Is(err, ErrTooManyProfiles):
		return Profile{}, err
	case isUniqueViolation(err):
		return Profile{}, ErrProfileName
	case err != nil:
		return Profile{}, fmt.Errorf("store: add a profile to account %d: %w", accountID, err)
	}
	return s.Profile(ctx, id)
}

// RenameProfile gives a profile another name. It fails with ErrProfileName
// or ErrNotFound.
func (s *Store) RenameProfile(ctx context.Context, id int64, name string) error {
	name, err := CleanProfileName(name)
	if err != nil {
		return err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET name = ? WHERE id = ?`, name, id)
	if isUniqueViolation(err) {
		return ErrProfileName
	}
	return oneRow(res, err, "rename profile", id)
}

// SetProfilePIN sets the hash of the PIN that switching to a profile asks
// for, or "" for none. It fails with ErrNotFound.
func (s *Store) SetProfilePIN(ctx context.Context, id int64, hash string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE profiles SET pin_hash = ? WHERE id = ?`, hash, id)
	return oneRow(res, err, "set the PIN of profile", id)
}

// RemoveProfile removes a profile, its favourites and its progress. The
// devices that played as it play as their account's first profile. It
// fails with ErrLastProfile for an account's only profile, or ErrNotFound.
func (s *Store) RemoveProfile(ctx context.Context, id int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var others int
		err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM profiles o JOIN profiles p ON p.account_id = o.account_id
			WHERE p.id = ? AND o.id <> p.id`, id).Scan(&others)
		if err != nil {
			return err
		}
		if others == 0 {
			var one int
			if err := tx.QueryRowContext(ctx, `SELECT 1 FROM profiles WHERE id = ?`, id).Scan(&one); errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			return ErrLastProfile
		}
		// The foreign key clears the devices' profile, and drops the
		// profile's favourites and progress with it.
		_, err = tx.ExecContext(ctx, `DELETE FROM profiles WHERE id = ?`, id)
		return err
	})
	if errors.Is(err, ErrLastProfile) || errors.Is(err, ErrNotFound) {
		return err
	}
	if err != nil {
		return fmt.Errorf("store: remove profile %d: %w", id, err)
	}
	return nil
}

// SetDeviceProfile makes a device play as one of its account's profiles;
// its account's first profile, as at first, with profileID 0. It fails
// with ErrNotFound if the device is not approved for an account, or the
// profile is not one of that account's.
func (s *Store) SetDeviceProfile(ctx context.Context, deviceID, profileID int64) error {
	var res sql.Result
	var err error
	if profileID == 0 {
		res, err = s.db.ExecContext(ctx, `UPDATE devices SET profile_id = NULL WHERE id = ? AND account_id IS NOT NULL`, deviceID)
	} else {
		res, err = s.db.ExecContext(ctx, `
			UPDATE devices SET profile_id = ?
			WHERE id = ? AND account_id = (SELECT account_id FROM profiles WHERE id = ?)`, profileID, deviceID, profileID)
	}
	return oneRow(res, err, "set the profile of device", deviceID)
}

// isUniqueViolation reports whether err is a unique constraint's.
func isUniqueViolation(err error) bool { return errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) }

// oneRow returns ErrNotFound when an update changed no row, and wraps
// other errors with what it did.
func oneRow(res sql.Result, err error, what string, id int64) error {
	if err != nil {
		return fmt.Errorf("store: %s %d: %w", what, id, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: %s %d: %w", what, id, err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}
