// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// addGuide gives the first source's channels 00 and 01 programmes, and
// hides channel 02, which has some too. Channel 00 has an hour-long
// programme every hour, P0 to P32, from three hours before h.now; channel
// 01 has one programme around h.now. It returns the lineup entries by name.
func (h *harness) addGuide() map[string]int64 {
	h.t.Helper()
	ctx := context.Background()
	h.addChannels()
	srcs, err := h.store.Sources(ctx)
	if err != nil {
		h.t.Fatal(err)
	}
	gcs, err := h.store.GuideChannels(ctx, srcs[0].ID)
	if err != nil {
		h.t.Fatal(err)
	}
	byName := map[string]int64{}
	for _, c := range gcs {
		byName[c.Name] = c.ID
	}
	w, err := h.store.BeginGuide(ctx, srcs[0].ID)
	if err != nil {
		h.t.Fatal(err)
	}
	start := h.now.Add(-3 * time.Hour)
	for i := range 33 {
		at := start.Add(time.Duration(i) * time.Hour)
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: byName["Channel 00"], Start: at, Stop: at.Add(time.Hour),
			Title: fmt.Sprintf("P%d", i), Description: "About P" + strconv.Itoa(i), Category: "Talk"}); err != nil {
			h.t.Fatal(err)
		}
	}
	for _, name := range []string{"Channel 01", "Channel 02"} {
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: byName[name], Start: h.now.Add(-30 * time.Minute),
			Stop: h.now.Add(30 * time.Minute), Title: name + " now"}); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := w.Commit(ctx, h.now, "hash"); err != nil {
		h.t.Fatal(err)
	}
	entries := map[string]int64{}
	cs, err := h.store.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range cs {
		entries[c.Name] = c.EntryID
	}
	if err := h.store.SetEntriesVisibility(ctx, store.DefaultLineupID, []int64{entries["Channel 02"]}, store.Hidden); err != nil {
		h.t.Fatal(err)
	}
	return entries
}

// callIn makes a portal call from a device in the time zone tz.
func (h *harness) callIn(tz, query, mac, token string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/stalker_portal/server/load.php?"+query+"&JsHttpRequest=1-xml", nil)
	r.AddCookie(&http.Cookie{Name: "mac", Value: url.QueryEscape(mac)})
	r.AddCookie(&http.Cookie{Name: "timezone", Value: url.QueryEscape(tz)})
	r.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.api.ServeHTTP(rec, r)
	return rec
}

func names(ps []epgProgramme) string {
	out := ""
	for i, p := range ps {
		if i > 0 {
			out += " "
		}
		out += p.Name
	}
	return out
}

func TestGetShortEPG(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.handshake(macA)
	id := entries["Channel 00"]

	var ps []epgProgramme
	h.js(h.call(fmt.Sprintf("type=itv&action=get_short_epg&ch_id=%d&size=3", id), macA, token), &ps)
	if names(ps) != "P3 P4 P5" {
		t.Fatalf("programmes %+v", ps)
	}
	want := epgProgramme{ID: ps[0].ID, ChID: strconv.FormatInt(id, 10), Name: "P3", Descr: "About P3", Category: "Talk",
		StartTimestamp: strconv.FormatInt(h.now.Unix(), 10), StopTimestamp: strconv.FormatInt(h.now.Add(time.Hour).Unix(), 10),
		MarkArchive: "0"}
	if ps[0] != want || ps[0].ID == "" {
		t.Errorf("programme\n%+v\nwant\n%+v", ps[0], want)
	}
	h.js(h.call(fmt.Sprintf("type=itv&action=get_short_epg&ch_id=%d", id), macA, token), &ps)
	if len(ps) != shortEPGSize {
		t.Errorf("%d programmes without a size", len(ps))
	}
	for _, q := range []string{"ch_id=" + strconv.FormatInt(entries["Channel 02"], 10), "ch_id=999999", "ch_id=x", ""} {
		h.js(h.call("type=itv&action=get_short_epg&"+q, macA, token), &ps)
		if len(ps) != 0 {
			t.Errorf("%s: %+v", q, ps)
		}
	}
}

func TestGetEPGInfo(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.handshake(macA)

	var info struct{ Data map[string][]epgProgramme }
	h.js(h.call("type=itv&action=get_epg_info&period=2", macA, token), &info)
	if len(info.Data) != 2 || names(info.Data[strconv.FormatInt(entries["Channel 00"], 10)]) != "P3 P4" ||
		names(info.Data[strconv.FormatInt(entries["Channel 01"], 10)]) != "Channel 01 now" {
		t.Errorf("data %+v", info.Data)
	}
	h.js(h.call("type=itv&action=get_epg_info&period=100", macA, token), &info)
	if got := len(info.Data[strconv.FormatInt(entries["Channel 00"], 10)]); got != maxEPGPeriod {
		t.Errorf("%d programmes over the longest period", got)
	}
}

func TestGetSimpleDataTable(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.handshake(macA)
	id := strconv.FormatInt(entries["Channel 00"], 10)

	for _, tc := range []struct {
		tz, query                    string
		total, page, selected, items int
		first                        string
	}{
		// h.now is 02:00 on 13 November in UTC, during P3; the day there runs P1 to P24.
		{"UTC", "", 24, 1, 3, 10, "P1"},
		{"UTC", "&p=3", 24, 3, 0, 4, "P21"},
		{"UTC", "&p=9", 24, 3, 0, 4, "P21"},
		{"UTC", "&date=2026-11-14", 8, 1, 0, 8, "P25"},
		{"UTC", "&date=2026-11-20", 0, 1, 0, 0, ""},
		// In Tokyo it is 11:00 on 13 November, whose day runs P0 to P15.
		{"Asia/Tokyo", "", 16, 1, 4, 10, "P0"},
		{"Asia/Tokyo", "&date=2026-11-12", 0, 1, 0, 0, ""},
		// A time zone that is not known is the server's own, UTC here.
		{"Nowhere/Special", "&date=bad", 24, 1, 3, 10, "P1"},
	} {
		var page epgPage
		h.js(h.callIn(tc.tz, "type=epg&action=get_simple_data_table&ch_id="+id+tc.query, macA, token), &page)
		first := ""
		if len(page.Data) > 0 {
			first = page.Data[0].Name
		}
		if page.TotalItems != tc.total || page.CurPage != tc.page || page.SelectedItem != tc.selected ||
			len(page.Data) != tc.items || page.MaxPageItems != epgPageSize || first != tc.first {
			t.Errorf("%s%s: total %d, page %d, selected %d, %d items from %q", tc.tz, tc.query,
				page.TotalItems, page.CurPage, page.SelectedItem, len(page.Data), first)
		}
	}
}
