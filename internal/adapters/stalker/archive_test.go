// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
)

// archiveNow is the fake portal's clock in the archive tests: its archive
// of Example Sports holds the two-hour programmes that began on the odd
// hours from 11:00 on 3 December to 07:00 on 4 December, UTC.
var archiveNow = time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC)

func archivePortal(t *testing.T) *stalkertest.Portal {
	t.Helper()
	p := stalkertest.New(t)
	p.Now = func() time.Time { return archiveNow }
	return p
}

// fetch returns what an address plays.
func fetch(t *testing.T, address string) string {
	t.Helper()
	resp, err := http.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", resp.Status, b)
	}
	return string(b)
}

func TestArchiveLink(t *testing.T) {
	p := archivePortal(t)
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 12, day, hour, minute, 0, 0, time.UTC) }

	link, err := c.ArchiveLink(ctx, "102", at(4, 5, 0))
	if err != nil || !strings.HasPrefix(link, p.URL+"/play/archive.ts?token=") {
		t.Fatalf("link %q, %v", link, err)
	}
	if got := fetch(t, link); got != string(stalkertest.Content(stalkertest.ProgrammeID("102", at(4, 5, 0)))) {
		t.Errorf("played %.40q", got)
	}
	if n := p.Calls("epg/get_simple_data_table"); n != 1 {
		t.Errorf("%d pages of the day's guide read, want 1", n)
	}

	// A start a few minutes out finds the programme nearest it.
	if _, err := c.ArchiveLink(ctx, "102", at(4, 5, 3)); err != nil {
		t.Errorf("three minutes late: %v", err)
	}
	// One on the day's second page.
	before := p.Calls("epg/get_simple_data_table")
	if _, err := c.ArchiveLink(ctx, "102", at(3, 21, 0)); err != nil {
		t.Errorf("on page 2: %v", err)
	}
	if n := p.Calls("epg/get_simple_data_table") - before; n != 2 {
		t.Errorf("%d pages read for the second page's programme, want 2", n)
	}

	// Programmes that the archive does not hold: one that began before it
	// reaches back, one that has not ended, one more than five minutes
	// from any, and one on a channel without an archive.
	for _, tc := range []struct {
		channel string
		start   time.Time
	}{{"102", at(3, 9, 0)}, {"102", at(4, 9, 0)}, {"102", at(4, 5, 7)}, {"101", at(4, 5, 0)}} {
		if _, err := c.ArchiveLink(ctx, tc.channel, tc.start); !errors.Is(err, ErrNotArchived) {
			t.Errorf("%s at %s: %v", tc.channel, tc.start.Format(time.Kitchen), err)
		}
	}
	if n := p.Calls("tv_archive/create_link"); n != 3 {
		t.Errorf("%d links asked for, want 3", n)
	}
}

// A portal that reads its day guides' dates in a zone other than the one
// the box's cookie names lists a programme near midnight under the day
// before or after.
func TestArchiveLinkFromAnotherDay(t *testing.T) {
	for _, tc := range []struct {
		zone  string
		start time.Time
	}{
		{"Asia/Tokyo", time.Date(2026, 12, 3, 23, 0, 0, 0, time.UTC)},      // Tokyo's 4 December
		{"America/New_York", time.Date(2026, 12, 4, 3, 0, 0, 0, time.UTC)}, // New York's 3 December
	} {
		p := archivePortal(t)
		p.DayZone, _ = time.LoadLocation(tc.zone)
		c := newClient(t, p, "/stalker_portal/c/")
		link, err := c.ArchiveLink(context.Background(), "102", tc.start)
		if err != nil {
			t.Errorf("%s: %v", tc.zone, err)
			continue
		}
		if got := fetch(t, link); got != string(stalkertest.Content(stalkertest.ProgrammeID("102", tc.start))) {
			t.Errorf("%s: played %.40q", tc.zone, got)
		}
	}
	// A box whose cookie names the portal's zone finds it on the first day.
	p := archivePortal(t)
	c := newClient(t, p, "/stalker_portal/c/", func(cfg *Config) { cfg.TimeZone = "Asia/Tokyo" })
	if _, err := c.ArchiveLink(context.Background(), "102", time.Date(2026, 12, 3, 23, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if n := p.Calls("epg/get_simple_data_table"); n != 1 {
		t.Errorf("%d pages read in the portal's zone, want 1", n)
	}
}

func TestArchiveLinkRefused(t *testing.T) {
	p := archivePortal(t)
	p.LinkError = "limit"
	c := newClient(t, p, "/stalker_portal/c/")
	_, err := c.ArchiveLink(context.Background(), "102", time.Date(2026, 12, 4, 5, 0, 0, 0, time.UTC))
	var lerr *LinkError
	if !errors.As(err, &lerr) || !lerr.Limit() {
		t.Errorf("error %v", err)
	}

	// A link that is a path is on the portal's host.
	p = archivePortal(t)
	p.RelativeLinks = true
	c = newClient(t, p, "/stalker_portal/c/")
	link, err := c.ArchiveLink(context.Background(), "102", time.Date(2026, 12, 4, 5, 0, 0, 0, time.UTC))
	if err != nil || !strings.HasPrefix(link, p.URL+"/play/archive.ts?token=") {
		t.Errorf("link %q, %v", link, err)
	}
}

func TestValidProgrammeID(t *testing.T) {
	for id, want := range map[string]bool{
		"102-29912345": true, "8812": true, "a_b.c:d": true, "": false, "1 2": false, "../1": false, "1/2": false,
		strings.Repeat("9", 65): false,
	} {
		if got := validProgrammeID(id); got != want {
			t.Errorf("%q: %v", id, got)
		}
	}
}
