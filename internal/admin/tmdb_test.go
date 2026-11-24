// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeFolders counts the reads of the folders, and the runs for
// providers' pictures, that the admin asks for.
type fakeFolders struct{ reads, pictures int }

func (f *fakeFolders) ReadFoldersAgain(context.Context) { f.reads++ }

func (f *fakeFolders) FillPictures(context.Context) { f.pictures++ }

// TestTMDB saves, checks and removes TMDB's key from the Settings page and
// the API, which never gives the key back, and reads the folders again when
// the key or the language changes (D88).
func TestTMDB(t *testing.T) {
	ta := newTestAdmin(t)
	folders := &fakeFolders{}
	ta.UseFolders(folders)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/3/configuration" || r.URL.Query().Get("api_key") != "made-up-key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"images":{}}`))
	}))
	defer srv.Close()
	ta.tmdbBase = srv.URL + "/3"

	page := ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "off", page, "Details from TMDB", "Off: films and series in folders show what",
		"This product uses the TMDB API but is not endorsed or certified by TMDB.")

	rec := ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "key", "a-wrong-key", "language", "en-GB"), c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "TMDB refused the key.") {
		t.Errorf("a refused key: %d", rec.Code)
	}
	if key, _ := ta.st.TMDBKey(ctx); key != "" {
		t.Errorf("kept a refused key")
	}
	rec = ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "key", "made-up-key", "language", "en-GB"), c, hx...)
	mustContain(t, "saved", rec.Body.String(), "TMDB&#39;s key is saved.", `<span class="badge ok">On</span>`,
		"None has been looked up yet", `value="en-GB"`, "Remove the key")
	if key, _ := ta.st.TMDBKey(ctx); key != "made-up-key" {
		t.Errorf("key %q", key)
	}
	if strings.Contains(rec.Body.String(), "made-up-key") {
		t.Error("the page shows the key")
	}
	// A new language and a key: the folders are read again for each.
	if folders.reads != 2 {
		t.Errorf("%d reads of the folders", folders.reads)
	}
	// The same language again reads nothing.
	ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "language", "en-GB"), c, hx...)
	if folders.reads != 2 {
		t.Errorf("%d reads of the folders after the same language", folders.reads)
	}
	if rec = ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "language", "English"), c, hx...); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad language: %d", rec.Code)
	}

	// The API says whether there is a key, never what it is.
	rec = ta.apiDo("GET", "settings", "", c)
	if strings.Contains(rec.Body.String(), "made-up-key") {
		t.Error("the API gives the key")
	}
	var got apiSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.TMDBKeySet || got.TMDBLanguage != "en-GB" {
		t.Errorf("settings %s", rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"tmdb_key": "another-key"}`, c, h...), http.StatusBadRequest, "bad_tmdb_key")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"tmdb_language": "eng"}`, c, h...), http.StatusBadRequest, "bad_tmdb_language")
	rec = ta.apiDo("PATCH", "settings", `{"tmdb_key": ""}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.TMDBKeySet {
		t.Errorf("removed: %s", rec.Body)
	}

	// TMDB out of reach: the key is kept, with a warning.
	srv.Close()
	rec = ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "key", "made-up-key"), c, hx...)
	mustContain(t, "unreachable", rec.Body.String(), "TMDB could not be asked whether it takes the key")
	if key, _ := ta.st.TMDBKey(ctx); key != "made-up-key" {
		t.Errorf("key %q", key)
	}
	reads := folders.reads
	rec = ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "remove", "1"), c, hx...)
	mustContain(t, "removed", rec.Body.String(), "TMDB&#39;s key is removed.", `<span class="badge">Off</span>`)
	if folders.reads != reads+1 {
		t.Errorf("%d reads of the folders after removing the key, from %d", folders.reads, reads)
	}
}

// TestTMDBPictures turns TMDB's pictures for providers' films and series
// on and off from the Settings page and the API (D143).
func TestTMDBPictures(t *testing.T) {
	ta := newTestAdmin(t)
	folders := &fakeFolders{}
	ta.UseFolders(folders)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()

	page := ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "off", page, "Pictures for providers' films and series",
		"Off: films and series of Xtream servers and Stalker portals show their sources' pictures.")
	rec := ta.do("POST", "/admin/settings/tmdb-pictures", lineupForm(csrf, "", "pictures", "1"), c, hx...)
	mustContain(t, "on", rec.Body.String(), "whose pictures want replacing take TMDB&#39;s",
		"On, once TMDB's key is saved")
	if on, err := ta.st.TMDBPictures(ctx); !on || err != nil || folders.pictures != 1 {
		t.Errorf("on: %v, %v, %d runs", on, err, folders.pictures)
	}
	if err := ta.st.SetTMDBKey(ctx, "made-up-key"); err != nil {
		t.Fatal(err)
	}
	page = ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "with a key", page, "looked up in the background. 0 titles show TMDB's now.")

	var got apiSettings
	rec = ta.apiDo("PATCH", "settings", `{"tmdb_pictures": false}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.TMDBPictures {
		t.Errorf("off: %s", rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"tmdb_pictures": "yes"}`, c, h...), http.StatusBadRequest, "bad_json")
	rec = ta.apiDo("PATCH", "settings", `{"tmdb_pictures": true}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.TMDBPictures || folders.pictures != 2 {
		t.Errorf("on again: %s, %d runs", rec.Body, folders.pictures)
	}
	mustContain(t, "lineup.txt", ta.bundleFiles(c)["lineup.txt"],
		"TMDB's pictures for providers' titles (D143): true; titles showing them: 0; sources' pictures that do not load: 0")
}
