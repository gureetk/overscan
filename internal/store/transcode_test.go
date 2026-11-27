// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"overscan.gureet.ca/internal/streamprofile"
)

func madeProfile(t *testing.T, p streamprofile.Profile) streamprofile.Profile {
	t.Helper()
	p, err := streamprofile.Check(p)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// The profiles that the administrator makes: added, read, changed with a
// new name that carries over to what names them, and removed once nothing
// names them (D147).
func TestTranscodeProfiles(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist))
	apply(t, s, ids["one"], at, grouped("Alpha/News", "Bravo/News"))
	alpha := entryIn(t, s, DefaultLineupID, "Alpha")
	d, err := s.ApproveDevice(ctx, "00:1A:79:00:00:01", HouseholdAccount, "", at)
	if err != nil {
		t.Fatal(err)
	}

	if list, err := s.TranscodeProfiles(ctx); err != nil || len(list) != 0 {
		t.Fatalf("at first: %+v, %v", list, err)
	}
	hevc := madeProfile(t, streamprofile.Profile{Name: "hevc-small", Codec: streamprofile.HEVC, MaxLines: 720,
		Quality: streamprofile.Low, MaxKbps: 3000, FrameRate: streamprofile.RateAtMost, Deinterlace: true, ToneMap: true,
		Sound: streamprofile.SoundAC3})
	if err := s.AddTranscodeProfile(ctx, hevc); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTranscodeProfile(ctx, hevc); !errors.Is(err, ErrProfileExists) {
		t.Errorf("the same name again: %v", err)
	}
	if err := s.AddTranscodeProfile(ctx, streamprofile.Profile{Name: "h264-720p", Made: true}); !errors.Is(err, ErrProfileExists) {
		t.Errorf("a fixed profile's name: %v", err)
	}
	got, err := s.TranscodeProfile(ctx, "hevc-small")
	if err != nil || got != hevc {
		t.Errorf("read back %+v, %v; want %+v", got, err, hevc)
	}
	if p, err := s.TranscodeProfile(ctx, "h264-1080p"); err != nil || p.Made || !p.Transcodes() {
		t.Errorf("a fixed one: %+v, %v", p, err)
	}
	if _, err := s.TranscodeProfile(ctx, "nobody"); !errors.Is(err, ErrNoSuchProfile) {
		t.Errorf("no such profile: %v", err)
	}

	// Everything can name it.
	if err := s.SetAccountOutputProfile(ctx, HouseholdAccount, "hevc-small"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDeviceOutputProfile(ctx, d.MAC, "hevc-small"); err != nil {
		t.Fatal(err)
	}
	if p, err := s.OutputProfileOf(ctx, d.ID); err != nil || p.Name != "hevc-small" || !p.Made {
		t.Errorf("the device's: %+v, %v", p, err)
	}
	if err := s.SetEntriesTranscode(ctx, DefaultLineupID, []int64{alpha.ID}, Transcode{Profile: "hevc-small"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLineupGroupTranscode(ctx, DefaultLineupID, alpha.GroupID, Transcode{Profile: "hevc-small"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLiveTranscode(ctx, Transcode{Profile: "hevc-small"}); err != nil {
		t.Fatal(err)
	}
	if a, d, err := s.OutputProfilesInUse(ctx); err != nil || a != 1 || d != 1 {
		t.Errorf("in use: %d accounts, %d devices, %v", a, d, err)
	}
	uses, err := s.TranscodeProfileUses(ctx, "hevc-small")
	want := ProfileUses{Accounts: 1, Devices: 1, Channels: 1, Groups: 1, LiveTV: true}
	if err != nil || uses != want || uses.String() != "1 account, 1 device, 1 channel, 1 group and live TV" {
		t.Errorf("uses %+v (%s), %v", uses, uses, err)
	}
	if err := s.RemoveTranscodeProfile(ctx, "hevc-small"); !errors.Is(err, ErrProfileInUse) {
		t.Errorf("removing it in use: %v", err)
	}

	// A new name carries over.
	renamed := hevc
	renamed.Name, renamed.Label, renamed.Quality = "hevc-low", "hevc-low", streamprofile.Medium
	if err := s.ChangeTranscodeProfile(ctx, "hevc-small", renamed); err != nil {
		t.Fatal(err)
	}
	if p, err := s.OutputProfileOf(ctx, d.ID); err != nil || p != renamed {
		t.Errorf("the device's, renamed: %+v, %v", p, err)
	}
	if p, err := s.AccountOutputProfile(ctx, HouseholdAccount); err != nil || p.Name != "hevc-low" {
		t.Errorf("the account's, renamed: %+v, %v", p, err)
	}
	e := entryIn(t, s, DefaultLineupID, "Alpha")
	if e.TranscodeOwn.Profile != "hevc-low" || e.Transcode.Profile != "hevc-low" || e.TranscodeBy != TranscodeByChannel {
		t.Errorf("the channel's, renamed: %+v %+v %q", e.TranscodeOwn, e.Transcode, e.TranscodeBy)
	}
	if live, err := s.LiveTranscode(ctx); err != nil || live.Profile != "hevc-low" {
		t.Errorf("live TV's, renamed: %+v, %v", live, err)
	}
	if uses, _ := s.TranscodeProfileUses(ctx, "hevc-small"); uses.Any() {
		t.Errorf("the old name is still used: %+v", uses)
	}
	if err := s.ChangeTranscodeProfile(ctx, "h264-720p", renamed); !errors.Is(err, ErrProfileFixed) {
		t.Errorf("changing a fixed one: %v", err)
	}
	if err := s.ChangeTranscodeProfile(ctx, "nobody", renamed); !errors.Is(err, ErrNotFound) {
		t.Errorf("changing none: %v", err)
	}
	other := madeProfile(t, streamprofile.Profile{Name: "other"})
	if err := s.AddTranscodeProfile(ctx, other); err != nil {
		t.Fatal(err)
	}
	clash := renamed
	clash.Name = "other"
	if err := s.ChangeTranscodeProfile(ctx, "hevc-low", clash); !errors.Is(err, ErrProfileExists) {
		t.Errorf("renaming to another's name: %v", err)
	}

	// Removed once nothing names it.
	if err := s.SetAccountOutputProfile(ctx, HouseholdAccount, streamprofile.Source); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDeviceOutputProfile(ctx, d.MAC, ""); err != nil {
		t.Fatal(err)
	}
	for _, f := range []func() error{
		func() error { return s.SetEntriesTranscode(ctx, DefaultLineupID, []int64{alpha.ID}, Transcode{}) },
		func() error {
			return s.SetLineupGroupTranscode(ctx, DefaultLineupID, alpha.GroupID, Transcode{Never: true})
		},
		func() error { return s.SetLiveTranscode(ctx, Transcode{}) },
	} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RemoveTranscodeProfile(ctx, "hevc-low"); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.TranscodeProfiles(ctx); len(list) != 1 || list[0].Name != "other" {
		t.Errorf("left: %+v", list)
	}
	if err := s.RemoveTranscodeProfile(ctx, "hevc-low"); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing it again: %v", err)
	}
	if err := s.RemoveTranscodeProfile(ctx, "source"); !errors.Is(err, ErrProfileFixed) {
		t.Errorf("removing a fixed one: %v", err)
	}
	c, err := s.TranscodeCounts(ctx)
	if err != nil || c != (TranscodeCounts{Profiles: 1, GroupsNever: 1}) {
		t.Errorf("counts %+v, %v", c, err)
	}

	// Settings name only profiles that exist and transcode.
	for _, tr := range []Transcode{{Profile: "nobody"}, {Profile: streamprofile.Source}} {
		if err := s.SetLiveTranscode(ctx, tr); err == nil {
			t.Errorf("live TV took %+v", tr)
		}
		if err := s.SetLineupGroupTranscode(ctx, DefaultLineupID, alpha.GroupID, tr); err == nil {
			t.Errorf("a group took %+v", tr)
		}
		if err := s.SetEntriesTranscode(ctx, DefaultLineupID, []int64{alpha.ID}, tr); err == nil {
			t.Errorf("a channel took %+v", tr)
		}
	}
	if err := s.SetAccountOutputProfile(ctx, HouseholdAccount, "nobody"); !errors.Is(err, ErrNoSuchProfile) {
		t.Errorf("an account took no such profile: %v", err)
	}
	for i := 1; i < MaxTranscodeProfiles; i++ {
		if err := s.AddTranscodeProfile(ctx, madeProfile(t, streamprofile.Profile{Name: "p" + string(rune('a'+i%26)) + string(rune('a'+i/26))})); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.AddTranscodeProfile(ctx, madeProfile(t, streamprofile.Profile{Name: "one-too-many"})); !errors.Is(err, ErrTooManyTranscodeProfiles) {
		t.Errorf("one too many: %v", err)
	}
}

// The narrowest setting wins: a channel's, then its group's, then live
// TV's; a backup's channel follows its own, then the entry's group's and
// live TV's (D147).
func TestTranscodePrecedence(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersPlaylist))
	apply(t, s, ids["one"], at, grouped("Alpha/News", "Bravo/News", "Delta/Sport"))
	apply(t, s, ids["two"], at, grouped("Alpha HD/Films"))
	alpha, bravo, delta := entryIn(t, s, DefaultLineupID, "Alpha"), entryIn(t, s, DefaultLineupID, "Bravo"), entryIn(t, s, DefaultLineupID, "Delta")
	hd := entryIn(t, s, DefaultLineupID, "Alpha HD")
	if err := s.SetEntryBackups(ctx, DefaultLineupID, alpha.ID, []int64{hd.ID}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddTranscodeProfile(ctx, madeProfile(t, streamprofile.Profile{Name: "small", MaxLines: 576})); err != nil {
		t.Fatal(err)
	}
	playable := func(id int64) PlayableEntry {
		t.Helper()
		p, err := s.PlayableEntry(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	check := func(what string, p PlayableEntry, want Transcode, by string) {
		t.Helper()
		if p.Transcode != want || p.TranscodeBy != by {
			t.Errorf("%s: %+v by %q, want %+v by %q", what, p.Transcode, p.TranscodeBy, want, by)
		}
	}

	// At first, as needed everywhere: nothing says anything.
	check("Alpha at first", playable(alpha.ID), Transcode{}, "")
	check("its backup at first", playable(alpha.ID).Backups[0], Transcode{}, "")
	if e := entryIn(t, s, DefaultLineupID, "Alpha"); !e.Transcode.AsNeeded() || !e.TranscodeOwn.AsNeeded() || e.TranscodeBy != "" {
		t.Errorf("the admin's entry at first: %+v", e)
	}

	if err := s.SetLiveTranscode(ctx, Transcode{Profile: "h264-720p"}); err != nil {
		t.Fatal(err)
	}
	check("live TV's", playable(bravo.ID), Transcode{Profile: "h264-720p"}, TranscodeByLive)
	if err := s.SetLineupGroupTranscode(ctx, DefaultLineupID, alpha.GroupID, Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	check("the group's over live TV's", playable(bravo.ID), Transcode{Profile: "small"}, TranscodeByGroup)
	check("another group keeps live TV's", playable(delta.ID), Transcode{Profile: "h264-720p"}, TranscodeByLive)
	if err := s.SetEntriesTranscode(ctx, DefaultLineupID, []int64{alpha.ID}, Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	p := playable(alpha.ID)
	check("the channel's over its group's", p, Transcode{Never: true}, TranscodeByChannel)
	check("its backup follows the entry's group", p.Backups[0], Transcode{Profile: "small"}, TranscodeByGroup)
	if err := s.SetEntriesTranscode(ctx, DefaultLineupID, []int64{hd.ID}, Transcode{Profile: "h264-1080p"}); err != nil {
		t.Fatal(err)
	}
	check("a backup's own channel's", playable(alpha.ID).Backups[0], Transcode{Profile: "h264-1080p"}, TranscodeByChannel)
	e := entryIn(t, s, DefaultLineupID, "Alpha")
	if !e.TranscodeOwn.Never || !e.Transcode.Never || e.TranscodeBy != TranscodeByChannel {
		t.Errorf("the admin's entry: %+v %+v %q", e.TranscodeOwn, e.Transcode, e.TranscodeBy)
	}
	if e := entryIn(t, s, DefaultLineupID, "Bravo"); !e.TranscodeOwn.AsNeeded() || e.Transcode.Profile != "small" || e.TranscodeBy != TranscodeByGroup {
		t.Errorf("the admin's Bravo: %+v %+v %q", e.TranscodeOwn, e.Transcode, e.TranscodeBy)
	}
	groups, err := s.LineupGroups(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if want := g.ID == alpha.GroupID; (g.Transcode.Profile == "small") != want {
			t.Errorf("group %s: %+v", g.Name, g.Transcode)
		}
	}

	// A channel's setting is its source's, in every lineup.
	kids, err := s.CopyLineup(ctx, DefaultLineupID, "Kids")
	if err != nil {
		t.Fatal(err)
	}
	if e := entryIn(t, s, kids, "Alpha"); !e.TranscodeOwn.Never {
		t.Errorf("the copy's Alpha: %+v", e.TranscodeOwn)
	}
	// A refresh keeps them.
	apply(t, s, ids["one"], at.Add(time.Hour), grouped("Alpha/News", "Bravo/News", "Delta/Sport"))
	check("after a refresh", playable(alpha.ID), Transcode{Never: true}, TranscodeByChannel)
	check("Bravo after a refresh", playable(bravo.ID), Transcode{Profile: "small"}, TranscodeByGroup)
}

// A group's setting goes into lineup files, and comes back on import; one
// that names a profile that the server lacks is left out and counted
// (D147).
func TestTranscodeInLineupFiles(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist))
	apply(t, s, ids["one"], at, grouped("Alpha/News", "Bravo/Sport", "Delta/Films"))
	if err := s.AddTranscodeProfile(ctx, madeProfile(t, streamprofile.Profile{Name: "small", MaxLines: 576})); err != nil {
		t.Fatal(err)
	}
	set := map[string]Transcode{"Alpha": {Profile: "small"}, "Bravo": {Never: true}, "Delta": {Profile: "h264-720p"}}
	for name, tr := range set {
		if err := s.SetLineupGroupTranscode(ctx, DefaultLineupID, entryIn(t, s, DefaultLineupID, name).GroupID, tr); err != nil {
			t.Fatal(err)
		}
	}
	f, err := s.ExportLineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Needs() != 4 {
		t.Errorf("the file needs version %d", f.Needs())
	}
	got := map[string]string{}
	for _, g := range f.Lineups[0].Groups {
		got[g.Name] = g.Transcode
	}
	if got["News"] != "small" || got["Sport"] != "never" || got["Films"] != "h264-720p" {
		t.Errorf("exported %v", got)
	}

	// Imported where the profile is gone: as needed, and counted.
	f.Lineups[0].Name = "Copy"
	if err := s.ChangeTranscodeProfile(ctx, "small", madeProfile(t, streamprofile.Profile{Name: "tiny", MaxLines: 576})); err != nil {
		t.Fatal(err)
	}
	res, err := s.ImportLineups(ctx, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].LeftOut.Transcodes != 1 || res[0].LeftOut.Total() != 1 {
		t.Errorf("imported %+v", res)
	}
	groups, err := s.LineupGroups(ctx, res[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		want := map[string]Transcode{"Sport": {Never: true}, "Films": {Profile: "h264-720p"}}[g.Name]
		if g.Transcode != want {
			t.Errorf("imported %s: %+v, want %+v", g.Name, g.Transcode, want)
		}
	}
}

func TestGPUTranscodes(t *testing.T) {
	st := openTest(t, newPath(t))
	ctx := context.Background()
	if n, err := st.GPUTranscodes(ctx); err != nil || n != DefaultGPUTranscodes {
		t.Fatalf("GPUTranscodes() = %d, %v; want %d", n, err, DefaultGPUTranscodes)
	}
	for _, n := range []int{0, MaxGPUTranscodes, 1} {
		if err := st.SetGPUTranscodes(ctx, n); err != nil {
			t.Fatal(err)
		}
		if got, err := st.GPUTranscodes(ctx); err != nil || got != n {
			t.Errorf("after %d: %d, %v", n, got, err)
		}
	}
	for _, n := range []int{-1, MaxGPUTranscodes + 1} {
		if err := st.SetGPUTranscodes(ctx, n); err == nil {
			t.Errorf("SetGPUTranscodes(%d) took it", n)
		}
	}
}

// What is transcoded of films and series (D150): a title's own, then its
// shelf's, then films' and series'; a profile that they name is in use,
// keeps them when renamed, and cannot be removed.
func TestTitleTranscode(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	for _, q := range []string{`
		INSERT INTO sources (id, name, type, host, enabled, max_connections, refresh_seconds, numbers,
				new_categories_hidden, public_only, refreshed_at, refresh_error)
			VALUES (3, 'main', 'xtream', 'provider.example', 1, 2, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', ''),
			       (4, 'portal', 'stalker', 'portal.example', 1, 1, 43200, 'playlist', 0, 0, '2026-12-04T03:00:00Z', '')`, `
		INSERT INTO library_categories (id, source_id, kind, source_key, name, position, generation)
			VALUES (20, 3, 'movie', '1', 'Drama', 1, 1), (21, 4, 'movie', '7', 'Drama', 1, 1)`, `
		INSERT INTO titles (id, source_id, kind, source_key, category_id, name, position, generation)
			VALUES (30, 3, 'movie', '100', 20, 'Quiet Harbour', 1, 1),
			       (31, 3, 'movie', '101', 20, 'Copper Field', 2, 1),
			       (32, 4, 'movie', '102', 21, 'Granite Peak', 1, 1),
			       (33, 3, 'movie', '103', NULL, 'Loose Ends', 3, 1)`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	small, _ := streamprofile.Check(streamprofile.Profile{Name: "small", MaxLines: 576})
	if err := s.AddTranscodeProfile(ctx, small); err != nil {
		t.Fatal(err)
	}
	want := func(id int64, tc Transcode, by string) {
		t.Helper()
		got, gotBy, err := s.TitleTranscode(ctx, id)
		if err != nil || got != tc || gotBy != by {
			t.Errorf("title %d: %+v by %q, %v; want %+v by %q", id, got, gotBy, err, tc, by)
		}
	}
	want(30, Transcode{}, "")

	// Films and series, then a shelf, then a title.
	if err := s.SetVODTranscode(ctx, Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	want(33, Transcode{Profile: "small"}, TranscodeByVOD)
	if err := s.SetShelfTranscode(ctx, KindMovie, "name:Drama", Transcode{Never: true}); err != nil {
		t.Fatal(err)
	}
	want(30, Transcode{Never: true}, TranscodeByShelf)
	want(32, Transcode{Profile: "small"}, TranscodeByVOD) // a portal's category is a shelf of its own
	if n, err := s.SetTitlesTranscode(ctx, []int64{31, 99}, Transcode{Profile: "h264-720p"}); err != nil || n != 1 {
		t.Fatalf("set titles: %d, %v", n, err)
	}
	want(31, Transcode{Profile: "h264-720p"}, TranscodeByTitle)
	want(30, Transcode{Never: true}, TranscodeByShelf)
	if _, err := s.SetTitlesTranscode(ctx, []int64{30}, Transcode{Profile: "source"}); !errors.Is(err, ErrNotTranscoding) {
		t.Errorf("source: %v", err)
	}
	if _, err := s.SetTitlesTranscode(ctx, []int64{30}, Transcode{Profile: "nobody"}); !errors.Is(err, ErrNoSuchProfile) {
		t.Errorf("nobody: %v", err)
	}
	if _, _, err := s.TitleTranscode(ctx, 99); !errors.Is(err, ErrNotFound) {
		t.Errorf("no title: %v", err)
	}

	// The shelf keeps its setting when another is dropped.
	if err := s.SetShelfHidden(ctx, KindMovie, "name:Drama", true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetShelfHidden(ctx, KindMovie, "name:Drama", false); err != nil {
		t.Fatal(err)
	}
	if err := s.PlaceShelves(ctx, KindMovie, nil); err != nil {
		t.Fatal(err)
	}
	want(30, Transcode{Never: true}, TranscodeByShelf)
	if err := s.SetShelfTranscode(ctx, KindMovie, "name:Drama", Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}

	// The profile's uses, its rename and its removal.
	u, err := s.TranscodeProfileUses(ctx, "small")
	if err != nil || u != (ProfileUses{Shelves: 1, FilmsAndSeries: true}) || u.String() != "1 shelf and every film and series" {
		t.Errorf("uses: %+v %q, %v", u, u.String(), err)
	}
	if _, err := s.SetTitlesTranscode(ctx, []int64{33}, Transcode{Profile: "small"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveTranscodeProfile(ctx, "small"); !errors.Is(err, ErrProfileInUse) {
		t.Errorf("remove: %v", err)
	}
	tiny := small
	tiny.Name = "tiny"
	if err := s.ChangeTranscodeProfile(ctx, "small", tiny); err != nil {
		t.Fatal(err)
	}
	want(33, Transcode{Profile: "tiny"}, TranscodeByTitle)
	want(30, Transcode{Profile: "tiny"}, TranscodeByShelf)
	if vod, err := s.VODTranscode(ctx); err != nil || vod.Profile != "tiny" {
		t.Errorf("films and series after the rename: %+v, %v", vod, err)
	}
	shelves, err := s.LibraryShelves(ctx, KindMovie)
	if err != nil || len(shelves) == 0 || shelves[0].Key != "name:Drama" || shelves[0].Transcode.Profile != "tiny" {
		t.Errorf("shelves: %+v, %v", shelves, err)
	}
	c, err := s.TranscodeCounts(ctx)
	if err != nil || c.TitlesAlways != 2 || c.ShelvesAlways != 1 || c.TitlesNever != 0 {
		t.Errorf("counts: %+v, %v", c, err)
	}
}
