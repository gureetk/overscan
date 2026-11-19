// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

func portalSource(name string) source.Settings {
	return source.Settings{Name: name, Type: source.TypeStalker, URL: "http://portal.example/c/",
		Device: source.Device{MAC: "00:1A:79:00:00:51"}}
}

// portalItems returns n films of a fictional portal, from ID first, the
// latest added first, with a series at the place given, if any.
func portalItems(first, n, series int) []PortalItem {
	out := make([]PortalItem, n)
	for i := range out {
		id := first + i
		out[i] = PortalItem{Kind: KindMovie, Title: LibraryTitle{Key: fmt.Sprintf("vod:%d", id),
			Name: fmt.Sprintf("Film %d", id), AddedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour),
			File: fmt.Sprintf("cmd=/media/%d.mpg", id)}}
		if i == series {
			out[i].Kind = KindSeries
			out[i].Title.Name = fmt.Sprintf("Series %d", id)
		}
	}
	return out
}

func shelfNames(t *testing.T, s *Store, kind string) []string {
	t.Helper()
	shelves, err := s.Shelves(context.Background(), kind)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, sh := range shelves {
		out = append(out, fmt.Sprintf("%s %d %v", sh.Name, sh.Count, sh.Portal))
	}
	return out
}

func listingNames(ls []TitleListing) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Kind+" "+l.Name)
	}
	return out
}

func TestPortalShelves(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"), portalSource("portal"))
	portal, main := ids["portal"], ids["main"]
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)

	// An Xtream source's Drama is a shelf of its own beside the portal's.
	if _, err := s.ApplyLibrary(ctx, main, KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPortalCategories(ctx, portal, KindMovie, []LibraryCategory{{Key: "vod:1", Name: "Drama"},
		{Key: "vod:2", Name: "Comedy"}, {Key: "vod:3", Name: "Empty"}}, at); err != nil {
		t.Fatal(err)
	}
	want := []string{"Drama 2 false", "Comedy 1 false", "Drama -1 true", "Comedy -1 true", "Empty -1 true"}
	if got := shelfNames(t, s, KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %q, want %q", got, want)
	}
	shelves, _ := s.Shelves(ctx, KindMovie)
	drama, err := s.PortalCategory(ctx, KindMovie, shelves[2].ID)
	if err != nil || drama.Key != "vod:1" || drama.SourceID != portal || drama.Total != -1 || drama.Name != "Drama" {
		t.Fatalf("portal category %+v, %v", drama, err)
	}
	if _, err := s.PortalCategory(ctx, KindMovie, shelves[0].ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("an Xtream shelf as a portal's: %v", err)
	}
	if _, err := s.PortalCategory(ctx, KindSeries, drama.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a film shelf as a series shelf: %v", err)
	}
	empty, _ := s.PortalCategory(ctx, KindMovie, shelves[4].ID)

	// Pages keep their places in the portal's order, films and series
	// alike, and the shelf counts what the portal says it holds.
	if at, err := s.PortalPageReadAt(ctx, drama.ID, 1); err != nil || !at.IsZero() {
		t.Errorf("an unread page read at %v, %v", at, err)
	}
	if err := s.ApplyPortalPage(ctx, drama.ID, 1, portalItems(1001, 14, 3), 20, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPortalPage(ctx, drama.ID, 2, portalItems(1015, 6, -1), 20, at); err != nil {
		t.Fatal(err)
	}
	if err := s.ApplyPortalPage(ctx, empty.ID, 1, nil, 0, at); err != nil {
		t.Fatal(err)
	}
	want = []string{"Drama 2 false", "Comedy 1 false", "Drama 20 true", "Comedy -1 true"}
	if got := shelfNames(t, s, KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %q, want %q", got, want)
	}
	if read, err := s.PortalPageReadAt(ctx, drama.ID, 2); err != nil || !read.Equal(at) {
		t.Errorf("page 2 read at %v, %v", read, err)
	}
	part, err := s.PortalTitles(ctx, drama.ID, 3, 3)
	if got := listingNames(part); err != nil || !reflect.DeepEqual(got, []string{"series Series 1004", "movie Film 1005",
		"movie Film 1006"}) || part[0].Shelf != "Drama" {
		t.Errorf("part %q, %v", got, err)
	}
	if part, _ := s.PortalTitles(ctx, drama.ID, 18, 5); len(part) != 2 || part[1].Name != "Film 1020" {
		t.Errorf("the shelf's end %q", listingNames(part))
	}
	reads, _ := s.LibraryReads(ctx)
	for _, r := range reads {
		if r.SourceID == portal && (r.Kind == KindMovie && r.Titles != 19 || r.Kind == KindSeries && r.Titles != 1) {
			t.Errorf("read %+v", r)
		}
	}

	// The Xtream source's shelf leaves out the portal's titles of the same
	// category name, and the portal's titles are among all of them.
	if titles, _ := s.ShelfTitles(ctx, KindMovie, shelves[0].ID, ByName); len(titles) != 2 {
		t.Errorf("the Xtream source's Drama has %d titles", len(titles))
	}
	if _, total, _ := s.TitlePage(ctx, KindMovie, 0, "", ByAdded, 0, 5); total != 4+19 {
		t.Errorf("%d films in all", total)
	}

	// Page 1 read again with two new films on top: the two that were last
	// on it lose their places until page 2 is read again.
	again := append(portalItems(2001, 2, -1), portalItems(1001, 12, 3)...)
	if err := s.ApplyPortalPage(ctx, drama.ID, 1, again, 22, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	part, _ = s.PortalTitles(ctx, drama.ID, 0, 28)
	if got := listingNames(part); len(got) != 20 || got[0] != "movie Film 2001" || got[5] != "series Series 1004" ||
		got[14] != "movie Film 1015" {
		t.Errorf("after page 1 again: %q", got)
	}

	// A film plays by what its page said.
	item, err := s.PlayableMovie(ctx, part[0].ID)
	if err != nil || item.StreamURL != "cmd=/media/2001.mpg" || item.Container != "mp4" || item.SourceID != portal {
		t.Errorf("playable %+v, %v", item, err)
	}
	title, err := s.Title(ctx, part[5].ID)
	if err != nil || title.Kind != KindSeries || title.File != "cmd=/media/1004.mpg" || title.SourceType != source.TypeStalker {
		t.Errorf("title %+v, %v", title, err)
	}

	// A read of the categories that no longer lists one drops its shelf,
	// and its row once no title is on it.
	if err := s.ApplyPortalCategories(ctx, portal, KindMovie, []LibraryCategory{{Key: "vod:1", Name: "Drama"}},
		at.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	want = []string{"Drama 2 false", "Comedy 1 false", "Drama 22 true"}
	if got := shelfNames(t, s, KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %q, want %q", got, want)
	}
	if _, err := s.PortalCategory(ctx, KindMovie, empty.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a category no longer listed: %v", err)
	}

	// A portal that is off has no shelves.
	if err := s.SetSourceEnabled(ctx, portal, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PortalCategory(ctx, KindMovie, drama.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a portal that is off: %v", err)
	}
	if part, _ := s.PortalTitles(ctx, drama.ID, 0, 14); len(part) != 0 {
		t.Errorf("a portal that is off shows %d titles", len(part))
	}
	want = []string{"Drama 2 false", "Comedy 1 false"}
	if got := shelfNames(t, s, KindMovie); !reflect.DeepEqual(got, want) {
		t.Errorf("shelves %q, want %q", got, want)
	}
}
