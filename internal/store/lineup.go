// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/ncruces/go-sqlite3"
)

// Lineup edits (D47): the administrator's changes to a lineup's groups and
// entries, which refreshes leave alone.

// Errors about lineup edits.
var (
	ErrGroupExists   = errors.New("store: the lineup has a group of that name")
	ErrGroupNotEmpty = errors.New("store: the group is not empty")
	ErrBadNumber     = fmt.Errorf("store: a number runs from 1 to %d", MaxNumber)
)

// Visibility is the administrator's choice of whether an entry is shown.
type Visibility int

// Visibilities.
const (
	AsGroup Visibility = iota // shown unless its group is hidden
	Shown                     // shown, even in a hidden group
	Hidden                    // hidden
)

// LineupGroup is a group of a lineup, as the admin edits it.
type LineupGroup struct {
	ID          int64 // 0 for the channels in no group
	Name        string
	Hidden      bool
	Locked      bool // its channels need the parental PIN (D55)
	Edited      bool // made or changed by the administrator
	Channels    int  // channels in it that their sources list
	Shown       int  // of those, the ones devices see
	FirstNumber int  // the lowest number devices see in it, 0 if none
	Categories  []GroupCategory
}

// GroupCategory is a source's category that a group holds.
type GroupCategory struct {
	ID       int64 // 0 if the source no longer has the category
	SourceID int64
	Source   string
	Name     string
}

// LineupGroups returns a lineup's groups: those with channels that devices
// see, in the order of their first channel, then the others by name. A group
// with ID 0 holds the channels in no group, if there are any.
func (s *Store) LineupGroups(ctx context.Context, lineupID int64) ([]LineupGroup, error) {
	fail := func(err error) ([]LineupGroup, error) {
		return nil, fmt.Errorf("store: groups of lineup %d: %w", lineupID, err)
	}
	byID := map[int64]*LineupGroup{}
	var order []int64
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, hidden, locked, edited FROM lineup_groups WHERE lineup_id = ?", lineupID)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		g := &LineupGroup{}
		if err := rows.Scan(&g.ID, &g.Name, &g.Hidden, &g.Locked, &g.Edited); err != nil {
			rows.Close()
			return fail(err)
		}
		byID[g.ID] = g
		order = append(order, g.ID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT COALESCE(g.id, 0), count(*),
			COALESCE(sum(e.number IS NOT NULL AND COALESCE(e.hidden, g.hidden, 0) = 0
				AND NOT (c.adult = 1 AND `+adultMarksSQL+` = '`+AdultHide+`')), 0),
			COALESCE(min(CASE WHEN COALESCE(e.hidden, g.hidden, 0) = 0 THEN e.number END), 0)`+lineupEntryJoins+`
		WHERE e.lineup_id = ? AND c.missing_since IS NULL AND s.enabled = 1
		GROUP BY COALESCE(g.id, 0)`,
		lineupID)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var id int64
		var channels, shown, first int
		if err := rows.Scan(&id, &channels, &shown, &first); err != nil {
			rows.Close()
			return fail(err)
		}
		g, ok := byID[id]
		if !ok { // the channels in no group
			g = &LineupGroup{}
			byID[0] = g
			order = append(order, 0)
		}
		g.Channels, g.Shown, g.FirstNumber = channels, shown, first
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT m.group_id, COALESCE(k.id, 0), m.source_id, src.name, m.category
		FROM lineup_categories m
		JOIN sources src ON src.id = m.source_id
		LEFT JOIN categories k ON k.source_id = m.source_id AND k.kind = 'live' AND k.name = m.category
		WHERE m.lineup_id = ?
		ORDER BY src.name, m.category`,
		lineupID)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var groupID int64
		var c GroupCategory
		if err := rows.Scan(&groupID, &c.ID, &c.SourceID, &c.Source, &c.Name); err != nil {
			rows.Close()
			return fail(err)
		}
		if g, ok := byID[groupID]; ok {
			g.Categories = append(g.Categories, c)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}

	out := make([]LineupGroup, 0, len(order))
	for _, id := range order {
		out = append(out, *byID[id])
	}
	sortGroups(out)
	return out, nil
}

