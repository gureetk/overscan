// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/source"
)

// The library (schema 17): films and series, read from their sources when
// devices ask for them (D63), and how far each account got into them (D65).
// Xtream servers, Jellyfin servers (D66, schema 18), folders (D67, schema
// 19), Plex servers (D72) and Stalker portals (D77, schema 23) have them.

// Kinds of title, and of what plays.
const (
	KindMovie   = "movie"
	KindSeries  = "series"
	KindEpisode = "episode" // only of what plays: an episode of a series
)

// LibraryGrace is how long a title or an episode that its source stopped
// listing is kept, so that it keeps its ID, and the progress made in it, if
// it comes back.
const LibraryGrace = 7 * 24 * time.Hour

// LibraryCategory is a category of films or of series as a read of its
// source lists it.
type LibraryCategory struct {
	Key  string // the source's ID for it
	Name string
}

// LibraryTitle is a film or a series as a read of its source lists it.
type LibraryTitle struct {
	Key       string // the source's ID for it
	Category  string // the Key of its category, "" if none
	Name      string
	Year      int    // 0 if not known
	Rating    int    // out of 100, 0 if not known
	Poster    string // the address of its picture, "" if none
	Container string // a film's file extension, "" if not known
	// File is the source's path of a film's file where its Key is not
	// enough to play it: a Plex server's part (D72); "" for other sources.
	File    string
	AddedAt time.Time
	Summary TitleSummary
	// Adult is set when the source marks the title as one for adults
	// (D86).
	Adult bool
}

// TitleSummary is what a source's list says about a title beyond its name.
type TitleSummary struct {
	Plot     string `json:"plot,omitempty"`
	Genre    string `json:"genre,omitempty"`
	Cast     string `json:"cast,omitempty"`
	Director string `json:"director,omitempty"`
	Backdrop string `json:"backdrop,omitempty"` // the address of a wide picture
	Runtime  int    `json:"runtime,omitempty"`  // minutes
}

// TitleDetails is what a source says about a title when a device opens it.
type TitleDetails struct {
	TitleSummary
	Poster    string          `json:"poster,omitempty"`
	Country   string          `json:"country,omitempty"`
	Released  string          `json:"released,omitempty"`
	AgeRating string          `json:"age_rating,omitempty"`
	Year      int             `json:"year,omitempty"`
	Rating    int             `json:"rating,omitempty"`   // out of 100
	Duration  int             `json:"duration,omitempty"` // a film's seconds
	Seasons   []SeasonDetails `json:"seasons,omitempty"`  // a series' seasons, as its source describes them
}

// SeasonDetails is what a source says about a season of a series.
type SeasonDetails struct {
	Number int    `json:"number"`
	Name   string `json:"name,omitempty"`
	Poster string `json:"poster,omitempty"`
	Plot   string `json:"plot,omitempty"`
}

// LibraryEpisode is an episode as a series' details list it.
type LibraryEpisode struct {
	Key       string // the source's ID for it
	Season    int
	Number    int
	Name      string
	Container string
	Plot      string
	Picture   string
	Duration  int    // seconds, 0 if not known
	File      string // as LibraryTitle's
}

// LibraryChanges counts what a read changed.
type LibraryChanges struct {
	Added   int // titles new to the library
	Updated int // titles it had already
	Missing int // titles the source stopped listing
	Purged  int // titles missing for longer than LibraryGrace, now removed
}

// checkKind returns an error unless kind is KindMovie or KindSeries.
func checkKind(kind string) error {
	if kind != KindMovie && kind != KindSeries {
		return fmt.Errorf("store: %q is not a kind of title", kind)
	}
	return nil
}

