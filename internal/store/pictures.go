// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// Pictures from TMDB for the films and series of Xtream servers and
// Stalker portals (D143, schema 51). Where the administrator turns them
// on, and has given TMDB's key (D88), a title whose own poster wants
// replacing gets TMDB's: one whose source gives none, one that does not
// load, and one that many of its source's titles share, which is a
// placeholder. Its wide picture is TMDB's too where it has none of its
// own. What TMDB gives is kept apart from what the source gives
// (titles.poster_fill, titles.backdrop_fill), so that a read of the
// source never loses it, and a source's picture that comes right takes
// its place again.

// settingTMDBPictures is "1" if providers' titles take TMDB's pictures
// where theirs want replacing; off until it is set.
const settingTMDBPictures = "tmdb_pictures"

// SharedPicture is how many of a source's films, or of its series, have
// the same poster for it to count as a placeholder.
const SharedPicture = 10

// titlePosterSQL is a title t's poster as devices see it: TMDB's, where
// it replaces the source's, else the source's.
const titlePosterSQL = `CASE WHEN t.poster_fill <> '' THEN t.poster_fill ELSE t.poster END`

// withFills gives a title's details and summary TMDB's pictures where
// they replace the source's: its poster comes before what the details
// say too, and its wide picture where the title has none of its own.
func withFills(d *TitleDetails, sum *TitleSummary, posterFill, backdropFill string) {
	if posterFill != "" && d != nil {
		d.Poster = posterFill
	}
	if backdropFill != "" && sum.Backdrop == "" && (d == nil || d.Backdrop == "") {
		sum.Backdrop = backdropFill
	}
}

// TMDBPictures reports whether providers' titles take TMDB's pictures
// where theirs want replacing (D143). It is off until set.
func (s *Store) TMDBPictures(ctx context.Context) (bool, error) {
	v, _, err := s.setting(ctx, settingTMDBPictures)
	return v == "1", err
}

// SetTMDBPictures turns TMDB's pictures for providers' titles on or off.
// Off, every title shows its source's pictures again.
func (s *Store) SetTMDBPictures(ctx context.Context, on bool) error {
	if err := s.setSetting(ctx, settingTMDBPictures, onOff(on)); err != nil {
		return fmt.Errorf("store: TMDB's pictures: %w", err)
	}
	if !on {
		return s.clearPictureFills(ctx, s.db)
	}
	return nil
}

// clearPictureFills gives every title its source's pictures again.
func (s *Store) clearPictureFills(ctx context.Context, db execer) error {
	if _, err := db.ExecContext(ctx, `
		UPDATE titles SET poster_fill = '', backdrop_fill = '' WHERE poster_fill <> '' OR backdrop_fill <> ''`); err != nil {
		return fmt.Errorf("store: TMDB's pictures: %w", err)
	}
	return nil
}

// wantedPosterSQL is true of a title t, of the source and kind given as
// its two arguments, whose poster wants replacing: it has none, or one
// that did not load, or one that at least SharedPicture of the source's
// titles of its kind share. The shared posters are found once, not for
// each title.
var wantedPosterSQL = `(t.poster = '' OR t.poster IN (SELECT sp.poster FROM titles sp
			WHERE sp.source_id = ? AND sp.kind = ? AND sp.poster <> '' AND sp.missing_since IS NULL
			GROUP BY sp.poster HAVING count(*) >= ` + strconv.Itoa(SharedPicture) + `)
		OR EXISTS (SELECT 1 FROM picture_checks pc WHERE pc.source_id = t.source_id AND pc.address = t.poster))`

// SettlePictures makes the pictures from TMDB of a source's films or
// series follow what their own pictures are now: a title whose own poster
// no longer wants replacing shows it again, and one whose poster wants
// replacing, and that TMDB found before, takes TMDB's. It returns how many
// titles showed their own pictures again, and how many took TMDB's.
func (s *Store) SettlePictures(ctx context.Context, sourceID int64, kind string) (restored, filled int, err error) {
	fail := func(err error) (int, int, error) {
		return 0, 0, fmt.Errorf("store: pictures of source %d: %w", sourceID, err)
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE titles AS t SET poster_fill = '', backdrop_fill = ''
		WHERE t.source_id = ? AND t.kind = ? AND (t.poster_fill <> '' OR t.backdrop_fill <> '') AND NOT `+wantedPosterSQL,
		sourceID, kind, sourceID, kind)
	if err == nil {
		restored, err = rowsAffected(res)
	}
	if err != nil {
		return fail(err)
	}
	res, err = s.db.ExecContext(ctx, `
		UPDATE titles AS t SET poster_fill = json_extract(tt.details, '$.details.poster'),
			backdrop_fill = COALESCE(json_extract(tt.details, '$.details.backdrop'), '')
		FROM tmdb_titles tt
		WHERE tt.source_id = t.source_id AND tt.kind = t.kind AND tt.key = t.source_key AND tt.tmdb_id <> 0
			AND COALESCE(json_extract(tt.details, '$.details.poster'), '') <> ''
			AND t.source_id = ? AND t.kind = ? AND t.missing_since IS NULL AND t.poster_fill = '' AND `+wantedPosterSQL,
		sourceID, kind, sourceID, kind)
	if err == nil {
		filled, err = rowsAffected(res)
	}
	if err != nil {
		return fail(err)
	}
	return restored, filled, nil
}

// PictureWant is a title whose poster wants replacing, to look up on TMDB.
type PictureWant struct {
	TitleID int64
	Key     string // the source's ID for it
	Name    string
	Year    int   // 0 if not known
	TMDBID  int64 // the TMDB ID that its source gives, 0 if none
}

// PictureWants returns up to limit of a source's films or series whose
// posters want replacing and that TMDB is due to be asked about: never
// asked, or asked before missAfter and not found, in the source's order.
// Those that TMDB found take its pictures with SettlePictures.
func (s *Store) PictureWants(ctx context.Context, sourceID int64, kind string, missAfter time.Time, limit int) ([]PictureWant, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT t.id, t.source_key, t.name, t.year,
			CASE WHEN t.details IS NOT NULL THEN json_extract(t.details, '$.tmdb_id') END
		FROM titles t LEFT JOIN tmdb_titles tt ON tt.source_id = t.source_id AND tt.kind = t.kind AND tt.key = t.source_key
		WHERE t.source_id = ? AND t.kind = ? AND t.missing_since IS NULL AND t.poster_fill = '' AND `+wantedPosterSQL+`
			AND (tt.key IS NULL OR tt.tmdb_id = 0 AND tt.checked_at < ?)
		ORDER BY t.position LIMIT ?`, sourceID, kind, sourceID, kind, missAfter.Unix(), limit)
	if err != nil {
		return nil, fmt.Errorf("store: pictures of source %d: %w", sourceID, err)
	}
	defer rows.Close()
	var out []PictureWant
	for rows.Next() {
		var w PictureWant
		var id sql.NullInt64
		if err := rows.Scan(&w.TitleID, &w.Key, &w.Name, &w.Year, &id); err != nil {
			return nil, fmt.Errorf("store: pictures of source %d: %w", sourceID, err)
		}
		w.TMDBID = max(0, id.Int64)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: pictures of source %d: %w", sourceID, err)
	}
	return out, nil
}

