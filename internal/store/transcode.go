// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/streamprofile"
)

// What is transcoded, and the profiles that the administrator makes (D147,
// schema 53). Live TV as a whole, a lineup's group and a channel each say
// whether their streams are transcoded: as needed, as before; always, with
// a profile; or never. The narrowest that says something wins.

var (
	// ErrProfileExists is the error for a profile whose name another has.
	ErrProfileExists = errors.New("store: a profile has that name")
	// ErrProfileInUse is the error for removing a profile that something
	// names.
	ErrProfileInUse = errors.New("store: a profile is in use")
	// ErrTooManyTranscodeProfiles is the error for one profile too many.
	ErrTooManyTranscodeProfiles = errors.New("store: the server has as many profiles as it can keep")
	// ErrProfileFixed is the error for changing or removing a fixed
	// profile (D110).
	ErrProfileFixed = errors.New("store: a fixed profile cannot be changed")
	// ErrNotTranscoding is the error for transcoding always with a profile
	// that sends streams as their sources send them.
	ErrNotTranscoding = errors.New("store: the profile does not transcode")
)

// MaxTranscodeProfiles caps the profiles that the administrator makes.
const MaxTranscodeProfiles = 50

// settingLiveTranscode is live TV's own setting of what is transcoded
// (D147): unset for as needed, "never", or a profile's name.
const settingLiveTranscode = "live_transcode"

// Never is how a setting of what is transcoded says never.
const Never = "never"

// Transcode says whether streams are transcoded (D147): as needed, which
// follows the level above it, then the device's profile; always, with
// Profile; or never.
type Transcode struct {
	Never   bool
	Profile string // the profile that transcodes always, "" if not always
}

// The modes of a Transcode, as the admin and the API name them.
const (
	ModeAsNeeded = "as_needed"
	ModeAlways   = "always"
	ModeNever    = "never"
)

// Mode returns ModeAsNeeded, ModeAlways or ModeNever.
func (t Transcode) Mode() string {
	switch {
	case t.Never:
		return ModeNever
	case t.Profile != "":
		return ModeAlways
	}
	return ModeAsNeeded
}

// AsNeeded reports whether t says nothing of its own.
func (t Transcode) AsNeeded() bool { return !t.Never && t.Profile == "" }

// value is t as the database keeps it.
func (t Transcode) value() any {
	switch {
	case t.Never:
		return Never
	case t.Profile != "":
		return t.Profile
	}
	return nil
}

// transcodeOf reads a Transcode from what the database keeps.
func transcodeOf(v sql.NullString) Transcode {
	switch {
	case !v.Valid || v.String == "":
		return Transcode{}
	case v.String == Never:
		return Transcode{Never: true}
	}
	return Transcode{Profile: v.String}
}

// Where a stream's setting of what is transcoded comes from.
const (
	TranscodeByChannel = "channel"
	TranscodeByGroup   = "group"
	TranscodeByLive    = "live TV"
)

// resolveTranscode returns the setting that wins for a channel in a group,
// and where it comes from: the channel's own, else its group's, else live
// TV's; as needed, from nowhere, if none says anything.
func resolveTranscode(channel, group, live sql.NullString) (Transcode, string) {
	for _, l := range []struct {
		v  sql.NullString
		by string
	}{{channel, TranscodeByChannel}, {group, TranscodeByGroup}, {live, TranscodeByLive}} {
		if t := transcodeOf(l.v); !t.AsNeeded() {
			return t, l.by
		}
	}
	return Transcode{}, ""
}

// liveTranscodeSQL is live TV's own setting as SQL: NULL for as needed.
const liveTranscodeSQL = `(SELECT NULLIF(value, '') FROM settings WHERE key = '` + settingLiveTranscode + `')`

const transcodeProfileColumns = `name, codec, max_lines, quality, max_kbps, frame_rate, deinterlace, tone_map, sound`

func scanTranscodeProfile(row rowScanner) (streamprofile.Profile, error) {
	var p streamprofile.Profile
	err := row.Scan(&p.Name, &p.Codec, &p.MaxLines, &p.Quality, &p.MaxKbps, &p.FrameRate, &p.Deinterlace, &p.ToneMap, &p.Sound)
	p.Made, p.Label = true, p.Name
	return p, err
}

