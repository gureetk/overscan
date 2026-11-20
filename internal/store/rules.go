// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"overscan.gureet.ca/internal/rules"
)

// A lineup's rules (D107): ordered rules that match channels by their
// source, group, name, guide ID or quality, and show, hide, rename,
// regroup, number or give a logo to those they match. They run in order
// after every refresh of a source, and whenever a rule changes; a later
// rule that does the same as an earlier one wins. What they do is kept
// apart from the administrator's own edits (D47), which win over it.

// ErrTooManyRules is the error for a lineup with as many rules as it may
// have.
var ErrTooManyRules = fmt.Errorf("store: a lineup has at most %d rules", rules.MaxRules)

// LineupRule is a rule of a lineup, with its place among them.
type LineupRule struct {
	ID       int64
	Position int // from 1
	rules.Rule
	// Matches counts the channels of the lineup that it matches.
	Matches int
}

// LineupRules returns a lineup's rules, in order, each with how many
// channels it matches.
func (s *Store) LineupRules(ctx context.Context, lineupID int64) ([]LineupRule, error) {
	var out []LineupRule
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		list, err := lineupRules(ctx, tx, lineupID)
		if err != nil {
			return err
		}
		entries, err := ruleEntries(ctx, tx, lineupID)
		if err != nil {
			return err
		}
		for _, r := range list {
			m, err := rules.Compile(r.Match)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if !e.missing && m.Matches(e.ch) {
					r.Matches++
				}
			}
			out = append(out, r)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("store: rules of lineup %d: %w", lineupID, err)
	}
	if out == nil {
		out = []LineupRule{}
	}
	return out, nil
}

// lineupRules reads a lineup's rules, in order.
func lineupRules(ctx context.Context, tx *sql.Tx, lineupID int64) ([]LineupRule, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, position, spec FROM lineup_rules WHERE lineup_id = ? ORDER BY position", lineupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LineupRule
	for rows.Next() {
		var r LineupRule
		var spec string
		if err := rows.Scan(&r.ID, &r.Position, &spec); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(spec), &r.Rule); err != nil {
			return nil, fmt.Errorf("rule %d: %w", r.ID, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// checkRule checks a rule as internal/rules does, for the store.
func checkRule(r rules.Rule) (string, error) {
	r, err := rules.Check(r)
	if err != nil {
		return "", err
	}
	spec, err := json.Marshal(r)
	return string(spec), err
}

// ruleSource checks that the source a rule names, if any, exists.
func ruleSource(ctx context.Context, tx *sql.Tx, r rules.Rule) error {
	if r.Match.Source == 0 {
		return nil
	}
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM sources WHERE id = ?)", r.Match.Source).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return &rules.Error{Text: "There is no such source."}
	}
	return nil
}

// dropSourceRules removes a lineup's rules that name a source that is gone,
// since a new source can take its ID, and runs those left.
func dropSourceRules(ctx context.Context, tx *sql.Tx, lineupID, sourceID int64) error {
	res, err := tx.ExecContext(ctx, "DELETE FROM lineup_rules WHERE lineup_id = ? AND json_extract(spec, '$.match.source') = ?",
		lineupID, sourceID)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return err
	}
	if err := orderRules(ctx, tx, lineupID, 0, 0); err != nil {
		return err
	}
	return applyRules(ctx, tx, lineupID)
}

// AddLineupRule adds a rule after a lineup's others, runs the lineup's
// rules, and returns its ID. A rule that internal/rules refuses, or that
// names a source that does not exist, gives a *rules.Error.
func (s *Store) AddLineupRule(ctx context.Context, lineupID int64, r rules.Rule) (int64, error) {
	spec, err := checkRule(r)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM lineups WHERE id = ?)", lineupID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM lineup_rules WHERE lineup_id = ?", lineupID).Scan(&n); err != nil {
			return err
		}
		if n >= rules.MaxRules {
			return ErrTooManyRules
		}
		if err := ruleSource(ctx, tx, r); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, "INSERT INTO lineup_rules (lineup_id, position, spec) VALUES (?, ?, ?) RETURNING id",
			lineupID, n+1, spec).Scan(&id); err != nil {
			return err
		}
		return applyRules(ctx, tx, lineupID)
	})
	return id, ruleStoreError(err, "add a rule to lineup", lineupID)
}