// ApplyLibrary makes a source's films or series match a read that listed
// categories and titles, in order, at the given time. It all happens in one
// transaction: listed categories and titles are added or updated, those not
// listed are marked missing, titles missing for longer than LibraryGrace are
// removed, and the read is recorded. Details that devices made Overscan read
// are kept. A title's container and picture are kept when the list gives
// none.
func (s *Store) ApplyLibrary(ctx context.Context, sourceID int64, kind string, categories []LibraryCategory,
	titles []LibraryTitle, at time.Time) (LibraryChanges, error) {
	var ch LibraryChanges
	fail := func(err error) (LibraryChanges, error) {
		return LibraryChanges{}, fmt.Errorf("store: %s library of source %d: %w", kind, sourceID, err)
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
		VALUES (?, ?, 1, ?, ?, '', ?)
		ON CONFLICT (source_id, kind) DO UPDATE SET
			generation = generation + 1,
			read_at = excluded.read_at,
			tried_at = excluded.tried_at,
			read_error = '',
			titles = excluded.titles
		RETURNING generation`,
		sourceID, kind, when, when, len(titles)).Scan(&gen); err != nil {
		return fail(err)
	}

	cats := map[string]int64{}
	for _, c := range categories {
		if c.Key == "" || c.Name == "" {
			continue
		}
		if _, ok := cats[c.Key]; ok {
			continue
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO library_categories (source_id, kind, source_key, name, position, generation)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (source_id, kind, source_key) DO UPDATE SET
				name = excluded.name, position = excluded.position, generation = excluded.generation
			RETURNING id`,
			sourceID, kind, c.Key, c.Name, len(cats)+1, gen).Scan(&id); err != nil {
			return fail(err)
		}
		cats[c.Key] = id
	}

	known := map[string]bool{}
	rows, err := tx.QueryContext(ctx, "SELECT source_key FROM titles WHERE source_id = ? AND kind = ?", sourceID, kind)
	if err != nil {
		return fail(err)
	}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return fail(err)
		}
		known[key] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fail(err)
	}

	upsert, err := tx.PrepareContext(ctx, `
		INSERT INTO titles (source_id, kind, source_key, category_id, name, year, rating, poster, container, file,
			added_at, summary, adult, position, generation, missing_since)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
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
			generation = excluded.generation,
			missing_since = NULL`)
	if err != nil {
		return fail(err)
	}
	defer upsert.Close()
	listed := map[string]bool{}
	for i, t := range titles {
		if t.Key == "" || t.Name == "" || listed[t.Key] {
			continue
		}
		listed[t.Key] = true
		var category any
		if id, ok := cats[t.Category]; ok {
			category = id
		}
		summary, err := json.Marshal(t.Summary)
		if err != nil {
			return fail(err)
		}
		if _, err := upsert.ExecContext(ctx, sourceID, kind, t.Key, category, t.Name, max(0, t.Year),
			min(max(0, t.Rating), 100), t.Poster, t.Container, t.File, unixOrZero(t.AddedAt), string(summary), t.Adult,
			i+1, gen); err != nil {
			return fail(err)
		}
		if known[t.Key] {
			ch.Updated++
		} else {
			ch.Added++
		}
	}

	if _, err := tx.ExecContext(ctx, "UPDATE library_reads SET titles = ? WHERE source_id = ? AND kind = ?",
		len(listed), sourceID, kind); err != nil {
		return fail(err)
	}
	res, err := tx.ExecContext(ctx, `
		UPDATE titles SET missing_since = ?
		WHERE source_id = ? AND kind = ? AND generation <> ? AND missing_since IS NULL`,
		when, sourceID, kind, gen)
	if err != nil {
		return fail(err)
	}
	if ch.Missing, err = rowsAffected(res); err != nil {
		return fail(err)
	}
	res, err = tx.ExecContext(ctx, `
		DELETE FROM titles
		WHERE source_id = ? AND kind = ? AND missing_since IS NOT NULL AND missing_since < ?`,
		sourceID, kind, formatTime(at.Add(-LibraryGrace)))
	if err != nil {
		return fail(err)
	}
	if ch.Purged, err = rowsAffected(res); err != nil {
		return fail(err)
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
	return ch, nil
}

// unixOrZero returns t in Unix seconds, or 0 for the zero time.
func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// timeOrZero returns Unix seconds as a time, or the zero time for 0.
func timeOrZero(n int64) time.Time {
	if n <= 0 {
		return time.Time{}
	}
	return time.Unix(n, 0).UTC()
}

// SetLibraryFailed records that a read of a source's films or series failed
// at the given time, and why. What earlier reads found stays.
func (s *Store) SetLibraryFailed(ctx context.Context, sourceID int64, kind string, at time.Time, why string) error {
	if err := checkKind(kind); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO library_reads (source_id, kind, tried_at, read_error) VALUES (?, ?, ?, ?)
		ON CONFLICT (source_id, kind) DO UPDATE SET tried_at = excluded.tried_at, read_error = excluded.read_error`,
		sourceID, kind, formatTime(at), why)
	if err != nil {
		return fmt.Errorf("store: %s library of source %d: %w", kind, sourceID, err)
	}
	return nil
}

// LibraryRead is what the reads of a source's films or series did.
type LibraryRead struct {
	SourceID int64
	Kind     string
	ReadAt   time.Time // when the last read that worked ended; zero if none has
	TriedAt  time.Time // when the last read ended; zero if none has
	Error    string    // why the last read failed, "" if it worked
	Titles   int       // what the last read that worked listed
}

// LibraryReads returns what the reads of every source's films and series
// did, by source and kind. A source and kind never read have no entry.
func (s *Store) LibraryReads(ctx context.Context) ([]LibraryRead, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_id, kind, read_at, tried_at, read_error, titles FROM library_reads ORDER BY source_id, kind`)
	if err != nil {
		return nil, fmt.Errorf("store: library reads: %w", err)
	}
	defer rows.Close()
	var out []LibraryRead
	for rows.Next() {
		var r LibraryRead
		var readAt, triedAt sql.NullString
		if err := rows.Scan(&r.SourceID, &r.Kind, &readAt, &triedAt, &r.Error, &r.Titles); err != nil {
			return nil, fmt.Errorf("store: library reads: %w", err)
		}
		for _, t := range []struct {
			text sql.NullString
			to   *time.Time
		}{{readAt, &r.ReadAt}, {triedAt, &r.TriedAt}} {
			if t.text.Valid {
				v, err := parseTime(t.text.String)
				if err != nil {
					return nil, fmt.Errorf("store: library reads: %w", err)
				}
				*t.to = v
			}
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: library reads: %w", err)
	}
	return out, nil
}

// libraryListed is the condition, on a title t and its source s, that the
// admin lists it: its source is an enabled Xtream, Jellyfin or Plex server,
// folder or Stalker portal, and still lists it.
const libraryListed = `s.enabled = 1 AND s.type IN ('` + source.TypeXtream + `', '` + source.TypeJellyfin + `', '` +
	source.TypePlex + `', '` + source.TypeFolder + `', '` + source.TypeStalker + `') AND t.missing_since IS NULL`

// hiddenCategories is the SQL of the IDs of the categories on the shelves
// that the administrator hid (D79): every category of a merged shelf's
// name, or a portal's category.
const hiddenCategories = `SELECT hc.id FROM library_categories hc JOIN sources hs ON hs.id = hc.source_id
	JOIN shelf_settings hss ON hss.kind = hc.kind AND hss.hidden = 1 AND hss.shelf =
		CASE WHEN hs.type = '` + source.TypeStalker + `' THEN 'category:' || hc.id ELSE 'name:' || hc.name END`