// SetPictureFill gives a title TMDB's poster, and its wide picture, in
// place of its source's.
func (s *Store) SetPictureFill(ctx context.Context, titleID int64, poster, backdrop string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE titles SET poster_fill = ?, backdrop_fill = ? WHERE id = ?`,
		poster, backdrop, titleID); err != nil {
		return fmt.Errorf("store: pictures of title %d: %w", titleID, err)
	}
	return nil
}

// PictureFailed records that a source's picture of a title did not load:
// its server said that it is not there, or it is no image (D143).
func (s *Store) PictureFailed(ctx context.Context, sourceID int64, address string, at time.Time) error {
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO picture_checks (source_id, address, checked_at) VALUES (?, ?, ?)
		ON CONFLICT (source_id, address) DO UPDATE SET checked_at = excluded.checked_at`,
		sourceID, address, formatTime(at)); err != nil {
		return fmt.Errorf("store: a picture's check: %w", err)
	}
	return nil
}

// PictureLoads records that a source's picture of a title loads, and
// gives the titles that show TMDB's in its place their own again, unless
// they share it with many others.
func (s *Store) PictureLoads(ctx context.Context, sourceID int64, address string) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM picture_checks WHERE source_id = ? AND address = ?`, sourceID, address)
		if err != nil {
			return fmt.Errorf("store: a picture's check: %w", err)
		}
		if n, err := rowsAffected(res); err != nil || n == 0 {
			return err
		}
		for _, kind := range []string{KindMovie, KindSeries} {
			if _, err := tx.ExecContext(ctx, `
				UPDATE titles AS t SET poster_fill = '', backdrop_fill = ''
				WHERE t.source_id = ? AND t.kind = ? AND t.poster = ? AND t.poster_fill <> '' AND NOT `+wantedPosterSQL,
				sourceID, kind, address, sourceID, kind); err != nil {
				return fmt.Errorf("store: a picture's check: %w", err)
			}
		}
		return nil
	})
}

// PictureFetch is a source's picture of a title to fetch again, with what
// fetching it needs.
type PictureFetch struct {
	SourceID   int64
	Address    string
	UserAgent  string
	PublicOnly bool
}

// PicturesToCheck returns up to limit of the sources' pictures recorded as
// not loading before a time, of enabled sources, to fetch again.
func (s *Store) PicturesToCheck(ctx context.Context, before time.Time, limit int) ([]PictureFetch, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT pc.source_id, pc.address, src.user_agent, src.public_only
		FROM picture_checks pc JOIN sources src ON src.id = pc.source_id
		WHERE pc.checked_at < ? AND src.enabled = 1 ORDER BY pc.checked_at LIMIT ?`, formatTime(before), limit)
	if err != nil {
		return nil, fmt.Errorf("store: pictures to check: %w", err)
	}
	defer rows.Close()
	var out []PictureFetch
	for rows.Next() {
		var f PictureFetch
		if err := rows.Scan(&f.SourceID, &f.Address, &f.UserAgent, &f.PublicOnly); err != nil {
			return nil, fmt.Errorf("store: pictures to check: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ForgetPictureChecks forgets the pictures recorded as not loading that no
// title shows any more.
func (s *Store) ForgetPictureChecks(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM picture_checks AS pc WHERE NOT EXISTS (
			SELECT 1 FROM titles t WHERE t.source_id = pc.source_id AND t.kind IN ('`+KindMovie+`', '`+KindSeries+`')
				AND t.poster = pc.address)`); err != nil {
		return fmt.Errorf("store: pictures' checks: %w", err)
	}
	return nil
}

// PictureCounts counts the titles that show TMDB's pictures, and the
// sources' pictures recorded as not loading.
func (s *Store) PictureCounts(ctx context.Context) (filled, failing int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM titles WHERE poster_fill <> ''), (SELECT count(*) FROM picture_checks)`).Scan(&filled, &failing)
	if err != nil {
		return 0, 0, fmt.Errorf("store: count TMDB's pictures: %w", err)
	}
	return filled, failing, nil
}
