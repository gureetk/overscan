// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/lineupfile"
	"overscan.gureet.ca/internal/rules"
)

// TestLogos checks the order of an entry's logo (D141): by hand, a rule's,
// its own if it loads, its guide's if it loads, a guide set to come first
// before its own, and none; and that every reader of logos agrees.
func TestLogos(t *testing.T) {
	s, src, _ := lineupSetup(t)
	ctx := context.Background()
	at := time.Date(2026, 12, 4, 6, 0, 0, 0, time.UTC)
	chs := grouped("Alpha/News", "Bravo/News", "Charlie/News", "Delta/News", "Echo/News")
	for i := range chs {
		if chs[i].Name != "Charlie" {
			chs[i].Logo = "http://logos.example/" + chs[i].Name + ".png"
		}
	}
	apply(t, s, src, at, chs)
	alpha, charlie, delta, echo := entryNamed(t, s, "Alpha"), entryNamed(t, s, "Charlie"),
		entryNamed(t, s, "Delta"), entryNamed(t, s, "Echo")
	// The source's guide gives Alpha and Charlie icons.
	w, err := s.BeginGuide(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	w.SetIcon(alpha.ChannelID, "http://icons.example/alpha.png")
	w.SetIcon(charlie.ChannelID, "http://icons.example/charlie.png")
	if err := w.Commit(ctx, at, "own"); err != nil {
		t.Fatal(err)
	}
	// A guide of the household's own fills Delta, with an icon.
	g, err := s.AddGuide(ctx, GuideSettings{Name: "extra", Address: "http://guide.example/", UserAgent: "Guide/1", PublicOnly: true}, at)
	if err != nil {
		t.Fatal(err)
	}
	readGuide(t, s, g, at, 0, []GuideListing{{XMLTVID: "delta.example", Names: []string{"Delta"}, Icon: "http://extra.example/delta.png"}},
		nil, func(ids map[string]int64) []GuideMatch {
			return []GuideMatch{{ChannelID: delta.ChannelID, GuideChannelID: ids["delta.example"], ByName: true}}
		})

	logos := func() map[string]string {
		t.Helper()
		cs, err := s.LineupChannels(ctx, DefaultLineupID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, c := range cs {
			out[c.Name] = c.Logo
			e, err := s.LineupEntry(ctx, DefaultLineupID, c.EntryID)
			if err != nil || e.Logo != c.Logo {
				t.Errorf("%s: the entry's logo %q, the lineup's %q, %v", c.Name, e.Logo, c.Logo, err)
			}
			info, err := s.EntryLogoInfo(ctx, DefaultLineupID, c.EntryID)
			if err != nil || info.Address != c.Logo {
				t.Errorf("%s: the logo's information %+v, the lineup's %q, %v", c.Name, info, c.Logo, err)
			}
			if logo, _, err := s.EntryLogo(ctx, c.EntryID); err != nil || logo != c.Logo {
				t.Errorf("%s: the served logo %q, the lineup's %q, %v", c.Name, logo, c.Logo, err)
			}
		}
		return out
	}
	from := func(e LineupChannel) string {
		t.Helper()
		info, err := s.EntryLogoInfo(ctx, DefaultLineupID, e.EntryID)
		if err != nil {
			t.Fatal(err)
		}
		return info.From
	}
	check := func(when string, want map[string]string) {
		t.Helper()
		if got := logos(); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", when, got, want)
		}
	}

	// As before: the source's logos, the guide's where it has none... but
	// Delta has its own.
	base := map[string]string{"Alpha": "http://logos.example/Alpha.png", "Bravo": "http://logos.example/Bravo.png", "Charlie": "",
		"Delta": "http://logos.example/Delta.png", "Echo": "http://logos.example/Echo.png"}
	check("at first", base)
	if from(alpha) != LogoFromSource || from(charlie) != LogoFromNothing {
		t.Errorf("from %q, %q", from(alpha), from(charlie))
	}

	// Logos that do not load give way to the guide's, or to none.
	for _, addr := range []string{"http://logos.example/Delta.png", "http://logos.example/Bravo.png"} {
		if err := s.SetLogoCheck(ctx, addr, false, at); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.SetLogoCheck(ctx, "http://logos.example/Alpha.png", true, at); err != nil {
		t.Fatal(err)
	}
	check("with two that do not load", map[string]string{"Alpha": "http://logos.example/Alpha.png", "Bravo": "", "Charlie": "",
		"Delta": "http://extra.example/delta.png", "Echo": "http://logos.example/Echo.png"})
	if info, _ := s.EntryLogoInfo(ctx, DefaultLineupID, delta.EntryID); info.From != LogoFromGuide || !info.OwnFails || info.Guide != "extra" {
		t.Errorf("Delta's logo %+v", info)
	}
	// A guide's icon is fetched as its guide is.
	if _, src, err := s.EntryLogo(ctx, delta.EntryID); err != nil || src.UserAgent != "Guide/1" || !src.PublicOnly {
		t.Errorf("Delta's logo's fetch %+v, %v", src, err)
	}
	if _, src, err := s.EntryLogo(ctx, echo.EntryID); err != nil || src.UserAgent != "" || src.PublicOnly {
		t.Errorf("Echo's logo's fetch %+v, %v", src, err)
	}

	// The source's guide's logos, where a channel's own is missing, then
	// first; the guide of its own, never, then first.
	if err := s.SetSourceGuideLogos(ctx, src, GuideLogosFill); err != nil {
		t.Fatal(err)
	}
	check("with the source's guide's logos", map[string]string{"Alpha": "http://logos.example/Alpha.png", "Bravo": "",
		"Charlie": "http://icons.example/charlie.png", "Delta": "http://extra.example/delta.png", "Echo": "http://logos.example/Echo.png"})
	if from(charlie) != LogoFromSourceGuide {
		t.Errorf("Charlie's logo from %q", from(charlie))
	}
	if err := s.SetSourceGuideLogos(ctx, src, GuideLogosFirst); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGuideLogos(ctx, g, GuideLogosNone); err != nil {
		t.Fatal(err)
	}
	check("the source's guide's first, the other's never", map[string]string{"Alpha": "http://icons.example/alpha.png", "Bravo": "",
		"Charlie": "http://icons.example/charlie.png", "Delta": "", "Echo": "http://logos.example/Echo.png"})
	if err := s.SetSourceGuideLogos(ctx, src, GuideLogosNone); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGuideLogos(ctx, g, GuideLogosFirst); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLogoCheck(ctx, "http://logos.example/Delta.png", true, at); err != nil {
		t.Fatal(err)
	}
	check("the guide of its own first", map[string]string{"Alpha": "http://logos.example/Alpha.png", "Bravo": "", "Charlie": "",
		"Delta": "http://extra.example/delta.png", "Echo": "http://logos.example/Echo.png"})
	if err := s.SetLogoCheck(ctx, "http://extra.example/delta.png", false, at); err != nil {
		t.Fatal(err)
	}
	check("its icon not loading", map[string]string{"Alpha": "http://logos.example/Alpha.png", "Bravo": "", "Charlie": "",
		"Delta": "http://logos.example/Delta.png", "Echo": "http://logos.example/Echo.png"})
	for _, bad := range []string{"", "some"} {
		if err := s.SetGuideLogos(ctx, g, bad); err == nil {
			t.Errorf("%q taken", bad)
		}
		if err := s.SetSourceGuideLogos(ctx, src, bad); err == nil {
			t.Errorf("%q taken for the source", bad)
		}
	}

	// A rule's logo, and a logo by hand, which wins over it; none by hand.
	addRule(t, s, rules.Rule{Action: rules.Logo, Match: rules.Match{Name: "Echo"}, Logo: "http://rules.example/echo.png"})
	hand, none := "http://hand.example/echo.png", ""
	if err := s.SetEntryLogo(ctx, DefaultLineupID, alpha.EntryID, &none); err != nil {
		t.Fatal(err)
	}
	if got := logos(); got["Echo"] != "http://rules.example/echo.png" || got["Alpha"] != "" || from(echo) != LogoFromRule || from(alpha) != LogoFromNone {
		t.Errorf("a rule's and none by hand: %v", got)
	}
	if err := s.SetEntryLogo(ctx, DefaultLineupID, echo.EntryID, &hand); err != nil {
		t.Fatal(err)
	}
	e, _ := s.LineupEntry(ctx, DefaultLineupID, echo.EntryID)
	if e.Logo != hand || e.LogoByHand == nil || *e.LogoByHand != hand || e.RuleLogo || from(echo) != LogoFromHand {
		t.Errorf("Echo by hand %+v", e)
	}
	if err := s.SetEntryLogo(ctx, DefaultLineupID, 999, &hand); err == nil {
		t.Error("an unknown entry took a logo")
	}

	// A copy of the lineup, and its file, keep the logos set by hand.
	copied, err := s.CopyLineup(ctx, DefaultLineupID, "Copy")
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.LineupChannels(ctx, copied)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Name == "Echo" && c.Logo != hand || c.Name == "Alpha" && c.Logo != "" {
			t.Errorf("the copy's %s: %q", c.Name, c.Logo)
		}
	}
	f, err := s.ExportLineups(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if f.Needs() != 3 {
		t.Errorf("the file needs version %d", f.Needs())
	}
	byKey := map[string]string{}
	for _, c := range f.Lineups[0].Channels {
		byKey[c.Key] = c.Logo
	}
	if byKey["Echo"] != hand || byKey["Alpha"] != lineupfile.NoLogo {
		t.Errorf("the file's logos %v", byKey)
	}

	// The checks to do: the own and the guide's of the channels with a
	// guide's logo; none for those without.
	if err := s.SetSourceGuideLogos(ctx, src, GuideLogosFill); err != nil {
		t.Fatal(err)
	}
	due, err := s.LogosToCheck(ctx, at.Add(time.Hour), 24*time.Hour, 30*24*time.Hour, 50)
	if err != nil {
		t.Fatal(err)
	}
	var addrs []string
	for _, d := range due {
		addrs = append(addrs, d.Address)
		if d.Address == "http://extra.example/delta.png" {
			t.Error("a logo that did not load an hour ago is due")
		}
	}
	if want := []string{"http://icons.example/alpha.png", "http://icons.example/charlie.png"}; !reflect.DeepEqual(addrs, want) {
		t.Errorf("due %v, want %v", addrs, want)
	}
	due, err = s.LogosToCheck(ctx, at.Add(25*time.Hour), 24*time.Hour, 30*24*time.Hour, 50)
	// Bravo's own does not load, but it has no guide's to take its place.
	if err != nil || len(due) != 3 {
		t.Errorf("due a day later: %+v, %v", due, err)
	}
	for _, d := range due {
		if d.Address == "http://extra.example/delta.png" && (d.UserAgent != "Guide/1" || !d.PublicOnly) {
			t.Errorf("the guide's icon is fetched as %+v", d)
		}
	}
	if checked, failing, err := s.LogoCounts(ctx); err != nil || checked != 4 || failing != 2 {
		t.Errorf("counts %d, %d, %v", checked, failing, err)
	}
	if err := s.ForgetLogoChecks(ctx, at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if checked, _, _ := s.LogoCounts(ctx); checked != 0 {
		t.Errorf("%d checks left", checked)
	}

	// A read of the source's guide without icons takes them away.
	w, err = s.BeginGuide(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(ctx, at.Add(time.Hour), "own"); err != nil {
		t.Fatal(err)
	}
	if got := logos(); got["Charlie"] != "" {
		t.Errorf("Charlie's logo after a read without icons: %q", got["Charlie"])
	}
}
