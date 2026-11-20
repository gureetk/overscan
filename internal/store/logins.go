// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Accounts with logins (D102, schema 36). A household starts with one
// account; the administrator can add more, for people who watch on players
// of their own, each with the devices that belong to it. An account may
// have a login for the outputs that players use, a playlist token, a limit
// on its streams at once and the outputs that it may use, all of which
// start unset or off.

// HouseholdAccount is the account that a household starts with (D19). It
// cannot be removed.
const HouseholdAccount = 1

// Limits on accounts.
const (
	MaxAccounts       = 50  // accounts on a server: a household or a small group of friends (plan, section 21.5)
	MaxAccountName    = 50  // characters in an account's name
	MaxUsername       = 64  // characters in a login's username
	MaxAccountStreams = 100 // the highest limit of streams at once that an account may have
)

var (
	// ErrAccountName is why an account cannot have a name: it is empty,
	// longer than MaxAccountName, holds a control character, or is another
	// account's, whatever its case.
	ErrAccountName = errors.New("store: an account's name is empty, too long, or another account's")
	// ErrTooManyAccounts is why another account cannot be added.
	ErrTooManyAccounts = errors.New("store: there are as many accounts as there may be")
	// ErrHousehold is why the household's account cannot be removed.
	ErrHousehold = errors.New("store: the household's account cannot be removed")
	// ErrAccountInUse is why an account that devices are approved for
	// cannot be removed.
	ErrAccountInUse = errors.New("store: devices are approved for the account")
	// ErrUsername is why a login cannot have a username: it is empty, too
	// long, or holds a character other than a letter, a digit, '.', '_',
	// '-' or '@'.
	ErrUsername = errors.New("store: a username is empty, too long, or holds a character it may not")
	// ErrUsernameTaken is why a login cannot have a username that another
	// account's has, whatever its case.
	ErrUsernameTaken = errors.New("store: another account has this username")
	// ErrNoPassword is why a login cannot be set without a password.
	ErrNoPassword = errors.New("store: a login needs a password")
	// ErrAccountStreams is why an account cannot have a limit of streams.
	ErrAccountStreams = errors.New("store: an account's limit of streams is out of range")
)

// CleanAccountName returns name as an account keeps it, without spaces at
// either end, or ErrAccountName if it cannot be an account's.
func CleanAccountName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > MaxAccountName ||
		strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return "", ErrAccountName
	}
	return name, nil
}

// CleanUsername returns username as a login keeps it, without spaces at
// either end, or ErrUsername. A username goes into the addresses that
// players ask for, so it holds only what needs no escaping there: ASCII
// letters and digits, '.', '_', '-' and '@'.
func CleanUsername(username string) (string, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(username) > MaxUsername {
		return "", ErrUsername
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-', r == '@':
		default:
			return "", ErrUsername
		}
	}
	return username, nil
}

