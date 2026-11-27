// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"overscan.gureet.ca/internal/source"
)

// What is transcoded of films and series (D150, schema 54): films and
// series as a whole, a shelf and a title each say whether their video is
// transcoded, as live TV, a group and a channel do (D147). A series' episodes
// follow it.

// settingVODTranscode is films' and series' own setting of what is
// transcoded: unset for as needed, "never", or a profile's name.
const settingVODTranscode = "vod_transcode"

// Where a film's or an episode's setting of what is transcoded comes from.
const (
	TranscodeByTitle = "title"
	TranscodeByShelf = "shelf"
	TranscodeByVOD   = "films and series"
)

// vodTranscodeSQL is films' and series' own setting as SQL: NULL for as
// needed.
const vodTranscodeSQL = `(SELECT NULLIF(value, '') FROM settings WHERE key = '` + settingVODTranscode + `')`

// shelfTranscodeSQL is, for a title t, its shelf's setting as SQL: that of
// the shelf that merges its category's name, or of its portal's category
// (D77); NULL for as needed, or for a title on no shelf.
const shelfTranscodeSQL = `(SELECT tss.transcode FROM library_categories tsc JOIN sources tscs ON tscs.id = tsc.source_id
	JOIN shelf_settings tss ON tss.kind = tsc.kind AND tss.shelf =
		CASE WHEN tscs.type = '` + source.TypeStalker + `' THEN 'category:' || tsc.id ELSE 'name:' || tsc.name END
	WHERE tsc.id = t.category_id)`

// resolveTitleTranscode returns the setting that wins for a title on a
// shelf, and where it comes from: the title's own, else its shelf's, else
// films' and series'; as needed, from nowhere, if none says anything.
func resolveTitleTranscode(title, shelf, all sql.NullString) (Transcode, string) {
	for _, l := range []struct {
		v  sql.NullString
		by string
	}{{title, TranscodeByTitle}, {shelf, TranscodeByShelf}, {all, TranscodeByVOD}} {
		if t := transcodeOf(l.v); !t.AsNeeded() {
			return t, l.by
		}
	}
	return Transcode{}, ""
}

// TitleTranscode returns what is transcoded of a film, or of a series'
// episodes, and where it comes from (D150).
func (s *Store) TitleTranscode(ctx context.Context, titleID int64) (Transcode, string, error) {
	var own, shelf, all sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT t.transcode, `+shelfTranscodeSQL+`, `+vodTranscodeSQL+`
		FROM titles t WHERE t.id = ?`, titleID).Scan(&own, &shelf, &all)
	if errors.Is(err, sql.ErrNoRows) {
		return Transcode{}, "", ErrNotFound
	}
	if err != nil {
		return Transcode{}, "", fmt.Errorf("store: what is transcoded of title %d: %w", titleID, err)
	}
	t, by := resolveTitleTranscode(own, shelf, all)
	return t, by, nil
}

// VODTranscode returns films' and series' own setting of what is
// transcoded.
func (s *Store) VODTranscode(ctx context.Context) (Transcode, error) {
	v, set, err := s.setting(ctx, settingVODTranscode)
	if err != nil {
		return Transcode{}, fmt.Errorf("store: films' and series' transcoding: %w", err)
	}
	return transcodeOf(sql.NullString{String: v, Valid: set}), nil
}

// SetVODTranscode sets films' and series' own setting of what is
// transcoded.
func (s *Store) SetVODTranscode(ctx context.Context, t Transcode) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkTranscode(ctx, tx, t); err != nil {
			return err
		}
		v, _ := t.value().(string)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingVODTranscode, v)
		return err
	})
}

// SetTitlesTranscode sets what is transcoded of films or series, and
// returns how many of them there were.
func (s *Store) SetTitlesTranscode(ctx context.Context, ids []int64, t Transcode) (int, error) {
	n := 0
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkTranscode(ctx, tx, t); err != nil {
			return err
		}
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, "UPDATE titles SET transcode = ? WHERE id = ?", t.value(), id)
			if err != nil {
				return fmt.Errorf("store: what is transcoded of a title: %w", err)
			}
			k, _ := res.RowsAffected()
			n += int(k)
		}
		return nil
	})
	return n, err
}

// SetShelfTranscode sets what is transcoded of the titles on a shelf of
// films or of series. key is the shelf's LibraryShelf.Key.
func (s *Store) SetShelfTranscode(ctx context.Context, kind, key string, t Transcode) error {
	if err := checkTranscode(ctx, s.db, t); err != nil {
		return err
	}
	return s.changeShelf(ctx, "what is transcoded of a shelf", kind, key, "transcode = ?", t.value())
}
