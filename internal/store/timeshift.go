// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"fmt"
	"strconv"
)

// settingTimeshift is how many minutes of a box's live channel the server
// keeps on disk, so that the box can pause live TV and go back in it
// (D92). 0, as until it is set, turns timeshift off.
const settingTimeshift = "timeshift_minutes"

// MaxTimeshift is the most minutes the setting takes.
const MaxTimeshift = 240

// Timeshift returns how many minutes of a box's live channel the server
// keeps: 0 while timeshift is off, as it is until set.
func (s *Store) Timeshift(ctx context.Context) (int, error) {
	v, _, err := s.setting(ctx, settingTimeshift)
	n, perr := strconv.Atoi(v)
	if perr != nil || n < 0 || n > MaxTimeshift {
		n = 0
	}
	return n, err
}

// SetTimeshift sets how many minutes of a box's live channel the server
// keeps; 0 turns timeshift off.
func (s *Store) SetTimeshift(ctx context.Context, minutes int) error {
	if minutes < 0 || minutes > MaxTimeshift {
		return fmt.Errorf("store: timeshift: %d minutes is not between 0 and %d", minutes, MaxTimeshift)
	}
	return s.setSetting(ctx, settingTimeshift, strconv.Itoa(minutes))
}
