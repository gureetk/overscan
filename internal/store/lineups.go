// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Several lineups (D106): a lineup is what an account sees of live TV and
// how (D45). A new one starts as a copy of another, with its groups, the
// categories they hold and its entries with their edits (D47); from then
// on each has edits of its own, and a refresh adds new channels and
// categories to every one.

// MaxLineups caps how many lineups a server has.
const MaxLineups = 20

// Errors about lineups.
var (
	ErrLineupExists   = errors.New("store: another lineup has that name")
	ErrLineupInUse    = errors.New("store: an account sees the lineup")
	ErrTooManyLineups = fmt.Errorf("store: a server has at most %d lineups", MaxLineups)
)

// LineupInfo is a lineup as the admin lists it.
type LineupInfo struct {
	Lineup
	Accounts []string // the names of the accounts that see it, by name
	Shown    int      // the channels it shows
	Rules    int      // its rules (D107), on or off
}

// Lineups returns every lineup, the first made first.
func (s *Store) Lineups(ctx context.Context) ([]LineupInfo, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT l.id, l.name, l.created_at, l.own_groups,
			COALESCE((SELECT group_concat(name, char(31)) FROM (
				SELECT a.name FROM accounts a WHERE a.lineup_id = l.id ORDER BY a.name COLLATE NOCASE)), ''),
			(SELECT count(*)`+lineupEntryJoins+` WHERE e.lineup_id = l.id AND `+shownEntry+`),
			(SELECT count(*) FROM lineup_rules r WHERE r.lineup_id = l.id)
		FROM lineups l ORDER BY l.id`)
	if err != nil {
		return nil, fmt.Errorf("store: lineups: %w", err)
	}
	defer rows.Close()
	out := []LineupInfo{}
	for rows.Next() {
		var l LineupInfo
		var created, accounts string
		if err := rows.Scan(&l.ID, &l.Name, &created, &l.OwnGroups, &accounts, &l.Shown, &l.Rules); err != nil {
			return nil, fmt.Errorf("store: lineups: %w", err)
		}
		if l.CreatedAt, err = parseTime(created); err != nil {
			return nil, fmt.Errorf("store: lineups: %w", err)
		}
		l.Accounts = []string{}
		if accounts != "" {
			l.Accounts = strings.Split(accounts, "\x1f")
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// lineupIDs returns the IDs of every lineup.
func lineupIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM lineups ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// numberEverywhere numbers a source's channels again in every lineup, as a
// change of its numbers setting does (D40). A lineup of its own groups
// numbers its channels in its own order, whatever the source's setting
// (D136), so it numbers only the channels without a number.
func numberEverywhere(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	lineups, err := lineupIDs(ctx, tx)
	if err != nil {
		return err
	}
	for _, lineupID := range lineups {
		if err := numberChannels(ctx, tx, lineupID, sourceID, true); err != nil {
			return err
		}
		if err := applyRules(ctx, tx, lineupID); err != nil {
			return err
		}
	}
	return nil
}

// lineupNameError turns a clash of names into ErrLineupExists.
func lineupNameError(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: lineups.name") {
		return ErrLineupExists
	}
	return err
}

// CopyLineup makes a lineup named name as a copy of another: whether it
// holds only what its groups take (D136), its groups, with their names,
// marks and the categories they hold, its entries, with their numbers,
// pins, names, groups, visibility and locks, and its rules, which then run
// on it. It returns the new lineup's ID.
func (s *Store) CopyLineup(ctx context.Context, from int64, name string) (int64, error) {
	fail := func(err error) (int64, error) { return 0, fmt.Errorf("store: copy lineup %d: %w", from, err) }
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM lineups").Scan(&n); err != nil {
			return err
		}
		if n >= MaxLineups {
			return ErrTooManyLineups
		}
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM lineups WHERE id = ?)", from).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO lineups (name, own_groups) SELECT ?, own_groups FROM lineups WHERE id = ? RETURNING id`,
			name, from).Scan(&id); err != nil {
			return lineupNameError(err)
		}
		// A group's name is unique in its lineup, so the copy's groups are
		// found by name.
		for _, q := range []string{`
			INSERT INTO lineup_groups (lineup_id, name, hidden, edited, locked, numbers_from)
			SELECT ?1, name, hidden, edited, locked, numbers_from FROM lineup_groups WHERE lineup_id = ?2`, `
			INSERT INTO lineup_categories (lineup_id, source_id, category, group_id)
			SELECT ?1, m.source_id, m.category, n.id
			FROM lineup_categories m
				JOIN lineup_groups o ON o.id = m.group_id
				JOIN lineup_groups n ON n.lineup_id = ?1 AND n.name = o.name
			WHERE m.lineup_id = ?2`, `
			INSERT INTO lineup_entries (lineup_id, channel_id, number, pinned, name, group_id, hidden, locked, numbered_in)
			SELECT ?1, e.channel_id, e.number, e.pinned, e.name, n.id, e.hidden, e.locked, nn.id
			FROM lineup_entries e
				LEFT JOIN lineup_groups o ON o.id = e.group_id
				LEFT JOIN lineup_groups n ON n.lineup_id = ?1 AND n.name = o.name
				LEFT JOIN lineup_groups no ON no.id = e.numbered_in
				LEFT JOIN lineup_groups nn ON nn.lineup_id = ?1 AND nn.name = no.name
			WHERE e.lineup_id = ?2`,
		} {
			if _, err := tx.ExecContext(ctx, q, id, from); err != nil {
				return err
			}
		}
		// Its channels' backups (D109), and its rules, which then run on
		// it (D107).
		if err := copyBackups(ctx, tx, from, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO lineup_rules (lineup_id, position, spec)
			SELECT ?1, position, spec FROM lineup_rules WHERE lineup_id = ?2`, id, from); err != nil {
			return err
		}
		return applyRules(ctx, tx, id)
	})
	if errors.Is(err, ErrLineupExists) || errors.Is(err, ErrTooManyLineups) || errors.Is(err, ErrNotFound) {
		return 0, err
	}
	if err != nil {
		return fail(err)
	}
	return id, nil
}

// RenameLineup gives a lineup another name.
func (s *Store) RenameLineup(ctx context.Context, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE lineups SET name = ? WHERE id = ?", name, id)
	if err = lineupNameError(err); err != nil {
		if errors.Is(err, ErrLineupExists) {
			return err
		}
		return fmt.Errorf("store: rename lineup %d: %w", id, err)
	}
	return mustAffect(res)
}

// RemoveLineup removes a lineup that no account sees, with its groups and
// entries; the favourites and the lists of what devices watched that point
// at its entries go with them.
func (s *Store) RemoveLineup(ctx context.Context, id int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var used bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM accounts WHERE lineup_id = ?)", id).Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrLineupInUse
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM lineups WHERE id = ?", id)
		if err != nil {
			return err
		}
		return mustAffect(res)
	})
	if err != nil && !errors.Is(err, ErrLineupInUse) && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: remove lineup %d: %w", id, err)
	}
	return err
}

// SetAccountLineup lets an account see another lineup. The favourites of
// its profiles and the lists of what its devices watched move to the same
// channels in that lineup, so that its boxes keep them; those that the
// lineup does not have are forgotten.
func (s *Store) SetAccountLineup(ctx context.Context, accountID, lineupID int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM lineups WHERE id = ?)", lineupID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		res, err := tx.ExecContext(ctx, "UPDATE accounts SET lineup_id = ? WHERE id = ?", lineupID, accountID)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		if err := moveFavourites(ctx, tx, "profile_id IN (SELECT id FROM profiles WHERE account_id = ?2)", lineupID, accountID); err != nil {
			return err
		}
		return moveWatched(ctx, tx, "device_id IN (SELECT id FROM devices WHERE account_id = ?2)", lineupID, accountID)
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: account %d's lineup: %w", accountID, err)
	}
	return err
}

// sameChannel is the SQL of the entry in lineup ?1 of the channel of the
// entry that a row of table names in its entry_id.
func sameChannel(table string) string {
	return `(SELECT n.id FROM lineup_entries o JOIN lineup_entries n ON n.channel_id = o.channel_id AND n.lineup_id = ?1
		WHERE o.id = ` + table + `.entry_id)`
}

// moveFavourites points the favourites that where picks, with ?2 its
// argument, at the entries of the same channels in lineup ?1, and forgets
// those that the lineup does not have.
func moveFavourites(ctx context.Context, tx *sql.Tx, where string, lineupID, arg int64) error {
	return moveEntries(ctx, tx, "favourites", where, lineupID, arg)
}

// moveWatched does for the lists of what devices watched what
// moveFavourites does for favourites.
func moveWatched(ctx context.Context, tx *sql.Tx, where string, lineupID, arg int64) error {
	return moveEntries(ctx, tx, "watched", where, lineupID, arg)
}

func moveEntries(ctx context.Context, tx *sql.Tx, table, where string, lineupID, arg int64) error {
	notThere := "entry_id NOT IN (SELECT id FROM lineup_entries WHERE lineup_id = ?1)"
	// A row whose channel the lineup has already, for the same profile or
	// device, stays where it is and is forgotten below.
	if _, err := tx.ExecContext(ctx, `UPDATE OR IGNORE `+table+` SET entry_id = `+sameChannel(table)+`
		WHERE `+where+` AND `+notThere+` AND `+sameChannel(table)+` IS NOT NULL`, lineupID, arg); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE `+where+` AND `+notThere, lineupID, arg)
	return err
}
