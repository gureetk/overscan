// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The viewer's own lists and settings (D53, schema 13): favourites, which
// belong to a profile (D94); and what a device watched, and its page's
// settings, which belong to the device.

// MaxWatched is how many channels a device's list of what it watched keeps.
const MaxWatched = 30

// EntryShown returns nil if devices of the lineup see the entry, and
// ErrNotFound if they do not: it is in another lineup, hidden, missing from
// its source, or gone.
func (s *Store) EntryShown(ctx context.Context, lineupID, entryID int64) error {
	var one int
	err := s.db.QueryRowContext(ctx, "SELECT 1"+lineupEntryJoins+" WHERE e.lineup_id = ? AND e.id = ? AND "+shownEntry,
		lineupID, entryID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: lineup entry %d: %w", entryID, err)
	}
	return nil
}

// Favourites returns the lineup entries that a profile marked as
// favourites, in the order they were marked. It can list entries that
// devices no longer see.
func (s *Store) Favourites(ctx context.Context, profileID int64) ([]int64, error) {
	return s.entryIDs(ctx, "SELECT entry_id FROM favourites WHERE profile_id = ? ORDER BY added_at, entry_id", profileID)
}

// SetFavourite marks a lineup entry as one of the profile's favourites, or
// unmarks it. Marking a favourite again, or unmarking one that is not, does
// nothing.
func (s *Store) SetFavourite(ctx context.Context, profileID, entryID int64, on bool, at time.Time) error {
	var err error
	if on {
		_, err = s.db.ExecContext(ctx,
			"INSERT INTO favourites (profile_id, entry_id, added_at) VALUES (?, ?, ?) ON CONFLICT DO NOTHING",
			profileID, entryID, formatTime(at))
	} else {
		_, err = s.db.ExecContext(ctx, "DELETE FROM favourites WHERE profile_id = ? AND entry_id = ?", profileID, entryID)
	}
	if err != nil {
		return fmt.Errorf("store: favourite %d of profile %d: %w", entryID, profileID, err)
	}
	return nil
}

// RecordWatch notes that a device played a lineup entry at a time, and
// forgets what it played longest ago beyond MaxWatched.
func (s *Store) RecordWatch(ctx context.Context, deviceID, entryID int64, at time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO watched (device_id, entry_id, watched_at) VALUES (?, ?, ?)
			ON CONFLICT (device_id, entry_id) DO UPDATE SET watched_at = excluded.watched_at`,
			deviceID, entryID, at.Unix()); err != nil {
			return fmt.Errorf("store: record what device %d watched: %w", deviceID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM watched WHERE device_id = ? AND entry_id NOT IN (
				SELECT entry_id FROM watched WHERE device_id = ? ORDER BY watched_at DESC, entry_id DESC LIMIT ?)`,
			deviceID, deviceID, MaxWatched); err != nil {
			return fmt.Errorf("store: record what device %d watched: %w", deviceID, err)
		}
		return nil
	})
}

// Watched returns the lineup entries that a device played, the latest
// first.
func (s *Store) Watched(ctx context.Context, deviceID int64) ([]int64, error) {
	return s.entryIDs(ctx,
		"SELECT entry_id FROM watched WHERE device_id = ? ORDER BY watched_at DESC, entry_id DESC", deviceID)
}

func (s *Store) entryIDs(ctx context.Context, query string, id int64) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, query, id)
	if err != nil {
		return nil, fmt.Errorf("store: entries of %d: %w", id, err)
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var entryID int64
		if err := rows.Scan(&entryID); err != nil {
			return nil, fmt.Errorf("store: entries of %d: %w", id, err)
		}
		out = append(out, entryID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: entries of %d: %w", id, err)
	}
	return out, nil
}

// DeviceSettings returns a device's settings for Overscan's page, a JSON
// object, or "" if it has none.
func (s *Store) DeviceSettings(ctx context.Context, deviceID int64) (string, error) {
	var settings string
	err := s.db.QueryRowContext(ctx, "SELECT settings FROM device_settings WHERE device_id = ?", deviceID).Scan(&settings)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: settings of device %d: %w", deviceID, err)
	}
	return settings, nil
}

// SetDeviceSettings stores a device's settings, a JSON object.
func (s *Store) SetDeviceSettings(ctx context.Context, deviceID int64, settings string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO device_settings (device_id, settings) VALUES (?, ?)
		ON CONFLICT (device_id) DO UPDATE SET settings = excluded.settings`, deviceID, settings)
	if err != nil {
		return fmt.Errorf("store: settings of device %d: %w", deviceID, err)
	}
	return nil
}

// EntryProgramme is a programme of a lineup entry.
type EntryProgramme struct {
	EntryID int64
	Programme
}

// SearchProgrammes returns up to limit programmes of a lineup's shown
// entries whose titles hold query, ignoring case for ASCII letters, that
// end after from and start before to, the earliest first.
func (s *Store) SearchProgrammes(ctx context.Context, lineupID int64, query string, from, to time.Time, limit int) ([]EntryProgramme, error) {
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(query) + "%"
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id, p.id, p.start + `+guideShiftSQL+` AS shown_start, p.stop + `+guideShiftSQL+`, p.title, p.subtitle,
			p.description, p.category`+lineupEntryJoins+`
		JOIN programmes p ON p.channel_id = `+guideChannelSQL+`
		JOIN source_guides sg ON sg.source_id = p.source_id AND sg.generation = p.generation
		WHERE e.lineup_id = ? AND `+shownEntry+` AND p.stop > ? - `+guideShiftSQL+` AND p.start < ? - `+guideShiftSQL+`
			AND p.title LIKE ? ESCAPE '\'
		ORDER BY shown_start, e.number
		LIMIT ?`,
		lineupID, from.Unix(), to.Unix(), pattern, limit)
	if err != nil {
		return nil, fmt.Errorf("store: search the guide of lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	out := []EntryProgramme{}
	for rows.Next() {
		var ep EntryProgramme
		var start, stop int64
		if err := rows.Scan(&ep.EntryID, &ep.ID, &start, &stop, &ep.Title, &ep.SubTitle, &ep.Description, &ep.Category); err != nil {
			return nil, fmt.Errorf("store: search the guide of lineup %d: %w", lineupID, err)
		}
		ep.Start, ep.Stop = time.Unix(start, 0).UTC(), time.Unix(stop, 0).UTC()
		out = append(out, ep)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: search the guide of lineup %d: %w", lineupID, err)
	}
	return out, nil
}

// EntryLogo returns the logo address of a lineup entry that devices see,
// "" if its channel has none, with the user agent its source asks for, ""
// for Overscan's own. It returns ErrNotFound for an entry that devices do
// not see.
func (s *Store) EntryLogo(ctx context.Context, entryID int64) (logo, userAgent string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT "+entryLogo+", s.user_agent"+lineupEntryJoins+" WHERE e.id = ? AND "+shownEntry,
		entryID).Scan(&logo, &userAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("store: logo of lineup entry %d: %w", entryID, err)
	}
	return logo, userAgent, nil
}
