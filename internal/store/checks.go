// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// PlaybackCheck is what a box's playback check found (D70).
type PlaybackCheck struct {
	DeviceID int64
	// Model is the device's model when it was checked, as the box reports
	// it; Firmware, its firmware's version, if the box said.
	Model, Firmware string
	// Findings say, by capability, such as "audio:dts", whether the box
	// plays it.
	Findings map[string]bool
	// Results are the page's answers, as it sent them.
	Results json.RawMessage
	// CheckedAt is when it was checked.
	CheckedAt time.Time
}

// SetPlaybackCheck keeps what a device's playback check found, in place of
// what an earlier one did. A device that no longer exists keeps nothing.
func (s *Store) SetPlaybackCheck(ctx context.Context, c PlaybackCheck) error {
	findings, err := json.Marshal(c.Findings)
	if err != nil {
		return fmt.Errorf("store: playback check of device %d: %w", c.DeviceID, err)
	}
	results := c.Results
	if len(results) == 0 {
		results = json.RawMessage("null")
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO playback_checks (device_id, model, firmware, findings, results, checked_at)
		SELECT ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM devices WHERE id = ?)
		ON CONFLICT (device_id) DO UPDATE SET model = excluded.model, firmware = excluded.firmware,
			findings = excluded.findings, results = excluded.results, checked_at = excluded.checked_at`,
		c.DeviceID, c.Model, c.Firmware, string(findings), string(results), c.CheckedAt.Unix(), c.DeviceID)
	if err != nil {
		return fmt.Errorf("store: playback check of device %d: %w", c.DeviceID, err)
	}
	return nil
}

const checkColumns = `device_id, model, firmware, findings, results, checked_at`

// scanCheck reads a row of checkColumns.
func scanCheck(row rowScanner) (PlaybackCheck, error) {
	var c PlaybackCheck
	var findings, results string
	var at int64
	if err := row.Scan(&c.DeviceID, &c.Model, &c.Firmware, &findings, &results, &at); err != nil {
		return PlaybackCheck{}, err
	}
	if err := json.Unmarshal([]byte(findings), &c.Findings); err != nil {
		return PlaybackCheck{}, fmt.Errorf("findings: %w", err)
	}
	c.Results = json.RawMessage(results)
	c.CheckedAt = time.Unix(at, 0).UTC()
	return c, nil
}

// PlaybackCheck returns what a device's playback check found, or
// ErrNotFound if it has not been checked.
func (s *Store) PlaybackCheck(ctx context.Context, deviceID int64) (PlaybackCheck, error) {
	c, err := scanCheck(s.db.QueryRowContext(ctx,
		`SELECT `+checkColumns+` FROM playback_checks WHERE device_id = ?`, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return PlaybackCheck{}, ErrNotFound
	}
	if err != nil {
		return PlaybackCheck{}, fmt.Errorf("store: playback check of device %d: %w", deviceID, err)
	}
	return c, nil
}

// LatestPlaybackCheck returns what the latest playback check of a device of
// a model found, or ErrNotFound if none of that model has been checked.
func (s *Store) LatestPlaybackCheck(ctx context.Context, model string) (PlaybackCheck, error) {
	c, err := scanCheck(s.db.QueryRowContext(ctx, `SELECT `+checkColumns+` FROM playback_checks
		WHERE model = ? ORDER BY checked_at DESC, device_id DESC LIMIT 1`, model))
	if errors.Is(err, sql.ErrNoRows) {
		return PlaybackCheck{}, ErrNotFound
	}
	if err != nil {
		return PlaybackCheck{}, fmt.Errorf("store: latest playback check of a %s: %w", model, err)
	}
	return c, nil
}

// PlaybackChecks returns what every device's playback check found, by
// device ID.
func (s *Store) PlaybackChecks(ctx context.Context) (map[int64]PlaybackCheck, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+checkColumns+` FROM playback_checks`)
	if err != nil {
		return nil, fmt.Errorf("store: playback checks: %w", err)
	}
	defer rows.Close()
	out := map[int64]PlaybackCheck{}
	for rows.Next() {
		c, err := scanCheck(rows)
		if err != nil {
			return nil, fmt.Errorf("store: playback checks: %w", err)
		}
		out[c.DeviceID] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: playback checks: %w", err)
	}
	return out, nil
}

// ForgetPlaybackCheck forgets what a device's playback check found, or
// returns ErrNotFound if it has not been checked.
func (s *Store) ForgetPlaybackCheck(ctx context.Context, deviceID int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM playback_checks WHERE device_id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("store: playback check of device %d: %w", deviceID, err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("store: playback check of device %d: %w", deviceID, err)
	} else if n == 0 {
		return ErrNotFound
	}
	return nil
}