// ruleStoreError wraps an error about rules, leaving the store's own
// errors as they are.
func ruleStoreError(err error, what string, lineupID int64) error {
	var bad *rules.Error
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooManyRules) || errors.As(err, &bad) {
		return err
	}
	return fmt.Errorf("store: %s %d: %w", what, lineupID, err)
}

// SetLineupRule replaces a rule of a lineup, and runs its rules.
func (s *Store) SetLineupRule(ctx context.Context, lineupID, ruleID int64, r rules.Rule) error {
	spec, err := checkRule(r)
	if err != nil {
		return err
	}
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		if err := ruleSource(ctx, tx, r); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE lineup_rules SET spec = ? WHERE id = ? AND lineup_id = ?", spec, ruleID, lineupID)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		return applyRules(ctx, tx, lineupID)
	})
	return ruleStoreError(err, "change a rule of lineup", lineupID)
}

// RemoveLineupRule removes a rule of a lineup, and runs its rules.
func (s *Store) RemoveLineupRule(ctx context.Context, lineupID, ruleID int64) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM lineup_rules WHERE id = ? AND lineup_id = ?", ruleID, lineupID)
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		if err := orderRules(ctx, tx, lineupID, 0, 0); err != nil {
			return err
		}
		return applyRules(ctx, tx, lineupID)
	})
	return ruleStoreError(err, "remove a rule of lineup", lineupID)
}

// MoveLineupRule moves a rule of a lineup to another place, from 1, and
// runs its rules.
func (s *Store) MoveLineupRule(ctx context.Context, lineupID, ruleID int64, position int) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM lineup_rules WHERE id = ? AND lineup_id = ?)",
			ruleID, lineupID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		if err := orderRules(ctx, tx, lineupID, ruleID, position); err != nil {
			return err
		}
		return applyRules(ctx, tx, lineupID)
	})
	return ruleStoreError(err, "move a rule of lineup", lineupID)
}

// orderRules numbers a lineup's rules from 1 in their order, with the rule
// move, if not 0, at place to.
func orderRules(ctx context.Context, tx *sql.Tx, lineupID, move int64, to int) error {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM lineup_rules WHERE lineup_id = ? ORDER BY position", lineupID)
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
		if id != move {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if move != 0 {
		to = min(max(to, 1), len(ids)+1)
		ids = slices.Insert(ids, to-1, move)
	}
	// Out of the way first, so that no place is held twice meanwhile.
	if _, err := tx.ExecContext(ctx, "UPDATE lineup_rules SET position = position + 1000000 WHERE lineup_id = ?", lineupID); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_rules SET position = ? WHERE id = ?", i+1, id); err != nil {
			return err
		}
	}
	return nil
}

// ruleEntry is an entry of a lineup as its rules see it, with what they
// did to it when they last ran.
type ruleEntry struct {
	id       int64
	number   int // 0 if none
	pinned   bool
	missing  bool
	source   int64
	position int
	ch       rules.Channel

	hidden     sql.NullInt64
	name       sql.NullString
	groupID    sql.NullInt64
	logo       sql.NullString
	rulePinned bool
}