// sortGroups orders groups as LineupGroups returns them.
func sortGroups(gs []LineupGroup) {
	less := func(a, b LineupGroup) bool {
		switch {
		case (a.FirstNumber > 0) != (b.FirstNumber > 0):
			return a.FirstNumber > 0
		case a.FirstNumber != b.FirstNumber:
			return a.FirstNumber < b.FirstNumber
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	}
	for i := 1; i < len(gs); i++ {
		for j := i; j > 0 && less(gs[j], gs[j-1]); j-- {
			gs[j], gs[j-1] = gs[j-1], gs[j]
		}
	}
}

// inTx runs fn in a transaction, which it commits if fn succeeds.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// checkGroup returns ErrNotFound unless the lineup has the group.
func checkGroup(ctx context.Context, tx *sql.Tx, lineupID, groupID int64) error {
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM lineup_groups WHERE id = ? AND lineup_id = ?", groupID, lineupID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// groupNameError turns a clash of group names into ErrGroupExists: the
// statements it is used for have no other unique constraint.
func groupNameError(err error) error {
	if errors.Is(err, sqlite3.CONSTRAINT_UNIQUE) {
		return ErrGroupExists
	}
	return err
}

// CreateLineupGroup makes a group of the administrator's in a lineup and
// returns its ID.
func (s *Store) CreateLineupGroup(ctx context.Context, lineupID int64, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx,
		"INSERT INTO lineup_groups (lineup_id, name, edited) VALUES (?, ?, 1) RETURNING id",
		lineupID, name).Scan(&id)
	if err = groupNameError(err); err != nil {
		return 0, err
	}
	return id, nil
}

// RenameLineupGroup renames a group.
func (s *Store) RenameLineupGroup(ctx context.Context, lineupID, groupID int64, name string) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE lineup_groups SET name = ?, edited = 1 WHERE id = ? AND lineup_id = ?", name, groupID, lineupID)
	if err = groupNameError(err); err != nil {
		return err
	}
	return mustAffect(res)
}

