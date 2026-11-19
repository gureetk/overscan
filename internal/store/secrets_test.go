// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
)

// streamColumns returns a channel's stream_url and stream_secret as stored.
func streamColumns(t *testing.T, s *Store, identityKey string) (string, []byte) {
	t.Helper()
	var plain string
	var sealed []byte
	if err := s.db.QueryRow("SELECT stream_url, stream_secret FROM channels WHERE identity_key = ?", identityKey).Scan(&plain, &sealed); err != nil {
		t.Fatal(err)
	}
	return plain, sealed
}

func TestStreamAddressesAreSealed(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, testSource("one", 1, NumbersPlaylist), testSource("two", 1, NumbersPlaylist))
	at := time.Date(2026, 11, 14, 3, 0, 0, 0, time.UTC)
	const url = "http://provider.example/live/user/s3cret/1.ts"
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{{IdentityKey: "a", Name: "Alpha", StreamURL: url}}, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyCatalog(ctx, ids["two"], []CatalogChannel{{IdentityKey: "b", Name: "Bravo", StreamURL: url}}, at); err != nil {
		t.Fatal(err)
	}
	plain, sealed := streamColumns(t, s, "a")
	if plain != "" || sealed == nil || bytes.Contains(sealed, []byte("s3cret")) {
		t.Fatalf("stored %q and %d sealed bytes", plain, len(sealed))
	}
	cs, err := s.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(cs) != 2 {
		t.Fatalf("lineup %+v, %v", cs, err)
	}
	if p, err := s.PlayableEntry(ctx, cs[0].EntryID); err != nil || p.StreamURL != url {
		t.Fatalf("entry %+v, %v", p, err)
	}

	// A value moved to another source's channel does not open there.
	if _, err := s.db.Exec("UPDATE channels SET stream_secret = ? WHERE identity_key = 'b'", sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PlayableEntry(ctx, cs[1].EntryID); !errors.Is(err, ErrUnreadable) || !errors.Is(err, secrets.ErrDamaged) {
		t.Errorf("a moved value: %v", err)
	}

	// Without a key, or with another, nothing is sealed or opened.
	other := openTest(t, newPath(t))
	ids2 := addSources(t, other, testSource("one", 1, NumbersPlaylist))
	other.box.Store(nil)
	if _, err := other.ApplyCatalog(ctx, ids2["one"], []CatalogChannel{{IdentityKey: "a", Name: "Alpha", StreamURL: url}}, at); !errors.Is(err, ErrNoKey) {
		t.Errorf("no key: %v", err)
	}
	s.UseKey(secrets.NewRandomBox())
	if _, err := s.PlayableEntry(ctx, cs[0].EntryID); !errors.Is(err, ErrUnreadable) || !errors.Is(err, secrets.ErrOtherKey) {
		t.Errorf("another key: %v", err)
	}
	elsewhere, err := s.StreamsSealedElsewhere(ctx)
	if err != nil || len(elsewhere) != 2 || elsewhere["one"] != 1 || elsewhere["two"] != 1 {
		t.Errorf("sealed elsewhere: %v, %v", elsewhere, err)
	}
	// A refresh seals the source's addresses again with the store's key.
	if _, err := s.ApplyCatalog(ctx, ids["one"], []CatalogChannel{{IdentityKey: "a", Name: "Alpha", StreamURL: url}}, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if p, err := s.PlayableEntry(ctx, cs[0].EntryID); err != nil || p.StreamURL != url {
		t.Errorf("after a refresh: %+v, %v", p, err)
	}
	if elsewhere, _ = s.StreamsSealedElsewhere(ctx); len(elsewhere) != 1 || elsewhere["two"] != 1 {
		t.Errorf("sealed elsewhere after a refresh: %v", elsewhere)
	}
}

func TestSealStreamURLsFromOlderVersions(t *testing.T) {
	ctx := context.Background()
	path := newPath(t)

	// Schema 8 kept the addresses as they were.
	old, err := open(ctx, path, quiet(), migrations[:8])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.db.Exec(`
		INSERT INTO sources (id, name, type) VALUES (1, 'one', 'm3u');
		INSERT INTO channels (id, source_id, identity_key, name, stream_url, position, listed_at)
			VALUES (1, 1, 'a', 'Alpha', 'http://provider.example/a?p=s3cret', 1, '2026-11-14T03:00:00Z'),
			       (2, 1, 'b', 'Bravo', 'http://provider.example/b?p=s3cret', 2, '2026-11-14T03:00:00Z');
		INSERT INTO lineup_entries (id, lineup_id, channel_id, number) VALUES (1, 1, 1, 1), (2, 1, 2, 2);`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(ctx, path, quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	// Without a key, the old addresses still play; nothing can be sealed.
	if p, err := s.PlayableEntry(ctx, 1); err != nil || p.StreamURL != "http://provider.example/a?p=s3cret" {
		t.Fatalf("before sealing: %+v, %v", p, err)
	}
	if _, err := s.SealStreamURLs(ctx); !errors.Is(err, ErrNoKey) {
		t.Errorf("sealing without a key: %v", err)
	}

	s.UseKey(secrets.NewRandomBox())
	if n, err := s.SealStreamURLs(ctx); err != nil || n != 2 {
		t.Fatalf("sealed %d, %v", n, err)
	}
	if plain, sealed := streamColumns(t, s, "b"); plain != "" || sealed == nil {
		t.Errorf("after sealing: %q, %d bytes", plain, len(sealed))
	}
	// Nothing of the old addresses stays in the files.
	for _, name := range []string{path, path + "-wal"} {
		data, err := os.ReadFile(name)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("s3cret")) {
			t.Errorf("%s still holds an old address", filepath.Base(name))
		}
	}
	if p, err := s.PlayableEntry(ctx, 2); err != nil || p.StreamURL != "http://provider.example/b?p=s3cret" {
		t.Errorf("after sealing: %+v, %v", p, err)
	}
	if n, err := s.SealStreamURLs(ctx); err != nil || n != 0 {
		t.Errorf("sealing again: %d, %v", n, err)
	}
}
