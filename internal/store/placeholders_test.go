// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// spans writes blocks as local start and stop times, for comparing.
func spans(bs []span, zone *time.Location) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.start.In(zone).Format("02 15:04")+"-"+b.stop.In(zone).Format("15:04"))
	}
	return out
}

// TestPlaceholderBlocks checks the blocks that placeholders fill: from
// midnight in the server's zone, none in the past, none beyond two days,
// and a day of 25 hours cut at its midnight (D142).
func TestPlaceholderBlocks(t *testing.T) {
	london, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatal(err)
	}
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 12, 4, 10, 20, 0, 0, time.UTC)
	hourly := placeholders{block: time.Hour, zone: time.UTC, now: now}

	// From now, and not before it, to three hours on.
	for _, from := range []time.Time{now, now.Add(-2 * time.Hour)} {
		got := spans(hourly.blocks(from, now.Add(3*time.Hour)), time.UTC)
		if want := []string{"04 10:00-11:00", "04 11:00-12:00", "04 12:00-13:00", "04 13:00-14:00"}; !reflect.DeepEqual(got, want) {
			t.Errorf("from %v: %v, want %v", from, got, want)
		}
	}
	// None that ended before now, and none that start two days on.
	if got := hourly.blocks(now.Add(-5*time.Hour), now.Add(-time.Hour)); len(got) != 0 {
		t.Errorf("in the past: %v", spans(got, time.UTC))
	}
	if got := hourly.blocks(now, now.Add(7*24*time.Hour)); len(got) != 49 ||
		!got[48].start.Equal(time.Date(2026, 12, 6, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("a week ahead: %d blocks, the last %v", len(got), spans(got[len(got)-1:], time.UTC))
	}
	// A window within a block.
	if got := spans(hourly.blocks(now.Add(time.Hour), now.Add(70*time.Minute)), time.UTC); !reflect.DeepEqual(got, []string{"04 11:00-12:00"}) {
		t.Errorf("within a block: %v", got)
	}
	from, to := hourly.window(now.Add(time.Hour), now.Add(70*time.Minute))
	if !from.Equal(now.Add(40*time.Minute)) || !to.Equal(now.Add(100*time.Minute)) {
		t.Errorf("window within a block: %v to %v", from, to)
	}
	if from, to := hourly.window(now.Add(-3*time.Hour), now.Add(-time.Hour)); !from.Equal(now.Add(-3*time.Hour)) || !to.Equal(now.Add(-time.Hour)) {
		t.Errorf("window in the past: %v to %v", from, to)
	}

	// Half-hour zones start their hours on the half hour, in UTC.
	got := spans(placeholders{block: time.Hour, zone: kolkata, now: now}.blocks(now, now.Add(2*time.Hour)), time.UTC)
	if want := []string{"04 09:30-10:30", "04 10:30-11:30", "04 11:30-12:30"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Kolkata: %v, want %v", got, want)
	}

	// The day that London's clocks go back lasts 25 hours: three-hour
	// blocks from its midnight, the last cut short at the next.
	day := time.Date(2026, 10, 25, 0, 0, 0, 0, london)
	threes := placeholders{block: 3 * time.Hour, zone: london, now: day}
	bs := threes.blocks(day, day.Add(25*time.Hour))
	if len(bs) != 9 || bs[8].stop.Sub(bs[8].start) != time.Hour || !bs[8].stop.Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, london)) {
		t.Errorf("London's long day: %v", spans(bs, london))
	}
	if next := threes.blockAt(time.Date(2026, 10, 26, 1, 0, 0, 0, london)); !next.start.Equal(time.Date(2026, 10, 26, 0, 0, 0, 0, london)) {
		t.Errorf("the day after: %v", spans([]span{next}, london))
	}
}

