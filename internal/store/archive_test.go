// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/source"
)

// archiveDay is when the archive tests' programmes are on.
var archiveDay = time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)

// entryNamed returns the lineup's channel of the given name.
func entryNamed(t *testing.T, s *Store, name string) LineupChannel {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no channel %q", name)
	return LineupChannel{}
}

// writeGuide writes a source's guide: an hour-long programme on each channel
// at each start.
func writeGuide(t *testing.T, s *Store, sourceID int64, at time.Time, keep time.Duration, channels []int64, starts ...time.Time) {
	t.Helper()
	ctx := context.Background()
	w, err := s.BeginGuide(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range channels {
		for _, start := range starts {
			if err := w.Add(ctx, GuideProgramme{ChannelID: ch, Start: start, Stop: start.Add(time.Hour),
				Title: "On at " + start.Format("15:04")}); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.KeepPast(keep)
	if err := w.Commit(ctx, at, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestPlayableArchive(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	xc := xtreamSource("main")
	xc.CatchupZone = ""
	ids := addSources(t, s, xc, source.Settings{Name: "one", Type: source.TypeM3U, Numbers: NumbersPlaylist,
		CatchupZone: "America/New_York"})
	if _, err := s.ApplyCatalog(ctx, ids["main"], []CatalogChannel{
		{IdentityKey: "xtream:12", Name: "Twelve", StreamURL: "live/12.ts", ArchiveDays: 3},
		{IdentityKey: "xtream:13", Name: "Thirteen", StreamURL: "live/13.ts"},
	}, archiveDay); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts", ArchiveDays: 2,
			Catchup: catchup.Playlist{Mode: catchup.ModeAppend, Source: "?utc={utc}&key=s3cret"}},
		{IdentityKey: "b", Name: "Bravo", StreamURL: "http://streams.example/b.ts", ArchiveDays: 5},
	}, archiveDay); err != nil {
		t.Fatal(err)
	}
	// The server says its zone once it is read.
	if _, err := s.SetSourceAccount(ctx, ids["main"], SourceAccount{Status: "Active", TimeZone: "Europe/Berlin"}); err != nil {
		t.Fatal(err)
	}

	// Devices see the days of an archive that plays, and none for one that
	// Overscan cannot play.
	for name, want := range map[string]int{"Twelve": 3, "Thirteen": 0, "Alpha": 2, "Bravo": 0} {
		if got := entryNamed(t, s, name).ArchiveDays; got != want {
			t.Errorf("%s: %d days, want %d", name, got, want)
		}
	}

	twelve := entryNamed(t, s, "Twelve")
	a, err := s.PlayableArchive(ctx, twelve.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Days != 3 || a.Archive.Xtream == nil || a.Archive.Zone.String() != "Europe/Berlin" ||
		a.StreamURL != "http://provider.example:8080/live/viewer/pw%2Fmain/12.ts" {
		t.Errorf("Xtream: %+v %+v", a, a.Archive.Xtream)
	}
	if got, err := a.Archive.Address(archiveDay, time.Hour, archiveDay.Add(2*time.Hour)); err != nil ||
		got != "http://provider.example:8080/timeshift/viewer/pw%2Fmain/60/2026-12-04:19-00/12.ts" {
		t.Errorf("Xtream's address: %s, %v", got, err)
	}

	// The administrator's zone wins over the server's.
	settings, err := s.SourceSettings(ctx, ids["main"])
	if err != nil {
		t.Fatal(err)
	}
	settings.CatchupZone = "UTC"
	if err := s.UpdateSource(ctx, ids["main"], settings); err != nil {
		t.Fatal(err)
	}
	if a, err := s.PlayableArchive(ctx, twelve.EntryID); err != nil || a.Archive.Zone.String() != "UTC" {
		t.Errorf("set to UTC: %v, %v", a.Archive.Zone, err)
	}

	alpha := entryNamed(t, s, "Alpha")
	a, err = s.PlayableArchive(ctx, alpha.EntryID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Days != 2 || a.Archive.Xtream != nil || a.Archive.Live != "http://streams.example/a.ts" ||
		a.Archive.Playlist.Source != "?utc={utc}&key=s3cret" || a.Archive.Zone.String() != "America/New_York" {
		t.Errorf("playlist: %+v", a)
	}
	// The catch-up source is sealed, as stream addresses are.
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT catchup_secret FROM channels WHERE identity_key = 'a'").Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if len(sealed) == 0 || containsBytes(sealed, "s3cret") {
		t.Errorf("catch-up source stored as %q", sealed)
	}

	for _, name := range []string{"Thirteen", "Bravo"} {
		if _, err := s.PlayableArchive(ctx, entryNamed(t, s, name).EntryID); !errors.Is(err, ErrNoArchive) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.PlayableArchive(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
	if days, err := s.EntryArchiveDays(ctx, DefaultLineupID, alpha.EntryID); err != nil || days != 2 {
		t.Errorf("Alpha's days: %d, %v", days, err)
	}
	if days, err := s.EntryArchiveDays(ctx, DefaultLineupID, 9999); err != nil || days != 0 {
		t.Errorf("an unknown entry's days: %d, %v", days, err)
	}

	// A refresh that no longer describes the catch-up leaves none.
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts", ArchiveDays: 2},
	}, archiveDay.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlayableArchive(ctx, alpha.EntryID); !errors.Is(err, ErrNoArchive) {
		t.Errorf("without its catch-up: %v", err)
	}
}

// A Stalker portal's channel with an archive plays it through the portal,
// which gives the link of a past programme for the portal's own ID for the
// channel (D84).
func TestPlayableArchiveOfAPortal(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, source.Settings{Name: "main", Type: source.TypeStalker, URL: "http://portal.example/c/",
		Device: source.Device{MAC: "00:1A:79:00:00:01"}, Numbers: NumbersPlaylist})
	if _, err := s.ApplyCatalog(ctx, ids["main"], []CatalogChannel{
		{IdentityKey: source.StalkerKeyPrefix + "102", Name: "Charlie", StreamURL: "ffrt http://localhost/ch/102_", ArchiveDays: 7},
		{IdentityKey: source.StalkerKeyPrefix + "103", Name: "Delta", StreamURL: "ffrt http://localhost/ch/103_"},
	}, archiveDay); err != nil {
		t.Fatal(err)
	}
	charlie := entryNamed(t, s, "Charlie")
	if charlie.ArchiveDays != 7 {
		t.Errorf("devices see %d days", charlie.ArchiveDays)
	}
	a, err := s.PlayableArchive(ctx, charlie.EntryID)
	if err != nil || a.Portal != "102" || a.Days != 7 || a.Archive.Xtream != nil || a.Archive.Live != "" ||
		a.StreamURL != "ffrt http://localhost/ch/102_" {
		t.Errorf("Charlie: %+v, %v", a, err)
	}
	if _, err := s.PlayableArchive(ctx, entryNamed(t, s, "Delta").EntryID); !errors.Is(err, ErrNoArchive) {
		t.Errorf("Delta: %v", err)
	}
}

func containsBytes(b []byte, s string) bool {
	for i := 0; i+len(s) <= len(b); i++ {
		if string(b[i:i+len(s)]) == s {
			return true
		}
	}
	return false
}

func TestProgrammeAt(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist))
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts"},
	}, archiveDay); err != nil {
		t.Fatal(err)
	}
	alpha := entryNamed(t, s, "Alpha")
	writeGuide(t, s, ids["one"], archiveDay, 0, []int64{alpha.ChannelID}, archiveDay, archiveDay.Add(time.Hour+30*time.Second))

	p, err := s.ProgrammeAt(ctx, DefaultLineupID, alpha.EntryID, archiveDay)
	if err != nil || p.Title != "On at 18:00" || !p.Stop.Equal(archiveDay.Add(time.Hour)) {
		t.Errorf("at 18:00: %+v, %v", p, err)
	}
	if _, err := s.ProgrammeAt(ctx, DefaultLineupID, alpha.EntryID, archiveDay.Add(time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Errorf("when none starts: %v", err)
	}
	minute := archiveDay.Add(time.Hour)
	if p, err := s.ProgrammeStarting(ctx, DefaultLineupID, alpha.EntryID, minute, minute.Add(time.Minute)); err != nil ||
		p.Title != "On at 19:00" {
		t.Errorf("in the minute: %+v, %v", p, err)
	}
	if _, err := s.ProgrammeAt(ctx, 2, alpha.EntryID, archiveDay); !errors.Is(err, ErrNotFound) {
		t.Errorf("another lineup: %v", err)
	}
}

