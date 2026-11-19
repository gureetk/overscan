// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

// entryIDs returns the default lineup's entry IDs by channel name.
func entryIDs(t *testing.T, s *Store) map[string]int64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT c.name, e.id FROM lineup_entries e JOIN channels c ON c.id = e.channel_id WHERE e.lineup_id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var name string
		var id int64
		if err := rows.Scan(&name, &id); err != nil {
			t.Fatal(err)
		}
		out[name] = id
	}
	return out
}

// fictional returns catalog channels named and keyed by names, with the
// numbers the playlist gives them (0 for none).
func fictional(names []string, numbers ...int) []CatalogChannel {
	out := make([]CatalogChannel, len(names))
	for i, name := range names {
		out[i] = CatalogChannel{IdentityKey: name, Name: name, StreamURL: "http://streams.example/" + name}
		if i < len(numbers) {
			out[i].Number = numbers[i]
		}
	}
	return out
}

func TestPlaylistNumbers(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("main", 1, ""))
	src := ids["main"]
	at := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)

	// Numbers follow the playlist, whatever numbers it gives.
	if _, err := s.ApplyCatalog(ctx, src, fictional([]string{"a", "b", "c", "d"}, 101, 0, 7), at); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"a": 1, "b": 2, "c": 3, "d": 4}; !reflect.DeepEqual(got, want) {
		t.Errorf("first refresh: %v, want %v", got, want)
	}

	// A channel added in the middle cannot take a listed channel's number,
	// so it goes after the highest; the others keep theirs.
	at = at.Add(time.Hour)
	if _, err := s.ApplyCatalog(ctx, src, fictional([]string{"a", "new", "b", "c", "d"}), at); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"a": 1, "new": 5, "b": 2, "c": 3, "d": 4}; !reflect.DeepEqual(got, want) {
		t.Errorf("a channel in the middle: %v, want %v", got, want)
	}

	// A new playlist takes the numbers of the channels it replaces at once,
	// rather than after the missing ones' grace period.
	at = at.Add(time.Hour)
	if _, err := s.ApplyCatalog(ctx, src, fictional([]string{"x", "y", "z"}), at); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"x": 1, "y": 2, "z": 3, "d": 4, "new": 5}; !reflect.DeepEqual(got, want) {
		t.Errorf("a new playlist: %v, want %v", got, want)
	}

	// A replaced channel that comes back wants its place in the playlist,
	// and takes it from a missing channel too.
	at = at.Add(time.Hour)
	if _, err := s.ApplyCatalog(ctx, src, fictional([]string{"x", "y", "z", "a"}), at); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"x": 1, "y": 2, "z": 3, "a": 4, "new": 5}; !reflect.DeepEqual(got, want) {
		t.Errorf("a came back: %v, want %v", got, want)
	}
}

func TestNumbersAgainWhenTheSettingChanges(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	specs := []source.Settings{testSource("one", 1, NumbersProvider), testSource("two", 1, NumbersProvider)}
	ids := addSources(t, s, specs...)
	at := time.Date(2026, 11, 13, 6, 0, 0, 0, time.UTC)

	// Source one numbered by the playlist's own numbers, with one channel
	// that has gone missing; source two holds 2.
	if _, err := s.ApplyCatalog(ctx, ids["one"], fictional([]string{"a", "b", "c", "gone"}, 9780, 0, 9781, 9782), at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["one"], fictional([]string{"a", "b", "c"}, 9780, 0, 9781), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["two"], fictional([]string{"other"}, 2), at); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"a": 9780, "b": 9783, "c": 9781, "gone": 9782, "other": 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provider numbers: %v, want %v", got, want)
	}
	before := entryIDs(t, s)

	// Switching source one to playlist numbers numbers it again: its listed
	// channels by their place in the playlist, except b, whose 2 source two
	// holds, so it goes after the highest; their entries are kept, and the
	// missing channel's entry gives up its number (D47).
	specs[0].Numbers = NumbersPlaylist
	if err := s.UpdateSource(ctx, ids["one"], specs[0]); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"a": 1, "b": 4, "c": 3, "other": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("playlist numbers: %v, want %v", got, want)
	}
	after := entryIDs(t, s)
	for _, name := range []string{"a", "b", "c", "other"} {
		if after[name] != before[name] {
			t.Errorf("%s's entry changed from %d to %d", name, before[name], after[name])
		}
	}

	// The same setting again changes nothing.
	if err := s.UpdateSource(ctx, ids["one"], specs[0]); err != nil {
		t.Fatal(err)
	}
	if got := lineupNumbers(t, s); got["a"] != 1 || got["b"] != 4 {
		t.Errorf("synced again: %v", got)
	}
	srcs, err := s.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if srcs[0].Name != "one" || srcs[0].Numbers != NumbersPlaylist || srcs[1].Numbers != NumbersProvider {
		t.Errorf("sources %+v", srcs)
	}

	// Back to the provider's numbers.
	specs[0].Numbers = NumbersProvider
	if err := s.UpdateSource(ctx, ids["one"], specs[0]); err != nil {
		t.Fatal(err)
	}
	if got, want := lineupNumbers(t, s), map[string]int{"a": 9780, "b": 9782, "c": 9781, "other": 2}; !reflect.DeepEqual(got, want) {
		t.Errorf("provider numbers again: %v, want %v", got, want)
	}
}

func TestNewSourcesDefaultToPlaylistNumbers(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	addSources(t, s, source.Settings{Name: "main"})
	srcs, err := s.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcs) != 1 || srcs[0].Numbers != NumbersPlaylist {
		t.Errorf("sources %+v", srcs)
	}
}
