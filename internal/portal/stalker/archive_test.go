// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
)

// withArchive gives the first source's channels the guide of addGuide, and
// Channel 00 an archive of a day, which its playlist describes. It returns
// the lineup entries by name.
func (h *harness) withArchive() map[string]int64 {
	h.t.Helper()
	ctx := context.Background()
	entries := h.addGuide()
	srcs, err := h.store.Sources(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	var one []store.CatalogChannel
	for i := range 20 {
		c := store.CatalogChannel{IdentityKey: strconv.Itoa(i), Name: "Channel " + pad(i), Number: i + 1,
			StreamURL: "http://streams.example/" + strconv.Itoa(i), ArchiveDays: 3}
		if i == 0 {
			c.ArchiveDays, c.Catchup = 1, catchup.Playlist{Mode: catchup.ModeShift}
		}
		if i%2 == 0 {
			c.Category = "News"
		} else {
			c.Category = "Sports"
		}
		c.XMLTVID = "ch" + strconv.Itoa(i) + ".example"
		c.Logo = "http://logos.example/x.png"
		one = append(one, c)
	}
	if _, err := h.store.ApplyCatalog(ctx, srcs[0].ID, one, h.now); err != nil {
		h.t.Fatal(err)
	}
	h.hub.SetSources(map[int64]hub.Source{srcs[0].ID: {Name: srcs[0].Name, MaxConnections: 1}})
	return entries
}

func pad(i int) string {
	if i < 10 {
		return "0" + strconv.Itoa(i)
	}
	return strconv.Itoa(i)
}

func TestArchiveFlags(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	ids := h.withArchive()
	token := h.handshake(macA)
	var all struct{ Data []itvChannel }
	h.js(h.call("type=itv&action=get_all_channels", macA, token), &all)
	for _, c := range all.Data {
		want := itvChannel{TVArchive: "0", EnableTVArchive: "0", Archive: "0", TVArchiveDuration: "0"}
		if c.ID == strconv.FormatInt(ids["Channel 00"], 10) {
			want = itvChannel{TVArchive: "1", EnableTVArchive: "1", Archive: "1", TVArchiveDuration: "24"}
		}
		if c.TVArchive != want.TVArchive || c.EnableTVArchive != want.EnableTVArchive || c.Archive != want.Archive ||
			c.TVArchiveDuration != want.TVArchiveDuration {
			t.Errorf("%s: %s %s %s %s", c.Name, c.TVArchive, c.EnableTVArchive, c.Archive, c.TVArchiveDuration)
		}
	}
}

// TestArchivedProgrammes checks the day's table of a channel with an
// archive: the programmes that have ended within it are marked, and every
// programme's ID stays the same when the guide is read again.
func TestArchivedProgrammes(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	ids := h.withArchive()
	token := h.handshake(macA)
	ch := strconv.FormatInt(ids["Channel 00"], 10)
	table := func() []epgProgramme {
		var page epgPage
		h.js(h.callIn("UTC", "type=epg&action=get_simple_data_table&ch_id="+ch+"&date=2026-11-12&p=1", macA, token), &page)
		var out []epgProgramme
		out = append(out, page.Data...)
		h.js(h.callIn("UTC", "type=epg&action=get_simple_data_table&ch_id="+ch+"&date=2026-11-13", macA, token), &page)
		return append(out, page.Data...)
	}
	// P0 began at 23:00 on the 12th, three hours before h.now, P1 and P2
	// have ended since, and P3 is on now.
	got := table()
	if len(got) < 4 || got[0].Name != "P0" {
		t.Fatalf("table %+v", got)
	}
	for i, p := range got[:5] {
		want := "0"
		if i < 3 {
			want = "1"
		}
		if p.MarkArchive != want {
			t.Errorf("%s: mark_archive %s, want %s", p.Name, p.MarkArchive, want)
		}
	}
	entry, minute, ok := programmeOf(got[1].ID)
	if !ok || entry != ids["Channel 00"] || !minute.Equal(h.now.Add(-2*time.Hour)) {
		t.Errorf("P1's ID %s names %d at %s", got[1].ID, entry, minute)
	}
	// Reading the guide again keeps the IDs.
	ctx := context.Background()
	srcs, err := h.store.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	gcs, err := h.store.GuideChannels(ctx, srcs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	w, err := h.store.BeginGuide(ctx, srcs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 33 {
		at := h.now.Add(time.Duration(i-3) * time.Hour)
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: gcs[0].ID, Start: at, Stop: at.Add(time.Hour),
			Title: "P" + strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(ctx, h.now, "hash"); err != nil {
		t.Fatal(err)
	}
	again := table()
	if again[1].ID != got[1].ID || again[1].Name != "P1" {
		t.Errorf("after a read, P1 is %+v, was %s", again[1], got[1].ID)
	}
}

func TestCreateArchiveLink(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	ids := h.withArchive()
	token := h.handshake(macA)
	type link struct{ Cmd, Error string }
	create := func(cmd string) link {
		t.Helper()
		var l link
		h.js(h.call("type=tv_archive&action=create_link&cmd="+url.QueryEscape(cmd)+"&series=&forced_storage=undefined", macA, token), &l)
		return l
	}
	p1 := programmeID(ids["Channel 00"], h.now.Add(-2*time.Hour))
	l := create("auto /media/" + p1 + ".mpg")
	if l.Error != "" || !strings.HasPrefix(l.Cmd, "ffmpeg http://example.com/archive/") || !strings.HasSuffix(l.Cmd, ".ts") {
		t.Errorf("P1: %+v", l)
	}
	if l := create("http://portal.example/media/" + p1 + ".mpg"); l.Error != "" {
		t.Errorf("as an address: %+v", l)
	}
	for name, cmd := range map[string]string{
		"on now":                "auto /media/" + programmeID(ids["Channel 00"], h.now) + ".mpg",
		"to come":               "auto /media/" + programmeID(ids["Channel 00"], h.now.Add(2*time.Hour)) + ".mpg",
		"a channel without one": "auto /media/" + programmeID(ids["Channel 01"], h.now.Add(-30*time.Minute)) + ".mpg",
		"a hidden channel":      "auto /media/" + programmeID(ids["Channel 02"], h.now.Add(-30*time.Minute)) + ".mpg",
		"no programme then":     "auto /media/" + programmeID(ids["Channel 00"], h.now.Add(-150*time.Minute)) + ".mpg",
		"a film's":              "auto /media/12.mpg",
		"garbled":               "auto /media/x.mpg",
		"empty":                 "",
		"another path":          "auto /ch/" + p1,
	} {
		if l := create(cmd); l.Error != "nothing_to_play" || l.Cmd != "" {
			t.Errorf("%s: %+v", name, l)
		}
	}

	// A locked channel is not there for a Stalker client once there is a
	// PIN (D55).
	ctx := context.Background()
	if err := h.store.SetEntriesLocked(ctx, store.DefaultLineupID, []int64{ids["Channel 00"]}, true); err != nil {
		t.Fatal(err)
	}
	hash, err := admin.HashPassword(ctx, "2468", admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetAccountPIN(ctx, 1, hash); err != nil {
		t.Fatal(err)
	}
	if l := create("auto /media/" + p1 + ".mpg"); l.Error != "nothing_to_play" {
		t.Errorf("locked: %+v", l)
	}
}

func TestProgrammeIDs(t *testing.T) {
	start := time.Date(2026, 12, 4, 20, 0, 30, 0, time.UTC)
	id := programmeID(77, start)
	entry, minute, ok := programmeOf(id)
	if !ok || entry != 77 || !minute.Equal(start.Truncate(time.Minute)) {
		t.Errorf("%s: %d %s %v", id, entry, minute, ok)
	}
	if n, err := strconv.ParseInt(id, 10, 64); err != nil || n >= 1<<53 {
		t.Errorf("%s does not fit a client's number", id)
	}
	for _, bad := range []string{"", "x", "0", "-5", "12"} {
		if _, _, ok := programmeOf(bad); ok {
			t.Errorf("%q names a programme", bad)
		}
	}
}
