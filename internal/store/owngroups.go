// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"overscan.gureet.ca/internal/rules"
)

// Lineups of the household's own groups (D136, schema 45). Such a lineup
// starts empty and holds only what its groups take: categories of any
// source, picked from one list of every source's categories, and channels
// moved into a group by hand. A source's refresh adds no group of its own
// to it, and numbers what joins it after its highest number. It has an
// entry for every channel, as other lineups do, but those it does not hold
// have no number, unless one was set by hand, and keep their edits and
// favourites for when a group takes their category.

// MaxGroupCategories caps how many categories a group takes.
const MaxGroupCategories = 1000

// ErrTooManyCategories is the error for a group given more than
// MaxGroupCategories.
var ErrTooManyCategories = fmt.Errorf("store: a group takes at most %d categories", MaxGroupCategories)

// NewLineup makes an empty lineup of its own groups, named name, with the
// given rules, such as one that merges the copies of a channel (D113), and
// returns its ID.
func (s *Store) NewLineup(ctx context.Context, name string, rs ...rules.Rule) (int64, error) {
	specs := make([]string, len(rs))
	for i, r := range rs {
		spec, err := checkRule(r)
		if err != nil {
			return 0, err
		}
		specs[i] = spec
	}
	var id int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM lineups").Scan(&n); err != nil {
			return err
		}
		if n >= MaxLineups {
			return ErrTooManyLineups
		}
		if err := tx.QueryRowContext(ctx, "INSERT INTO lineups (name, own_groups) VALUES (?, 1) RETURNING id", name).Scan(&id); err != nil {
			return lineupNameError(err)
		}
		for i, r := range rs {
			if err := ruleSource(ctx, tx, r); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO lineup_rules (lineup_id, position, spec) VALUES (?, ?, ?)",
				id, i+1, specs[i]); err != nil {
				return err
			}
		}
		// Its entries, without numbers, keep favourites that move to it
		// with an account.
		sources, err := sourceIDs(ctx, tx)
		if err != nil {
			return err
		}
		for _, sourceID := range sources {
			if err := numberChannels(ctx, tx, id, sourceID, false); err != nil {
				return err
			}
		}
		return applyRules(ctx, tx, id)
	})
	var bad *rules.Error
	if errors.Is(err, ErrLineupExists) || errors.Is(err, ErrTooManyLineups) || errors.As(err, &bad) {
		return 0, err
	}
	if err != nil {
		return 0, fmt.Errorf("store: new lineup: %w", err)
	}
	return id, nil
}

// CategoryRef names a source's live category: by its name, "" for the
// channels without one.
type CategoryRef struct {
	SourceID int64
	Name     string
}

// LineupCategory is a source's live category as a lineup's groups can take
// it.
type LineupCategory struct {
	CategoryRef
	Source string // the source's name
	Items  int    // the channels it held at the last read that listed it
	// GroupID is the group of the lineup that takes it, 0 if none, and
	// Group its name.
	GroupID int64
	Group   string
}

// MaxLineupCategories caps how many categories LineupCategories returns.
const MaxLineupCategories = 2000

