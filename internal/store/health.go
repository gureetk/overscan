// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"overscan.gureet.ca/internal/streaminfo"
)

// Channel health, from what happens as people watch (D134, D138, schema
// 47). The hub notes when a channel's stream fails to start and when one
// starts, and what a stream holds as it passes; Overscan never opens a
// channel to find out. A channel that failed can be hidden from devices
// until it plays again: as another channel's backup or merged copy (D109,
// D113), which devices still reach, or when the administrator shows it.

// ChannelHealth is what Overscan has seen of a channel's stream.
type ChannelHealth struct {
	FailedAt time.Time // when its stream last failed to start; zero if never
	Failures int       // times it failed in a row since it last played
	PlayedAt time.Time // when its stream last started; zero if never
	Held     bool      // hidden from devices until it plays again
	Details  streaminfo.Details
	SeenAt   time.Time // when its details were last read; zero if never
}

// Failing reports whether its stream failed the last time that it was
// played.
func (h ChannelHealth) Failing() bool { return h.Failures > 0 }

// heldChannel is true of an entry e whose channel c is hidden until it
// plays again.
const heldChannel = `EXISTS (SELECT 1 FROM channel_health hh WHERE hh.channel_id = c.id AND hh.held = 1)`

// NoteStreamFailed notes that a channel's stream failed to start at at.
func (s *Store) NoteStreamFailed(ctx context.Context, channelID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_health (channel_id, failed_at, failures) SELECT ?1, ?2, 1 WHERE EXISTS (SELECT 1 FROM channels WHERE id = ?1)
		ON CONFLICT (channel_id) DO UPDATE SET failed_at = excluded.failed_at, failures = failures + 1`,
		channelID, formatTime(at))
	if err != nil {
		return fmt.Errorf("store: note channel %d failed: %w", channelID, err)
	}
	return nil
}

// NoteStreamPlayed notes that a channel's stream started at at: it no
// longer counts as failing, and shows again if it was hidden until it
// played. It reports whether it was.
func (s *Store) NoteStreamPlayed(ctx context.Context, channelID int64, at time.Time) (wasHeld bool, err error) {
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT held FROM channel_health WHERE channel_id = ?", channelID).Scan(&wasHeld)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO channel_health (channel_id, played_at) SELECT ?1, ?2 WHERE EXISTS (SELECT 1 FROM channels WHERE id = ?1)
			ON CONFLICT (channel_id) DO UPDATE SET played_at = excluded.played_at, failures = 0, held = 0`,
			channelID, formatTime(at)); err != nil || !wasHeld {
			return err
		}
		return applyEveryLineup(ctx, tx)
	})
	if err != nil {
		return false, fmt.Errorf("store: note channel %d played: %w", channelID, err)
	}
	return wasHeld, nil
}

// keepPicture is true, in NoteStreamDetails's update, when the new read
// found the same video codec as before but not its picture.
const keepPicture = `(excluded.height = 0 AND excluded.video <> '' AND excluded.video = video)`

