// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// adminShelves returns the shelves as the admin lists them: name, count,
// and what is set of them.
func adminShelves(t *testing.T, s *Store, kind string) []string {
	t.Helper()
	shelves, err := s.LibraryShelves(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, sh := range shelves {
		line := sh.Name + " " + sh.Key + " " + strings.Join(sh.Sources, "+")
		if sh.Hidden {
			line += " hidden"
		}
		if sh.Renamed() {
			line += " renamed"
		}
		if sh.Placed {
			line += " placed"
		}
		out = append(out, line)
	}
	return out
}

// TestShelfSettings hides, renames and orders shelves, and hides titles, as
// the administrator does (D79): devices see the result, the admin sees
// everything, and the settings outlast a read of the sources.
func TestShelfSettings(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"), xtreamSource("one"), portalSource("portal"))
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyLibrary(ctx, ids["one"], KindMovie, []LibraryCategory{{Key: "1", Name: "Drama"}},
		[]LibraryTitle{{Key: "77", Category: "1", Name: "Other Drama"}}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPortalCategories(ctx, ids["portal"], KindMovie, []LibraryCategory{{Key: "vod:1", Name: "Kids"}}, at); err != nil {
		t.Fatal(err)
	}
	shelves, _ := s.Shelves(ctx, KindMovie, EverySource)
	kids := shelves[2]
	if err := s.ApplyPortalPage(ctx, kids.ID, 1, portalItems(1001, 3, -1), 3, at); err != nil {
		t.Fatal(err)
	}
	if got, want := shelfNames(t, s, KindMovie), []string{"Drama 3 false", "Comedy 1 false", "Kids 3 true"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("shelves %q, want %q", got, want)
	}
	if got, want := adminShelves(t, s, KindMovie), []string{"Drama name:Drama main+one", "Comedy name:Comedy main",
		"Kids category:" + itoa(kids.ID) + " portal"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("admin shelves %q, want %q", got, want)
	}
	byName := titleIDs(t, s)

	// Hidden: Drama, from both sources, and its titles everywhere.
	if err := s.SetShelfHidden(ctx, KindMovie, "name:Drama", true); err != nil {
		t.Fatal(err)
	}
	if got, want := shelfNames(t, s, KindMovie), []string{"Comedy 1 false", "Kids 3 true"}; !reflect.DeepEqual(got, want) {
		t.Errorf("shelves with Drama hidden %q, want %q", got, want)
	}
	if found, _ := s.SearchTitles(ctx, "drama", 10, EverySource); len(found) != 0 {
		t.Errorf("found on a hidden shelf: %q", names(found))
	}
	if _, err := s.Title(ctx, byName["The Long Field"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("a title on a hidden shelf: %v", err)
	}
	if _, err := s.PlayableMovie(ctx, byName["Other Drama"]); !errors.Is(err, ErrNotFound) {
		t.Errorf("playing a title on a hidden shelf: %v", err)
	}
	if recent, _ := s.RecentTitles(ctx, KindMovie, 10, EverySource); reflect.DeepEqual(names(recent), []string{}) ||
		strings.Contains(strings.Join(names(recent), ","), "Long Field") {
		t.Errorf("recent %q", names(recent))
	}
	if got := adminShelves(t, s, KindMovie)[0]; got != "Drama name:Drama main+one hidden" {
		t.Errorf("the admin's Drama: %q", got)
	}

	// Renamed, and put in places of their own.
	if err := s.RenameShelf(ctx, KindMovie, "name:Comedy", "  Laughs "); err != nil {
		t.Fatal(err)
	}
	if err := s.PlaceShelves(ctx, KindMovie, []string{"category:" + itoa(kids.ID), "name:Comedy"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetShelfHidden(ctx, KindMovie, "name:Drama", false); err != nil {
		t.Fatal(err)
	}
	if got, want := shelfNames(t, s, KindMovie), []string{"Kids 3 true", "Laughs 1 false", "Drama 3 false"}; !reflect.DeepEqual(got, want) {
		t.Errorf("shelves renamed and placed %q, want %q", got, want)
	}
	if got, want := adminShelves(t, s, KindMovie), []string{"Kids category:" + itoa(kids.ID) + " portal placed",
		"Laughs name:Comedy main renamed placed", "Drama name:Drama main+one"}; !reflect.DeepEqual(got, want) {
		t.Errorf("admin shelves %q, want %q", got, want)
	}
	shelves, _ = s.Shelves(ctx, KindMovie, EverySource)
	if rows, err := s.ShelfTitles(ctx, KindMovie, shelves[1].ID, ByName, EverySource); err != nil || !reflect.DeepEqual(names(rows), []string{"quiet harbour"}) {
		t.Errorf("the renamed shelf's titles %q, %v", names(rows), err)
	}

	// A title hidden on its own; the admin still lists it.
	if n, err := s.SetTitlesHidden(ctx, []int64{byName["Around 100% of_it"], 99999}, true); err != nil || n != 1 {
		t.Fatalf("hid %d, %v", n, err)
	}
	if rows, _ := s.ShelfTitles(ctx, KindMovie, shelves[2].ID, ByName, EverySource); !reflect.DeepEqual(names(rows), []string{"Other Drama", "The Long Field"}) {
		t.Errorf("titles with one hidden %q", names(rows))
	}
	drama, err := s.LibraryShelf(ctx, KindMovie, shelves[2].ID)
	if err != nil || drama.Titles != 3 || drama.HiddenTitles != 1 || drama.Own != "Drama" || drama.Renamed() {
		t.Fatalf("the admin's shelf %+v, %v", drama, err)
	}
	listed, total, err := s.AdminTitles(ctx, KindMovie, &drama, "", 0, 10)
	if err != nil || total != 3 || len(listed) != 3 || listed[0].Name != "Around 100% of_it" || !listed[0].Hidden ||
		listed[0].Source != "main" || listed[0].Shelf != "Drama" || listed[1].Name != "Other Drama" || listed[1].Source != "one" ||
		listed[1].Hidden {
		t.Errorf("admin titles %+v, %d, %v", listed, total, err)
	}
	if a, err := s.AdminTitleByID(ctx, byName["Around 100% of_it"]); err != nil || !a.Hidden {
		t.Errorf("a hidden title for the admin %+v, %v", a, err)
	}
	if listed, total, _ := s.AdminTitles(ctx, KindMovie, nil, "FILM 100", 1, 1); total != 3 || len(listed) != 1 || listed[0].Name != "Film 1002" {
		t.Errorf("searched %+v, %d", listed, total)
	}
	kidsShelf, _ := s.LibraryShelf(ctx, KindMovie, kids.ID)
	if listed, total, _ := s.AdminTitles(ctx, KindMovie, &kidsShelf, "", 0, 10); total != 3 || len(listed) != 3 {
		t.Errorf("the portal's shelf %+v, %d", listed, total)
	}

	// A read of the source keeps what was set.
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, want := shelfNames(t, s, KindMovie), []string{"Kids 3 true", "Laughs 1 false", "Drama 2 false"}; !reflect.DeepEqual(got, want) {
		t.Errorf("after a read %q, want %q", got, want)
	}

	// A portal's shelf hidden is gone for devices.
	if err := s.SetShelfHidden(ctx, KindMovie, "category:"+itoa(kids.ID), true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PortalCategory(ctx, KindMovie, kids.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a hidden portal shelf: %v", err)
	}
	if part, _ := s.PortalTitles(ctx, kids.ID, 1, 3); len(part) != 0 {
		t.Errorf("a hidden portal shelf's titles %q", listingNames(part))
	}

	// Names given back, and the sources' order.
	if err := s.RenameShelf(ctx, KindMovie, "name:Comedy", "Comedy"); err != nil {
		t.Fatal(err)
	}
	if err := s.PlaceShelves(ctx, KindMovie, nil); err != nil {
		t.Fatal(err)
	}
	if got, want := shelfNames(t, s, KindMovie), []string{"Drama 2 false", "Comedy 1 false"}; !reflect.DeepEqual(got, want) {
		t.Errorf("given back %q, want %q", got, want)
	}
	for _, bad := range []string{strings.Repeat("x", MaxShelfName+1), "tab\there"} {
		if err := s.RenameShelf(ctx, KindMovie, "name:Comedy", bad); !errors.Is(err, ErrBadName) {
			t.Errorf("%q: %v", bad, err)
		}
	}
	var settings int
	if err := s.db.QueryRow("SELECT count(*) FROM shelf_settings").Scan(&settings); err != nil || settings != 1 {
		t.Errorf("%d shelf settings left, %v", settings, err)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
