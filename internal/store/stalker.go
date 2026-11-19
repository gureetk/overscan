// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// SetStalkerToken records the hash of the token a device got from a Stalker
// handshake, replacing the one before.
func (s *Store) SetStalkerToken(ctx context.Context, deviceID int64, tokenHash string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO stalker_tokens (device_id, token_hash, issued_at) VALUES (?, ?, ?)
		ON CONFLICT (device_id) DO UPDATE SET
			token_hash = excluded.token_hash,
			issued_at = excluded.issued_at`,
		deviceID, tokenHash, formatTime(at))
	if err != nil {
		return fmt.Errorf("store: stalker token for device %d: %w", deviceID, err)
	}
	return nil
}

// StalkerTokenHash returns the hash of a device's current Stalker token, or
// ErrNotFound if the device has none.
func (s *Store) StalkerTokenHash(ctx context.Context, deviceID int64) (string, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		"SELECT token_hash FROM stalker_tokens WHERE device_id = ?", deviceID).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("store: stalker token for device %d: %w", deviceID, err)
	}
	return hash, nil
}

// CountDevices returns how many devices are in the given state.
func (s *Store) CountDevices(ctx context.Context, state DeviceState) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		"SELECT count(*) FROM devices WHERE state = ?", string(state)).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count %s devices: %w", state, err)
	}
	return n, nil
}

// Capture is a kind of request Overscan does not implement (D13), kept so
// that what real boxes and clients ask for can be studied later.
type Capture struct {
	Module    string // the request's type parameter
	Action    string
	Params    string // parameter names, sorted, comma-separated
	Example   string // the last such request's query, redacted
	UserAgent string
	DeviceID  int64 // 0 if unknown
	Count     int64
	FirstSeen time.Time
	LastSeen  time.Time
}

// CaptureRequest adds c to the capture log, or counts it again if a request
// with the same module, action and parameter names is there already. A new
// kind of request is dropped once the log holds maxKinds kinds; stored
// reports whether it was kept.
func (s *Store) CaptureRequest(ctx context.Context, c Capture, at time.Time, maxKinds int) (stored bool, err error) {
	var device any
	if c.DeviceID != 0 {
		device = c.DeviceID
	}
	when := formatTime(at)
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO capture_log (module, action, params, example, user_agent, device_id, first_seen, last_seen)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?
		WHERE (SELECT count(*) FROM capture_log) < ?
			OR EXISTS (SELECT 1 FROM capture_log WHERE module = ? AND action = ? AND params = ?)
		ON CONFLICT (module, action, params) DO UPDATE SET
			count = capture_log.count + 1,
			example = excluded.example,
			user_agent = excluded.user_agent,
			device_id = excluded.device_id,
			last_seen = excluded.last_seen`,
		c.Module, c.Action, c.Params, c.Example, c.UserAgent, device, when, when,
		maxKinds, c.Module, c.Action, c.Params)
	if err != nil {
		return false, fmt.Errorf("store: capture %s/%s: %w", c.Module, c.Action, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: capture %s/%s: %w", c.Module, c.Action, err)
	}
	return n > 0, nil
}

// ClearCaptures empties the capture log and returns how many kinds of
// request it held.
func (s *Store) ClearCaptures(ctx context.Context) (int, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM capture_log")
	if err != nil {
		return 0, fmt.Errorf("store: clear the capture log: %w", err)
	}
	n, err := rowsAffected(res)
	if err != nil {
		return 0, fmt.Errorf("store: clear the capture log: %w", err)
	}
	return n, nil
}

// Captures returns the capture log, most recent first.
func (s *Store) Captures(ctx context.Context) ([]Capture, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT module, action, params, example, user_agent, device_id, count, first_seen, last_seen
		FROM capture_log ORDER BY last_seen DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("store: captures: %w", err)
	}
	defer rows.Close()
	var cs []Capture
	for rows.Next() {
		var c Capture
		var device sql.NullInt64
		var first, last string
		if err := rows.Scan(&c.Module, &c.Action, &c.Params, &c.Example, &c.UserAgent, &device, &c.Count, &first, &last); err != nil {
			return nil, fmt.Errorf("store: captures: %w", err)
		}
		c.DeviceID = device.Int64
		if c.FirstSeen, err = parseTime(first); err != nil {
			return nil, fmt.Errorf("store: captures: %w", err)
		}
		if c.LastSeen, err = parseTime(last); err != nil {
			return nil, fmt.Errorf("store: captures: %w", err)
		}
		cs = append(cs, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: captures: %w", err)
	}
	return cs, nil
}
