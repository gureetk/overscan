// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"overscan.gureet.ca/internal/natural"
)

// Numbers and order (D137, schema 46). A group can have a first number:
// its channels are numbered from it, in their order, and a channel that
// joins the group takes the next free number after the group's last.
// Numbers set by hand or by a rule stay where they are (D47, D107), and so
// do the numbers of other groups' channels: a number that another channel
// holds is skipped. Only the channels that devices can see are numbered,
// so that the group's numbers run without gaps; one hidden on its own, or
// as a merged copy (D113), gives its number up, and takes the next one
// when it is shown again. A group's channels can also be sorted by name,
// in natural order, on the numbers they hold.

// groupMember is an entry in a group, as numberGroups and SortGroupByName
// see it.
type groupMember struct {
	id     int64
	number int // 0 if none
	in     bool
	hidden bool
	name   string
}

// groupMembers returns the entries of a lineup whose channels are in a
// group, as devices would see them, that its sources list and that the
// lineup holds (D136), without numbers set by hand or by a rule: in their
// order, those without numbers last.
func groupMembers(ctx context.Context, tx *sql.Tx, lineupID, groupID int64) ([]groupMember, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, COALESCE(e.number, 0), COALESCE(e.numbered_in = ?2, 0), COALESCE(e.hidden, e.rule_hidden, 0) = 1,
			`+entryName+`
		FROM lineup_entries e JOIN channels c ON c.id = e.channel_id`+takenJoins+`
		WHERE e.lineup_id = ?1 AND COALESCE(e.group_id, e.rule_group_id, m.group_id) = ?2 AND c.missing_since IS NULL
			AND e.pinned = 0 AND e.rule_pinned = 0 AND `+takenEntry+`
		ORDER BY e.number IS NULL, e.number, c.source_id, c.position`, lineupID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []groupMember
	for rows.Next() {
		var m groupMember
		if err := rows.Scan(&m.id, &m.number, &m.in, &m.hidden, &m.name); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// numberGroups numbers what joined the lineup's groups that have a first
// number: each channel that the group's numbering did not number, in
// order, takes the next free number after the group's last, or its first
// number if it has none yet; a hidden one gives its number up. An entry
// that left the group that numbered it keeps its number, as any other.
func numberGroups(ctx context.Context, tx *sql.Tx, lineupID int64) error {
	// What left the group that numbered it is that group's no more.
	if _, err := tx.ExecContext(ctx, `
		UPDATE lineup_entries SET numbered_in = NULL WHERE id IN (
			SELECT e.id FROM lineup_entries e JOIN channels c ON c.id = e.channel_id`+takenJoins+`
			WHERE e.lineup_id = ? AND e.numbered_in IS NOT NULL
				AND COALESCE(e.group_id, e.rule_group_id, m.group_id) IS NOT e.numbered_in)`, lineupID); err != nil {
		return err
	}
	type numbered struct {
		id   int64
		from int
	}
	var groups []numbered
	rows, err := tx.QueryContext(ctx, `
		SELECT id, numbers_from FROM lineup_groups WHERE lineup_id = ? AND numbers_from IS NOT NULL
		ORDER BY numbers_from, id`, lineupID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var g numbered
		if err := rows.Scan(&g.id, &g.from); err != nil {
			rows.Close()
			return err
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(groups) == 0 {
		return err
	}

	held := map[int]bool{}
	rows, err = tx.QueryContext(ctx, "SELECT number FROM lineup_entries WHERE lineup_id = ? AND number IS NOT NULL", lineupID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		held[n] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	update, err := tx.PrepareContext(ctx, "UPDATE lineup_entries SET number = ?, numbered_in = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer update.Close()
	for _, g := range groups {
		members, err := groupMembers(ctx, tx, lineupID, g.id)
		if err != nil {
			return err
		}
		last := g.from - 1
		var newcomers []groupMember
		for _, m := range members {
			switch {
			case m.hidden:
				if m.number != 0 || m.in {
					delete(held, m.number)
					if _, err := update.ExecContext(ctx, nil, nil, m.id); err != nil {
						return err
					}
				}
			case m.in && m.number != 0:
				last = max(last, m.number)
			default:
				// Its number, if any, is free for the group to give.
				delete(held, m.number)
				newcomers = append(newcomers, m)
			}
		}
		// A number is held once at a time: the newcomers give theirs up
		// before any takes one.
		for _, m := range newcomers {
			if m.number != 0 {
				if _, err := update.ExecContext(ctx, nil, nil, m.id); err != nil {
					return err
				}
			}
		}
		for _, m := range newcomers {
			n := last + 1
			for n <= MaxNumber && held[n] {
				n++
			}
			var number, in any
			if n <= MaxNumber {
				number, in = n, g.id
				held[n] = true
				last = n
			}
			if _, err := update.ExecContext(ctx, number, in, m.id); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetGroupNumbersFrom gives a group of the lineup a first number (D137):
// its channels are numbered from it again, in their order, and those that
// join it later take the next free number after its last. 0 takes the
// first number away, and its channels keep the numbers they have. It
// returns ErrNotFound for a group that is not the lineup's, and
// ErrBadNumber for a number out of range.
func (s *Store) SetGroupNumbersFrom(ctx context.Context, lineupID, groupID int64, from int) error {
	if from < 0 || from > MaxNumber {
		return ErrBadNumber
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
			return err
		}
		var value any
		if from > 0 {
			value = from
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET numbers_from = ?, edited = 1 WHERE id = ?", value, groupID); err != nil {
			return err
		}
		// Everything in it is numbered again, from the new first number;
		// without one, everything keeps its number.
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET numbered_in = NULL WHERE numbered_in = ?", groupID); err != nil {
			return err
		}
		return applyRules(ctx, tx, lineupID)
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: first number of group %d: %w", groupID, err)
	}
	return err
}

// SortGroupByName orders the channels of a group of the lineup by their
// names as devices see them, in natural order (D137): they take the
// numbers that they hold between them, the lowest for the first name.
// Channels hidden on their own, and numbers set by hand or by a rule, stay
// where they are. It returns how many channels it sorted, and ErrNotFound
// for a group that is not the lineup's.
func (s *Store) SortGroupByName(ctx context.Context, lineupID, groupID int64) (int, error) {
	var n int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
			return err
		}
		all, err := groupMembers(ctx, tx, lineupID, groupID)
		if err != nil {
			return err
		}
		var members []groupMember
		var numbers []int
		for _, m := range all {
			if m.hidden || m.number == 0 {
				continue
			}
			members = append(members, m)
			numbers = append(numbers, m.number)
		}
		slices.Sort(numbers)
		slices.SortStableFunc(members, func(a, b groupMember) int {
			return cmp.Or(natural.Compare(a.name, b.name), cmp.Compare(a.number, b.number))
		})
		// A number is held once at a time: those that move give theirs up
		// before any takes another.
		for i, m := range members {
			if m.number == numbers[i] {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET number = NULL WHERE id = ?", m.id); err != nil {
				return err
			}
		}
		for i, m := range members {
			if m.number == numbers[i] {
				continue
			}
			if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET number = ? WHERE id = ?", numbers[i], m.id); err != nil {
				return err
			}
			n++
		}
		return applyRules(ctx, tx, lineupID)
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return 0, fmt.Errorf("store: sort group %d: %w", groupID, err)
	}
	return n, err
}

// SetLineupGroupNames renames several groups of a lineup at once, by ID,
// as a find and replace in their names does (D137). It returns
// ErrNotFound if the lineup lacks a group, and ErrGroupExists if two of its
// groups would have the same name; then nothing changes.
func (s *Store) SetLineupGroupNames(ctx context.Context, lineupID int64, names map[int64]string) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for id := range names {
			if err := checkGroup(ctx, tx, lineupID, id); err != nil {
				return err
			}
		}
		// Every group first takes a name that no group can have, so that
		// two groups can swap names.
		for id := range names {
			if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET name = char(1) || id WHERE id = ?", id); err != nil {
				return err
			}
		}
		for id, name := range names {
			if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET name = ?, edited = 1 WHERE id = ?", name, id); err != nil {
				if isUniqueViolation(err) {
					return ErrGroupExists
				}
				return err
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrGroupExists) {
		return fmt.Errorf("store: rename groups of lineup %d: %w", lineupID, err)
	}
	return err
}

// RenameShelves gives shelves of films or of series the names that devices
// see them by, by their keys, at once, as a find and replace does (D137);
// "" gives a shelf back its sources' name. It fails with ErrBadName for a
// name that cannot be one, and then nothing changes.
func (s *Store) RenameShelves(ctx context.Context, kind string, names map[string]string) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if utf8.RuneCountInString(name) > MaxShelfName || strings.IndexFunc(name, unicode.IsControl) >= 0 {
			return ErrBadName
		}
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for key, name := range names {
			name = strings.TrimSpace(name)
			var value any = name
			if own, ok := strings.CutPrefix(key, "name:"); name == "" || ok && own == name {
				value = nil
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO shelf_settings (kind, shelf) VALUES (?, ?)
				ON CONFLICT (kind, shelf) DO NOTHING`, kind, key); err != nil {
				return fmt.Errorf("store: rename shelves: %w", err)
			}
			if _, err := tx.ExecContext(ctx, "UPDATE shelf_settings SET name = ? WHERE kind = ? AND shelf = ?", value, kind, key); err != nil {
				return fmt.Errorf("store: rename shelves: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shelf_settings
			WHERE kind = ? AND hidden = 0 AND locked = 0 AND name IS NULL AND position IS NULL`, kind); err != nil {
			return fmt.Errorf("store: rename shelves: %w", err)
		}
		return nil
	})
}
