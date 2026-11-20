// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"overscan.gureet.ca/internal/streamprofile"
)

// An account's output profile is its devices', unless a device has its
// own; every one starts as its source sends it (D110).
func TestOutputProfiles(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	d, err := s.ApproveDevice(ctx, "00:1A:79:00:00:01", HouseholdAccount, "", at)
	if err != nil {
		t.Fatal(err)
	}
	name := func(p streamprofile.Profile, err error) string {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return p.Name
	}
	if got := name(s.OutputProfileOf(ctx, d.ID)); got != streamprofile.Source {
		t.Errorf("at first: %q", got)
	}
	if a, _ := s.Account(ctx, HouseholdAccount); a.OutputProfile != streamprofile.Source || d.OutputProfile != "" {
		t.Errorf("at first: account %q, device %q", a.OutputProfile, d.OutputProfile)
	}
	if err := s.SetAccountOutputProfile(ctx, HouseholdAccount, "h264-720p"); err != nil {
		t.Fatal(err)
	}
	if got := name(s.OutputProfileOf(ctx, d.ID)); got != "h264-720p" {
		t.Errorf("the account's: %q", got)
	}
	if got := name(s.AccountOutputProfile(ctx, HouseholdAccount)); got != "h264-720p" {
		t.Errorf("the account's own: %q", got)
	}
	if d, err = s.SetDeviceOutputProfile(ctx, d.MAC, streamprofile.Source); err != nil || d.OutputProfile != streamprofile.Source {
		t.Fatalf("the device's own: %+v, %v", d, err)
	}
	if got := name(s.OutputProfileOf(ctx, d.ID)); got != streamprofile.Source {
		t.Errorf("the device's own comes first: %q", got)
	}
	accounts, devices, err := s.OutputProfilesInUse(ctx)
	if err != nil || accounts != 1 || devices != 0 {
		t.Errorf("in use: %d accounts, %d devices, %v", accounts, devices, err)
	}
	if _, err := s.SetDeviceOutputProfile(ctx, d.MAC, ""); err != nil {
		t.Fatal(err)
	}
	if got := name(s.OutputProfileOf(ctx, d.ID)); got != "h264-720p" {
		t.Errorf("back to the account's: %q", got)
	}
	for _, err := range []error{
		s.SetAccountOutputProfile(ctx, HouseholdAccount, "hd"),
		s.SetAccountOutputProfile(ctx, HouseholdAccount, ""),
	} {
		if !errors.Is(err, ErrNoSuchProfile) {
			t.Errorf("a profile that does not exist: %v", err)
		}
	}
	if _, err := s.SetDeviceOutputProfile(ctx, d.MAC, "hd"); !errors.Is(err, ErrNoSuchProfile) {
		t.Errorf("a device's profile that does not exist: %v", err)
	}
	if err := s.SetAccountOutputProfile(ctx, 999, "h264-720p"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such account: %v", err)
	}
	// A device that is not approved, or not known, plays as its source sends.
	if got := name(s.OutputProfileOf(ctx, 999)); got != streamprofile.Source {
		t.Errorf("an unknown device: %q", got)
	}
}
