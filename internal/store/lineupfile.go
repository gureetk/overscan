// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"unicode/utf8"

	"overscan.gureet.ca/internal/lineupfile"
)

// A lineup file (D112) holds what the administrator changed in each lineup:
// the groups they made or changed, with the categories in them; the
// channels they changed by hand, with their backups; and the rules. An
// import adds the file's lineups as new ones, which no account sees yet,
// and never changes a lineup that is there.

// ExportLineups returns every lineup as a lineup file holds it, the first
// made first.
func (s *Store) ExportLineups(ctx context.Context) (lineupfile.File, error) {
	var f lineupfile.File
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		ids, err := lineupIDs(ctx, tx)
		if err != nil {
			return err
		}
		sources, err := sourceNames(ctx, tx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			l, err := exportLineup(ctx, tx, id, sources)
			if err != nil {
				return fmt.Errorf("lineup %d: %w", id, err)
			}
			f.Lineups = append(f.Lineups, l)
		}
		return nil
	})
	if err != nil {
		return lineupfile.File{}, fmt.Errorf("store: export the lineups: %w", err)
	}
	f.Kind, f.Version = lineupfile.Kind, f.Needs()
	return f, nil
}

// sourceNames returns the name of each source, by its ID.
func sourceNames(ctx context.Context, tx *sql.Tx) (map[int64]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id, name FROM sources")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

func exportLineup(ctx context.Context, tx *sql.Tx, id int64, sources map[int64]string) (lineupfile.Lineup, error) {
	var l lineupfile.Lineup
	if err := tx.QueryRowContext(ctx, "SELECT name, own_groups FROM lineups WHERE id = ?", id).Scan(&l.Name, &l.OwnGroups); err != nil {
		return l, err
	}

	// The groups that the administrator made or changed, by name, with
	// the categories that they hold.
	rows, err := tx.QueryContext(ctx, `
		SELECT g.id, g.name, g.hidden, g.locked, COALESCE(g.numbers_from, 0), COALESCE(m.source_id, 0), COALESCE(m.category, '')
		FROM lineup_groups g LEFT JOIN lineup_categories m ON m.group_id = g.id
		WHERE g.lineup_id = ? AND g.edited = 1
		ORDER BY g.name, g.id, m.source_id, m.category`, id)
	if err != nil {
		return l, err
	}
	var last int64
	for rows.Next() {
		var gid, source int64
		var g lineupfile.Group
		var category string
		if err := rows.Scan(&gid, &g.Name, &g.Hidden, &g.Locked, &g.NumbersFrom, &source, &category); err != nil {
			rows.Close()
			return l, err
		}
		if gid != last {
			l.Groups = append(l.Groups, g)
			last = gid
		}
		if source != 0 {
			cur := &l.Groups[len(l.Groups)-1]
			cur.Categories = append(cur.Categories, lineupfile.Category{Source: sources[source], Name: category})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return l, err
	}

	// The channels changed by hand, or given backups.
	rows, err = tx.QueryContext(ctx, `
		SELECT e.id, c.source_id, c.identity_key, c.name, e.pinned, COALESCE(e.number, 0), COALESCE(e.name, ''),
			COALESCE(g.name, ''), e.hidden, e.locked
		FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
			LEFT JOIN lineup_groups g ON g.id = e.group_id
		WHERE e.lineup_id = ? AND (e.pinned = 1 OR e.name IS NOT NULL OR e.group_id IS NOT NULL
			OR e.hidden IS NOT NULL OR e.locked = 1
			OR EXISTS (SELECT 1 FROM lineup_entry_backups b WHERE b.entry_id = e.id))`, id)
	if err != nil {
		return l, err
	}
	var entries []int64
	for rows.Next() {
		var entry, source int64
		var pinned bool
		var hidden sql.NullBool
		var c lineupfile.Channel
		if err := rows.Scan(&entry, &source, &c.Key, &c.Title, &pinned, &c.Number, &c.Name, &c.Group, &hidden, &c.Locked); err != nil {
			rows.Close()
			return l, err
		}
		c.Source = sources[source]
		if !pinned {
			c.Number = 0
		}
		if hidden.Valid {
			c.Hidden = &hidden.Bool
		}
		l.Channels = append(l.Channels, c)
		entries = append(entries, entry)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return l, err
	}
	for i, entry := range entries {
		rows, err := tx.QueryContext(ctx, `
			SELECT c.source_id, c.identity_key FROM lineup_entry_backups b JOIN channels c ON c.id = b.channel_id
			WHERE b.entry_id = ? ORDER BY b.position`, entry)
		if err != nil {
			return l, err
		}
		for rows.Next() {
			var source int64
			var b lineupfile.Ref
			if err := rows.Scan(&source, &b.Key); err != nil {
				rows.Close()
				return l, err
			}
			b.Source = sources[source]
			l.Channels[i].Backups = append(l.Channels[i].Backups, b)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return l, err
		}
	}
	// In an order that stays the same as numbers change, which keeps a
	// file in git readable.
	sortChannels(l.Channels)

	list, err := lineupRules(ctx, tx, id)
	if err != nil {
		return l, err
	}
	for _, r := range list {
		rule := r.Rule
		if rule.Match.Source != 0 {
			rule.Match.SourceName = sources[rule.Match.Source]
			rule.Match.Source = 0
		}
		for _, id := range rule.Sources {
			rule.SourceNames = append(rule.SourceNames, sources[id])
		}
		rule.Sources = nil
		l.Rules = append(l.Rules, rule)
	}
	return l, nil
}

// sortChannels sorts a file's channels by source, then by key.
func sortChannels(cs []lineupfile.Channel) {
	slices.SortFunc(cs, func(a, b lineupfile.Channel) int {
		return cmp.Or(cmp.Compare(a.Source, b.Source), cmp.Compare(a.Key, b.Key))
	})
}

// LineupImport is what the import of a lineup did.
type LineupImport struct {
	ID   int64
	Name string // which can differ from the file's, if another lineup has it
	// What it took from the file.
	Groups, Channels, Rules int
	// What it left out, since this server has no such source or channel.
	LeftOut LeftOut
}

// LeftOut counts what an import left out.
type LeftOut struct {
	Categories, Channels, Backups, Rules int
}

// Total is everything left out.
func (l LeftOut) Total() int { return l.Categories + l.Channels + l.Backups + l.Rules }

// ImportLineups adds a file's lineups as new lineups (D112), all of them or
// none. Each starts as a lineup made anew, with a group for each category
// and every channel numbered as D40 says, or empty if it holds only what
// its groups take (D136), then takes the file's groups, channels, backups
// and rules. Sources are found by name and channels by
// their source's key; what names a source or a channel that this server
// lacks is left out and counted. A lineup whose name another has gets a
// number after it. It fails with ErrTooManyLineups when the server would
// have more than MaxLineups.
func (s *Store) ImportLineups(ctx context.Context, f lineupfile.File) ([]LineupImport, error) {
	if err := f.Check(); err != nil {
		return nil, err
	}
	var out []LineupImport
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM lineups").Scan(&n); err != nil {
			return err
		}
		if n+len(f.Lineups) > MaxLineups {
			return ErrTooManyLineups
		}
		names, err := sourceNames(ctx, tx)
		if err != nil {
			return err
		}
		sources := map[string]int64{}
		var order []int64
		for id, name := range names {
			sources[name] = id
			order = append(order, id)
		}
		slices.Sort(order)
		for _, l := range f.Lineups {
			res, err := importLineup(ctx, tx, l, sources, order)
			if err != nil {
				return fmt.Errorf("lineup %s: %w", l.Name, err)
			}
			out = append(out, res)
		}
		return nil
	})
	if errors.Is(err, ErrTooManyLineups) {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("store: import lineups: %w", err)
	}
	return out, nil
}

