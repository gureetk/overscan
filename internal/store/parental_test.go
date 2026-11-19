// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestAccountPIN(t *testing.T) {
	s, _, _ := lineupSetup(t)
	ctx := context.Background()
	if a, err := s.Account(ctx, householdID); err != nil || a.PINHash != "" {
		t.Fatalf("a new account: %+v, %v", a, err)
	}
	if err := s.SetAccountPIN(ctx, householdID, "$argon2id$made-up"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, householdID); a.PINHash != "$argon2id$made-up" {
		t.Errorf("after setting: %q", a.PINHash)
	}
	if err := s.SetAccountPIN(ctx, householdID, ""); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.Account(ctx, householdID); a.PINHash != "" {
		t.Errorf("after removing: %q", a.PINHash)
	}
	if err := s.SetAccountPIN(ctx, 999, "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown account: %v", err)
	}
}

func TestLocks(t *testing.T) {
	s, src, at := lineupSetup(t)
	ctx := context.Background()
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sports", "d/Sports"))
	locked := func() []string {
		t.Helper()
		cs, err := s.LineupChannels(ctx, DefaultLineupID)
		if err != nil {
			t.Fatal(err)
		}
		out := []string{}
		for _, c := range cs {
			if c.Locked {
				out = append(out, c.Name)
			}
		}
		return out
	}
	if got := locked(); len(got) != 0 {
		t.Errorf("before any lock: %v", got)
	}

	news := groupOf(t, s, "News")
	if err := s.SetLineupGroupLocked(ctx, DefaultLineupID, news.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesLocked(ctx, DefaultLineupID, []int64{entryOf(t, s, "c").ID}, true); err != nil {
		t.Fatal(err)
	}
	if got := locked(); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("locked %v", got)
	}
	if g := groupOf(t, s, "News"); !g.Locked || !g.Edited {
		t.Errorf("group %+v", g)
	}
	if e := entryOf(t, s, "a"); e.Locked || !e.GroupLocked {
		t.Errorf("a: %+v", e)
	}
	if e := entryOf(t, s, "c"); !e.Locked || e.GroupLocked {
		t.Errorf("c: %+v", e)
	}
	p, err := s.PlayableEntry(ctx, entryOf(t, s, "b").ID)
	if err != nil || !p.Locked {
		t.Errorf("playable b: %+v, %v", p, err)
	}
	if p, _ := s.PlayableEntry(ctx, entryOf(t, s, "d").ID); p.Locked {
		t.Error("d is locked")
	}

	// Unlocking the group leaves the channel locked on its own.
	if err := s.SetLineupGroupLocked(ctx, DefaultLineupID, news.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEntriesLocked(ctx, DefaultLineupID, []int64{entryOf(t, s, "a").ID}, true); err != nil {
		t.Fatal(err)
	}
	if got := locked(); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("locked %v", got)
	}
	if err := s.SetEntriesLocked(ctx, DefaultLineupID, []int64{999999}, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown entry: %v", err)
	}
	if err := s.SetLineupGroupLocked(ctx, DefaultLineupID, 999999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("an unknown group: %v", err)
	}
	// Locks stay through a refresh.
	apply(t, s, src, at, grouped("a/News", "b/News", "c/Sports", "d/Sports"))
	if got := locked(); !reflect.DeepEqual(got, []string{"a", "c"}) {
		t.Errorf("after a refresh: %v", got)
	}
}
