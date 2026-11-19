// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"
	"time"
)

// lockedNames returns the names of the titles that rows says are locked.
func lockedNames(rows []TitleRow) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		if r.Locked {
			out[r.Name] = true
		}
	}
	return out
}

// Films and series can be locked one by one or by their shelf (D85), and a
// source's marks for adults lock or hide what they mark as the setting
// says (D86).
func TestTitleLocksAndAdultMarks(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	marked := append([]LibraryTitle(nil), films...)
	marked[2].Adult = true // Around 100% of_it, on Drama
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, marked, at); err != nil {
		t.Fatal(err)
	}
	titles := titleIDs(t, s)
	shelves, err := s.Shelves(ctx, KindMovie)
	if err != nil {
		t.Fatal(err)
	}
	drama, comedy := shelves[0], shelves[1]
	if drama.Name != "Drama" || comedy.Name != "Comedy" {
		t.Fatalf("shelves %+v", shelves)
	}
	all := func() map[string]bool {
		t.Helper()
		rows, total, err := s.TitlePage(ctx, KindMovie, 0, "", ByName, 0, 10, false)
		if err != nil {
			t.Fatal(err)
		}
		out := lockedNames(listingRows(rows))
		out["count"] = total == 4 // Lost Reel is on no shelf
		return out
	}

	// Marks do nothing until the setting says what.
	if marks, err := s.AdultMarks(ctx); err != nil || marks != AdultNothing {
		t.Errorf("marks %q, %v", marks, err)
	}
	if got := all(); len(got) != 1 || !got["count"] {
		t.Errorf("at first: %v", got)
	}

	// The administrator locks a title, then a shelf.
	if n, err := s.SetTitlesLocked(ctx, []int64{titles["quiet harbour"], 999999}, true); err != nil || n != 1 {
		t.Fatalf("locked %d, %v", n, err)
	}
	if err := s.SetShelfLocked(ctx, KindMovie, "name:Drama", true); err != nil {
		t.Fatal(err)
	}
	if got := all(); !got["quiet harbour"] || !got["The Long Field"] || !got["Around 100% of_it"] || !got["count"] {
		t.Errorf("locked: %v", got)
	}
	if shelves, _ := s.Shelves(ctx, KindMovie); !shelves[0].Locked || shelves[1].Locked {
		t.Errorf("shelves %+v", shelves)
	}
	if n, err := s.LibraryLocks(ctx); err != nil || n != 2 {
		t.Errorf("%d locks, %v", n, err)
	}
	film, err := s.PlayableMovie(ctx, titles["The Long Field"])
	if err != nil || !film.Locked {
		t.Errorf("playable %+v, %v", film, err)
	}
	// Devices that leave locked titles out see the rest.
	if rows, total, err := s.TitlePage(ctx, KindMovie, 0, "", ByName, 0, 10, true); err != nil || total != 1 ||
		len(rows) != 1 || rows[0].Name != "Lost Reel" {
		t.Errorf("unlocked titles: %d, %v", total, err)
	}

	// Unlocking the shelf leaves the title locked on its own.
	if err := s.SetShelfLocked(ctx, KindMovie, "name:Drama", false); err != nil {
		t.Fatal(err)
	}
	if got := all(); !got["quiet harbour"] || got["The Long Field"] || got["Around 100% of_it"] {
		t.Errorf("shelf unlocked: %v", got)
	}
	var settings int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM shelf_settings").Scan(&settings); err != nil || settings != 0 {
		t.Errorf("%d shelf settings left, %v", settings, err)
	}

	// Marks for adults that lock, then that hide.
	if err := s.SetAdultMarks(ctx, AdultLock); err != nil {
		t.Fatal(err)
	}
	if got := all(); !got["Around 100% of_it"] || got["The Long Field"] {
		t.Errorf("marks lock: %v", got)
	}
	if err := s.SetAdultMarks(ctx, AdultHide); err != nil {
		t.Fatal(err)
	}
	rows, total, err := s.TitlePage(ctx, KindMovie, 0, "", ByName, 0, 10, false)
	if err != nil || total != 3 || len(rows) != 3 {
		t.Errorf("marks hide: %d titles, %v", total, err)
	}
	if found, _ := s.SearchTitles(ctx, "100%", 5); len(found) != 0 {
		t.Errorf("search finds a hidden title: %+v", found)
	}
	// The admin still lists it.
	listed, all2, err := s.AdminTitles(ctx, KindMovie, nil, "", 0, 10)
	if err != nil || all2 != 4 {
		t.Fatalf("the admin lists %d, %v", all2, err)
	}
	for _, a := range listed {
		if a.Name == "Around 100% of_it" && (!a.Adult || a.Own) || a.Name == "quiet harbour" && !a.Own {
			t.Errorf("admin's %+v", a)
		}
	}
	if err := s.SetAdultMarks(ctx, "sometimes"); err == nil {
		t.Error("a mark that does something unknown was set")
	}
}

// listingRows returns the rows of listings.
func listingRows(ls []TitleListing) []TitleRow {
	out := make([]TitleRow, len(ls))
	for i, l := range ls {
		out[i] = l.TitleRow
	}
	return out
}

// A source's marks for adults lock or hide its channels as the setting
// says (D86).
func TestChannelAdultMarks(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist))
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts"},
		{IdentityKey: "x", Name: "X-ray", StreamURL: "http://streams.example/x.ts", Adult: true},
	}, archiveDay); err != nil {
		t.Fatal(err)
	}
	lineup := func() map[string]bool {
		t.Helper()
		cs, err := s.LineupChannels(ctx, DefaultLineupID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, c := range cs {
			out[c.Name] = c.Locked
		}
		return out
	}
	if got := lineup(); len(got) != 2 || got["X-ray"] {
		t.Errorf("at first: %v", got)
	}
	if err := s.SetAdultMarks(ctx, AdultLock); err != nil {
		t.Fatal(err)
	}
	if got := lineup(); !got["X-ray"] || got["Alpha"] {
		t.Errorf("marks lock: %v", got)
	}
	x := entryNamed(t, s, "X-ray")
	if e, err := s.PlayableEntry(ctx, x.EntryID); err != nil || !e.Locked {
		t.Errorf("playable %+v, %v", e, err)
	}
	if err := s.SetAdultMarks(ctx, AdultHide); err != nil {
		t.Fatal(err)
	}
	if got := lineup(); len(got) != 1 || got["X-ray"] {
		t.Errorf("marks hide: %v", got)
	}
	if _, err := s.PlayableEntry(ctx, x.EntryID); err == nil {
		t.Error("a hidden channel plays")
	}
	// A read that no longer marks it brings it back.
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a.ts"},
		{IdentityKey: "x", Name: "X-ray", StreamURL: "http://streams.example/x.ts"},
	}, archiveDay.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := lineup(); len(got) != 2 {
		t.Errorf("unmarked: %v", got)
	}
}
