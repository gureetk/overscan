// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// addChannels gives the lineup fictional channels from two sources: the
// second source's News shares a genre with the first's.
func (h *harness) addChannels() {
	h.t.Helper()
	ctx := context.Background()
	ids := map[string]int64{}
	for _, name := range []string{"one", "two"} {
		id, err := h.store.AddSource(ctx, source.Settings{Name: name, URL: "http://provider.example/" + name + ".m3u", MaxConnections: 1})
		if err != nil {
			h.t.Fatal(err)
		}
		ids[name] = id
	}
	var one []store.CatalogChannel
	for i := range 20 {
		cat := "Sports"
		if i%2 == 0 {
			cat = "News"
		}
		one = append(one, store.CatalogChannel{
			IdentityKey: fmt.Sprint(i), Name: fmt.Sprintf("Channel %02d", i), Number: i + 1, Category: cat,
			XMLTVID: fmt.Sprintf("ch%d.example", i), StreamURL: fmt.Sprintf("http://streams.example/%d", i),
			Logo: "http://logos.example/x.png", ArchiveDays: 3,
		})
	}
	if _, err := h.store.ApplyCatalog(ctx, ids["one"], one, h.now); err != nil {
		h.t.Fatal(err)
	}
	two := []store.CatalogChannel{
		{IdentityKey: "a", Name: "aardvark news", Category: "News", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Ungrouped", StreamURL: "http://streams.example/b"},
	}
	if _, err := h.store.ApplyCatalog(ctx, ids["two"], two, h.now); err != nil {
		h.t.Fatal(err)
	}
}

func TestGetGenres(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.addChannels()
	token := h.handshake(macA)

	var genres []genre
	h.js(h.call("type=itv&action=get_genres", macA, token), &genres)
	if len(genres) != 3 {
		t.Fatalf("genres %+v", genres)
	}
	if genres[0] != (genre{ID: "*", Title: "All", Censored: "0"}) || genres[1].Title != "News" || genres[2].Title != "Sports" {
		t.Errorf("genres %+v", genres)
	}
}

func TestGetAllChannels(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.addChannels()
	token := h.handshake(macA)

	var all struct{ Data []itvChannel }
	h.js(h.call("type=itv&action=get_all_channels", macA, token), &all)
	if len(all.Data) != 22 {
		t.Fatalf("%d channels, want 22", len(all.Data))
	}
	first := all.Data[0]
	if first.Name != "Channel 00" || first.Number != "1" || first.XMLTVID != "ch0.example" || first.Censored != "0" {
		t.Errorf("first channel %+v", first)
	}
	if first.Cmd != "ffrt http://example.com/ch/"+first.ID {
		t.Errorf("cmd %q", first.Cmd)
	}
	for _, c := range all.Data {
		if c.TVArchive != "0" || strings.Contains(c.Cmd, "streams.example") {
			t.Errorf("channel %+v gives away an archive or a stream address", c)
		}
		// A logo comes from Overscan, never from its source (D54).
		if c.Logo != "" && (!strings.HasPrefix(c.Logo, "http://example.com/artwork/logo/"+c.ID+"/") || strings.Contains(c.Logo, "logos.example")) {
			t.Errorf("channel %s: logo %q", c.ID, c.Logo)
		}
	}
	if first.Logo == "" {
		t.Error("a channel with a logo has none")
	}

	// Channels with the same group name share one genre ID across sources.
	var genres []genre
	h.js(h.call("type=itv&action=get_genres", macA, token), &genres)
	news := genres[1].ID
	var aardvark itvChannel
	for _, c := range all.Data {
		if c.Name == "aardvark news" {
			aardvark = c
		}
	}
	if aardvark.TVGenreID != news || first.TVGenreID != news {
		t.Errorf("News genre %q; channel genres %q and %q", news, first.TVGenreID, aardvark.TVGenreID)
	}
}

func TestGetOrderedList(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.addChannels()
	token := h.handshake(macA)
	list := func(query string) page {
		t.Helper()
		var p page
		h.js(h.call("type=itv&action=get_ordered_list&"+query, macA, token), &p)
		return p
	}

	p := list("genre=*&p=1&sortby=number&fav=0&hd=0")
	if p.TotalItems != 22 || p.MaxPageItems != 14 || len(p.Data) != 14 || p.Data[0].Number != "1" {
		t.Errorf("page 1: total %d, max %d, %d items", p.TotalItems, p.MaxPageItems, len(p.Data))
	}
	if p = list("genre=*&p=2"); len(p.Data) != 8 {
		t.Errorf("page 2: %d items, want 8", len(p.Data))
	}
	if p = list("genre=*&p=9"); len(p.Data) != 0 || p.TotalItems != 22 {
		t.Errorf("page past the end: %+v", p)
	}
	if p = list("genre=*&p=0"); len(p.Data) != 14 {
		t.Errorf("page 0 is not page 1: %d items", len(p.Data))
	}

	var genres []genre
	h.js(h.call("type=itv&action=get_genres", macA, token), &genres)
	if p = list("genre=" + genres[2].ID + "&p=1"); p.TotalItems != 10 {
		t.Errorf("Sports: %d channels, want 10", p.TotalItems)
	}

	if p = list("genre=*&p=1&sortby=name"); p.Data[0].Name != "aardvark news" {
		t.Errorf("first by name: %q", p.Data[0].Name)
	}
	if p = list("genre=*&p=1&fav=1"); p.TotalItems != 0 {
		t.Errorf("favourites: %d channels, want none yet", p.TotalItems)
	}
	// The account's favourites, as its boxes mark them (D53).
	all := list("genre=*&p=1")
	for _, i := range []int{3, 1} {
		id, _ := strconv.ParseInt(all.Data[i].ID, 10, 64)
		if err := h.store.SetFavourite(context.Background(), 1, id, true, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if p = list("genre=*&p=1&fav=1"); p.TotalItems != 2 || p.Data[0].ID != all.Data[1].ID || p.Data[1].ID != all.Data[3].ID {
		t.Errorf("favourites: %+v", p)
	}
}

func TestChannelListsNeedApproval(t *testing.T) {
	h := newHarness(t)
	if _, err := h.store.BlockDevice(context.Background(), macA); err != nil {
		t.Fatal(err)
	}
	token := h.handshake(macA)
	wantAuthFailed(t, h.call("type=itv&action=get_all_channels", macA, token))
}

func TestCreateLink(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.addChannels()
	token := h.handshake(macA)

	var all struct{ Data []itvChannel }
	h.js(h.call("type=itv&action=get_all_channels", macA, token), &all)
	type link struct {
		Cmd   string `json:"cmd"`
		Error string `json:"error"`
	}
	var got link
	h.js(h.call("type=itv&action=create_link&cmd="+url.QueryEscape(all.Data[0].Cmd)+"&series=&forced_storage=undefined&disable_ad=0&download=0", macA, token), &got)
	if got.Error != "" || !strings.HasPrefix(got.Cmd, "ffmpeg http://example.com/live/") || !strings.HasSuffix(got.Cmd, ".ts") {
		t.Errorf("link %+v", got)
	}
	if strings.Contains(got.Cmd, "streams.example") {
		t.Errorf("the link gives away the stream address: %q", got.Cmd)
	}
	// The device's list of what it watched has the channel (D53).
	d, err := h.store.Device(context.Background(), macA)
	if err != nil {
		t.Fatal(err)
	}
	if watched, err := h.store.Watched(context.Background(), d.ID); err != nil || len(watched) != 1 ||
		strconv.FormatInt(watched[0], 10) != all.Data[0].ID {
		t.Errorf("watched %v, %v", watched, err)
	}

	for _, cmd := range []string{"", "ffrt http://example.com/ch/999999", "ffrt http://example.com/media/1.mpg", "garbage"} {
		h.js(h.call("type=itv&action=create_link&cmd="+url.QueryEscape(cmd), macA, token), &got)
		if got.Cmd != "" || got.Error != "nothing_to_play" {
			t.Errorf("cmd %q: %+v", cmd, got)
		}
	}
}

func TestEntryFromCmd(t *testing.T) {
	for cmd, want := range map[string]int64{
		"ffrt http://host:8080/ch/42":              42,
		"ffmpeg http://host/ch/7":                  7,
		"http://host/ch/7":                         7,
		"auto /ch/9":                               9,
		"ffrt http://host/ch/0":                    0,
		"ffrt http://host/ch/-1":                   0,
		"ffrt http://host/ch/12/extra":             0,
		"ffrt http://host/live/12":                 0,
		"ffrt http://host/ch/99999999999999999999": 0,
	} {
		id, ok := entryFromCmd(cmd)
		if (want == 0 && ok) || (want != 0 && (!ok || id != want)) {
			t.Errorf("entryFromCmd(%q) = %d, %v; want %d", cmd, id, ok, want)
		}
	}
}

func TestLogoURL(t *testing.T) {
	r := httptest.NewRequest("GET", "http://192.0.2.10:8080/stalker_portal/server/load.php", nil)
	c := store.LineupChannel{EntryID: 5, Logo: "http://img.example/x.png"}
	if got, want := logoURL(r, c), "http://192.0.2.10:8080"+artwork.LogoPath(5, c.Logo); got != want {
		t.Errorf("logo %q, want %q", got, want)
	}
	if got := logoURL(r, store.LineupChannel{EntryID: 5}); got != "" {
		t.Errorf("no logo: %q", got)
	}
}

func TestLockedChannelsAreLeftOut(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.addChannels()
	token := h.handshake(macA)
	ctx := context.Background()
	var all struct{ Data []itvChannel }
	h.js(h.call("type=itv&action=get_all_channels", macA, token), &all)
	first, _ := strconv.ParseInt(all.Data[0].ID, 10, 64)
	if err := h.store.SetEntriesLocked(ctx, store.DefaultLineupID, []int64{first}, true); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var got struct{ Data []itvChannel }
		h.js(h.call("type=itv&action=get_all_channels", macA, token), &got)
		return len(got.Data)
	}
	if n := count(); n != len(all.Data) {
		t.Errorf("a lock without a PIN left out a channel: %d", n)
	}
	if err := h.store.SetAccountPIN(ctx, 1, "$argon2id$made-up"); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != len(all.Data)-1 {
		t.Errorf("%d channels, want %d", n, len(all.Data)-1)
	}
	var got struct {
		Cmd   string `json:"cmd"`
		Error string `json:"error"`
	}
	h.js(h.call("type=itv&action=create_link&cmd="+url.QueryEscape(all.Data[0].Cmd), macA, token), &got)
	if got.Cmd != "" || got.Error != "nothing_to_play" {
		t.Errorf("link to a locked channel: %+v", got)
	}
}
