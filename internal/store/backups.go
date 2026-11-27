// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// A lineup channel's backups (D109): other channels, of any source, in
// order, whose streams the hub tries in turn when the channel's own cannot
// start. They belong to the lineup's entry, so that each lineup has its
// own, and a copy of a lineup copies them.

// MaxBackups is how many backups a lineup's channel can have.
const MaxBackups = 5

// Errors about backups.
var (
	ErrTooManyBackups = fmt.Errorf("store: a channel has at most %d backups", MaxBackups)
	ErrBadBackup      = errors.New("store: a channel cannot be its own backup, or a backup twice")
	ErrSeveral        = errors.New("store: several channels have that name")
)

// EntryBackup is a backup of a lineup's channel, as the lineup shows it.
type EntryBackup struct {
	EntryID   int64 // the backup's own entry in the lineup
	ChannelID int64
	Name      string // as devices see it
	Source    string // its source's name
	Number    int    // 0 if it has none
	Missing   bool   // its source no longer lists it
}

// EntryBackups returns the backups of a lineup's entry, in order. It returns
// ErrNotFound for an entry that the lineup does not have.
func (s *Store) EntryBackups(ctx context.Context, lineupID, entryID int64) ([]EntryBackup, error) {
	var out []EntryBackup
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := entryInLineup(ctx, tx, lineupID, entryID); err != nil {
			return err
		}
		var err error
		out, err = entryBackups(ctx, tx, lineupID, entryID)
		return err
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: backups of lineup entry %d: %w", entryID, err)
	}
	return out, err
}

// EntryRuleBackups returns the backups that the lineup's merge rules give
// one of its entries (D113), in order: the copies of its channel that they
// hide. It returns ErrNotFound for an entry that the lineup does not have.
func (s *Store) EntryRuleBackups(ctx context.Context, lineupID, entryID int64) ([]EntryBackup, error) {
	var out []EntryBackup
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := entryInLineup(ctx, tx, lineupID, entryID); err != nil {
			return err
		}
		var err error
		out, err = listBackups(ctx, tx, "lineup_entry_rule_backups", lineupID, entryID)
		return err
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: rules' backups of lineup entry %d: %w", entryID, err)
	}
	return out, err
}

// entryInLineup returns ErrNotFound unless the lineup has the entry.
func entryInLineup(ctx context.Context, tx *sql.Tx, lineupID, entryID int64) error {
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM lineup_entries WHERE id = ? AND lineup_id = ?)",
		entryID, lineupID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

func entryBackups(ctx context.Context, tx *sql.Tx, lineupID, entryID int64) ([]EntryBackup, error) {
	return listBackups(ctx, tx, "lineup_entry_backups", lineupID, entryID)
}

// listBackups lists an entry's backups from a table of them: those picked
// by hand, or those that rules give (D113).
func listBackups(ctx context.Context, tx *sql.Tx, table string, lineupID, entryID int64) ([]EntryBackup, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, c.id, `+entryName+`, s.name, COALESCE(e.number, 0), c.missing_since IS NOT NULL
		FROM `+table+` b
		JOIN channels c ON c.id = b.channel_id
		JOIN sources s ON s.id = c.source_id
		JOIN lineup_entries e ON e.lineup_id = ? AND e.channel_id = c.id
		WHERE b.entry_id = ? ORDER BY b.position`, lineupID, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EntryBackup{}
	for rows.Next() {
		var b EntryBackup
		if err := rows.Scan(&b.EntryID, &b.ChannelID, &b.Name, &b.Source, &b.Number, &b.Missing); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// SetEntryBackups makes the lineup's entries given, in order, the backups
// of one of its entries, in place of those it had. It returns ErrNotFound
// for an entry that the lineup does not have, ErrBadBackup for the entry
// itself or one given twice, and ErrTooManyBackups for more than
// MaxBackups.
func (s *Store) SetEntryBackups(ctx context.Context, lineupID, entryID int64, backups []int64) error {
	if len(backups) > MaxBackups {
		return ErrTooManyBackups
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := entryInLineup(ctx, tx, lineupID, entryID); err != nil {
			return err
		}
		seen := map[int64]bool{}
		channels := make([]int64, 0, len(backups))
		for _, id := range backups {
			if id == entryID || seen[id] {
				return ErrBadBackup
			}
			seen[id] = true
			var channel int64
			err := tx.QueryRowContext(ctx, "SELECT channel_id FROM lineup_entries WHERE id = ? AND lineup_id = ?",
				id, lineupID).Scan(&channel)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			if err != nil {
				return err
			}
			channels = append(channels, channel)
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM lineup_entry_backups WHERE entry_id = ?", entryID); err != nil {
			return err
		}
		for i, channel := range channels {
			if _, err := tx.ExecContext(ctx, "INSERT INTO lineup_entry_backups (entry_id, position, channel_id) VALUES (?, ?, ?)",
				entryID, i+1, channel); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrBadBackup) {
		err = fmt.Errorf("store: backups of lineup entry %d: %w", entryID, err)
	}
	return err
}

// FindEntry returns the ID of a lineup's entry by its number, for text that
// is one, or else by its name, as devices see it or as its source gives
// it, whatever its case. It returns ErrNotFound if none has it, and
// ErrSeveral if more than one has the name.
func (s *Store) FindEntry(ctx context.Context, lineupID int64, text string) (int64, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, ErrNotFound
	}
	if n, err := strconv.Atoi(text); err == nil {
		var id int64
		err := s.db.QueryRowContext(ctx, "SELECT id FROM lineup_entries WHERE lineup_id = ? AND number = ?", lineupID, n).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrNotFound
		}
		if err != nil {
			return 0, fmt.Errorf("store: find channel %d of lineup %d: %w", n, lineupID, err)
		}
		return id, nil
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT e.id FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
		WHERE e.lineup_id = ? AND (`+entryName+` = ? COLLATE NOCASE OR c.name = ? COLLATE NOCASE)
		LIMIT 2`, lineupID, text, text)
	if err != nil {
		return 0, fmt.Errorf("store: find a channel of lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("store: find a channel of lineup %d: %w", lineupID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("store: find a channel of lineup %d: %w", lineupID, err)
	}
	switch len(ids) {
	case 0:
		return 0, ErrNotFound
	case 1:
		return ids[0], nil
	}
	return 0, ErrSeveral
}

