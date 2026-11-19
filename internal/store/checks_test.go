// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"
)

func TestPlaybackChecks(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	a, err := s.ApproveDevice(ctx, macA, 1, "Living room", at)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.ApproveDevice(ctx, macB, 1, "Bedroom", at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlaybackCheck(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a device never checked: %v", err)
	}
	if _, err := s.LatestPlaybackCheck(ctx, "MAG322"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a model never checked: %v", err)
	}

	first := PlaybackCheck{DeviceID: a.ID, Model: "MAG322", Firmware: "2.20.16-322",
		Findings: map[string]bool{"audio:dts": false, "audio:eac3": true},
		Results:  json.RawMessage(`[{"id":"dts","answer":"no"}]`), CheckedAt: at}
	if err := s.SetPlaybackCheck(ctx, first); err != nil {
		t.Fatal(err)
	}
	got, err := s.PlaybackCheck(ctx, a.ID)
	if err != nil || got.Model != "MAG322" || got.Firmware != "2.20.16-322" || !maps.Equal(got.Findings, first.Findings) ||
		string(got.Results) != string(first.Results) || !got.CheckedAt.Equal(at) {
		t.Errorf("PlaybackCheck = %+v, %v", got, err)
	}

	// Another box of the model, checked later, is the model's latest; a
	// check again replaces the device's.
	second := first
	second.DeviceID, second.CheckedAt = b.ID, at.Add(time.Hour)
	second.Findings = map[string]bool{"audio:dts": true}
	if err := s.SetPlaybackCheck(ctx, second); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestPlaybackCheck(ctx, "MAG322"); err != nil || got.DeviceID != b.ID {
		t.Errorf("latest %+v, %v", got, err)
	}
	first.CheckedAt = at.Add(2 * time.Hour)
	first.Results = nil
	if err := s.SetPlaybackCheck(ctx, first); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LatestPlaybackCheck(ctx, "MAG322"); err != nil || got.DeviceID != a.ID || string(got.Results) != "null" {
		t.Errorf("latest after a check again %+v, %v", got, err)
	}
	all, err := s.PlaybackChecks(ctx)
	if err != nil || len(all) != 2 || all[b.ID].Findings["audio:dts"] != true {
		t.Errorf("PlaybackChecks = %+v, %v", all, err)
	}

	// Forgetting, and removing the device, drop it.
	if err := s.ForgetPlaybackCheck(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ForgetPlaybackCheck(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("forgetting twice: %v", err)
	}
	if err := s.RemoveDevice(ctx, macB); err != nil {
		t.Fatal(err)
	}
	if all, err := s.PlaybackChecks(ctx); err != nil || len(all) != 0 {
		t.Errorf("after removing: %+v, %v", all, err)
	}
	// Nothing is kept of a device that does not exist.
	second.DeviceID = 99999
	if err := s.SetPlaybackCheck(ctx, second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlaybackCheck(ctx, 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("kept a check of no device: %v", err)
	}
}
