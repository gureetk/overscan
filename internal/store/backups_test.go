// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"
)

// A lineup's channel has backups of any source, in order, which play when
// they can, follow a copy of the lineup, and go with their source (D109).
func TestBackups(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersPlaylist))
	apply(t, s, ids["one"], at, grouped("Alpha/News", "Bravo/News", "Same/News"))
	apply(t, s, ids["two"], at, grouped("Alpha HD/News", "Alpha SD/News", "Same/News"))
	alpha := entryIn(t, s, DefaultLineupID, "Alpha")
	hd, sd := entryIn(t, s, DefaultLineupID, "Alpha HD"), entryIn(t, s, DefaultLineupID, "Alpha SD")

	if err := s.SetEntryBackups(ctx, DefaultLineupID, alpha.ID, []int64{hd.ID, sd.ID}); err != nil {
		t.Fatal(err)
	}
	list, err := s.EntryBackups(ctx, DefaultLineupID, alpha.ID)
	if err != nil || len(list) != 2 || list[0].EntryID != hd.ID || list[0].Source != "two" || list[0].Name != "Alpha HD" ||
		list[0].Number != hd.Number || list[1].EntryID != sd.ID {
		t.Fatalf("backups: %+v, %v", list, err)
	}
	if e := entryIn(t, s, DefaultLineupID, "Alpha"); e.Backups != 2 {
		t.Errorf("Alpha counts %d backups", e.Backups)
	}
	p, err := s.PlayableEntry(ctx, alpha.ID)
	if err != nil || len(p.Backups) != 2 || p.Backups[0].SourceID != ids["two"] || p.Backups[0].ChannelID != hd.ChannelID ||
		p.Backups[0].StreamURL != "http://streams.example/Alpha HD" || p.Backups[0].EntryID != alpha.ID ||
		p.Backups[0].Name != "Alpha" || len(p.Backups[0].Backups) != 0 {
		t.Errorf("playable: %+v, %v", p, err)
	}

	for _, tc := range []struct {
		entry   int64
		backups []int64
		want    error
	}{
		{alpha.ID, []int64{alpha.ID}, ErrBadBackup},
		{alpha.ID, []int64{hd.ID, hd.ID}, ErrBadBackup},
		{alpha.ID, []int64{hd.ID, sd.ID, 3, 4, 5, 6}, ErrTooManyBackups},
		{alpha.ID, []int64{999}, ErrNotFound},
		{999, []int64{hd.ID}, ErrNotFound},
	} {
		if err := s.SetEntryBackups(ctx, DefaultLineupID, tc.entry, tc.backups); !errors.Is(err, tc.want) {
			t.Errorf("SetEntryBackups(%d, %v) = %v, want %v", tc.entry, tc.backups, err, tc.want)
		}
	}
	if list, _ := s.EntryBackups(ctx, DefaultLineupID, alpha.ID); len(list) != 2 {
		t.Errorf("a refused change changed them: %+v", list)
	}

	// Found by number, or by name whatever its case.
	for text, want := range map[string]error{"alpha hd": nil, "ALPHA": nil, "Same": ErrSeveral, "Nobody": ErrNotFound, "": ErrNotFound} {
		if _, err := s.FindEntry(ctx, DefaultLineupID, text); !errors.Is(err, want) {
			t.Errorf("FindEntry(%q) = %v, want %v", text, err, want)
		}
	}
	if id, err := s.FindEntry(ctx, DefaultLineupID, " "+strconv.Itoa(sd.Number)+" "); err != nil || id != sd.ID {
		t.Errorf("FindEntry(%d) = %d, %v", sd.Number, id, err)
	}

	// A backup of a source that is off does not play, but stays.
	if err := s.SetSourceEnabled(ctx, ids["two"], false); err != nil {
		t.Fatal(err)
	}
	if p, _ := s.PlayableEntry(ctx, alpha.ID); len(p.Backups) != 0 {
		t.Errorf("backups of a source that is off: %+v", p.Backups)
	}
	if err := s.SetSourceEnabled(ctx, ids["two"], true); err != nil {
		t.Fatal(err)
	}

	// A copy of the lineup has them, for its own entries.
	kids, err := s.CopyLineup(ctx, DefaultLineupID, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	copied, err := s.EntryBackups(ctx, kids, entryIn(t, s, kids, "Alpha").ID)
	if err != nil || len(copied) != 2 || copied[0].EntryID != entryIn(t, s, kids, "Alpha HD").ID {
		t.Errorf("the copy's: %+v, %v", copied, err)
	}
	// Another lineup's entries are not this one's.
	if err := s.SetEntryBackups(ctx, kids, entryIn(t, s, kids, "Alpha").ID, []int64{hd.ID}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a backup from another lineup: %v", err)
	}

	// They go with their source.
	if err := s.DeleteSource(ctx, ids["two"]); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.EntryBackups(ctx, DefaultLineupID, alpha.ID); len(list) != 0 {
		t.Errorf("after the source went: %+v", list)
	}
	// And an empty list removes them.
	if err := s.SetEntryBackups(ctx, DefaultLineupID, alpha.ID, nil); err != nil {
		t.Error(err)
	}
}
