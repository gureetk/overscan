// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// debugServer returns a server whose lineup has two fictional channels.
func debugServer(t *testing.T) (*Server, []store.LineupChannel) {
	t.Helper()
	s, st := newServerWithStore(t, config.Default(), nil)
	ctx := context.Background()
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1, Numbers: source.NumbersProvider})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, srcID, []store.CatalogChannel{
		{IdentityKey: "a", Name: "Example News, Weather", Number: 5, Category: `Group "A"`, XMLTVID: "news.example", StreamURL: "http://streams.example/a?pass=secret"},
		{IdentityKey: "b", Name: "Example Sports", StreamURL: "http://streams.example/b?pass=secret"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	channels, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	return s, channels
}

func TestDebugLineup(t *testing.T) {
	s, channels := debugServer(t)
	for name, path := range map[string]string{
		"no token":    "/debug/lineup.m3u",
		"wrong token": "/debug/lineup.m3u?token=" + strings.Repeat("0", 32),
	} {
		if rec := get(s, path); rec.Code != http.StatusForbidden {
			t.Errorf("%s: status %d", name, rec.Code)
		}
	}

	rec := get(s, "/debug/lineup.m3u?token="+testAdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	body := rec.Body.String()
	want := "#EXTM3U\n" +
		"#EXTINF:-1 tvg-id=\"news.example\" tvg-chno=\"5\" group-title=\"Group 'A'\",Example News, Weather\n" +
		"http://example.com/debug/play/" + itoa(channels[0].EntryID) + "?token=" + testAdminToken + "\n"
	if !strings.HasPrefix(body, want) {
		t.Errorf("playlist:\n%s\nwant it to start with:\n%s", body, want)
	}
	if strings.Contains(body, "secret") {
		t.Errorf("the playlist gives away stream addresses:\n%s", body)
	}
}

func TestDebugPlay(t *testing.T) {
	s, channels := debugServer(t)
	path := "/debug/play/" + itoa(channels[0].EntryID)

	if rec := get(s, path+"?token=wrong"); rec.Code != http.StatusForbidden {
		t.Errorf("wrong token: status %d", rec.Code)
	}
	if rec := get(s, "/debug/play/999999?token="+testAdminToken); rec.Code != http.StatusNotFound {
		t.Errorf("unknown entry: status %d", rec.Code)
	}
	rec := get(s, path+"?token="+testAdminToken)
	if loc := rec.Header().Get("Location"); rec.Code != http.StatusFound || !strings.HasPrefix(loc, "/live/") {
		t.Errorf("status %d, location %q", rec.Code, loc)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
