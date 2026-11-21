// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"net/http"
	"net/url"
	"testing"

	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
)

// FuzzChannels feeds a portal's answers to get_genres, get_all_channels
// and create_link to their readers, which must never fail on them, keep a
// channel without an ID or a command, or give a link or a logo that is not
// an http or https address.
func FuzzChannels(f *testing.F) {
	f.Add(`{"js":{"data":[{"id":"1","name":"News","number":"1","cmd":"ffrt http://portal.example/ch/1","logo":"/logo.png",
		"tv_genre_id":"10","xmltv_id":"news","tv_archive":"1","tv_archive_duration":"72"},{"id":1},[]]}}`)
	f.Add(`{"js":[{"id":"10","title":"News","censored":1},{"id":["x"]}]}`)
	f.Add(`{"js":{"cmd":"ffmpeg javascript:alert(1)","error":""}}`)
	f.Add(`{"js":{"cmd":"","error":"nothing_to_play"}}`)
	c, _ := New(Config{Portal: "http://portal.example/c/", MAC: stalkertest.MAC}, http.DefaultClient)
	f.Fuzz(func(t *testing.T, body string) {
		js, err := jsMember([]byte(body))
		if err != nil {
			return
		}
		var all struct {
			Data list[channelJSON] `json:"data"`
		}
		if decode(js, &all) == nil {
			if chans, _, err := c.keep(all.Data); err == nil {
				for _, ch := range chans {
					if ch.ID == "" || ch.Cmd == "" || ch.Number < 0 || ch.ArchiveHours < 0 || !webAddress(ch.Logo) {
						t.Errorf("kept %+v", ch)
					}
				}
			}
		}
		var gs list[genreJSON]
		if decode(js, &gs) == nil {
			for _, g := range genres(gs) {
				if g.ID == "" {
					t.Errorf("kept a genre without an ID: %+v", g)
				}
			}
		}
		var answer linkJSON
		if decode(js, &answer) == nil {
			if address, err := c.link(answer); err == nil && (address == "" || !webAddress(address)) {
				t.Errorf("link %q", address)
			}
		}
	})
}

// webAddress reports whether s is empty or an http or https address with
// a host.
func webAddress(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// FuzzReferences reads references and dates as the store keeps them and
// portals send them, which must never fail, and a reference that a Play or
// an Opening made reads back as it was.
func FuzzReferences(f *testing.F) {
	f.Add("cmd=ffrt+http%3A%2F%2Fportal.example%2Fmedia%2F1.mpg&series=3")
	f.Add("film=12")
	f.Add("shape=episodes&id=7&season=2&episodes=1,2,3")
	f.Add("2026-12-04 10:00:00")
	f.Add("1796378400")
	f.Add("December 4, 2026, 10:00 am")
	f.Fuzz(func(t *testing.T, ref string) {
		if p, ok := ParsePlay(ref); ok {
			if again, ok := ParsePlay(p.Ref()); !ok || again != p {
				t.Errorf("play %+v reads back as %+v, %v", p, again, ok)
			}
		}
		if o, ok := ParseOpening(ref); ok {
			if again, ok := ParseOpening(o.Ref()); !ok || again.Ref() != o.Ref() {
				t.Errorf("opening %+v reads back as %+v, %v", o, again, ok)
			}
		}
		if d, ok := ParseDate(ref); ok && (d.Year() < 1970 || d.Year() > 10000) {
			t.Errorf("date %q reads as %v", ref, d)
		}
	})
}
