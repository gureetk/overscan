// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestThemes(t *testing.T) {
	ctx := context.Background()
	s := openTest(t, newPath(t))
	at := time.Date(2026, 12, 4, 18, 0, 0, 0, time.UTC)

	first, err := s.Profiles(ctx, householdID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.ProfileTheme(ctx, first[0].ID); ok || err != nil {
		t.Errorf("at first, a theme: %v, %v", ok, err)
	}
	green, replaced, err := s.InstallTheme(ctx, "Evergreen", []byte(`{"v": 1}`), at)
	if err != nil || replaced || green.Name != "Evergreen" || string(green.Data) != `{"v": 1}` {
		t.Fatalf("install: %+v, %v, %v", green, replaced, err)
	}
	// The same name, whatever its case, replaces it.
	again, replaced, err := s.InstallTheme(ctx, "EVERGREEN", []byte(`{"v": 2}`), at)
	if err != nil || !replaced || again.ID != green.ID || again.Name != "EVERGREEN" || string(again.Data) != `{"v": 2}` {
		t.Errorf("install again: %+v, %v, %v", again, replaced, err)
	}
	dusk, _, err := s.InstallTheme(ctx, "Dusk", []byte(`{"v": 3}`), at)
	if err != nil {
		t.Fatal(err)
	}

	// The household's, then a profile's own.
	if err := s.SetAccountTheme(ctx, householdID, green.ID); err != nil {
		t.Fatal(err)
	}
	alex, err := s.AddProfile(ctx, householdID, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if th, ok, err := s.ProfileTheme(ctx, alex.ID); !ok || err != nil || th.ID != green.ID {
		t.Errorf("Alex has the household's: %+v, %v, %v", th, ok, err)
	}
	if err := s.SetProfileTheme(ctx, alex.ID, dusk.ID); err != nil {
		t.Fatal(err)
	}
	if th, _, _ := s.ProfileTheme(ctx, alex.ID); th.ID != dusk.ID {
		t.Errorf("Alex's own: %+v", th)
	}
	if acct, _ := s.Account(ctx, householdID); acct.ThemeID != green.ID {
		t.Errorf("the account's theme %d", acct.ThemeID)
	}
	if p, _ := s.Profile(ctx, alex.ID); p.ThemeID != dusk.ID {
		t.Errorf("Alex's theme %d", p.ThemeID)
	}
	list, err := s.Themes(ctx)
	if err != nil || len(list) != 2 || list[0].Name != "Dusk" || !reflect.DeepEqual(list[0].Uses, []string{"Alex"}) ||
		!reflect.DeepEqual(list[1].Uses, []string{"Household"}) {
		t.Errorf("themes %+v, %v", list, err)
	}
	if err := s.SetProfileTheme(ctx, alex.ID, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing theme: %v", err)
	}
	if err := s.SetAccountTheme(ctx, 999, green.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing account: %v", err)
	}

	// Removing a theme leaves its users with the next one along.
	if err := s.RemoveTheme(ctx, dusk.ID); err != nil {
		t.Fatal(err)
	}
	if th, _, _ := s.ProfileTheme(ctx, alex.ID); th.ID != green.ID {
		t.Errorf("after removing Alex's own: %+v", th)
	}
	if err := s.RemoveTheme(ctx, green.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.ProfileTheme(ctx, alex.ID); ok {
		t.Error("a theme after removing them all")
	}
	if err := s.RemoveTheme(ctx, green.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing again: %v", err)
	}
	// IDs are never used again.
	if th, _, _ := s.InstallTheme(ctx, "Dawn", []byte("{}"), at); th.ID <= dusk.ID {
		t.Errorf("a new theme has ID %d", th.ID)
	}
}
