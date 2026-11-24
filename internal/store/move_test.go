// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"
)

// lineupView writes what a lineup shows, as "number name (channel's source)",
// by number.
func lineupView(t *testing.T, s *Store, lineupID int64, sources map[int64]string) []string {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), lineupID)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, c := range cs {
		out = append(out, fmt.Sprintf("%02d %s (%s)", c.Number, c.Name, sources[c.SourceID]))
	}
	return out
}

// TestMoveChannels moves a source's channels to another's, and checks that
// every lineup's entry keeps its number, name, group, favourites,
// recordings and backups, that the new channels' own entries go with their
// favourites kept, that what is a channel's own goes with it, and that the
// old source's reads give the moved channels no entry again (D144).
func TestMoveChannels(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersPlaylist))
	one, two := ids["one"], ids["two"]
	names := map[int64]string{one: "one", two: "two"}
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	oldChannels := []CatalogChannel{
		{IdentityKey: "a", Name: "UK: Alpha HD", XMLTVID: "alpha.uk", Category: "News", StreamURL: "http://one.example/a"},
		{IdentityKey: "b", Name: "Bravo", Category: "News", StreamURL: "http://one.example/b"},
		{IdentityKey: "c", Name: "Charlie", Category: "Sport", StreamURL: "http://one.example/c"},
		{IdentityKey: "d", Name: "Delta", Category: "Sport", StreamURL: "http://one.example/d"},
		{IdentityKey: "e", Name: "Echo", Category: "News", StreamURL: "http://one.example/e"},
	}
	apply(t, s, one, at, oldChannels)
	apply(t, s, two, at, []CatalogChannel{
		{IdentityKey: "1", Name: "Alpha", XMLTVID: "alpha.uk", Category: "General", StreamURL: "http://two.example/1"},
		{IdentityKey: "2", Name: "Bravo", Category: "General", StreamURL: "http://two.example/2"},
		{IdentityKey: "3", Name: "Charlie", Category: "Sports", StreamURL: "http://two.example/3"},
		{IdentityKey: "5", Name: "Echo", Category: "News", StreamURL: "http://two.example/5"},
	})
	kids, err := s.CopyLineup(ctx, DefaultLineupID, "kids")
	if err != nil {
		t.Fatal(err)
	}
	chOf := func(source int64, name string) int64 {
		t.Helper()
		var id int64
		if err := s.db.QueryRow("SELECT id FROM channels WHERE source_id = ? AND name = ?", source, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	entryOf := func(lineup, channel int64) int64 {
		t.Helper()
		var id int64
		if err := s.db.QueryRow("SELECT id FROM lineup_entries WHERE lineup_id = ? AND channel_id = ?", lineup, channel).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldA, oldB, oldC, oldD := chOf(one, "UK: Alpha HD"), chOf(one, "Bravo"), chOf(one, "Charlie"), chOf(one, "Delta")
	newA, newB, newC := chOf(two, "Alpha"), chOf(two, "Bravo"), chOf(two, "Charlie")
	oldE, newE := chOf(one, "Echo"), chOf(two, "Echo")
	eA, eB, eC := entryOf(DefaultLineupID, oldA), entryOf(DefaultLineupID, oldB), entryOf(DefaultLineupID, oldC)

	// The household's edits of the old entries, and what it did with the
	// new channel Bravo's own entry.
	if err := s.RenameEntry(ctx, DefaultLineupID, eB, "Bravo One"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesLocked(ctx, DefaultLineupID, []int64{eB}, true); err != nil {
		t.Fatal(err)
	}
	on := true
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{eA}, &on); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntryBackups(ctx, DefaultLineupID, eC, []int64{oldB, newA}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGuideChoice(ctx, GuideChoice{ChannelID: oldC, UseChannel: oldB}, at); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.Profiles(ctx, HouseholdAccount)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("profiles %+v, %v", profiles, err)
	}
	profile := profiles[0].ID
	for _, e := range []int64{eA, entryOf(DefaultLineupID, newB)} {
		if err := s.SetFavourite(ctx, profile, e, true, at); err != nil {
			t.Fatal(err)
		}
	}
	rec, err := s.ScheduleRecording(ctx, Recording{AccountID: HouseholdAccount, EntryID: eC, Channel: "Charlie", Title: "Match",
		Start: at.Add(time.Hour), Stop: at.Add(2 * time.Hour), CreatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	before := lineupView(t, s, DefaultLineupID, names)

	// Moves that cannot be change nothing.
	for _, bad := range [][]ChannelMove{
		nil,
		{{From: oldA, To: oldB}},
		{{From: oldA, To: newA}, {From: oldB, To: newA}},
		{{From: oldA, To: newA}, {From: newA, To: newB}},
		{{From: oldA, To: 999999}},
	} {
		if _, err := s.MoveChannels(ctx, bad); !errors.Is(err, ErrBadMove) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if got := lineupView(t, s, DefaultLineupID, names); !reflect.DeepEqual(got, before) {
		t.Fatalf("a refused move changed the lineup:\n%v\n%v", got, before)
	}

	res, err := s.MoveChannels(ctx, []ChannelMove{{From: oldA, To: newA}, {From: oldB, To: newB}, {From: oldC, To: newC},
		{From: oldE, To: newE}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Channels != 4 || res.Entries != 8 || res.Merged != 8 {
		t.Errorf("result %+v", res)
	}
	// Each lineup shows the old entries, playing from two, with their
	// numbers and names, and Delta, which stays on one.
	want := []string{before[0][:3] + "UK: Alpha HD (two)", before[1][:3] + "Bravo One (two)", before[2][:3] + "Charlie (two)",
		before[3][:3] + "Delta (one)", before[4][:3] + "Echo (two)"}
	if got := lineupView(t, s, DefaultLineupID, names); !reflect.DeepEqual(got, want) {
		t.Errorf("after the move:\n%v\nwant\n%v (before %v)", got, want, before)
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		wantGroup := map[string]string{"UK: Alpha HD": "News", "Bravo One": "News", "Charlie": "Sport", "Delta": "Sport",
			"Echo": "News"}[c.Name]
		if c.Group != wantGroup {
			t.Errorf("%s is in %q, want %q", c.Name, c.Group, wantGroup)
		}
	}
	// Echo's new category has the group that it showed: it keeps none
	// of its own, nor a name.
	var group, name any
	if err := s.db.QueryRow("SELECT group_id, name FROM lineup_entries WHERE lineup_id = ? AND channel_id = ?",
		DefaultLineupID, newE).Scan(&group, &name); err != nil || group != nil || name != nil {
		t.Errorf("Echo's own group %v and name %v, %v", group, name, err)
	}
	if got := lineupView(t, s, kids, names); len(got) != 5 || got[3] != want[3] {
		t.Errorf("the kids' lineup: %v", got)
	}
	// The entries are the same entries, with what hangs off them.
	if entryOf(DefaultLineupID, newA) != eA || entryOf(DefaultLineupID, newB) != eB || entryOf(DefaultLineupID, newC) != eC {
		t.Error("the moved entries are not the old ones")
	}
	favs, err := s.Favourites(ctx, profile)
	if slices.Sort(favs); err != nil || !reflect.DeepEqual(favs, []int64{eA, eB}) {
		t.Errorf("favourites %v, %v", favs, err)
	}
	if r, err := s.RecordingByID(ctx, rec.ID); err != nil || r.EntryID != eC {
		t.Errorf("the recording %+v, %v", r, err)
	}
	backups, err := s.EntryBackups(ctx, DefaultLineupID, eC)
	if err != nil || len(backups) != 2 || backups[0].ChannelID != newB || backups[1].ChannelID != newA {
		t.Errorf("Charlie's backups %+v, %v", backups, err)
	}
	var locked bool
	var repair *bool
	if err := s.db.QueryRow("SELECT e.locked, c.repair FROM lineup_entries e JOIN channels c ON c.id = ? WHERE e.id = ?",
		newA, eB).Scan(&locked, &repair); err != nil || !locked || repair == nil || !*repair {
		t.Errorf("Bravo's lock %v, Alpha's repair %v, %v", locked, repair, err)
	}
	if c, ok, err := s.GuideChoiceOf(ctx, newC); err != nil || !ok || c.UseChannel != newB {
		t.Errorf("Charlie's guide %+v, %v, %v", c, ok, err)
	}

	// The old source's next read gives the moved channels no entry.
	apply(t, s, one, at.Add(time.Hour), oldChannels)
	if got := lineupView(t, s, DefaultLineupID, names); !reflect.DeepEqual(got, want) {
		t.Errorf("after one's next read: %v", got)
	}
	if n, err := s.MovedChannels(ctx, one); n != 4 || err != nil {
		t.Errorf("moved %d, %v", n, err)
	}
	movable, err := s.MovableChannels(ctx, one)
	if err != nil || len(movable) != 1 || movable[0].ID != oldD {
		t.Errorf("movable %+v, %v", movable, err)
	}
	// A moved channel cannot move again.
	if _, err := s.MoveChannels(ctx, []ChannelMove{{From: oldA, To: newB}}); !errors.Is(err, ErrBadMove) {
		t.Errorf("moving again: %v", err)
	}

	// Without two, one's channels come back at its next read.
	if err := s.DeleteSource(ctx, two); err != nil {
		t.Fatal(err)
	}
	apply(t, s, one, at.Add(2*time.Hour), oldChannels)
	if got := lineupView(t, s, DefaultLineupID, names); len(got) != 5 {
		t.Errorf("without two: %v", got)
	}
	if n, _ := s.MovedChannels(ctx, one); n != 0 {
		t.Errorf("still moved: %d", n)
	}
}
