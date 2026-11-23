// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/streaminfo"
)

// channelID returns the ID of the channel of that name.
func channelID(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	var id int64
	if err := s.db.QueryRow("SELECT id FROM channels WHERE name = ?", name).Scan(&id); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return id
}

// A channel whose stream fails is listed, can be hidden until it plays
// again, and shows again when it does (D138).
func TestChannelHealth(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	l := int64(DefaultLineupID)
	bad := channelID(t, s, "one News 2")
	if err := s.NoteStreamFailed(ctx, bad, at); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteStreamFailed(ctx, bad, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := s.NoteStreamFailed(ctx, 9999, at); err != nil {
		t.Errorf("a channel that is gone: %v", err)
	}
	failed, total, err := s.FailedChannels(ctx)
	if err != nil || total != 1 || len(failed) != 1 || failed[0].Name != "one News 2" || failed[0].Source != "one" ||
		failed[0].Failures != 2 || !failed[0].FailedAt.Equal(at.Add(time.Minute)) || !failed[0].PlayedAt.IsZero() || failed[0].Held {
		t.Fatalf("failed %+v, %d, %v", failed, total, err)
	}

	// Hidden until it plays again: devices do not see it, and a group with
	// a first number takes its number back, as for a channel hidden by
	// hand (D137).
	if err := s.SetGroupNumbersFrom(ctx, l, groupID(t, s, l, "News"), 100); err != nil {
		t.Fatal(err)
	}
	if err := s.SetChannelsHeld(ctx, []int64{bad}, true); err != nil {
		t.Fatal(err)
	}
	names, numbers := groupOrder(t, s, l, "News")
	if len(names) != 5 || !reflect.DeepEqual(numbers, []int{100, 102, 103, 104, 105}) {
		t.Errorf("News without it: %v %v", names, numbers)
	}
	entries, _, err := s.LineupEntries(ctx, l, EntryQuery{Text: "one News 2"})
	if err != nil || len(entries) != 1 || !entries[0].Hidden || !entries[0].Health.Held || entries[0].Health.Failures != 2 {
		t.Errorf("its entry %+v, %v", entries, err)
	}
	if failed, _, _ := s.FailedChannels(ctx); len(failed) != 1 || !failed[0].Held {
		t.Errorf("failed %+v", failed)
	}

	// It plays, as a backup: it shows again, and is no longer failing.
	was, err := s.NoteStreamPlayed(ctx, bad, at.Add(time.Hour))
	if err != nil || !was {
		t.Fatalf("played: %v, %v", was, err)
	}
	if names, _ := groupOrder(t, s, l, "News"); len(names) != 6 {
		t.Errorf("News with it again: %v", names)
	}
	if failed, total, _ := s.FailedChannels(ctx); total != 0 || len(failed) != 0 {
		t.Errorf("failed after it played %+v", failed)
	}
	h, err := s.HealthOf(ctx, bad)
	if err != nil || !h.PlayedAt.Equal(at.Add(time.Hour)) || h.Failing() || h.Held || !h.FailedAt.Equal(at.Add(time.Minute)) {
		t.Errorf("its health %+v, %v", h, err)
	}
	if was, err := s.NoteStreamPlayed(ctx, bad, at.Add(2*time.Hour)); err != nil || was {
		t.Errorf("played again: %v, %v", was, err)
	}

	// Its details.
	d := streaminfo.Details{Video: "HEVC", Width: 3840, Height: 2160, BitDepth: 10, FrameRate: 50, DynamicRange: "HLG",
		Audio: []string{"AAC", "AC-3"}}
	if err := s.NoteStreamDetails(ctx, bad, d, at); err != nil {
		t.Fatal(err)
	}
	if h, err := s.HealthOf(ctx, bad); err != nil || !reflect.DeepEqual(h.Details, d) || !h.SeenAt.Equal(at) {
		t.Errorf("its details %+v, %v", h, err)
	}
	if h, err := s.HealthOf(ctx, channelID(t, s, "two News 1")); err != nil || !reflect.DeepEqual(h, ChannelHealth{}) {
		t.Errorf("a channel never played: %+v, %v", h, err)
	}
	if err := s.SetChannelsHeld(ctx, []int64{9999}, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("hiding a channel that is not: %v", err)
	}

	// Every channel that failed, hidden at once, and shown again.
	for _, name := range []string{"two News 1", "two News 2", "three News 1"} {
		if err := s.NoteStreamFailed(ctx, channelID(t, s, name), at); err != nil {
			t.Fatal(err)
		}
	}
	counts := func(failing, held int) {
		t.Helper()
		if f, h, err := s.HealthCounts(ctx); err != nil || f != failing || h != held {
			t.Errorf("counts %d, %d, %v; want %d, %d", f, h, err, failing, held)
		}
	}
	counts(3, 0)
	if n, err := s.SetFailingHeld(ctx, true); err != nil || n != 3 {
		t.Errorf("hid %d, %v", n, err)
	}
	counts(0, 3)
	if names, _ := groupOrder(t, s, l, "News"); len(names) != 3 {
		t.Errorf("News without them: %v", names)
	}
	// A source turned off: its channels leave the list.
	if err := s.SetSourceEnabled(ctx, ids["three"], false); err != nil {
		t.Fatal(err)
	}
	counts(0, 2)
	if n, err := s.SetFailingHeld(ctx, false); err != nil || n != 2 {
		t.Errorf("showed %d, %v", n, err)
	}
	counts(2, 0)
	if n, err := s.SetFailingHeld(ctx, false); err != nil || n != 0 {
		t.Errorf("showed again %d, %v", n, err)
	}
}

// A merge rule can show the copy whose stream was the best when last seen,
// and never shows first a copy hidden until it plays (D113, D138).
func TestMergePrefersTheBest(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)
	ids := threeSources(t, s, at)
	l := int64(DefaultLineupID)
	for name, d := range map[string]streaminfo.Details{
		"one News 1":   {Video: "H.264", Height: 720},
		"two News 1":   {Video: "HEVC", Height: 2160, DynamicRange: "HDR10"},
		"three News 1": {Video: "H.264", Height: 1080, Interlaced: true},
	} {
		if err := s.NoteStreamDetails(ctx, channelID(t, s, name), d, at); err != nil {
			t.Fatal(err)
		}
	}
	rule := rules.Rule{Action: rules.Merge, Copies: rules.ByName, Replace: "/^(one|two|three) /", Prefer: rules.Best}
	if _, err := s.AddLineupRule(ctx, l, rule); err != nil {
		t.Fatal(err)
	}
	shown := func() map[string]bool {
		names, _ := groupOrder(t, s, l, "News")
		out := map[string]bool{}
		for _, n := range names {
			out[n] = true
		}
		return out
	}
	if got := shown(); len(got) != 2 || !got["two News 1"] {
		t.Errorf("the best copy: %v", got)
	}
	// Hidden until it plays, the best copy gives way to the next best.
	if err := s.SetChannelsHeld(ctx, []int64{channelID(t, s, "two News 1")}, true); err != nil {
		t.Fatal(err)
	}
	if got := shown(); len(got) != 2 || !got["three News 1"] {
		t.Errorf("without the best: %v", got)
	}
	// The order of sources comes first.
	rs, _ := s.LineupRules(ctx, l)
	rule.Sources = []int64{ids["one"]}
	if err := s.SetLineupRule(ctx, l, rs[0].ID, rule); err != nil {
		t.Fatal(err)
	}
	if got := shown(); !got["one News 1"] {
		t.Errorf("the source first: %v", got)
	}
}
