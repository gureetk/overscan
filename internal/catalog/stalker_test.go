// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// portalSource returns the settings of a source that reads p as the box
// that its provider registered.
func portalSource(p *stalkertest.Portal) source.Settings {
	return source.Settings{Name: "main", Type: source.TypeStalker, URL: p.URL + "/stalker_portal/c/",
		Device: source.Device{MAC: stalkertest.MAC, Serial: "SN0001"}, Refresh: time.Hour, MaxConnections: 1,
		Numbers: source.NumbersPlaylist}
}

func newPortalRefresher(t *testing.T) (*Refresher, *store.Store, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	r := NewRefresher(st, slog.New(slog.NewTextHandler(logs, nil)), "test")
	return r, st, logs
}

func TestStalkerPortal(t *testing.T) {
	p := stalkertest.New(t)
	r, st, logs := newPortalRefresher(t)
	pool := stalker.NewPool(nil)
	r.UseStalker(pool)
	ctx := context.Background()
	settings := portalSource(p)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}

	changes, err := r.Refresh(ctx, settings, id)
	if err != nil || changes.Added != 3 {
		t.Fatalf("refresh: %+v, %v", changes, err)
	}
	channels, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range channels {
		got = append(got, strings.Join([]string{c.Name, c.Group, c.XMLTVID, c.Logo}, "|"))
	}
	want := []string{
		"Example News|News|news.example|",
		"Example Sports|Sports||http://img.example/102.png",
		"Example Weather|News||" + p.URL + "/stalker_portal/misc/logos/103.png",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("channels\n%q\nwant\n%q", got, want)
	}
	kept, err := st.GuideChannels(ctx, id)
	if err != nil || len(kept) != 3 {
		t.Fatalf("guide channels: %+v, %v", kept, err)
	}
	if channels[1].Number != 2 || kept[1].ArchiveDays != 1 || channels[1].ArchiveDays != 1 {
		t.Errorf("Example Sports: number %d, archive %d days, %d for devices", channels[1].Number, kept[1].ArchiveDays,
			channels[1].ArchiveDays)
	}
	refs, err := st.StreamsByKey(ctx, id, "xtream:") // every key but those
	if err != nil {
		t.Fatal(err)
	}
	if refs["stalker:101"] != "ffrt http://localhost/ch/101_" || refs["stalker:103"] != "ffmpeg http://localhost/ch/103_" {
		t.Errorf("stream references %q", refs)
	}
	src, err := st.Source(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if a := src.Account; a.Status != "Active" || !a.ExpiresAt.Equal(time.Unix(1804809600, 0)) {
		t.Errorf("account %+v", a)
	}

	// A refresh reads in the source's session, which the pool keeps.
	c, err := pool.Client(id, stalker.FromSettings(settings))
	if err != nil {
		t.Fatal(err)
	}
	if c.Profile().ID != "7" {
		t.Errorf("the pool's client has not signed in: %+v", c.Profile())
	}
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	if n := p.Calls("stb/handshake"); n != 1 {
		t.Errorf("%d handshakes for two refreshes, want 1", n)
	}

	// A test signs in apart.
	tested, err := r.Test(ctx, settings)
	if err != nil || tested.Channels != 3 || tested.Account == nil || tested.Account.ExpiresAt.IsZero() {
		t.Errorf("test: %+v, %v", tested, err)
	}
	if n := p.Calls("stb/handshake"); n != 2 {
		t.Errorf("%d handshakes after a test, want 2", n)
	}
	if l := logs.String(); strings.Contains(l, stalkertest.MAC) || strings.Contains(l, "SN0001") {
		t.Errorf("the log shows the box's details:\n%s", l)
	}
}

func TestStalkerPortalRefusals(t *testing.T) {
	p := stalkertest.New(t)
	r, st, _ := newPortalRefresher(t)
	ctx := context.Background()

	unknown := portalSource(p)
	unknown.Device.MAC = "00:1A:79:00:00:52"
	if _, err := r.Test(ctx, unknown); !errors.Is(err, stalker.ErrRefused) {
		t.Errorf("an unknown box: %v", err)
	}

	blocked := stalkertest.New(t)
	blocked.Blocked = true
	settings := portalSource(blocked)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, settings, id); !errors.Is(err, stalker.ErrBlocked) {
		t.Errorf("a blocked box: %v", err)
	}
	src, _ := st.Source(ctx, id)
	if src.RefreshError != stalker.ErrBlocked.Error() {
		t.Errorf("recorded error %q", src.RefreshError)
	}
}
