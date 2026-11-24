// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/artwork"
)

// M9's Done when (the plan, section 14), walked end to end with this
// tool's fictional provider, its guides and a box: an added guide fills the
// channels whose sources give none, by ID and by name, with no wrong match
// among fictional guides; a guide chosen by hand survives refreshes; logos
// follow their order; placeholders show only where set; and a lineup moved
// from one source to two others keeps its numbers, groups and favourites.
// TMDB's pictures for titles whose own are missing, broken or placeholders
// are walked against a fake TMDB in the library's tests
// (TestPicturesDoneWhen), since the server cannot be pointed at another
// TMDB; memory with a large guide is the soak's (docs/testing.md); and that
// an upgrade changes nothing is the rest of this tool's work, with the
// household's guide, choices, logos, placeholders, pictures and move
// (household.setUpGuides and after).
func TestGuidesDoneWhen(t *testing.T) {
	w := startWalk(t)
	p, a := w.p, w.a
	call := func(method, path string, body, out any) {
		t.Helper()
		w.call(t, method, path, body, out)
	}

	// Sources two and three, three with a channel under a guide ID that
	// its guide lacks, whose logo is not there, and one without a logo.
	p.add("three", fakeChannel{key: "broken-qw", name: "Quay Weather", group: "Travel", guide: "quay-weather.example"})
	p.add("three", fakeChannel{key: "pn", name: "Pier Notices", group: "Travel", noLogo: true})
	sources := map[string]int64{}
	var list struct{ Sources []idName }
	call("GET", "sources", nil, &list)
	for _, s := range list.Sources {
		sources[s.Name] = s.ID
	}
	addSources := func(names ...string) {
		t.Helper()
		for _, name := range names {
			var added idName
			call("POST", "sources", map[string]any{"name": name, "type": "m3u", "url": p.url + "/" + name + ".m3u", "max_connections": 2}, &added)
			sources[name] = added.ID
		}
		// Sources not read since the server started are the new ones.
		if err := waitForSources(a, w.srv.started); err != nil {
			t.Fatal(err)
		}
	}
	addSources("two", "three")

	// A box of the household's, to see what devices see.
	b := newBox(w.base, boxMAC)
	s, err := b.session()
	if err != nil {
		t.Fatal(err)
	}
	code, _ := s["code"].(string)
	call("POST", "pairings/"+code+"/approve", map[string]any{"label": "Den"}, nil)
	if _, err := b.session(); err != nil || b.token == "" {
		t.Fatalf("the box: %v", err)
	}

	// An added guide fills the channels whose sources give none, by ID and
	// by name, with no wrong match: two guides, extra and more, more with
	// traps. Each is read at once.
	guides := map[string]int64{}
	for _, name := range []string{"extra", "more"} {
		var added idName
		call("POST", "guides", map[string]any{"name": name, "address": p.url + "/" + name + ".xml"}, &added)
		guides[name] = added.ID
	}
	type guideState struct {
		Reading   bool    `json:"reading"`
		ReadAt    *string `json:"read_at"`
		ReadError string  `json:"read_error"`
		Fills     int     `json:"fills"`
		ByName    int     `json:"by_name"`
		ByHand    int     `json:"by_hand"`
		Waiting   int     `json:"waiting"`
	}
	waitGuide := func(name string, ok func(g guideState) bool) guideState {
		t.Helper()
		deadline := time.Now().Add(60 * time.Second)
		for {
			var g guideState
			call("GET", fmt.Sprintf("guides/%d", guides[name]), nil, &g)
			if g.ReadError != "" {
				t.Fatalf("the guide %s: %s", name, g.ReadError)
			}
			if g.ReadAt != nil && !g.Reading && g.Waiting == 0 && ok(g) {
				return g
			}
			if time.Now().After(deadline) {
				t.Fatalf("the guide %s, after 60 seconds: %+v", name, g)
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	// extra fills three channels by name; more fills Quay Weather by its
	// guide ID and Pier Notices by name, and nothing else.
	if g := waitGuide("extra", func(g guideState) bool { return g.Fills == len(extraFills) }); g.ByName != len(extraFills) {
		t.Errorf("extra: %+v", g)
	}
	if g := waitGuide("more", func(g guideState) bool { return g.Fills == 2 }); g.ByName != 1 {
		t.Errorf("more: %+v", g)
	}
	onNow := func() map[string][]boxProgramme {
		t.Helper()
		return boxGuide(t, b, entriesBySource(t, a, ""))
	}
	shown := onNow()
	for name, want := range map[string]string{
		"Lantern Sport":   "Lantern Sport, from the extra guide", // more lists it too; extra comes first
		"Cobalt Films":    "Cobalt Films, from the extra guide",
		"Orchard Extra":   "Orchard Extra, from the extra guide",
		"Quay Weather":    "QW Channel, from the more guide",   // by its guide ID
		"Pier Notices":    "Pier Notices, from the more guide", // by name
		"Harbour News":    "Harbour News at ",                  // its source's guide, whatever more lists
		"Granite News":    "Granite News at ",                  // its source's, whatever extra lists
		"Granite Kids":    "Granite Kids at ",
		"Willow Travel":   "", // UK and US tie, and nothing chooses
		"Meadow Bulletin": "", // more's Meadow Bulletin Extra is not its name
		"Granite Kids +1": "", // more's Granite Kids is not its name
		"Juniper Shop":    "",
	} {
		got := shown[name]
		switch {
		case want == "" && len(got) != 0:
			t.Errorf("%s shows %+v, want nothing", name, got)
		case want != "" && (len(got) == 0 || !strings.HasPrefix(got[0].Title, want)):
			t.Errorf("%s shows %+v, want %q", name, got, want)
		}
	}

	// A guide chosen by hand: Meadow Bulletin takes more's Meadow Bulletin
	// Extra, which more then reads, and keeps it through a refresh of its
	// source and a read of the guide.
	channels := entriesBySource(t, a, "")
	bulletin := channels[sourced{"three", "Meadow Bulletin"}]
	var found struct {
		Options []struct {
			GuideChannelID int64  `json:"guide_channel_id"`
			Guide          string `json:"guide"`
		} `json:"options"`
	}
	call("GET", "guide-channels?find="+url.QueryEscape("Meadow Bulletin Extra"), nil, &found)
	var extraBulletin int64
	for _, o := range found.Options {
		if o.Guide == "more" {
			extraBulletin = o.GuideChannelID
		}
	}
	if extraBulletin == 0 {
		t.Fatalf("more's Meadow Bulletin Extra is not offered: %+v", found)
	}
	call("PUT", fmt.Sprintf("channel-guides/%d", bulletin.ChannelID), map[string]any{"guide_channel_id": extraBulletin}, nil)
	waitGuide("more", func(g guideState) bool { return g.ByHand == 1 && g.Fills == 3 })
	byHand := func(when string) {
		t.Helper()
		var g struct {
			From  string `json:"from"`
			Guide *struct {
				ByHand bool   `json:"by_hand"`
				Guide  string `json:"guide"`
			} `json:"guide"`
		}
		call("GET", fmt.Sprintf("channel-guides/%d", bulletin.ChannelID), nil, &g)
		if g.From != "guide" || g.Guide == nil || !g.Guide.ByHand || g.Guide.Guide != "more" {
			t.Errorf("%s, Meadow Bulletin's guide: %+v", when, g)
		}
		if got := onNow()["Meadow Bulletin"]; len(got) == 0 || got[0].Title != "Meadow Bulletin Extra, from the more guide" {
			t.Errorf("%s, Meadow Bulletin shows %+v", when, got)
		}
	}
	byHand("chosen")
	call("POST", fmt.Sprintf("sources/%d/refresh", sources["three"]), nil, nil)
	byHand("after its source's refresh")
	call("POST", fmt.Sprintf("guides/%d/read", guides["more"]), nil, nil)
	waitGuide("more", func(g guideState) bool { return g.ByHand == 1 })
	byHand("after the guide's read")

	// Logos follow their order: by hand, then a rule, then the channel's
	// own if it loads, then its guide's if it loads, then none.
	logo := func(e entry, address string) string {
		if address == "" {
			return ""
		}
		return artwork.LogoPath(e.ID, address)
	}
	entries := entriesBySource(t, a, "")
	call("PATCH", fmt.Sprintf("lineup/channels/%d", entries[sourced{"main", "Granite News"}].ID),
		map[string]any{"logo": p.url + "/logo/by-hand.png"}, nil)
	call("POST", "lineup/rules", map[string]any{"match": map[string]any{"name": "/^(Granite|Copper) News$/"}, "action": "logo",
		"logo": p.url + "/logo/rule.png"}, nil)
	wantLogos := map[sourced]string{
		{"main", "Granite News"}:   p.url + "/logo/by-hand.png", // by hand, over the rule
		{"main", "Copper News"}:    p.url + "/logo/rule.png",    // the rule's
		{"main", "Lantern Sport"}:  p.url + "/logo/ls.png",      // its own, which loads, before extra's icon
		{"three", "Quay Weather"}:  p.url + "/logo/more-qw.png", // its own is not there: more's icon
		{"three", "Pier Notices"}:  "",                          // it has none, and more's icon is not there
		{"three", "Willow Travel"}: p.url + "/logo/wt.png",      // its own: no guide fills it
	}
	checkLogos := func(when string, want map[sourced]string) {
		t.Helper()
		// The check of logos in the background follows the guides' reads.
		deadline := time.Now().Add(30 * time.Second)
		for {
			entries := entriesBySource(t, a, "")
			var wrong []string
			for c, address := range want {
				e := entries[c]
				got := ""
				if e.Logo != nil {
					got = *e.Logo
				}
				if got != logo(e, address) {
					wrong = append(wrong, fmt.Sprintf("%s's %s: %s, want %s", c.source, c.name, got, address))
				}
			}
			if len(wrong) == 0 {
				return
			}
			if time.Now().After(deadline) {
				slices.Sort(wrong)
				t.Errorf("%s, logos:\n%s", when, strings.Join(wrong, "\n"))
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}
	checkLogos("at first", wantLogos)
	// extra set to give its logos first: Lantern Sport takes its icon.
	call("PATCH", fmt.Sprintf("guides/%d", guides["extra"]), map[string]any{"logos": "first"}, nil)
	wantLogos[sourced{"main", "Lantern Sport"}] = p.url + "/logo/extra-1.example-icon.png"
	checkLogos("with extra's first", wantLogos)
	call("PATCH", fmt.Sprintf("guides/%d", guides["extra"]), map[string]any{"logos": "fill"}, nil)
	wantLogos[sourced{"main", "Lantern Sport"}] = p.url + "/logo/ls.png"
	checkLogos("with extra's back where they fill", wantLogos)

	// Placeholders show only where set: none at first; with hour-long
	// ones, a channel without a guide shows one in each hour, and one with
	// programmes none; none again once they are off.
	placeholders := func(name string) (n int, programmes []boxProgramme) {
		for _, p := range onNow()[name] {
			if p.Placeholder {
				n++
				if p.Title != name || p.Stop-p.Start != 3600 {
					t.Errorf("%s's placeholder: %+v", name, p)
				}
			}
			programmes = append(programmes, p)
		}
		return n, programmes
	}
	if n, _ := placeholders("Willow Travel"); n != 0 {
		t.Errorf("placeholders before they are set: %d", n)
	}
	call("PATCH", "settings", map[string]any{"guide_placeholders": 60}, nil)
	if n, _ := placeholders("Willow Travel"); n < 3 {
		t.Errorf("Willow Travel shows %d placeholders over three hours", n)
	}
	for _, name := range []string{"Harbour News", "Quay Weather", "Meadow Bulletin"} {
		if n, got := placeholders(name); n != 0 || len(got) == 0 {
			t.Errorf("%s, which has programmes, shows %+v", name, got)
		}
	}
	call("PATCH", "settings", map[string]any{"guide_placeholders": 0}, nil)
	if n, got := placeholders("Willow Travel"); n != 0 || len(got) != 0 {
		t.Errorf("placeholders after they are off: %+v", got)
	}

	// A lineup moved from one source to two others keeps its numbers,
	// groups and favourites: main's channels go to four and five, a new
	// provider's sources, after the box marked three of them.
	for _, name := range []string{"Harbour News", "Granite Kids", "Lantern Films"} {
		if err := b.call("PUT", fmt.Sprintf("channels/%d/favourite", entries[sourced{"main", name}].ID), nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	before := entriesBySource(t, a, "")
	addSources("four", "five")
	var plan struct {
		Matches []struct {
			Channel struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"channel"`
			To struct {
				ID       int64  `json:"id"`
				SourceID int64  `json:"source_id"`
				Name     string `json:"name"`
			} `json:"to"`
			By string `json:"by"`
		} `json:"matches"`
		Unmatched []struct {
			Channel struct {
				Name string `json:"name"`
			} `json:"channel"`
			Reason string `json:"reason"`
		} `json:"unmatched"`
	}
	call("GET", fmt.Sprintf("sources/%d/moves?to=%d,%d", sources["main"], sources["four"], sources["five"]), nil, &plan)
	matched := map[string]string{}
	var moves []map[string]int64
	for _, m := range plan.Matches {
		to := "four"
		if m.To.SourceID == sources["five"] {
			to = "five"
		}
		matched[m.Channel.Name] = to + ": " + m.To.Name + " by " + m.By
		moves = append(moves, map[string]int64{"channel": m.Channel.ID, "to": m.To.ID})
	}
	wantMatched := map[string]string{
		"Harbour News":  "four: UK: Harbour News by guide_id",
		"Granite News":  "four: Granite News by guide_id",
		"Copper News":   "four: Copper News HD by guide_id",
		"Meadow Sport":  "four: Meadow Sport by guide_id",
		"Lantern Sport": "four: Lantern Sport by name",
		"Granite Kids":  "five: Granite Kids by name",
		"Willow Kids":   "five: Willow Kids by name",
		"Lantern Films": "five: Lantern Films by name",
		"Orchard Films": "five: Orchard Films by name",
	}
	if !mapsEqual(matched, wantMatched) {
		t.Errorf("the matches:\n%v\nwant\n%v", matched, wantMatched)
	}
	unmatched := map[string]string{}
	for _, u := range plan.Unmatched {
		unmatched[u.Channel.Name] = u.Reason
	}
	if unmatched["Harbour News HD"] != "taken" || unmatched["Granite Kids +1"] != "no_match" || unmatched["Summit News"] != "no_match" {
		t.Errorf("without a match: %v", unmatched)
	}
	var moved struct {
		Channels int `json:"channels"`
	}
	call("POST", fmt.Sprintf("sources/%d/moves", sources["main"]), map[string]any{"moves": moves}, &moved)
	if moved.Channels != len(wantMatched) {
		t.Errorf("moved %d channels, want %d", moved.Channels, len(wantMatched))
	}
	after := entriesBySource(t, a, "")
	byID := map[int64]entry{}
	for _, e := range after {
		byID[e.ID] = e
	}
	for name, match := range wantMatched {
		was := before[sourced{"main", name}]
		e, ok := byID[was.ID]
		to, newName, _ := strings.Cut(strings.TrimSuffix(strings.TrimSuffix(match, " by guide_id"), " by name"), ": ")
		switch {
		case !ok:
			t.Errorf("%s's entry is gone", name)
		case e.Source != to || e.ChannelName != newName:
			t.Errorf("%s plays %s's %s, want %s's %s", name, e.Source, e.ChannelName, to, newName)
		case !samePtr(e.Number, was.Number) || !samePtr(e.Group, was.Group) || e.Name != was.Name:
			t.Errorf("%s's entry was number %v in %v named %q; now %v in %v named %q", name, deref(was.Number), deref(was.Group), was.Name,
				deref(e.Number), deref(e.Group), e.Name)
		}
		// The new source's own entry of the channel went.
		for _, other := range after {
			if other.ID != was.ID && other.Source == to && other.ChannelName == newName {
				t.Errorf("%s's %s keeps an entry of its own: %+v", to, newName, other)
			}
		}
	}
	if _, ok := after[sourced{"four", "Tidal News"}]; !ok {
		t.Error("four's Tidal News, which nothing moved to, has no entry")
	}
	if e := byID[before[sourced{"main", "Granite News"}].ID]; e.Logo == nil || *e.Logo != logo(e, p.url+"/logo/by-hand.png") {
		t.Errorf("Granite News's logo set by hand: %v", e.Logo)
	}
	var lineup struct {
		Favourites []int64 `json:"favourites"`
	}
	if err := b.call("GET", "channels", nil, &lineup); err != nil {
		t.Fatal(err)
	}
	var wantFavourites []int64
	for _, name := range []string{"Harbour News", "Granite Kids", "Lantern Films"} {
		wantFavourites = append(wantFavourites, before[sourced{"main", name}].ID)
	}
	if !slices.Equal(lineup.Favourites, wantFavourites) {
		t.Errorf("the box's favourites: %v, want %v", lineup.Favourites, wantFavourites)
	}
	var play struct {
		URL string `json:"url"`
	}
	if err := b.call("POST", fmt.Sprintf("channels/%d/play", before[sourced{"main", "Harbour News"}].ID), map[string]any{}, &play); err != nil {
		t.Fatal(err)
	}
	asked := len(p.asked())
	readStream(t, play.URL)
	if got := p.asked(); len(got) <= asked || got[len(got)-1] != "hn4" {
		t.Errorf("Harbour News played %v, want four's", got[asked:])
	}
	// main's next read gives what moved no entry again.
	call("POST", fmt.Sprintf("sources/%d/refresh", sources["main"]), nil, nil)
	for name := range wantMatched {
		if e, ok := entriesBySource(t, a, "")[sourced{"main", name}]; ok {
			t.Errorf("main's %s came back: %+v", name, e)
		}
	}
}

// sourced names a channel by its source and its name there.
type sourced struct{ source, name string }

// entriesBySource returns a lineup's channels by their sources and names
// there; query picks the lineup, "" for the household's.
func entriesBySource(t *testing.T, a *admin, query string) map[sourced]entry {
	t.Helper()
	path := "lineup/channels?limit=1000"
	if query != "" {
		path += "&" + strings.TrimPrefix(query, "?")
	}
	var list struct {
		Channels []entry `json:"channels"`
	}
	if err := a.call("GET", path, nil, &list); err != nil {
		t.Fatal(err)
	}
	out := map[sourced]entry{}
	for _, e := range list.Channels {
		out[sourced{e.Source, e.ChannelName}] = e
	}
	return out
}

// A boxProgramme is a programme as the box's guide gives it.
type boxProgramme struct {
	Title       string `json:"title"`
	Start       int64  `json:"start"`
	Stop        int64  `json:"stop"`
	Placeholder bool   `json:"placeholder"`
}

// boxGuide returns what the box's guide shows over the next three hours,
// by the names of the channels at their sources.
func boxGuide(t *testing.T, b *box, entries map[sourced]entry) map[string][]boxProgramme {
	t.Helper()
	var ids []string
	names := map[string]string{}
	for c, e := range entries {
		id := strconv.FormatInt(e.ID, 10)
		ids = append(ids, id)
		names[id] = c.name
	}
	out := map[string][]boxProgramme{}
	for i := 0; i < len(ids); i += 100 {
		var g struct {
			Channels map[string][]boxProgramme `json:"channels"`
		}
		if err := b.call("GET", "guide?ids="+strings.Join(ids[i:min(i+100, len(ids))], ","), nil, &g); err != nil {
			t.Fatal(err)
		}
		for id, ps := range g.Channels {
			out[names[id]] = ps
		}
	}
	return out
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func samePtr[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func deref[T any](p *T) any {
	if p == nil {
		return nil
	}
	return *p
}
