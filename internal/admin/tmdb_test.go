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

// TestTMDB saves, checks and removes TMDB's key from the Settings page and
// the API, which never gives the key back (D88).
func TestTMDB(t *testing.T) {
	ta := newTestAdmin(t)
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
	rec = ta.do("POST", "/admin/settings/tmdb", lineupForm(csrf, "", "remove", "1"), c, hx...)
	mustContain(t, "removed", rec.Body.String(), "TMDB&#39;s key is removed.", `<span class="badge">Off</span>`)
}