// ChannelsWithBackups counts a lineup's entries that have backups, picked
// by hand or given by its rules (D113).
func (s *Store) ChannelsWithBackups(ctx context.Context, lineupID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT b.entry_id) FROM (
			SELECT entry_id FROM lineup_entry_backups UNION ALL SELECT entry_id FROM lineup_entry_rule_backups) b
		JOIN lineup_entries e ON e.id = b.entry_id WHERE e.lineup_id = ?`, lineupID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: backups of lineup %d: %w", lineupID, err)
	}
	return n, nil
}

// copyBackups gives the entries of a new lineup the backups that the same
// channels have in the lineup it copies.
func copyBackups(ctx context.Context, tx *sql.Tx, from, to int64) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO lineup_entry_backups (entry_id, position, channel_id)
		SELECT n.id, b.position, b.channel_id
		FROM lineup_entry_backups b
		JOIN lineup_entries o ON o.id = b.entry_id AND o.lineup_id = ?
		JOIN lineup_entries n ON n.lineup_id = ? AND n.channel_id = o.channel_id`, from, to)
	return err
}

// playableBackups returns the streams of a lineup entry's backups that can
// play, in order: from enabled sources, of channels that their sources
// still list; those picked by hand first, then those that its rules give
// (D113), at most MaxBackups. Each is p with its channel's stream in place
// of p's own.
func (s *Store) playableBackups(ctx context.Context, p PlayableEntry) ([]PlayableEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.source_id, c.stream_url, c.stream_secret, s.name, s.type,
			(SELECT secret FROM source_secrets WHERE source_id = s.id), COALESCE(c.repair, `+repairDefaultSQL+`), c.radio,
			c.transcode
		FROM (
			SELECT channel_id, position FROM lineup_entry_backups WHERE entry_id = ?1
			UNION ALL
			SELECT channel_id, `+strconv.Itoa(MaxBackups)+` + position FROM lineup_entry_rule_backups
			WHERE entry_id = ?1 AND channel_id NOT IN (SELECT channel_id FROM lineup_entry_backups WHERE entry_id = ?1)) b
		JOIN channels c ON c.id = b.channel_id
		JOIN sources s ON s.id = c.source_id
		WHERE s.enabled = 1 AND c.missing_since IS NULL AND c.id <> ?2
		ORDER BY b.position
		LIMIT ?3`, p.EntryID, p.ChannelID, MaxBackups)
	if err != nil {
		return nil, err
	}
	type found struct {
		b                      PlayableEntry
		sealed, sourceSealed   []byte
		sourceName, sourceType string
	}
	var list []found
	for rows.Next() {
		f := found{b: p}
		f.b.Backups = nil
		var own sql.NullString
		if err := rows.Scan(&f.b.ChannelID, &f.b.SourceID, &f.b.StreamURL, &f.sealed, &f.sourceName, &f.sourceType,
			&f.sourceSealed, &f.b.Repair, &f.b.Radio, &own); err != nil {
			rows.Close()
			return nil, err
		}
		// A backup's channel's own setting, else the entry's group's and
		// live TV's (D147).
		f.b.Transcode, f.b.TranscodeBy = resolveTranscode(own, p.groupTranscode, p.liveTranscode)
		list = append(list, f)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]PlayableEntry, 0, len(list))
	for _, f := range list {
		var err error
		f.b.StreamURL, err = s.streamAddress(f.b.SourceID, f.b.StreamURL, f.sealed, f.sourceName, f.sourceType, f.sourceSealed)
		if err != nil {
			return nil, fmt.Errorf("backup channel %d: %w", f.b.ChannelID, err)
		}
		out = append(out, f.b)
	}
	return out, nil
}