// ruleEntries reads every entry of a lineup with its channel.
func ruleEntries(ctx context.Context, tx *sql.Tx, lineupID int64) ([]ruleEntry, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, COALESCE(e.number, 0), e.pinned, c.missing_since IS NOT NULL, c.source_id, c.position,
			COALESCE(k.name, ''), c.name, c.xmltv_id,
			e.rule_hidden, e.rule_name, e.rule_group_id, e.rule_logo, e.rule_pinned
		FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
			LEFT JOIN categories k ON k.id = c.category_id
		WHERE e.lineup_id = ?`, lineupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ruleEntry
	for rows.Next() {
		var e ruleEntry
		if err := rows.Scan(&e.id, &e.number, &e.pinned, &e.missing, &e.source, &e.position,
			&e.ch.Category, &e.ch.Name, &e.ch.Guide,
			&e.hidden, &e.name, &e.groupID, &e.logo, &e.rulePinned); err != nil {
			return nil, err
		}
		e.ch.Source = e.source
		out = append(out, e)
	}
	return out, rows.Err()
}

// applyRules runs a lineup's rules: it works out what they do to each
// entry, writes what changed, numbers what they number, and removes the
// groups that only they made and that nothing is in any more.
func applyRules(ctx context.Context, tx *sql.Tx, lineupID int64) error {
	list, err := lineupRules(ctx, tx, lineupID)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		// Without rules, nothing of theirs is left.
		if _, err := tx.ExecContext(ctx, `
			UPDATE lineup_entries SET rule_hidden = NULL, rule_name = NULL, rule_group_id = NULL, rule_logo = NULL,
				rule_pinned = 0
			WHERE lineup_id = ? AND (rule_hidden IS NOT NULL OR rule_name IS NOT NULL OR rule_group_id IS NOT NULL
				OR rule_logo IS NOT NULL OR rule_pinned = 1)`, lineupID); err != nil {
			return err
		}
		return removeEmptyGroups(ctx, tx, lineupID)
	}
	entries, err := ruleEntries(ctx, tx, lineupID)
	if err != nil {
		return err
	}

	type effect struct {
		hidden sql.NullInt64
		name   sql.NullString
		group  string // the name of the group a rule moves it to, "" if none
		logo   sql.NullString
	}
	effects := make([]effect, len(entries))
	var numberings []numbering
	for _, r := range list {
		if r.Off {
			continue
		}
		m, err := rules.Compile(r.Match)
		if err != nil {
			return fmt.Errorf("rule %d: %w", r.ID, err)
		}
		var rename rules.Renamer
		if r.Action == rules.Rename {
			if rename, err = rules.CompileRename(r.Rule); err != nil {
				return fmt.Errorf("rule %d: %w", r.ID, err)
			}
		}
		var matched []int
		for i, e := range entries {
			if !m.Matches(e.ch) {
				continue
			}
			ef := &effects[i]
			switch r.Action {
			case rules.Include:
				ef.hidden = sql.NullInt64{Int64: 0, Valid: true}
			case rules.Exclude:
				ef.hidden = sql.NullInt64{Int64: 1, Valid: true}
			case rules.Rename:
				name := e.ch.Name
				if ef.name.Valid {
					name = ef.name.String
				}
				if name = rename(name); name != e.ch.Name {
					ef.name = sql.NullString{String: name, Valid: true}
				} else {
					ef.name = sql.NullString{}
				}
			case rules.Regroup:
				ef.group = r.Group
			case rules.Logo:
				ef.logo = sql.NullString{String: r.Logo, Valid: true}
			case rules.Renumber:
				matched = append(matched, i)
			}
		}
		if r.Action == rules.Renumber {
			numberings = append(numberings, numbering{from: r.From, entries: matched})
		}
	}

	// The groups that rules move channels to: the lineup's group of that
	// name, whatever its case, or else a new one.
	groups := map[string]int64{}
	for _, ef := range effects {
		if ef.group == "" {
			continue
		}
		if _, ok := groups[ef.group]; ok {
			continue
		}
		var id int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM lineup_groups WHERE lineup_id = ? AND name = ? COLLATE NOCASE
			ORDER BY name = ? DESC, id LIMIT 1`, lineupID, ef.group, ef.group).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			err = tx.QueryRowContext(ctx, "INSERT INTO lineup_groups (lineup_id, name) VALUES (?, ?) RETURNING id",
				lineupID, ef.group).Scan(&id)
		}
		if err != nil {
			return err
		}
		groups[ef.group] = id
	}

	update, err := tx.PrepareContext(ctx,
		"UPDATE lineup_entries SET rule_hidden = ?, rule_name = ?, rule_group_id = ?, rule_logo = ? WHERE id = ?")
	if err != nil {
		return err
	}
	defer update.Close()
	for i, e := range entries {
		ef := effects[i]
		var group sql.NullInt64
		if ef.group != "" {
			group = sql.NullInt64{Int64: groups[ef.group], Valid: true}
		}
		if ef.hidden == e.hidden && ef.name == e.name && group == e.groupID && ef.logo == e.logo {
			continue
		}
		if _, err := update.ExecContext(ctx, ef.hidden, ef.name, group, ef.logo, e.id); err != nil {
			return err
		}
	}
	if err := numberByRules(ctx, tx, lineupID, entries, numberings); err != nil {
		return err
	}
	return removeEmptyGroups(ctx, tx, lineupID)
}

