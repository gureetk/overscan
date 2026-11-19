// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Recordings made on the server (D89, schema 29).

// The states of a recording.
const (
	RecordingScheduled = "scheduled" // waiting for its start
	RecordingRecording = "recording" // being written
	RecordingDone      = "done"      // written, and in the recordings folder
	RecordingFailed    = "failed"    // not made, or cut short; Error says why
	RecordingCancelled = "cancelled" // called off before or while it was made
)

// settingRecordingsCap is the most room, in gigabytes, that the recordings
// folder may take; 0, until it is set, for no recordings at all (D89).
const settingRecordingsCap = "recordings_cap"

// MaxRecordingsCap is the largest cap the setting takes, in gigabytes.
const MaxRecordingsCap = 4000

// RecordingsCap returns the most room, in gigabytes, that recordings may
// take: 0 while recording is off, as it is until set.
func (s *Store) RecordingsCap(ctx context.Context) (int, error) {
	v, _, err := s.setting(ctx, settingRecordingsCap)
	n, perr := strconv.Atoi(v)
	if perr != nil || n < 0 || n > MaxRecordingsCap {
		n = 0
	}
	return n, err
}

// SetRecordingsCap sets the most room that recordings may take, in
// gigabytes; 0 turns recording off.
func (s *Store) SetRecordingsCap(ctx context.Context, gb int) error {
	if gb < 0 || gb > MaxRecordingsCap {
		return fmt.Errorf("store: recordings' room: %d is not between 0 and %d", gb, MaxRecordingsCap)
	}
	return s.setSetting(ctx, settingRecordingsCap, strconv.Itoa(gb))
}

// Recording is a programme to record, or recorded.
type Recording struct {
	ID        int64
	AccountID int64
	DeviceID  int64
	EntryID   int64
	Channel   string
	Title     string
	Start     time.Time
	Stop      time.Time
	State     string
	File      string // its file's name in the recordings folder, "" until it is made
	Size      int64
	Error     string
	CreatedAt time.Time
}

const recordingColumns = `id, account_id, device_id, entry_id, channel, title, start, stop, state, file, size, error, created_at`

func scanRecording(row rowScanner) (Recording, error) {
	var r Recording
	var start, stop, created int64
	err := row.Scan(&r.ID, &r.AccountID, &r.DeviceID, &r.EntryID, &r.Channel, &r.Title, &start, &stop, &r.State,
		&r.File, &r.Size, &r.Error, &created)
	r.Start, r.Stop, r.CreatedAt = time.Unix(start, 0), time.Unix(stop, 0), time.Unix(created, 0)
	return r, err
}

// ScheduleRecording schedules a programme on a lineup entry. One already
// scheduled or under way is returned as it is; one that failed or was
// cancelled is scheduled again.
func (s *Store) ScheduleRecording(ctx context.Context, r Recording) (Recording, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recordings (account_id, device_id, entry_id, channel, title, start, stop, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id, start) DO UPDATE SET
			account_id = excluded.account_id, device_id = excluded.device_id, channel = excluded.channel,
			title = excluded.title, stop = excluded.stop, state = excluded.state, error = '', size = 0, file = '',
			created_at = excluded.created_at
		WHERE recordings.state IN (?, ?)`,
		r.AccountID, r.DeviceID, r.EntryID, r.Channel, r.Title, r.Start.Unix(), r.Stop.Unix(), RecordingScheduled,
		r.CreatedAt.Unix(), RecordingFailed, RecordingCancelled)
	if err != nil {
		return Recording{}, fmt.Errorf("store: schedule a recording: %w", err)
	}
	out, err := scanRecording(s.db.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM recordings WHERE entry_id = ? AND start = ?`,
		r.EntryID, r.Start.Unix()))
	if err != nil {
		return Recording{}, fmt.Errorf("store: schedule a recording: %w", err)
	}
	return out, nil
}

// RecordingByID returns a recording, or ErrNotFound.
func (s *Store) RecordingByID(ctx context.Context, id int64) (Recording, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM recordings WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, ErrNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("store: recording %d: %w", id, err)
	}
	return r, nil
}

// Recordings returns the recordings in the given states, all of them if
// none is given, by start, at most limit of them (0 for all).
func (s *Store) Recordings(ctx context.Context, limit int, states ...string) ([]Recording, error) {
	return s.recordings(ctx, `start, id`, limit, states)
}

// RecentRecordings returns the recordings that are done, failed or called
// off, the latest first, at most limit of them.
func (s *Store) RecentRecordings(ctx context.Context, limit int) ([]Recording, error) {
	return s.recordings(ctx, `start DESC, id DESC`, limit, []string{RecordingDone, RecordingFailed, RecordingCancelled})
}

func (s *Store) recordings(ctx context.Context, order string, limit int, states []string) ([]Recording, error) {
	q := `SELECT ` + recordingColumns + ` FROM recordings`
	args := []any{}
	if len(states) > 0 {
		q += ` WHERE state IN (?` + strings.Repeat(", ?", len(states)-1) + `)`
		for _, st := range states {
			args = append(args, st)
		}
	}
	q += ` ORDER BY ` + order
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: recordings: %w", err)
	}
	defer rows.Close()
	var out []Recording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, fmt.Errorf("store: recordings: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetRecordingState records what became of a recording: its state, its
// file, its size and, for one that failed, why.
func (s *Store) SetRecordingState(ctx context.Context, id int64, state, file string, size int64, why string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE recordings SET state = ?, file = ?, size = ?, error = ? WHERE id = ?`,
		state, file, size, why, id)
	if err != nil {
		return fmt.Errorf("store: recording %d: %w", id, err)
	}
	return mustAffect(res)
}

// RecordingCounts counts the recordings in each state.
func (s *Store) RecordingCounts(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM recordings GROUP BY state`)
	if err != nil {
		return nil, fmt.Errorf("store: count recordings: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, fmt.Errorf("store: count recordings: %w", err)
		}
		out[state] = n
	}
	return out, rows.Err()
}

// PruneRecordings forgets the recordings that failed with no file or were
// called off, and that ended before a time; it returns how many.
func (s *Store) PruneRecordings(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM recordings WHERE state IN (?, ?) AND file = '' AND stop < ?`,
		RecordingFailed, RecordingCancelled, before.Unix())
	if err != nil {
		return 0, fmt.Errorf("store: prune recordings: %w", err)
	}
	return res.RowsAffected()
}

// DeleteRecording forgets a recording; its file is the caller's to remove.
func (s *Store) DeleteRecording(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM recordings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: delete recording %d: %w", id, err)
	}
	return mustAffect(res)
}
