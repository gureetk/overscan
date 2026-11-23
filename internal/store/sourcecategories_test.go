// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// keysOf returns the keys of categories, and whether each is ticked.
func keysOf(cs []SourceCategory) map[string]bool {
	out := map[string]bool{}
	for _, c := range cs {
		out[c.Key] = c.Ticked
	}
	return out
}

// shownNames returns the names of what the default lineup shows.
func shownNames(t *testing.T, s *Store) []string {
	t.Helper()
	cs, err := s.LineupChannels(context.Background(), DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// The live channels of a fictional source, in four categories and one
// without a category.
var categoryChannels = []CatalogChannel{
	{IdentityKey: "a", Name: "Alpha News", Category: "News", StreamURL: "http://streams.example/a"},
	{IdentityKey: "b", Name: "Bravo Sport", Category: "Sport", StreamURL: "http://streams.example/b"},
	{IdentityKey: "c", Name: "Charlie Kids", Category: "Kids", StreamURL: "http://streams.example/c"},
	{IdentityKey: "d", Name: "Delta News", Category: "News", StreamURL: "http://streams.example/d"},
	{IdentityKey: "e", Name: "Echo", StreamURL: "http://streams.example/e"},
	{IdentityKey: "f", Name: "Foxtrot Music", Category: "Music", StreamURL: "http://streams.example/f"},
}

func TestSourceBringsEveryCategoryAtFirst(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	src := addSources(t, s, testSource("main", 1, NumbersPlaylist))["main"]
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ch, err := s.ApplyCatalog(ctx, src, categoryChannels, at)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Added != 6 || ch.LeftOut != 0 {
		t.Errorf("changes %+v", ch)
	}
	if got := shownNames(t, s); len(got) != 6 {
		t.Errorf("shown %v", got)
	}
	for _, kind := range []string{KindLive, KindMovie, KindSeries} {
		if c, err := s.CategoryChoice(ctx, src, kind); err != nil || c != (CategoryChoice{}) {
			t.Errorf("%s: %+v, %v", kind, c, err)
		}
	}
	cats, total, err := s.SourceCategories(ctx, src, KindLive, "")
	if err != nil || total != 5 {
		t.Fatalf("categories %+v, %d, %v", cats, total, err)
	}
	want := []SourceCategory{
		{Key: "News", Name: "News", Items: 2, Ticked: true, ListedAt: at},
		{Key: "Sport", Name: "Sport", Items: 1, Ticked: true, ListedAt: at},
		{Key: "Kids", Name: "Kids", Items: 1, Ticked: true, ListedAt: at},
		{Key: "", Name: "", Items: 1, Ticked: true, ListedAt: at},
		{Key: "Music", Name: "Music", Items: 1, Ticked: true, ListedAt: at},
	}
	if !reflect.DeepEqual(cats, want) {
		t.Errorf("categories\n got %+v\nwant %+v", cats, want)
	}
	// Ticks wait while the source brings every category.
	if n, err := s.TickCategories(ctx, src, KindLive, map[string]bool{"Sport": false}, at); err != nil || n != 1 {
		t.Fatalf("untick: %d, %v", n, err)
	}
	if ch, err := s.ApplyCatalog(ctx, src, categoryChannels, at.Add(time.Hour)); err != nil || ch.LeftOut != 0 || ch.Missing != 0 {
		t.Errorf("a read with a tick that waits: %+v, %v", ch, err)
	}
	if c, err := s.CategoryChoice(ctx, src, KindLive); err != nil || c != (CategoryChoice{}) {
		t.Errorf("a tick made a choice: %+v, %v", c, err)
	}
}

func TestSourceBringsTickedCategories(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	src := addSources(t, s, testSource("main", 1, NumbersPlaylist))["main"]
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCatalog(ctx, src, categoryChannels, at); err != nil {
		t.Fatal(err)
	}
	ids := channelIDs(t, s)
	var bravo int64
	if err := s.db.QueryRow("SELECT id FROM lineup_entries WHERE channel_id = ?", ids["b"]).Scan(&bravo); err != nil {
		t.Fatal(err)
	}
	if err := s.RenameEntry(ctx, DefaultLineupID, bravo, "Bravo, renamed"); err != nil {
		t.Fatal(err)
	}
	before := lineupNumbers(t, s)

	// Only the ticked categories: every one is ticked, so nothing changes
	// until one is unticked.
	if _, err := s.SetCategoryChoice(ctx, src, KindLive, true, false, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if ch, err := s.ApplyCatalog(ctx, src, categoryChannels, at.Add(time.Hour)); err != nil || ch.LeftOut != 0 || ch.Missing != 0 {
		t.Errorf("every category ticked: %+v, %v", ch, err)
	}
	if n, err := s.TickCategories(ctx, src, KindLive, map[string]bool{"Sport": false, "": false, "Nowhere": false},
		at.Add(2*time.Hour)); err != nil || n != 2 {
		t.Fatalf("untick: %d, %v", n, err)
	}
	if c, err := s.CategoryChoice(ctx, src, KindLive); err != nil || !c.OnlyTicked || c.TickNew ||
		!c.ChangedAt.Equal(at.Add(2*time.Hour)) {
		t.Errorf("choice %+v, %v", c, err)
	}
	ch, err := s.ApplyCatalog(ctx, src, categoryChannels, at.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if ch.LeftOut != 2 || ch.Missing != 2 || ch.Updated != 4 {
		t.Errorf("changes %+v", ch)
	}
	if got := shownNames(t, s); !reflect.DeepEqual(got, []string{"Alpha News", "Charlie Kids", "Delta News", "Foxtrot Music"}) {
		t.Errorf("shown %v", got)
	}
	// The source's channels, for the guide, are those it brings.
	if listed, err := s.GuideChannels(ctx, src); err != nil || len(listed) != 4 {
		t.Errorf("the guide's channels %+v, %v", listed, err)
	}

	// Ticked again within the grace, they come back with their edits.
	if _, err := s.TickCategories(ctx, src, KindLive, map[string]bool{"Sport": true, "": true}, at.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ch, err := s.ApplyCatalog(ctx, src, categoryChannels, at.Add(5*time.Hour)); err != nil || ch.LeftOut != 0 || ch.Updated != 6 {
		t.Errorf("ticked again: %+v, %v", ch, err)
	}
	if got := shownNames(t, s); len(got) != 6 || got[1] != "Bravo, renamed" {
		t.Errorf("shown %v", got)
	}
	if got := lineupNumbers(t, s); !reflect.DeepEqual(got, before) {
		t.Errorf("numbers %v, were %v", got, before)
	}

	// Unticked for longer than the grace, they go, and only the ticked
	// categories' channels stay.
	if _, err := s.TickCategories(ctx, src, KindLive, map[string]bool{"Sport": false, "Kids": false, "Music": false, "": false},
		at.Add(6*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, src, categoryChannels, at.Add(7*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ch, err = s.ApplyCatalog(ctx, src, categoryChannels, at.Add(7*time.Hour+MissingGrace+time.Minute))
	if err != nil || ch.Purged != 4 || ch.LeftOut != 4 {
		t.Errorf("after the grace: %+v, %v", ch, err)
	}
	var stored int
	if err := s.db.QueryRow("SELECT count(*) FROM channels WHERE source_id = ?", src).Scan(&stored); err != nil || stored != 2 {
		t.Errorf("channels kept: %d, %v", stored, err)
	}
	if got := shownNames(t, s); !reflect.DeepEqual(got, []string{"Alpha News", "Delta News"}) {
		t.Errorf("shown %v", got)
	}

	// Every category again: the others come back, as new channels.
	if _, err := s.SetCategoryChoice(ctx, src, KindLive, false, false, at.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if ch, err := s.ApplyCatalog(ctx, src, categoryChannels, at.Add(8*24*time.Hour)); err != nil || ch.Added != 4 || ch.LeftOut != 0 {
		t.Errorf("every category again: %+v, %v", ch, err)
	}
}

func TestNewCategoriesFollowTheChoice(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersPlaylist))
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	// Both choose before their first read: one ticks what comes later, two
	// does not.
	if _, err := s.SetCategoryChoice(ctx, ids["one"], KindLive, true, true, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetCategoryChoice(ctx, ids["two"], KindLive, true, false, at); err != nil {
		t.Fatal(err)
	}
	ch, err := s.ApplyCatalog(ctx, ids["two"], categoryChannels, at)
	if err != nil || ch.Added != 0 || ch.LeftOut != 6 {
		t.Errorf("two's first read: %+v, %v", ch, err)
	}
	if cats, _, err := s.SourceCategories(ctx, ids["two"], KindLive, ""); err != nil ||
		!reflect.DeepEqual(keysOf(cats), map[string]bool{"News": false, "Sport": false, "Kids": false, "": false, "Music": false}) {
		t.Errorf("two's categories %+v, %v", cats, err)
	}
	if ch, err := s.ApplyCatalog(ctx, ids["one"], categoryChannels, at); err != nil || ch.Added != 6 {
		t.Errorf("one's first read: %+v, %v", ch, err)
	}
	// No group in the lineup for categories that a source does not bring.
	var groups int
	if err := s.db.QueryRow("SELECT count(*) FROM lineup_categories WHERE source_id = ?", ids["two"]).Scan(&groups); err != nil || groups != 0 {
		t.Errorf("two's categories in the lineup: %d, %v", groups, err)
	}

	// two ticks News; then both list a new category.
	if _, err := s.TickCategories(ctx, ids["two"], KindLive, map[string]bool{"News": true}, at); err != nil {
		t.Fatal(err)
	}
	more := append(append([]CatalogChannel{}, categoryChannels...),
		CatalogChannel{IdentityKey: "g", Name: "Golf Weather", Category: "Weather", StreamURL: "http://streams.example/g"})
	if ch, err := s.ApplyCatalog(ctx, ids["one"], more, at.Add(time.Hour)); err != nil || ch.Added != 1 || ch.LeftOut != 0 {
		t.Errorf("one gains Weather: %+v, %v", ch, err)
	}
	if ch, err := s.ApplyCatalog(ctx, ids["two"], more, at.Add(time.Hour)); err != nil || ch.Added != 2 || ch.LeftOut != 5 {
		t.Errorf("two leaves Weather out: %+v, %v", ch, err)
	}
	if cats, _, err := s.SourceCategories(ctx, ids["two"], KindLive, "weath"); err != nil || len(cats) != 1 || cats[0].Ticked {
		t.Errorf("two's Weather %+v, %v", cats, err)
	}

	// A category that no read lists for the grace is forgotten.
	later := at.Add(MissingGrace + 2*time.Hour)
	if _, err := s.ApplyCatalog(ctx, ids["one"], categoryChannels[:1], later); err != nil {
		t.Fatal(err)
	}
	if cats, total, err := s.SourceCategories(ctx, ids["one"], KindLive, ""); err != nil || total != 1 || cats[0].Key != "News" {
		t.Errorf("one's categories after the grace %+v, %d, %v", cats, total, err)
	}

	counts, err := s.CategoryCounts(ctx)
	if err != nil || counts[ids["two"]][KindLive] != (CategoryCount{Listed: 6, Ticked: 1, LastRead: at.Add(time.Hour)}) ||
		counts[ids["one"]][KindLive] != (CategoryCount{Listed: 1, Ticked: 1, LastRead: later}) {
		t.Errorf("counts %+v, %v", counts, err)
	}
	choices, err := s.CategoryChoices(ctx)
	if err != nil || len(choices) != 2 || !choices[ids["one"]][KindLive].TickNew || choices[ids["two"]][KindLive].TickNew {
		t.Errorf("choices %+v, %v", choices, err)
	}
}

func TestTickingCategories(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	src := addSources(t, s, testSource("main", 1, NumbersPlaylist))["main"]
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCatalog(ctx, src, categoryChannels, at); err != nil {
		t.Fatal(err)
	}
	// Untick those whose names hold "s", whatever its case, then tick
	// every one.
	if n, err := s.TickAllCategories(ctx, src, KindLive, "O", false, at); err != nil || n != 1 {
		t.Errorf("untick matching: %d, %v", n, err)
	}
	if n, err := s.TickAllCategories(ctx, src, KindLive, " s ", false, at); err != nil || n != 3 {
		t.Errorf("untick matching: %d, %v", n, err)
	}
	if cats, _, err := s.SourceCategories(ctx, src, KindLive, ""); err != nil ||
		!reflect.DeepEqual(keysOf(cats), map[string]bool{"News": false, "Sport": false, "Kids": false, "": true, "Music": false}) {
		t.Errorf("after unticking %+v, %v", cats, err)
	}
	if n, err := s.TickAllCategories(ctx, src, KindLive, "", true, at); err != nil || n != 4 {
		t.Errorf("tick all: %d, %v", n, err)
	}

	// Unknown sources and kinds.
	if _, err := s.TickCategories(ctx, 99, KindLive, map[string]bool{"News": true}, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown source's ticks: %v", err)
	}
	if _, err := s.SetCategoryChoice(ctx, 99, KindLive, true, false, at); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown source's choice: %v", err)
	}
	if _, err := s.SetCategoryChoice(ctx, src, "radio", true, false, at); err == nil {
		t.Error("a choice of an unknown kind")
	}
	if _, _, err := s.SourceCategories(ctx, src, "episode", ""); err == nil {
		t.Error("categories of an unknown kind")
	}
	// A deleted source takes its categories and choices with it.
	if _, err := s.SetCategoryChoice(ctx, src, KindMovie, true, true, at); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.db.QueryRow("SELECT (SELECT count(*) FROM source_categories) + (SELECT count(*) FROM source_category_choices)").Scan(&left); err != nil || left != 0 {
		t.Errorf("left after deleting the source: %d, %v", left, err)
	}
}

func TestSourceBringsTickedFilmsAndSeries(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"), portalSource("portal"))
	src := ids["main"]
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ch, err := s.ApplyLibrary(ctx, src, KindMovie, filmCategories, films, at)
	if err != nil || ch.Added != 4 || ch.LeftOut != 0 {
		t.Fatalf("first read %+v, %v", ch, err)
	}
	cats, _, err := s.SourceCategories(ctx, src, KindMovie, "")
	if err != nil {
		t.Fatal(err)
	}
	items := map[string]int{}
	for _, c := range cats {
		items[c.Key] = c.Items
	}
	// "Twice" is 5001 again, which the store keeps once; "Lost Reel" is in a
	// category that the read did not list.
	if !reflect.DeepEqual(items, map[string]int{"21": 2, "22": 1, "23": 0, "": 1}) {
		t.Errorf("items %v", items)
	}
	if _, err := s.SetCategoryChoice(ctx, src, KindMovie, true, false, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.TickCategories(ctx, src, KindMovie, map[string]bool{"21": false, "": false}, at); err != nil {
		t.Fatal(err)
	}
	ch, err = s.ApplyLibrary(ctx, src, KindMovie, filmCategories, films, at.Add(time.Hour))
	if err != nil || ch.LeftOut != 3 || ch.Missing != 3 || ch.Updated != 1 {
		t.Errorf("ticked read %+v, %v", ch, err)
	}
	shelves, err := s.Shelves(ctx, KindMovie, Sources{})
	if err != nil || len(shelves) != 1 || shelves[0].Name != "Comedy" || shelves[0].Count != 1 {
		t.Errorf("shelves %+v, %v", shelves, err)
	}
	// Series are a kind of their own: every one still.
	if ch, err := s.ApplyLibrary(ctx, src, KindSeries, []LibraryCategory{{Key: "31", Name: "Drama Series"}},
		[]LibraryTitle{{Key: "7001", Category: "31", Name: "Shore Lane"}}, at); err != nil || ch.Added != 1 {
		t.Errorf("series %+v, %v", ch, err)
	}

	// A portal's categories that it does not bring are not shelves, so no
	// device reads their pages.
	portal := ids["portal"]
	if _, err := s.SetCategoryChoice(ctx, portal, KindMovie, true, false, at); err != nil {
		t.Fatal(err)
	}
	portalCats := []LibraryCategory{{Key: "vod:1", Name: "Drama"}, {Key: "vod:2", Name: "Kids"}}
	if err := s.ApplyPortalCategories(ctx, portal, KindMovie, portalCats, at); err != nil {
		t.Fatal(err)
	}
	if got, _, err := s.SourceCategories(ctx, portal, KindMovie, ""); err != nil || len(got) != 2 || got[0].Items != -1 || got[0].Ticked {
		t.Errorf("the portal's categories %+v, %v", got, err)
	}
	if _, err := s.TickCategories(ctx, portal, KindMovie, map[string]bool{"vod:2": true}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPortalCategories(ctx, portal, KindMovie, portalCats, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var listed []string
	rows, err := s.db.Query(`SELECT c.name FROM library_categories c JOIN sources s ON s.id = c.source_id
		WHERE c.source_id = ? AND `+portalCategoryListed, portal)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		listed = append(listed, name)
	}
	if !reflect.DeepEqual(listed, []string{"Kids"}) {
		t.Errorf("the portal's shelves %v", listed)
	}
}