// AddAccount adds an account with the given name, which sees the
// household's lineup. It takes the household's parental PIN and theme, so
// that a box moved to it is neither less locked nor different to look at,
// and starts with no login, no token, no end date, no limit and no output.
// It fails with ErrAccountName or ErrTooManyAccounts.
func (s *Store) AddAccount(ctx context.Context, name string) (Account, error) {
	name, err := CleanAccountName(name)
	if err != nil {
		return Account{}, err
	}
	var id int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var n, same int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*), count(*) FILTER (WHERE name = ? COLLATE NOCASE) FROM accounts`, name).Scan(&n, &same); err != nil {
			return err
		}
		switch {
		case same > 0:
			return ErrAccountName
		case n >= MaxAccounts:
			return ErrTooManyAccounts
		}
		return tx.QueryRowContext(ctx, `
			INSERT INTO accounts (name, lineup_id, pin_hash, theme_id)
			SELECT ?, h.lineup_id, h.pin_hash, h.theme_id FROM accounts h WHERE h.id = ?
			RETURNING id`, name, HouseholdAccount).Scan(&id)
	})
	switch {
	case errors.Is(err, ErrAccountName), errors.Is(err, ErrTooManyAccounts):
		return Account{}, err
	case isUniqueViolation(err):
		return Account{}, ErrAccountName
	case err != nil:
		return Account{}, fmt.Errorf("store: add an account: %w", err)
	}
	return s.Account(ctx, id)
}

// RenameAccount gives an account another name. It fails with
// ErrAccountName or ErrNotFound.
func (s *Store) RenameAccount(ctx context.Context, id int64, name string) error {
	name, err := CleanAccountName(name)
	if err != nil {
		return err
	}
	var same int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE name = ? COLLATE NOCASE AND id <> ?`,
		name, id).Scan(&same); err != nil {
		return fmt.Errorf("store: rename account %d: %w", id, err)
	}
	if same > 0 {
		return ErrAccountName
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET name = ? WHERE id = ?`, name, id)
	if err == nil {
		err = mustAffect(res)
	}
	switch {
	case isUniqueViolation(err):
		return ErrAccountName
	case errors.Is(err, ErrNotFound):
		return err
	case err != nil:
		return fmt.Errorf("store: rename account %d: %w", id, err)
	}
	return nil
}

// RemoveAccount removes an account, with its profiles, their favourites and
// resume points, and its messages. Blocked devices that it named keep no
// account. It fails with ErrHousehold for the household's account,
// ErrAccountInUse while a device is approved for it, or ErrNotFound.
func (s *Store) RemoveAccount(ctx context.Context, id int64) error {
	if id == HouseholdAccount {
		return ErrHousehold
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var approved int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM devices WHERE account_id = ? AND state = 'approved'`,
			id).Scan(&approved); err != nil {
			return err
		}
		if approved > 0 {
			return ErrAccountInUse
		}
		if _, err := tx.ExecContext(ctx, `UPDATE devices SET account_id = NULL, profile_id = NULL WHERE account_id = ?`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	switch {
	case errors.Is(err, ErrAccountInUse), errors.Is(err, ErrNotFound):
		return err
	case err != nil:
		return fmt.Errorf("store: remove account %d: %w", id, err)
	}
	return nil
}

// SetAccountLogin sets an account's login: its username, and the argon2id
// hash of its password, or with "" the password it has. It fails with
// ErrUsername, ErrUsernameTaken, ErrNoPassword for an account without a
// password and no hash given, or ErrNotFound.
func (s *Store) SetAccountLogin(ctx context.Context, id int64, username, passwordHash string) error {
	username, err := CleanUsername(username)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var taken int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM accounts WHERE username = ? COLLATE NOCASE AND id <> ?`,
			username, id).Scan(&taken); err != nil {
			return err
		}
		if taken > 0 {
			return ErrUsernameTaken
		}
		var current string
		err := tx.QueryRowContext(ctx, `SELECT password_hash FROM accounts WHERE id = ?`, id).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if passwordHash == "" {
			if current == "" {
				return ErrNoPassword
			}
			passwordHash = current
		}
		_, err = tx.ExecContext(ctx, `UPDATE accounts SET username = ?, password_hash = ? WHERE id = ?`, username, passwordHash, id)
		return err
	})
	switch {
	case errors.Is(err, ErrUsernameTaken), errors.Is(err, ErrNoPassword), errors.Is(err, ErrNotFound):
		return err
	case isUniqueViolation(err):
		return ErrUsernameTaken
	case err != nil:
		return fmt.Errorf("store: login of account %d: %w", id, err)
	}
	return nil
}

// RemoveAccountLogin takes an account's login away: its username and
// password. It fails with ErrNotFound.
func (s *Store) RemoveAccountLogin(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET username = NULL, password_hash = '' WHERE id = ?`, id)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: login of account %d: %w", id, err)
	}
	return err
}

// AccountByUsername returns the account whose login has the username,
// whatever its case, or ErrNotFound.
func (s *Store) AccountByUsername(ctx context.Context, username string) (Account, error) {
	a, err := scanAccount(s.db.QueryRowContext(ctx,
		"SELECT "+accountColumns+" FROM accounts WHERE username = ? COLLATE NOCASE", username))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account of a login: %w", err)
	}
	return a, nil
}

// tokenBytes is how much randomness a playlist token holds.
const tokenBytes = 32

// tokenHash returns the form in which the store finds an account by its
// token.
func tokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func accountTokenContext(id int64) string { return "account-token/" + strconv.FormatInt(id, 10) }