// TestPlaceholderFill checks that placeholders fill only the blocks in
// which a channel has no programme at all, among its programmes, which
// keep to the window (D142).
func TestPlaceholderFill(t *testing.T) {
	now := time.Date(2026, 12, 4, 10, 20, 0, 0, time.UTC)
	f := placeholders{block: time.Hour, zone: time.UTC, now: now}
	at := func(h, m int) time.Time { return time.Date(2026, 12, 4, h, m, 0, 0, time.UTC) }
	ps := []Programme{
		{ID: 1, Title: "Ended", Start: at(8, 0), Stop: at(9, 0)},
		{ID: 2, Title: "Morning", Start: at(9, 30), Stop: at(10, 30)},
		{ID: 3, Title: "Short", Start: at(12, 15), Stop: at(12, 45)},
		{ID: 4, Title: "Late", Start: at(17, 0), Stop: at(18, 0)},
	}
	from, to := now, at(16, 20)
	var got []string
	ids := map[int64]bool{}
	for _, p := range f.fill(ps, "Bare", from, to) {
		got = append(got, p.Title+" "+p.Start.Format("15:04")+"-"+p.Stop.Format("15:04"))
		if p.Placeholder != (p.Title == "Bare") || p.Placeholder && (p.ID > -placeholderIDs || p.Start.Location() != time.UTC) {
			t.Errorf("%+v", p)
		}
		if ids[p.ID] {
			t.Errorf("ID %d twice", p.ID)
		}
		ids[p.ID] = true
	}
	want := []string{"Morning 09:30-10:30", "Bare 11:00-12:00", "Short 12:15-12:45", "Bare 13:00-14:00", "Bare 14:00-15:00",
		"Bare 15:00-16:00", "Bare 16:00-17:00"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
	// A long programme over several blocks fills them.
	long := []Programme{{ID: 5, Title: "Film", Start: at(9, 0), Stop: at(13, 10)}}
	got = nil
	for _, p := range f.fill(long, "Bare", from, at(15, 0)) {
		got = append(got, p.Title+" "+p.Start.Format("15:04"))
	}
	if want := []string{"Film 09:00", "Bare 14:00"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a long programme: %v, want %v", got, want)
	}
	// Nothing before now.
	if got := f.fill(ps[:1], "Bare", at(7, 0), at(9, 30)); len(got) != 1 || got[0].Title != "Ended" {
		t.Errorf("before now: %+v", got)
	}
}

// listings returns what each reader of the listings gives a lineup entry
// between from and to at now, checking that they agree, as titles with
// their start times.
func listings(t *testing.T, s *Store, e LineupChannel, from, to, now time.Time) []string {
	t.Helper()
	ctx := context.Background()
	line := func(p Programme) string {
		mark := ""
		if p.Placeholder {
			mark = "*"
		}
		return mark + p.Title + " " + p.Start.Format("02 15:04")
	}
	ps, err := s.EntryListings(ctx, DefaultLineupID, e.EntryID, from, to, now)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range ps {
		titles = append(titles, line(p))
	}
	var each []string
	last := int64(0)
	seen := map[int64]bool{}
	if err := s.EachLineupListing(ctx, DefaultLineupID, from, to, now, func(entry int64, p Programme) error {
		if entry != last {
			if seen[entry] {
				t.Errorf("entry %d's programmes came apart", entry)
			}
			seen[entry], last = true, entry
		}
		if entry == e.EntryID {
			each = append(each, line(p))
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(each, titles) {
		t.Errorf("%s: EachLineupListing %v, EntryListings %v", e.Name, each, titles)
	}
	for _, only := range [][]int64{{e.EntryID}, nil} {
		byEntry, err := s.LineupListings(ctx, DefaultLineupID, from, to, now, only...)
		if err != nil {
			t.Fatal(err)
		}
		var lineup []string
		for _, p := range byEntry[e.EntryID] {
			lineup = append(lineup, line(p))
		}
		if !reflect.DeepEqual(lineup, titles) {
			t.Errorf("%s: LineupListings(%v) %v, EntryListings %v", e.Name, only, lineup, titles)
		}
	}
	return titles
}

// TestPlaceholders checks the setting, and that placeholders reach every
// reader of the listings, named as devices see the channel, and none of
// what reads programmes alone (D142).
func TestPlaceholders(t *testing.T) {
	s, src, _ := lineupSetup(t)
	s.zone = time.UTC
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	apply(t, s, src, at, grouped("Own/News", "Bare/News", "Hidden/News"))
	own, bare, hidden := entryNamed(t, s, "Own"), entryNamed(t, s, "Bare"), entryNamed(t, s, "Hidden")
	nine, ten := at.Add(3*time.Hour), at.Add(4*time.Hour)
	writeGuide(t, s, src, at, 0, []int64{own.ChannelID}, nine, ten)
	if err := s.RenameEntry(ctx, DefaultLineupID, bare.EntryID, "Bare TV"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{hidden.EntryID}, Hidden); err != nil {
		t.Fatal(err)
	}
	now := at.Add(2*time.Hour + 20*time.Minute) // 08:20
	from, to := now, now.Add(4*time.Hour)

	// Off at first: the listings are the programmes.
	if block, err := s.Placeholders(ctx); block != 0 || err != nil {
		t.Fatalf("at first: %v, %v", block, err)
	}
	if got := listings(t, s, own, from, to, now); !reflect.DeepEqual(got, []string{"On at 09:00 04 09:00", "On at 10:00 04 10:00"}) {
		t.Errorf("Own, off: %v", got)
	}
	if got := listings(t, s, bare, from, to, now); got != nil {
		t.Errorf("Bare, off: %v", got)
	}
	for _, bad := range []time.Duration{-time.Hour, 45 * time.Minute, 4 * time.Hour} {
		if err := s.SetPlaceholders(ctx, bad); err == nil {
			t.Errorf("%v was taken", bad)
		}
	}

	if err := s.SetPlaceholders(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
	if block, err := s.Placeholders(ctx); block != time.Hour || err != nil {
		t.Fatalf("set: %v, %v", block, err)
	}
	if got, want := listings(t, s, own, from, to, now), []string{"*Own 04 08:00", "On at 09:00 04 09:00", "On at 10:00 04 10:00",
		"*Own 04 11:00", "*Own 04 12:00"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Own, on: %v, want %v", got, want)
	}
	if got, want := listings(t, s, bare, from, to, now), []string{"*Bare TV 04 08:00", "*Bare TV 04 09:00", "*Bare TV 04 10:00",
		"*Bare TV 04 11:00", "*Bare TV 04 12:00"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Bare, on: %v, want %v", got, want)
	}
	// None in the past, nor on a channel that devices do not see.
	if got := listings(t, s, bare, at, now.Add(-time.Hour), now); got != nil {
		t.Errorf("Bare, in the past: %v", got)
	}
	if _, err := s.EntryListings(ctx, DefaultLineupID, hidden.EntryID, from, to, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("Hidden: %v", err)
	}
	byEntry, err := s.LineupListings(ctx, DefaultLineupID, from, to, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(byEntry) != 2 || byEntry[hidden.EntryID] != nil {
		t.Errorf("the lineup's listings: %v", byEntry)
	}

	// What reads programmes alone sees no placeholder.
	if ps, err := s.EntryProgrammes(ctx, DefaultLineupID, bare.EntryID, from, to); len(ps) != 0 || err != nil {
		t.Errorf("Bare's programmes: %+v, %v", ps, err)
	}
	if _, err := s.ProgrammeAt(ctx, DefaultLineupID, bare.EntryID, ten); !errors.Is(err, ErrNotFound) {
		t.Errorf("Bare's programme at 10:00: %v", err)
	}
	if found, err := s.SearchProgrammes(ctx, DefaultLineupID, "Bare", from, to, 10); len(found) != 0 || err != nil {
		t.Errorf("search: %+v, %v", found, err)
	}
	ps, _ := s.EntryListings(ctx, DefaultLineupID, bare.EntryID, from, to, now)
	if InArchive(ps[0], 7, to) {
		t.Errorf("a placeholder plays from an archive: %+v", ps[0])
	}

	// A guide of the household's own fills Bare: its programmes come
	// with placeholders where it has none.
	g, err := s.AddGuide(ctx, GuideSettings{Name: "extra", Address: "http://guide.example/"}, at)
	if err != nil {
		t.Fatal(err)
	}
	listed := []GuideListing{{XMLTVID: "bare.example", Names: []string{"Bare"}}}
	readGuide(t, s, g, at, 24*time.Hour, listed, []string{"bare.example"}, func(ids map[string]int64) []GuideMatch {
		return []GuideMatch{{ChannelID: bare.ChannelID, GuideChannelID: ids["bare.example"], ByName: true}}
	}, ten)
	got := listings(t, s, bare, from, to, now)
	if want := []string{"*Bare TV 04 08:00", "*Bare TV 04 09:00", "bare.example at 10:00 04 10:00", "*Bare TV 04 11:00",
		"*Bare TV 04 12:00"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Bare, filled: %v, want %v", got, want)
	}

	// Two-hour blocks, and off again.
	if err := s.SetPlaceholders(ctx, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := listings(t, s, own, from, to, now); strings.Join(got, ", ") != "On at 09:00 04 09:00, On at 10:00 04 10:00, *Own 04 12:00" {
		t.Errorf("Own, two-hour blocks: %v", got)
	}
	if err := s.SetPlaceholders(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if got := listings(t, s, bare, from, to, now); !reflect.DeepEqual(got, []string{"bare.example at 10:00 04 10:00"}) {
		t.Errorf("Bare, off again: %v", got)
	}
}