// NoteStreamDetails notes what a channel's stream held, as the hub read it
// at at. A read that found the video's codec but not its picture keeps
// the picture read before with the same codec, so that a stream whose
// parameters came late does not lose what was known of it.
func (s *Store) NoteStreamDetails(ctx context.Context, channelID int64, d streaminfo.Details, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO channel_health (channel_id, video, width, height, interlaced, bit_depth, frame_rate, dynamic_range, audio, seen_at)
		SELECT ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10 WHERE EXISTS (SELECT 1 FROM channels WHERE id = ?1)
		ON CONFLICT (channel_id) DO UPDATE SET video = excluded.video, audio = excluded.audio, seen_at = excluded.seen_at,
			width = iif(`+keepPicture+`, width, excluded.width), height = iif(`+keepPicture+`, height, excluded.height),
			interlaced = iif(`+keepPicture+`, interlaced, excluded.interlaced),
			bit_depth = iif(`+keepPicture+`, bit_depth, excluded.bit_depth),
			frame_rate = iif(`+keepPicture+`, frame_rate, excluded.frame_rate),
			dynamic_range = iif(`+keepPicture+`, dynamic_range, excluded.dynamic_range)`,
		channelID, d.Video, d.Width, d.Height, d.Interlaced, d.BitDepth, d.FrameRate, d.DynamicRange,
		strings.Join(d.Audio, ","), formatTime(at))
	if err != nil {
		return fmt.Errorf("store: note channel %d's details: %w", channelID, err)
	}
	return nil
}

// healthColumns are a channel_health row's columns, as scanHealth reads
// them, for a row aliased hh, which may be missing.
const healthColumns = `hh.failed_at, COALESCE(hh.failures, 0), hh.played_at, COALESCE(hh.held, 0), COALESCE(hh.video, ''),
	COALESCE(hh.width, 0), COALESCE(hh.height, 0), COALESCE(hh.interlaced, 0), COALESCE(hh.bit_depth, 0),
	COALESCE(hh.frame_rate, 0), COALESCE(hh.dynamic_range, ''), COALESCE(hh.audio, ''), hh.seen_at`

// healthScan holds what healthColumns read, for scanHealth to finish.
type healthScan struct {
	failed, played, seen sql.NullString
	audio                string
	h                    ChannelHealth
}

func (hs *healthScan) dest() []any {
	d := &hs.h.Details
	return []any{&hs.failed, &hs.h.Failures, &hs.played, &hs.h.Held, &d.Video, &d.Width, &d.Height, &d.Interlaced,
		&d.BitDepth, &d.FrameRate, &d.DynamicRange, &hs.audio, &hs.seen}
}

func (hs *healthScan) health() ChannelHealth {
	h := hs.h
	h.FailedAt, _ = parseNullTime(hs.failed)
	h.PlayedAt, _ = parseNullTime(hs.played)
	h.SeenAt, _ = parseNullTime(hs.seen)
	if hs.audio != "" {
		h.Details.Audio = strings.Split(hs.audio, ",")
	}
	return h
}

// parseNullTime parses a time that may be NULL, as the zero time.
func parseNullTime(v sql.NullString) (time.Time, error) {
	if !v.Valid {
		return time.Time{}, nil
	}
	return parseTime(v.String)
}

// HealthOf returns what Overscan has seen of a channel's stream.
func (s *Store) HealthOf(ctx context.Context, channelID int64) (ChannelHealth, error) {
	var hs healthScan
	err := s.db.QueryRowContext(ctx, "SELECT "+healthColumns+" FROM channel_health hh WHERE hh.channel_id = ?", channelID).
		Scan(hs.dest()...)
	if err == sql.ErrNoRows {
		return ChannelHealth{}, nil
	}
	if err != nil {
		return ChannelHealth{}, fmt.Errorf("store: health of channel %d: %w", channelID, err)
	}
	return hs.health(), nil
}

// ChannelNames returns a channel's name, as its source gives it, and its
// source's; ErrNotFound if there is no such channel.
func (s *Store) ChannelNames(ctx context.Context, channelID int64) (channel, source string, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT c.name, s.name FROM channels c JOIN sources s ON s.id = c.source_id WHERE c.id = ?",
		channelID).Scan(&channel, &source)
	switch {
	case err == sql.ErrNoRows:
		return "", "", ErrNotFound
	case err != nil:
		return "", "", fmt.Errorf("store: channel %d's name: %w", channelID, err)
	}
	return channel, source, nil
}

// FailedChannel is a channel whose stream failed the last time it was
// played, or that is hidden until it plays again.
type FailedChannel struct {
	ChannelID int64
	Name      string // as its source names it
	SourceID  int64
	Source    string
	ChannelHealth
}

// MaxFailedChannels caps how many FailedChannels returns.
const MaxFailedChannels = 500

// FailedChannels returns the channels whose streams failed the last time
// that they were played, or that are hidden until they play again, those
// that failed last first: at most MaxFailedChannels, and how many there
// are. Channels that their sources no longer list, and those of sources
// turned off, are left out.
func (s *Store) FailedChannels(ctx context.Context) ([]FailedChannel, int, error) {
	fail := func(err error) ([]FailedChannel, int, error) {
		return nil, 0, fmt.Errorf("store: failed channels: %w", err)
	}
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*)"+failedFrom).Scan(&total); err != nil {
		return fail(err)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT c.id, c.name, s.id, s.name, "+healthColumns+failedFrom+`
		ORDER BY hh.failed_at IS NULL, hh.failed_at DESC, c.id LIMIT ?`, MaxFailedChannels)
	if err != nil {
		return fail(err)
	}
	defer rows.Close()
	var out []FailedChannel
	for rows.Next() {
		var f FailedChannel
		var hs healthScan
		if err := rows.Scan(append([]any{&f.ChannelID, &f.Name, &f.SourceID, &f.Source}, hs.dest()...)...); err != nil {
			return fail(err)
		}
		f.ChannelHealth = hs.health()
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return fail(err)
	}
	return out, total, nil
}

// failedFrom picks the channel_health rows hh of FailedChannels, with their
// channels c and sources s.
const failedFrom = `
	FROM channel_health hh JOIN channels c ON c.id = hh.channel_id JOIN sources s ON s.id = c.source_id
	WHERE (hh.failures > 0 OR hh.held = 1) AND c.missing_since IS NULL AND s.enabled = 1`

// HealthCounts counts the channels that FailedChannels lists: those that
// failed and are shown, and those hidden until they play again.
func (s *Store) HealthCounts(ctx context.Context) (failing, held int, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT COALESCE(sum(hh.held = 0), 0), COALESCE(sum(hh.held), 0)"+failedFrom).Scan(&failing, &held)
	if err != nil {
		return 0, 0, fmt.Errorf("store: count failed channels: %w", err)
	}
	return failing, held, nil
}

// SetFailingHeld hides every channel that FailedChannels lists until it
// plays again, or shows every hidden one again. It returns how many it
// changed.
func (s *Store) SetFailingHeld(ctx context.Context, held bool) (int, error) {
	var n int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE channel_health SET held = ?1 WHERE held <> ?1 AND (failures > 0 OR ?1 = 0)
			AND channel_id IN (SELECT c.id FROM channels c JOIN sources s ON s.id = c.source_id
				WHERE c.missing_since IS NULL AND s.enabled = 1)`, held)
		if err != nil {
			return err
		}
		if n, err = res.RowsAffected(); err != nil || n == 0 {
			return err
		}
		return applyEveryLineup(ctx, tx)
	})
	if err != nil {
		return 0, fmt.Errorf("store: hide failed channels until they play: %w", err)
	}
	return int(n), nil
}

// SetChannelsHeld hides channels from devices until they play again, or
// shows them again. It returns ErrNotFound if a channel does not exist.
func (s *Store) SetChannelsHeld(ctx context.Context, channelIDs []int64, held bool) error {
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		for _, id := range channelIDs {
			var exists bool
			if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM channels WHERE id = ?)", id).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return ErrNotFound
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO channel_health (channel_id, held) VALUES (?, ?)
				ON CONFLICT (channel_id) DO UPDATE SET held = excluded.held`, id, held); err != nil {
				return err
			}
		}
		return applyEveryLineup(ctx, tx)
	})
	if err != nil && err != ErrNotFound {
		return fmt.Errorf("store: hide channels until they play: %w", err)
	}
	return err
}

// applyEveryLineup runs every lineup's rules again, after a channel is
// hidden until it plays or shown again: merges show a copy that devices
// see (D113), and groups with first numbers number only what they show
// (D137).
func applyEveryLineup(ctx context.Context, tx *sql.Tx) error {
	ids, err := lineupIDs(ctx, tx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := applyRules(ctx, tx, id); err != nil {
			return err
		}
	}
	return nil
}
