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

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/source"
)

func TestGuideSettings(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)

	var bad *source.Error
	for _, g := range []GuideSettings{
		{Name: "Extra", Address: "http://guide.example/guide.xml"},
		{Name: "extra", Address: "ftp://guide.example/guide.xml"},
		{Name: "extra", Address: "relative/guide.xml"},
		{Name: "extra", Address: ""},
		{Name: "extra", Address: "http://guide.example/guide.xml", UserAgent: "bad\nagent"},
	} {
		if _, err := s.AddGuide(ctx, g, at); !errors.As(err, &bad) {
			t.Errorf("%+v: %v, want a setting's error", g, err)
		}
	}
	if s.secretsSealed(t) != 0 {
		t.Fatal("a refused guide left a row")
	}

	address := "http://user:secret@guide.example:8080/guide.xml.gz?token=abc"
	one, err := s.AddGuide(ctx, GuideSettings{Name: "one", Address: address, UserAgent: "Player/1", PublicOnly: true}, at)
	if err != nil {
		t.Fatal(err)
	}
	two, err := s.AddGuide(ctx, GuideSettings{Name: "two", Address: "/srv/guides/two.xml"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddGuide(ctx, GuideSettings{Name: "one", Address: "http://guide.example/"}, at); !errors.Is(err, ErrGuideExists) {
		t.Errorf("a name taken: %v", err)
	}
	g, err := s.Guide(ctx, one)
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "one" || g.Host != "guide.example:8080" || g.UserAgent != "Player/1" || !g.PublicOnly || !g.Enabled ||
		g.Position != 1 || !g.AddedAt.Equal(at) || g.Address != AddressSaved || !g.ReadAt.IsZero() {
		t.Errorf("guide %+v", g)
	}
	if g, _ := s.Guide(ctx, two); g.Host != "file two.xml" || g.Position != 2 {
		t.Errorf("the file's guide %+v", g)
	}
	settings, err := s.GuideSettings(ctx, one)
	if err != nil || settings.Address != address || settings.IsFile() {
		t.Errorf("settings %+v, %v", settings, err)
	}
	// The address is sealed: the row does not hold it.
	var raw []byte
	if err := s.db.QueryRow("SELECT address FROM guides WHERE id = ?", one).Scan(&raw); err != nil || containsBytes(raw, "secret") ||
		containsBytes(raw, "guide.example") {
		t.Errorf("the stored address %q, %v", raw, err)
	}

	// A change keeps the address unless it gives one; a new address, user
	// agent or choice of public addresses makes the guide due.
	if _, err := s.db.Exec("UPDATE guides SET read_from = 'read'"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateGuide(ctx, one, GuideSettings{Name: "first", UserAgent: "Player/1", PublicOnly: true}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Guide(ctx, one); g.Name != "first" || g.ReadFrom != "read" || g.Host != "guide.example:8080" {
		t.Errorf("after renaming: %+v", g)
	}
	if settings, _ := s.GuideSettings(ctx, one); settings.Address != address {
		t.Errorf("the address after renaming: %q", settings.Address)
	}
	if err := s.UpdateGuide(ctx, one, GuideSettings{Name: "first", UserAgent: "Player/2", PublicOnly: true}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Guide(ctx, one); g.ReadFrom != "" {
		t.Errorf("a new user agent left the guide read: %+v", g)
	}
	if err := s.UpdateGuide(ctx, one, GuideSettings{Name: "first", Address: "https://other.example/g.xml"}); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Guide(ctx, one); g.Host != "other.example" {
		t.Errorf("after a new address: %+v", g)
	}
	if err := s.UpdateGuide(ctx, one, GuideSettings{Name: "two"}); !errors.Is(err, ErrGuideExists) {
		t.Errorf("renaming to a name taken: %v", err)
	}
	if err := s.UpdateGuide(ctx, 999, GuideSettings{Name: "nine"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("no such guide: %v", err)
	}

	// Order.
	three, err := s.AddGuide(ctx, GuideSettings{Name: "three", Address: "http://three.example/"}, at)
	if err != nil {
		t.Fatal(err)
	}
	order := func() []string {
		gs, err := s.Guides(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, g := range gs {
			out = append(out, fmt.Sprintf("%d %s", g.Position, g.Name))
		}
		return out
	}
	if err := s.MoveGuide(ctx, three, 1); err != nil {
		t.Fatal(err)
	}
	if got, want := order(), []string{"1 three", "2 first", "3 two"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after moving three first: %v, want %v", got, want)
	}
	if err := s.MoveGuide(ctx, three, 99); err != nil {
		t.Fatal(err)
	}
	if got, want := order(), []string{"1 first", "2 two", "3 three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after moving three last: %v, want %v", got, want)
	}
	if err := s.DeleteGuide(ctx, one); err != nil {
		t.Fatal(err)
	}
	if got, want := order(), []string{"1 two", "2 three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after removing the first: %v, want %v", got, want)
	}
	if err := s.DeleteGuide(ctx, one); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing it again: %v", err)
	}
	if err := s.SetGuideEnabled(ctx, two, false); err != nil {
		t.Fatal(err)
	}
	if g, _ := s.Guide(ctx, two); g.Enabled {
		t.Error("turned off, it is on")
	}

	// At most MaxGuides.
	for i := len(order()); i < MaxGuides; i++ {
		if _, err := s.AddGuide(ctx, GuideSettings{Name: fmt.Sprintf("g%d", i), Address: "http://more.example/"}, at); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AddGuide(ctx, GuideSettings{Name: "toomany", Address: "http://more.example/"}, at); !errors.Is(err, ErrTooManyGuides) {
		t.Errorf("one too many: %v", err)
	}
}

// secretsSealed counts the guides' rows, for the test above.
func (s *Store) secretsSealed(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM guides").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// readGuide writes a read of a guide of the household's own: the channels
// that it lists, by ID and names, then an hour-long programme at each start
// on each of the channels kept, by the guide's ID for them, and the
// matches. It returns the guide's channels' IDs by the guide's IDs.
func readGuide(t *testing.T, s *Store, guideID int64, at time.Time, keep time.Duration, listed []GuideListing,
	kept []string, matches func(ids map[string]int64) []GuideMatch, starts ...time.Time) map[string]int64 {
	t.Helper()
	ctx := context.Background()
	w, err := s.BeginGuideRead(ctx, guideID)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := w.ListChannels(ctx, listed, at)
	if err != nil {
		t.Fatal(err)
	}
	var keptIDs []int64
	for _, x := range kept {
		keptIDs = append(keptIDs, ids[x])
		for _, start := range starts {
			if err := w.Add(ctx, GuideProgramme{ChannelID: ids[x], Start: start, Stop: start.Add(time.Hour),
				Title: x + " at " + start.Format("15:04")}); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.KeepPast(keep)
	if err := w.Commit(ctx, at, "test", GuideRead{Listed: len(listed), Kept: keptIDs, Matches: matches(ids)}); err != nil {
		t.Fatal(err)
	}
	return ids
}

// TestGuidesProgrammes checks that the programmes of a guide of the
// household's own reach every reader of the guide for the channels that it
// fills, and only for those (D139).
func TestGuidesProgrammes(t *testing.T) {
	s, src, _ := lineupSetup(t)
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	apply(t, s, src, at, grouped("Own/News", "Filled/News", "Ruled/News", "Bare/News"))
	own, filled, ruled, bare := entryNamed(t, s, "Own"), entryNamed(t, s, "Filled"), entryNamed(t, s, "Ruled"), entryNamed(t, s, "Bare")
	nine, ten := at.Add(3*time.Hour), at.Add(4*time.Hour)
	writeGuide(t, s, src, at, 0, []int64{own.ChannelID}, nine, ten)

	// Only the channels without programmes of their own are candidates.
	// (The icons of the guide's channels are checked further down.)
	cands, err := s.MatchCandidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cands {
		names = append(names, c.Name+"/"+c.Category)
	}
	if want := []string{"Filled/News", "Ruled/News", "Bare/News"}; !reflect.DeepEqual(names, want) {
		t.Errorf("candidates %v, want %v", names, want)
	}

	g, err := s.AddGuide(ctx, GuideSettings{Name: "extra", Address: "http://guide.example/"}, at)
	if err != nil {
		t.Fatal(err)
	}
	listed := []GuideListing{{XMLTVID: "filled.example", Names: []string{"Filled", "Filled TV"}, Icon: "http://logos.example/filled.png"},
		{XMLTVID: "other.example", Names: []string{"Other"}}}
	ids := readGuide(t, s, g, at, 24*time.Hour, listed, []string{"filled.example"}, func(ids map[string]int64) []GuideMatch {
		return []GuideMatch{{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"]}}
	}, nine, ten)
	// A rule shows Filled's guide on Ruled, an hour later (D108).
	addRule(t, s, rules.Rule{Action: rules.Guide, Match: rules.Match{Name: "Ruled"}, Channel: "Filled"})
	addRule(t, s, rules.Rule{Action: rules.Shift, Match: rules.Match{Name: "Ruled"}, Minutes: 60})

	day := at.Add(-time.Hour)
	want := map[int64][]string{
		own.EntryID:    {"On at 09:00 09:00", "On at 10:00 10:00"},
		filled.EntryID: {"filled.example at 09:00 09:00", "filled.example at 10:00 10:00"},
		ruled.EntryID:  {"filled.example at 09:00 10:00", "filled.example at 10:00 11:00"},
	}
	got := map[int64][]string{}
	if err := s.EachLineupProgramme(ctx, DefaultLineupID, day, day.Add(24*time.Hour), func(entry int64, p Programme) error {
		if entry != own.EntryID && p.ID >= 0 || entry == own.EntryID && p.ID <= 0 {
			t.Errorf("entry %d's programme %q has the ID %d", entry, p.Title, p.ID)
		}
		got[entry] = append(got[entry], p.Title+" "+p.Start.Format("15:04"))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("EachLineupProgramme %v, want %v", got, want)
	}
	byEntry, err := s.LineupProgrammes(ctx, DefaultLineupID, day, day.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got = map[int64][]string{}
	for entry, ps := range byEntry {
		for _, p := range ps {
			got[entry] = append(got[entry], p.Title+" "+p.Start.Format("15:04"))
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("LineupProgrammes %v, want %v", got, want)
	}
	if only, err := s.LineupProgrammes(ctx, DefaultLineupID, day, day.Add(24*time.Hour), filled.EntryID); err != nil ||
		len(only) != 1 || len(only[filled.EntryID]) != 2 {
		t.Errorf("LineupProgrammes of Filled alone: %v, %v", only, err)
	}
	for _, e := range []LineupChannel{own, filled, ruled} {
		ps, err := s.EntryProgrammes(ctx, DefaultLineupID, e.EntryID, day, day.Add(24*time.Hour))
		var titles []string
		for _, p := range ps {
			titles = append(titles, p.Title+" "+p.Start.Format("15:04"))
		}
		if err != nil || !reflect.DeepEqual(titles, want[e.EntryID]) {
			t.Errorf("EntryProgrammes of %s: %v, %v", e.Name, titles, err)
		}
	}
	if ps, err := s.EntryProgrammes(ctx, DefaultLineupID, bare.EntryID, day, day.Add(24*time.Hour)); err != nil || len(ps) != 0 {
		t.Errorf("Bare's programmes %v, %v", ps, err)
	}
	if p, err := s.ProgrammeAt(ctx, DefaultLineupID, ruled.EntryID, ten.Add(time.Hour)); err != nil || p.Title != "filled.example at 10:00" ||
		p.ID >= 0 || p.Start != ten.Add(time.Hour) {
		t.Errorf("Ruled's programme at 11:00: %+v, %v", p, err)
	}
	if _, err := s.ProgrammeAt(ctx, DefaultLineupID, ruled.EntryID, nine); !errors.Is(err, ErrNotFound) {
		t.Errorf("Ruled's programme at 09:00: %v", err)
	}
	if p, err := s.ProgrammeAt(ctx, DefaultLineupID, own.EntryID, ten); err != nil || p.Title != "On at 10:00" || p.ID <= 0 {
		t.Errorf("Own's programme at 10:00: %+v, %v", p, err)
	}
	// The search finds both, in order of their times, then of numbers.
	found, err := s.SearchProgrammes(ctx, DefaultLineupID, "at 10", day, day.Add(24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	var hits []string
	for _, f := range found {
		hits = append(hits, fmt.Sprintf("%d %s", f.EntryID, f.Start.Format("15:04")))
	}
	if want := []string{fmt.Sprintf("%d 10:00", own.EntryID), fmt.Sprintf("%d 10:00", filled.EntryID),
		fmt.Sprintf("%d 11:00", ruled.EntryID)}; !reflect.DeepEqual(hits, want) {
		t.Errorf("search %v, want %v", hits, want)
	}
	if found, err := s.SearchProgrammes(ctx, DefaultLineupID, "at", day, day.Add(24*time.Hour), 2); err != nil || len(found) != 2 ||
		found[0].Start != nine || found[1].Start != nine {
		t.Errorf("search to 2: %+v, %v", found, err)
	}

	gs, err := s.Guides(ctx)
	if err != nil || len(gs) != 1 || gs[0].Matched != 1 || gs[0].ByName != 0 || gs[0].Waiting != 0 || gs[0].Listed != 2 ||
		gs[0].Channels != 1 || gs[0].Programmes != 2 {
		t.Errorf("the guide %+v, %v", gs, err)
	}
	if n, err := s.ChannelsWithoutGuide(ctx); err != nil || n != 2 {
		t.Errorf("%d channels without a guide, %v; want Ruled and Bare", n, err)
	}

	// A match with a channel of the guide that the read did not keep
	// waits for the next read.
	if _, err := s.SetGuideMatches(ctx, []GuideMatch{
		{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"]},
		{ChannelID: bare.ChannelID, GuideChannelID: ids["other.example"], ByName: true}}); err != nil {
		t.Fatal(err)
	}
	if waiting, err := s.GuidesWaiting(ctx); err != nil || !reflect.DeepEqual(waiting, []int64{g}) {
		t.Errorf("waiting %v, %v", waiting, err)
	}
	if g, _ := s.Guide(ctx, g); g.Matched != 2 || g.ByName != 1 || g.Waiting != 1 {
		t.Errorf("the guide %+v", g)
	}

	// The next read keeps both, and carries over what ended (D83).
	later := at.Add(6 * time.Hour)
	readGuide(t, s, g, later, 24*time.Hour, listed, []string{"filled.example", "other.example"}, func(ids map[string]int64) []GuideMatch {
		return []GuideMatch{{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"]},
			{ChannelID: bare.ChannelID, GuideChannelID: ids["other.example"], ByName: true}}
	}, later)
	if ps, err := s.EntryProgrammes(ctx, DefaultLineupID, filled.EntryID, day, day.Add(24*time.Hour)); err != nil || len(ps) != 3 {
		t.Errorf("Filled's programmes after the next read: %+v, %v", ps, err)
	}
	if ps, err := s.EntryProgrammes(ctx, DefaultLineupID, bare.EntryID, day, day.Add(24*time.Hour)); err != nil || len(ps) != 1 {
		t.Errorf("Bare's programmes after the next read: %+v, %v", ps, err)
	}
	if waiting, err := s.GuidesWaiting(ctx); err != nil || len(waiting) != 0 {
		t.Errorf("waiting after the read: %v, %v", waiting, err)
	}

	// A guide turned off fills nothing, at once.
	if err := s.SetGuideEnabled(ctx, g, false); err != nil {
		t.Fatal(err)
	}
	if ps, err := s.EntryProgrammes(ctx, DefaultLineupID, filled.EntryID, day, day.Add(24*time.Hour)); err != nil || len(ps) != 0 {
		t.Errorf("Filled's programmes with the guide off: %+v, %v", ps, err)
	}
	if listed, err := s.ListedChannels(ctx); err != nil || len(listed) != 0 {
		t.Errorf("an off guide's channels are listed: %+v, %v", listed, err)
	}
	if err := s.SetGuideEnabled(ctx, g, true); err != nil {
		t.Fatal(err)
	}
	if listed, err := s.ListedChannels(ctx); err != nil || len(listed) != 2 || listed[0].XMLTVID != "filled.example" ||
		!slices.Equal(listed[0].Names, []string{"Filled", "Filled TV"}) || !listed[0].Kept {
		t.Errorf("listed channels %+v, %v", listed, err)
	}
	var icon string
	if err := s.db.QueryRow("SELECT icon FROM guide_channels WHERE xmltv_id = 'filled.example'").Scan(&icon); err != nil ||
		icon != "http://logos.example/filled.png" {
		t.Errorf("the icon %q, %v", icon, err)
	}

	// A channel that the guide stops listing is kept a while, missing,
	// and then forgotten with its programmes and matches.
	if _, err := s.SetGuideMatches(ctx, []GuideMatch{{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"]}}); err != nil {
		t.Fatal(err)
	}
	readGuide(t, s, g, later.Add(time.Hour), 0, listed[:1], []string{"filled.example"}, func(ids map[string]int64) []GuideMatch {
		return []GuideMatch{{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"]}}
	})
	if listed, err := s.ListedChannels(ctx); err != nil || len(listed) != 1 {
		t.Errorf("a missing channel is listed: %+v, %v", listed, err)
	}
	var n int
	if err := s.db.QueryRow("SELECT count(*) FROM guide_channels WHERE missing_since IS NOT NULL").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d missing channels, %v", n, err)
	}
	readGuide(t, s, g, later.Add(MissingGrace+2*time.Hour), 0, listed[:1], nil, func(map[string]int64) []GuideMatch { return nil })
	if err := s.db.QueryRow("SELECT count(*) FROM guide_channels").Scan(&n); err != nil || n != 1 {
		t.Errorf("%d channels after the grace, %v", n, err)
	}

	// Removing the guide takes its channels, programmes and matches along.
	if err := s.DeleteGuide(ctx, g); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"guide_channels", "guide_programmes", "channel_guide_matches"} {
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Errorf("%s: %d rows, %v", table, n, err)
		}
	}
}

// TestGuideReadAbort checks that a failed read keeps the guide as it was.
func TestGuideReadAbort(t *testing.T) {
	s, src, _ := lineupSetup(t)
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	apply(t, s, src, at, grouped("Filled/News"))
	filled := entryNamed(t, s, "Filled")
	g, err := s.AddGuide(ctx, GuideSettings{Name: "extra", Address: "http://guide.example/"}, at)
	if err != nil {
		t.Fatal(err)
	}
	listed := []GuideListing{{XMLTVID: "filled.example", Names: []string{"Filled"}}}
	readGuide(t, s, g, at, 0, listed, []string{"filled.example"}, func(ids map[string]int64) []GuideMatch {
		return []GuideMatch{{ChannelID: filled.ChannelID, GuideChannelID: ids["filled.example"]}}
	}, at.Add(time.Hour))
	w, err := s.BeginGuideRead(ctx, g)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ListChannels(ctx, listed, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := w.Add(ctx, GuideProgramme{ChannelID: 1, Start: at, Stop: at.Add(time.Hour), Title: "Half a read"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Abort(ctx, at.Add(time.Hour), "from", "the guide broke off"); err != nil {
		t.Fatal(err)
	}
	ps, err := s.EntryProgrammes(ctx, DefaultLineupID, filled.EntryID, at, at.Add(24*time.Hour))
	if err != nil || len(ps) != 1 || ps[0].Title != "filled.example at 07:00" {
		t.Errorf("after a failed read: %+v, %v", ps, err)
	}
	if gd, _ := s.Guide(ctx, g); gd.ReadError != "the guide broke off" || gd.ReadFrom != "from" || gd.Programmes != 1 {
		t.Errorf("the guide after a failed read: %+v", gd)
	}
}

// TestSourceGuideFills counts, by source, the channels that its own guide
// gives programmes, leaving out those that moved or that it no longer
// lists.
func TestSourceGuideFills(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("One/News", "Two/News", "Three/News", "Four/News"))
	one, two, three := entryNamed(t, s, "One"), entryNamed(t, s, "Two"), entryNamed(t, s, "Three")
	if fills, err := s.SourceGuideFills(ctx); err != nil || fills[src] != (SourceGuideFill{Channels: 4}) {
		t.Fatalf("without a guide: %+v, %v", fills, err)
	}
	writeGuide(t, s, src, at, 0, []int64{one.ChannelID, two.ChannelID, three.ChannelID}, at.Add(time.Hour))
	if fills, err := s.SourceGuideFills(ctx); err != nil || fills[src] != (SourceGuideFill{Channels: 4, Filled: 3}) {
		t.Fatalf("with a guide: %+v, %v", fills, err)
	}
	// Three moves to another source's channel, and the source no longer
	// lists Two.
	other := addSources(t, s, testSource("other", 2, NumbersPlaylist))["other"]
	apply(t, s, other, at, grouped("Three/News"))
	if _, err := s.db.ExecContext(ctx, `UPDATE channels SET moved_to = (SELECT id FROM channels WHERE source_id = ?) WHERE id = ?`,
		other, three.ChannelID); err != nil {
		t.Fatal(err)
	}
	apply(t, s, src, at.Add(time.Minute), grouped("One/News", "Three/News", "Four/News"))
	fills, err := s.SourceGuideFills(ctx)
	if err != nil || fills[src] != (SourceGuideFill{Channels: 2, Filled: 1}) || fills[other] != (SourceGuideFill{Channels: 1}) {
		t.Errorf("after a move and a channel gone: %+v, %v", fills, err)
	}
}
