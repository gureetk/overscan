// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"overscan.gureet.ca/internal/source"
)

// The films and series of Stalker portals (D77, schema 23). A portal lists
// them 14 to a page, so they are read as devices browse them: a portal's
// categories when a device first asks for films or series, and a
// category's pages as devices ask for parts of its shelf. Each category is
// a shelf of its own, whose titles keep their places in the portal's order.

// PortalPageSize is how many items a page of a portal's list holds.
const PortalPageSize = 14

// PortalCategory is a category of a Stalker portal's films or series: a
// shelf of its own (D77).
type PortalCategory struct {
	ID       int64 // the shelf's ID
	SourceID int64
	Kind     string // KindMovie for a category of the portal's films, KindSeries for one of its series module
	Key      string // the category's key as the read of the portal's categories gave it
	Name     string
	Total    int // how many items the portal last said it holds; -1 until a page of it has been read
}

// ApplyPortalCategories records a read of a portal's categories of films or
// of series, in order, at the given time. Titles stay as they are: they
// come a page at a time (ApplyPortalPage). A category that the read does
// not list leaves the shelves, and the store, once no title is on it.
func (s *Store) ApplyPortalCategories(ctx context.Context, sourceID int64, kind string, categories []LibraryCategory,
	at time.Time) error {
	fail := func(err error) error {
		return fmt.Errorf("store: %s categories of portal %d: %w", kind, sourceID, err)
	}
	if err := checkKind(kind); err != nil {
		return fail(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	when := formatTime(at)
	var gen int64
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO library_reads (source_id, kind, generation, read_at, tried_at, read_error, titles)
		VALUES (?, ?, 1, ?, ?, '', (SELECT count(*) FROM titles WHERE source_id = ? AND kind = ?))
		ON CONFLICT (source_id, kind) DO UPDATE SET
			generation = generation + 1,
			read_at = excluded.read_at,
			tried_at = excluded.tried_at,
			read_error = '',
			titles = excluded.titles
		RETURNING generation`,
		sourceID, kind, when, when, sourceID, kind).Scan(&gen); err != nil {
		return fail(err)
	}
	seen := map[string]bool{}
	for _, c := range categories {
		if c.Key == "" || c.Name == "" || seen[c.Key] {
			continue
		}
		seen[c.Key] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO library_categories (source_id, kind, source_key, name, position, generation)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (source_id, kind, source_key) DO UPDATE SET
				name = excluded.name, position = excluded.position, generation = excluded.generation`,
			sourceID, kind, c.Key, c.Name, len(seen), gen); err != nil {
			return fail(err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM library_categories WHERE source_id = ? AND kind = ? AND generation <> ?
			AND NOT EXISTS (SELECT 1 FROM titles WHERE category_id = library_categories.id)`,
		sourceID, kind, gen); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return nil
}

// portalCategoryListed is the condition, on a category c and its source s,
// that the admin lists it as a portal's shelf: its portal is enabled, and
// the last read of the portal's categories listed it.
const portalCategoryListed = `s.enabled = 1 AND s.type = '` + source.TypeStalker + `' AND c.generation =
	(SELECT r.generation FROM library_reads r WHERE r.source_id = c.source_id AND r.kind = c.kind)`

// portalCategoryShown is the condition, on a category c and its source s,
// that devices see it as a portal's shelf: listed, and not hidden by the
// administrator (D79).
const portalCategoryShown = portalCategoryListed + ` AND c.id NOT IN (` + hiddenCategories + `)`

// PortalCategory returns the portal's category that a shelf of films or of
// series is, and ErrNotFound if the shelf is not one that devices see.
func (s *Store) PortalCategory(ctx context.Context, kind string, shelfID int64) (PortalCategory, error) {
	var c PortalCategory
	var total sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT c.id, c.source_id, c.kind, c.source_key, c.name, c.total
		FROM library_categories c JOIN sources s ON s.id = c.source_id
		WHERE c.id = ? AND c.kind = ? AND `+portalCategoryShown, shelfID, kind).Scan(
		&c.ID, &c.SourceID, &c.Kind, &c.Key, &c.Name, &total)
	if errors.Is(err, sql.ErrNoRows) {
		return PortalCategory{}, ErrNotFound
	}
	if err != nil {
		return PortalCategory{}, fmt.Errorf("store: portal shelf %d: %w", shelfID, err)
	}
	c.Total = -1
	if total.Valid {
		c.Total = int(total.Int64)
	}
	return c, nil
}

// PortalPageReadAt returns when page p, from 1, of a portal's category was
// last read; the zero time if it never was.
func (s *Store) PortalPageReadAt(ctx context.Context, categoryID int64, p int) (time.Time, error) {
	var at string
	err := s.db.QueryRowContext(ctx, "SELECT read_at FROM portal_pages WHERE category_id = ? AND page = ?",
		categoryID, p).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("store: page %d of portal shelf %d: %w", p, categoryID, err)
	}
	return parseTime(at)
}

// PortalItem is a film or a series as a page of a portal's list gives it.
type PortalItem struct {
	Kind  string // KindMovie or KindSeries: a portal lists series among its films where it has no series module
	Title LibraryTitle
}

// ApplyPortalPage keeps page p, from 1, of a portal's category as a read at
// the given time found it: its items take the page's places in the
// category's order, the titles that were in those places and are no longer
// lose them, and the category's total becomes what the portal said.
// Details that devices made Overscan read are kept, and so are a title's
// container and picture when the page gives none.
func (s *Store) ApplyPortalPage(ctx context.Context, categoryID int64, p int, items []PortalItem, total int,
	at time.Time) error {
	fail := func(err error) error {
		return fmt.Errorf("store: page %d of portal shelf %d: %w", p, categoryID, err)
	}
	if p < 1 {
		return fail(errors.New("pages count from 1"))
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fail(err)
	}
	defer tx.Rollback()
	var sourceID int64
	var kind string
	if err := tx.QueryRowContext(ctx, "SELECT source_id, kind FROM library_categories WHERE id = ?", categoryID).Scan(
		&sourceID, &kind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = ErrNotFound
		}
		return fail(err)
	}
	first := (p-1)*PortalPageSize + 1
	if _, err := tx.ExecContext(ctx, "UPDATE titles SET position = 0 WHERE category_id = ? AND position BETWEEN ? AND ?",
		categoryID, first, first+PortalPageSize-1); err != nil {
		return fail(err)
	}
	upsert, err := tx.PrepareContext(ctx, `
		INSERT INTO titles (source_id, kind, source_key, category_id, name, year, rating, poster, container, file,
			added_at, summary, adult, position, generation, missing_since)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			COALESCE((SELECT generation FROM library_reads WHERE source_id = ? AND kind = ?), 0), NULL)
		ON CONFLICT (source_id, kind, source_key) DO UPDATE SET
			category_id = excluded.category_id,
			name = excluded.name,
			year = excluded.year,
			rating = excluded.rating,
			poster = CASE WHEN excluded.poster <> '' THEN excluded.poster ELSE titles.poster END,
			container = CASE WHEN excluded.container <> '' THEN excluded.container ELSE titles.container END,
			file = CASE WHEN excluded.file <> '' THEN excluded.file ELSE titles.file END,
			added_at = excluded.added_at,
			summary = excluded.summary,
			adult = excluded.adult,
			position = excluded.position,
			missing_since = NULL`)
	if err != nil {
		return fail(err)
	}
	defer upsert.Close()
	listed := map[[2]string]bool{}
	for i, it := range items {
		t := it.Title
		key := [2]string{it.Kind, t.Key}
		if checkKind(it.Kind) != nil || t.Key == "" || t.Name == "" || listed[key] || i >= PortalPageSize {
			continue
		}
		listed[key] = true
		summary, err := json.Marshal(t.Summary)
		if err != nil {
			return fail(err)
		}
		if _, err := upsert.ExecContext(ctx, sourceID, it.Kind, t.Key, categoryID, t.Name, max(0, t.Year),
			min(max(0, t.Rating), 100), t.Poster, t.Container, t.File, unixOrZero(t.AddedAt), string(summary), t.Adult,
			first+i, sourceID, it.Kind); err != nil {
			return fail(err)
		}
	}
	when := formatTime(at)
	if _, err := tx.ExecContext(ctx, "UPDATE library_categories SET total = ? WHERE id = ?", max(0, total),
		categoryID); err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO portal_pages (category_id, page, read_at) VALUES (?, ?, ?)
		ON CONFLICT (category_id, page) DO UPDATE SET read_at = excluded.read_at`, categoryID, p, when); err != nil {
		return fail(err)
	}
	// What the admin shows: how many of the portal's films, and series,
	// have been read so far.
	if _, err := tx.ExecContext(ctx, `
		UPDATE library_reads SET titles = (SELECT count(*) FROM titles t
			WHERE t.source_id = library_reads.source_id AND t.kind = library_reads.kind)
		WHERE source_id = ?`, sourceID); err != nil {
		return fail(err)
	}
	if err := tx.Commit(); err != nil {
		return fail(err)
	}
	return nil
}

// PortalTitles returns the titles of a portal's category at positions from
// from+1 to from+count of its order, films and series alike, in that order.
func (s *Store) PortalTitles(ctx context.Context, categoryID int64, from, count int) ([]TitleListing, error) {
	if count <= 0 {
		return []TitleListing{}, nil
	}
	return s.listings(ctx, fmt.Sprintf("portal shelf %d", categoryID), `
		SELECT `+titleRowColumns+`, c.name, c.id, t.summary, t.details
		FROM titles t JOIN library_categories c ON c.id = t.category_id JOIN sources s ON s.id = t.source_id
		WHERE t.category_id = ? AND t.position BETWEEN ? AND ? AND `+libraryShown+`
		ORDER BY t.position`, categoryID, max(0, from)+1, max(0, from)+count)
}

// listings returns the listings that query selects: a title's row, the
// name and the ID of its category, its summary and its details.
func (s *Store) listings(ctx context.Context, what, query string, args ...any) ([]TitleListing, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close()
	out := []TitleListing{}
	for rows.Next() {
		var l TitleListing
		var summary string
		var details sql.NullString
		if l.TitleRow, err = scanTitleRow(rows, &l.Shelf, &l.CategoryID, &summary, &details); err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		if err := json.Unmarshal([]byte(summary), &l.Summary); err != nil {
			return nil, fmt.Errorf("store: %s: title %d: %w", what, l.ID, err)
		}
		if details.Valid {
			l.Details = &TitleDetails{}
			if err := json.Unmarshal([]byte(details.String), l.Details); err != nil {
				return nil, fmt.Errorf("store: %s: title %d: %w", what, l.ID, err)
			}
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}
