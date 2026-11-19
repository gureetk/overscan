// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

// householdID is the account that the first schema makes.
const householdID = 1

func TestFavourites(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sports"))
	a, b := entryOf(t, s, "a").ID, entryOf(t, s, "b").ID

	for i, id := range []int64{b, a, b} { // marking again changes nothing
		if err := s.SetFavourite(ctx, householdID, id, true, at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := s.Favourites(ctx, householdID); err != nil || !reflect.DeepEqual(got, []int64{b, a}) {
		t.Errorf("favourites %v, %v; want %v", got, err, []int64{b, a})
	}
	for range 2 { // unmarking again changes nothing
		if err := s.SetFavourite(ctx, householdID, b, false, at); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := s.Favourites(ctx, householdID); !reflect.DeepEqual(got, []int64{a}) {
		t.Errorf("after unmarking: %v", got)
	}

	if err := s.EntryShown(ctx, DefaultLineupID, a); err != nil {
		t.Errorf("a shown entry: %v", err)
	}
	for _, id := range []int64{0, 999999} {
		if err := s.EntryShown(ctx, DefaultLineupID, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("entry %d: %v", id, err)
		}
	}
	if err := s.EntryShown(ctx, DefaultLineupID+1, a); !errors.Is(err, ErrNotFound) {
		t.Errorf("another lineup: %v", err)
	}

	// A channel that goes with its source takes its favourite along.
	if err := s.DeleteSource(ctx, src); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Favourites(ctx, householdID); len(got) != 0 {
		t.Errorf("after the source went: %v", got)
	}
}

func TestWatched(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	var specs []string
	for i := range MaxWatched + 5 {
		specs = append(specs, fmt.Sprintf("c%02d/News", i))
	}
	apply(t, s, src, at, grouped(specs...))
	d, err := s.ApproveDevice(ctx, "00:1A:79:00:00:01", householdID, "Box", at)
	if err != nil {
		t.Fatal(err)
	}
	entry := func(i int) int64 { return entryOf(t, s, fmt.Sprintf("c%02d", i)).ID }
	for i := range MaxWatched + 5 {
		if err := s.RecordWatch(ctx, d.ID, entry(i), at.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Watched(ctx, d.ID)
	if err != nil || len(got) != MaxWatched || got[0] != entry(MaxWatched+4) || got[MaxWatched-1] != entry(5) {
		t.Fatalf("watched %v, %v", got, err)
	}
	// Watching one again puts it first.
	if err := s.RecordWatch(ctx, d.ID, entry(10), at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.Watched(ctx, d.ID); len(got) != MaxWatched || got[0] != entry(10) {
		t.Errorf("after watching c10 again: %v", got)
	}
	// Another device has its own list.
	other, err := s.ApproveDevice(ctx, "00:1A:79:00:00:02", householdID, "Other", at)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Watched(ctx, other.ID); len(got) != 0 {
		t.Errorf("another device: %v", got)
	}
	// A device that is removed takes its list along.
	if err := s.RemoveDevice(ctx, d.MAC); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, "SELECT count(*) FROM watched").Scan(&n); err != nil || n != 0 {
		t.Errorf("rows left: %d, %v", n, err)
	}
}

func TestDeviceSettings(t *testing.T) {
	s, _, at := lineupSetup(t)
	ctx := context.Background()
	d, err := s.ApproveDevice(ctx, "00:1A:79:00:00:01", householdID, "Box", at)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.DeviceSettings(ctx, d.ID); got != "" || err != nil {
		t.Errorf("before any: %q, %v", got, err)
	}
	for _, v := range []string{`{"home":"picture"}`, `{"home":"simple","clock":"12"}`} {
		if err := s.SetDeviceSettings(ctx, d.ID, v); err != nil {
			t.Fatal(err)
		}
		if got, err := s.DeviceSettings(ctx, d.ID); got != v || err != nil {
			t.Errorf("got %q, %v; want %q", got, err, v)
		}
	}
	if err := s.SetDeviceSettings(ctx, d.ID, "{not json"); err == nil {
		t.Error("stored settings that are not JSON")
	}
}

func TestSearchProgrammes(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/Sports", "hidden/Sports"))
	ids := channelIDs(t, s)
	if err := s.SetEntriesVisibility(ctx, DefaultLineupID, []int64{entryOf(t, s, "hidden").ID}, Hidden); err != nil {
		t.Fatal(err)
	}
	w, err := s.BeginGuide(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	add := func(channel string, startMin, stopMin int, title string) {
		t.Helper()
		if err := w.Add(ctx, GuideProgramme{ChannelID: ids[channel], Start: at.Add(time.Duration(startMin) * time.Minute),
			Stop: at.Add(time.Duration(stopMin) * time.Minute), Title: title}); err != nil {
			t.Fatal(err)
		}
	}
	add("a", -120, -60, "Morning News")
	add("a", -30, 30, "Evening News")
	add("a", 30, 90, "News at Ten")
	add("b", 0, 60, "100% Sport")
	add("b", 60, 120, "Under_score")
	add("hidden", 0, 60, "Hidden News")
	if err := w.Commit(ctx, at, "hash"); err != nil {
		t.Fatal(err)
	}
	titles := func(q string, limit int) []string {
		t.Helper()
		ps, err := s.SearchProgrammes(ctx, DefaultLineupID, q, at, at.Add(24*time.Hour), limit)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, p := range ps {
			out = append(out, p.Title)
		}
		return out
	}
	for q, want := range map[string][]string{
		"NEWS": {"Evening News", "News at Ten"}, // case and the past ignored, hidden channels left out
		"%":    {"100% Sport"},
		"_":    {"Under_score"},
		"none": {},
	} {
		if got := titles(q, 10); !reflect.DeepEqual(got, want) {
			t.Errorf("%q: %v, want %v", q, got, want)
		}
	}
	if got := titles("e", 2); len(got) != 2 {
		t.Errorf("limit: %v", got)
	}
}
