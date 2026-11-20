// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/lineupfile"
	"overscan.gureet.ca/internal/rules"
)

// A lineup exported to a file and imported again is the same lineup, as
// a new one; what the file names and the server lacks is left out.
func TestExportAndImportLineups(t *testing.T) {
	s, main, at := lineupSetup(t)
	ctx := context.Background()
	one := addSources(t, s, testSource("one", 1, NumbersPlaylist))["one"]
	apply(t, s, main, at, grouped("a/News", "b/News", "c/Sport", "d/Film", "e/Kids"))
	apply(t, s, one, at, grouped("x/News", "y/Late"))

	// The administrator's edits.
	if err := s.RenameLineupGroup(ctx, DefaultLineupID, groupOf(t, s, "News").ID, "Headlines"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLineupGroupHidden(ctx, DefaultLineupID, groupOf(t, s, "Film").ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLineupGroupLocked(ctx, DefaultLineupID, groupOf(t, s, "Late").ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameEntry(ctx, DefaultLineupID, entryOf(t, s, "b").ID, "Bravo"); err != nil {
		t.Fatal(err)
	}
	if err := s.PinEntryNumber(ctx, DefaultLineupID, entryOf(t, s, "c").ID, 30); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesLocked(ctx, DefaultLineupID, []int64{entryOf(t, s, "a").ID}, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{entryOf(t, s, "d").ID}, Shown); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesGroup(ctx, DefaultLineupID, []int64{entryOf(t, s, "e").ID}, groupOf(t, s, "Sport").ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntryBackups(ctx, DefaultLineupID, entryOf(t, s, "a").ID, []int64{entryOf(t, s, "x").ID}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []rules.Rule{
		{Match: rules.Match{Source: one, Name: "x"}, Action: rules.Rename, Name: "Xray"},
		{Match: rules.Match{Name: "y"}, Action: rules.Renumber, From: 500},
	} {
		if _, err := s.AddLineupRule(ctx, DefaultLineupID, r); err != nil {
			t.Fatal(err)
		}
	}
	want := shownIn(t, s, DefaultLineupID)

	f, err := s.ExportLineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Lineups) != 1 {
		t.Fatalf("exported %+v", f)
	}
	l := f.Lineups[0]
	var groups []string
	for _, g := range l.Groups {
		groups = append(groups, g.Name)
	}
	if !reflect.DeepEqual(groups, []string{"Film", "Headlines", "Late", "Sport"}) {
		t.Errorf("exported the groups %q", groups)
	}
	var keys []string
	for _, c := range l.Channels {
		keys = append(keys, c.Source+" "+c.Key)
	}
	if !reflect.DeepEqual(keys, []string{"main a", "main b", "main c", "main d", "main e"}) {
		t.Errorf("exported the channels %q", keys)
	}
	if c := l.Channels[0]; !c.Locked || len(c.Backups) != 1 || c.Backups[0] != (lineupfile.Ref{Source: "one", Key: "x"}) {
		t.Errorf("exported a as %+v", c)
	}
	if c := l.Channels[2]; c.Number != 30 || c.Title != "c" {
		t.Errorf("exported c as %+v", c)
	}
	if c := l.Channels[3]; c.Hidden == nil || *c.Hidden {
		t.Errorf("exported d as %+v", c)
	}
	if len(l.Rules) != 2 || l.Rules[0].Match.SourceName != "one" || l.Rules[0].Match.Source != 0 {
		t.Errorf("exported the rules %+v", l.Rules)
	}

	// Through a file, and back as a new lineup.
	data, err := lineupfile.Marshal(f, "A test.")
	if err != nil {
		t.Fatal(err)
	}
	read, err := lineupfile.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.ImportLineups(ctx, read)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Default (2)" || got[0].Groups != 4 || got[0].Channels != 5 || got[0].Rules != 2 ||
		got[0].LeftOut.Total() != 0 {
		t.Fatalf("imported %+v", got)
	}
	copied := got[0].ID
	if shows := shownIn(t, s, copied); !reflect.DeepEqual(shows, want) {
		t.Errorf("the import shows\n%q\nwant\n%q", shows, want)
	}
	if e := entryIn(t, s, copied, "a"); !e.Locked {
		t.Errorf("the import's a is not locked: %+v", e)
	}
	if e := entryIn(t, s, copied, "c"); !e.Pinned || e.Number != 30 {
		t.Errorf("the import's c is not pinned at 30: %+v", e)
	}
	backups, err := s.EntryBackups(ctx, copied, entryIn(t, s, copied, "a").ID)
	if err != nil || len(backups) != 1 || backups[0].Source != "one" {
		t.Errorf("the import's a has the backups %+v, %v", backups, err)
	}
	if again, err := s.ExportLineups(ctx); err != nil || !reflect.DeepEqual(again.Lineups[1].Rules, l.Rules) ||
		!reflect.DeepEqual(again.Lineups[1].Channels, l.Channels) || !reflect.DeepEqual(again.Lineups[1].Groups, l.Groups) {
		t.Errorf("the import exports as\n%+v\nwant\n%+v (%v)", again.Lineups[1], l, err)
	}

	// What names a source or a channel that the server lacks is left out.
	l.Name = "Elsewhere"
	l.Groups[1].Categories = append(l.Groups[1].Categories, lineupfile.Category{Source: "gone", Name: "News"})
	l.Channels = append(l.Channels, lineupfile.Channel{Ref: lineupfile.Ref{Source: "main", Key: "zz"}, Name: "Nowhere"})
	l.Channels[0].Backups = append(l.Channels[0].Backups, lineupfile.Ref{Source: "gone", Key: "x"})
	l.Rules = append(l.Rules, rules.Rule{Match: rules.Match{SourceName: "gone"}, Action: rules.Exclude})
	got, err = s.ImportLineups(ctx, lineupfile.File{Kind: lineupfile.Kind, Version: lineupfile.Version, Lineups: []lineupfile.Lineup{l}})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Elsewhere" ||
		got[0].LeftOut != (LeftOut{Categories: 1, Channels: 1, Backups: 1, Rules: 1}) {
		t.Errorf("imported %+v", got)
	}
	if shows := shownIn(t, s, got[0].ID); !reflect.DeepEqual(shows, want) {
		t.Errorf("the second import shows %q, want %q", shows, want)
	}

	// All or nothing, within the number of lineups a server has.
	many := lineupfile.File{Kind: lineupfile.Kind, Version: lineupfile.Version}
	for i := range MaxLineups - 3 + 1 {
		many.Lineups = append(many.Lineups, lineupfile.Lineup{Name: "L" + strings.Repeat("x", i)})
	}
	if _, err := s.ImportLineups(ctx, many); !errors.Is(err, ErrTooManyLineups) {
		t.Errorf("an import past the limit: %v", err)
	}
	if all, err := s.Lineups(ctx); err != nil || len(all) != 3 {
		t.Errorf("after a refused import, %d lineups (%v)", len(all), err)
	}

	// A name that another lineup has gets a number, cut to fit.
	long := strings.Repeat("n", 50)
	for _, name := range []string{long, long} {
		if _, err := s.ImportLineups(ctx, lineupfile.File{Kind: lineupfile.Kind, Version: lineupfile.Version,
			Lineups: []lineupfile.Lineup{{Name: name}}}); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Lineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if last := all[len(all)-1].Name; last != strings.Repeat("n", 46)+" (2)" {
		t.Errorf("the second lineup named %s… is %q", long[:5], last)
	}
}