// SetLineupGroupHidden hides a group, and the channels in it that are not
// shown on their own, or shows it.
func (s *Store) SetLineupGroupHidden(ctx context.Context, lineupID, groupID int64, hidden bool) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE lineup_groups SET hidden = ?, edited = 1 WHERE id = ? AND lineup_id = ?", hidden, groupID, lineupID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// SetLineupGroupLocked locks or unlocks a group: a locked group's channels
// need the parental PIN (D55).
func (s *Store) SetLineupGroupLocked(ctx context.Context, lineupID, groupID int64, locked bool) error {
	res, err := s.db.ExecContext(ctx,
		"UPDATE lineup_groups SET locked = ?, edited = 1 WHERE id = ? AND lineup_id = ?", locked, groupID, lineupID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// MergeLineupGroup moves everything in group from, its categories and the
// channels moved there, into group into, and removes from.
func (s *Store) MergeLineupGroup(ctx context.Context, lineupID, from, into int64) error {
	if from == into {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range []int64{from, into} {
			if err := checkGroup(ctx, tx, lineupID, id); err != nil {
				return err
			}
		}
		for _, st := range []struct {
			q    string
			args []any
		}{
			{"UPDATE lineup_categories SET group_id = ? WHERE group_id = ?", []any{into, from}},
			{"UPDATE lineup_entries SET group_id = ? WHERE group_id = ?", []any{into, from}},
			{"UPDATE lineup_groups SET edited = 1 WHERE id = ?", []any{into}},
			{"DELETE FROM lineup_groups WHERE id = ?", []any{from}},
		} {
			if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// DeleteLineupGroup removes a group that holds no category that its source
// still has and no channel moved there; otherwise it returns
// ErrGroupNotEmpty.
func (s *Store) DeleteLineupGroup(ctx context.Context, lineupID, groupID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `
			SELECT (SELECT count(*) FROM lineup_categories m
					JOIN categories k ON k.source_id = m.source_id AND k.kind = 'live' AND k.name = m.category
					WHERE m.group_id = ?1)
				+ (SELECT count(*) FROM lineup_entries WHERE group_id = ?1)`,
			groupID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrGroupNotEmpty
		}
		_, err := tx.ExecContext(ctx, "DELETE FROM lineup_groups WHERE id = ?", groupID)
		return err
	})
}

// MoveCategory puts a source's category, by its ID, in a group of the
// lineup.
func (s *Store) MoveCategory(ctx context.Context, lineupID, categoryID, groupID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
			return err
		}
		var sourceID int64
		var name string
		err := tx.QueryRowContext(ctx, "SELECT source_id, name FROM categories WHERE id = ? AND kind = 'live'", categoryID).Scan(&sourceID, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (?, ?, ?, ?)
			ON CONFLICT (lineup_id, source_id, category) DO UPDATE SET group_id = excluded.group_id`,
			lineupID, sourceID, name, groupID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET edited = 1 WHERE id = ?", groupID); err != nil {
			return err
		}
		return removeEmptyGroups(ctx, tx, lineupID)
	})
}

// LineupEntry is a lineup entry as the admin edits it.
type LineupEntry struct {
	ID          int64
	ChannelID   int64
	SourceID    int64
	Source      string // the source's name
	Number      int    // 0 if it has none
	Pinned      bool   // the administrator set the number
	Name        string // as devices see it
	ChannelName string // as the source names it
	Renamed     bool   // the administrator named it
	GroupID     int64  // as devices see it, 0 if none
	Group       string
	Moved       bool   // the administrator chose its group
	Category    string // the source's category, "" if none
	Visibility  Visibility
	Hidden      bool // devices do not see it, for whatever reason
	Missing     bool // its source no longer lists it
	Logo        string
	Locked      bool  // the administrator locked it (D55)
	GroupLocked bool  // its group is locked
	Repair      bool  // its stream goes through ffmpeg: its own switch says so, or else the default does (D57, D58)
	RepairOwn   *bool // its own switch; nil if it follows the default
	// Adult is set when its source marks it for adults (D86), and
	// AdultLocked or AdultHidden when the setting locks or hides such
	// channels.
	Adult, AdultLocked, AdultHidden bool
	Radio                           bool // its source lists it as a radio station (D87)
}

// EntryQuery picks a lineup's entries.
type EntryQuery struct {
	Group    *int64 // only the entries in this group (0: in none); nil for all
	NotGroup *int64 // only the entries in other groups than this, or in none
	Text     string // only the entries whose name holds this, in any case
	Offset   int
	Limit    int // 0 for no limit
}

// LineupEntries returns the entries of a lineup that the query picks, by
// number, with the unnumbered last, and how many there are in all. Entries
// of disabled sources are left out.
func (s *Store) LineupEntries(ctx context.Context, lineupID int64, q EntryQuery) ([]LineupEntry, int, error) {
	where := " WHERE e.lineup_id = ? AND s.enabled = 1"
	args := []any{lineupID}
	if q.Group != nil {
		where += " AND COALESCE(g.id, 0) = ?"
		args = append(args, *q.Group)
	}
	if q.NotGroup != nil {
		where += " AND COALESCE(g.id, 0) <> ?"
		args = append(args, *q.NotGroup)
	}
	if q.Text != "" {
		where += ` AND COALESCE(e.name, c.name) LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscaper.Replace(q.Text)+"%")
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+lineupEntryJoins+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: entries of lineup %d: %w", lineupID, err)
	}
	limit := q.Limit
	if limit <= 0 {
		limit = -1
	}
	rows, err := s.db.QueryContext(ctx, entrySelect+lineupEntryJoins+where+`
		ORDER BY e.number IS NULL, e.number, COALESCE(e.name, c.name), e.id
		LIMIT ? OFFSET ?`, append(args, limit, max(q.Offset, 0))...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: entries of lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	var out []LineupEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: entries of lineup %d: %w", lineupID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: entries of lineup %d: %w", lineupID, err)
	}
	return out, total, nil
}

// LineupEntry returns an entry of a lineup, or ErrNotFound.
func (s *Store) LineupEntry(ctx context.Context, lineupID, entryID int64) (LineupEntry, error) {
	e, err := scanEntry(s.db.QueryRowContext(ctx, entrySelect+lineupEntryJoins+" WHERE e.lineup_id = ? AND e.id = ?", lineupID, entryID))
	if errors.Is(err, sql.ErrNoRows) {
		return LineupEntry{}, ErrNotFound
	}
	if err != nil {
		return LineupEntry{}, fmt.Errorf("store: lineup entry %d: %w", entryID, err)
	}
	return e, nil
}

const entrySelect = `
		SELECT e.id, c.id, c.source_id, s.name, COALESCE(e.number, 0), e.pinned, COALESCE(e.name, c.name), c.name,
			e.name IS NOT NULL, COALESCE(g.id, 0), COALESCE(g.name, ''), e.group_id IS NOT NULL, COALESCE(k.name, ''),
			CASE e.hidden WHEN 0 THEN 1 WHEN 1 THEN 2 ELSE 0 END,
			e.number IS NULL OR COALESCE(e.hidden, g.hidden, 0) = 1 OR c.missing_since IS NOT NULL,
			c.missing_since IS NOT NULL, c.logo, e.locked, COALESCE(g.locked, 0),
			COALESCE(c.repair, ` + repairDefaultSQL + `), c.repair, c.adult,
			c.adult = 1 AND ` + adultMarksSQL + ` = '` + AdultLock + `', c.adult = 1 AND ` + adultMarksSQL + ` = '` + AdultHide + `', c.radio`

