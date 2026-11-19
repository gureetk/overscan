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

// lineupSetup makes a store with one source, main, numbered in playlist
// order, and returns the store, the source's ID and a time to refresh at.
func lineupSetup(t *testing.T) (*Store, int64, time.Time) {
	t.Helper()
	s := openTest(t, newPath(t))
	ids := addSources(t, s, testSource("main", 1, NumbersPlaylist))
	return s, ids["main"], time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)
}

// grouped returns catalog channels named and keyed by the names before the
// slash in specs such as "a/News", in the category after it, if any.
func grouped(specs ...string) []CatalogChannel {
	out := make([]CatalogChannel, len(specs))
	for i, spec := range specs {
		name, category := spec, ""
		for j := range spec {
			if spec[j] == '/' {
				name, category = spec[:j], spec[j+1:]
				break
			}
		}
		out[i] = CatalogChannel{IdentityKey: name, Name: name, Category: category, StreamURL: "http://streams.example/" + name}
	}
	return out
}

func apply(t *testing.T, s *Store, src int64, at time.Time, chs []CatalogChannel) {
	t.Helper()
	if _, err := s.ApplyCatalog(context.Background(), src, chs, at); err != nil {
		t.Fatal(err)
	}
}

// shown returns what devices see: "number name group" for each channel.
func shown(t *testing.T, s *Store) []string {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range cs {
		out = append(out, strconv.Itoa(c.Number)+" "+c.Name+" "+c.Group)
	}
	return out
}

