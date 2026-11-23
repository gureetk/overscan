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

// What a source brings (D135, schema 44). A source brings every category of
// its live channels, of its films and of its series, as before, or only the
// categories that the administrator ticks, each kind on its own. It is read
// with the same requests either way: what a read lists is filtered before it
// is stored. The channels and titles of a category that a source no longer
// brings are kept as those that it stopped listing are, for MissingGrace or
// LibraryGrace, so that ticking the category again brings them back with
// what the administrator set on them.

// KindLive is the kind of a source's categories of live channels, beside
// KindMovie and KindSeries.
const KindLive = "live"

// checkCategoryKind returns an error unless kind is a kind of category.
func checkCategoryKind(kind string) error {
	if kind != KindLive && kind != KindMovie && kind != KindSeries {
		return fmt.Errorf("store: %q is not a kind of category", kind)
	}
	return nil
}

// SourceCategory is a category that a read of a source listed.
type SourceCategory struct {
	// Key is a live category's name, or the source's own ID for a category
	// of films or of series; "" for the channels or titles without one.
	Key  string
	Name string
	// Items is how many channels or titles the last read that listed it
	// found in it; -1 if it is not known, as for a Stalker portal's
	// categories, whose titles are read a page at a time (D77).
	Items    int
	Ticked   bool
	ListedAt time.Time
}

// CategoryChoice is what a source brings of a kind of category.
type CategoryChoice struct {
	// OnlyTicked is set when the source brings only the ticked categories;
	// otherwise it brings every one, as before.
	OnlyTicked bool
	// TickNew makes a category that the source adds later start ticked
	// while it brings only the ticked ones. While it brings every
	// category, a new one starts ticked whatever TickNew says.
	TickNew bool
	// ChangedAt is when the administrator last changed the choice or a
	// tick, zero if never.
	ChangedAt time.Time
}

// listedCategory is a category as a read lists it.
type listedCategory struct {
	key, name string
	items     int // -1 if not known
}

