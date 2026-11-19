// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What the administrator sets of the films and series that devices see
// (D79): shelves hidden, renamed, locked (D85) or put in a place of their
// own, and titles hidden or locked. The settings outlast the reads of the
// sources, as the lineup's do.

// MaxShelfName is the longest name, in characters, that a shelf can be
// given.
const MaxShelfName = 100

// ErrBadName is why a shelf cannot be given a name: it is empty once
// trimmed, too long, or holds control characters.
var ErrBadName = errors.New("store: a shelf's name must be 1 to 100 characters, without control characters")

// LibraryShelf is a shelf of films or of series as the admin lists it.
type LibraryShelf struct {
	ID int64 // as Shelf's, which devices know it by
	// Key is the shelf's in its settings: name:<its name> for a shelf that
	// merges the categories of that name, category:<its ID> for a portal's
	// (D77).
	Key       string
	Own       string   // the name its sources give it
	Name      string   // the name devices see it by: Own, or the administrator's
	SourceIDs []int64  // the sources whose categories it holds
	Sources   []string // their names
	// Titles counts its titles, hidden ones too; for a portal's, those read
	// so far. HiddenTitles counts those the administrator hid one by one.
	Titles       int
	HiddenTitles int
	Total        int // for a portal's, how many items the portal says it holds; -1 if not known or not a portal's
	Portal       bool
	Hidden       bool // the administrator hid it
	Locked       bool // the administrator locked it, and every title on it (D85)
	Placed       bool // the administrator put it in a place of its own
}

// Renamed reports whether the administrator gave the shelf a name.
func (sh LibraryShelf) Renamed() bool { return sh.Name != sh.Own }

