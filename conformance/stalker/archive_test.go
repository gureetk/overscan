// SPDX-License-Identifier: Apache-2.0

package stalker_test

import (
	"io"
	"net/http"
	"strconv"
	"testing"
	"time"
)

// archiveChannel is what clients read of a channel's archive (protocol
// notes, section 4): its flags, and how far back it reaches, in hours.
type archiveChannel struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	TVArchive         string `json:"tv_archive"`
	EnableTVArchive   string `json:"enable_tv_archive"`
	Archive           string `json:"archive"`
	TVArchiveDuration string `json:"tv_archive_duration"`
}

// dayTable returns a channel's programmes that start on a day, in the
// client's time zone, every page of them.
func (c *client) dayTable(chID, date string) []programme {
	c.t.Helper()
	var out []programme
	for p := 1; p <= 5; p++ {
		var page struct {
			TotalItems flexInt     `json:"total_items"`
			Data       []programme `json:"data"`
		}
		c.js(&page, "epg", "get_simple_data_table", "ch_id", chID, "date", date, "p", strconv.Itoa(p))
		out = append(out, page.Data...)
		if len(out) >= int(page.TotalItems) || len(page.Data) == 0 {
			break
		}
	}
	return out
}

// A channel with an archive says so; its programmes that have ended are
// marked, and tv_archive/create_link plays one from the archive; one that
// is still to come has no link (protocol notes, sections 4 and 5).
func TestArchive(t *testing.T) {
	c := signedIn(t)
	var all struct{ Data []archiveChannel }
	c.js(&all, "itv", "get_all_channels")
	news := ""
	for _, ch := range all.Data {
		on := ch.Name == "Example News"
		if on {
			news = ch.ID
		}
		if (ch.TVArchive == "1") != on || (ch.EnableTVArchive == "1") != on || (ch.Archive == "1") != on {
			t.Errorf("%s's archive flags: %+v", ch.Name, ch)
		}
		if on && ch.TVArchiveDuration != "24" {
			t.Errorf("%s keeps %s hours of archive, want 24", ch.Name, ch.TVArchiveDuration)
		}
	}
	if news == "" {
		t.Fatal("no Example News")
	}

	london, err := time.LoadLocation("Europe/London") // the client's timezone cookie
	if err != nil {
		t.Skip("no time zone data:", err)
	}
	now := time.Now()
	var past, toCome *programme
	for _, day := range []time.Time{now.AddDate(0, 0, -1), now} {
		for _, p := range c.dayTable(news, day.In(london).Format("2006-01-02")) {
			start, stop := p.times(t)
			ended := !stop.After(now)
			if (p.MarkArchive == "1") != ended {
				t.Errorf("%s, %s to %s: mark_archive %q", p.Name, start, stop, p.MarkArchive)
			}
			if ended {
				past = &p
			} else if start.After(now) && toCome == nil {
				toCome = &p
			}
		}
	}
	if past == nil || toCome == nil {
		t.Fatalf("no programme that has ended, or none to come: %+v %+v", past, toCome)
	}

	var l link
	c.js(&l, "tv_archive", "create_link", "cmd", "auto /media/"+past.ID+".mpg", "series", "", "forced_storage", "undefined",
		"disable_ad", "0", "download", "0")
	if l.Error != "" || !hasPrefix(l.Cmd, "ffmpeg ", "ffrt ", "auto ") {
		t.Fatalf("the link of %s: %+v", past.Name, l)
	}
	// A stream that another test closed may take a moment to let go of the
	// source's one connection.
	var resp *http.Response
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		resp = play(t, l)
		if resp.StatusCode != http.StatusServiceUnavailable || time.Now().After(deadline) {
			break
		}
		resp.Body.Close()
	}
	packet := make([]byte, 188)
	_, err = io.ReadFull(resp.Body, packet)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || err != nil || packet[0] != 0x47 {
		t.Errorf("playing %s: status %d, %v, first byte %#x", past.Name, resp.StatusCode, err, packet[0])
	}
	archiveAsked.Lock()
	asked := append([]string(nil), archiveAsked.starts...)
	archiveAsked.Unlock()
	if len(asked) == 0 || asked[len(asked)-1] != past.StartTimestamp {
		t.Errorf("the provider was asked for its archive from %q, want %s", asked, past.StartTimestamp)
	}

	c.js(&l, "tv_archive", "create_link", "cmd", "auto /media/"+toCome.ID+".mpg")
	if l.Error != "nothing_to_play" || l.Cmd != "" {
		t.Errorf("a programme to come: %+v", l)
	}
}
