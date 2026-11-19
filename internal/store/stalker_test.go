// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStalkerToken(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	d, err := s.ApproveDevice(ctx, macA, 1, "", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.StalkerTokenHash(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("token before a handshake: %v", err)
	}

	for _, hash := range []string{"first", "second"} {
		if err := s.SetStalkerToken(ctx, d.ID, hash, time.Now()); err != nil {
			t.Fatal(err)
		}
		got, err := s.StalkerTokenHash(ctx, d.ID)
		if err != nil || got != hash {
			t.Errorf("token hash %q, %v; want %q", got, err, hash)
		}
	}

	// Removing the device removes its token.
	if err := s.RemoveDevice(ctx, macA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.StalkerTokenHash(ctx, d.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("token after removing the device: %v", err)
	}
}

func TestCountDevices(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.SeeDevice(ctx, macA, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveDevice(ctx, macB, 1, "", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountDevices(ctx, DevicePending); err != nil || n != 1 {
		t.Errorf("pending: %d, %v", n, err)
	}
}

func TestCaptureRequest(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	d, err := s.ApproveDevice(ctx, macA, 1, "", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	c := Capture{Module: "itv", Action: "set_fav", Params: "fav_ch", Example: "fav_ch=1", UserAgent: "test", DeviceID: d.ID}

	for i := range 2 {
		stored, err := s.CaptureRequest(ctx, c, at.Add(time.Duration(i)*time.Minute), 2)
		if err != nil || !stored {
			t.Fatalf("capture %d: %v, %v", i, stored, err)
		}
	}
	other := c
	other.Action = "get_fav_ids"
	if stored, err := s.CaptureRequest(ctx, other, at, 2); err != nil || !stored {
		t.Fatalf("second kind: %v, %v", stored, err)
	}

	// The log is full: a third kind is dropped, known kinds still count.
	third := c
	third.Action = "set_played"
	if stored, err := s.CaptureRequest(ctx, third, at, 2); err != nil || stored {
		t.Errorf("third kind: %v, %v; want it dropped", stored, err)
	}
	if stored, err := s.CaptureRequest(ctx, c, at.Add(time.Hour), 2); err != nil || !stored {
		t.Errorf("known kind when full: %v, %v", stored, err)
	}

	cs, err := s.Captures(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("%d captures, want 2: %+v", len(cs), cs)
	}
	got := cs[0] // most recent first
	if got.Action != "set_fav" || got.Count != 3 || got.DeviceID != d.ID ||
		!got.FirstSeen.Equal(at) || !got.LastSeen.Equal(at.Add(time.Hour)) {
		t.Errorf("capture %+v", got)
	}

	// Removing the device keeps its captures.
	if err := s.RemoveDevice(ctx, macA); err != nil {
		t.Fatal(err)
	}
	if cs, err = s.Captures(ctx); err != nil || len(cs) != 2 || cs[0].DeviceID != 0 {
		t.Errorf("captures after removing the device: %+v, %v", cs, err)
	}

	// Clearing the log makes room again.
	if n, err := s.ClearCaptures(ctx); err != nil || n != 2 {
		t.Errorf("cleared %d, %v", n, err)
	}
	third.DeviceID = 0 // removed above
	if stored, err := s.CaptureRequest(ctx, third, at, 2); err != nil || !stored {
		t.Errorf("after clearing: %v, %v", stored, err)
	}
}