// libraryShown is the condition, on a title t and its source s, that
// devices see it: the admin lists it, the administrator has not hidden it,
// nor the shelf it is on (D79), and it is not one that its source marks for
// adults, which the setting says to hide (D86).
const libraryShown = libraryListed + ` AND t.hidden = 0 AND (t.category_id IS NULL OR t.category_id NOT IN (` +
	hiddenCategories + `)) AND NOT (t.adult = 1 AND ` + adultMarksSQL + ` = '` + AdultHide + `')`

// lockedCategories is the SQL of the IDs of the categories on the shelves
// that the administrator locked (D85), as hiddenCategories is of those
// hidden.
const lockedCategories = `SELECT lc.id FROM library_categories lc JOIN sources ls ON ls.id = lc.source_id
	JOIN shelf_settings lss ON lss.kind = lc.kind AND lss.locked = 1 AND lss.shelf =
		CASE WHEN ls.type = '` + source.TypeStalker + `' THEN 'category:' || lc.id ELSE 'name:' || lc.name END`

// titleLocked is true of a title t that needs the parental PIN, once the
// account has one: the administrator locked it, or the shelf it is on
// (D85), or its source marks it for adults, which the setting says to lock
// (D86).
const titleLocked = `(t.locked = 1 OR t.category_id IS NOT NULL AND t.category_id IN (` + lockedCategories + `)
	OR t.adult = 1 AND ` + adultMarksSQL + ` = '` + AdultLock + `')`

// mergedShelf is the condition, on a source s, that its categories are
// shelves that merge with other sources' categories of the same name: a
// Stalker portal's are shelves of their own (D77).
const mergedShelf = `s.type <> '` + source.TypeStalker + `'`

// Shelf is a category of films or of series as devices see it: the
// categories of the same name from every source, as one, or a Stalker
// portal's category, as a shelf of its own (D77).
type Shelf struct {
	ID    int64 // the ID of one of its categories, which stays the same across reads
	Name  string
	Count int // the titles on it; for a portal's, how many items the portal says it holds, -1 until one is read
	// Portal says that the shelf is a Stalker portal's category, whose
	// titles are read a page at a time as devices browse it (D77).
	Portal bool
	// Locked is set when the administrator locked the shelf, which locks
	// every title on it (D85).
	Locked bool
}