// TranscodeProfiles returns the profiles that the administrator made, by
// name.
func (s *Store) TranscodeProfiles(ctx context.Context) ([]streamprofile.Profile, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+transcodeProfileColumns+" FROM transcode_profiles ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	defer rows.Close()
	var out []streamprofile.Profile
	for rows.Next() {
		p, err := scanTranscodeProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("store: profiles: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: profiles: %w", err)
	}
	return out, nil
}

// TranscodeProfile returns the profile with a name: a fixed one (D110), or
// one that the administrator made. Otherwise it returns ErrNoSuchProfile.
func (s *Store) TranscodeProfile(ctx context.Context, name string) (streamprofile.Profile, error) {
	return profileNamed(ctx, s.db, name)
}

// profileNamed returns the profile with a name, as TranscodeProfile does,
// with q.
func profileNamed(ctx context.Context, q querier, name string) (streamprofile.Profile, error) {
	if p, ok := streamprofile.Find(name); ok {
		return p, nil
	}
	p, err := scanTranscodeProfile(q.QueryRowContext(ctx, "SELECT "+transcodeProfileColumns+" FROM transcode_profiles WHERE name = ?", name))
	if errors.Is(err, sql.ErrNoRows) {
		return streamprofile.Profile{}, ErrNoSuchProfile
	}
	if err != nil {
		return streamprofile.Profile{}, fmt.Errorf("store: profile %q: %w", name, err)
	}
	return p, nil
}

// AddTranscodeProfile adds a profile that the administrator made, which
// streamprofile.Check has checked.
func (s *Store) AddTranscodeProfile(ctx context.Context, p streamprofile.Profile) error {
	if !p.Made {
		return ErrProfileFixed
	}
	if _, fixed := streamprofile.Find(p.Name); fixed {
		return ErrProfileExists
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, "SELECT count(*) FROM transcode_profiles").Scan(&n); err != nil {
			return err
		}
		if n >= MaxTranscodeProfiles {
			return ErrTooManyTranscodeProfiles
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO transcode_profiles (`+transcodeProfileColumns+`, made_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.Name, p.Codec, p.MaxLines, p.Quality, p.MaxKbps, p.FrameRate, p.Deinterlace, p.ToneMap, p.Sound,
			formatTime(time.Now()))
		if isUniqueViolation(err) {
			return ErrProfileExists
		}
		return err
	})
}

