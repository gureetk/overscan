// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
)

// Timeshift is off until set, and keeps whole minutes up to MaxTimeshift
// (D92).
func TestTimeshiftSetting(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if n, err := s.Timeshift(ctx); err != nil || n != 0 {
		t.Errorf("at first: %d, %v", n, err)
	}
	if err := s.SetTimeshift(ctx, 30); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Timeshift(ctx); err != nil || n != 30 {
		t.Errorf("set: %d, %v", n, err)
	}
	for _, bad := range []int{-1, MaxTimeshift + 1} {
		if err := s.SetTimeshift(ctx, bad); err == nil {
			t.Errorf("%d minutes taken", bad)
		}
	}
}