// Shelves returns the shelves of films or of series that devices see, by
// the names and in the order that the administrator set, or else their
// sources' (D79), leaving out those with nothing on them; a portal's
// categories are on it until a page of them shows them empty.
func (s *Store) Shelves(ctx context.Context, kind string) ([]Shelf, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, n, portal, locked FROM (
			SELECT min(c.id) AS id, COALESCE(max(ss.name), c.name) AS name, count(t.id) AS n, 0 AS portal,
				min(c.source_id) AS src, min(c.position) AS pos, max(ss.position) AS place, c.name AS own,
				COALESCE(max(ss.locked), 0) AS locked
			FROM library_categories c
				JOIN titles t ON t.category_id = c.id
				JOIN sources s ON s.id = t.source_id
				LEFT JOIN shelf_settings ss ON ss.kind = c.kind AND ss.shelf = 'name:' || c.name
			WHERE c.kind = ? AND `+libraryShown+` AND `+mergedShelf+`
			GROUP BY c.name
			UNION ALL
			SELECT c.id, COALESCE(ss.name, c.name), COALESCE(c.total, -1), 1, c.source_id, c.position, ss.position, c.name,
				COALESCE(ss.locked, 0)
			FROM library_categories c JOIN sources s ON s.id = c.source_id
				LEFT JOIN shelf_settings ss ON ss.kind = c.kind AND ss.shelf = 'category:' || c.id
			WHERE c.kind = ? AND `+portalCategoryShown+` AND COALESCE(c.total, -1) <> 0)
		ORDER BY place IS NULL, place, src, pos, own`, kind, kind)
	if err != nil {
		return nil, fmt.Errorf("store: %s shelves: %w", kind, err)
	}
	defer rows.Close()
	out := []Shelf{}
	for rows.Next() {
		var sh Shelf
		if err := rows.Scan(&sh.ID, &sh.Name, &sh.Count, &sh.Portal, &sh.Locked); err != nil {
			return nil, fmt.Errorf("store: %s shelves: %w", kind, err)
		}
		out = append(out, sh)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s shelves: %w", kind, err)
	}
	return out, nil
}

// TitleRow is a film or a series as a list shows it.
type TitleRow struct {
	ID      int64
	Kind    string
	Name    string
	Year    int    // 0 if not known
	Rating  int    // out of 100, 0 if not known
	Poster  string // the source's address of its picture, "" if none; devices get one on Overscan
	AddedAt time.Time
	// Locked is set when devices need the parental PIN to play it, once
	// the account has one (D85, D86).
	Locked bool
}

const titleRowColumns = `t.id, t.kind, t.name, t.year, t.rating, t.poster, t.added_at, ` + titleLocked

func scanTitleRow(row rowScanner, extra ...any) (TitleRow, error) {
	var r TitleRow
	var added int64
	if err := row.Scan(append([]any{&r.ID, &r.Kind, &r.Name, &r.Year, &r.Rating, &r.Poster, &added, &r.Locked},
		extra...)...); err != nil {
		return TitleRow{}, err
	}
	r.AddedAt = timeOrZero(added)
	return r, nil
}

func (s *Store) titleRows(ctx context.Context, what, query string, args ...any) ([]TitleRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	defer rows.Close()
	out := []TitleRow{}
	for rows.Next() {
		r, err := scanTitleRow(rows)
		if err != nil {
			return nil, fmt.Errorf("store: %s: %w", what, err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: %s: %w", what, err)
	}
	return out, nil
}

// Orders of a shelf's titles.
const (
	ByAdded = "added" // the latest added first
	ByName  = "name"  // A to Z
)

// ShelfTitles returns the titles on a shelf that devices see, the latest
// added first or by name, and ErrNotFound for a shelf that does not exist.
func (s *Store) ShelfTitles(ctx context.Context, kind string, shelfID int64, order string) ([]TitleRow, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT name FROM library_categories WHERE id = ? AND kind = ?", shelfID, kind).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: shelf %d: %w", shelfID, err)
	}
	sort := "t.added_at DESC, t.source_id, t.position"
	if order == ByName {
		sort = "t.name COLLATE NOCASE, t.id"
	}
	return s.titleRows(ctx, "shelf "+strconv.FormatInt(shelfID, 10), `
		SELECT `+titleRowColumns+`
		FROM library_categories c
			JOIN titles t ON t.category_id = c.id
			JOIN sources s ON s.id = t.source_id
		WHERE c.kind = ? AND c.name = ? AND `+libraryShown+` AND `+mergedShelf+`
		ORDER BY `+sort, kind, name)
}

// RecentTitles returns the films or series that their sources added last,
// at most limit of them, the latest first. Titles whose source gives no
// time are left out.
func (s *Store) RecentTitles(ctx context.Context, kind string, limit int) ([]TitleRow, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	return s.titleRows(ctx, "recent "+kind, `
		SELECT `+titleRowColumns+`
		FROM titles t JOIN sources s ON s.id = t.source_id
		WHERE t.kind = ? AND t.added_at > 0 AND `+libraryShown+`
		ORDER BY t.added_at DESC, t.source_id, t.position
		LIMIT ?`, kind, limit)
}

// SearchTitles returns the films and series whose names hold text,
// ignoring the case of letters A to Z, by name, at most limit of each
// kind.
func (s *Store) SearchTitles(ctx context.Context, text string, limit int) ([]TitleRow, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return []TitleRow{}, nil
	}
	pattern := "%" + likeEscaper.Replace(text) + "%"
	return s.titleRows(ctx, "search titles", `
		SELECT `+titleRowColumns+` FROM (
			SELECT t.*, row_number() OVER (PARTITION BY t.kind ORDER BY t.name COLLATE NOCASE, t.id) AS n
			FROM titles t JOIN sources s ON s.id = t.source_id
			WHERE t.name LIKE ? ESCAPE '\' AND `+libraryShown+`) t
		WHERE t.n <= ?
		ORDER BY t.kind, t.name COLLATE NOCASE, t.id`, pattern, limit)
}

// TitleListing is a film or a series as a list of the Stalker API shows it:
// its row, its category, and what its source said of it.
type TitleListing struct {
	TitleRow
	Shelf      string // the name of its category, "" if none
	CategoryID int64  // its category's ID, 0 if none
	Summary    TitleSummary
	Details    *TitleDetails // nil until a device opened it
}

// TitlePage returns a page of the films or of the series that devices see:
// those on a shelf, or all of them for shelf 0, whose names hold text if it
// is not "" (ignoring the case of letters A to Z), in an order (ByAdded or
// ByName), from offset, at most limit of them; and how many there are in
// all. With unlocked, it leaves out the titles that are locked (D85). It
// returns ErrNotFound for a shelf that does not exist.
func (s *Store) TitlePage(ctx context.Context, kind string, shelfID int64, text, order string, offset, limit int,
	unlocked bool) ([]TitleListing, int, error) {
	if err := checkKind(kind); err != nil {
		return nil, 0, err
	}
	what := "page of " + kind
	where := `t.kind = ? AND ` + libraryShown
	if unlocked {
		where += ` AND NOT ` + titleLocked
	}
	args := []any{kind}
	if shelfID != 0 {
		var name string
		err := s.db.QueryRowContext(ctx, "SELECT name FROM library_categories WHERE id = ? AND kind = ?", shelfID, kind).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, 0, ErrNotFound
		}
		if err != nil {
			return nil, 0, fmt.Errorf("store: %s: shelf %d: %w", what, shelfID, err)
		}
		where += " AND t.category_id IN (SELECT id FROM library_categories WHERE kind = ? AND name = ?) AND " + mergedShelf
		args = append(args, kind, name)
	}
	if text = strings.TrimSpace(text); text != "" {
		where += ` AND t.name LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscaper.Replace(text)+"%")
	}
	const from = ` FROM titles t JOIN sources s ON s.id = t.source_id
		LEFT JOIN library_categories c ON c.id = t.category_id WHERE `
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+from+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("store: %s: %w", what, err)
	}
	sort := "t.added_at DESC, t.source_id, t.position"
	if order == ByName {
		sort = "t.name COLLATE NOCASE, t.id"
	}
	out, err := s.listings(ctx, what, `SELECT `+titleRowColumns+`, COALESCE(c.name, ''), COALESCE(t.category_id, 0),
		t.summary, t.details`+from+where+` ORDER BY `+sort+` LIMIT ? OFFSET ?`,
		append(args, max(0, limit), max(0, offset))...)
	if err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// Title is a film or a series with all that Overscan knows of it.