// ChangeTranscodeProfile changes the profile named name to p, which
// streamprofile.Check has checked. A new name carries over to everything
// that names the profile: accounts, devices, channels, groups and live TV.
func (s *Store) ChangeTranscodeProfile(ctx context.Context, name string, p streamprofile.Profile) error {
	if _, fixed := streamprofile.Find(name); fixed {
		return ErrProfileFixed
	}
	if _, fixed := streamprofile.Find(p.Name); fixed || !p.Made {
		return ErrProfileExists
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE transcode_profiles SET name = ?, codec = ?, max_lines = ?, quality = ?, max_kbps = ?, frame_rate = ?,
				deinterlace = ?, tone_map = ?, sound = ?
			WHERE name = ?`,
			p.Name, p.Codec, p.MaxLines, p.Quality, p.MaxKbps, p.FrameRate, p.Deinterlace, p.ToneMap, p.Sound, name)
		if isUniqueViolation(err) {
			return ErrProfileExists
		}
		if err != nil {
			return err
		}
		if err := mustAffect(res); err != nil {
			return err
		}
		if p.Name == name {
			return nil
		}
		for _, q := range []string{
			"UPDATE accounts SET output_profile = ?1 WHERE output_profile = ?2",
			"UPDATE devices SET output_profile = ?1 WHERE output_profile = ?2",
			"UPDATE channels SET transcode = ?1 WHERE transcode = ?2",
			"UPDATE lineup_groups SET transcode = ?1 WHERE transcode = ?2",
			"UPDATE settings SET value = ?1 WHERE key = '" + settingLiveTranscode + "' AND value = ?2",
		} {
			if _, err := tx.ExecContext(ctx, q, p.Name, name); err != nil {
				return err
			}
		}
		return nil
	})
}

// ProfileUses counts what names a profile.
type ProfileUses struct {
	Accounts, Devices, Channels, Groups int
	LiveTV                              bool
}

// Any reports whether anything names the profile.
func (u ProfileUses) Any() bool {
	return u.Accounts+u.Devices+u.Channels+u.Groups > 0 || u.LiveTV
}

// String says what names the profile, such as "2 accounts and live TV".
func (u ProfileUses) String() string {
	var parts []string
	add := func(n int, one, many string) {
		switch {
		case n == 1:
			parts = append(parts, "1 "+one)
		case n > 1:
			parts = append(parts, fmt.Sprintf("%d %s", n, many))
		}
	}
	add(u.Accounts, "account", "accounts")
	add(u.Devices, "device", "devices")
	add(u.Channels, "channel", "channels")
	add(u.Groups, "group", "groups")
	if u.LiveTV {
		parts = append(parts, "live TV")
	}
	switch len(parts) {
	case 0:
		return "nothing"
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// TranscodeProfileUses counts what names a profile.
func (s *Store) TranscodeProfileUses(ctx context.Context, name string) (ProfileUses, error) {
	var u ProfileUses
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM accounts WHERE output_profile = ?1),
			(SELECT count(*) FROM devices WHERE output_profile = ?1),
			(SELECT count(*) FROM channels WHERE transcode = ?1),
			(SELECT count(*) FROM lineup_groups WHERE transcode = ?1),
			EXISTS (SELECT 1 FROM settings WHERE key = '`+settingLiveTranscode+`' AND value = ?1)`, name).
		Scan(&u.Accounts, &u.Devices, &u.Channels, &u.Groups, &u.LiveTV)
	if err != nil {
		return ProfileUses{}, fmt.Errorf("store: uses of profile %q: %w", name, err)
	}
	return u, nil
}

// RemoveTranscodeProfile removes a profile that the administrator made, if
// nothing names it; otherwise it returns ErrProfileInUse.
func (s *Store) RemoveTranscodeProfile(ctx context.Context, name string) error {
	if _, fixed := streamprofile.Find(name); fixed {
		return ErrProfileFixed
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var used bool
		err := tx.QueryRowContext(ctx, `
			SELECT EXISTS (SELECT 1 FROM accounts WHERE output_profile = ?1)
				OR EXISTS (SELECT 1 FROM devices WHERE output_profile = ?1)
				OR EXISTS (SELECT 1 FROM channels WHERE transcode = ?1)
				OR EXISTS (SELECT 1 FROM lineup_groups WHERE transcode = ?1)
				OR EXISTS (SELECT 1 FROM settings WHERE key = '`+settingLiveTranscode+`' AND value = ?1)`, name).Scan(&used)
		if err != nil {
			return err
		}
		if used {
			return ErrProfileInUse
		}
		res, err := tx.ExecContext(ctx, "DELETE FROM transcode_profiles WHERE name = ?", name)
		if err == nil {
			err = mustAffect(res)
		}
		return err
	})
}

// checkTranscode checks that a setting that transcodes always names a
// profile that exists and transcodes: ErrNoSuchProfile or
// ErrNotTranscoding otherwise.
func checkTranscode(ctx context.Context, q querier, t Transcode) error {
	if t.Never || t.Profile == "" {
		return nil
	}
	p, err := profileNamed(ctx, q, t.Profile)
	if err != nil {
		return err
	}
	if !p.Transcodes() {
		return ErrNotTranscoding
	}
	return nil
}

// LiveTranscode returns live TV's own setting of what is transcoded.
func (s *Store) LiveTranscode(ctx context.Context) (Transcode, error) {
	v, set, err := s.setting(ctx, settingLiveTranscode)
	if err != nil {
		return Transcode{}, fmt.Errorf("store: live TV's transcoding: %w", err)
	}
	return transcodeOf(sql.NullString{String: v, Valid: set}), nil
}