// bringCategories records the categories of kind that a read of a source
// listed, in order, at the given time, and returns whether the source
// brings a category, by its key. A category that the source adds starts
// ticked while it brings every category of the kind, and as the choice
// says otherwise. A category that no read has listed for MissingGrace is
// forgotten.
func bringCategories(ctx context.Context, tx *sql.Tx, sourceID int64, kind string, listed []listedCategory,
	at time.Time) (func(key string) bool, error) {
	choice, err := categoryChoice(ctx, tx, sourceID, kind)
	if err != nil {
		return nil, err
	}
	tickNew := !choice.OnlyTicked || choice.TickNew
	when := formatTime(at)
	upsert, err := tx.PrepareContext(ctx, `
		INSERT INTO source_categories (source_id, kind, source_key, name, position, items, ticked, listed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, kind, source_key) DO UPDATE SET
			name = excluded.name,
			position = excluded.position,
			items = excluded.items,
			listed_at = excluded.listed_at`)
	if err != nil {
		return nil, err
	}
	defer upsert.Close()
	for i, c := range listed {
		var items any
		if c.items >= 0 {
			items = c.items
		}
		if _, err := upsert.ExecContext(ctx, sourceID, kind, c.key, c.name, i+1, items, tickNew, when); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM source_categories WHERE source_id = ? AND kind = ? AND listed_at < ?`,
		sourceID, kind, formatTime(at.Add(-MissingGrace))); err != nil {
		return nil, err
	}
	if !choice.OnlyTicked {
		return func(string) bool { return true }, nil
	}
	ticked := map[string]bool{}
	rows, err := tx.QueryContext(ctx, `
		SELECT source_key FROM source_categories WHERE source_id = ? AND kind = ? AND ticked = 1`, sourceID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, err
		}
		ticked[key] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return func(key string) bool { return ticked[key] }, nil
}

// querier is what both a store's database and a transaction answer.
type querier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// categoryChoice returns what a source brings of kind: every category, the
// zero CategoryChoice, unless the administrator chose otherwise.
func categoryChoice(ctx context.Context, q querier, sourceID int64, kind string) (CategoryChoice, error) {
	var c CategoryChoice
	var changed string
	err := q.QueryRowContext(ctx, `
		SELECT only_ticked, tick_new, changed_at FROM source_category_choices WHERE source_id = ? AND kind = ?`,
		sourceID, kind).Scan(&c.OnlyTicked, &c.TickNew, &changed)
	if errors.Is(err, sql.ErrNoRows) {
		return CategoryChoice{}, nil
	}
	if err != nil {
		return CategoryChoice{}, err
	}
	if c.ChangedAt, err = parseTime(changed); err != nil {
		return CategoryChoice{}, err
	}
	return c, nil
}

// CategoryChoice returns what a source brings of a kind of category.
func (s *Store) CategoryChoice(ctx context.Context, sourceID int64, kind string) (CategoryChoice, error) {
	if err := checkCategoryKind(kind); err != nil {
		return CategoryChoice{}, err
	}
	c, err := categoryChoice(ctx, s.db, sourceID, kind)
	if err != nil {
		return CategoryChoice{}, fmt.Errorf("store: categories of source %d: %w", sourceID, err)
	}
	return c, nil
}

// CategoryChoices returns what every source brings of each kind of
// category, by source and kind, leaving out those that bring every
// category and were never changed.
func (s *Store) CategoryChoices(ctx context.Context) (map[int64]map[string]CategoryChoice, error) {
	fail := func(err error) (map[int64]map[string]CategoryChoice, error) {
		return nil, fmt.Errorf("store: category choices: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT source_id, kind, only_ticked, tick_new, changed_at FROM source_category_choices")
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	out := map[int64]map[string]CategoryChoice{}
	for rows.Next() {
		var id int64
		var kind, changed string
		var c CategoryChoice
		if err := rows.Scan(&id, &kind, &c.OnlyTicked, &c.TickNew, &changed); err != nil {
			return fail(err)
		}
		if c.ChangedAt, err = parseTime(changed); err != nil {
			return fail(err)
		}
		if out[id] == nil {
			out[id] = map[string]CategoryChoice{}
		}
		out[id][kind] = c
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	return out, nil
}

// SetCategoryChoice sets what a source brings of a kind of category, at the
// given time, and reports whether that changed it. It returns ErrNotFound
// for a source that does not exist. The change takes effect at the
// source's next read.
func (s *Store) SetCategoryChoice(ctx context.Context, sourceID int64, kind string, onlyTicked, tickNew bool,
	at time.Time) (bool, error) {
	fail := func(err error) (bool, error) {
		return false, fmt.Errorf("store: categories of source %d: %w", sourceID, err)
	}
	if err := checkCategoryKind(kind); err != nil {
		return fail(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if err := sourceExists(ctx, tx, sourceID); err != nil {
		return false, err
	}
	before, err := categoryChoice(ctx, tx, sourceID, kind)
	if err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO source_category_choices (source_id, kind, only_ticked, tick_new, changed_at) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (source_id, kind) DO UPDATE SET
			only_ticked = excluded.only_ticked,
			tick_new = excluded.tick_new,
			changed_at = CASE WHEN only_ticked <> excluded.only_ticked OR tick_new <> excluded.tick_new
				THEN excluded.changed_at ELSE changed_at END`,
		sourceID, kind, onlyTicked, tickNew, formatTime(at)); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return before.OnlyTicked != onlyTicked || before.TickNew != tickNew, nil
}

// sourceExists returns ErrNotFound unless the source exists.
func sourceExists(ctx context.Context, tx *sql.Tx, sourceID int64) error {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM sources WHERE id = ?", sourceID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("store: source %d: %w", sourceID, err)
	}
	return nil
}

// MaxCategoriesListed caps how many categories SourceCategories returns:
// more than a page can show at once, which search narrows.
const MaxCategoriesListed = 1000

