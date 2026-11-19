// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"overscan.gureet.ca/internal/media"
)

// What the files of films and episodes hold (D68), as ffprobe read them.

// MediaKey returns the keys under which the store keeps what p's file
// holds: the film's title ID and 0, or the episode's series and the
// episode.
func (p PlayableItem) MediaKey() (titleID, episodeID int64) {
	if p.Kind == KindEpisode {
		return p.TitleID, p.ID
	}
	return p.ID, 0
}

// Media returns what the file of a film or an episode holds, as last read,
// and whether it has been read.
func (s *Store) Media(ctx context.Context, titleID, episodeID int64) (media.Info, bool, error) {
	var text string
	err := s.db.QueryRowContext(ctx, `SELECT info FROM media WHERE title_id = ? AND episode_id = ?`,
		titleID, episodeID).Scan(&text)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Info{}, false, nil
	}
	if err != nil {
		return media.Info{}, false, fmt.Errorf("store: media of %d/%d: %w", titleID, episodeID, err)
	}
	var info media.Info
	if err := json.Unmarshal([]byte(text), &info); err != nil || info.Format == "" {
		// Kept by a build that wrote it otherwise: read it again.
		return media.Info{}, false, nil
	}
	return info, true, nil
}

// SetMedia keeps what the file of a film or an episode holds, as read at
// the time at. A title or an episode that no longer exists keeps nothing.
func (s *Store) SetMedia(ctx context.Context, titleID, episodeID int64, info media.Info, at time.Time) error {
	b, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("store: media of %d/%d: %w", titleID, episodeID, err)
	}
	_, err = s.db.ExecContext(ctx, `
		INSERT INTO media (title_id, episode_id, info, read_at)
		SELECT ?, ?, ?, ?
		WHERE EXISTS (SELECT 1 FROM titles WHERE id = ?)
			AND (? = 0 OR EXISTS (SELECT 1 FROM episodes WHERE id = ? AND title_id = ?))
		ON CONFLICT (title_id, episode_id) DO UPDATE SET info = excluded.info, read_at = excluded.read_at`,
		titleID, episodeID, string(b), at.Unix(),
		titleID, episodeID, episodeID, titleID)
	if err != nil {
		return fmt.Errorf("store: media of %d/%d: %w", titleID, episodeID, err)
	}
	return nil
}

// ForgetMedia drops what is kept of the file of a film or an episode, which
// has changed since it was read.
func (s *Store) ForgetMedia(ctx context.Context, titleID, episodeID int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM media WHERE title_id = ? AND episode_id = ?`,
		titleID, episodeID); err != nil {
		return fmt.Errorf("store: media of %d/%d: %w", titleID, episodeID, err)
	}
	return nil
}
