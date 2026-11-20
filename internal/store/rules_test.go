// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/rules"
)

// addRule adds a rule to the first lineup and returns its ID.
func addRule(t *testing.T, s *Store, r rules.Rule) int64 {
	t.Helper()
	id, err := s.AddLineupRule(context.Background(), DefaultLineupID, r)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Rules show, hide, rename, regroup and give logos; a later rule wins over
// an earlier one; the administrator's edits win over both; and what a
// rule did goes when the rule goes (D107).
func TestRules(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("ZZ: Alpha HD/News", "ZZ: Bravo/News", "Charlie HD/Sport", "Delta/Sport"))

	hideAll := addRule(t, s, rules.Rule{Action: rules.Exclude})
	if got := shown(t, s); len(got) != 0 {
		t.Errorf("after excluding everything: %q", got)
	}
	addRule(t, s, rules.Rule{Action: rules.Include, Match: rules.Match{Group: "news"}})
	addRule(t, s, rules.Rule{Action: rules.Rename, Replace: "ZZ: "})
	addRule(t, s, rules.Rule{Action: rules.Rename, Replace: " HD"})
	addRule(t, s, rules.Rule{Action: rules.Regroup, Group: "Headlines", Match: rules.Match{Quality: "HD"}})
	addRule(t, s, rules.Rule{Action: rules.Logo, Logo: "https://logos.example/alpha.png", Match: rules.Match{Name: "*alpha*"}})
	want := []string{"1 Alpha Headlines", "2 Bravo News"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("devices see %q, want %q", got, want)
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	if cs[0].Logo != "https://logos.example/alpha.png" || cs[1].Logo != "" {
		t.Errorf("logos: %q, %q", cs[0].Logo, cs[1].Logo)
	}
	if e := entryOf(t, s, "ZZ: Alpha HD"); !e.RuleRenamed || !e.RuleMoved || !e.RuleShown || !e.RuleLogo || e.Renamed || e.Moved {
		t.Errorf("Alpha's marks: %+v", e)
	}
	if e := entryOf(t, s, "Delta"); !e.RuleHidden || !e.Hidden {
		t.Errorf("Delta's marks: %+v", e)
	}

	// The administrator's edits win.
	if err := s.RenameEntry(ctx, DefaultLineupID, entryOf(t, s, "ZZ: Alpha HD").ID, "Alpha One"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{entryOf(t, s, "Delta").ID}, Shown); err != nil {
		t.Fatal(err)
	}
	want = []string{"1 Alpha One Headlines", "2 Bravo News", "4 Delta Sport"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("with the administrator's edits, devices see %q, want %q", got, want)
	}

	// A refresh keeps what the rules do, and they reach a new channel.
	apply(t, s, src, at.Add(time.Hour), grouped("ZZ: Alpha HD/News", "ZZ: Bravo/News", "Charlie HD/Sport", "Delta/Sport", "ZZ: Echo/News"))
	want = []string{"1 Alpha One Headlines", "2 Bravo News", "4 Delta Sport", "5 Echo News"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("after a refresh, devices see %q, want %q", got, want)
	}

	// Without the first rule, nothing is hidden by a rule any more.
	if err := s.RemoveLineupRule(ctx, DefaultLineupID, hideAll); err != nil {
		t.Fatal(err)
	}
	want = []string{"1 Alpha One Headlines", "2 Bravo News", "3 Charlie Headlines", "4 Delta Sport", "5 Echo News"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("without the first rule, devices see %q, want %q", got, want)
	}
	list, err := s.LineupRules(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 || list[0].Position != 1 || list[0].Action != rules.Include || list[0].Matches != 3 || list[3].Matches != 2 {
		t.Errorf("rules: %+v", list)
	}

	// An off rule does nothing; every rule gone, every mark goes, and so
	// does the group that only a rule made.
	r := list[3].Rule
	r.Off = true
	if err := s.SetLineupRule(ctx, DefaultLineupID, list[3].ID, r); err != nil {
		t.Fatal(err)
	}
	if got := shown(t, s); got[2] != "3 Charlie Sport" {
		t.Errorf("with the regroup off: %q", got)
	}
	for _, r := range list {
		if err := s.RemoveLineupRule(ctx, DefaultLineupID, r.ID); err != nil {
			t.Fatal(err)
		}
	}
	want = []string{"1 Alpha One News", "2 ZZ: Bravo News", "3 Charlie HD Sport", "4 Delta Sport", "5 ZZ: Echo News"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("without rules, devices see %q, want %q", got, want)
	}
	gs, err := s.LineupGroups(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Name == "Headlines" {
			t.Errorf("the rule's group is still there: %+v", g)
		}
	}
	var marks int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM lineup_entries WHERE rule_hidden IS NOT NULL OR rule_name IS NOT NULL
		OR rule_group_id IS NOT NULL OR rule_logo IS NOT NULL OR rule_pinned = 1`).Scan(&marks); err != nil || marks != 0 {
		t.Errorf("marks left: %d, %v", marks, err)
	}
}

// A renumbering rule numbers its channels from its number, skipping the
// numbers that the administrator pinned; a channel in its way goes after
// the highest; the numbers stay over refreshes, and a new channel that the
// rule matches joins them.
func TestRenumberingRules(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/Sport", "c/News", "d/Sport", "e/Sport"))
	if err := s.PinEntryNumber(ctx, DefaultLineupID, entryOf(t, s, "a").ID, 11); err != nil {
		t.Fatal(err)
	}
	// e holds 5; it is in Sport, which goes from 4: b 4, d 5 (e moves), e 6.
	addRule(t, s, rules.Rule{Action: rules.Renumber, From: 4, Match: rules.Match{Group: "Sport"}})
	want := []string{"3 c News", "4 b Sport", "5 d Sport", "6 e Sport", "11 a News"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("devices see %q, want %q", got, want)
	}
	// From 10: 10 b, 11 is the administrator's, so 12 d and 13 e.
	list, err := s.LineupRules(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	r := list[0].Rule
	r.From = 10
	if err := s.SetLineupRule(ctx, DefaultLineupID, list[0].ID, r); err != nil {
		t.Fatal(err)
	}
	want = []string{"3 c News", "10 b Sport", "11 a News", "12 d Sport", "13 e Sport"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("from 10, devices see %q, want %q", got, want)
	}
	if !entryOf(t, s, "b").RuleNumbered || entryOf(t, s, "c").RuleNumbered {
		t.Errorf("marks: b %+v, c %+v", entryOf(t, s, "b"), entryOf(t, s, "c"))
	}
	// A refresh with a new Sport channel, which wants number 6.
	apply(t, s, src, at.Add(time.Hour), grouped("a/News", "b/Sport", "c/News", "d/Sport", "e/Sport", "f/Sport"))
	want = []string{"3 c News", "10 b Sport", "11 a News", "12 d Sport", "13 e Sport", "14 f Sport"}
	if got := shown(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("after a refresh, devices see %q, want %q", got, want)
	}
	// A new source numbering setting leaves the rule's numbers alone.
	settings, err := s.SourceSettings(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	settings.Numbers = NumbersProvider
	if err := s.UpdateSource(ctx, src, settings); err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]int{"a": 11, "b": 10, "d": 12, "e": 13, "f": 14} {
		if e := entryOf(t, s, name); e.Number != n {
			t.Errorf("after the source's numbers changed, %s is %d, want %d", name, e.Number, n)
		}
	}
	// Without the rule, the numbers stay, no longer pinned.
	if err := s.RemoveLineupRule(ctx, DefaultLineupID, list[0].ID); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "b"); e.Number != 10 || e.RuleNumbered {
		t.Errorf("b without the rule: %+v", e)
	}
}

func TestRuleChecksAndOrder(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News"))
	var bad *rules.Error
	if _, err := s.AddLineupRule(ctx, DefaultLineupID, rules.Rule{Action: "explode"}); !errors.As(err, &bad) {
		t.Errorf("a wrong rule: %v", err)
	}
	if _, err := s.AddLineupRule(ctx, 999, rules.Rule{Action: rules.Exclude}); !errors.Is(err, ErrNotFound) {
		t.Errorf("a rule of no lineup: %v", err)
	}
	one := addRule(t, s, rules.Rule{Action: rules.Rename, Name: "One"})
	two := addRule(t, s, rules.Rule{Action: rules.Rename, Name: "Two"})
	if got := shown(t, s); got[0] != "1 Two News" {
		t.Errorf("the later rule does not win: %q", got)
	}
	if err := s.MoveLineupRule(ctx, DefaultLineupID, two, 1); err != nil {
		t.Fatal(err)
	}
	if got := shown(t, s); got[0] != "1 One News" {
		t.Errorf("after the move: %q", got)
	}
	list, _ := s.LineupRules(ctx, DefaultLineupID)
	if len(list) != 2 || list[0].ID != two || list[1].ID != one || list[1].Position != 2 {
		t.Errorf("order: %+v", list)
	}
	if err := s.MoveLineupRule(ctx, DefaultLineupID, 999, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("moving no rule: %v", err)
	}
	if err := s.SetLineupRule(ctx, DefaultLineupID, 999, rules.Rule{Action: rules.Exclude}); !errors.Is(err, ErrNotFound) {
		t.Errorf("changing no rule: %v", err)
	}
	// A copy of the lineup has the rules, which run on it.
	kids, err := s.CopyLineup(ctx, DefaultLineupID, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	copied, _ := s.LineupRules(ctx, kids)
	if len(copied) != 2 || copied[1].Name != "One" {
		t.Errorf("the copy's rules: %+v", copied)
	}
	if got := shownIn(t, s, kids); got[0] != "1 One News" {
		t.Errorf("the copy shows %q", got)
	}
	for i := 2; i < rules.MaxRules; i++ {
		addRule(t, s, rules.Rule{Action: rules.Include})
	}
	if _, err := s.AddLineupRule(ctx, DefaultLineupID, rules.Rule{Action: rules.Include}); !errors.Is(err, ErrTooManyRules) {
		t.Errorf("past the most rules: %v", err)
	}
}

// A rule moves channels to the lineup's group of the name it gives,
// whatever its case, rather than make a second one.
func TestRuleGroupWhateverItsCase(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("Alpha/News", "Bravo/Sport"))
	id, err := s.CreateLineupGroup(ctx, DefaultLineupID, "Children")
	if err != nil {
		t.Fatal(err)
	}
	addRule(t, s, rules.Rule{Action: rules.Regroup, Group: "children", Match: rules.Match{Name: "alpha"}})
	if e := entryOf(t, s, "Alpha"); e.GroupID != id || e.Group != "Children" || !e.RuleMoved {
		t.Errorf("Alpha: %+v", e)
	}
	gs, err := s.LineupGroups(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range gs {
		if g.Name == "children" {
			t.Errorf("a second group: %+v", gs)
		}
	}
}

// A rule gives a channel another channel's guide and another moves its
// times, and every way of reading the guide shows them so (D108).
func TestGuideRules(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("Alpha/News", "Alpha +1/News", "Bravo/Sport"))
	alpha, plus := entryOf(t, s, "Alpha"), entryOf(t, s, "Alpha +1")
	day := archiveDay
	writeGuide(t, s, src, day, 0, []int64{alpha.ChannelID}, day, day.Add(time.Hour))
	window := func(id int64) []string {
		t.Helper()
		ps, err := s.EntryProgrammes(ctx, DefaultLineupID, id, day, day.Add(3*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return starts(ps)
	}
	if got := window(plus.ID); len(got) != 0 {
		t.Fatalf("Alpha +1 before any rule: %v", got)
	}

	plusOnes := rules.Match{Name: "* +1"}
	addRule(t, s, rules.Rule{Action: rules.Guide, Replace: "+1", Match: plusOnes})
	shift := addRule(t, s, rules.Rule{Action: rules.Shift, Minutes: 60, Match: plusOnes})
	if e := entryOf(t, s, "Alpha +1"); e.RuleGuide != "Alpha" || e.RuleShift != time.Hour {
		t.Errorf("Alpha +1's entry: %+v", e)
	}
	if e := entryOf(t, s, "Alpha"); e.RuleGuide != "" || e.RuleShift != 0 {
		t.Errorf("Alpha's entry: %+v", e)
	}
	want := map[int64][]string{alpha.ID: {"18:00 On at 18:00", "19:00 On at 19:00"}, plus.ID: {"19:00 On at 18:00", "20:00 On at 19:00"}}
	for id, w := range want {
		if got := window(id); !reflect.DeepEqual(got, w) {
			t.Errorf("EntryProgrammes(%d) = %v, want %v", id, got, w)
		}
	}
	all, err := s.LineupProgrammes(ctx, DefaultLineupID, day, day.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for id, w := range want {
		if got := starts(all[id]); !reflect.DeepEqual(got, w) {
			t.Errorf("LineupProgrammes[%d] = %v, want %v", id, got, w)
		}
	}
	// A window that the shift alone brings a programme into.
	late, err := s.LineupProgrammes(ctx, DefaultLineupID, day.Add(150*time.Minute), day.Add(4*time.Hour))
	if err != nil || len(late[alpha.ID]) != 0 || !reflect.DeepEqual(starts(late[plus.ID]), []string{"20:00 On at 19:00"}) {
		t.Errorf("from 20:30: %v, %v", late, err)
	}
	each := map[int64][]string{}
	err = s.EachLineupProgramme(ctx, DefaultLineupID, day, day.Add(3*time.Hour), func(id int64, p Programme) error {
		each[id] = append(each[id], p.Start.Format("15:04")+" "+p.Title)
		return nil
	})
	if err != nil || !reflect.DeepEqual(each, want) {
		t.Errorf("EachLineupProgramme: %v, %v", each, err)
	}
	if p, err := s.ProgrammeAt(ctx, DefaultLineupID, plus.ID, day.Add(time.Hour)); err != nil || p.Title != "On at 18:00" ||
		!p.Stop.Equal(day.Add(2*time.Hour)) {
		t.Errorf("ProgrammeAt: %+v, %v", p, err)
	}
	found, err := s.SearchProgrammes(ctx, DefaultLineupID, "on at 18", day, day.Add(3*time.Hour), 10)
	if err != nil || len(found) != 2 || found[0].EntryID != alpha.ID || found[1].EntryID != plus.ID ||
		!found[1].Start.Equal(day.Add(time.Hour)) {
		t.Errorf("SearchProgrammes: %+v, %v", found, err)
	}

	// Off, the shift goes; the guide stays its own channel's.
	r, err := s.LineupRules(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	r[1].Off = true
	if err := s.SetLineupRule(ctx, DefaultLineupID, shift, r[1].Rule); err != nil {
		t.Fatal(err)
	}
	if got := window(plus.ID); !reflect.DeepEqual(got, want[alpha.ID]) {
		t.Errorf("Alpha +1 without the shift: %v", got)
	}
	// A rule that names no channel of the lineup does nothing.
	addRule(t, s, rules.Rule{Action: rules.Guide, Channel: "Nobody", Match: rules.Match{Name: "Bravo"}})
	if e := entryOf(t, s, "Bravo"); e.RuleGuide != "" {
		t.Errorf("Bravo: %+v", e)
	}
}

// starts returns programmes as "15:04 title".
func starts(ps []Programme) []string {
	out := []string{}
	for _, p := range ps {
		out = append(out, p.Start.Format("15:04")+" "+p.Title)
	}
	return out
}
