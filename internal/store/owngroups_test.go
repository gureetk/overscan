// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
	"time"

	"overscan.gureet.ca/internal/lineupfile"
	"overscan.gureet.ca/internal/rules"
)

// lineupShown returns what a lineup shows, by name, with each channel's
// number and group.
func lineupShown(t *testing.T, s *Store, lineupID int64) map[string][2]any {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), lineupID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][2]any{}
	for _, c := range cs {
		out[c.Name] = [2]any{c.Number, c.Group}
	}
	return out
}

// namesOf returns the names in shown, sorted.
func namesOf(shown map[string][2]any) []string {
	var out []string
	for n := range shown {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// threeSources adds the fictional sources one, two and three, each with two
// categories of two channels, and reads them.
func threeSources(t *testing.T, s *Store, at time.Time) map[string]int64 {
	t.Helper()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersProvider),
		testSource("three", 1, NumbersPlaylist))
	for _, name := range []string{"one", "two", "three"} {
		var chs []CatalogChannel
		for i, cat := range []string{"News", "Sport"} {
			for j := 1; j <= 2; j++ {
				key := name + "-" + cat + string(rune('0'+j))
				chs = append(chs, CatalogChannel{IdentityKey: key, Name: name + " " + cat + " " + string(rune('0'+j)),
					Category: cat, Number: 100*(i+1) + j, StreamURL: "http://streams.example/" + key})
			}
		}
		chs = append(chs, CatalogChannel{IdentityKey: name + "-loose", Name: name + " Loose", StreamURL: "http://streams.example/" + name})
		if _, err := s.ApplyCatalog(context.Background(), ids[name], chs, at); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// A lineup started empty, with a group that takes categories of three
// sources, holds exactly their channels; a channel that a source adds to
// one of them joins after a refresh, numbered after the lineup's last; and
// numbers stay unique (D136).
func TestLineupOfOwnGroups(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	household := lineupShown(t, s, DefaultLineupID)

	id, err := s.NewLineup(ctx, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	l, err := s.Lineup(ctx, id)
	if err != nil || !l.OwnGroups || l.Name != "Ours" {
		t.Fatalf("the lineup %+v, %v", l, err)
	}
	if got := lineupShown(t, s, id); len(got) != 0 {
		t.Errorf("a new lineup shows %v", got)
	}
	// It has an entry for each channel, without a number, so that
	// favourites can move to it with an account.
	if _, err := s.db.Exec(`
		INSERT INTO profiles (id, account_id, name) VALUES (9, 1, 'Second');
		INSERT INTO favourites (profile_id, entry_id, added_at)
			SELECT 9, e.id, '2026-12-04T03:00:00Z' FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
			WHERE e.lineup_id = 1 AND c.name = 'two News 1'`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAccountLineup(ctx, 1, id); err != nil {
		t.Fatal(err)
	}
	if favs, err := s.Favourites(ctx, 9); err != nil || len(favs) != 1 {
		t.Errorf("favourites %v, %v", favs, err)
	}
	if groups, err := s.LineupGroups(ctx, id); err != nil || len(groups) != 0 {
		t.Errorf("a new lineup's groups %+v, %v", groups, err)
	}
	cats, total, err := s.LineupCategories(ctx, id, "")
	if err != nil || total != 9 || len(cats) != 9 || cats[0].Source != "one" || cats[0].Name != "News" || cats[0].Items != 2 ||
		cats[0].GroupID != 0 {
		t.Fatalf("categories %+v, %d, %v", cats, total, err)
	}

	news, err := s.CreateLineupGroup(ctx, id, "All the news")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, news, []CategoryRef{
		{ids["one"], "News"}, {ids["two"], "News"}, {ids["three"], "News"}}); err != nil {
		t.Fatal(err)
	}
	shown := lineupShown(t, s, id)
	want := []string{"one News 1", "one News 2", "three News 1", "three News 2", "two News 1", "two News 2"}
	if got := namesOf(shown); !reflect.DeepEqual(got, want) {
		t.Errorf("shown %v", got)
	}
	numbers := map[int]bool{}
	for name, v := range shown {
		n := v[0].(int)
		if n < 1 || n > 6 || numbers[n] || v[1] != "All the news" {
			t.Errorf("%s: %v", name, v)
		}
		numbers[n] = true
	}
	if cats, _, err := s.LineupCategories(ctx, id, "NEWS"); err != nil || len(cats) != 3 || cats[1].GroupID != news ||
		cats[1].Group != "All the news" {
		t.Errorf("news %+v, %v", cats, err)
	}
	// The household's lineup is as it was.
	if got := lineupShown(t, s, DefaultLineupID); !reflect.DeepEqual(got, household) {
		t.Errorf("the household's lineup changed: %v", got)
	}

	// A source adds a channel to one of them: it joins after a refresh,
	// numbered after the last. One added to a category that no group takes
	// stays out.
	var two []CatalogChannel
	for i, cat := range []string{"News", "Sport"} {
		for j := 1; j <= 3; j++ {
			key := "two-" + cat + string(rune('0'+j))
			two = append(two, CatalogChannel{IdentityKey: key, Name: "two " + cat + " " + string(rune('0'+j)),
				Category: cat, Number: 100*(i+1) + j, StreamURL: "http://streams.example/" + key})
		}
	}
	if _, err := s.ApplyCatalog(ctx, ids["two"], two, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	shown = lineupShown(t, s, id)
	if v, ok := shown["two News 3"]; !ok || v[0] != 7 {
		t.Errorf("the new channel %v, %v", v, ok)
	}
	if _, ok := shown["two Sport 3"]; ok || len(shown) != 7 {
		t.Errorf("shown %v", namesOf(shown))
	}

	// Another group takes a category from the first, which keeps the rest;
	// "Without a category" is a category too.
	more, err := s.CreateLineupGroup(ctx, id, "More")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, more, []CategoryRef{{ids["three"], "News"}, {ids["one"], ""}}); err != nil {
		t.Fatal(err)
	}
	shown = lineupShown(t, s, id)
	if shown["three News 1"][1] != "More" || shown["one Loose"][1] != "More" || shown["one News 1"][1] != "All the news" {
		t.Errorf("after moving %v", shown)
	}
	// A channel moved there by hand stays when its category leaves.
	var entry int64
	if err := s.db.QueryRow("SELECT e.id FROM lineup_entries e JOIN channels c ON c.id = e.channel_id WHERE e.lineup_id = ? AND c.name = 'one News 2'",
		id).Scan(&entry); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesGroup(ctx, id, []int64{entry}, more); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameEntry(ctx, id, entry, "One, by hand"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, news, []CategoryRef{{ids["two"], "News"}}); err != nil {
		t.Fatal(err)
	}
	shown = lineupShown(t, s, id)
	if _, ok := shown["one News 1"]; ok {
		t.Errorf("one News 1 is still in the lineup: %v", shown)
	}
	if v := shown["One, by hand"]; v[1] != "More" {
		t.Errorf("the channel moved by hand %v", v)
	}
	entries, total, err := s.LineupEntries(ctx, id, EntryQuery{})
	if err != nil || total != len(shown) || len(entries) != total {
		t.Errorf("the admin's list: %d of %d, %v", len(entries), total, err)
	}
	// What the lineup no longer holds gives its number up.
	var numbered int
	if err := s.db.QueryRow("SELECT count(*) FROM lineup_entries e JOIN channels c ON c.id = e.channel_id WHERE e.lineup_id = ? AND c.name = 'one News 1' AND e.number IS NOT NULL",
		id).Scan(&numbered); err != nil || numbered != 0 {
		t.Errorf("one News 1 still numbered: %d, %v", numbered, err)
	}
	// Taken again, it comes back, numbered after the last.
	last := 0
	for _, v := range shown {
		last = max(last, v[0].(int))
	}
	if err := s.SetGroupCategories(ctx, id, news, []CategoryRef{{ids["two"], "News"}, {ids["one"], "News"}}); err != nil {
		t.Fatal(err)
	}
	shown = lineupShown(t, s, id)
	if v, ok := shown["one News 1"]; !ok || v[1] != "All the news" || v[0].(int) <= last {
		t.Errorf("one News 1 taken again: %v, %v", v, ok)
	}
	seen := map[int]string{}
	for name, v := range shown {
		if other, dup := seen[v[0].(int)]; dup {
			t.Errorf("%s and %s share %v", name, other, v[0])
		}
		seen[v[0].(int)] = name
	}

	// A copy holds only what its groups take, as the original.
	copied, err := s.CopyLineup(ctx, id, "Ours too")
	if err != nil {
		t.Fatal(err)
	}
	if l, err := s.Lineup(ctx, copied); err != nil || !l.OwnGroups {
		t.Errorf("the copy %+v, %v", l, err)
	}
	if got := lineupShown(t, s, copied); !reflect.DeepEqual(got, shown) {
		t.Errorf("the copy shows %v", namesOf(got))
	}
	lineups, err := s.Lineups(ctx)
	if err != nil || len(lineups) != 3 || lineups[0].OwnGroups || !lineups[1].OwnGroups {
		t.Errorf("lineups %+v, %v", lineups, err)
	}

	// Mistakes.
	if err := s.SetGroupCategories(ctx, id, news, []CategoryRef{{ids["one"], "Weather"}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a category that no source lists: %v", err)
	}
	if err := s.SetGroupCategories(ctx, DefaultLineupID, news, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("another lineup's group: %v", err)
	}
	if _, err := s.NewLineup(ctx, "Ours"); !errors.Is(err, ErrLineupExists) {
		t.Errorf("a name taken: %v", err)
	}
}

// A lineup that takes every category takes back a category that leaves a
// group, in a group of its own name, as before.
func TestGroupCategoriesOfAnEveryLineup(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	mine, err := s.CreateLineupGroup(ctx, DefaultLineupID, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, DefaultLineupID, mine, []CategoryRef{{ids["one"], "Sport"}, {ids["two"], ""}}); err != nil {
		t.Fatal(err)
	}
	shown := lineupShown(t, s, DefaultLineupID)
	if shown["one Sport 1"][1] != "Mine" || shown["two Loose"][1] != "Mine" || shown["two Sport 1"][1] != "Sport" || len(shown) != 15 {
		t.Errorf("shown %v", shown)
	}
	if err := s.SetGroupCategories(ctx, DefaultLineupID, mine, nil); err != nil {
		t.Fatal(err)
	}
	shown = lineupShown(t, s, DefaultLineupID)
	if shown["one Sport 1"][1] != "Sport" || shown["two Loose"][1] != "" || len(shown) != 15 {
		t.Errorf("shown %v", shown)
	}
}

// A new lineup can merge the copies of a channel from the start (D113).
func TestNewLineupWithAMergeRule(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	id, err := s.NewLineup(ctx, "Merged", rules.Rule{Action: rules.Merge, Copies: rules.ByName, Replace: "/^(one|two|three) /"})
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateLineupGroup(ctx, id, "News")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, g, []CategoryRef{{ids["one"], "News"}, {ids["two"], "News"}, {ids["three"], "News"}}); err != nil {
		t.Fatal(err)
	}
	// One copy of each is shown, the others are its backups.
	got := namesOf(lineupShown(t, s, id))
	if len(got) != 2 || got[0][len(got[0])-6:] != "News 1" || got[1][len(got[1])-6:] != "News 2" {
		t.Errorf("merged %v", got)
	}
	rs, err := s.LineupRules(ctx, id)
	if err != nil || len(rs) != 1 || rs[0].Rule.Action != rules.Merge {
		t.Errorf("rules %+v, %v", rs, err)
	}
	if _, err := s.NewLineup(ctx, "Bad", rules.Rule{Action: rules.Merge, Copies: "colour"}); err == nil {
		t.Error("a rule that is not one")
	}
}

func TestLineupViewers(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if v, err := s.LineupViewers(ctx, DefaultLineupID); err != nil || len(v) != 1 || v[0].Account != "Household" ||
		v[0].Boxes != 0 || v[0].Playlist || v[0].Login {
		t.Errorf("viewers %+v, %v", v, err)
	}
	if _, err := s.db.Exec(`
		INSERT INTO devices (mac, state, account_id, secret_hash) VALUES ('00:1A:79:00:00:01', 'approved', 1, 'x');
		INSERT INTO devices (mac, state, account_id) VALUES ('00:1A:79:00:00:02', 'approved', 1), ('00:1A:79:00:00:03', 'pending', 1);
		UPDATE accounts SET output_m3u = 1, token_hash = 'h', output_xtream = 1 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if v, err := s.LineupViewers(ctx, DefaultLineupID); err != nil || len(v) != 1 || v[0].Boxes != 1 || v[0].Clients != 1 ||
		!v[0].Playlist || v[0].Login {
		t.Errorf("viewers %+v, %v", v, err)
	}
	if v, err := s.LineupViewers(ctx, 99); err != nil || len(v) != 0 {
		t.Errorf("no lineup: %+v, %v", v, err)
	}
}

// A lineup of its own groups goes to a lineup file of version 2, and comes
// back as one that holds the same; a file of lineups that take every
// category stays version 1 (D112, D136).
func TestExportAndImportOwnGroups(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	if f, err := s.ExportLineups(ctx); err != nil || f.Version != 1 {
		t.Fatalf("a file of lineups that take every category: %+v, %v", f, err)
	}
	id, err := s.NewLineup(ctx, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateLineupGroup(ctx, id, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, g, []CategoryRef{{ids["one"], "News"}, {ids["two"], ""}}); err != nil {
		t.Fatal(err)
	}
	f, err := s.ExportLineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 2 || len(f.Lineups) != 2 || f.Lineups[0].OwnGroups || !f.Lineups[1].OwnGroups {
		t.Fatalf("the file %+v", f)
	}
	want := []lineupfile.Category{{Source: "one", Name: "News"}, {Source: "two", Name: ""}}
	if gs := f.Lineups[1].Groups; len(gs) != 1 || !reflect.DeepEqual(gs[0].Categories, want) {
		t.Errorf("its groups %+v", gs)
	}
	data, err := lineupfile.Marshal(f, "")
	if err != nil {
		t.Fatal(err)
	}
	back, err := lineupfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.ImportLineups(ctx, lineupfile.File{Kind: back.Kind, Version: back.Version, Lineups: back.Lineups[1:]})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Name != "Ours (2)" {
		t.Fatalf("imported %+v", res)
	}
	if l, err := s.Lineup(ctx, res[0].ID); err != nil || !l.OwnGroups {
		t.Errorf("the lineup imported %+v, %v", l, err)
	}
	if got, want := lineupShown(t, s, res[0].ID), lineupShown(t, s, id); !reflect.DeepEqual(got, want) {
		t.Errorf("imported %v, want %v", got, want)
	}
}

// A channel moved by hand into a group of a lineup of its own groups joins
// it, numbered after the last, and leaves it when moved back (D136).
func TestMoveIntoOwnGroups(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	id, err := s.NewLineup(ctx, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateLineupGroup(ctx, id, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, g, []CategoryRef{{ids["one"], "News"}}); err != nil {
		t.Fatal(err)
	}
	found, total, err := s.LineupEntries(ctx, id, EntryQuery{NotGroup: &g, Text: "loose", Untaken: true})
	if err != nil || total != 3 || !found[0].Untaken || found[0].Number != 0 {
		t.Fatalf("found %+v, %d, %v", found, total, err)
	}
	if err := s.SetEntriesGroup(ctx, id, []int64{found[0].ID}, g); err != nil {
		t.Fatal(err)
	}
	shown := lineupShown(t, s, id)
	if v, ok := shown[found[0].Name]; !ok || v[0] != 3 || v[1] != "Mine" {
		t.Errorf("moved in: %v, %v", v, ok)
	}
	if e, err := s.LineupEntry(ctx, id, found[0].ID); err != nil || e.Untaken || e.Number != 3 {
		t.Errorf("the entry %+v, %v", e, err)
	}
	if err := s.SetEntriesGroup(ctx, id, []int64{found[0].ID}, 0); err != nil {
		t.Fatal(err)
	}
	if shown := lineupShown(t, s, id); len(shown) != 2 {
		t.Errorf("moved back: %v", shown)
	}
	if _, total, err := s.LineupEntries(ctx, id, EntryQuery{}); err != nil || total != 2 {
		t.Errorf("the admin's list: %d, %v", total, err)
	}
}

// A number set by hand in a lineup of its own groups stays while the lineup
// does not hold its channel; returned to the lineup's numbering, it goes
// (D136).
func TestPinnedNumbersInOwnGroups(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	id, err := s.NewLineup(ctx, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateLineupGroup(ctx, id, "Mine")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, g, []CategoryRef{{ids["one"], "News"}}); err != nil {
		t.Fatal(err)
	}
	cs, err := s.LineupChannels(ctx, id)
	if err != nil || len(cs) != 2 {
		t.Fatalf("%+v, %v", cs, err)
	}
	if err := s.PinEntryNumber(ctx, id, cs[0].EntryID, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, g, nil); err != nil {
		t.Fatal(err)
	}
	if e, err := s.LineupEntry(ctx, id, cs[0].EntryID); err != nil || e.Number != 50 || !e.Pinned || !e.Untaken {
		t.Errorf("the pinned entry, not held: %+v, %v", e, err)
	}
	if e, err := s.LineupEntry(ctx, id, cs[1].EntryID); err != nil || e.Number != 0 {
		t.Errorf("the other: %+v, %v", e, err)
	}
	if err := s.ResetEntryNumber(ctx, id, cs[0].EntryID); err != nil {
		t.Fatal(err)
	}
	if e, err := s.LineupEntry(ctx, id, cs[0].EntryID); err != nil || e.Number != 0 || e.Pinned {
		t.Errorf("returned: %+v, %v", e, err)
	}
	// Taken again, they are numbered after the last, as at first.
	if err := s.SetGroupCategories(ctx, id, g, []CategoryRef{{ids["one"], "News"}}); err != nil {
		t.Fatal(err)
	}
	if got := lineupShown(t, s, id); len(got) != 2 || got["one News 1"][0] != 1 || got["one News 2"][0] != 2 {
		t.Errorf("taken again: %v", got)
	}
}
