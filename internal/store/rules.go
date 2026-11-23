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
	"strings"

	"overscan.gureet.ca/internal/rules"
)

// A lineup's rules (D107): ordered rules that match channels by their
// source, group, name, guide ID or quality, and show, hide, rename,
// regroup, number or give a logo to those they match, or show another
// channel's guide on them or move their guide's times (D108). They run in order
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
	// For a merge rule (D113): CopiesMerged counts the channels that it
	// merges into others, and ChannelsMerged the channels that it merges
	// them into.
	CopiesMerged, ChannelsMerged int
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
			var matched []int
			for i, e := range entries {
				if !e.missing && m.Matches(e.ch) {
					r.Matches++
					matched = append(matched, i)
				}
			}
			if r.Action == rules.Merge {
				merger, err := rules.CompileMerge(r.Rule)
				if err != nil {
					return err
				}
				for _, g := range copiesOf(entries, matched, merger) {
					r.CopiesMerged += len(g) - 1
					r.ChannelsMerged++
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
	channel  int64
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
	guide      sql.NullInt64 // the channel whose guide it shows (D108)
	shift      sql.NullInt64 // seconds

	// What the administrator set, and whether its source is on, for the
	// merge rules, which show the copy that devices see first (D113).
	handHidden sql.NullInt64
	handGroup  sql.NullInt64
	enabled    bool
}

// ruleEntries reads every entry of a lineup with its channel.
func ruleEntries(ctx context.Context, tx *sql.Tx, lineupID int64) ([]ruleEntry, error) {
	// An entry that a lineup of its own groups does not hold (D136) counts
	// as a missing channel's: no rule merges it.
	rows, err := tx.QueryContext(ctx, `
		SELECT e.id, c.id, COALESCE(e.number, 0), e.pinned, c.missing_since IS NOT NULL OR NOT `+takenEntry+`,
			c.source_id, c.position, COALESCE(k.name, ''), c.name, c.xmltv_id,
			e.rule_hidden, e.rule_name, e.rule_group_id, e.rule_logo, e.rule_pinned, e.rule_guide_id, e.rule_guide_shift,
			e.hidden, e.group_id, s.enabled
		FROM lineup_entries e JOIN channels c ON c.id = e.channel_id JOIN sources s ON s.id = c.source_id`+takenJoins+`
		WHERE e.lineup_id = ?`, lineupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ruleEntry
	for rows.Next() {
		var e ruleEntry
		if err := rows.Scan(&e.id, &e.channel, &e.number, &e.pinned, &e.missing, &e.source, &e.position,
			&e.ch.Category, &e.ch.Name, &e.ch.Guide,
			&e.hidden, &e.name, &e.groupID, &e.logo, &e.rulePinned, &e.guide, &e.shift,
			&e.handHidden, &e.handGroup, &e.enabled); err != nil {
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
				rule_pinned = 0, rule_guide_id = NULL, rule_guide_shift = NULL
			WHERE lineup_id = ? AND (rule_hidden IS NOT NULL OR rule_name IS NOT NULL OR rule_group_id IS NOT NULL
				OR rule_logo IS NOT NULL OR rule_pinned = 1 OR rule_guide_id IS NOT NULL OR rule_guide_shift IS NOT NULL)`,
			lineupID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM lineup_entry_rule_backups WHERE entry_id IN (SELECT id FROM lineup_entries WHERE lineup_id = ?)`,
			lineupID); err != nil {
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
		guide  sql.NullInt64
		shift  sql.NullInt64
	}
	var guides *guideNames
	effects := make([]effect, len(entries))
	var numberings []numbering
	backups := make([][]int64, len(entries)) // the channels that merge rules make an entry's backups
	var seen *groupVisibility
	for _, r := range list {
		if r.Off {
			continue
		}
		m, err := rules.Compile(r.Match)
		if err != nil {
			return fmt.Errorf("rule %d: %w", r.ID, err)
		}
		var rename rules.Renamer
		var merger rules.Merger
		switch r.Action {
		case rules.Rename:
			rename, err = rules.CompileRename(r.Rule)
		case rules.Guide:
			rename, err = rules.CompileGuide(r.Rule)
			if guides == nil {
				guides = newGuideNames(entries)
			}
		case rules.Merge:
			merger, err = rules.CompileMerge(r.Rule)
			if err == nil && seen == nil {
				seen, err = loadGroupVisibility(ctx, tx, lineupID)
			}
		}
		if err != nil {
			return fmt.Errorf("rule %d: %w", r.ID, err)
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
			case rules.Guide:
				if ch, ok := guides.find(rename(e.ch.Name), e); ok {
					ef.guide = sql.NullInt64{Int64: ch, Valid: true}
				}
			case rules.Shift:
				ef.shift = sql.NullInt64{Int64: int64(r.Minutes) * 60, Valid: true}
			case rules.Renumber, rules.Merge:
				matched = append(matched, i)
			}
		}
		switch r.Action {
		case rules.Renumber:
			numberings = append(numberings, numbering{from: r.From, entries: matched})
		case rules.Merge:
			for _, g := range copiesOf(entries, matched, merger) {
				// The copy that devices see comes first, then one of the
				// quality that the rule prefers, then the lineup's order.
				rank := func(i int) int {
					r := 0
					if !seen.shows(entries[i], effects[i].hidden) {
						r += 2
					}
					if !merger.Prefers(entries[i].ch) {
						r++
					}
					return r
				}
				slices.SortStableFunc(g, func(a, b int) int {
					x, y := entries[a], entries[b]
					return cmp.Or(cmp.Compare(rank(a), rank(b)), compareNumbers(x.number, y.number),
						cmp.Compare(x.source, y.source), cmp.Compare(x.position, y.position))
				})
				backups[g[0]] = backups[g[0]][:0]
				for _, i := range g[1:] {
					effects[i].hidden = sql.NullInt64{Int64: 1, Valid: true}
					backups[i] = nil
					if len(backups[g[0]]) < MaxBackups {
						backups[g[0]] = append(backups[g[0]], entries[i].channel)
					}
				}
			}
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

	update, err := tx.PrepareContext(ctx, `UPDATE lineup_entries
		SET rule_hidden = ?, rule_name = ?, rule_group_id = ?, rule_logo = ?, rule_guide_id = ?, rule_guide_shift = ?
		WHERE id = ?`)
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
		if ef.hidden == e.hidden && ef.name == e.name && group == e.groupID && ef.logo == e.logo &&
			ef.guide == e.guide && ef.shift == e.shift {
			continue
		}
		if _, err := update.ExecContext(ctx, ef.hidden, ef.name, group, ef.logo, ef.guide, ef.shift, e.id); err != nil {
			return err
		}
	}
	if err := writeRuleBackups(ctx, tx, lineupID, entries, backups); err != nil {
		return err
	}
	if err := numberByRules(ctx, tx, lineupID, entries, numberings); err != nil {
		return err
	}
	return removeEmptyGroups(ctx, tx, lineupID)
}

// copiesOf groups the entries that a merge rule matched, as indexes into
// entries, by what its copies have the same: the groups of two or more,
// each in the lineup's order. Channels that their sources no longer list,
// or whose sources are off, are no one's copies.
func copiesOf(entries []ruleEntry, matched []int, merger rules.Merger) [][]int {
	byKey := map[string][]int{}
	var keys []string
	for _, i := range matched {
		e := entries[i]
		if e.missing || !e.enabled {
			continue
		}
		key := merger.Key(e.ch)
		if key == "" {
			continue
		}
		if _, ok := byKey[key]; !ok {
			keys = append(keys, key)
		}
		byKey[key] = append(byKey[key], i)
	}
	var out [][]int
	for _, key := range keys {
		if g := byKey[key]; len(g) > 1 {
			out = append(out, g)
		}
	}
	return out
}

// compareNumbers orders numbers up, with 0, no number, last.
func compareNumbers(a, b int) int {
	switch {
	case a == b:
		return 0
	case a == 0:
		return 1
	case b == 0:
		return -1
	}
	return cmp.Compare(a, b)
}

// groupVisibility says which of a lineup's groups are hidden, and which
// group each category is in, so that a merge rule can show the copy that
// devices see.
type groupVisibility struct {
	hidden     map[int64]bool
	categories map[categoryKey]int64
}

type categoryKey struct {
	source int64
	name   string
}

func loadGroupVisibility(ctx context.Context, tx *sql.Tx, lineupID int64) (*groupVisibility, error) {
	g := &groupVisibility{hidden: map[int64]bool{}, categories: map[categoryKey]int64{}}
	rows, err := tx.QueryContext(ctx, "SELECT id, hidden FROM lineup_groups WHERE lineup_id = ?", lineupID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var hidden bool
		if err := rows.Scan(&id, &hidden); err != nil {
			rows.Close()
			return nil, err
		}
		g.hidden[id] = hidden
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows, err = tx.QueryContext(ctx, "SELECT source_id, category, group_id FROM lineup_categories WHERE lineup_id = ?", lineupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k categoryKey
		var group int64
		if err := rows.Scan(&k.source, &k.name, &group); err != nil {
			return nil, err
		}
		g.categories[k] = group
	}
	return g, rows.Err()
}

// shows reports whether devices see an entry, as far as the administrator
// and the rules so far have it: shown or hidden by hand, else by a rule,
// else as its group is.
func (g *groupVisibility) shows(e ruleEntry, ruleHidden sql.NullInt64) bool {
	switch {
	case e.handHidden.Valid:
		return e.handHidden.Int64 == 0
	case ruleHidden.Valid:
		return ruleHidden.Int64 == 0
	case e.handGroup.Valid:
		return !g.hidden[e.handGroup.Int64]
	}
	group, ok := g.categories[categoryKey{e.source, e.ch.Category}]
	return !ok || !g.hidden[group]
}

// writeRuleBackups stores the backups that merge rules give a lineup's
// entries, where they changed.
func writeRuleBackups(ctx context.Context, tx *sql.Tx, lineupID int64, entries []ruleEntry, backups [][]int64) error {
	have := map[int64][]int64{}
	rows, err := tx.QueryContext(ctx, `
		SELECT b.entry_id, b.channel_id FROM lineup_entry_rule_backups b JOIN lineup_entries e ON e.id = b.entry_id
		WHERE e.lineup_id = ? ORDER BY b.entry_id, b.position`, lineupID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var entry, channel int64
		if err := rows.Scan(&entry, &channel); err != nil {
			rows.Close()
			return err
		}
		have[entry] = append(have[entry], channel)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, e := range entries {
		if slices.Equal(backups[i], have[e.id]) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM lineup_entry_rule_backups WHERE entry_id = ?", e.id); err != nil {
			return err
		}
		for n, channel := range backups[i] {
			if _, err := tx.ExecContext(ctx, "INSERT INTO lineup_entry_rule_backups (entry_id, position, channel_id) VALUES (?, ?, ?)",
				e.id, n+1, channel); err != nil {
				return err
			}
		}
	}
	return nil
}

// guideNames finds a lineup's channels by their names, for the rules that
// show another channel's guide (D108).
type guideNames struct {
	byName  map[string][]int // a name in lower case: indexes into entries
	entries []ruleEntry
}

func newGuideNames(entries []ruleEntry) *guideNames {
	g := &guideNames{byName: map[string][]int{}, entries: entries}
	for i, e := range entries {
		key := strings.ToLower(e.ch.Name)
		g.byName[key] = append(g.byName[key], i)
	}
	return g
}

// find returns the channel named name, whatever its case, whose guide the
// entry e shows: one of e's own source first, then of any; one that its
// source still lists first; the first listed of those. It is never e's own
// channel.
func (g *guideNames) find(name string, e ruleEntry) (int64, bool) {
	best := -1
	better := func(i int) bool {
		if best < 0 {
			return true
		}
		x, y := g.entries[i], g.entries[best]
		if (x.source == e.source) != (y.source == e.source) {
			return x.source == e.source
		}
		if x.missing != y.missing {
			return !x.missing
		}
		return cmp.Or(cmp.Compare(x.source, y.source), cmp.Compare(x.position, y.position)) < 0
	}
	for _, i := range g.byName[strings.ToLower(name)] {
		if g.entries[i].channel != e.channel && better(i) {
			best = i
		}
	}
	if best < 0 {
		return 0, false
	}
	return g.entries[best].channel, true
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
