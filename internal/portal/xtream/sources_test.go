// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// A login whose account uses only some sources lists and plays only what
// comes from them (D114).
func TestAccountSources(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	sources, err := h.store.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, s := range sources {
		ids[s.Name] = s.ID
	}
	var all []liveStreamJSON
	h.api(&all, "action", "get_live_streams")
	var films []filmJSON
	h.api(&films, "action", "get_vod_streams")
	if len(all) == 0 || len(films) == 0 {
		t.Fatalf("channels %d, films %d", len(all), len(films))
	}
	alpha := fmt.Sprint(h.ids["Alpha"])

	// Films alone: no channel, and the films.
	if err := h.store.SetAccountSources(ctx, store.HouseholdAccount, store.Sources{Only: true, IDs: []int64{ids["films"]}}); err != nil {
		t.Fatal(err)
	}
	var live []liveStreamJSON
	h.api(&live, "action", "get_live_streams")
	var cats []category
	h.api(&cats, "action", "get_live_categories")
	if len(live) != 0 || len(cats) != 0 {
		t.Errorf("live %+v, categories %+v", live, cats)
	}
	if resp, _ := h.get(path("live", alpha+".ts")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("Alpha: %s", resp.Status)
	}
	if resp, body := h.get("/xmltv.php?username=" + username + "&password=" + password); resp.StatusCode != http.StatusOK ||
		strings.Contains(body, "<channel") {
		t.Errorf("guide: %s %q", resp.Status, body)
	}
	var someFilms []filmJSON
	h.api(&someFilms, "action", "get_vod_streams")
	if len(someFilms) != len(films) {
		t.Errorf("films %+v", someFilms)
	}

	// Channels alone: the channels, and no film.
	if err := h.store.SetAccountSources(ctx, store.HouseholdAccount, store.Sources{Only: true, IDs: []int64{ids["main"]}}); err != nil {
		t.Fatal(err)
	}
	h.api(&live, "action", "get_live_streams")
	if len(live) != len(all) {
		t.Errorf("live %+v", live)
	}
	var none []filmJSON
	h.api(&none, "action", "get_vod_streams")
	h.api(&cats, "action", "get_vod_categories")
	if len(none) != 0 || len(cats) != 0 {
		t.Errorf("films %+v, categories %+v", none, cats)
	}
	film := fmt.Sprint(films[0].StreamID)
	if resp, _ := h.get(path("movie", film+"."+films[0].Container)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a film: %s", resp.Status)
	}
	// As for a film that does not exist.
	var info map[string]any
	h.api(&info, "action", "get_vod_info", "vod_id", film)
	if data, ok := info["movie_data"].([]any); !ok || len(data) != 0 {
		t.Errorf("a film's details: %v", info)
	}
}
