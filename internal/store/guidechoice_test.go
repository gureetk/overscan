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

// entryTitles returns the titles and start times of every programme that a
// lineup entry shows in a day from from, as each reader of the guide sees
// it, checking that the readers agree.
func entryTitles(t *testing.T, s *Store, e LineupChannel, from time.Time) []string {
	t.Helper()
	ctx := context.Background()
	to := from.Add(24 * time.Hour)
	ps, err := s.EntryProgrammes(ctx, DefaultLineupID, e.EntryID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range ps {
		titles = append(titles, p.Title+" "+p.Start.Format("15:04"))
	}
	var each []string
	if err := s.EachLineupProgramme(ctx, DefaultLineupID, from, to, func(entry int64, p Programme) error {
		if entry == e.EntryID {
			each = append(each, p.Title+" "+p.Start.Format("15:04"))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(each, titles) {
		t.Errorf("%s: EachLineupProgramme %v, EntryProgrammes %v", e.Name, each, titles)
	}
	byEntry, err := s.LineupProgrammes(ctx, DefaultLineupID, from, to, e.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	var lineup []string
	for _, p := range byEntry[e.EntryID] {
		lineup = append(lineup, p.Title+" "+p.Start.Format("15:04"))
	}
	if !reflect.DeepEqual(lineup, titles) {
		t.Errorf("%s: LineupProgrammes %v, EntryProgrammes %v", e.Name, lineup, titles)
	}
	return titles
}

// TestGuideChoices checks that a channel's guide chosen by hand wins over
// its own source's, a guide of the household's own and rules, everywhere
// the guide is read, and that it stays through refreshes (D140).
func TestGuideChoices(t *testing.T) {
	s, src, _ := lineupSetup(t)
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	channels := grouped("Own/News", "Filled/News", "Ruled/News", "Bare/News", "Spare/Films")
	apply(t, s, src, at, channels)
	own, filled, ruled, bare, spare := entryNamed(t, s, "Own"), entryNamed(t, s, "Filled"), entryNamed(t, s, "Ruled"),
		entryNamed(t, s, "Bare"), entryNamed(t, s, "Spare")
	nine, ten := at.Add(3*time.Hour), at.Add(4*time.Hour)
	day := at.Add(-time.Hour)
	writeGuide(t, s, src, at, 0, []int64{own.ChannelID}, nine, ten)
	g, err := s.AddGuide(ctx, GuideSettings{Name: "extra", Address: "http://guide.example/"}, at)
	if err != nil {
		t.Fatal(err)
	}
	listed := []GuideListing{{XMLTVID: "filled.example", Names: []string{"Filled"}}, {XMLTVID: "other.example", Names: []string{"Other"}}}
	auto := func(ids map[string]int64) []GuideMatch {
		return []GuideMatch{{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"], ByName: true}}
	}
	ids := readGuide(t, s, g, at, 24*time.Hour, listed, []string{"filled.example"}, auto, nine, ten)
	// A rule gives Ruled Filled's guide, an hour later (D108).
	addRule(t, s, rules.Rule{Action: rules.Guide, Match: rules.Match{Name: "Ruled"}, Channel: "Filled"})
	addRule(t, s, rules.Rule{Action: rules.Shift, Match: rules.Match{Name: "Ruled"}, Minutes: 60})
	if got := entryTitles(t, s, ruled, day); !reflect.DeepEqual(got, []string{"filled.example at 09:00 10:00", "filled.example at 10:00 11:00"}) {
		t.Fatalf("Ruled before any choice: %v", got)
	}

	// What cannot be chosen.
	if err := s.SetGuideChoice(ctx, GuideChoice{ChannelID: 999}, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown channel: %v", err)
	}
	for _, c := range []GuideChoice{{ChannelID: bare.ChannelID, UseChannel: bare.ChannelID},
		{ChannelID: bare.ChannelID, UseChannel: 999}, {ChannelID: bare.ChannelID, GuideChannelID: 999},
		{ChannelID: bare.ChannelID, UseChannel: own.ChannelID, GuideChannelID: ids["other.example"]}} {
		if err := s.SetGuideChoice(ctx, c, at); !errors.Is(err, ErrNoGuideChoice) {
			t.Errorf("%+v: %v", c, err)
		}
	}
	if _, ok, err := s.GuideChoiceOf(ctx, bare.ChannelID); ok || err != nil {
		t.Fatalf("a refused choice was kept: %v", err)
	}

	// Bare takes Own's guide; Own takes none; Ruled takes the guide's
	// Other, over its rule's guide, with the rule's shift.
	chose := at.Add(time.Minute)
	for _, c := range []GuideChoice{{ChannelID: bare.ChannelID, UseChannel: own.ChannelID}, {ChannelID: own.ChannelID},
		{ChannelID: ruled.ChannelID, GuideChannelID: ids["other.example"]}} {
		if err := s.SetGuideChoice(ctx, c, chose); err != nil {
			t.Fatal(err)
		}
	}
	if c, ok, err := s.GuideChoiceOf(ctx, bare.ChannelID); !ok || err != nil || c.UseChannel != own.ChannelID || !c.ChosenAt.Equal(chose) {
		t.Errorf("Bare's choice %+v, %v, %v", c, ok, err)
	}
	if c, _, _ := s.GuideChoiceOf(ctx, own.ChannelID); !c.None() {
		t.Errorf("Own's choice %+v", c)
	}
	for _, c := range []struct {
		e    LineupChannel
		want []string
	}{
		{bare, []string{"On at 09:00 09:00", "On at 10:00 10:00"}},
		{own, nil},
		// Other's programmes come with the guide's next read.
		{ruled, nil},
		{filled, []string{"filled.example at 09:00 09:00", "filled.example at 10:00 10:00"}},
	} {
		if got := entryTitles(t, s, c.e, day); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.e.Name, got, c.want)
		}
	}
	if _, err := s.ProgrammeAt(ctx, DefaultLineupID, own.EntryID, ten); !errors.Is(err, ErrNotFound) {
		t.Errorf("Own's programme at 10:00, with no guide: %v", err)
	}
	if p, err := s.ProgrammeAt(ctx, DefaultLineupID, bare.EntryID, ten); err != nil || p.Title != "On at 10:00" || p.ID <= 0 {
		t.Errorf("Bare's programme at 10:00: %+v, %v", p, err)
	}
	if found, err := s.SearchProgrammes(ctx, DefaultLineupID, "On at", day, day.Add(24*time.Hour), 10); err != nil || len(found) != 2 ||
		found[0].EntryID != bare.EntryID {
		t.Errorf("search: %+v, %v", found, err)
	}

	// The choice of the guide's channel is its match, by hand, which the
	// guide's next read keeps the programmes of.
	hand, err := s.HandMatches(ctx)
	if err != nil || len(hand) != 1 || hand[0].ChannelID != ruled.ChannelID || hand[0].GuideChannelID != ids["other.example"] || !hand[0].ByHand {
		t.Errorf("hand matches %+v, %v", hand, err)
	}
	if waiting, err := s.GuidesWaiting(ctx); err != nil || !reflect.DeepEqual(waiting, []int64{g}) {
		t.Errorf("waiting %v, %v", waiting, err)
	}
	cands, err := s.MatchCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cands {
		if c.ID != filled.ChannelID && c.ID != spare.ChannelID {
			t.Errorf("candidate %s, whose guide was chosen by hand", c.Name)
		}
	}
	both := func(ids map[string]int64) []GuideMatch {
		return append(auto(ids), hand[0].GuideMatch)
	}
	readGuide(t, s, g, at.Add(time.Hour), 24*time.Hour, listed, []string{"filled.example", "other.example"}, both, nine, ten)
	if got := entryTitles(t, s, ruled, day); !reflect.DeepEqual(got, []string{"other.example at 09:00 10:00", "other.example at 10:00 11:00"}) {
		t.Errorf("Ruled after the read: %v", got)
	}
	if p, err := s.ProgrammeAt(ctx, DefaultLineupID, ruled.EntryID, ten.Add(time.Hour)); err != nil || p.Title != "other.example at 10:00" || p.ID >= 0 {
		t.Errorf("Ruled's programme at 11:00: %+v, %v", p, err)
	}
	if gs, err := s.Guides(ctx); err != nil || gs[0].Matched != 2 || gs[0].ByName != 1 || gs[0].ByHand != 1 || gs[0].Waiting != 0 {
		t.Errorf("the guide %+v, %v", gs, err)
	}

	// Where each channel's programmes come from, and the lists.
	for _, c := range []struct {
		e    LineupChannel
		from string
	}{{own, "none"}, {filled, "guide"}, {ruled, "guide"}, {bare, "channel"}, {spare, ""}} {
		cg, err := s.ChannelGuideOf(ctx, c.e.ChannelID)
		if err != nil || cg.From() != c.from {
			t.Errorf("%s comes from %q, want %q: %+v, %v", c.e.Name, cg.From(), c.from, cg, err)
		}
	}
	if cg, _ := s.ChannelGuideOf(ctx, ruled.ChannelID); !cg.ByHand || cg.ByName || cg.Guide != "extra" || cg.GuideXMLTVID != "other.example" ||
		!reflect.DeepEqual(cg.GuideNames, []string{"Other"}) || !cg.GuideOn || !cg.Chosen {
		t.Errorf("Ruled's guide %+v", cg)
	}
	if cg, _ := s.ChannelGuideOf(ctx, bare.ChannelID); cg.UseName != "Own" || cg.UseSource != "main" || !cg.Chosen {
		t.Errorf("Bare's guide %+v", cg)
	}
	if cg, _ := s.ChannelGuideOf(ctx, filled.ChannelID); !cg.ByName || cg.Chosen {
		t.Errorf("Filled's guide %+v", cg)
	}
	if _, err := s.ChannelGuideOf(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown channel's guide: %v", err)
	}
	list := func(show, find string, offset, limit int) ([]string, int) {
		t.Helper()
		cgs, total, err := s.ChannelGuides(ctx, show, find, offset, limit)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, cg := range cgs {
			names = append(names, cg.Name)
		}
		return names, total
	}
	for _, c := range []struct {
		show, find    string
		offset, limit int
		want          []string
		total         int
	}{
		{ShowWithoutGuide, "", 0, 10, []string{"Spare"}, 1},
		{ShowByName, "", 0, 10, []string{"Filled"}, 1},
		{ShowByHand, "", 0, 10, []string{"Own", "Ruled", "Bare"}, 3},
		{ShowByHand, "", 1, 1, []string{"Ruled"}, 3},
		{ShowAll, "e", 0, 10, []string{"Filled", "Ruled", "Bare", "Spare"}, 4},
		{ShowAll, "%", 0, 10, nil, 0},
	} {
		if got, total := list(c.show, c.find, c.offset, c.limit); !reflect.DeepEqual(got, c.want) || total != c.total {
			t.Errorf("%s %q from %d: %v of %d, want %v of %d", c.show, c.find, c.offset, got, total, c.want, c.total)
		}
	}
	if _, _, err := s.ChannelGuides(ctx, "some", "", 0, 10); err == nil {
		t.Error("a list that does not exist")
	}
	if counts, err := s.ChannelGuideCounts(ctx); err != nil ||
		!reflect.DeepEqual(counts, map[string]int{ShowWithoutGuide: 1, ShowByName: 1, ShowByHand: 3}) {
		t.Errorf("counts %v, %v", counts, err)
	}
	if n, err := s.ChannelsWithoutGuide(ctx); err != nil || n != 1 {
		t.Errorf("%d without a guide, %v; want Spare alone", n, err)
	}
	if guided, err := s.GuidedChannels(ctx); err != nil || len(guided) != 1 || guided[0].Name != "Own" || guided[0].Source != "main" {
		t.Errorf("guided channels %+v, %v", guided, err)
	}
	if ps, err := s.GuideChannelProgrammes(ctx, ids["other.example"], day, day.Add(24*time.Hour)); err != nil || len(ps) != 2 || ps[0].ID >= 0 {
		t.Errorf("Other's programmes %+v, %v", ps, err)
	}
	if ps, err := s.ChannelProgrammes(ctx, own.ChannelID, day, day.Add(24*time.Hour)); err != nil || len(ps) != 2 {
		t.Errorf("Own's programmes %+v, %v", ps, err)
	}

	// The choices stay through a refresh, and while a channel is missing.
	apply(t, s, src, at.Add(2*time.Hour), channels)
	apply(t, s, src, at.Add(3*time.Hour), grouped("Own/News", "Filled/News", "Bare/News", "Spare/Films"))
	if _, ok, _ := s.GuideChoiceOf(ctx, ruled.ChannelID); !ok {
		t.Error("Ruled's choice went while it is missing")
	}
	if hand, err := s.HandMatches(ctx); err != nil || len(hand) != 0 {
		t.Errorf("a missing channel's hand match %+v, %v", hand, err)
	}
	apply(t, s, src, at.Add(4*time.Hour), channels)
	if got := entryTitles(t, s, bare, day); len(got) != 2 {
		t.Errorf("Bare after refreshes: %v", got)
	}

	// The guide turned off fills nothing; Ruled keeps its choice.
	if err := s.SetGuideEnabled(ctx, g, false); err != nil {
		t.Fatal(err)
	}
	if got := entryTitles(t, s, ruled, day); len(got) != 0 {
		t.Errorf("Ruled with the guide off: %v", got)
	}
	if cg, _ := s.ChannelGuideOf(ctx, ruled.ChannelID); !cg.Chosen || cg.GuideOn || cg.GuideChannelID != ids["other.example"] || cg.From() != "guide" {
		t.Errorf("Ruled's guide with the guide off %+v", cg)
	}
	if hand, err := s.HandMatches(ctx); err != nil || len(hand) != 0 {
		t.Errorf("hand matches of an off guide %+v, %v", hand, err)
	}
	if err := s.SetGuideEnabled(ctx, g, true); err != nil {
		t.Fatal(err)
	}
	// The guides' matches are worked out again once it is on, which the
	// guides' reader does.
	if _, err := s.SetGuideMatches(ctx, auto(ids)); err != nil {
		t.Fatal(err)
	}
	// Choosing again, with the guide on, makes the match at once.
	if err := s.SetGuideChoice(ctx, GuideChoice{ChannelID: ruled.ChannelID, GuideChannelID: ids["other.example"]}, chose); err != nil {
		t.Fatal(err)
	}
	if got := entryTitles(t, s, ruled, day); len(got) != 2 {
		t.Errorf("Ruled chosen again: %v", got)
	}

	// Taken back, Own has its own guide again, and Ruled its rule's.
	for _, e := range []LineupChannel{own, ruled} {
		if had, err := s.ClearGuideChoice(ctx, e.ChannelID); err != nil || !had {
			t.Errorf("%s: %v, %v", e.Name, had, err)
		}
	}
	if had, err := s.ClearGuideChoice(ctx, spare.ChannelID); err != nil || had {
		t.Errorf("Spare had a choice: %v, %v", had, err)
	}
	if got := entryTitles(t, s, own, day); len(got) != 2 || got[0] != "On at 09:00 09:00" {
		t.Errorf("Own taken back: %v", got)
	}
	if got := entryTitles(t, s, ruled, day); !reflect.DeepEqual(got, []string{"filled.example at 09:00 10:00", "filled.example at 10:00 11:00"}) {
		t.Errorf("Ruled taken back: %v", got)
	}
	if hand, err := s.HandMatches(ctx); err != nil || len(hand) != 0 {
		t.Errorf("hand matches after taking back %+v, %v", hand, err)
	}

	// A choice goes with the guide's channel that it names: removing the
	// guide gives Spare back to the guides' matching, with nothing left.
	if err := s.SetGuideChoice(ctx, GuideChoice{ChannelID: spare.ChannelID, GuideChannelID: ids["other.example"]}, chose); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteGuide(ctx, g); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.GuideChoiceOf(ctx, spare.ChannelID); ok {
		t.Error("Spare's choice stayed after its guide went")
	}
	if _, ok, _ := s.GuideChoiceOf(ctx, bare.ChannelID); !ok {
		t.Error("Bare's choice of another channel went with the guide")
	}
}