// NewAccountToken gives an account a new playlist token, which replaces the
// one it had, and returns it. The addresses made with the old one stop
// working. It needs the store's key, and fails with ErrNoKey without it, or
// with ErrNotFound.
func (s *Store) NewAccountToken(ctx context.Context, id int64) (string, error) {
	raw := make([]byte, tokenBytes)
	_, _ = rand.Read(raw)
	token := base64.RawURLEncoding.EncodeToString(raw)
	sealed, err := s.seal(accountTokenContext(id), token)
	if err != nil {
		return "", err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET token_hash = ?, token_sealed = ? WHERE id = ?`,
		tokenHash(token), sealed, id)
	if err == nil {
		err = mustAffect(res)
	}
	switch {
	case errors.Is(err, ErrNotFound):
		return "", err
	case err != nil:
		return "", fmt.Errorf("store: token of account %d: %w", id, err)
	}
	return token, nil
}

// AccountToken returns an account's playlist token, or ErrNotFound if it
// has none. It needs the store's key.
func (s *Store) AccountToken(ctx context.Context, id int64) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT token_sealed FROM accounts WHERE id = ?`, id).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) || err == nil && sealed == nil {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: token of account %d: %w", id, err)
	}
	return s.open(accountTokenContext(id), sealed)
}

// RemoveAccountToken takes an account's playlist token away, so that no
// address names the account any more.
func (s *Store) RemoveAccountToken(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET token_hash = NULL, token_sealed = NULL WHERE id = ?`, id)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: token of account %d: %w", id, err)
	}
	return err
}

// AccountByToken returns the account whose playlist token is token, or
// ErrNotFound.
func (s *Store) AccountByToken(ctx context.Context, token string) (Account, error) {
	if token == "" {
		return Account{}, ErrNotFound
	}
	a, err := scanAccount(s.db.QueryRowContext(ctx,
		"SELECT "+accountColumns+" FROM accounts WHERE token_hash = ?", tokenHash(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("store: account of a token: %w", err)
	}
	return a, nil
}

// SetAccountStreams sets the most streams that an account's devices and
// players play at once, 0 for no limit. It fails with ErrAccountStreams or
// ErrNotFound.
func (s *Store) SetAccountStreams(ctx context.Context, id int64, n int) error {
	if n < 0 || n > MaxAccountStreams {
		return ErrAccountStreams
	}
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET max_streams = ? WHERE id = ?`, n, id)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: streams of account %d: %w", id, err)
	}
	return err
}

// SetAccountOutputs sets the outputs that an account may use. It fails with
// ErrNotFound.
func (s *Store) SetAccountOutputs(ctx context.Context, id int64, o Outputs) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET output_m3u = ?, output_xtream = ? WHERE id = ?`, o.M3U, o.Xtream, id)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: outputs of account %d: %w", id, err)
	}
	return err
}

// SetDeviceAccount moves an approved device to another account. It plays
// as that account's first profile from then on. It fails with ErrNotFound
// for a device that is not approved, or an account that does not exist.
func (s *Store) SetDeviceAccount(ctx context.Context, mac string, accountID int64) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	if err := s.checkAccount(ctx, accountID); err != nil {
		return Device{}, err
	}
	return s.queryDevice(ctx, m, `
		UPDATE devices SET account_id = ?2,
			profile_id = CASE WHEN account_id = ?2 THEN profile_id END
		WHERE mac = ?1 AND state = 'approved'
		RETURNING `+deviceColumns, m, accountID)
}

// StreamsOf returns the account that a device's streams count against, and
// that account's limit of streams at once, 0 for none (D102). A device that
// is not approved has none: 0 and 0.
func (s *Store) StreamsOf(ctx context.Context, deviceID int64) (accountID int64, limit int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT a.id, a.max_streams FROM devices d JOIN accounts a ON a.id = d.account_id
		WHERE d.id = ? AND d.state = 'approved'`, deviceID).Scan(&accountID, &limit)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("store: account of device %d: %w", deviceID, err)
	}
	return accountID, limit, nil
}

// AccountStreams returns an account's limit of streams at once, 0 for none,
// or ErrNotFound.
func (s *Store) AccountStreams(ctx context.Context, id int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT max_streams FROM accounts WHERE id = ?`, id).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: streams of account %d: %w", id, err)
	}
	return n, nil
}

// RecordingAccount returns the account of the box that asked for a
// recording, 0 if not known.
func (s *Store) RecordingAccount(ctx context.Context, id int64) (int64, error) {
	var account int64
	err := s.db.QueryRowContext(ctx, `SELECT account_id FROM recordings WHERE id = ?`, id).Scan(&account)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("store: account of recording %d: %w", id, err)
	}
	return account, nil
}
