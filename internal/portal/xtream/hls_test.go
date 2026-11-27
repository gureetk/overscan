// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// An account's HLS for players (D153): off, its login offers MPEG-TS
// alone, get.php gives .ts addresses whatever it is asked, and a channel
// whose source sends MPEG-TS has no .m3u8; on, the login offers HLS after
// MPEG-TS, get.php gives .m3u8 addresses, but for radio stations, when
// asked with output=m3u8, and a channel that the hub cannot cut, here for
// want of a folder for buffers, plays at its .m3u8 address as at its .ts.
func TestHLSForPlayers(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	a, r := fmt.Sprint(h.ids["Alpha"]), fmt.Sprint(h.ids["Radio Three"])
	login := "username=" + username + "&password=" + password
	formats := func() []string {
		var info struct {
			User userInfo `json:"user_info"`
		}
		h.api(&info)
		return info.User.Formats
	}
	links := func(query string) (alpha, radio string) {
		resp, body := h.get("/get.php?" + login + "&type=m3u_plus" + query)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get.php%s: %s", query, resp.Status)
		}
		for _, l := range strings.Split(body, "\n") {
			switch {
			case strings.HasSuffix(l, "/"+a+".ts"), strings.HasSuffix(l, "/"+a+".m3u8"):
				alpha = l[strings.LastIndex(l, "/")+1:]
			case strings.HasSuffix(l, "/"+r+".ts"), strings.HasSuffix(l, "/"+r+".m3u8"):
				radio = l[strings.LastIndex(l, "/")+1:]
			}
		}
		return alpha, radio
	}

	if f := formats(); len(f) != 1 || f[0] != "ts" {
		t.Errorf("formats while off: %v", f)
	}
	if alpha, radio := links("&output=m3u8"); alpha != a+".ts" || radio != r+".ts" {
		t.Errorf("get.php with output=m3u8 while off: %s, %s", alpha, radio)
	}
	if resp, body := h.get(path("live", a+".m3u8")); resp.StatusCode != http.StatusNotFound {
		t.Errorf(".m3u8 while off: %s %q", resp.Status, body)
	}

	acct, err := h.store.Account(ctx, store.HouseholdAccount)
	if err != nil {
		t.Fatal(err)
	}
	o := acct.Outputs
	o.HLS = true
	if err := h.store.SetAccountOutputs(ctx, store.HouseholdAccount, o); err != nil {
		t.Fatal(err)
	}
	if f := formats(); len(f) != 2 || f[0] != "ts" || f[1] != "m3u8" {
		t.Errorf("formats while on: %v", f)
	}
	for _, query := range []string{"&output=m3u8", "&output=HLS"} {
		if alpha, radio := links(query); alpha != a+".m3u8" || radio != r+".ts" {
			t.Errorf("get.php%s while on: %s, %s", query, alpha, radio)
		}
	}
	for _, query := range []string{"", "&output=ts", "&output=mpegts"} {
		if alpha, _ := links(query); alpha != a+".ts" {
			t.Errorf("get.php%s while on: %s", query, alpha)
		}
	}
	resp, err := http.Get(h.front.URL + path("live", a+".m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 188*2)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || resp.Header.Get("Content-Type") != "video/mp2t" || buf[0] != 0x47 {
		t.Errorf(".m3u8 of a channel that the hub cannot cut: %s %s %v", resp.Status, resp.Header.Get("Content-Type"), err)
	}
}
