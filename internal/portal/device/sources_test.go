// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// A box whose account uses only some sources lists and plays only what
// comes from them: channels, their guide, favourites and searches, and films
// and series (D114).
func TestAccountSources(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_, srv := newLibraryPanel(t)
	token := h.withLibrary(srv)
	two, err := h.store.AddSource(ctx, source.Settings{Name: "two", URL: "http://provider.example/two.m3u", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.ApplyCatalog(ctx, two, []store.CatalogChannel{
		{IdentityKey: "t1", Name: "Two One", Category: "News", StreamURL: "http://streams.example/t1.ts"}}, h.now); err != nil {
		t.Fatal(err)
	}
	var all channelsResponse
	if code := h.call("GET", Prefix+"channels", token, "", &all); code != http.StatusOK || len(all.Channels) != 6 {
		t.Fatalf("channels: %d, %+v", code, all.Channels)
	}
	var mainChannel, twoChannel int64
	for _, c := range all.Channels {
		switch c.Name {
		case "Channel 0":
			mainChannel = c.ID
		case "Two One":
			twoChannel = c.ID
		}
	}
	var movies libraryResponse
	if code := h.call("GET", Prefix+"movies", token, "", &movies); code != http.StatusOK || len(movies.Shelves) == 0 {
		t.Fatalf("movies: %d, %+v", code, movies)
	}
	var recent shelfResponse
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	if len(recent.Titles) == 0 {
		t.Fatalf("recent %+v", recent)
	}
	film := recent.Titles[0].ID

	// The household uses two alone.
	if err := h.store.SetAccountSources(ctx, store.HouseholdAccount, store.Sources{Only: true, IDs: []int64{two}}); err != nil {
		t.Fatal(err)
	}
	var list channelsResponse
	if code := h.call("GET", Prefix+"channels", token, "", &list); code != http.StatusOK || len(list.Channels) != 1 ||
		list.Channels[0].ID != twoChannel {
		t.Errorf("channels: %d, %+v", code, list.Channels)
	}
	for _, path := range []string{
		fmt.Sprintf("channels/%d/play", mainChannel),
	} {
		if code, _ := h.errorCode("POST", Prefix+path, token); code != http.StatusNotFound {
			t.Errorf("POST %s: %d, want 404", path, code)
		}
	}
	for _, path := range []string{
		fmt.Sprintf("channels/%d/favourite", mainChannel),
	} {
		if code, _ := h.errorCode("PUT", Prefix+path, token); code != http.StatusNotFound {
			t.Errorf("PUT %s: %d, want 404", path, code)
		}
	}
	if code, _ := h.errorCode("GET", fmt.Sprintf("%schannels/%d/guide", Prefix, mainChannel), token); code != http.StatusNotFound {
		t.Errorf("a channel of main's guide: %d, want 404", code)
	}
	var found searchResponse
	h.call("GET", Prefix+"search?q=channel", token, "", &found)
	if len(found.Channels) != 0 || len(found.Movies) != 0 {
		t.Errorf("search %+v", found)
	}
	h.call("GET", Prefix+"search?q=two", token, "", &found)
	if len(found.Channels) != 1 || found.Channels[0] != twoChannel {
		t.Errorf("search %+v", found)
	}
	if code := h.call("GET", Prefix+"movies", token, "", &movies); code != http.StatusOK || len(movies.Shelves) != 0 {
		t.Errorf("movies: %d, %+v", code, movies)
	}
	for _, path := range []string{
		fmt.Sprintf("movies/%d", film),
		"movies/shelves/recent",
	} {
		var shelf shelfResponse
		code := h.call("GET", Prefix+path, token, "", &shelf)
		if code != http.StatusNotFound && !(code == http.StatusOK && len(shelf.Titles) == 0) {
			t.Errorf("GET %s: %d, %+v", path, code, shelf)
		}
	}
	if code, _ := h.errorCode("POST", fmt.Sprintf("%smovies/%d/play", Prefix, film), token); code != http.StatusNotFound {
		t.Errorf("playing a film: %d, want 404", code)
	}

	// Every source again: all of it is back.
	if err := h.store.SetAccountSources(ctx, store.HouseholdAccount, store.EverySource); err != nil {
		t.Fatal(err)
	}
	if code := h.call("GET", Prefix+"channels", token, "", &list); code != http.StatusOK || len(list.Channels) != 6 {
		t.Errorf("channels: %d, %+v", code, list.Channels)
	}
	if code, _ := h.errorCode("PUT", fmt.Sprintf("%schannels/%d/favourite", Prefix, mainChannel), token); code != http.StatusOK {
		t.Errorf("a favourite of main: %d", code)
	}
}
