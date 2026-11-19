// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

// Keys of the settings for the whole server (schema 16).
const (
	// settingRepairDefault is "1" if channels are repaired unless they say
	// otherwise (D58).
	settingRepairDefault = "repair_default"

	// settingRepairHLS is "1" if a repaired channel whose address is HLS
	// comes to devices as one continuous stream, so that it can be repaired
	// (D61).
	settingRepairHLS = "repair_hls"

	// settingTranscodeVideo is how many films and episodes at once this
	// server may transcode whose video a box cannot play, from a source
	// without a transcoder of its own: "0", "1", "2" or "3" (D73).
	settingTranscodeVideo = "transcode_video"

	// settingConvertSound is "0" if films and episodes whose sound or file
	// a box cannot play go to it as they are, rather than through ffmpeg,
	// which converts them (D68); on until it is set (D78).
	settingConvertSound = "convert_sound"

	// settingServerTranscodes is "0" if films and episodes whose video a
	// box cannot play go to it as they are, rather than through their
	// source's server, which transcodes them (D71); on until it is set
	// (D78).
	settingServerTranscodes = "server_transcodes"
)

// MaxTranscodes is the most films and episodes whose video the server may
// be set to transcode at once (D73).
const MaxTranscodes = 3

// repairDefaultSQL is whether channels are repaired by default, as SQL.
const repairDefaultSQL = `COALESCE((SELECT value = '1' FROM settings WHERE key = '` + settingRepairDefault + `'), 0)`

// repairHLSSQL is whether HLS streams are repaired, as SQL.
const repairHLSSQL = `COALESCE((SELECT value = '1' FROM settings WHERE key = '` + settingRepairHLS + `'), 0)`

// setting returns the value of a setting, and whether it is set.
func (s *Store) setting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

// setSetting sets a setting's value.
func (s *Store) setSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// RepairDefault reports whether channels are repaired unless their own
// switch says otherwise (D58). It is off until set.
func (s *Store) RepairDefault(ctx context.Context) (bool, error) {
	v, _, err := s.setting(ctx, settingRepairDefault)
	return v == "1", err
}

// SetRepairDefault sets whether channels are repaired unless their own
// switch says otherwise.
func (s *Store) SetRepairDefault(ctx context.Context, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.setSetting(ctx, settingRepairDefault, v)
}

// RepairHLS reports whether a repaired channel whose address is HLS comes
// to devices as one continuous stream, so that it can be repaired (D61). It
// is off until set.
func (s *Store) RepairHLS(ctx context.Context) (bool, error) {
	v, _, err := s.setting(ctx, settingRepairHLS)
	return v == "1", err
}

// SetRepairHLS sets whether a repaired channel whose address is HLS comes
// to devices as one continuous stream.
func (s *Store) SetRepairHLS(ctx context.Context, on bool) error {
	v := "0"
	if on {
		v = "1"
	}
	return s.setSetting(ctx, settingRepairHLS, v)
}

// Transcodes returns how many films and episodes at once this server may
// transcode whose video a box cannot play (D73): 0, until it is set, for
// none.
func (s *Store) Transcodes(ctx context.Context) (int, error) {
	v, _, err := s.setting(ctx, settingTranscodeVideo)
	n, perr := strconv.Atoi(v)
	if perr != nil || n < 0 || n > MaxTranscodes {
		n = 0
	}
	return n, err
}

// SetTranscodes sets how many films and episodes at once this server may
// transcode whose video a box cannot play, from 0 for none to
// MaxTranscodes.
func (s *Store) SetTranscodes(ctx context.Context, n int) error {
	if n < 0 || n > MaxTranscodes {
		return fmt.Errorf("store: transcodes at once: %d is not between 0 and %d", n, MaxTranscodes)
	}
	return s.setSetting(ctx, settingTranscodeVideo, strconv.Itoa(n))
}

// ConvertsSound reports whether films and episodes whose sound or file a
// box cannot play go through ffmpeg, which converts them (D68): true until
// it is set otherwise (D78).
func (s *Store) ConvertsSound(ctx context.Context) (bool, error) {
	v, _, err := s.setting(ctx, settingConvertSound)
	return v != "0", err
}

// SetConvertsSound sets whether films and episodes whose sound or file a
// box cannot play go through ffmpeg.
func (s *Store) SetConvertsSound(ctx context.Context, on bool) error {
	return s.setSetting(ctx, settingConvertSound, onOff(on))
}

// ServerTranscodes reports whether films and episodes whose video a box
// cannot play go through their source's server where it transcodes, as a
// Jellyfin server does (D71): true until it is set otherwise (D78).
func (s *Store) ServerTranscodes(ctx context.Context) (bool, error) {
	v, _, err := s.setting(ctx, settingServerTranscodes)
	return v != "0", err
}

// SetServerTranscodes sets whether films and episodes whose video a box
// cannot play go through their source's server where it transcodes.
func (s *Store) SetServerTranscodes(ctx context.Context, on bool) error {
	return s.setSetting(ctx, settingServerTranscodes, onOff(on))
}

// onOff writes a switch as a setting's value.
func onOff(on bool) string {
	if on {
		return "1"
	}
	return "0"
}
