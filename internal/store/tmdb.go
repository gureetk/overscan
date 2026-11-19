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

// TMDB's key, and what TMDB says of the films and series in folders (D88,
// schema 28).

// sealedTMDBKey is the sealed setting that holds TMDB's key.
const sealedTMDBKey = "tmdb_key"

// settingTMDBLanguage is the language that TMDB is asked in, such as
// en-GB; TMDB's own default until it is set.
const settingTMDBLanguage = "tmdb_language"

// tmdbImages is where TMDB's pictures are.
const tmdbImages = "https://image.tmdb.org/"

// sealedSettingContext is the context that a sealed setting is sealed in.
func sealedSettingContext(key string) string { return "setting/" + key }

// TMDBKey returns the key for TMDB that the administrator gave, or "" for
// none.
func (s *Store) TMDBKey(ctx context.Context) (string, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, `SELECT secret FROM sealed_settings WHERE key = ?`, sealedTMDBKey).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: TMDB's key: %w", err)
	}
	return s.open(sealedSettingContext(sealedTMDBKey), sealed)
}

// SetTMDBKey keeps the key for TMDB, sealed. An empty key removes it, and
// with it what TMDB said of titles and the posters on TMDB that folders'
// titles had from it; the next read of each folder drops the rest.
func (s *Store) SetTMDBKey(ctx context.Context, key string) error {
	if key == "" {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("store: remove TMDB's key: %w", err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, `DELETE FROM sealed_settings WHERE key = ?`, sealedTMDBKey); err != nil {
			return fmt.Errorf("store: remove TMDB's key: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM tmdb_titles`); err != nil {
			return fmt.Errorf("store: remove TMDB's key: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE titles SET poster = '' WHERE poster LIKE ? || '%'
				AND source_id IN (SELECT id FROM sources WHERE type = ?)`, tmdbImages, source.TypeFolder); err != nil {
			return fmt.Errorf("store: remove TMDB's key: %w", err)
		}
		return tx.Commit()
	}
	sealed, err := s.seal(sealedSettingContext(sealedTMDBKey), key)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO sealed_settings (key, secret) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET secret = excluded.secret`, sealedTMDBKey, sealed); err != nil {
		return fmt.Errorf("store: keep TMDB's key: %w", err)
	}
	return nil
}

// TMDBLanguage returns the language that TMDB is asked in, or "" for its
// own default.
func (s *Store) TMDBLanguage(ctx context.Context) (string, error) {
	v, _, err := s.setting(ctx, settingTMDBLanguage)
	return v, err
}

// SetTMDBLanguage sets the language that TMDB is asked in; "" for its own.
// A new language makes every title due to be asked about again, keeping
// what TMDB said until it answers.
func (s *Store) SetTMDBLanguage(ctx context.Context, language string) error {
	old, err := s.TMDBLanguage(ctx)
	if err != nil {
		return err
	}
	if err := s.setSetting(ctx, settingTMDBLanguage, language); err != nil {
		return err
	}
	if old != language {
		if _, err := s.db.ExecContext(ctx, `UPDATE tmdb_titles SET checked_at = 0`); err != nil {
			return fmt.Errorf("store: TMDB's language: %w", err)
		}
	}
	return nil
}

// TMDBEpisode is what TMDB says of an episode of a series.
type TMDBEpisode struct {
	Season  int    `json:"season"`
	Number  int    `json:"number"`
	Name    string `json:"name,omitempty"`
	Plot    string `json:"plot,omitempty"`
	Picture string `json:"picture,omitempty"` // the address of a still
	Runtime int    `json:"runtime,omitempty"` // minutes
}

// TMDBTitle is what TMDB says of a film or a series in a folder.
type TMDBTitle struct {
	ID int64 // TMDB's ID for it; 0 when TMDB had no match
	// Details are its details, with the addresses of its pictures on TMDB,
	// and a series' seasons.
	Details TitleDetails
	// Episodes are a series' episodes, read once a device opened it.
	Episodes  []TMDBEpisode
	CheckedAt time.Time
}

type tmdbStored struct {
	Details  TitleDetails  `json:"details"`
	Episodes []TMDBEpisode `json:"episodes,omitempty"`
}

// TMDBTitles returns what TMDB says of a source's films or series, by their
// keys.
func (s *Store) TMDBTitles(ctx context.Context, sourceID int64, kind string) (map[string]TMDBTitle, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT key, tmdb_id, details, checked_at FROM tmdb_titles WHERE source_id = ? AND kind = ?`, sourceID, kind)
	if err != nil {
		return nil, fmt.Errorf("store: TMDB's titles: %w", err)
	}
	defer rows.Close()
	out := map[string]TMDBTitle{}
	for rows.Next() {
		var key string
		t, err := scanTMDB(rows, &key)
		if err != nil {
			return nil, fmt.Errorf("store: TMDB's titles: %w", err)
		}
		out[key] = t
	}
	return out, rows.Err()
}

// TMDBTitleOf returns what TMDB says of one title, and whether it has been
// asked.
func (s *Store) TMDBTitleOf(ctx context.Context, sourceID int64, kind, key string) (TMDBTitle, bool, error) {
	var k string
	t, err := scanTMDB(s.db.QueryRowContext(ctx, `
		SELECT key, tmdb_id, details, checked_at FROM tmdb_titles WHERE source_id = ? AND kind = ? AND key = ?`,
		sourceID, kind, key), &k)
	if errors.Is(err, sql.ErrNoRows) {
		return TMDBTitle{}, false, nil
	}
	if err != nil {
		return TMDBTitle{}, false, fmt.Errorf("store: TMDB's title: %w", err)
	}
	return t, true, nil
}

func scanTMDB(row rowScanner, key *string) (TMDBTitle, error) {
	var t TMDBTitle
	var details string
	var checked int64
	if err := row.Scan(key, &t.ID, &details, &checked); err != nil {
		return t, err
	}
	var stored tmdbStored
	if err := json.Unmarshal([]byte(details), &stored); err == nil {
		t.Details, t.Episodes = stored.Details, stored.Episodes
	}
	t.CheckedAt = time.Unix(checked, 0)
	return t, nil
}

// SetTMDBTitle keeps what TMDB says of a source's film or series.
func (s *Store) SetTMDBTitle(ctx context.Context, sourceID int64, kind, key string, t TMDBTitle) error {
	data, err := json.Marshal(tmdbStored{Details: t.Details, Episodes: t.Episodes})
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO tmdb_titles (source_id, kind, key, tmdb_id, details, checked_at) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (source_id, kind, key) DO UPDATE SET
			tmdb_id = excluded.tmdb_id, details = excluded.details, checked_at = excluded.checked_at`,
		sourceID, kind, key, t.ID, string(data), t.CheckedAt.Unix()); err != nil {
		return fmt.Errorf("store: keep TMDB's title: %w", err)
	}
	return nil
}

// TMDBCounts counts the titles that TMDB found, and those it had no match
// for.
func (s *Store) TMDBCounts(ctx context.Context) (found, missed int, err error) {
	err = s.db.QueryRowContext(ctx, `
		SELECT COALESCE(sum(tmdb_id <> 0), 0), COALESCE(sum(tmdb_id = 0), 0) FROM tmdb_titles`).Scan(&found, &missed)
	if err != nil {
		return 0, 0, fmt.Errorf("store: count TMDB's titles: %w", err)
	}
	return found, missed, nil
}