func scanEntry(row rowScanner) (LineupEntry, error) {
	var e LineupEntry
	var own sql.NullBool
	err := row.Scan(&e.ID, &e.ChannelID, &e.SourceID, &e.Source, &e.Number, &e.Pinned, &e.Name, &e.ChannelName,
		&e.Renamed, &e.GroupID, &e.Group, &e.Moved, &e.Category, &e.Visibility, &e.Hidden, &e.Missing, &e.Logo,
		&e.Locked, &e.GroupLocked, &e.Repair, &own, &e.Adult, &e.AdultLocked, &e.AdultHidden, &e.Radio)
	if own.Valid {
		e.RepairOwn = &own.Bool
	}
	return e, err
}

var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

// mustAffect returns ErrNotFound if a statement changed no row.
func mustAffect(res sql.Result) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// RenameEntry names an entry's channel in the lineup; an empty name gives it
// its source's name again.
func (s *Store) RenameEntry(ctx context.Context, lineupID, entryID int64, name string) error {
	var value any
	if name != "" {
		value = name
	}
	res, err := s.db.ExecContext(ctx, "UPDATE lineup_entries SET name = ? WHERE id = ? AND lineup_id = ?", value, entryID, lineupID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// SetEntriesGroup moves entries to a group of the lineup; group 0 puts them
// back in their categories' groups. It returns ErrNotFound if the lineup
// lacks the group or any of the entries.
func (s *Store) SetEntriesGroup(ctx context.Context, lineupID int64, entryIDs []int64, groupID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var value any
		if groupID != 0 {
			if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET edited = 1 WHERE id = ?", groupID); err != nil {
				return err
			}
			value = groupID
		}
		for _, id := range entryIDs {
			res, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET group_id = ? WHERE id = ? AND lineup_id = ?", value, id, lineupID)
			if err != nil {
				return err
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return removeEmptyGroups(ctx, tx, lineupID)
	})
}

// SetEntriesVisibility sets whether entries are shown. It returns
// ErrNotFound if the lineup lacks any of them.
func (s *Store) SetEntriesVisibility(ctx context.Context, lineupID int64, entryIDs []int64, v Visibility) error {
	var value any
	switch v {
	case Shown:
		value = 0
	case Hidden:
		value = 1
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range entryIDs {
			res, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET hidden = ? WHERE id = ? AND lineup_id = ?", value, id, lineupID)
			if err != nil {
				return err
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetEntriesLocked locks or unlocks entries (D55). An entry in a locked group
// stays locked whatever its own lock.
func (s *Store) SetEntriesLocked(ctx context.Context, lineupID int64, entryIDs []int64, locked bool) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range entryIDs {
			res, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET locked = ? WHERE id = ? AND lineup_id = ?", locked, id, lineupID)
			if err != nil {
				return err
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return nil
	})
}

// LockedEntries counts the lineup's channels locked one by one, not with
// their group (D55).
func (s *Store) LockedEntries(ctx context.Context, lineupID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM lineup_entries WHERE lineup_id = ? AND locked = 1", lineupID).Scan(&n)
	return n, err
}

// SetEntriesRepair sets the repair switch of the channels of entries
// (D57): repaired or not, or, with nil, as the default (D58). The switch
// belongs to the channel, so it holds in every lineup that has it.
func (s *Store) SetEntriesRepair(ctx context.Context, lineupID int64, entryIDs []int64, repair *bool) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range entryIDs {
			res, err := tx.ExecContext(ctx, `
				UPDATE channels SET repair = ?
				WHERE id = (SELECT channel_id FROM lineup_entries WHERE id = ? AND lineup_id = ?)`, repair, id, lineupID)
			if err != nil {
				return err
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return nil
	})
}

// PinEntryNumber gives an entry a number and pins it (D47): renumbering no
// longer moves it, and no other channel takes it. The entry that held the
// number takes the pinned entry's old number, or, if it had none, goes after
// the highest.
func (s *Store) PinEntryNumber(ctx context.Context, lineupID, entryID int64, number int) error {
	return s.inTx(ctx, func(tx *sql.Tx) error { return pinNumber(ctx, tx, lineupID, entryID, number) })
}

func pinNumber(ctx context.Context, tx *sql.Tx, lineupID, entryID int64, number int) error {
	if number < 1 || number > MaxNumber {
		return ErrBadNumber
	}
	var cur sql.NullInt64
	err := tx.QueryRowContext(ctx, "SELECT number FROM lineup_entries WHERE id = ? AND lineup_id = ?", entryID, lineupID).Scan(&cur)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if cur.Valid && cur.Int64 == int64(number) {
		_, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET pinned = 1 WHERE id = ?", entryID)
		return err
	}
	var holder int64
	err = tx.QueryRowContext(ctx, "SELECT id FROM lineup_entries WHERE lineup_id = ? AND number = ?", lineupID, number).Scan(&holder)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET pinned = 0, number = NULL WHERE id = ?", entryID); err != nil {
		return err
	}
	if holder != 0 {
		var err error
		if cur.Valid {
			_, err = tx.ExecContext(ctx, "UPDATE lineup_entries SET number = ? WHERE id = ?", cur.Int64, holder)
		} else {
			_, err = tx.ExecContext(ctx, `
				UPDATE lineup_entries SET number = (SELECT max(number) + 1 FROM lineup_entries WHERE lineup_id = ?)
				WHERE id = ?`, lineupID, holder)
		}
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE lineup_entries SET number = ?, pinned = 1 WHERE id = ?", number, entryID)
	return err
}

// ResetEntryNumber returns a pinned entry to D40's numbering: it takes the
// number its channel wants if no listed channel holds it, and otherwise keeps
// the number it has, unpinned.
func (s *Store) ResetEntryNumber(ctx context.Context, lineupID, entryID int64) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var cur sql.NullInt64
		var pinned, missing bool
		var own, position int
		var numbers string
		err := tx.QueryRowContext(ctx, `
			SELECT e.number, e.pinned, c.missing_since IS NOT NULL, c.number, c.position, s.numbers
			FROM lineup_entries e JOIN channels c ON c.id = e.channel_id JOIN sources s ON s.id = c.source_id
			WHERE e.id = ? AND e.lineup_id = ?`,
			entryID, lineupID).Scan(&cur, &pinned, &missing, &own, &position, &numbers)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil || !pinned {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET pinned = 0 WHERE id = ?", entryID); err != nil {
			return err
		}
		wants := own
		if numbers == NumbersPlaylist {
			wants = position
		}
		if missing || wants < 1 || wants > MaxNumber || (cur.Valid && cur.Int64 == int64(wants)) {
			return nil
		}
		var holder int64
		var holderPinned, holderMissing bool
		err = tx.QueryRowContext(ctx, `
			SELECT e.id, e.pinned, c.missing_since IS NOT NULL
			FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
			WHERE e.lineup_id = ? AND e.number = ?`,
			lineupID, wants).Scan(&holder, &holderPinned, &holderMissing)
		switch {
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return err
		case holderPinned || !holderMissing:
			return nil // a listed channel, or a pinned one, holds it
		default:
			if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET number = NULL WHERE id = ?", holder); err != nil {
				return err
			}
		}
		_, err = tx.ExecContext(ctx, "UPDATE lineup_entries SET number = ? WHERE id = ?", wants, entryID)
		return err
	})
}

