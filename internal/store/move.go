// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Moving a source's channels to other sources (D144, schema 52). For a
// household that changes provider, or that stops reading another
// manager's output: once the administrator confirms which of the other
// sources' channels stands for each of the old source's, every lineup's
// entry of an old channel takes the new one in its place, and keeps its
// number, name, group, logo, hidden and locked state, favourites, what
// devices watched, recordings and backups. The new channel's own entry,
// which its source's first read gave it, goes, its favourites with the
// old entry. The channel's own switch for repair and its guide chosen by
// hand go to the new channel where it has none of its own. The old
// channel records where it went (channels.moved_to), so that the old
// source's reads give it no entry again; the old source stays until the
// administrator removes it, with the channels that did not move.

// ErrBadMove is returned for a move that cannot be: a channel that is not
// there, or that moved already, or that moves twice, or to a channel of
// its own source, or to one that another channel moves to too.
var ErrBadMove = errors.New("store: no such move")

// MovableChannel is a channel, as moving reads it: one to move, or one to
// move to.
type MovableChannel struct {
	ID       int64
	SourceID int64
	Name     string // as its source gives it
	Category string // its category's name at its source, "" if none
	XMLTVID  string
	Position int
}

// MovableChannels returns the channels of a source that can move, or be
// moved to: those that it still lists and that have not moved, in its
// order.
func (s *Store) MovableChannels(ctx context.Context, sourceID int64) ([]MovableChannel, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.id, c.source_id, c.name, COALESCE(k.name, ''), c.xmltv_id, c.position
		FROM channels c LEFT JOIN categories k ON k.id = c.category_id
		WHERE c.source_id = ? AND c.missing_since IS NULL AND c.moved_to IS NULL
		ORDER BY c.position, c.id`, sourceID)
	if err != nil {
		return nil, fmt.Errorf("store: channels of source %d: %w", sourceID, err)
	}
	defer rows.Close()
	var out []MovableChannel
	for rows.Next() {
		var c MovableChannel
		if err := rows.Scan(&c.ID, &c.SourceID, &c.Name, &c.Category, &c.XMLTVID, &c.Position); err != nil {
			return nil, fmt.Errorf("store: channels of source %d: %w", sourceID, err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: channels of source %d: %w", sourceID, err)
	}
	return out, nil
}

// MovedChannels counts the channels of a source that moved to others.
func (s *Store) MovedChannels(ctx context.Context, sourceID int64) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM channels WHERE source_id = ? AND moved_to IS NOT NULL`,
		sourceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: moved channels of source %d: %w", sourceID, err)
	}
	return n, nil
}

// ChannelMove says that a channel moves to another source's channel.
type ChannelMove struct {
	From, To int64
}

// MoveResult says what a move did.
type MoveResult struct {
	Channels int // the channels that moved
	Entries  int // the lineups' entries that took a new channel
	Merged   int // the new channels' own entries that went, their favourites with the old ones
}

// MoveChannels moves channels to other sources' channels, as D144 says,
// in one transaction, and runs every lineup's rules again. It returns
// ErrBadMove, and changes nothing, if any of the moves cannot be.
func (s *Store) MoveChannels(ctx context.Context, moves []ChannelMove) (MoveResult, error) {
	var res MoveResult
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkMoves(ctx, tx, moves); err != nil {
			return err
		}
		for _, m := range moves {
			n, merged, err := moveChannel(ctx, tx, m)
			if err != nil {
				return err
			}
			res.Channels++
			res.Entries += n
			res.Merged += merged
		}
		lineups, err := lineupIDs(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range lineups {
			if err := applyRules(ctx, tx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrBadMove) {
			return MoveResult{}, err
		}
		return MoveResult{}, fmt.Errorf("store: move channels: %w", err)
	}
	return res, nil
}

// checkMoves checks that every move can be.
func checkMoves(ctx context.Context, tx *sql.Tx, moves []ChannelMove) error {
	if len(moves) == 0 {
		return fmt.Errorf("%w: nothing to move", ErrBadMove)
	}
	from, to := map[int64]bool{}, map[int64]bool{}
	for _, m := range moves {
		if from[m.From] || to[m.To] || m.From == m.To {
			return fmt.Errorf("%w: channel %d or %d is in two moves", ErrBadMove, m.From, m.To)
		}
		from[m.From], to[m.To] = true, true
	}
	for _, m := range moves {
		if to[m.From] || from[m.To] {
			return fmt.Errorf("%w: channel %d moves and is moved to", ErrBadMove, m.From)
		}
		var fromSource, toSource int64
		var fromOK, toOK bool
		err := tx.QueryRowContext(ctx, `
			SELECT f.source_id, t.source_id, f.missing_since IS NULL AND f.moved_to IS NULL,
				t.missing_since IS NULL AND t.moved_to IS NULL
			FROM channels f, channels t WHERE f.id = ? AND t.id = ?`, m.From, m.To).Scan(&fromSource, &toSource, &fromOK, &toOK)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: no channel %d or %d", ErrBadMove, m.From, m.To)
		}
		if err != nil {
			return err
		}
		if !fromOK || !toOK || fromSource == toSource {
			return fmt.Errorf("%w: channel %d cannot move to channel %d", ErrBadMove, m.From, m.To)
		}
	}
	return nil
}