type Title struct {
	TitleRow
	SourceID   int64
	SourceType string // such as source.TypeXtream
	Key        string // the source's ID for it
	Container  string // a film's file extension, "" if not known
	// File is as LibraryTitle's; for a Stalker portal's title, what its
	// list said that plays it, or opens a series (D77).
	File       string
	Shelf      string // the name of its category, "" if none
	CategoryID int64  // its category's ID, 0 if none
	Summary    TitleSummary
	Details    *TitleDetails // nil until a device opened it
	DetailsAt  time.Time
}

// Title returns the title with the given ID if devices see it, and
// ErrNotFound if they do not.
func (s *Store) Title(ctx context.Context, id int64) (Title, error) {
	var t Title
	var summary string
	var details, detailsAt sql.NullString
	r, err := scanTitleRow(s.db.QueryRowContext(ctx, `
		SELECT `+titleRowColumns+`, t.source_id, s.type, t.source_key, t.container, t.file, COALESCE(c.name, ''),
			COALESCE(t.category_id, 0), t.summary, t.details, t.details_at
		FROM titles t JOIN sources s ON s.id = t.source_id
			LEFT JOIN library_categories c ON c.id = t.category_id
		WHERE t.id = ? AND `+libraryShown, id),
		&t.SourceID, &t.SourceType, &t.Key, &t.Container, &t.File, &t.Shelf, &t.CategoryID, &summary, &details,
		&detailsAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Title{}, ErrNotFound
	}
	fail := func(err error) (Title, error) { return Title{}, fmt.Errorf("store: title %d: %w", id, err) }
	if err != nil {
		return fail(err)
	}
	t.TitleRow = r
	if err := json.Unmarshal([]byte(summary), &t.Summary); err != nil {
		return fail(err)
	}
	if details.Valid {
		t.Details = &TitleDetails{}
		if err := json.Unmarshal([]byte(details.String), t.Details); err != nil {
			return fail(err)
		}
		if detailsAt.Valid {
			if t.DetailsAt, err = parseTime(detailsAt.String); err != nil {
				return fail(err)
			}
		}
	}
	return t, nil
}

// SetMovieDetails keeps what a source said about a film when a device
// opened it, and its file's extension if the details give one.
func (s *Store) SetMovieDetails(ctx context.Context, id int64, d TitleDetails, container string, at time.Time) error {
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE titles SET details = ?, details_at = ?, container = CASE WHEN ? <> '' THEN ? ELSE container END
		WHERE id = ? AND kind = ?`,
		string(data), formatTime(at), container, container, id, KindMovie)
	if err != nil {
		return fmt.Errorf("store: details of film %d: %w", id, err)
	}
	return nil
}

// SetSeriesDetails keeps what a source said about a series when a device
// opened it, and makes its episodes match the ones the details list, in
// one transaction: listed episodes are added or updated, those not listed
// are marked missing, and those missing for longer than LibraryGrace are
// removed with the progress made in them.
func (s *Store) SetSeriesDetails(ctx context.Context, id int64, d TitleDetails, episodes []LibraryEpisode, at time.Time) error {
	fail := func(err error) error { return fmt.Errorf("store: details of series %d: %w", id, err) }
	data, err := json.Marshal(d)
	if err != nil {
		return fail(err)
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		when := formatTime(at)
		res, err := tx.ExecContext(ctx, "UPDATE titles SET details = ?, details_at = ? WHERE id = ? AND kind = ?",
			string(data), when, id, KindSeries)
		if err != nil {
			return fail(err)
		}
		if n, err := rowsAffected(res); err != nil || n == 0 {
			if err == nil {
				err = ErrNotFound
			}
			return fail(err)
		}
		// Every episode is missing until the upsert finds it listed.
		if _, err := tx.ExecContext(ctx, `UPDATE episodes SET missing_since = ?
			WHERE title_id = ? AND missing_since IS NULL`, when, id); err != nil {
			return fail(err)
		}
		upsert, err := tx.PrepareContext(ctx, `
			INSERT INTO episodes (title_id, source_key, season, number, name, container, duration, plot, picture,
				file, position, missing_since)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
			ON CONFLICT (title_id, source_key) DO UPDATE SET
				season = excluded.season, number = excluded.number, name = excluded.name,
				container = excluded.container, duration = excluded.duration, plot = excluded.plot,
				picture = excluded.picture, file = excluded.file, position = excluded.position, missing_since = NULL`)
		if err != nil {
			return fail(err)
		}
		defer upsert.Close()
		listed := map[string]bool{}
		for i, e := range episodes {
			if e.Key == "" || listed[e.Key] {
				continue
			}
			listed[e.Key] = true
			if _, err := upsert.ExecContext(ctx, id, e.Key, max(0, e.Season), max(0, e.Number), e.Name, e.Container,
				max(0, e.Duration), e.Plot, e.Picture, e.File, i+1); err != nil {
				return fail(err)
			}
		}
		gone := `SELECT id FROM episodes WHERE title_id = ? AND missing_since IS NOT NULL AND missing_since < ?`
		cutoff := formatTime(at.Add(-LibraryGrace))
		if _, err := tx.ExecContext(ctx, `DELETE FROM progress WHERE title_id = ? AND episode_id IN (`+gone+`)`,
			id, id, cutoff); err != nil {
			return fail(err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM episodes WHERE id IN (`+gone+`)`, id, cutoff); err != nil {
			return fail(err)
		}
		return nil
	})
}

// Episode is an episode of a series.
type Episode struct {
	ID        int64
	TitleID   int64
	Season    int
	Number    int
	Name      string // "" if the source gives none
	Container string
	Plot      string
	Picture   string // the source's address of a picture from it
	Duration  int    // seconds, 0 if not known
}

