// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
)

func TestRepairSwitch(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	on, off := true, false
	apply(t, s, src, at, grouped("a/News", "b/News"))
	a, b := entryOf(t, s, "a"), entryOf(t, s, "b")
	if a.Repair || b.Repair || a.RepairOwn != nil {
		t.Fatalf("repaired from the start: %+v, %+v", a, b)
	}
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{a.ID}, &on); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "a"); !e.Repair || e.RepairOwn == nil || !*e.RepairOwn {
		t.Errorf("a: %+v", e)
	}
	if e := entryOf(t, s, "b"); e.Repair {
		t.Errorf("b: %+v", e)
	}
	if p, err := s.PlayableEntry(ctx, a.ID); err != nil || !p.Repair {
		t.Errorf("playable a: %+v, %v", p, err)
	}
	if p, _ := s.PlayableEntry(ctx, b.ID); p.Repair {
		t.Error("playable b is repaired")
	}
	// Setting it again changes nothing, and it stays through a refresh.
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{a.ID}, &on); err != nil {
		t.Fatal(err)
	}
	apply(t, s, src, at, grouped("a/News", "b/News"))
	if e := entryOf(t, s, "a"); !e.Repair {
		t.Errorf("after a refresh: %+v", e)
	}
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{a.ID, b.ID}, &off); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "a"); e.Repair || e.RepairOwn == nil || *e.RepairOwn {
		t.Errorf("a after turning it off: %+v", e)
	}
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{a.ID, 999999}, &on); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown entry: %v", err)
	}
	if e := entryOf(t, s, "a"); e.Repair {
		t.Error("a changed although the change failed")
	}
}

func TestRepairDefault(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	on, off := true, false
	apply(t, s, src, at, grouped("a/News", "b/News", "c/News"))
	a, b := entryOf(t, s, "a"), entryOf(t, s, "b")
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{a.ID}, &on); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{b.ID}, &off); err != nil {
		t.Fatal(err)
	}
	if d, err := s.RepairDefault(ctx); err != nil || d {
		t.Fatalf("the default at first: %v, %v", d, err)
	}

	check := func(when string, want map[string]bool) {
		t.Helper()
		for name, repaired := range want {
			e := entryOf(t, s, name)
			p, err := s.PlayableEntry(ctx, e.ID)
			if err != nil {
				t.Fatal(err)
			}
			if e.Repair != repaired || p.Repair != repaired {
				t.Errorf("%s, %s: entry %v, playable %v, want %v", when, name, e.Repair, p.Repair, repaired)
			}
		}
	}
	check("default off", map[string]bool{"a": true, "b": false, "c": false})
	if err := s.SetRepairDefault(ctx, true); err != nil {
		t.Fatal(err)
	}
	if d, _ := s.RepairDefault(ctx); !d {
		t.Error("the default did not turn on")
	}
	check("default on", map[string]bool{"a": true, "b": false, "c": true})

	// Back to the default: c follows it again, whichever way it goes.
	if err := s.SetEntriesRepair(ctx, DefaultLineupID, []int64{a.ID, b.ID}, nil); err != nil {
		t.Fatal(err)
	}
	if e := entryOf(t, s, "b"); e.RepairOwn != nil {
		t.Errorf("b's own switch %v", *e.RepairOwn)
	}
	check("all as the default, on", map[string]bool{"a": true, "b": true, "c": true})
	if err := s.SetRepairDefault(ctx, false); err != nil {
		t.Fatal(err)
	}
	check("all as the default, off", map[string]bool{"a": false, "b": false, "c": false})
}

func TestRepairHLS(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News"))
	playable := func() PlayableEntry {
		t.Helper()
		p, err := s.PlayableEntry(ctx, entryOf(t, s, "a").ID)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if on, err := s.RepairHLS(ctx); err != nil || on || playable().RepairHLS {
		t.Fatalf("at first: %v, %v; playable %v", on, err, playable().RepairHLS)
	}
	for _, want := range []bool{true, false} {
		if err := s.SetRepairHLS(ctx, want); err != nil {
			t.Fatal(err)
		}
		if on, err := s.RepairHLS(ctx); err != nil || on != want || playable().RepairHLS != want {
			t.Errorf("set to %v: %v, %v; playable %v", want, on, err, playable().RepairHLS)
		}
	}
	// It is a setting of its own: the default for repair stays as it was.
	if d, _ := s.RepairDefault(ctx); d {
		t.Error("the repair default turned on")
	}
}

func TestUpgradeKeepsRepairSwitches(t *testing.T) {
	path := newPath(t)
	ctx := context.Background()
	old, err := open(ctx, path, quiet(), migrations[:15])
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO sources (id, name, type) VALUES (1, 'one', 'm3u')`,
		`INSERT INTO channels (id, source_id, identity_key, name, stream_url, position, listed_at, repair) VALUES
			(10, 1, 'a', 'Alpha', 'sealed-a', 1, '2026-12-04T00:00:00Z', 1),
			(11, 1, 'b', 'Bravo', 'sealed-b', 2, '2026-12-04T00:00:00Z', 0)`,
	} {
		if _, err := old.db.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s := openTest(t, path)
	var alpha, bravo *bool
	rows, err := s.db.QueryContext(ctx, `SELECT id, repair FROM channels ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var own *bool
		if err := rows.Scan(&id, &own); err != nil {
			t.Fatal(err)
		}
		if id == 10 {
			alpha = own
		} else {
			bravo = own
		}
	}
	if alpha == nil || !*alpha || bravo != nil {
		t.Errorf("after the upgrade: Alpha %v, Bravo %v", alpha, bravo)
	}
	if d, err := s.RepairDefault(ctx); err != nil || d {
		t.Errorf("the default after the upgrade: %v, %v", d, err)
	}
}

func TestTranscodes(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if n, err := s.Transcodes(ctx); err != nil || n != 0 {
		t.Fatalf("at first: %d, %v", n, err)
	}
	for _, n := range []int{2, MaxTranscodes, 0, 1} {
		if err := s.SetTranscodes(ctx, n); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Transcodes(ctx); err != nil || got != n {
			t.Errorf("set %d, got %d, %v", n, got, err)
		}
	}
	for _, n := range []int{-1, MaxTranscodes + 1} {
		if err := s.SetTranscodes(ctx, n); err == nil {
			t.Errorf("%d was taken", n)
		}
	}
	if err := s.setSetting(ctx, settingTranscodeVideo, "lots"); err != nil {
		t.Fatal(err)
	}
	if n, err := s.Transcodes(ctx); err != nil || n != 0 {
		t.Errorf("a setting that is not a number: %d, %v", n, err)
	}
}

// TestConversionSwitches keeps whether films are converted, and whether
// their source's server transcodes them: both on until they are set (D78).
func TestConversionSwitches(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	for name, sw := range map[string]struct {
		get func(context.Context) (bool, error)
		set func(context.Context, bool) error
	}{
		"convert sound":     {s.ConvertsSound, s.SetConvertsSound},
		"server transcodes": {s.ServerTranscodes, s.SetServerTranscodes},
	} {
		if on, err := sw.get(ctx); err != nil || !on {
			t.Errorf("%s at first: %v, %v", name, on, err)
		}
		for _, want := range []bool{false, true, false} {
			if err := sw.set(ctx, want); err != nil {
				t.Fatal(err)
			}
			if on, err := sw.get(ctx); err != nil || on != want {
				t.Errorf("%s set %v, got %v, %v", name, want, on, err)
			}
		}
	}
}
