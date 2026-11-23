// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"overscan.gureet.ca/internal/rules"
)

// groupOrder returns the names of the channels that a lineup shows in a
// group, by number, with their numbers.
func groupOrder(t *testing.T, s *Store, lineupID int64, group string) ([]string, []int) {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), lineupID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var numbers []int
	for _, c := range cs {
		if c.Group == group {
			names = append(names, c.Name)
			numbers = append(numbers, c.Number)
		}
	}
	return names, numbers
}

// uniqueNumbers fails the test if two entries of a lineup hold one number.
func uniqueNumbers(t *testing.T, s *Store, lineupID int64) {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM (SELECT number FROM lineup_entries WHERE lineup_id = ? AND number IS NOT NULL
		GROUP BY number HAVING count(*) > 1)`, lineupID).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d numbers held twice, %v", n, err)
	}
}

// groupID returns the ID of a lineup's group of that name.
func groupID(t *testing.T, s *Store, lineupID int64, name string) int64 {
	t.Helper()
	groups, err := s.LineupGroups(context.Background(), lineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.Name == name {
			return g.ID
		}
	}
	t.Fatalf("no group %s", name)
	return 0
}

// news is a source's News channels and one more, as it lists them.
func news(name string, n int) []CatalogChannel {
	var out []CatalogChannel
	for j := 1; j <= n; j++ {
		key := name + "-News" + string(rune('0'+j))
		out = append(out, CatalogChannel{IdentityKey: key, Name: name + " News " + string(rune('0'+j)), Category: "News",
			Number: 100 + j, StreamURL: "http://streams.example/" + key})
	}
	for j := 1; j <= 2; j++ {
		key := name + "-Sport" + string(rune('0'+j))
		out = append(out, CatalogChannel{IdentityKey: key, Name: name + " Sport " + string(rune('0'+j)), Category: "Sport",
			Number: 200 + j, StreamURL: "http://streams.example/" + key})
	}
	return out
}

// A group's first number numbers its channels from it, in their order, and
// gives a channel that joins it the next free number after its last;
// numbers set by hand stay, other groups' numbers are skipped, and a
// channel hidden on its own gives its number up (D137).
func TestGroupNumbersFrom(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	l := int64(DefaultLineupID)
	newsID := groupID(t, s, l, "News")
	before, _ := groupOrder(t, s, l, "News")
	if len(before) != 6 {
		t.Fatalf("News %v", before)
	}

	if err := s.SetGroupNumbersFrom(ctx, l, newsID, 100); err != nil {
		t.Fatal(err)
	}
	names, numbers := groupOrder(t, s, l, "News")
	if !reflect.DeepEqual(names, before) || !reflect.DeepEqual(numbers, []int{100, 101, 102, 103, 104, 105}) {
		t.Errorf("numbered from 100: %v %v", names, numbers)
	}
	if g, _ := s.LineupGroups(ctx, l); g[slices.IndexFunc(g, func(g LineupGroup) bool { return g.ID == newsID })].NumbersFrom != 100 {
		t.Errorf("the group's first number: %+v", g)
	}
	uniqueNumbers(t, s, l)

	// A number set by hand stays; a channel that joins takes the next.
	cs, _ := s.LineupChannels(ctx, l)
	var second int64
	for _, c := range cs {
		if c.Name == before[1] {
			second = c.EntryID
		}
	}
	if err := s.PinEntryNumber(ctx, l, second, 900); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["two"], news("two", 3), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	names, numbers = groupOrder(t, s, l, "News")
	if names[len(names)-1] != before[1] || numbers[len(numbers)-1] != 900 {
		t.Errorf("the pinned channel: %v %v", names, numbers)
	}
	if i := slices.Index(names, "two News 3"); i < 0 || numbers[i] != 106 {
		t.Errorf("the new channel: %v %v", names, numbers)
	}
	uniqueNumbers(t, s, l)

	// Hidden on its own, a channel gives its number up; shown again, it
	// takes the next.
	first := cs[slices.IndexFunc(cs, func(c LineupChannel) bool { return c.Name == before[0] })].EntryID
	if err := s.SetEntriesVisibility(ctx, l, []int64{first}, Hidden); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.LineupEntry(ctx, l, first); e.Number != 0 {
		t.Errorf("hidden, it holds %d", e.Number)
	}
	if err := s.SetEntriesVisibility(ctx, l, []int64{first}, AsGroup); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.LineupEntry(ctx, l, first); e.Number != 107 {
		t.Errorf("shown again, it holds %d", e.Number)
	}

	// Another group numbered into the same numbers skips those held.
	sport := groupID(t, s, l, "Sport")
	if err := s.SetGroupNumbersFrom(ctx, l, sport, 104); err != nil {
		t.Fatal(err)
	}
	_, sn := groupOrder(t, s, l, "Sport")
	if len(sn) != 6 || sn[0] != 108 {
		t.Errorf("Sport from 104: %v", sn)
	}
	uniqueNumbers(t, s, l)

	// A channel moved by hand into the group joins it; moved out, it
	// keeps its number.
	sp, _ := s.LineupChannels(ctx, l)
	moved := sp[slices.IndexFunc(sp, func(c LineupChannel) bool { return c.Group == "Sport" })]
	if err := s.SetEntriesGroup(ctx, l, []int64{moved.EntryID}, newsID); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.LineupEntry(ctx, l, moved.EntryID); e.Number != 108 || e.GroupID != newsID {
		t.Errorf("moved into News: %+v", e)
	}
	if _, sn := groupOrder(t, s, l, "Sport"); sn[0] != 109 {
		t.Errorf("Sport after one left: %v", sn)
	}
	uniqueNumbers(t, s, l)

	// Without a first number, the channels keep theirs, and a newcomer is
	// numbered as before (D40).
	if err := s.SetGroupNumbersFrom(ctx, l, newsID, 0); err != nil {
		t.Fatal(err)
	}
	keep, kn := groupOrder(t, s, l, "News")
	if _, err := s.ApplyCatalog(ctx, ids["two"], news("two", 4), at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, an := groupOrder(t, s, l, "News")
	for i, name := range keep {
		if j := slices.Index(after, name); j < 0 || an[j] != kn[i] {
			t.Errorf("%s moved from %d", name, kn[i])
		}
	}
	// It wants its source's number, 104, which another holds: the next
	// free number after the highest.
	if i := slices.Index(after, "two News 4"); i < 0 || an[i] != 901 {
		t.Errorf("the newcomer without a first number: %v %v", after, an)
	}
	uniqueNumbers(t, s, l)

	if err := s.SetGroupNumbersFrom(ctx, l, newsID, MaxNumber+1); !errors.Is(err, ErrBadNumber) {
		t.Errorf("too high: %v", err)
	}
	if err := s.SetGroupNumbersFrom(ctx, l, 9999, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such group: %v", err)
	}
}

// The Done when's case: in a lineup of its own groups, a group with a
// first number takes categories of three sources, and a channel that a
// source adds to one of them is numbered after the group's last (D136,
// D137).
func TestOwnGroupNumbersFrom(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	id, err := s.NewLineup(ctx, "Ours")
	if err != nil {
		t.Fatal(err)
	}
	sport, err := s.CreateLineupGroup(ctx, id, "Sport")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.CreateLineupGroup(ctx, id, "All the news")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, sport, []CategoryRef{{ids["one"], "Sport"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupNumbersFrom(ctx, id, g, 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupCategories(ctx, id, g, []CategoryRef{{ids["one"], "News"}, {ids["two"], "News"}, {ids["three"], "News"}}); err != nil {
		t.Fatal(err)
	}
	_, numbers := groupOrder(t, s, id, "All the news")
	if !reflect.DeepEqual(numbers, []int{100, 101, 102, 103, 104, 105}) {
		t.Errorf("the news %v", numbers)
	}
	if _, sn := groupOrder(t, s, id, "Sport"); !reflect.DeepEqual(sn, []int{1, 2}) {
		t.Errorf("sport %v", sn)
	}
	// A channel that a source adds to Sport, which has no first number,
	// takes the number after the lineup's highest; one added to the news,
	// the number after the news's last.
	if _, err := s.ApplyCatalog(ctx, ids["one"], news("one", 3), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	names, numbers := groupOrder(t, s, id, "All the news")
	if i := slices.Index(names, "one News 3"); i < 0 || numbers[i] != 106 {
		t.Errorf("the new channel %v %v", names, numbers)
	}
	uniqueNumbers(t, s, id)
}

// A group's channels sort by name in natural order, on the numbers that
// they hold between them; numbers set by hand and hidden channels stay
// (D137).
func TestSortGroupByName(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := addSources(t, s, testSource("main", 1, NumbersPlaylist))
	var chs []CatalogChannel
	for _, name := range []string{"Channel 10", "channel 2", "Channel 1", "Bravo", "Channel 3", "Alpha"} {
		chs = append(chs, CatalogChannel{IdentityKey: name, Name: name, Category: "Mixed", StreamURL: "http://streams.example/" + name})
	}
	if _, err := s.ApplyCatalog(ctx, ids["main"], chs, at); err != nil {
		t.Fatal(err)
	}
	l := int64(DefaultLineupID)
	mixed := groupID(t, s, l, "Mixed")
	cs, _ := s.LineupChannels(ctx, l)
	byName := map[string]int64{}
	for _, c := range cs {
		byName[c.Name] = c.EntryID
	}
	if err := s.PinEntryNumber(ctx, l, byName["Bravo"], 50); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesVisibility(ctx, l, []int64{byName["Channel 3"]}, Hidden); err != nil {
		t.Fatal(err)
	}
	n, err := s.SortGroupByName(ctx, l, mixed)
	if err != nil {
		t.Fatal(err)
	}
	names, numbers := groupOrder(t, s, l, "Mixed")
	if want := []string{"Alpha", "Channel 1", "channel 2", "Channel 10", "Bravo"}; !reflect.DeepEqual(names, want) ||
		!reflect.DeepEqual(numbers, []int{1, 2, 3, 6, 50}) || n != 4 {
		t.Errorf("sorted %v %v, %d moved", names, numbers, n)
	}
	if e, _ := s.LineupEntry(ctx, l, byName["Channel 3"]); e.Number != 5 {
		t.Errorf("the hidden channel moved to %d", e.Number)
	}
	uniqueNumbers(t, s, l)
	if _, err := s.SortGroupByName(ctx, l, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such group: %v", err)
	}
}

// A merge rule shows the copy of the source that it puts first (D137);
// a source removed leaves its order, and the rule stays.
func TestMergePrefersSources(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	l := int64(DefaultLineupID)
	rule := rules.Rule{Action: rules.Merge, Copies: rules.ByName, Replace: "/^(one|two|three) /",
		Sources: []int64{ids["three"], ids["two"]}}
	if _, err := s.AddLineupRule(ctx, l, rule); err != nil {
		t.Fatal(err)
	}
	names, _ := groupOrder(t, s, l, "News")
	if !reflect.DeepEqual(names, []string{"three News 1", "three News 2"}) {
		t.Errorf("merged %v", names)
	}
	if err := s.DeleteSource(ctx, ids["three"]); err != nil {
		t.Fatal(err)
	}
	rs, err := s.LineupRules(ctx, l)
	if err != nil || len(rs) != 1 || !reflect.DeepEqual(rs[0].Rule.Sources, []int64{ids["two"]}) {
		t.Fatalf("the rule after its source went: %+v, %v", rs, err)
	}
	if names, _ := groupOrder(t, s, l, "News"); !reflect.DeepEqual(names, []string{"two News 1", "two News 2"}) {
		t.Errorf("merged %v", names)
	}
	bad := rule
	bad.Sources = []int64{ids["two"], ids["two"]}
	var rerr *rules.Error
	if _, err := s.AddLineupRule(ctx, l, bad); !errors.As(err, &rerr) {
		t.Errorf("a source twice: %v", err)
	}
	bad.Sources = []int64{9999}
	if _, err := s.AddLineupRule(ctx, l, bad); !errors.As(err, &rerr) {
		t.Errorf("no such source: %v", err)
	}
}

// Groups can swap names in one change; a change that would give two the
// same name changes nothing (D137).
func TestSetLineupGroupNames(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	threeSources(t, s, at)
	l := int64(DefaultLineupID)
	news, sport := groupID(t, s, l, "News"), groupID(t, s, l, "Sport")
	if err := s.SetLineupGroupNames(ctx, l, map[int64]string{news: "Sport", sport: "News"}); err != nil {
		t.Fatal(err)
	}
	if groupID(t, s, l, "Sport") != news || groupID(t, s, l, "News") != sport {
		t.Error("the names did not swap")
	}
	if err := s.SetLineupGroupNames(ctx, l, map[int64]string{news: "News"}); !errors.Is(err, ErrGroupExists) {
		t.Errorf("a name taken: %v", err)
	}
	if groupID(t, s, l, "Sport") != news {
		t.Error("a refused change changed a name")
	}
	if err := s.SetLineupGroupNames(ctx, l, map[int64]string{9999: "X"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such group: %v", err)
	}
	// The groups keep their categories: a refresh adds no group of the
	// old names.
	if _, err := s.ApplyCatalog(ctx, ids(t, s)["one"], news2(), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	groups, _ := s.LineupGroups(ctx, l)
	if len(groups) != 3 || groups[2].ID != 0 { // and the channels in no group
		t.Errorf("groups after a refresh: %+v", groups)
	}
}

// ids returns the sources' IDs by name.
func ids(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	all, err := s.Sources(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, src := range all {
		out[src.Name] = src.ID
	}
	return out
}

// news2 is source one's channels with a News channel more.
func news2() []CatalogChannel { return news("one", 3) }

// Shelves are renamed at once, and given back their sources' names with "".
func TestRenameShelves(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	src := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, src["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameShelves(ctx, KindMovie, map[string]string{"name:Drama": "Dramas", "name:Comedy": " Comedies "}); err != nil {
		t.Fatal(err)
	}
	if got, want := adminShelves(t, s, KindMovie), []string{"Dramas name:Drama main renamed", "Comedies name:Comedy main renamed"}; !reflect.DeepEqual(got, want) {
		t.Errorf("renamed %q, want %q", got, want)
	}
	if err := s.RenameShelves(ctx, KindMovie, map[string]string{"name:Drama": "", "name:Comedy": "Comedy"}); err != nil {
		t.Fatal(err)
	}
	if got, want := adminShelves(t, s, KindMovie), []string{"Drama name:Drama main", "Comedy name:Comedy main"}; !reflect.DeepEqual(got, want) {
		t.Errorf("given back %q, want %q", got, want)
	}
	if err := s.RenameShelves(ctx, KindMovie, map[string]string{"name:Drama": "a\tb"}); !errors.Is(err, ErrBadName) {
		t.Errorf("a bad name: %v", err)
	}
}

// A lineup file holds a group's first number and a merge's order of
// sources, by name, in version 2 (D112, D137).
func TestExportAndImportGroupNumbers(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	src := threeSources(t, s, at)
	l := int64(DefaultLineupID)
	if err := s.SetGroupNumbersFrom(ctx, l, groupID(t, s, l, "News"), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddLineupRule(ctx, l, rules.Rule{Action: rules.Merge, Copies: rules.ByGuide,
		Sources: []int64{src["two"], src["one"]}}); err != nil {
		t.Fatal(err)
	}
	f, err := s.ExportLineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Version != 2 || len(f.Lineups) != 1 || len(f.Lineups[0].Rules) != 1 ||
		!reflect.DeepEqual(f.Lineups[0].Rules[0].SourceNames, []string{"two", "one"}) || f.Lineups[0].Rules[0].Sources != nil {
		t.Fatalf("the file %+v", f)
	}
	var from int
	for _, g := range f.Lineups[0].Groups {
		if g.Name == "News" {
			from = g.NumbersFrom
		}
	}
	if from != 100 {
		t.Errorf("News's first number in the file: %d", from)
	}
	f.Lineups[0].Rules[0].SourceNames = []string{"gone", "two", "one"}
	res, err := s.ImportLineups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if _, numbers := groupOrder(t, s, res[0].ID, "News"); !reflect.DeepEqual(numbers, []int{100, 101, 102, 103, 104, 105}) {
		t.Errorf("the imported News %v", numbers)
	}
	rs, err := s.LineupRules(ctx, res[0].ID)
	if err != nil || len(rs) != 1 || !reflect.DeepEqual(rs[0].Rule.Sources, []int64{src["two"], src["one"]}) {
		t.Errorf("the imported rule %+v, %v", rs, err)
	}
}
