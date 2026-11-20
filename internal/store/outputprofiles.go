// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"overscan.gureet.ca/internal/streamprofile"
)

// Output profiles (D110): how an account's devices and players get live
// channels, as their sources send them or transcoded by the hub's ffmpeg. A
// device's own profile, if it has one, comes before its account's.

// ErrNoSuchProfile is the error for an output profile that does not exist.
var ErrNoSuchProfile = errors.New("store: no such output profile")

// SetAccountOutputProfile sets the output profile of an account's devices
// and players.
func (s *Store) SetAccountOutputProfile(ctx context.Context, accountID int64, name string) error {
	if _, ok := streamprofile.Find(name); !ok {
		return ErrNoSuchProfile
	}
	res, err := s.db.ExecContext(ctx, "UPDATE accounts SET output_profile = ? WHERE id = ?", name, accountID)
	if err == nil {
		err = mustAffect(res)
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		err = fmt.Errorf("store: output profile of account %d: %w", accountID, err)
	}
	return err
}

// SetDeviceOutputProfile sets a device's own output profile, "" for its
// account's.
func (s *Store) SetDeviceOutputProfile(ctx context.Context, mac, name string) (Device, error) {
	m, err := ParseMAC(mac)
	if err != nil {
		return Device{}, err
	}
	if _, ok := streamprofile.Find(name); !ok && name != "" {
		return Device{}, ErrNoSuchProfile
	}
	return s.queryDevice(ctx, m, "UPDATE devices SET output_profile = ? WHERE mac = ? RETURNING "+deviceColumns, name, m)
}

// OutputProfileOf returns the output profile that a device plays live
// channels through: its own, or else its account's; the Source profile for
// a device that has neither, or that is not approved.
func (s *Store) OutputProfileOf(ctx context.Context, deviceID int64) (streamprofile.Profile, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(NULLIF(d.output_profile, ''), a.output_profile) FROM devices d JOIN accounts a ON a.id = d.account_id
		WHERE d.id = ? AND d.state = 'approved'`, deviceID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		name, err = streamprofile.Source, nil
	}
	if err != nil {
		return streamprofile.Profile{}, fmt.Errorf("store: output profile of device %d: %w", deviceID, err)
	}
	return outputProfile(name), nil
}

// AccountOutputProfile returns the output profile of an account's players.
func (s *Store) AccountOutputProfile(ctx context.Context, accountID int64) (streamprofile.Profile, error) {
	var name string
	err := s.db.QueryRowContext(ctx, "SELECT output_profile FROM accounts WHERE id = ?", accountID).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		name, err = streamprofile.Source, nil
	}
	if err != nil {
		return streamprofile.Profile{}, fmt.Errorf("store: output profile of account %d: %w", accountID, err)
	}
	return outputProfile(name), nil
}

// outputProfile returns the profile with a name, or the Source profile for
// a name that no longer names one.
func outputProfile(name string) streamprofile.Profile {
	if p, ok := streamprofile.Find(name); ok {
		return p
	}
	p, _ := streamprofile.Find(streamprofile.Source)
	return p
}

// OutputProfilesInUse counts the accounts and devices whose output profiles
// transcode, for the support bundle.
func (s *Store) OutputProfilesInUse(ctx context.Context) (accounts, devices int, err error) {
	var transcoding []any
	for _, p := range streamprofile.Profiles {
		if p.Transcodes() {
			transcoding = append(transcoding, p.Name)
		}
	}
	in := "''"
	if len(transcoding) > 0 {
		in = "?" + strings.Repeat(", ?", len(transcoding)-1)
	}
	if err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM accounts WHERE output_profile IN ("+in+")", transcoding...).Scan(&accounts); err == nil {
		err = s.db.QueryRowContext(ctx, "SELECT count(*) FROM devices WHERE output_profile IN ("+in+")", transcoding...).Scan(&devices)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("store: output profiles in use: %w", err)
	}
	return accounts, devices, nil
}