// freeLineupName returns name, or name with a number after it that no
// lineup has, cut to fit a lineup's name.
func freeLineupName(ctx context.Context, tx *sql.Tx, name string) (string, error) {
	try := name
	for i := 2; ; i++ {
		var taken bool
		if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM lineups WHERE name = ?)", try).Scan(&taken); err != nil {
			return "", err
		}
		if !taken {
			return try, nil
		}
		suffix := " (" + strconv.Itoa(i) + ")"
		base := []rune(name)
		if keep := maxLineupNameLen - utf8.RuneCountInString(suffix); len(base) > keep {
			base = base[:keep]
		}
		try = string(base) + suffix
	}
}

// maxLineupNameLen is the most characters a lineup's name has, as the
// admin checks it.
const maxLineupNameLen = 50

func importLineup(ctx context.Context, tx *sql.Tx, l lineupfile.Lineup, sources map[string]int64, order []int64) (LineupImport, error) {
	res := LineupImport{}
	name, err := freeLineupName(ctx, tx, l.Name)
	if err != nil {
		return res, err
	}
	res.Name = name
	if err := tx.QueryRowContext(ctx, "INSERT INTO lineups (name, own_groups) VALUES (?, ?) RETURNING id",
		name, l.OwnGroups).Scan(&res.ID); err != nil {
		return res, lineupNameError(err)
	}
	id := res.ID

	// A lineup made anew: every source's categories and channels, as a
	// refresh adds them, in the order the sources were added. One of its
	// own groups starts with none.
	for _, source := range order {
		if err := groupCategories(ctx, tx, id, source); err != nil {
			return res, err
		}
		if err := numberChannels(ctx, tx, id, source, false); err != nil {
			return res, err
		}
	}

	// The groups, and the categories they hold.
	group := func(name string) (int64, error) {
		var gid int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO lineup_groups (lineup_id, name, edited) VALUES (?, ?, 1)
			ON CONFLICT (lineup_id, name) DO UPDATE SET edited = 1
			RETURNING id`, id, name).Scan(&gid)
		return gid, err
	}
	for _, g := range l.Groups {
		gid, err := group(g.Name)
		if err != nil {
			return res, err
		}
		var from any
		if g.NumbersFrom > 0 {
			from = g.NumbersFrom
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET hidden = ?, locked = ?, numbers_from = ? WHERE id = ?",
			g.Hidden, g.Locked, from, gid); err != nil {
			return res, err
		}
		res.Groups++
		for _, c := range g.Categories {
			source, ok := sources[c.Source]
			if !ok {
				res.LeftOut.Categories++
				continue
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (?, ?, ?, ?)
				ON CONFLICT (lineup_id, source_id, category) DO UPDATE SET group_id = excluded.group_id`,
				id, source, c.Name, gid); err != nil {
				return res, err
			}
		}
	}

	// The channels changed by hand: their entries, made for a channel
	// that its source no longer lists, which has none in a new lineup.
	channel := func(r lineupfile.Ref) (int64, bool, error) {
		source, ok := sources[r.Source]
		if !ok {
			return 0, false, nil
		}
		var cid int64
		err := tx.QueryRowContext(ctx, "SELECT id FROM channels WHERE source_id = ? AND identity_key = ?", source, r.Key).Scan(&cid)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, false, nil
		}
		return cid, err == nil, err
	}
	type pin struct {
		entry  int64
		number int
	}
	var pins []pin
	for _, c := range l.Channels {
		cid, ok, err := channel(c.Ref)
		if err != nil {
			return res, err
		}
		if !ok {
			res.LeftOut.Channels++
			continue
		}
		var entry int64
		err = tx.QueryRowContext(ctx, `
			INSERT INTO lineup_entries (lineup_id, channel_id) VALUES (?, ?)
			ON CONFLICT (lineup_id, channel_id) DO UPDATE SET channel_id = excluded.channel_id
			RETURNING id`, id, cid).Scan(&entry)
		if err != nil {
			return res, err
		}
		var name, groupID, hidden any
		if c.Name != "" {
			name = c.Name
		}
		if c.Group != "" {
			if groupID, err = group(c.Group); err != nil {
				return res, err
			}
		}
		if c.Hidden != nil {
			hidden = *c.Hidden
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_entries SET name = ?, group_id = ?, hidden = ?, locked = ? WHERE id = ?",
			name, groupID, hidden, c.Locked, entry); err != nil {
			return res, err
		}
		position := 0
		for _, b := range c.Backups {
			bid, ok, err := channel(b)
			if err != nil {
				return res, err
			}
			if !ok || bid == cid {
				res.LeftOut.Backups++
				continue
			}
			position++
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO lineup_entry_backups (entry_id, position, channel_id) VALUES (?, ?, ?)
				ON CONFLICT DO NOTHING`, entry, position, bid); err != nil {
				return res, err
			}
		}
		if c.Number != 0 {
			pins = append(pins, pin{entry, c.Number})
		}
		res.Channels++
	}
	if l.OwnGroups {
		// It numbers what its groups now take.
		for _, source := range order {
			if err := numberChannels(ctx, tx, id, source, false); err != nil {
				return res, err
			}
		}
	}
	slices.SortFunc(pins, func(a, b pin) int { return cmp.Compare(a.number, b.number) })
	for _, p := range pins {
		if err := pinNumber(ctx, tx, id, p.entry, p.number); err != nil {
			return res, err
		}
	}
	if err := removeEmptyGroups(ctx, tx, id); err != nil {
		return res, err
	}

	// The rules, in order, which then run.
	for _, r := range l.Rules {
		if r.Match.SourceName != "" {
			source, ok := sources[r.Match.SourceName]
			if !ok {
				res.LeftOut.Rules++
				continue
			}
			r.Match.Source = source
		}
		// A source that this server lacks leaves the merge's order.
		for _, name := range r.SourceNames {
			if source, ok := sources[name]; ok {
				r.Sources = append(r.Sources, source)
			}
		}
		r.SourceNames = nil
		spec, err := checkRule(r)
		if err != nil {
			return res, err
		}
		res.Rules++
		if _, err := tx.ExecContext(ctx, "INSERT INTO lineup_rules (lineup_id, position, spec) VALUES (?, ?, ?)",
			id, res.Rules, spec); err != nil {
			return res, err
		}
	}
	return res, applyRules(ctx, tx, id)
}
