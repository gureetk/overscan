// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lineup works out how a lineup looks to devices, so that every
// portal shows it the same way.
package lineup

import "overscan.gureet.ca/internal/store"

// Group is a group of channels in a lineup, which Stalker clients call a
// genre. Its ID is the lineup group's (D47), which stays the same when the
// group is renamed.
type Group struct {
	ID   int64
	Name string
}

// Groups returns the groups of the channels, in the order of their first
// channel. Channels in no group are left out.
func Groups(channels []store.LineupChannel) []Group {
	groups := []Group{}
	seen := map[int64]bool{}
	for _, c := range channels {
		if c.GroupID == 0 || seen[c.GroupID] {
			continue
		}
		seen[c.GroupID] = true
		groups = append(groups, Group{ID: c.GroupID, Name: c.Group})
	}
	return groups
}