func TestInArchive(t *testing.T) {
	now := archiveDay.Add(48 * time.Hour)
	p := Programme{Start: archiveDay, Stop: archiveDay.Add(time.Hour)}
	for _, tt := range []struct {
		days int
		now  time.Time
		want bool
	}{
		{3, now, true},
		{2, now, false}, // it began two days and more ago
		{0, now, false},
		{3, archiveDay.Add(30 * time.Minute), false}, // on now
		{3, archiveDay.Add(time.Hour), true},         // just ended
	} {
		if got := InArchive(p, tt.days, tt.now); got != tt.want {
			t.Errorf("%d days at %s: %v", tt.days, tt.now, got)
		}
	}
}

// TestGuideKeepsThePast checks that a read of a guide keeps the past
// programmes of the read before that it lacks, for as long as the channel's
// archive reaches back, or a day (D83).
func TestGuideKeepsThePast(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist))
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts", ArchiveDays: 3},
		{IdentityKey: "b", Name: "Bravo", StreamURL: "http://streams.example/b.ts"},
	}, archiveDay); err != nil {
		t.Fatal(err)
	}
	alpha, bravo := entryNamed(t, s, "Alpha"), entryNamed(t, s, "Bravo")
	both := []int64{alpha.ChannelID, bravo.ChannelID}
	day0 := archiveDay.Add(-48 * time.Hour)
	// The first read lists the day before yesterday, yesterday and today.
	var starts []time.Time
	for h := 0; h < 72; h += 6 {
		starts = append(starts, day0.Add(time.Duration(h)*time.Hour))
	}
	writeGuide(t, s, ids["one"], day0, 24*time.Hour, both, starts...)
	// The next lists from an hour ago on, the hour at 17:00 among them.
	now := archiveDay.Add(30 * time.Minute)
	writeGuide(t, s, ids["one"], now, 24*time.Hour, both, archiveDay.Add(-time.Hour), archiveDay.Add(time.Hour))

	count := func(c LineupChannel) int {
		ps, err := s.EntryProgrammes(ctx, DefaultLineupID, c.EntryID, day0.Add(-time.Hour), now.Add(48*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return len(ps)
	}
	// Alpha keeps three days: everything of the first read that ended
	// before 17:00 (8 of its programmes) and the second read's two.
	if got := count(alpha); got != 10 {
		t.Errorf("Alpha has %d programmes, want 10", got)
	}
	// Bravo keeps a day: what ended after 18:30 yesterday (yesterday's
	// 18:00, and today's 00:00, 06:00 and 12:00) and the second read's two.
	if got := count(bravo); got != 6 {
		t.Errorf("Bravo has %d programmes, want 6", got)
	}
	g, err := s.SourceGuide(ctx, ids["one"])
	if err != nil || g.Programmes != 16 || g.Channels != 2 {
		t.Errorf("guide state %+v, %v", g, err)
	}
	// A read that keeps nothing of the past drops it.
	writeGuide(t, s, ids["one"], now, 0, both, archiveDay.Add(time.Hour))
	if got := count(alpha); got != 1 {
		t.Errorf("without keeping the past, Alpha has %d programmes", got)
	}

	// The past of a channel that its source no longer lists, or no longer
	// brings (D135), is not carried over: it has no guide.
	writeGuide(t, s, ids["one"], now, 24*time.Hour, both, archiveDay.Add(-time.Hour), archiveDay.Add(time.Hour))
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts", ArchiveDays: 3},
	}, now); err != nil {
		t.Fatal(err)
	}
	writeGuide(t, s, ids["one"], now.Add(time.Hour), 24*time.Hour, []int64{alpha.ChannelID}, archiveDay.Add(2*time.Hour))
	if g, err := s.SourceGuide(ctx, ids["one"]); err != nil || g.Channels != 1 || g.Programmes != 2 {
		t.Errorf("guide state without Bravo %+v, %v", g, err)
	}
}
