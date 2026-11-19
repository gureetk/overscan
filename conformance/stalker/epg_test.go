// SPDX-License-Identifier: Apache-2.0

package stalker_test

import (
	"strconv"
	"testing"
	"time"
)

// programme is what clients read of a programme (protocol notes, section 5).
type programme struct {
	ID             string `json:"id"`
	ChID           string `json:"ch_id"`
	Name           string `json:"name"`
	Descr          string `json:"descr"`
	StartTimestamp string `json:"start_timestamp"`
	StopTimestamp  string `json:"stop_timestamp"`
	MarkArchive    string `json:"mark_archive"`
}

func (p programme) times(t *testing.T) (time.Time, time.Time) {
	t.Helper()
	start, err1 := strconv.ParseInt(p.StartTimestamp, 10, 64)
	stop, err2 := strconv.ParseInt(p.StopTimestamp, 10, 64)
	if err1 != nil || err2 != nil || stop <= start {
		t.Fatalf("programme %+v: its times are not Unix seconds as strings, in order", p)
	}
	return time.Unix(start, 0), time.Unix(stop, 0)
}

// channelIDs returns the fixture's channel IDs by name.
func (c *client) channelIDs() map[string]string {
	c.t.Helper()
	ids := map[string]string{}
	for _, ch := range c.allChannels() {
		ids[ch.Name] = ch.ID
	}
	return ids
}

func TestShortEPG(t *testing.T) {
	c := signedIn(t)
	ids := c.channelIDs()
	var ps []programme
	c.js(&ps, "itv", "get_short_epg", "ch_id", ids["Example News"], "size", "3")
	if len(ps) != 3 {
		t.Fatalf("%d programmes, want 3: %+v", len(ps), ps)
	}
	now := time.Now()
	start, stop := ps[0].times(t)
	if start.After(now) || !stop.After(now) {
		t.Errorf("the first programme, %s to %s, is not on now", start, stop)
	}
	for i, p := range ps {
		if p.ID == "" || p.ChID != ids["Example News"] || p.Name == "" || p.MarkArchive != "0" {
			t.Errorf("programme %+v", p)
		}
		if i > 0 {
			if s, _ := p.times(t); s.Before(stop) {
				t.Errorf("programme %d starts before the one before it ends", i)
			}
			_, stop = p.times(t)
		}
	}
	c.js(&ps, "itv", "get_short_epg", "ch_id", ids["Example Weather"])
	if len(ps) != 0 {
		t.Errorf("a channel without a guide has %+v", ps)
	}
}

func TestEPGInfo(t *testing.T) {
	c := signedIn(t)
	ids := c.channelIDs()
	var info struct {
		Data map[string][]programme `json:"data"`
	}
	c.js(&info, "itv", "get_epg_info", "period", "3")
	now := time.Now()
	for _, name := range []string{"Example News", "Example Sports"} {
		ps := info.Data[ids[name]]
		if len(ps) < 2 {
			t.Errorf("%s: %+v", name, ps)
		}
		for _, p := range ps {
			if start, stop := p.times(t); !stop.After(now) || !start.Before(now.Add(3*time.Hour+time.Minute)) {
				t.Errorf("%s: %s, %s to %s, is outside the period", name, p.Name, start, stop)
			}
		}
	}
	if ps := info.Data[ids["Example Weather"]]; len(ps) != 0 {
		t.Errorf("a channel without a guide has %+v", ps)
	}
}

func TestSimpleDataTable(t *testing.T) {
	c := signedIn(t)
	ids := c.channelIDs()
	london, err := time.LoadLocation("Europe/London") // the client's timezone cookie
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	var page struct {
		TotalItems   flexInt     `json:"total_items"`
		MaxPageItems flexInt     `json:"max_page_items"`
		SelectedItem flexInt     `json:"selected_item"`
		CurPage      flexInt     `json:"cur_page"`
		Data         []programme `json:"data"`
	}
	c.js(&page, "epg", "get_simple_data_table", "ch_id", ids["Example News"])
	today := time.Now().In(london).Format("2006-01-02")
	if page.TotalItems == 0 || len(page.Data) == 0 || int(page.MaxPageItems) < len(page.Data) || page.SelectedItem == 0 {
		t.Fatalf("today's page %+v", page)
	}
	selected := page.Data[page.SelectedItem-1]
	if start, stop := selected.times(t); start.After(time.Now()) || !stop.After(time.Now()) {
		t.Errorf("the selected programme, %s, is not on now", selected.Name)
	}
	for _, p := range page.Data {
		if start, _ := p.times(t); start.In(london).Format("2006-01-02") != today {
			t.Errorf("%s does not start today (%s) in the device's time zone", p.Name, today)
		}
	}
	c.js(&page, "epg", "get_simple_data_table", "ch_id", ids["Example News"], "date",
		time.Now().In(london).AddDate(0, 0, 5).Format("2006-01-02"), "p", "1")
	if page.TotalItems != 0 || len(page.Data) != 0 {
		t.Errorf("a day the guide does not reach: %+v", page)
	}
}