// SourceCategories returns the categories of a kind that the reads of a
// source listed, in the order of the last read that listed each, those
// whose name holds text (whatever its case) if text is not "", at most
// MaxCategoriesListed, and how many match in all.
func (s *Store) SourceCategories(ctx context.Context, sourceID int64, kind, text string) ([]SourceCategory, int, error) {
	fail := func(err error) ([]SourceCategory, int, error) {
		return nil, 0, fmt.Errorf("store: categories of source %d: %w", sourceID, err)
	}
	if err := checkCategoryKind(kind); err != nil {
		return fail(err)
	}
	where, args := categoryText(sourceID, kind, text)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM source_categories WHERE "+where, args...).Scan(&total); err != nil {
		return fail(err)
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_key, name, COALESCE(items, -1), ticked, listed_at FROM source_categories
		WHERE `+where+` ORDER BY listed_at DESC, position, name LIMIT ?`,
		append(args, MaxCategoriesListed)...)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	out := []SourceCategory{}
	for rows.Next() {
		var c SourceCategory
		var listed string
		if err := rows.Scan(&c.Key, &c.Name, &c.Items, &c.Ticked, &listed); err != nil {
			return fail(err)
		}
		if c.ListedAt, err = parseTime(listed); err != nil {
			return fail(err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	return out, total, nil
}

// categoryText returns the condition, and its arguments, for a source's
// categories of a kind whose names hold text.
func categoryText(sourceID int64, kind, text string) (string, []any) {
	where := "source_id = ? AND kind = ?"
	args := []any{sourceID, kind}
	if text = strings.TrimSpace(text); text != "" {
		where += ` AND instr(lower(name), lower(?)) > 0`
		args = append(args, text)
	}
	return where, args
}

// TickCategories ticks or unticks a source's categories of a kind, by
// their keys, at the given time, and returns how many it changed. Keys that
// the source has no category for are left out. It returns ErrNotFound for a
// source that does not exist.
func (s *Store) TickCategories(ctx context.Context, sourceID int64, kind string, ticks map[string]bool,
	at time.Time) (int, error) {
	return s.tickCategories(ctx, sourceID, kind, at, func(tx *sql.Tx) (int, error) {
		update, err := tx.PrepareContext(ctx, `
			UPDATE source_categories SET ticked = ? WHERE source_id = ? AND kind = ? AND source_key = ? AND ticked <> ?`)
		if err != nil {
			return 0, err
		}
		defer update.Close()
		changed := 0
		for key, on := range ticks {
			res, err := update.ExecContext(ctx, on, sourceID, kind, key, on)
			if err != nil {
				return 0, err
			}
			n, err := rowsAffected(res)
			if err != nil {
				return 0, err
			}
			changed += n
		}
		return changed, nil
	})
}

// TickAllCategories ticks or unticks every category of a kind of a source
// whose name holds text, or every one if text is "", at the given time, and
// returns how many it changed.
func (s *Store) TickAllCategories(ctx context.Context, sourceID int64, kind, text string, on bool,
	at time.Time) (int, error) {
	return s.tickCategories(ctx, sourceID, kind, at, func(tx *sql.Tx) (int, error) {
		where, args := categoryText(sourceID, kind, text)
		res, err := tx.ExecContext(ctx, "UPDATE source_categories SET ticked = ? WHERE "+where+" AND ticked <> ?",
			append(append([]any{on}, args...), on)...)
		if err != nil {
			return 0, err
		}
		return rowsAffected(res)
	})
}

// tickCategories runs change in a transaction, and records when the ticks
// changed if it changed any.
func (s *Store) tickCategories(ctx context.Context, sourceID int64, kind string, at time.Time,
	change func(*sql.Tx) (int, error)) (int, error) {
	fail := func(err error) (int, error) {
		return 0, fmt.Errorf("store: categories of source %d: %w", sourceID, err)
	}
	if err := checkCategoryKind(kind); err != nil {
		return fail(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	if err := sourceExists(ctx, tx, sourceID); err != nil {
		return 0, err
	}
	n, err := change(tx)
	if err != nil {
		return fail(err)
	}
	// Ticks change what the source brings only while it brings the ticked
	// categories alone; otherwise they wait for that.
	if n > 0 {
		if _, err := tx.ExecContext(ctx, `
			UPDATE source_category_choices SET changed_at = ? WHERE source_id = ? AND kind = ? AND only_ticked = 1`,
			formatTime(at), sourceID, kind); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return n, nil
}

// CategoryCount sums up a source's categories of a kind.
type CategoryCount struct {
	Listed int // the categories that its reads listed
	Ticked int // of those, the ticked ones
	// LastRead is when a read last listed any of them: the last read of
	// that kind that worked.
	LastRead time.Time
}

// CategoryCounts returns how many categories of each kind every source's
// reads listed, and how many are ticked, by source and kind.
func (s *Store) CategoryCounts(ctx context.Context) (map[int64]map[string]CategoryCount, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_id, kind, count(*), COALESCE(sum(ticked), 0), max(listed_at) FROM source_categories GROUP BY source_id, kind`)
	if err != nil {
		return nil, fmt.Errorf("store: category counts: %w", err)
	}
	defer rows.Close()
	out := map[int64]map[string]CategoryCount{}
	for rows.Next() {
		var id int64
		var kind string
		var c CategoryCount
		var last string
		if err := rows.Scan(&id, &kind, &c.Listed, &c.Ticked, &last); err != nil {
			return nil, fmt.Errorf("store: category counts: %w", err)
		}
		if c.LastRead, err = parseTime(last); err != nil {
			return nil, fmt.Errorf("store: category counts: %w", err)
		}
		if out[id] == nil {
			out[id] = map[string]CategoryCount{}
		}
		out[id][kind] = c
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: category counts: %w", err)
	}
	return out, nil
}
