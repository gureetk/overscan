// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// TestRadio marks radio stations on the Live TV page and in the API, and
// sets from the Settings page and the API whether boxes show them on their
// Radio screen alone (D87).
func TestRadio(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()

	page := ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "no station", page, "None of your sources lists radio stations.", "Radio apart from TV",
		"Off: boxes show radio stations on their Radio screen, and among the channels of Live TV and the TV Guide too.")

	// Bravo becomes a radio station.
	sources, err := ta.st.Sources(ctx)
	if err != nil || len(sources) != 1 {
		t.Fatalf("sources %+v, %v", sources, err)
	}
	var chs []store.CatalogChannel
	for _, spec := range []string{"Alpha/News", "Bravo/Sports", "Charlie/News", "Delta/Sports"} {
		name, category, _ := strings.Cut(spec, "/")
		chs = append(chs, store.CatalogChannel{IdentityKey: name, Name: name, Category: category,
			StreamURL: "http://streams.example/" + name, Radio: name == "Bravo"})
	}
	if _, err := ta.st.ApplyCatalog(ctx, sources[0].ID, chs, ta.clock); err != nil {
		t.Fatal(err)
	}
	entries, _ := ta.channels(c, "")
	for name, e := range entries {
		if e.Radio != (name == "Bravo") {
			t.Errorf("%s: radio %v", name, e.Radio)
		}
	}
	mustContain(t, "the Live TV page", ta.do("GET", "/admin/lineup", nil, c).Body.String(), "Radio</span>")

	rec := ta.do("POST", "/admin/settings/radio", lineupForm(csrf, "", "apart", "1"), c, hx...)
	mustContain(t, "apart", rec.Body.String(), "Boxes show radio stations on their Radio screen alone.",
		"Your sources list 1 radio station.", `id="radio-apart" name="apart" value="1" checked`)
	if on, _ := ta.st.RadioApart(ctx); !on {
		t.Error("the setting is off")
	}
	var settings apiSettings
	rec = ta.apiDo("PATCH", "settings", `{"radio_apart": false}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil || settings.RadioApart {
		t.Errorf("radio_apart off: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"radio_apart": "yes"}`, c, h...), http.StatusBadRequest, "bad_json")
}