// moveChannel moves one channel's entries to another channel, and what is
// the channel's own, and returns how many entries moved and how many of
// the new channel's own entries went.
func moveChannel(ctx context.Context, tx *sql.Tx, m ChannelMove) (moved, merged int, err error) {
	var newName string
	if err := tx.QueryRowContext(ctx, "SELECT name FROM channels WHERE id = ?", m.To).Scan(&newName); err != nil {
		return 0, 0, err
	}
	// What each lineup shows of the old channel, to keep.
	type shown struct {
		entryID, lineupID int64
		name              string
		group             sql.NullInt64
	}
	var entries []shown
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, e.lineup_id, `+entryName+`, COALESCE(e.group_id, e.rule_group_id, m.group_id)`+lineupEntryJoins+`
		WHERE e.channel_id = ?`, m.From)
	if err != nil {
		return 0, 0, err
	}
	for rows.Next() {
		var e shown
		if err := rows.Scan(&e.entryID, &e.lineupID, &e.name, &e.group); err != nil {
			rows.Close()
			return 0, 0, err
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		var own int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM lineup_entries WHERE lineup_id = ? AND channel_id = ?",
			e.lineupID, m.To).Scan(&own)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return 0, 0, err
		default:
			// The new channel's own entry goes, what profiles and devices
			// did with it to the old one.
			for _, q := range []string{
				`INSERT OR IGNORE INTO favourites (profile_id, entry_id, added_at)
					SELECT profile_id, ?, added_at FROM favourites WHERE entry_id = ?`,
				`INSERT OR IGNORE INTO watched (device_id, entry_id, watched_at)
					SELECT device_id, ?, watched_at FROM watched WHERE entry_id = ?`,
				`UPDATE OR IGNORE recordings SET entry_id = ? WHERE entry_id = ?`,
			} {
				if _, err := tx.ExecContext(ctx, q, e.entryID, own); err != nil {
					return 0, 0, err
				}
			}
			if _, err := tx.ExecContext(ctx, "DELETE FROM lineup_entries WHERE id = ?", own); err != nil {
				return 0, 0, err
			}
			merged++
		}
		// The entry keeps what it showed: its name and its group, where
		// the new channel's differ, its number, pinned, and what rules
		// gave it, as its own, since the rules read the old channel.
		var name any
		if e.name != newName {
			name = e.name
		}
		var natural sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT m.group_id FROM channels c LEFT JOIN categories k ON k.id = c.category_id
				LEFT JOIN lineup_categories m ON m.lineup_id = ? AND m.source_id = c.source_id AND m.category = COALESCE(k.name, '')
			WHERE c.id = ?`, e.lineupID, m.To).Scan(&natural); err != nil {
			return 0, 0, err
		}
		if natural == e.group {
			e.group = sql.NullInt64{}
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE lineup_entries SET channel_id = ?, name = COALESCE(name, ?), group_id = COALESCE(group_id, ?),
				hidden = COALESCE(hidden, rule_hidden), logo = COALESCE(logo, rule_logo),
				pinned = CASE WHEN number IS NOT NULL THEN 1 ELSE pinned END
			WHERE id = ?`, m.To, name, e.group, e.entryID); err != nil {
			return 0, 0, err
		}
		moved++
	}
	for _, q := range []string{
		// Backups by hand: the old channel's place goes to the new one,
		// which is no entry's backup of itself.
		`UPDATE OR IGNORE lineup_entry_backups SET channel_id = ?2 WHERE channel_id = ?1`,
		`DELETE FROM lineup_entry_backups WHERE channel_id IN (?1, ?2)
			AND channel_id = (SELECT e.channel_id FROM lineup_entries e WHERE e.id = lineup_entry_backups.entry_id)`,
		`DELETE FROM lineup_entry_backups WHERE channel_id = ?1 AND ?2 IS NOT NULL`,
		// Its own switch for repair, where the new channel has none.
		`UPDATE channels SET repair = (SELECT repair FROM channels WHERE id = ?1) WHERE id = ?2 AND repair IS NULL`,
		// Its guide chosen by hand, where the new channel has none, and the
		// choices of channels that show its guide (D140).
		`INSERT OR IGNORE INTO channel_guide_choices (channel_id, use_channel, guide_channel_id, chosen_at)
			SELECT ?2, use_channel, guide_channel_id, chosen_at FROM channel_guide_choices
			WHERE channel_id = ?1 AND (use_channel IS NULL OR use_channel <> ?2)`,
		`DELETE FROM channel_guide_choices WHERE channel_id = ?2 AND use_channel = ?1`,
		`UPDATE channel_guide_choices SET use_channel = ?2 WHERE use_channel = ?1`,
		`UPDATE channels SET moved_to = ?2 WHERE id = ?1`,
	} {
		if _, err := tx.ExecContext(ctx, q, m.From, m.To); err != nil {
			return 0, 0, fmt.Errorf("%s: %w", strings.Fields(q)[0], err)
		}
	}
	return moved, merged, nil
}