const episodeColumns = `e.id, e.title_id, e.season, e.number, e.name, e.container, e.plot, e.picture, e.duration`

func scanEpisode(row rowScanner) (Episode, error) {
	var e Episode
	err := row.Scan(&e.ID, &e.TitleID, &e.Season, &e.Number, &e.Name, &e.Container, &e.Plot, &e.Picture, &e.Duration)
	return e, err
}

// Episodes returns the episodes of a series that its source still lists,
// by season and number.
func (s *Store) Episodes(ctx context.Context, titleID int64) ([]Episode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+episodeColumns+` FROM episodes e
		WHERE e.title_id = ? AND e.missing_since IS NULL
		ORDER BY e.season, e.number, e.position`, titleID)
	if err != nil {
		return nil, fmt.Errorf("store: episodes of %d: %w", titleID, err)
	}
	defer rows.Close()
	out := []Episode{}
	for rows.Next() {
		e, err := scanEpisode(rows)
		if err != nil {
			return nil, fmt.Errorf("store: episodes of %d: %w", titleID, err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: episodes of %d: %w", titleID, err)
	}
	return out, nil
}

// PlayableItem is what the stream hub needs to play a film or an episode.
// It holds the stream's address, with the source's login in it, so it never
// goes to a device.
type PlayableItem struct {
	Kind      string // KindMovie or KindEpisode
	ID        int64  // the film's title ID, or the episode's ID
	TitleID   int64  // the film, or the episode's series
	SourceID  int64
	Name      string // as logs and the admin show it
	StreamURL string // "" for a file in a folder
	// TranscodeURL is where the source's server transcodes it (D71), for a
	// Jellyfin server's film or episode; "" for others.
	TranscodeURL string
	// Folder and File are, for a film or an episode in a folder, the
	// folder's path and the file's path in it.
	Folder    string
	File      string
	Container string // the file's extension: mp4 when the source gives none
	// Key is the source's ID for the film or the episode: a Jellyfin
	// item's, an Xtream stream's or episode's, or the file's path in a
	// folder.
	Key string
	// Locked is set when devices need the parental PIN to play it, once
	// the account has one: the film, or the episode's series, is locked
	// (D85, D86).
	Locked bool
}

// defaultContainer is the extension of a file whose source gives none.
const defaultContainer = "mp4"

// PlayableMovie returns what plays the film with the given title ID, if
// devices see it.
func (s *Store) PlayableMovie(ctx context.Context, id int64) (PlayableItem, error) {
	p := PlayableItem{Kind: KindMovie, ID: id, TitleID: id}
	var key, file string
	var sourceName, sourceType string
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT t.source_id, t.name, t.source_key, t.container, t.file, s.name, s.type,
			(SELECT secret FROM source_secrets WHERE source_id = s.id), `+titleLocked+`
		FROM titles t JOIN sources s ON s.id = t.source_id
		WHERE t.id = ? AND t.kind = ? AND `+libraryShown, id, KindMovie).Scan(
		&p.SourceID, &p.Name, &key, &p.Container, &file, &sourceName, &sourceType, &sealed, &p.Locked)
	if errors.Is(err, sql.ErrNoRows) {
		return PlayableItem{}, ErrNotFound
	}
	if err == nil {
		p.Key = key
		err = s.libraryStream(&p, sourceName, sourceType, sealed, "movie", key, file)
	}
	if err != nil {
		return PlayableItem{}, fmt.Errorf("store: film %d: %w", id, err)
	}
	return p, nil
}

// PlayableEpisode returns what plays the episode with the given ID, if
// devices see it and its series.
func (s *Store) PlayableEpisode(ctx context.Context, id int64) (PlayableItem, error) {
	p := PlayableItem{Kind: KindEpisode, ID: id}
	var key, file, series string
	var season, number int
	var sourceName, sourceType string
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id, t.source_id, t.name, e.season, e.number, e.source_key, e.container, e.file, s.name, s.type,
			(SELECT secret FROM source_secrets WHERE source_id = s.id), `+titleLocked+`
		FROM episodes e JOIN titles t ON t.id = e.title_id JOIN sources s ON s.id = t.source_id
		WHERE e.id = ? AND e.missing_since IS NULL AND `+libraryShown, id).Scan(
		&p.TitleID, &p.SourceID, &series, &season, &number, &key, &p.Container, &file, &sourceName, &sourceType,
		&sealed, &p.Locked)
	if errors.Is(err, sql.ErrNoRows) {
		return PlayableItem{}, ErrNotFound
	}
	if err == nil {
		p.Name = EpisodeLabel(series, season, number)
		p.Key = key
		err = s.libraryStream(&p, sourceName, sourceType, sealed, "series", key, file)
	}
	if err != nil {
		return PlayableItem{}, fmt.Errorf("store: episode %d: %w", id, err)
	}
	return p, nil
}

// EpisodeLabel names an episode as logs and the admin show it, such as
// "Coastline S1 E2".
func EpisodeLabel(series string, season, number int) string {
	return series + " S" + strconv.Itoa(season) + " E" + strconv.Itoa(number)
}

