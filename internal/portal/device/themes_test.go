// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/theme"
)

// installTheme checks a made-up theme file and keeps it, as the admin
// does.
func installTheme(t *testing.T, st *store.Store, file string) store.Theme {
	t.Helper()
	parsed, err := theme.Parse([]byte(file))
	if err != nil {
		t.Fatal(err)
	}
	th, _, err := st.InstallTheme(context.Background(), parsed.Name, parsed.JSON(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// A box loads the stylesheet of its profile's theme, else its household's,
// and none for Overscan's own (D97).
func TestProfilesNameTheirTheme(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	token := h.session(macA).Token

	var got profilesResponse
	if code := h.call("GET", Prefix+"profiles", token, "", &got); code != http.StatusOK || got.Theme != "" {
		t.Fatalf("with Overscan's own theme: %d %+v", code, got)
	}
	first := got.Profile
	evergreen := installTheme(t, h.store, `{"overscan_theme": 1, "name": "Evergreen", "background": "#0e1512", "text": "#eef4f0"}`)
	paper := installTheme(t, h.store, `{"overscan_theme": 1, "name": "Paper", "background": "#f4f1ea", "text": "#1d1b17"}`)

	if err := h.store.SetAccountTheme(ctx, 1, evergreen.ID); err != nil {
		t.Fatal(err)
	}
	h.call("GET", Prefix+"profiles", token, "", &got)
	if got.Theme != theme.Path(evergreen.ID, evergreen.Data) {
		t.Errorf("the household's theme: %q", got.Theme)
	}

	// A profile's own theme comes before the household's.
	if err := h.store.SetProfileTheme(ctx, first, paper.ID); err != nil {
		t.Fatal(err)
	}
	h.call("GET", Prefix+"profiles", token, "", &got)
	if got.Theme != theme.Path(paper.ID, paper.Data) {
		t.Errorf("the profile's theme: %q", got.Theme)
	}

	// Switching to a profile without one of its own gives the household's.
	alex, err := h.store.AddProfile(ctx, 1, "Alex")
	if err != nil {
		t.Fatal(err)
	}
	if code := h.call("PUT", Prefix+"profile", token, fmt.Sprintf(`{"id": %d}`, alex.ID), &got); code != http.StatusOK ||
		got.Theme != theme.Path(evergreen.ID, evergreen.Data) {
		t.Errorf("after switching: %d %q", code, got.Theme)
	}

	// A theme changed in the admin has a new stylesheet.
	changed := installTheme(t, h.store, `{"overscan_theme": 1, "name": "Evergreen", "background": "#0e1512", "text": "#ffffff"}`)
	h.call("GET", Prefix+"profiles", token, "", &got)
	if changed.ID != evergreen.ID || got.Theme != theme.Path(changed.ID, changed.Data) || got.Theme == theme.Path(evergreen.ID, evergreen.Data) {
		t.Errorf("after changing the theme: %q", got.Theme)
	}

	// Removed, Overscan's own.
	if err := h.store.RemoveTheme(ctx, evergreen.ID); err != nil {
		t.Fatal(err)
	}
	got = profilesResponse{} // the answer leaves out theme
	h.call("GET", Prefix+"profiles", token, "", &got)
	if got.Theme != "" {
		t.Errorf("after removing it: %q", got.Theme)
	}
}
