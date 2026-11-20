// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"
)

// shownIn returns what devices of a lineup see: "number name group" for
// each channel.
func shownIn(t *testing.T, s *Store, lineupID int64) []string {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), lineupID)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range cs {
		out = append(out, strconv.Itoa(c.Number)+" "+c.Name+" "+c.Group)
	}
	return out
}

// entryIn returns the entry of a channel in a lineup.
func entryIn(t *testing.T, s *Store, lineupID int64, name string) LineupEntry {
	t.Helper()
	es, _, err := s.LineupEntries(context.Background(), lineupID, EntryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.ChannelName == name {
			return e
		}
	}
	t.Fatalf("no entry for %q in lineup %d", name, lineupID)
	return LineupEntry{}
}

// A copy starts as its lineup was, edits and all, and then goes its own
// way; a refresh adds new channels and categories to every lineup (D106).
func TestCopyLineup(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sport", "d/Film"))
	news := groupOf(t, s, "News")
	if err := s.RenameLineupGroup(ctx, DefaultLineupID, news.ID, "Headlines"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameEntry(ctx, DefaultLineupID, entryOf(t, s, "b").ID, "Bravo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PinEntryNumber(ctx, DefaultLineupID, entryOf(t, s, "c").ID, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLineupGroupHidden(ctx, DefaultLineupID, groupOf(t, s, "Film").ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesLocked(ctx, DefaultLineupID, []int64{entryOf(t, s, "a").ID}, true); err != nil {
		t.Fatal(err)
	}

	kids, err := s.CopyLineup(ctx, DefaultLineupID, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1 a Headlines", "2 Bravo Headlines", "30 c Sport"}
	if got := shownIn(t, s, kids); !reflect.DeepEqual(got, want) {
		t.Errorf("the copy shows %q, want %q", got, want)
	}
	if e := entryIn(t, s, kids, "a"); !e.Locked {
		t.Errorf("the copy's a is not locked: %+v", e)
	}
	if e := entryIn(t, s, kids, "c"); !e.Pinned {
		t.Errorf("the copy's c is not pinned: %+v", e)
	}

	// Each goes its own way.
	if err := s.SetEntriesVisibility(ctx, kids, []int64{entryIn(t, s, kids, "a").ID}, Hidden); err != nil {
		t.Fatal(err)
	}
	if got := shownIn(t, s, DefaultLineupID); !reflect.DeepEqual(got, want) {
		t.Errorf("an edit of the copy changed the first: %q", got)
	}

	// A refresh adds a new channel, and a new category, to both.
	apply(t, s, src, at.Add(time.Hour), grouped("a/News", "b/News", "c/Sport", "d/Film", "e/Kids"))
	if got, want := shownIn(t, s, DefaultLineupID), []string{"1 a Headlines", "2 Bravo Headlines", "5 e Kids", "30 c Sport"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the first after a refresh: %q, want %q", got, want)
	}
	if got, want := shownIn(t, s, kids), []string{"2 Bravo Headlines", "5 e Kids", "30 c Sport"}; !reflect.DeepEqual(got, want) {
		t.Errorf("the copy after a refresh: %q, want %q", got, want)
	}

	// A change of the source's numbers numbers both again.
	settings, err := s.SourceSettings(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	settings.Numbers = NumbersProvider
	if err := s.UpdateSource(ctx, src, settings); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{DefaultLineupID, kids} {
		if e := entryIn(t, s, id, "c"); e.Number != 30 {
			t.Errorf("lineup %d: a pinned number moved: %+v", id, e)
		}
	}
}

func TestLineupNamesAndRemoval(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News"))
	kids, err := s.CopyLineup(ctx, DefaultLineupID, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CopyLineup(ctx, kids, "Kids"); !errors.Is(err, ErrLineupExists) {
		t.Errorf("a second Kids: %v", err)
	}
	if _, err := s.CopyLineup(ctx, 999, "Other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a copy of no lineup: %v", err)
	}
	if err := s.RenameLineup(ctx, kids, "Default"); !errors.Is(err, ErrLineupExists) {
		t.Errorf("renamed to the first's name: %v", err)
	}
	if err := s.RenameLineup(ctx, kids, "Children"); err != nil {
		t.Fatal(err)
	}
	sam, err := s.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLineup(ctx, sam.ID, kids); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLineup(ctx, sam.ID, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("an account to no lineup: %v", err)
	}
	all, err := s.Lineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != "Default" || !reflect.DeepEqual(all[0].Accounts, []string{"Household"}) ||
		all[1].Name != "Children" || !reflect.DeepEqual(all[1].Accounts, []string{"Sam"}) || all[1].Shown != 1 {
		t.Errorf("lineups: %+v", all)
	}
	if err := s.RemoveLineup(ctx, kids); !errors.Is(err, ErrLineupInUse) {
		t.Errorf("removing a lineup that an account sees: %v", err)
	}
	if err := s.SetAccountLineup(ctx, sam.ID, DefaultLineupID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveLineup(ctx, kids); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveLineup(ctx, kids); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing it again: %v", err)
	}
	for i := 1; i < MaxLineups; i++ {
		if _, err := s.CopyLineup(ctx, DefaultLineupID, "Copy "+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.CopyLineup(ctx, DefaultLineupID, "One too many"); !errors.Is(err, ErrTooManyLineups) {
		t.Errorf("past the most lineups: %v", err)
	}
}

// An account that sees another lineup keeps its favourites and its
// devices' lists of what they watched, for the channels the lineup has; a
// device moved to another account keeps its list too.
func TestFavouritesFollowTheLineup(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sport"))
	kids, err := s.CopyLineup(ctx, DefaultLineupID, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	sam, err := s.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}
	profiles, err := s.Profiles(ctx, sam.ID)
	if err != nil {
		t.Fatal(err)
	}
	box, err := s.ApproveDevice(ctx, "00:1A:79:00:00:01", sam.ID, "", at)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "c"} {
		if err := s.SetFavourite(ctx, profiles[0].ID, entryOf(t, s, name).ID, true, at); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordWatch(ctx, box.ID, entryOf(t, s, name).ID, at); err != nil {
			t.Fatal(err)
		}
	}
	// The copy lacks c: its entry is gone, as if a rule had left it out.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM lineup_entries WHERE id = ?", entryIn(t, s, kids, "c").ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLineup(ctx, sam.ID, kids); err != nil {
		t.Fatal(err)
	}
	favourites, err := s.Favourites(ctx, profiles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	watched, err := s.Watched(ctx, box.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := entryIn(t, s, kids, "a").ID
	if !reflect.DeepEqual(favourites, []int64{a}) || !reflect.DeepEqual(watched, []int64{a}) {
		t.Errorf("favourites %v, watched %v; want [%d] for both", favourites, watched, a)
	}

	// Moved back to the household, the box's list follows it.
	if _, err := s.SetDeviceAccount(ctx, box.MAC, HouseholdAccount); err != nil {
		t.Fatal(err)
	}
	if watched, err = s.Watched(ctx, box.ID); err != nil {
		t.Fatal(err)
	}
	if want := []int64{entryOf(t, s, "a").ID}; !reflect.DeepEqual(watched, want) {
		t.Errorf("watched after the move: %v, want %v", watched, want)
	}
}