// numbering is what a renumbering rule matched: the entries it numbers,
// as indexes into the lineup's entries, from its first number.
type numbering struct {
	from    int
	entries []int
}

// numberByRules gives the channels that renumbering rules match their
// numbers: each rule numbers its channels from its first number, those
// that it numbered before first, in the order of their numbers, then the
// others in the order of theirs, those without one last in their sources'
// order, skipping the numbers that the administrator pinned and those that
// an earlier rule gave. A channel that the administrator numbered keeps its
// number, and one that a source no longer lists is left alone. A channel
// that held a number that a rule gives another goes after the highest
// number, as D40 numbers a channel that cannot have the number it wants.
// A number that rules gave stays pinned for D40's numbering.
func numberByRules(ctx context.Context, tx *sql.Tx, lineupID int64, entries []ruleEntry, numberings []numbering) error {
	held := map[int]int{} // number: index into entries
	for i, e := range entries {
		if e.number > 0 {
			held[e.number] = i
		}
	}
	target := map[int]int{} // index into entries: the number a rule gives it
	given := map[int]bool{} // numbers that rules give
	for _, n := range numberings {
		var picked []int
		for _, i := range n.entries {
			if _, done := target[i]; !done && !entries[i].pinned && !entries[i].missing {
				picked = append(picked, i)
			}
		}
		// Those that rules numbered before keep their order, so that
		// their numbers stay; the others follow, by their numbers, then
		// those without one in their sources' order.
		slices.SortStableFunc(picked, func(a, b int) int {
			x, y := entries[a], entries[b]
			switch {
			case x.rulePinned != y.rulePinned:
				if x.rulePinned {
					return -1
				}
				return 1
			case x.number > 0 && y.number > 0:
				return cmp.Compare(x.number, y.number)
			case x.number > 0:
				return -1
			case y.number > 0:
				return 1
			}
			return cmp.Or(cmp.Compare(x.source, y.source), cmp.Compare(x.position, y.position))
		})
		next := n.from
		for _, i := range picked {
			for next <= MaxNumber {
				h, taken := held[next]
				if !given[next] && (!taken || !entries[h].pinned) {
					break
				}
				next++
			}
			if next > MaxNumber {
				break
			}
			target[i] = next
			given[next] = true
			next++
		}
	}

	// What changes: numbers that rules no longer give stay, unpinned;
	// entries whose number a rule gives elsewhere make way.
	var move, unpin, pin []int
	for i, e := range entries {
		n, ok := target[i]
		switch {
		case !ok && e.rulePinned:
			unpin = append(unpin, i)
		case ok && n == e.number && !e.rulePinned:
			pin = append(pin, i)
		case ok && n != e.number:
			move = append(move, i)
		}
	}
	if len(move) == 0 && len(unpin) == 0 && len(pin) == 0 {
		return nil
	}
	// Every entry that moves, and every entry that holds a number that one
	// moves to, gives its number up first, so that no number is held twice
	// meanwhile; one that no rule numbers is numbered again below.
	freed := map[int]bool{}
	for _, i := range move {
		freed[i] = true
		if h, taken := held[target[i]]; taken {
			freed[h] = true
		}
	}
	for i := range freed {
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET number = NULL, rule_pinned = 0 WHERE id = ?", entries[i].id); err != nil {
			return err
		}
	}
	for _, i := range move {
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET number = ?, rule_pinned = 1 WHERE id = ?", target[i], entries[i].id); err != nil {
			return err
		}
	}
	for _, i := range pin {
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET rule_pinned = 1 WHERE id = ?", entries[i].id); err != nil {
			return err
		}
	}
	for _, i := range unpin {
		if freed[i] {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET rule_pinned = 0 WHERE id = ?", entries[i].id); err != nil {
			return err
		}
	}
	// The entries that made way get numbers again, as D40 gives them.
	sources := map[int64]bool{}
	for i := range freed {
		if _, ruled := target[i]; !ruled {
			sources[entries[i].source] = true
		}
	}
	for src := range sources {
		if err := numberChannels(ctx, tx, lineupID, src, false); err != nil {
			return err
		}
	}
	return nil
}