// NumberGroupFrom pins the numbers of the channels that devices see in a
// group to from, from+1 and so on, in their present order, swapping as
// PinEntryNumber does, and returns how many it numbered.
func (s *Store) NumberGroupFrom(ctx context.Context, lineupID, groupID int64, from int) (int, error) {
	var n int
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT e.id"+lineupEntryJoins+`
			WHERE e.lineup_id = ? AND g.id = ? AND `+shownEntry+`
			ORDER BY e.number`,
			lineupID, groupID)
		if err != nil {
			return err
		}
		var ids []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if from < 1 || from+len(ids)-1 > MaxNumber {
			return ErrBadNumber
		}
		for i, id := range ids {
			if err := pinNumber(ctx, tx, lineupID, id, from+i); err != nil {
				return err
			}
		}
		n = len(ids)
		return nil
	})
	return n, err
}

// SetNewCategoriesHidden sets whether the categories that a source adds
// later start hidden in the lineup (D47).
func (s *Store) SetNewCategoriesHidden(ctx context.Context, sourceID int64, hidden bool) error {
	res, err := s.db.ExecContext(ctx, "UPDATE sources SET new_categories_hidden = ? WHERE id = ?", hidden, sourceID)
	if err != nil {
		return err
	}
	return mustAffect(res)
}
