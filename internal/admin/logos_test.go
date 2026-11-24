// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/store"
)

// TestLogoByHand checks a channel's logo set by hand, from its panel and
// through the API, and what its panel says of where its logo comes from
// (D141).
func TestLogoByHand(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	ctx := context.Background()
	chs, _ := ta.channels(c, "")
	alpha := chs["Alpha"].ID
	path := fmt.Sprintf("/admin/lineup/channels/%d", alpha)
	hx := []string{"HX-Request", "true"}

	body := ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", alpha), nil, c).Body.String()
	mustContain(t, "the panel", body, "<legend>Logo</legend>", `name="logo_choice" value="auto" checked`,
		"None: its source gives none, and no guide has one. Devices show its name.")

	logo := "https://logos.example/alpha.png"
	panel := fmt.Sprintf("/admin/lineup?edit=%d", alpha)
	rec := ta.do("POST", path, lineupForm(csrf, "", "visibility", "group", "logo_choice", "address", "logo", " "+logo+" "), c, hx...)
	mustContain(t, "an address", rec.Body.String(), "Saved Alpha.")
	mustContain(t, "its panel", ta.do("GET", panel, nil, c).Body.String(), "Set by hand, from its address.",
		`value="address" checked`, `value="`+logo+`"`, `<img src="`+artwork.LogoPath(alpha, logo)+`"`)
	e, err := ta.st.LineupEntry(ctx, store.DefaultLineupID, alpha)
	if err != nil || e.Logo != logo || e.LogoByHand == nil || *e.LogoByHand != logo {
		t.Errorf("Alpha by hand %+v, %v", e, err)
	}
	rec = ta.do("POST", path, lineupForm(csrf, "", "visibility", "group", "logo_choice", "address", "logo", logo), c, hx...)
	mustContain(t, "the same again", rec.Body.String(), "Nothing to change.")
	ta.do("POST", path, lineupForm(csrf, "", "visibility", "group", "logo_choice", "none", "logo", logo), c, hx...)
	mustContain(t, "none", ta.do("GET", panel, nil, c).Body.String(), "None, set by hand: devices show its name.")
	if a, _ := ta.channels(c, ""); a["Alpha"].Logo != nil || a["Alpha"].LogoByHand == nil || *a["Alpha"].LogoByHand != "" {
		t.Errorf("Alpha with none %+v", a["Alpha"])
	}
	ta.do("POST", path, lineupForm(csrf, "", "visibility", "group", "logo_choice", "auto"), c, hx...)
	if e, _ := ta.st.LineupEntry(ctx, store.DefaultLineupID, alpha); e.LogoByHand != nil {
		t.Errorf("Alpha automatic again %+v", e)
	}
	for _, tc := range []struct {
		form url.Values
		text string
	}{
		{lineupForm(csrf, "", "logo_choice", "address", "logo", ""), "Enter the logo&#39;s address"},
		{lineupForm(csrf, "", "logo_choice", "address", "logo", "logo.png"), "A logo&#39;s address starts with http:// or https://."},
		{lineupForm(csrf, "", "logo_choice", "maybe"), "Choose its logo."},
	} {
		rec := ta.do("POST", path, tc.form, c, hx...)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.text) {
			t.Errorf("%v: status %d", tc.form, rec.Code)
		}
	}

	// The API: an address, none, and back to automatic.
	api := fmt.Sprintf("lineup/channels/%d", alpha)
	var got apiEntry
	rec = ta.apiDo("PATCH", api, `{"logo": "`+logo+`"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Logo == nil ||
		*got.Logo != artwork.LogoPath(alpha, logo) || got.LogoByHand == nil || *got.LogoByHand != logo {
		t.Errorf("PATCH an address: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", api, `{"logo": ""}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Logo != nil || got.LogoByHand == nil || *got.LogoByHand != "" {
		t.Errorf("PATCH none: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", api, `{"logo": null}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.LogoByHand != nil {
		t.Errorf("PATCH automatic: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", api, `{"logo": "ftp://logos.example/a.png"}`, c, h...), http.StatusBadRequest, "bad_logo")
	apiErrorOf(t, ta.apiDo("PATCH", api, `{"logo": 4}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/channels", fmt.Sprintf(`{"ids": [%d], "logo": ""}`, alpha), c, h...),
		http.StatusBadRequest, "bad_json")
}

// TestGuideLogosSettings checks how the admin sets the use of a guide's
// logos, and of a source's guide's (D141).
func TestGuideLogosSettings(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withGuides()
	h := []string{csrfHeader, csrf}
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}

	// A guide of the household's own: its form and card, and the API.
	rec := ta.do("POST", "/admin/guides", url.Values{"csrf": {csrf}, "name": {"extra"}, "address": {secretGuide}, "logos": {"first"}}, c, hx...)
	mustContain(t, "added", rec.Body.String(), "<dt>Its logos</dt><dd>Before a channel&#39;s own</dd>")
	body := ta.do("GET", "/admin/guides/1", nil, c).Body.String()
	mustContain(t, "its page", body, `<option value="first" selected>Before a channel's own</option>`)
	var g apiOwnGuide
	rec = ta.apiDo("PATCH", "guides/1", `{"logos": "none"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || g.Logos != "none" {
		t.Errorf("PATCH logos: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "guides/1", `{"logos": "some"}`, c, h...), http.StatusBadRequest, "bad_logos")
	apiErrorOf(t, ta.apiDo("PATCH", "guides/1", `{"logos": ""}`, c, h...), http.StatusBadRequest, "bad_logos")
	rec = ta.apiDo("POST", "guides", `{"name": "second", "address": "/srv/second.xml"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || g.Logos != "fill" {
		t.Errorf("a new guide's logos: status %d, %s", rec.Code, rec.Body)
	}

	// A source's guide's: not used at first; its card and the API. Each
	// change tells the guides, whose matches the logos' checks follow.
	changes := f.changed()
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the sources", body, "<dt>Its guide's logos</dt>", `<option value="none" selected>Not used</option>`)
	rec = ta.do("POST", "/admin/sources/1/guide-logos", url.Values{"csrf": {csrf}, "guide_logos": {"fill"}}, c, hx...)
	mustContain(t, "set", rec.Body.String(), "The logos of main&#39;s guide: where a channel&#39;s own is missing or does not load.",
		`<option value="fill" selected>`)
	if src, err := ta.st.Source(ctx, 1); err != nil || src.GuideLogos != store.GuideLogosFill {
		t.Errorf("the source %+v, %v", src, err)
	}
	if f.changed() != changes+1 {
		t.Errorf("the guides were told of %d changes, want 1", f.changed()-changes)
	}
	if rec := ta.do("POST", "/admin/sources/1/guide-logos", url.Values{"csrf": {csrf}, "guide_logos": {"some"}}, c, hx...); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad choice: status %d", rec.Code)
	}
	var src apiSource
	rec = ta.apiDo("PATCH", "sources/1", `{"guide_logos": "first"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &src); err != nil || src.GuideLogos != "first" {
		t.Errorf("PATCH guide_logos: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "sources/1", `{"guide_logos": "always"}`, c, h...), http.StatusBadRequest, "bad_guide_logos")

	// The support bundle says so.
	files := ta.bundleFiles(c)
	mustContain(t, "sources.txt", files["sources.txt"], "Its guide's logos", "Before a channel's own")
	mustContain(t, "guides.txt", files["guides.txt"], "Its logos", "Not used", "Logos checked (D141): 0, of which 0 do not load",
		"Placeholder programmes (D142): off")
}

// bundleFiles returns the support bundle's files by name.
func (ta *testAdmin) bundleFiles(c *http.Cookie) map[string]string {
	ta.t.Helper()
	rec := ta.do("GET", "/admin/support-bundle", nil, c)
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		ta.t.Fatal(err)
	}
	files := map[string]string{}
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			ta.t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			ta.t.Fatal(err)
		}
		files[f.Name] = string(data)
	}
	return files
}

func TestLogoText(t *testing.T) {
	none, addr := "", "http://logos.example/a.png"
	for _, c := range []struct {
		info store.EntryLogoInfo
		want string
	}{
		{store.EntryLogoInfo{From: store.LogoFromHand, ByHand: &addr}, "Set by hand, from its address."},
		{store.EntryLogoInfo{From: store.LogoFromNone, ByHand: &none}, "None, set by hand: devices show its name."},
		{store.EntryLogoInfo{From: store.LogoFromRule}, "From a rule."},
		{store.EntryLogoInfo{From: store.LogoFromSource}, "From its source."},
		{store.EntryLogoInfo{From: store.LogoFromGuide, Guide: "extra", OwnFails: true}, "From the guide extra, since its source's does not load."},
		{store.EntryLogoInfo{From: store.LogoFromGuide, Guide: "extra"}, "From the guide extra."},
		{store.EntryLogoInfo{From: store.LogoFromSourceGuide}, "From its source's guide."},
		{store.EntryLogoInfo{OwnFails: true}, "None: its source's does not load, and no guide has one. Devices show its name."},
		{store.EntryLogoInfo{}, "None: its source gives none, and no guide has one. Devices show its name."},
	} {
		info := c.info
		if got := (entryRow{Logo: &info}).LogoText(); got != c.want {
			t.Errorf("%+v: %q, want %q", c.info, got, c.want)
		}
	}
	if got := (entryRow{}).LogoText(); got != "" {
		t.Errorf("without information: %q", got)
	}
}
