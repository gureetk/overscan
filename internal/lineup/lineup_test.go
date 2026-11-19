// SPDX-License-Identifier: AGPL-3.0-or-later

package lineup

import (
	"slices"
	"testing"

	"overscan.gureet.ca/internal/store"
)

func TestGroups(t *testing.T) {
	channels := []store.LineupChannel{
		{Name: "a", GroupID: 7, Group: "Sports"},
		{Name: "b", GroupID: 3, Group: "News"},
		{Name: "c"},
		{Name: "d", GroupID: 7, Group: "Sports"},
		{Name: "e", GroupID: 3, Group: "News"},
	}
	if got, want := Groups(channels), []Group{{7, "Sports"}, {3, "News"}}; !slices.Equal(got, want) {
		t.Errorf("groups %+v, want %+v", got, want)
	}
	if got := Groups(nil); got == nil || len(got) != 0 {
		t.Errorf("no channels: %#v", got)
	}
}