// LineupCategories returns the live categories of every source that the
// source brings (D135), whose names hold text if it is not "", by source
// and in each source's order, with the group of the lineup that takes
// each: at most MaxLineupCategories, and how many match in all.
func (s *Store) LineupCategories(ctx context.Context, lineupID int64, text string) ([]LineupCategory, int, error) {
	fail := func(err error) ([]LineupCategory, int, error) {
		return nil, 0, fmt.Errorf("store: categories for lineup %d: %w", lineupID, err)
	}
	where := `sc.kind = '` + KindLive + `' AND (sc.ticked = 1 OR NOT EXISTS (SELECT 1 FROM source_category_choices ch
		WHERE ch.source_id = sc.source_id AND ch.kind = sc.kind AND ch.only_ticked = 1))`
	args := []any{lineupID}
	if text = strings.TrimSpace(text); text != "" {
		where += ` AND instr(lower(sc.name), lower(?)) > 0`
		args = append(args, text)
	}
	from := `
		FROM source_categories sc JOIN sources src ON src.id = sc.source_id
		LEFT JOIN lineup_categories m ON m.lineup_id = ? AND m.source_id = sc.source_id AND m.category = sc.source_key
		LEFT JOIN lineup_groups g ON g.id = m.group_id
		WHERE ` + where
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+from, args...).Scan(&total); err != nil {
		return fail(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT sc.source_id, src.name, sc.source_key, COALESCE(sc.items, 0), COALESCE(g.id, 0), COALESCE(g.name, '')`+from+`
		ORDER BY src.name, sc.listed_at DESC, sc.position LIMIT ?`, append(args, MaxLineupCategories)...)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	out := []LineupCategory{}
	for rows.Next() {
		var c LineupCategory
		if err := rows.Scan(&c.SourceID, &c.Source, &c.Name, &c.Items, &c.GroupID, &c.Group); err != nil {
			return fail(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	return out, total, nil
}

// SetGroupCategories makes a group take exactly the given categories of
// any source: those that another group of the lineup took move to it, and
// those that it took and the list leaves out leave it. In a lineup of its
// own groups their channels then leave the lineup, keeping their edits for
// when a group takes them again; in one that takes every category, each
// goes back to a group of its own name. A category must be one that a read
// of its source listed, or one that the group takes already, or
// ErrNotFound is returned, as it is for a group that is not the lineup's.
func (s *Store) SetGroupCategories(ctx context.Context, lineupID, groupID int64, cats []CategoryRef) error {
	if len(cats) > MaxGroupCategories {
		return ErrTooManyCategories
	}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkGroup(ctx, tx, lineupID, groupID); err != nil {
			return err
		}
		want := map[CategoryRef]bool{}
		touched := map[int64]bool{}
		for _, c := range cats {
			var known bool
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (SELECT 1 FROM source_categories WHERE source_id = ?1 AND kind = '`+KindLive+`' AND source_key = ?2)
					OR EXISTS (SELECT 1 FROM categories WHERE source_id = ?1 AND kind = 'live' AND name = ?2)
					OR EXISTS (SELECT 1 FROM lineup_categories WHERE group_id = ?3 AND source_id = ?1 AND category = ?2)`,
				c.SourceID, c.Name, groupID).Scan(&known); err != nil {
				return err
			}
			if !known {
				return ErrNotFound
			}
			want[c] = true
		}
		rows, err := tx.QueryContext(ctx, "SELECT source_id, category FROM lineup_categories WHERE group_id = ?", groupID)
		if err != nil {
			return err
		}
		var gone []CategoryRef
		for rows.Next() {
			var c CategoryRef
			if err := rows.Scan(&c.SourceID, &c.Name); err != nil {
				rows.Close()
				return err
			}
			if !want[c] {
				gone = append(gone, c)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, c := range gone {
			if _, err := tx.ExecContext(ctx, "DELETE FROM lineup_categories WHERE lineup_id = ? AND source_id = ? AND category = ?",
				lineupID, c.SourceID, c.Name); err != nil {
				return err
			}
			touched[c.SourceID] = true
		}
		for c := range want {
			res, err := tx.ExecContext(ctx, `
				INSERT INTO lineup_categories (lineup_id, source_id, category, group_id) VALUES (?, ?, ?, ?)
				ON CONFLICT (lineup_id, source_id, category) DO UPDATE SET group_id = excluded.group_id
				WHERE lineup_categories.group_id <> excluded.group_id`,
				lineupID, c.SourceID, c.Name, groupID)
			if err != nil {
				return err
			}
			if n, err := res.RowsAffected(); err != nil {
				return err
			} else if n > 0 {
				touched[c.SourceID] = true
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET edited = 1 WHERE id = ?", groupID); err != nil {
			return err
		}
		// In the order the sources were added, as a refresh numbers them.
		for _, sourceID := range slices.Sorted(maps.Keys(touched)) {
			if err := groupCategories(ctx, tx, lineupID, sourceID); err != nil {
				return err
			}
			if err := numberChannels(ctx, tx, lineupID, sourceID, false); err != nil {
				return err
			}
		}
		if err := removeEmptyGroups(ctx, tx, lineupID); err != nil {
			return err
		}
		return applyRules(ctx, tx, lineupID)
	})
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("store: categories of group %d: %w", groupID, err)
	}
	return err
}

// LineupViewer is an account that sees a lineup, with how its devices and
// players reach it.
type LineupViewer struct {
	AccountID int64
	Account   string
	Boxes     int  // approved boxes that run Overscan's page: paired with a secret
	Clients   int  // approved Stalker clients
	Playlist  bool // its playlist and guide are on, and it has a token (D103)
	Login     bool // its players' Xtream login is on, and it has a username (D104)
}

// LineupViewers returns the accounts that see a lineup, by name, with their
// devices and outputs. Whether the outputs are on for the whole server is
// for the caller to say.
func (s *Store) LineupViewers(ctx context.Context, lineupID int64) ([]LineupViewer, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT a.id, a.name,
			(SELECT count(*) FROM devices d WHERE d.account_id = a.id AND d.state = 'approved' AND d.secret_hash IS NOT NULL),
			(SELECT count(*) FROM devices d WHERE d.account_id = a.id AND d.state = 'approved' AND d.secret_hash IS NULL),
			a.output_m3u = 1 AND a.token_hash IS NOT NULL, a.output_xtream = 1 AND COALESCE(a.username, '') <> ''
		FROM accounts a WHERE a.lineup_id = ? ORDER BY a.name COLLATE NOCASE`, lineupID)
	if err != nil {
		return nil, fmt.Errorf("store: accounts of lineup %d: %w", lineupID, err)
	}
	defer rows.Close()
	out := []LineupViewer{}
	for rows.Next() {
		var v LineupViewer
		if err := rows.Scan(&v.AccountID, &v.Account, &v.Boxes, &v.Clients, &v.Playlist, &v.Login); err != nil {
			return nil, fmt.Errorf("store: accounts of lineup %d: %w", lineupID, err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// sourceIDs returns the ID of every source.
func sourceIDs(ctx context.Context, tx *sql.Tx) ([]int64, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id FROM sources ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// numberOwnLineup numbers what a lineup of its own groups now holds, and
// takes back the numbers of what it no longer holds, after a change to
// what its groups take; its rules then run again, since merges work only
// on what it holds. Other lineups it leaves alone.
func numberOwnLineup(ctx context.Context, tx *sql.Tx, lineupID int64) error {
	var own bool
	if err := tx.QueryRowContext(ctx, "SELECT own_groups FROM lineups WHERE id = ?", lineupID).Scan(&own); err != nil || !own {
		return err
	}
	sources, err := sourceIDs(ctx, tx)
	if err != nil {
		return err
	}
	for _, sourceID := range sources {
		if err := numberChannels(ctx, tx, lineupID, sourceID, false); err != nil {
			return err
		}
	}
	return applyRules(ctx, tx, lineupID)
}
