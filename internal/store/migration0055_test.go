// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"testing"

	"overscan.gureet.ca/internal/secrets"
)

// TestMigration0055AccountHLS upgrades a database of schema 54 with two
// accounts, one with its playlist and its login on and an output profile,
// and checks that nothing changes: both keep what was set of them, and
// neither gives its players HLS (D153). The switch then turns on and off
// without touching the other outputs.
func TestMigration0055AccountHLS(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)
	old, err := open(ctx, path, quiet(), migrations[:54])
	if err != nil {
		t.Fatal(err)
	}
	if v, err := old.SchemaVersion(ctx); err != nil || v != 54 {
		t.Fatalf("the old database's schema: %d, %v", v, err)
	}
	for _, q := range []string{`
		INSERT INTO accounts (id, name, lineup_id, username, max_streams, output_m3u, output_xtream, output_profile)
			VALUES (5, 'family', 1, 'family', 3, 1, 1, 'h264-720p'),
			       (6, 'guests', 1, NULL, 0, 0, 0, '')`,
	} {
		if _, err := old.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	old.Close()

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(secrets.NewRandomBox())
	if v, err := s.SchemaVersion(ctx); err != nil || v < 55 {
		t.Fatalf("the upgraded schema: %d, %v", v, err)
	}
	family, err := s.Account(ctx, 5)
	if err != nil {
		t.Fatal(err)
	}
	if family.Outputs != (Outputs{M3U: true, Xtream: true}) || family.MaxStreams != 3 || family.OutputProfile != "h264-720p" ||
		family.Username != "family" {
		t.Errorf("family after the upgrade: %+v", family)
	}
	guests, err := s.Account(ctx, 6)
	if err != nil {
		t.Fatal(err)
	}
	if guests.Outputs != (Outputs{}) || guests.MaxStreams != 0 {
		t.Errorf("guests after the upgrade: %+v", guests)
	}

	if err := s.SetAccountOutputs(ctx, 5, Outputs{M3U: true, Xtream: true, HLS: true}); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Account(ctx, 5); err != nil || a.Outputs != (Outputs{M3U: true, Xtream: true, HLS: true}) {
		t.Errorf("family with HLS: %+v, %v", a.Outputs, err)
	}
	if err := s.SetAccountOutputs(ctx, 5, Outputs{Xtream: true}); err != nil {
		t.Fatal(err)
	}
	if a, err := s.Account(ctx, 5); err != nil || a.Outputs != (Outputs{Xtream: true}) {
		t.Errorf("family with its login alone: %+v, %v", a.Outputs, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE accounts SET output_hls = 2 WHERE id = 6`); err == nil {
		t.Error("output_hls took 2")
	}
}
