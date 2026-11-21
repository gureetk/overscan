// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// channelNames returns the names of channels, in order.
func channelNames(list []LineupChannel) []string {
	out := []string{}
	for _, c := range list {
		out = append(out, c.Name)
	}
	return out
}

// An account uses every source until it is given only some (D114). Its
// channels are then those with a stream from one of them, its own or a
// backup's, and play from those streams alone; its films and series are
// those of its sources; and a removed source leaves it with none.
func TestAccountSources(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersPlaylist),
		xtreamSource("three"), xtreamSource("four"))
	apply(t, s, ids["one"], at, grouped("Alpha/News", "Bravo/News"))
	apply(t, s, ids["two"], at, grouped("Charlie/News", "Delta/News"))
	for _, name := range []string{"three", "four"} {
		if _, err := s.ApplyLibrary(ctx, ids[name], KindMovie, []LibraryCategory{{Key: "9", Name: "Drama"}},
			[]LibraryTitle{{Key: "1", Category: "9", Name: "Film from " + name, AddedAt: at}}, at); err != nil {
			t.Fatal(err)
		}
	}
	bravo, charlie := entryIn(t, s, DefaultLineupID, "Bravo"), entryIn(t, s, DefaultLineupID, "Charlie")
	delta := entryIn(t, s, DefaultLineupID, "Delta")
	// Charlie, of two, has Bravo, of one, as its backup.
	if err := s.SetEntryBackups(ctx, DefaultLineupID, charlie.ID, []int64{bravo.ID}); err != nil {
		t.Fatal(err)
	}

	// At first, every account uses every source.
	household, err := s.Account(ctx, 1)
	if err != nil || !reflect.DeepEqual(household.Sources, EverySource) {
		t.Fatalf("household %+v, %v", household.Sources, err)
	}
	list, err := s.AccountChannels(ctx, household)
	if err != nil || !reflect.DeepEqual(channelNames(list), []string{"Alpha", "Bravo", "Charlie", "Delta"}) {
		t.Fatalf("household's channels %q, %v", channelNames(list), err)
	}

	kids, err := s.AddAccount(ctx, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountSources(ctx, kids.ID, Sources{Only: true, IDs: []int64{ids["one"], ids["one"]}}); err != nil {
		t.Fatal(err)
	}
	if kids, err = s.Account(ctx, kids.ID); err != nil || !kids.Sources.Only || !reflect.DeepEqual(kids.Sources.IDs, []int64{ids["one"]}) {
		t.Fatalf("kids %+v, %v", kids.Sources, err)
	}
	if !kids.Sources.Uses(ids["one"]) || kids.Sources.Uses(ids["two"]) || !household.Sources.Uses(ids["two"]) {
		t.Error("Uses is wrong")
	}
	if n, err := s.LimitedAccounts(ctx); err != nil || n != 1 {
		t.Errorf("LimitedAccounts = %d, %v", n, err)
	}

	// Charlie shows by its backup, without past programmes of its own
	// source; Delta does not show.
	list, err = s.AccountChannels(ctx, kids)
	if err != nil || !reflect.DeepEqual(channelNames(list), []string{"Alpha", "Bravo", "Charlie"}) {
		t.Fatalf("kids' channels %q, %v", channelNames(list), err)
	}
	if err := s.AccountSees(ctx, kids, charlie.ID); err != nil {
		t.Errorf("AccountSees(Charlie) = %v", err)
	}
	if err := s.AccountSees(ctx, kids, delta.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("AccountSees(Delta) = %v", err)
	}
	seen, err := s.AccountEntries(ctx, kids)
	if err != nil || len(seen) != 3 || !seen[charlie.ID] || seen[delta.ID] {
		t.Errorf("AccountEntries = %v, %v", seen, err)
	}

	// Charlie plays from Bravo's stream alone for Kids, and from its own
	// first for the household.
	p, err := s.PlayableEntry(ctx, charlie.ID)
	if err != nil {
		t.Fatal(err)
	}
	if own, ok := household.Sources.For(p); !ok || own.SourceID != ids["two"] || len(own.Backups) != 1 {
		t.Errorf("household plays %+v, %v", own, ok)
	}
	got, ok := kids.Sources.For(p)
	if !ok || got.SourceID != ids["one"] || got.ChannelID != bravo.ChannelID || got.EntryID != charlie.ID ||
		got.Name != "Charlie" || len(got.Backups) != 0 {
		t.Errorf("kids play %+v, %v", got, ok)
	}
	if p, err := s.PlayableEntry(ctx, delta.ID); err != nil {
		t.Fatal(err)
	} else if _, ok := kids.Sources.For(p); ok {
		t.Error("kids play Delta")
	}

	// Films and series of its sources only: none of one, then those of
	// three.
	if page, total, err := s.TitlePage(ctx, KindMovie, 0, "", ByName, 0, 10, false, kids.Sources); err != nil || total != 0 || len(page) != 0 {
		t.Errorf("kids' films %+v, %d, %v", page, total, err)
	}
	if err := s.SetAccountSources(ctx, kids.ID, Sources{Only: true, IDs: []int64{ids["three"], ids["one"]}}); err != nil {
		t.Fatal(err)
	}
	if kids, err = s.Account(ctx, kids.ID); err != nil || !reflect.DeepEqual(kids.Sources.IDs, []int64{ids["one"], ids["three"]}) {
		t.Fatalf("kids %+v, %v", kids.Sources, err)
	}
	page, total, err := s.TitlePage(ctx, KindMovie, 0, "", ByName, 0, 10, false, kids.Sources)
	if err != nil || total != 1 || len(page) != 1 || page[0].Name != "Film from three" {
		t.Errorf("kids' films %+v, %d, %v", page, total, err)
	}
	shelves, err := s.Shelves(ctx, KindMovie, kids.Sources)
	if err != nil || len(shelves) != 1 || shelves[0].Count != 1 {
		t.Fatalf("kids' shelves %+v, %v", shelves, err)
	}
	if rows, err := s.ShelfTitles(ctx, KindMovie, shelves[0].ID, ByName, kids.Sources); err != nil ||
		!reflect.DeepEqual(names(rows), []string{"Film from three"}) {
		t.Errorf("kids' shelf %q, %v", names(rows), err)
	}
	if rows, err := s.RecentTitles(ctx, KindMovie, 10, kids.Sources); err != nil || !reflect.DeepEqual(names(rows), []string{"Film from three"}) {
		t.Errorf("kids' recent %q, %v", names(rows), err)
	}
	if rows, err := s.SearchTitles(ctx, "film", 10, kids.Sources); err != nil || !reflect.DeepEqual(names(rows), []string{"Film from three"}) {
		t.Errorf("kids' search %q, %v", names(rows), err)
	}
	if rows, err := s.SearchTitles(ctx, "film", 10, household.Sources); err != nil || len(rows) != 2 {
		t.Errorf("household's search %q, %v", names(rows), err)
	}
	film := titleIDs(t, s)["Film from four"]
	if err := s.SetProgress(ctx, kidsProfile(t, s, kids.ID), Progress{TitleID: film, Position: 600, Duration: 6000, WatchedAt: at}); err != nil {
		t.Fatal(err)
	}
	if w, err := s.ContinueWatching(ctx, kidsProfile(t, s, kids.ID), KindMovie, 10, kids.Sources); err != nil || len(w) != 0 {
		t.Errorf("kids continue %+v, %v", w, err)
	}
	if w, err := s.ContinueWatching(ctx, kidsProfile(t, s, kids.ID), KindMovie, 10, EverySource); err != nil || len(w) != 1 {
		t.Errorf("continue %+v, %v", w, err)
	}

	// Mistakes change nothing.
	for _, tc := range []struct {
		account int64
		u       Sources
		want    error
	}{
		{kids.ID, Sources{Only: true}, ErrNoSources},
		{kids.ID, Sources{Only: true, IDs: []int64{ids["one"], 9999}}, ErrNoSuchSource},
		{9999, Sources{Only: true, IDs: []int64{ids["one"]}}, ErrNotFound},
	} {
		if err := s.SetAccountSources(ctx, tc.account, tc.u); !errors.Is(err, tc.want) {
			t.Errorf("SetAccountSources(%d, %+v) = %v, want %v", tc.account, tc.u, err, tc.want)
		}
	}
	if again, _ := s.Account(ctx, kids.ID); !reflect.DeepEqual(again.Sources, kids.Sources) {
		t.Errorf("a mistake changed the sources: %+v", again.Sources)
	}

	// Every source again.
	if err := s.SetAccountSources(ctx, kids.ID, EverySource); err != nil {
		t.Fatal(err)
	}
	if kids, _ = s.Account(ctx, kids.ID); !reflect.DeepEqual(kids.Sources, EverySource) {
		t.Errorf("kids %+v", kids.Sources)
	}

	// A removed source leaves an account that used only it with none.
	if err := s.SetAccountSources(ctx, kids.ID, Sources{Only: true, IDs: []int64{ids["one"]}}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(ctx, ids["one"]); err != nil {
		t.Fatal(err)
	}
	if kids, _ = s.Account(ctx, kids.ID); !kids.Sources.Only || len(kids.Sources.IDs) != 0 {
		t.Errorf("kids %+v", kids.Sources)
	}
	if list, err := s.AccountChannels(ctx, kids); err != nil || len(list) != 0 {
		t.Errorf("kids' channels %q, %v", channelNames(list), err)
	}
	if page, total, err := s.TitlePage(ctx, KindMovie, 0, "", ByName, 0, 10, false, kids.Sources); err != nil || total != 0 || len(page) != 0 {
		t.Errorf("kids' films %+v, %d, %v", page, total, err)
	}
}

// kidsProfile returns the ID of an account's first profile.
func kidsProfile(t *testing.T, s *Store, accountID int64) int64 {
	t.Helper()
	profiles, err := s.Profiles(context.Background(), accountID)
	if err != nil || len(profiles) == 0 {
		t.Fatalf("profiles %+v, %v", profiles, err)
	}
	return profiles[0].ID
}

// Sources.For keeps the streams of the sources that it holds, in order.
func TestSourcesFor(t *testing.T) {
	p := PlayableEntry{EntryID: 7, SourceID: 1, ChannelID: 10, Backups: []PlayableEntry{
		{EntryID: 7, SourceID: 2, ChannelID: 20}, {EntryID: 7, SourceID: 1, ChannelID: 11}, {EntryID: 7, SourceID: 3, ChannelID: 30}}}
	streams := func(p PlayableEntry) []int64 {
		out := []int64{p.ChannelID}
		for _, b := range p.Backups {
			out = append(out, b.ChannelID)
		}
		return out
	}
	for _, tc := range []struct {
		u    Sources
		want []int64
	}{
		{EverySource, []int64{10, 20, 11, 30}},
		{Sources{Only: true, IDs: []int64{1}}, []int64{10, 11}},
		{Sources{Only: true, IDs: []int64{3, 2}}, []int64{20, 30}},
		{Sources{Only: true, IDs: []int64{4}}, nil},
		{Sources{Only: true}, nil},
	} {
		got, ok := tc.u.For(p)
		if tc.want == nil {
			if ok {
				t.Errorf("%+v plays %v", tc.u, streams(got))
			}
			continue
		}
		if !ok || !reflect.DeepEqual(streams(got), tc.want) || got.EntryID != 7 {
			t.Errorf("%+v plays %v, %v; want %v", tc.u, streams(got), ok, tc.want)
		}
	}
	if len(p.Backups) != 3 {
		t.Error("For changed its entry")
	}
}
