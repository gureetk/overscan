// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestSetRefreshResult(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("main", 1, NumbersProvider))
	at := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	if err := s.SetRefreshResult(ctx, ids["main"], at, "connection refused"); err != nil {
		t.Fatal(err)
	}
	srcs, err := s.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !srcs[0].RefreshedAt.Equal(at) || srcs[0].RefreshError != "connection refused" {
		t.Errorf("source %+v", srcs[0])
	}
}

// lineupNumbers returns the default lineup's numbers by channel name.
func lineupNumbers(t *testing.T, s *Store) map[string]int {
	t.Helper()
	rows, err := s.db.Query(`SELECT c.name, e.number FROM lineup_entries e JOIN channels c ON c.id = e.channel_id
		WHERE e.lineup_id = 1 AND e.number IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		out[name] = n
	}
	return out
}

func channelIDs(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	rows, err := s.db.Query("SELECT identity_key, id FROM channels")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var key string
		var id int64
		if err := rows.Scan(&key, &id); err != nil {
			t.Fatal(err)
		}
		out[key] = id
	}
	return out
}

func TestApplyCatalog(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("main", 1, NumbersProvider))
	src := ids["main"]
	at := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)

	// Fictional channels: two ask for 101, one has no number.
	list := []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", Number: 101, Category: "News", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Bravo", Category: "Sports", StreamURL: "http://streams.example/b"},
		{IdentityKey: "c", Name: "Charlie", Number: 101, Category: "News", StreamURL: "http://streams.example/c"},
		{IdentityKey: "d", Name: "Delta", Number: 5, StreamURL: "http://streams.example/d"},
	}
	ch, err := s.ApplyCatalog(ctx, src, list, at)
	if err != nil {
		t.Fatal(err)
	}
	if ch != (CatalogChanges{Added: 4}) {
		t.Errorf("first refresh: %+v", ch)
	}
	want := map[string]int{"Alpha": 101, "Delta": 5, "Bravo": 102, "Charlie": 103}
	if got := lineupNumbers(t, s); !reflect.DeepEqual(got, want) {
		t.Errorf("numbers %v, want %v", got, want)
	}
	firstIDs := channelIDs(t, s)

	// The same list again changes nothing but the details.
	list[0].Name = "Alpha HD"
	at = at.Add(time.Hour)
	if ch, err = s.ApplyCatalog(ctx, src, list, at); err != nil {
		t.Fatal(err)
	}
	if ch != (CatalogChanges{Updated: 4}) {
		t.Errorf("second refresh: %+v", ch)
	}
	if got := channelIDs(t, s); !reflect.DeepEqual(got, firstIDs) {
		t.Errorf("IDs changed: %v, then %v", firstIDs, got)
	}
	if got := lineupNumbers(t, s); got["Alpha HD"] != 101 {
		t.Errorf("numbers after a rename %v", got)
	}

	// Bravo disappears: it is kept, marked missing, with its number.
	short := []CatalogChannel{list[0], list[2], list[3]}
	at = at.Add(time.Hour)
	if ch, err = s.ApplyCatalog(ctx, src, short, at); err != nil {
		t.Fatal(err)
	}
	if ch != (CatalogChanges{Updated: 3, Missing: 1}) {
		t.Errorf("third refresh: %+v", ch)
	}
	var categories int
	if err := s.db.QueryRow("SELECT count(*) FROM categories").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if categories != 2 {
		t.Errorf("%d categories while Bravo's Sports still has a missing channel, want 2", categories)
	}

	// It comes back within the grace period: same ID, same number.
	at = at.Add(24 * time.Hour)
	if ch, err = s.ApplyCatalog(ctx, src, list, at); err != nil {
		t.Fatal(err)
	}
	if ch != (CatalogChanges{Updated: 4}) {
		t.Errorf("fourth refresh: %+v", ch)
	}
	if got := channelIDs(t, s); got["b"] != firstIDs["b"] || lineupNumbers(t, s)["Bravo"] != 102 {
		t.Errorf("Bravo came back as %d with number %d", got["b"], lineupNumbers(t, s)["Bravo"])
	}

	// Gone for longer than the grace period: removed, with its entry and
	// its now empty category.
	at = at.Add(time.Hour)
	if _, err = s.ApplyCatalog(ctx, src, short, at); err != nil {
		t.Fatal(err)
	}
	at = at.Add(MissingGrace + time.Hour)
	if ch, err = s.ApplyCatalog(ctx, src, short, at); err != nil {
		t.Fatal(err)
	}
	if ch != (CatalogChanges{Updated: 3, Purged: 1}) {
		t.Errorf("refresh after the grace period: %+v", ch)
	}
	if _, ok := lineupNumbers(t, s)["Bravo"]; ok {
		t.Error("Bravo's lineup entry survived")
	}
	if err := s.db.QueryRow("SELECT count(*) FROM categories").Scan(&categories); err != nil {
		t.Fatal(err)
	}
	if categories != 1 {
		t.Errorf("%d categories, want 1", categories)
	}
}

func TestApplyCatalogKeepsSourcesApart(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersProvider), testSource("two", 1, NumbersProvider))
	at := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	same := []CatalogChannel{{IdentityKey: "x", Name: "Echo", Number: 7, StreamURL: "http://streams.example/x"}}
	if _, err := s.ApplyCatalog(ctx, ids["one"], same, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["two"], same, at); err != nil {
		t.Fatal(err)
	}
	// The second source's channel cannot have 7 again.
	var numbers []int
	rows, err := s.db.Query("SELECT number FROM lineup_entries ORDER BY number")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		numbers = append(numbers, n)
	}
	if !reflect.DeepEqual(numbers, []int{7, 8}) {
		t.Errorf("numbers %v, want [7 8]", numbers)
	}

	// An empty refresh of one source does not touch the other.
	if _, err := s.ApplyCatalog(ctx, ids["one"], nil, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := s.db.QueryRow("SELECT count(*) FROM channels WHERE missing_since IS NOT NULL").Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing != 1 {
		t.Errorf("%d missing channels, want 1", missing)
	}
}

func TestLineupChannels(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersProvider), testSource("two", 1, NumbersProvider))
	at := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", Number: 2, Category: "News", Logo: "http://logos.example/a.png", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Bravo", Number: 1, StreamURL: "http://streams.example/b"},
	}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["two"], []CatalogChannel{
		{IdentityKey: "c", Name: "Charlie", StreamURL: "http://streams.example/c"},
	}, at); err != nil {
		t.Fatal(err)
	}

	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range cs {
		names = append(names, c.Name)
	}
	if !reflect.DeepEqual(names, []string{"Bravo", "Alpha", "Charlie"}) {
		t.Errorf("channels %v", names)
	}
	if a := cs[1]; a.Number != 2 || a.Group != "News" || a.GroupID == 0 || a.Logo == "" {
		t.Errorf("Alpha %+v", a)
	}

	// A source turned off hides its channels.
	if err := s.SetSourceEnabled(ctx, ids["two"], false); err != nil {
		t.Fatal(err)
	}
	if cs, err = s.LineupChannels(ctx, DefaultLineupID); err != nil || len(cs) != 2 {
		t.Errorf("channels after disabling a source: %+v, %v", cs, err)
	}
}

func TestPlayableEntry(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersProvider))
	at := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Bravo", StreamURL: "http://streams.example/b"},
	}, at); err != nil {
		t.Fatal(err)
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}

	p, err := s.PlayableEntry(ctx, cs[0].EntryID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Alpha" || p.StreamURL != "http://streams.example/a" || p.SourceID != ids["one"] || p.LineupID != DefaultLineupID {
		t.Errorf("entry %+v", p)
	}

	// Bravo stops being listed: it can no longer be played.
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a"},
	}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlayableEntry(ctx, cs[1].EntryID); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing channel: %v", err)
	}
	if _, err := s.PlayableEntry(ctx, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v", err)
	}
}