// SetLiveTranscode sets live TV's own setting of what is transcoded.
func (s *Store) SetLiveTranscode(ctx context.Context, t Transcode) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkTranscode(ctx, tx, t); err != nil {
			return err
		}
		v, _ := t.value().(string)
		_, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT (key) DO UPDATE SET value = excluded.value`, settingLiveTranscode, v)
		return err
	})
}

// SetEntriesTranscode sets what is transcoded of the channels of entries
// of a lineup: at their sources, so in every lineup, as repair is (D57).
func (s *Store) SetEntriesTranscode(ctx context.Context, lineupID int64, entryIDs []int64, t Transcode) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkTranscode(ctx, tx, t); err != nil {
			return err
		}
		for _, id := range entryIDs {
			res, err := tx.ExecContext(ctx, `
				UPDATE channels SET transcode = ?
				WHERE id = (SELECT channel_id FROM lineup_entries WHERE id = ? AND lineup_id = ?)`, t.value(), id, lineupID)
			if err != nil {
				return err
			}
			if err := mustAffect(res); err != nil {
				return err
			}
		}
		return nil
	})
}

// SetLineupGroupTranscode sets what is transcoded of a lineup's group.
func (s *Store) SetLineupGroupTranscode(ctx context.Context, lineupID, groupID int64, t Transcode) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if err := checkTranscode(ctx, tx, t); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE lineup_groups SET transcode = ?, edited = 1 WHERE id = ? AND lineup_id = ?",
			t.value(), groupID, lineupID)
		if err == nil {
			err = mustAffect(res)
		}
		return err
	})
}

// TranscodeCounts counts, for the support bundle, the profiles that the
// administrator made, and the channels and groups that say what is
// transcoded of them, always or never.
type TranscodeCounts struct {
	Profiles                      int
	ChannelsAlways, ChannelsNever int
	GroupsAlways, GroupsNever     int
}

// TranscodeCounts returns the counts for the support bundle.
func (s *Store) TranscodeCounts(ctx context.Context) (TranscodeCounts, error) {
	var c TranscodeCounts
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT count(*) FROM transcode_profiles),
			(SELECT count(*) FROM channels WHERE transcode IS NOT NULL AND transcode <> ?1),
			(SELECT count(*) FROM channels WHERE transcode = ?1),
			(SELECT count(*) FROM lineup_groups WHERE transcode IS NOT NULL AND transcode <> ?1),
			(SELECT count(*) FROM lineup_groups WHERE transcode = ?1)`, Never).
		Scan(&c.Profiles, &c.ChannelsAlways, &c.ChannelsNever, &c.GroupsAlways, &c.GroupsNever)
	if err != nil {
		return TranscodeCounts{}, fmt.Errorf("store: transcoding's counts: %w", err)
	}
	return c, nil
}

// settingGPUTranscodes is how many streams at once the server's graphics
// may transcode (D148): "0" to "4"; DefaultGPUTranscodes until it is set.
const settingGPUTranscodes = "gpu_transcodes"

// Limits on how many streams at once the graphics may transcode (D148).
const (
	DefaultGPUTranscodes = 2
	MaxGPUTranscodes     = 4
)

// GPUTranscodes returns how many streams at once the server's graphics may
// transcode: from 0, for none, to MaxGPUTranscodes; DefaultGPUTranscodes
// until it is set (D148).
func (s *Store) GPUTranscodes(ctx context.Context) (int, error) {
	v, set, err := s.setting(ctx, settingGPUTranscodes)
	if err != nil || !set {
		return DefaultGPUTranscodes, err
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > MaxGPUTranscodes {
		return DefaultGPUTranscodes, nil
	}
	return n, nil
}

// SetGPUTranscodes sets how many streams at once the server's graphics may
// transcode, from 0 for none to MaxGPUTranscodes.
func (s *Store) SetGPUTranscodes(ctx context.Context, n int) error {
	if n < 0 || n > MaxGPUTranscodes {
		return fmt.Errorf("store: streams at once on the graphics: %d is not between 0 and %d", n, MaxGPUTranscodes)
	}
	return s.setSetting(ctx, settingGPUTranscodes, strconv.Itoa(n))
}