// libraryStream fills in where a film or an episode plays from: on an
// Xtream source, the address with /movie/ or /series/, the login, and the
// file; on a Jellyfin source, the item's file with the API key; on a Plex
// source, the path of its file's part with the token; in a folder, the
// folder and the file; on a Stalker portal, what its create_link takes,
// which the hub turns into an address. It fills in the default container
// if the source gave none.
func (s *Store) libraryStream(p *PlayableItem, name, typ string, sealed []byte, kind, key, file string) error {
	if typ != source.TypeXtream && typ != source.TypeJellyfin && typ != source.TypePlex && typ != source.TypeFolder &&
		typ != source.TypeStalker {
		return fmt.Errorf("source %s has no library", name)
	}
	if typ == source.TypeStalker {
		// What the portal's create_link takes, which the stream hub asks
		// the portal with as the stream starts (D77).
		if file == "" {
			return errors.New("the portal gave nothing to play it with")
		}
		p.StreamURL = file
		if p.Container == "" {
			p.Container = defaultContainer
		}
		return nil
	}
	if sealed == nil {
		return ErrNoAddress
	}
	secret, err := s.openSource(p.SourceID, sealed)
	if err != nil {
		return err
	}
	if typ == source.TypeFolder {
		p.Folder, p.File = secret.URL, key
		if p.Container == "" {
			p.Container = defaultContainer
		}
		return nil
	}
	if typ == source.TypePlex {
		if file == "" {
			return errors.New("the source gave no file for it")
		}
		if p.Container == "" {
			p.Container = defaultContainer
		}
		settings := source.Settings{Name: name, Type: typ, URL: secret.URL, Password: secret.Password}
		p.StreamURL, err = settings.StreamURL(kind + "/" + url.PathEscape(file))
		return err
	}
	ref := kind + "/" + key
	switch {
	case p.Container != "":
		ref += "." + p.Container
	case typ == source.TypeXtream:
		p.Container = defaultContainer
		ref += "." + p.Container
	default:
		// Jellyfin plays a file without an extension as it is; the box's
		// link still gets one.
		p.Container = defaultContainer
	}
	settings := source.Settings{Name: name, Type: typ, URL: secret.URL, Username: secret.Username, Password: secret.Password}
	if p.StreamURL, err = settings.StreamURL(ref); err != nil {
		return err
	}
	p.TranscodeURL, err = settings.TranscodeURL(ref)
	return err
}

// Progress is how far an account got into a film or an episode.
type Progress struct {
	TitleID   int64 // the film, or the episode's series
	EpisodeID int64 // 0 for a film
	Position  int   // seconds; 0 once finished
	Duration  int   // seconds, 0 if not known
	Finished  bool
	WatchedAt time.Time
}

