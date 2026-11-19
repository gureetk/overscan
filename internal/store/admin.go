// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// adminID is the administrator's row: there is one administrator (D44).
const adminID = 1

// maxAdminSessions caps how many sessions are kept: signing in beyond it ends
// the sessions seen least recently.
const maxAdminSessions = 20

// AdminUser is the administrator (D44).
type AdminUser struct {
	Username      string
	PasswordHash  string // argon2id, in the PHC string format
	PasswordSetAt time.Time
}

// AdminSession is a session of the administrator, signed in to the admin.
type AdminSession struct {
	ID        int64
	Username  string
	CSRFToken string // the token that the session's forms and API writes carry
	Address   string // where the session signed in from
	UserAgent string
	CreatedAt time.Time
	LastSeen  time.Time
}

// AdminUser returns the administrator, or ErrNotFound if no password has been
// set yet.
func (s *Store) AdminUser(ctx context.Context) (AdminUser, error) {
	var u AdminUser
	var setAt string
	err := s.db.QueryRowContext(ctx,
		"SELECT username, password_hash, password_set_at FROM admin_users WHERE id = ?", adminID,
	).Scan(&u.Username, &u.PasswordHash, &setAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminUser{}, ErrNotFound
	}
	if err != nil {
		return AdminUser{}, fmt.Errorf("store: administrator: %w", err)
	}
	if u.PasswordSetAt, err = parseTime(setAt); err != nil {
		return AdminUser{}, fmt.Errorf("store: administrator: %w", err)
	}
	return u, nil
}

// SetAdminPassword sets the administrator's username and password hash,
// creating the administrator if there is none, and ends every session but the
// one with the ID keep (0 for none).
func (s *Store) SetAdminPassword(ctx context.Context, username, hash string, at time.Time, keep int64) error {
	if username == "" || hash == "" {
		return errors.New("store: administrator: the username and the password hash must not be empty")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: administrator: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO admin_users (id, username, password_hash, password_set_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET
			username = excluded.username,
			password_hash = excluded.password_hash,
			password_set_at = excluded.password_set_at`,
		adminID, username, hash, formatTime(at)); err != nil {
		return fmt.Errorf("store: administrator: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM admin_sessions WHERE id <> ?", keep); err != nil {
		return fmt.Errorf("store: administrator: end sessions: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: administrator: %w", err)
	}
	return nil
}

// CreateAdminSession records a new session of the administrator, kept by the
// hash of its cookie's token. It ends the sessions seen least recently beyond
// the 20 most recent. It returns ErrNotFound if there is no administrator.
func (s *Store) CreateAdminSession(ctx context.Context, tokenHash, csrfToken, address, userAgent string, at time.Time) (AdminSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	defer tx.Rollback()
	var username string
	err = tx.QueryRowContext(ctx, "SELECT username FROM admin_users WHERE id = ?", adminID).Scan(&username)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSession{}, ErrNotFound
	}
	if err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	when := formatTime(at)
	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO admin_sessions (user_id, token_hash, csrf_token, address, user_agent, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		adminID, tokenHash, csrfToken, address, userAgent, when, when).Scan(&id)
	if err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM admin_sessions WHERE id NOT IN (
			SELECT id FROM admin_sessions ORDER BY last_seen DESC, id DESC LIMIT ?)`,
		maxAdminSessions); err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	return AdminSession{
		ID: id, Username: username, CSRFToken: csrfToken, Address: address, UserAgent: userAgent,
		CreatedAt: at.UTC().Truncate(time.Second), LastSeen: at.UTC().Truncate(time.Second),
	}, nil
}

// AdminSession returns the session whose cookie's token has the given hash, or
// ErrNotFound.
func (s *Store) AdminSession(ctx context.Context, tokenHash string) (AdminSession, error) {
	var a AdminSession
	var created, seen string
	err := s.db.QueryRowContext(ctx, `
		SELECT s.id, u.username, s.csrf_token, s.address, s.user_agent, s.created_at, s.last_seen
		FROM admin_sessions s JOIN admin_users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, tokenHash,
	).Scan(&a.ID, &a.Username, &a.CSRFToken, &a.Address, &a.UserAgent, &created, &seen)
	if errors.Is(err, sql.ErrNoRows) {
		return AdminSession{}, ErrNotFound
	}
	if err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	if a.CreatedAt, err = parseTime(created); err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	if a.LastSeen, err = parseTime(seen); err != nil {
		return AdminSession{}, fmt.Errorf("store: admin session: %w", err)
	}
	return a, nil
}

// TouchAdminSession records that a session was used at the given time.
func (s *Store) TouchAdminSession(ctx context.Context, id int64, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, "UPDATE admin_sessions SET last_seen = ? WHERE id = ?", formatTime(at), id); err != nil {
		return fmt.Errorf("store: admin session %d: %w", id, err)
	}
	return nil
}

// EndAdminSession ends a session. Ending one that has already ended is not an
// error.
func (s *Store) EndAdminSession(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, "DELETE FROM admin_sessions WHERE id = ?", id); err != nil {
		return fmt.Errorf("store: admin session %d: %w", id, err)
	}
	return nil
}

// EndExpiredAdminSessions ends the sessions created before createdBefore or
// last seen before seenBefore, and returns how many it ended.
func (s *Store) EndExpiredAdminSessions(ctx context.Context, createdBefore, seenBefore time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		"DELETE FROM admin_sessions WHERE created_at < ? OR last_seen < ?",
		formatTime(createdBefore), formatTime(seenBefore))
	if err != nil {
		return 0, fmt.Errorf("store: admin sessions: %w", err)
	}
	return rowsAffected(res)
}
