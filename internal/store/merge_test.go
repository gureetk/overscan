// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/rules"
)

// withGuides returns catalog channels named and keyed as grouped does,
// each with the guide ID after a second slash, such as "a/News/g1".
func withGuides(specs ...string) []CatalogChannel {
	out := make([]CatalogChannel, len(specs))
	for i, spec := range specs {
		name, rest, _ := strings.Cut(spec, "/")
		category, guide, _ := strings.Cut(rest, "/")
		out[i] = CatalogChannel{IdentityKey: name, Name: name, Category: category, XMLTVID: guide,
			StreamURL: "http://streams.example/" + name}
	}
	return out
}

// ruleBackupNames returns the names of an entry's rule backups.
func ruleBackupNames(t *testing.T, s *Store, lineup, entry int64) []string {
	t.Helper()
	list, err := s.EntryRuleBackups(context.Background(), lineup, entry)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, b := range list {
		out = append(out, b.Name)
	}
	return out
}

// A merge rule finds copies of a channel by guide ID or by name, shows the
// one that devices see first, hides the others and makes them its backups,
// after those picked by hand; the administrator's edits win, and what it
// did goes when it goes (D113).
func TestMergeRules(t *testing.T) {
	s, main, at := lineupSetup(t)
	ctx := context.Background()
	one := addSources(t, s, testSource("one", 1, NumbersPlaylist))["one"]
	apply(t, s, main, at, withGuides("Granite One HD/News/g1", "Granite One FHD/News/g1", "Granite Two/News/g2",
		"Harbour/News/", "Harbour (Backup)/Extra/"))
	apply(t, s, one, at, withGuides("Granite One SD/Other/g1", "Meadow/Other/g3"))
	shownBefore := shownIn(t, s, DefaultLineupID)
	if len(shownBefore) != 7 {
		t.Fatalf("before: %q", shownBefore)
	}

	// By guide ID, across sources: the first in the lineup shows.
	byGuide := addRule(t, s, rules.Rule{Action: rules.Merge, Copies: rules.ByGuide})
	want := []string{"1 Granite One HD News", "3 Granite Two News", "4 Harbour News", "5 Harbour (Backup) Extra", "7 Meadow Other"}
	if got := shownIn(t, s, DefaultLineupID); !reflect.DeepEqual(got, want) {
		t.Errorf("merged by guide ID: %q, want %q", got, want)
	}
	hd := entryIn(t, s, DefaultLineupID, "Granite One HD")
	if got := ruleBackupNames(t, s, DefaultLineupID, hd.ID); !reflect.DeepEqual(got, []string{"Granite One FHD", "Granite One SD"}) {
		t.Errorf("Granite One HD's backups by rule: %q", got)
	}
	if e := entryIn(t, s, DefaultLineupID, "Granite One FHD"); !e.RuleHidden || e.MergedInto != "Granite One HD" {
		t.Errorf("the FHD copy: %+v", e)
	}
	if hd.RuleBackups != 2 || hd.MergedInto != "" {
		t.Errorf("the HD channel: %+v", hd)
	}
	list, err := s.LineupRules(ctx, DefaultLineupID)
	if err != nil || len(list) != 1 || list[0].CopiesMerged != 2 || list[0].ChannelsMerged != 1 || list[0].Matches != 7 {
		t.Errorf("the rule: %+v, %v", list, err)
	}

	// It plays the copies after the backups picked by hand.
	harbour := entryIn(t, s, DefaultLineupID, "Harbour")
	if err := s.SetEntryBackups(ctx, DefaultLineupID, hd.ID, []int64{harbour.ID}); err != nil {
		t.Fatal(err)
	}
	p, err := s.PlayableEntry(ctx, hd.ID)
	if err != nil {
		t.Fatal(err)
	}
	var streams []string
	for _, b := range p.Backups {
		streams = append(streams, b.StreamURL)
	}
	if want := []string{"http://streams.example/Harbour", "http://streams.example/Granite One FHD",
		"http://streams.example/Granite One SD"}; !reflect.DeepEqual(streams, want) {
		t.Errorf("playable backups: %q, want %q", streams, want)
	}
	if n, err := s.ChannelsWithBackups(ctx, DefaultLineupID); err != nil || n != 1 {
		t.Errorf("channels with backups: %d, %v", n, err)
	}

	// A copy that the administrator hides, or one of the quality that the
	// rule prefers, comes before the others.
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{hd.ID}, Hidden); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLineupRule(ctx, DefaultLineupID, byGuide, rules.Rule{Action: rules.Merge, Copies: rules.ByGuide, Prefer: "SD"}); err != nil {
		t.Fatal(err)
	}
	sd := entryIn(t, s, DefaultLineupID, "Granite One SD")
	if got := ruleBackupNames(t, s, DefaultLineupID, sd.ID); !reflect.DeepEqual(got, []string{"Granite One FHD", "Granite One HD"}) {
		t.Errorf("preferring SD: Granite One SD's backups are %q", got)
	}
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{hd.ID}, AsGroup); err != nil {
		t.Fatal(err)
	}

	// By name without quality words, the names changed first; a copy that
	// the administrator shows stays shown.
	if err := s.SetLineupRule(ctx, DefaultLineupID, byGuide, rules.Rule{Action: rules.Merge, Copies: rules.ByName,
		Replace: `/\s*\(backup\)$/`}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{entryIn(t, s, DefaultLineupID, "Granite One FHD").ID}, Shown); err != nil {
		t.Fatal(err)
	}
	want = []string{"1 Granite One HD News", "2 Granite One FHD News", "3 Granite Two News", "4 Harbour News", "7 Meadow Other"}
	if got := shownIn(t, s, DefaultLineupID); !reflect.DeepEqual(got, want) {
		t.Errorf("merged by name: %q, want %q", got, want)
	}
	if got := ruleBackupNames(t, s, DefaultLineupID, harbour.ID); !reflect.DeepEqual(got, []string{"Harbour (Backup)"}) {
		t.Errorf("Harbour's backups by rule: %q", got)
	}

	// A copy that its source no longer lists is no one's; the rule off,
	// or removed, undoes all it did.
	apply(t, s, main, at.Add(time.Hour), withGuides("Granite One HD/News/g1", "Granite One FHD/News/g1", "Granite Two/News/g2",
		"Harbour/News/"))
	if got := ruleBackupNames(t, s, DefaultLineupID, harbour.ID); len(got) != 0 {
		t.Errorf("a missing copy is still a backup: %q", got)
	}
	if err := s.SetLineupRule(ctx, DefaultLineupID, byGuide, rules.Rule{Action: rules.Merge, Copies: rules.ByName, Off: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Granite One HD", "Granite One SD"} {
		if e := entryIn(t, s, DefaultLineupID, name); e.RuleBackups != 0 || e.RuleHidden {
			t.Errorf("with the rule off, %s: %+v", name, e)
		}
	}
	if err := s.SetLineupRule(ctx, DefaultLineupID, byGuide, rules.Rule{Action: rules.Merge, Copies: rules.ByGuide}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveLineupRule(ctx, DefaultLineupID, byGuide); err != nil {
		t.Fatal(err)
	}
	if e := entryIn(t, s, DefaultLineupID, "Granite One HD"); e.RuleBackups != 0 {
		t.Errorf("with the rule removed: %+v", e)
	}
}

// A merge rule gives a channel at most MaxBackups copies as backups, and
// hides every copy.
func TestMergeRuleCap(t *testing.T) {
	s, main, at := lineupSetup(t)
	var specs []string
	for i := range MaxBackups + 3 {
		specs = append(specs, "Copy "+strconv.Itoa(i)+"/News/same")
	}
	apply(t, s, main, at, withGuides(specs...))
	addRule(t, s, rules.Rule{Action: rules.Merge, Copies: rules.ByGuide})
	if got := shownIn(t, s, DefaultLineupID); !reflect.DeepEqual(got, []string{"1 Copy 0 News"}) {
		t.Errorf("shown: %q", got)
	}
	if got := ruleBackupNames(t, s, DefaultLineupID, entryIn(t, s, DefaultLineupID, "Copy 0").ID); len(got) != MaxBackups {
		t.Errorf("backups: %q", got)
	}
}