// SetProgress keeps how far an account got into a film or an episode. A
// finished one keeps position 0.
func (s *Store) SetProgress(ctx context.Context, accountID int64, p Progress) error {
	if p.Finished {
		p.Position = 0
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO progress (account_id, title_id, episode_id, position, duration, finished, watched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (account_id, title_id, episode_id) DO UPDATE SET
			position = excluded.position, duration = excluded.duration, finished = excluded.finished,
			watched_at = excluded.watched_at`,
		accountID, p.TitleID, p.EpisodeID, max(0, p.Position), max(0, p.Duration), p.Finished, p.WatchedAt.Unix())
	if err != nil {
		return fmt.Errorf("store: progress of account %d in %d/%d: %w", accountID, p.TitleID, p.EpisodeID, err)
	}
	return nil
}

const progressColumns = `p.title_id, p.episode_id, p.position, p.duration, p.finished, p.watched_at`

func scanProgress(row rowScanner) (Progress, error) {
	var p Progress
	var at int64
	err := row.Scan(&p.TitleID, &p.EpisodeID, &p.Position, &p.Duration, &p.Finished, &at)
	p.WatchedAt = timeOrZero(at)
	return p, err
}

// TitleProgress returns how far an account got into a film, or into each
// episode of a series that it played, the latest first.
func (s *Store) TitleProgress(ctx context.Context, accountID, titleID int64) ([]Progress, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+progressColumns+` FROM progress p
		WHERE p.account_id = ? AND p.title_id = ?
		ORDER BY p.watched_at DESC, p.episode_id DESC`, accountID, titleID)
	if err != nil {
		return nil, fmt.Errorf("store: progress in %d: %w", titleID, err)
	}
	defer rows.Close()
	out := []Progress{}
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("store: progress in %d: %w", titleID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: progress in %d: %w", titleID, err)
	}
	return out, nil
}

// Watching is a title that an account is part way through.
type Watching struct {
	Title    TitleRow
	Progress Progress // in a series, of the episode it played last
	Episode  *Episode // that episode, nil for a film
	Next     *Episode // the episode after it, if the account finished that one
}

// ContinueWatching returns, the latest first and at most limit of them,
// the films that an account is part way through, or the series whose
// latest episode it is part way through or has an episode after it, with
// that episode if the account finished the latest.
func (s *Store) ContinueWatching(ctx context.Context, accountID int64, kind string, limit int) ([]Watching, error) {
	if err := checkKind(kind); err != nil {
		return nil, err
	}
	var query string
	if kind == KindMovie {
		query = `
			SELECT ` + titleRowColumns + `, ` + progressColumns + `
			FROM progress p JOIN titles t ON t.id = p.title_id JOIN sources s ON s.id = t.source_id
			WHERE p.account_id = ? AND p.episode_id = 0 AND t.kind = 'movie' AND p.finished = 0 AND p.position > 0
				AND ` + libraryShown + `
			ORDER BY p.watched_at DESC LIMIT ?`
	} else {
		query = `
			WITH latest AS (
				SELECT p.*, row_number() OVER (PARTITION BY p.title_id ORDER BY p.watched_at DESC, p.episode_id DESC) AS n
				FROM progress p WHERE p.account_id = ? AND p.episode_id <> 0)
			SELECT ` + titleRowColumns + `, ` + progressColumns + `, ` + episodeColumns + `,
				coalesce(n.id, 0), coalesce(n.season, 0), coalesce(n.number, 0), coalesce(n.name, '')
			FROM latest p JOIN titles t ON t.id = p.title_id JOIN sources s ON s.id = t.source_id
				JOIN episodes e ON e.id = p.episode_id
				LEFT JOIN episodes n ON n.id = (
					SELECT later.id FROM episodes later WHERE later.title_id = t.id AND later.missing_since IS NULL
						AND (later.season, later.number) > (e.season, e.number)
					ORDER BY later.season, later.number LIMIT 1)
			WHERE p.n = 1 AND t.kind = 'series' AND ` + libraryShown + ` AND e.missing_since IS NULL
				AND ((p.finished = 0 AND p.position > 0) OR n.id IS NOT NULL)
			ORDER BY p.watched_at DESC LIMIT ?`
	}
	rows, err := s.db.QueryContext(ctx, query, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("store: continue watching %s: %w", kind, err)
	}
	defer rows.Close()
	out := []Watching{}
	for rows.Next() {
		var w Watching
		var at int64
		dest := []any{&w.Progress.TitleID, &w.Progress.EpisodeID, &w.Progress.Position, &w.Progress.Duration,
			&w.Progress.Finished, &at}
		var e, next Episode
		if kind == KindSeries {
			dest = append(dest, &e.ID, &e.TitleID, &e.Season, &e.Number, &e.Name, &e.Container, &e.Plot, &e.Picture, &e.Duration,
				&next.ID, &next.Season, &next.Number, &next.Name)
		}
		if w.Title, err = scanTitleRow(rows, dest...); err != nil {
			return nil, fmt.Errorf("store: continue watching %s: %w", kind, err)
		}
		w.Progress.WatchedAt = timeOrZero(at)
		if kind == KindSeries {
			w.Episode = &e
			if w.Progress.Finished && next.ID != 0 {
				next.TitleID = e.TitleID
				w.Next = &next
			}
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: continue watching %s: %w", kind, err)
	}
	return out, nil
}

// ArtSource is the source of a title's pictures: its pictures in a folder
// are read from the folder, the others fetched with its user agent.
type ArtSource struct {
	ID        int64
	Type      string
	UserAgent string
}

// TitleArt returns the addresses of a title's pictures that devices may ask
// for, if devices see it: its posters, from its source's list and from its
// details, and its wide pictures, with its source.
func (s *Store) TitleArt(ctx context.Context, id int64) (posters, backdrops []string, src ArtSource, err error) {
	var poster, summary string
	var details sql.NullString
	err = s.db.QueryRowContext(ctx, `
		SELECT t.poster, t.summary, t.details, s.id, s.type, s.user_agent
		FROM titles t JOIN sources s ON s.id = t.source_id
		WHERE t.id = ? AND `+libraryShown, id).Scan(&poster, &summary, &details, &src.ID, &src.Type, &src.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, ArtSource{}, ErrNotFound
	}
	if err != nil {
		return nil, nil, ArtSource{}, fmt.Errorf("store: pictures of title %d: %w", id, err)
	}
	var sum TitleSummary
	var d TitleDetails
	_ = json.Unmarshal([]byte(summary), &sum)
	if details.Valid {
		_ = json.Unmarshal([]byte(details.String), &d)
	}
	for _, p := range []string{poster, d.Poster} {
		if p != "" {
			posters = append(posters, p)
		}
	}
	for _, b := range []string{sum.Backdrop, d.Backdrop} {
		if b != "" {
			backdrops = append(backdrops, b)
		}
	}
	return posters, backdrops, src, nil
}

// EpisodeArt returns the address of an episode's picture, if devices see
// it, with its source.
func (s *Store) EpisodeArt(ctx context.Context, id int64) (picture string, src ArtSource, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT e.picture, s.id, s.type, s.user_agent
		FROM episodes e JOIN titles t ON t.id = e.title_id JOIN sources s ON s.id = t.source_id
		WHERE e.id = ? AND e.missing_since IS NULL AND `+libraryShown, id).Scan(&picture, &src.ID, &src.Type, &src.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ArtSource{}, ErrNotFound
	}
	if err != nil {
		return "", ArtSource{}, fmt.Errorf("store: picture of episode %d: %w", id, err)
	}
	return picture, src, nil
}

// MovieProgress returns how far an account got into each film that it
// played, by the film's title ID.
func (s *Store) MovieProgress(ctx context.Context, accountID int64) (map[int64]Progress, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+progressColumns+` FROM progress p WHERE p.account_id = ? AND p.episode_id = 0`, accountID)
	if err != nil {
		return nil, fmt.Errorf("store: progress of account %d in films: %w", accountID, err)
	}
	defer rows.Close()
	out := map[int64]Progress{}
	for rows.Next() {
		p, err := scanProgress(rows)
		if err != nil {
			return nil, fmt.Errorf("store: progress of account %d in films: %w", accountID, err)
		}
		out[p.TitleID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: progress of account %d in films: %w", accountID, err)
	}
	return out, nil
}

// EpisodeSeries returns the series of an episode that devices see.
func (s *Store) EpisodeSeries(ctx context.Context, episodeID int64) (int64, error) {
	var titleID int64
	err := s.db.QueryRowContext(ctx, `
		SELECT t.id FROM episodes e JOIN titles t ON t.id = e.title_id JOIN sources s ON s.id = t.source_id
		WHERE e.id = ? AND e.missing_since IS NULL AND `+libraryShown, episodeID).Scan(&titleID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("store: series of episode %d: %w", episodeID, err)
	}
	return titleID, nil
}
