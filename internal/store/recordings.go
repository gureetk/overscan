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

// settingRecordUntil says how long a recording that a box starts on a
// channel runs while the guide shows a programme on it (D98).
const settingRecordUntil = "recordings_until"

// What a recording that a box starts on a channel runs until, while the
// guide shows a programme on it: the programme's end, as at first, or
// until it is stopped. Without a programme, it runs until it is stopped.
const (
	RecordUntilProgramme = "programme"
	RecordUntilStopped   = "stopped"
)

// RecordUntil returns how long a recording that a box starts on a channel
// runs while the guide shows a programme: RecordUntilProgramme or
// RecordUntilStopped.
func (s *Store) RecordUntil(ctx context.Context) (string, error) {
	v, _, err := s.setting(ctx, settingRecordUntil)
	if v != RecordUntilStopped {
		v = RecordUntilProgramme
	}
	return v, err
}

// SetRecordUntil sets how long a recording that a box starts on a channel
// runs while the guide shows a programme.
func (s *Store) SetRecordUntil(ctx context.Context, v string) error {
	if v != RecordUntilProgramme && v != RecordUntilStopped {
		return fmt.Errorf("store: recordings run until %q: not %q or %q", v, RecordUntilProgramme, RecordUntilStopped)
	}
	return s.setSetting(ctx, settingRecordUntil, v)
}

// settingRecordingHours is the most hours that a recording which runs
// until it is stopped may run (D98).
const settingRecordingHours = "recordings_max_hours"

// The hours that a recording which runs until it is stopped may run: 4 at
// first, from 1 to MaxRecordingHours.
const (
	DefaultRecordingHours = 4
	MaxRecordingHours     = 24
)

// RecordingHours returns the most hours that a recording which runs until
// it is stopped may run.
func (s *Store) RecordingHours(ctx context.Context) (int, error) {
	v, ok, err := s.setting(ctx, settingRecordingHours)
	n, perr := strconv.Atoi(v)
	if !ok || perr != nil || n < 1 || n > MaxRecordingHours {
		n = DefaultRecordingHours
	}
	return n, err
}

// SetRecordingHours sets the most hours that a recording which runs until
// it is stopped may run.
func (s *Store) SetRecordingHours(ctx context.Context, hours int) error {
	if hours < 1 || hours > MaxRecordingHours {
		return fmt.Errorf("store: recordings' hours: %d is not between 1 and %d", hours, MaxRecordingHours)
	}
	return s.setSetting(ctx, settingRecordingHours, strconv.Itoa(hours))
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
	File      string // its file's name, "" until it is made
	// Dir is the folder its file was made in, "" for the recordings
	// folder (schema 30).
	Dir       string
	Size      int64
	Error     string
	CreatedAt time.Time
	// UntilStopped is set for a recording that runs until it is stopped,
	// at the latest until Stop, rather than to the end of a programme
	// (D98, schema 34).
	UntilStopped bool
}

const recordingColumns = `id, account_id, device_id, entry_id, channel, title, start, stop, state, file, size, error, created_at, dir, until_stopped`

func scanRecording(row rowScanner) (Recording, error) {
	var r Recording
	var start, stop, created int64
	err := row.Scan(&r.ID, &r.AccountID, &r.DeviceID, &r.EntryID, &r.Channel, &r.Title, &start, &stop, &r.State,
		&r.File, &r.Size, &r.Error, &created, &r.Dir, &r.UntilStopped)
	r.Start, r.Stop, r.CreatedAt = time.Unix(start, 0), time.Unix(stop, 0), time.Unix(created, 0)
	return r, err
}

// ScheduleRecording schedules a programme on a lineup entry. One already
// scheduled or under way is returned as it is; one that failed or was
// cancelled is scheduled again.
func (s *Store) ScheduleRecording(ctx context.Context, r Recording) (Recording, error) {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO recordings (account_id, device_id, entry_id, channel, title, start, stop, state, created_at, until_stopped)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (entry_id, start) DO UPDATE SET
			account_id = excluded.account_id, device_id = excluded.device_id, channel = excluded.channel,
			title = excluded.title, stop = excluded.stop, state = excluded.state, error = '', size = 0, file = '', dir = '',
			created_at = excluded.created_at, until_stopped = excluded.until_stopped
		WHERE recordings.state IN (?, ?)`,
		r.AccountID, r.DeviceID, r.EntryID, r.Channel, r.Title, r.Start.Unix(), r.Stop.Unix(), RecordingScheduled,
		r.CreatedAt.Unix(), r.UntilStopped, RecordingFailed, RecordingCancelled)
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

// ActiveRecording returns the recording of a lineup entry that is under
// way, or due to run at a time, or ErrNotFound.
func (s *Store) ActiveRecording(ctx context.Context, entryID int64, at time.Time) (Recording, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM recordings
		WHERE entry_id = ? AND (state = ? OR state = ? AND start <= ? AND stop > ?)
		ORDER BY start DESC, id DESC LIMIT 1`,
		entryID, RecordingRecording, RecordingScheduled, at.Unix(), at.Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return Recording{}, ErrNotFound
	}
	if err != nil {
		return Recording{}, fmt.Errorf("store: lineup entry %d's recording: %w", entryID, err)
	}
	return r, nil
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

// SetRecordingState records what became of a recording: its state, the
// folder its file is in and the file's name, its size and, for one that
// failed, why.
func (s *Store) SetRecordingState(ctx context.Context, id int64, state, dir, file string, size int64, why string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE recordings SET state = ?, dir = ?, file = ?, size = ?, error = ? WHERE id = ?`,
		state, dir, file, size, why, id)
	if err != nil {
		return fmt.Errorf("store: recording %d: %w", id, err)
	}
	return mustAffect(res)
}

// UpcomingRecordings counts an account's recordings that are to come or
// under way.
func (s *Store) UpcomingRecordings(ctx context.Context, accountID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recordings WHERE account_id = ? AND state IN (?, ?)`,
		accountID, RecordingScheduled, RecordingRecording).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: count an account's recordings: %w", err)
	}
	return n, nil
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