// LibraryShelves returns the shelves of films or of series that the admin
// lists, hidden ones too, in the order that devices see them in.
func (s *Store) LibraryShelves(ctx context.Context, kind string) ([]LibraryShelf, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	names, err := s.sourceNames(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, own, COALESCE(given, own), srcs, n, hid, total, portal, off, lock, place FROM (
			SELECT min(c.id) AS id, c.name AS own, max(ss.name) AS given, group_concat(DISTINCT c.source_id) AS srcs,
				count(t.id) AS n, count(CASE WHEN t.hidden = 1 THEN 1 END) AS hid, -1 AS total, 0 AS portal,
				COALESCE(max(ss.hidden), 0) AS off, COALESCE(max(ss.locked), 0) AS lock, max(ss.position) AS place,
				min(c.source_id) AS src, min(c.position) AS pos
			FROM library_categories c
				JOIN titles t ON t.category_id = c.id
				JOIN sources s ON s.id = t.source_id
				LEFT JOIN shelf_settings ss ON ss.kind = c.kind AND ss.shelf = 'name:' || c.name
			WHERE c.kind = ? AND `+libraryListed+` AND `+mergedShelf+`
			GROUP BY c.name
			UNION ALL
			SELECT c.id, c.name, ss.name, CAST(c.source_id AS TEXT),
				(SELECT count(*) FROM titles t WHERE t.category_id = c.id AND t.missing_since IS NULL AND t.position > 0),
				(SELECT count(*) FROM titles t WHERE t.category_id = c.id AND t.missing_since IS NULL AND t.position > 0 AND t.hidden = 1),
				COALESCE(c.total, -1), 1, COALESCE(ss.hidden, 0), COALESCE(ss.locked, 0), ss.position, c.source_id, c.position
			FROM library_categories c JOIN sources s ON s.id = c.source_id
				LEFT JOIN shelf_settings ss ON ss.kind = c.kind AND ss.shelf = 'category:' || c.id
			WHERE c.kind = ? AND `+portalCategoryListed+` AND COALESCE(c.total, -1) <> 0)
		ORDER BY place IS NULL, place, src, pos, own`, kind, kind)
	if err != nil {
		return nil, fmt.Errorf("store: %s shelves for the admin: %w", kind, err)
	}
	defer rows.Close()
	out := []LibraryShelf{}
	for rows.Next() {
		var sh LibraryShelf
		var srcs string
		var place sql.NullInt64
		if err := rows.Scan(&sh.ID, &sh.Own, &sh.Name, &srcs, &sh.Titles, &sh.HiddenTitles, &sh.Total, &sh.Portal,
			&sh.Hidden, &sh.Locked, &place); err != nil {
			return nil, fmt.Errorf("store: %s shelves for the admin: %w", kind, err)
		}
		sh.Placed = place.Valid
		sh.Key = shelfKey(sh.Portal, sh.ID, sh.Own)
		for _, f := range strings.Split(srcs, ",") {
			if id, err := strconv.ParseInt(f, 10, 64); err == nil {
				sh.SourceIDs = append(sh.SourceIDs, id)
				sh.Sources = append(sh.Sources, names[id])
			}
		}
		out = append(out, sh)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s shelves for the admin: %w", kind, err)
	}
	return out, nil
}

// shelfKey returns a shelf's key in its settings.
func shelfKey(portal bool, id int64, name string) string {
	if portal {
		return "category:" + strconv.FormatInt(id, 10)
	}
	return "name:" + name
}

// sourceNames returns the sources' names by ID.
func (s *Store) sourceNames(ctx context.Context) (map[int64]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name FROM sources")
	if err != nil {
		return nil, fmt.Errorf("store: sources' names: %w", err)
	}
	defer rows.Close()
	names := map[int64]string{}
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("store: sources' names: %w", err)
		}
		names[id] = name
	}
	return names, rows.Err()
}

// LibraryShelf returns the shelf of films or of series that the admin
// lists with the given ID, and ErrNotFound if there is none.
func (s *Store) LibraryShelf(ctx context.Context, kind string, id int64) (LibraryShelf, error) {
	shelves, err := s.LibraryShelves(ctx, kind)
	if err != nil {
		return LibraryShelf{}, err
	}
	for _, sh := range shelves {
		if sh.ID == id {
			return sh, nil
		}
	}
	return LibraryShelf{}, ErrNotFound
}

// changeShelf changes what is set of a shelf with set, an SQL assignment
// with its arguments, and drops a shelf's settings once they hold nothing.
func (s *Store) changeShelf(ctx context.Context, what, kind, key, set string, args ...any) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO shelf_settings (kind, shelf) VALUES (?, ?)
			ON CONFLICT (kind, shelf) DO NOTHING`, kind, key); err != nil {
			return fmt.Errorf("store: %s: %w", what, err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE shelf_settings SET "+set+" WHERE kind = ? AND shelf = ?",
			append(args, kind, key)...); err != nil {
			return fmt.Errorf("store: %s: %w", what, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shelf_settings
			WHERE kind = ? AND shelf = ? AND hidden = 0 AND locked = 0 AND name IS NULL AND position IS NULL`,
			kind, key); err != nil {
			return fmt.Errorf("store: %s: %w", what, err)
		}
		return nil
	})
}

// SetShelfHidden hides a shelf of films or of series from devices, with
// every title on it, or shows it again. key is the shelf's LibraryShelf.Key.
func (s *Store) SetShelfHidden(ctx context.Context, kind, key string, hidden bool) error {
	return s.changeShelf(ctx, "hide a shelf", kind, key, "hidden = ?", hidden)
}

// SetShelfLocked locks a shelf of films or of series, with every title on
// it, so that devices need the parental PIN to play them (D85), or unlocks
// it. key is the shelf's LibraryShelf.Key.
func (s *Store) SetShelfLocked(ctx context.Context, kind, key string, locked bool) error {
	return s.changeShelf(ctx, "lock a shelf", kind, key, "locked = ?", locked)
}

// RenameShelf gives a shelf of films or of series the name that devices
// see it by; "" gives it back its sources' name. It fails with ErrBadName
// for a name that cannot be one.
func (s *Store) RenameShelf(ctx context.Context, kind, key, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return s.changeShelf(ctx, "rename a shelf", kind, key, "name = NULL")
	}
	if utf8.RuneCountInString(name) > MaxShelfName || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return ErrBadName
	}
	if own, ok := strings.CutPrefix(key, "name:"); ok && own == name {
		return s.changeShelf(ctx, "rename a shelf", kind, key, "name = NULL")
	}
	return s.changeShelf(ctx, "rename a shelf", kind, key, "name = ?", name)
}

// PlaceShelves puts shelves of films or of series in an order of their
// own: those whose keys are given, in that order, from the first; the
// others after them, in their sources' order. No keys gives every shelf back
// its sources' order.
func (s *Store) PlaceShelves(ctx context.Context, kind string, keys []string) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE shelf_settings SET position = NULL WHERE kind = ?", kind); err != nil {
			return fmt.Errorf("store: place shelves: %w", err)
		}
		for i, key := range keys {
			if _, err := tx.ExecContext(ctx, `INSERT INTO shelf_settings (kind, shelf, position) VALUES (?, ?, ?)
				ON CONFLICT (kind, shelf) DO UPDATE SET position = excluded.position`, kind, key, i+1); err != nil {
				return fmt.Errorf("store: place shelves: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM shelf_settings
			WHERE kind = ? AND hidden = 0 AND locked = 0 AND name IS NULL AND position IS NULL`, kind); err != nil {
			return fmt.Errorf("store: place shelves: %w", err)
		}
		return nil
	})
}