// entryOf returns the lineup entry of the channel with the given name.
func entryOf(t *testing.T, s *Store, name string) LineupEntry {
	t.Helper()
	es, _, err := s.LineupEntries(context.Background(), DefaultLineupID, EntryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range es {
		if e.ChannelName == name {
			return e
		}
	}
	t.Fatalf("no entry for %q", name)
	return LineupEntry{}
}

// groupOf returns the lineup group with the given name.
func groupOf(t *testing.T, s *Store, name string) LineupGroup {
	t.Helper()
	gs, err := s.LineupGroups(context.Background(), DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Name == name {
			return g
		}
	}
	t.Fatalf("no group %q in %+v", name, gs)
	return LineupGroup{}
}

func TestUpgradeKeepsGenreIDsAndEntries(t *testing.T) {
	path := newPath(t)
	ctx := context.Background()
	old, err := open(ctx, path, quiet(), migrations[:7])
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO sources (id, name, type) VALUES (1, 'one', 'm3u'), (2, 'two', 'm3u')`,
		`INSERT INTO categories (id, source_id, kind, name, position) VALUES (5, 1, 'live', 'News', 1), (3, 2, 'live', 'News', 1), (8, 1, 'live', 'Sports', 2)`,
		`INSERT INTO channels (id, source_id, identity_key, name, category_id, stream_url, position, listed_at) VALUES
			(10, 1, 'a', 'Alpha', 5, 'http://streams.example/a', 1, '2026-11-13T00:00:00Z'),
			(11, 1, 'b', 'Bravo', 8, 'http://streams.example/b', 2, '2026-11-13T00:00:00Z'),
			(12, 2, 'c', 'Charlie', 3, 'http://streams.example/c', 1, '2026-11-13T00:00:00Z')`,
		`INSERT INTO lineup_entries (id, lineup_id, channel_id, number, hidden) VALUES (40, 1, 10, 1, 0), (41, 1, 11, 2, 1), (42, 1, 12, 3, 0)`,
	} {
		if _, err := old.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s := openTest(t, path)
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	// News of both sources is one group, with the lowest of their IDs, as
	// its genre had; Bravo stays hidden.
	if len(cs) != 2 || cs[0].EntryID != 40 || cs[0].GroupID != 3 || cs[0].Group != "News" || cs[1].EntryID != 42 || cs[1].GroupID != 3 {
		t.Errorf("after the upgrade: %+v", cs)
	}
	if e := entryOf(t, s, "Bravo"); e.ID != 41 || e.Number != 2 || e.Visibility != Hidden || e.GroupID != 8 || e.Group != "Sports" {
		t.Errorf("Bravo %+v", e)
	}
	if e := entryOf(t, s, "Alpha"); e.Visibility != AsGroup || e.Pinned {
		t.Errorf("Alpha %+v", e)
	}
}

func TestCategoriesMakeGroups(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	ids := addSources(t, s, testSource("more", 1, ""))
	apply(t, s, src, at, grouped("a/News", "b/Sports", "c"))
	apply(t, s, ids["more"], at, grouped("d/News"))
	if got, want := shown(t, s), []string{"1 a News", "2 b Sports", "3 c ", "4 d News"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	news := groupOf(t, s, "News")
	if news.Channels != 2 || news.Shown != 2 || news.FirstNumber != 1 || len(news.Categories) != 2 || news.Edited {
		t.Errorf("News %+v", news)
	}
	gs, _ := s.LineupGroups(ctx, DefaultLineupID)
	if len(gs) != 3 || gs[0].Name != "News" || gs[1].Name != "Sports" || gs[2].ID != 0 || gs[2].Channels != 1 {
		t.Errorf("groups %+v", gs)
	}
}

func TestHiddenGroups(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/Sports"))
	sports := groupOf(t, s, "Sports")
	if err := s.SetLineupGroupHidden(ctx, DefaultLineupID, sports.ID, true); err != nil {
		t.Fatal(err)
	}
	// A channel the source adds to a hidden group is hidden too.
	apply(t, s, src, at.Add(time.Hour), grouped("a/News", "b/Sports", "c/Sports"))
	if got, want := shown(t, s), []string{"1 a News"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// One channel shown on its own.
	c := entryOf(t, s, "c")
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{c.ID}, Shown); err != nil {
		t.Fatal(err)
	}
	if got, want := shown(t, s), []string{"1 a News", "3 c Sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := s.PlayableEntry(ctx, entryOf(t, s, "b").ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a hidden channel can be played: %v", err)
	}
	if _, err := s.PlayableEntry(ctx, c.ID); err != nil {
		t.Errorf("a channel shown on its own cannot be played: %v", err)
	}
	if g := groupOf(t, s, "Sports"); !g.Hidden || !g.Edited || g.Shown != 1 || g.Channels != 2 {
		t.Errorf("Sports %+v", g)
	}
}

func TestNewCategoriesCanStartHidden(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News"))
	if err := s.SetNewCategoriesHidden(ctx, src, true); err != nil {
		t.Fatal(err)
	}
	if srcs, _ := s.Sources(ctx); !srcs[0].NewCategoriesHidden {
		t.Errorf("the setting did not stick: %+v", srcs[0])
	}
	apply(t, s, src, at.Add(time.Hour), grouped("a/News", "b/News", "c/Movies Now"))
	// A new channel in a known category is shown; a new category is hidden.
	if got, want := shown(t, s), []string{"1 a News", "2 b News"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if g := groupOf(t, s, "Movies Now"); !g.Hidden || g.Edited {
		t.Errorf("the new category's group %+v", g)
	}
	if err := s.SetNewCategoriesHidden(ctx, 999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown source: %v", err)
	}
}

func TestEditsSurviveRefreshesAndAbsence(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sports"))
	mine, err := s.CreateLineupGroup(ctx, DefaultLineupID, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	b := entryOf(t, s, "b")
	if err := s.RenameEntry(ctx, DefaultLineupID, b.ID, "Bravo"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesGroup(ctx, DefaultLineupID, []int64{b.ID}, mine); err != nil {
		t.Fatal(err)
	}
	apply(t, s, src, at.Add(time.Hour), grouped("a/News", "b/News", "c/Sports"))
	if got, want := shown(t, s), []string{"1 a News", "2 Bravo Mine", "3 c Sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after a refresh: %v, want %v", got, want)
	}

	// b goes, and a new channel takes its number; b keeps its edits.
	apply(t, s, src, at.Add(2*time.Hour), grouped("a/News", "x/News", "c/Sports"))
	if got, want := shown(t, s), []string{"1 a News", "2 x News", "3 c Sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("while b is missing: %v, want %v", got, want)
	}
	if e := entryOf(t, s, "b"); e.ID != b.ID || e.Number != 0 || !e.Missing || !e.Renamed || !e.Moved {
		t.Errorf("b while missing %+v", e)
	}
	// b comes back, with its edits, and a number after the highest.
	apply(t, s, src, at.Add(3*time.Hour), grouped("a/News", "x/News", "c/Sports", "b/News"))
	if got, want := shown(t, s), []string{"1 a News", "2 x News", "3 c Sports", "4 Bravo Mine"}; !reflect.DeepEqual(got, want) {
		t.Errorf("b back: %v, want %v", got, want)
	}

	// Resetting the edits.
	if err := s.RenameEntry(ctx, DefaultLineupID, b.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesGroup(ctx, DefaultLineupID, []int64{b.ID}, 0); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "b"); e.Name != "b" || e.Renamed || e.Moved || e.Group != "News" {
		t.Errorf("b reset %+v", e)
	}
	// Mine is the administrator's, so it stays though nothing is in it.
	if g := groupOf(t, s, "Mine"); g.Channels != 0 || !g.Edited {
		t.Errorf("Mine %+v", g)
	}
	if err := s.RenameEntry(ctx, DefaultLineupID, 9999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}

func TestPinnedNumbers(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a", "b", "c", "d"))

	// Pinning c to 1 swaps it with a.
	if err := s.PinEntryNumber(ctx, DefaultLineupID, entryOf(t, s, "c").ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"c": 1, "b": 2, "a": 3, "d": 4}; !reflect.DeepEqual(got, want) {
		t.Errorf("after pinning: %v, want %v", got, want)
	}
	if e := entryOf(t, s, "c"); !e.Pinned {
		t.Errorf("c %+v", e)
	}

	// Renumbering moves the others, never c; a, which wants 1, goes after
	// the highest.
	if err := s.UpdateSource(ctx, src, testSource("main", 1, NumbersProvider)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateSource(ctx, src, testSource("main", 1, NumbersPlaylist)); err != nil {
		t.Fatal(err)
	}
	if got := lineupNumbers(t, s); got["c"] != 1 || got["b"] != 2 || got["d"] != 4 || got["a"] != 5 {
		t.Errorf("after renumbering: %v", got)
	}

	// While c is missing, its pinned number stays its own: a new channel
	// that wants 1 goes after the highest.
	apply(t, s, src, at.Add(time.Hour), grouped("new", "b", "a", "d"))
	if got := lineupNumbers(t, s); got["c"] != 1 || got["new"] != 6 {
		t.Errorf("c missing: %v", got)
	}

	if err := s.PinEntryNumber(ctx, DefaultLineupID, entryOf(t, s, "b").ID, 0); !errors.Is(err, ErrBadNumber) {
		t.Errorf("number 0: %v", err)
	}
	if err := s.PinEntryNumber(ctx, DefaultLineupID, entryOf(t, s, "b").ID, MaxNumber+1); !errors.Is(err, ErrBadNumber) {
		t.Errorf("number too high: %v", err)
	}
	if err := s.PinEntryNumber(ctx, DefaultLineupID, 9999, 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}

func TestPinningAnUnnumberedEntry(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a", "b"))
	apply(t, s, src, at.Add(time.Hour), grouped("x", "b")) // x takes a's 1
	a := entryOf(t, s, "a")
	if a.Number != 0 {
		t.Fatalf("a %+v", a)
	}
	// a takes 1 back; x, which held it, goes after the highest.
	if err := s.PinEntryNumber(ctx, DefaultLineupID, a.ID, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"a": 1, "b": 2, "x": 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResetEntryNumber(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a", "b", "c"))
	c := entryOf(t, s, "c")
	if err := s.PinEntryNumber(ctx, DefaultLineupID, c.ID, 50); err != nil {
		t.Fatal(err)
	}
	// c wants 3, which is free again: it takes it.
	if err := s.ResetEntryNumber(ctx, DefaultLineupID, c.ID); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "c"); e.Number != 3 || e.Pinned {
		t.Errorf("c %+v", e)
	}
	// b pinned to 1 swaps with a; reset, b wants 2, which a, listed, holds:
	// b keeps 1, unpinned.
	b := entryOf(t, s, "b")
	if err := s.PinEntryNumber(ctx, DefaultLineupID, b.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := s.ResetEntryNumber(ctx, DefaultLineupID, b.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"b": 1, "a": 2, "c": 3}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if entryOf(t, s, "b").Pinned {
		t.Error("b is still pinned")
	}
}

func TestNumberGroupFrom(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/Sports", "c/News", "d/Sports", "e/News"))
	news := groupOf(t, s, "News")
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{entryOf(t, s, "e").ID}, Hidden); err != nil {
		t.Fatal(err)
	}
	n, err := s.NumberGroupFrom(ctx, DefaultLineupID, news.ID, 2)
	if err != nil || n != 2 {
		t.Fatalf("numbered %d, %v", n, err)
	}
	// a and c become 2 and 3; b and d, which held them, swap; hidden e
	// keeps its number.
	if got, want := lineupNumbers(t, s), map[string]int{"b": 1, "a": 2, "c": 3, "d": 4, "e": 5}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if !entryOf(t, s, "a").Pinned || !entryOf(t, s, "c").Pinned || entryOf(t, s, "b").Pinned {
		t.Error("only the group's channels are pinned")
	}
	if _, err := s.NumberGroupFrom(ctx, DefaultLineupID, news.ID, MaxNumber); !errors.Is(err, ErrBadNumber) {
		t.Errorf("past the highest number: %v", err)
	}
}

func TestMergeMoveAndDeleteGroups(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/UK News", "b/US News", "c/Sports"))
	uk, us := groupOf(t, s, "UK News"), groupOf(t, s, "US News")

	if err := s.RenameLineupGroup(ctx, DefaultLineupID, uk.ID, "News"); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameLineupGroup(ctx, DefaultLineupID, us.ID, "News"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("a name taken: %v", err)
	}
	if _, err := s.CreateLineupGroup(ctx, DefaultLineupID, "Sports"); !errors.Is(err, ErrGroupExists) {
		t.Errorf("making a group whose name is taken: %v", err)
	}
	if err := s.MergeLineupGroup(ctx, DefaultLineupID, us.ID, uk.ID); err != nil {
		t.Fatal(err)
	}
	if got, want := shown(t, s), []string{"1 a News", "2 b News", "3 c Sports"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after merging: %v, want %v", got, want)
	}
	if g := groupOf(t, s, "News"); len(g.Categories) != 2 || g.Channels != 2 {
		t.Errorf("News %+v", g)
	}
	// The merged categories stay together through a refresh.
	apply(t, s, src, at.Add(time.Hour), grouped("a/UK News", "b/US News", "c/Sports"))
	if got := shown(t, s); got[1] != "2 b News" {
		t.Errorf("after a refresh: %v", got)
	}

	if err := s.DeleteLineupGroup(ctx, DefaultLineupID, uk.ID); !errors.Is(err, ErrGroupNotEmpty) {
		t.Errorf("deleting a group with channels: %v", err)
	}

	// Moving Sports' category into News leaves Sports empty, and Sports,
	// which Overscan made and nobody changed, goes.
	var sportsCategory int64
	for _, c := range groupOf(t, s, "Sports").Categories {
		sportsCategory = c.ID
	}
	if err := s.MoveCategory(ctx, DefaultLineupID, sportsCategory, uk.ID); err != nil {
		t.Fatal(err)
	}
	gs, _ := s.LineupGroups(ctx, DefaultLineupID)
	if len(gs) != 1 || gs[0].Name != "News" || gs[0].Channels != 3 {
		t.Errorf("groups %+v", gs)
	}

	// An empty group of the administrator's can be deleted.
	mine, _ := s.CreateLineupGroup(ctx, DefaultLineupID, "Mine")
	if err := s.DeleteLineupGroup(ctx, DefaultLineupID, mine); err != nil {
		t.Errorf("deleting an empty group: %v", err)
	}
	if err := s.DeleteLineupGroup(ctx, DefaultLineupID, mine); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleting it again: %v", err)
	}
	if err := s.MoveCategory(ctx, DefaultLineupID, 9999, uk.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown category: %v", err)
	}
}

func TestEmptyGroupsGoUnlessEdited(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/Sports", "c/Films"))
	if err := s.SetLineupGroupHidden(ctx, DefaultLineupID, groupOf(t, s, "Films").ID, true); err != nil {
		t.Fatal(err)
	}
	// The source drops Sports and Films: Sports goes; Films, which the
	// administrator hid, stays, and takes its category back when it returns.
	apply(t, s, src, at.Add(time.Hour), grouped("a/News"))
	apply(t, s, src, at.Add(8*24*time.Hour), grouped("a/News"))
	gs, _ := s.LineupGroups(ctx, DefaultLineupID)
	var names []string
	for _, g := range gs {
		names = append(names, g.Name)
	}
	if !reflect.DeepEqual(names, []string{"News", "Films"}) {
		t.Errorf("groups %v", names)
	}
	apply(t, s, src, at.Add(9*24*time.Hour), grouped("a/News", "c/Films"))
	if got, want := shown(t, s), []string{"1 a News"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Films came back shown: %v, want %v", got, want)
	}
}

func TestLineupEntriesQuery(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("Alpha/News", "Bravo/News", "100% Charlie/Sports", "Delta"))
	news := groupOf(t, s, "News").ID
	none := int64(0)
	for _, tc := range []struct {
		q     EntryQuery
		names []string
		total int
	}{
		{EntryQuery{}, []string{"Alpha", "Bravo", "100% Charlie", "Delta"}, 4},
		{EntryQuery{Group: &news}, []string{"Alpha", "Bravo"}, 2},
		{EntryQuery{Group: &none}, []string{"Delta"}, 1},
		{EntryQuery{NotGroup: &news}, []string{"100% Charlie", "Delta"}, 2},
		{EntryQuery{NotGroup: &none, Text: "a"}, []string{"Alpha", "Bravo", "100% Charlie"}, 3},
		{EntryQuery{Text: "BRA"}, []string{"Bravo"}, 1},
		{EntryQuery{Text: "%"}, []string{"100% Charlie"}, 1},
		{EntryQuery{Text: "_"}, nil, 0},
		{EntryQuery{Limit: 2, Offset: 1}, []string{"Bravo", "100% Charlie"}, 4},
	} {
		es, total, err := s.LineupEntries(ctx, DefaultLineupID, tc.q)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, e := range es {
			names = append(names, e.Name)
		}
		if !reflect.DeepEqual(names, tc.names) || total != tc.total {
			t.Errorf("%+v: %v of %d, want %v of %d", tc.q, names, total, tc.names, tc.total)
		}
	}
	e, err := s.LineupEntry(ctx, DefaultLineupID, entryOf(t, s, "Alpha").ID)
	if err != nil || e.Name != "Alpha" || e.Category != "News" || e.Source != "main" || e.Number != 1 {
		t.Errorf("entry %+v, %v", e, err)
	}
	if _, err := s.LineupEntry(ctx, DefaultLineupID, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}