// AdminTitle is a film or a series as the admin lists it. Its Locked says
// whether devices need the PIN for it, for whatever reason.
type AdminTitle struct {
	TitleRow
	SourceID int64
	Source   string // its source's name
	Shelf    string // its category's name, as its source gives it; "" if none
	Hidden   bool   // the administrator hid it
	Own      bool   // the administrator locked it, rather than its shelf (D85)
	Adult    bool   // its source marks it for adults (D86)
}

// adminTitleColumns are what the admin lists of a title beyond its row.
const adminTitleColumns = `s.id, s.name, COALESCE(c.name, ''), t.hidden, t.locked, t.adult`

func scanAdminTitle(row rowScanner) (AdminTitle, error) {
	var a AdminTitle
	r, err := scanTitleRow(row, &a.SourceID, &a.Source, &a.Shelf, &a.Hidden, &a.Own, &a.Adult)
	a.TitleRow = r
	return a, err
}

// AdminTitles returns a page of the films or of the series that the admin
// lists, hidden ones too: those on a shelf, by its LibraryShelf, or all of
// them for nil; whose names hold text if it is not "" (ignoring the case of
// letters A to Z); by name, from offset, at most limit of them; and how many
// there are in all.
func (s *Store) AdminTitles(ctx context.Context, kind string, shelf *LibraryShelf, text string, offset, limit int) ([]AdminTitle, int, error) {
	if err := checkKind(kind); err != nil {
		return nil, 0, err
	}
	where := `t.kind = ? AND ` + libraryListed
	args := []any{kind}
	switch {
	case shelf == nil:
	case shelf.Portal:
		where += " AND t.category_id = ? AND t.position > 0"
		args = append(args, shelf.ID)
	default:
		where += " AND t.category_id IN (SELECT id FROM library_categories WHERE kind = ? AND name = ?) AND " + mergedShelf
		args = append(args, kind, shelf.Own)
	}
	if text = strings.TrimSpace(text); text != "" {
		where += ` AND t.name LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscaper.Replace(text)+"%")
	}
	const from = ` FROM titles t JOIN sources s ON s.id = t.source_id
		LEFT JOIN library_categories c ON c.id = t.category_id WHERE `
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+from+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: titles for the admin: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+titleRowColumns+`, `+adminTitleColumns+
		from+where+` ORDER BY t.name COLLATE NOCASE, t.id LIMIT ? OFFSET ?`, append(args, max(0, limit), max(0, offset))...)
	if err != nil {
		return nil, 0, fmt.Errorf("store: titles for the admin: %w", err)
	}
	defer rows.Close()
	out := []AdminTitle{}
	for rows.Next() {
		a, err := scanAdminTitle(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("store: titles for the admin: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("store: titles for the admin: %w", err)
	}
	return out, total, nil
}

// SetTitlesHidden hides films or series from devices, or shows them again,
// and returns how many of them there were.
func (s *Store) SetTitlesHidden(ctx context.Context, ids []int64, hidden bool) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, "UPDATE titles SET hidden = ? WHERE id = ?", hidden, id)
			if err != nil {
				return fmt.Errorf("store: hide a title: %w", err)
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return nil
	})
	return n, err
}

// SetTitlesLocked locks films or series, so that devices need the parental
// PIN to play them (D85), or unlocks them, and returns how many of them
// there were. A title on a locked shelf stays locked.
func (s *Store) SetTitlesLocked(ctx context.Context, ids []int64, locked bool) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, "UPDATE titles SET locked = ? WHERE id = ?", locked, id)
			if err != nil {
				return fmt.Errorf("store: lock a title: %w", err)
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return nil
	})
	return n, err
}

// LibraryLocks counts the shelves of films and of series that the
// administrator locked, and the titles locked one by one (D85).
func (s *Store) LibraryLocks(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM shelf_settings WHERE locked = 1)
		+ (SELECT count(*) FROM titles WHERE locked = 1 AND missing_since IS NULL)`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("store: locks on films and series: %w", err)
	}
	return n, nil
}

// AdminTitleByID returns a film or a series that the admin lists, hidden or
// not, and ErrNotFound if there is none.
func (s *Store) AdminTitleByID(ctx context.Context, id int64) (AdminTitle, error) {
	a, err := scanAdminTitle(s.db.QueryRowContext(ctx, `SELECT `+titleRowColumns+`, `+adminTitleColumns+`
		FROM titles t JOIN sources s ON s.id = t.source_id LEFT JOIN library_categories c ON c.id = t.category_id
		WHERE t.id = ? AND `+libraryListed, id))
	if errors.Is(err, sql.ErrNoRows) {
		return AdminTitle{}, ErrNotFound
	}
	if err != nil {
		return AdminTitle{}, fmt.Errorf("store: title %d for the admin: %w", id, err)
	}
	return a, nil
}
