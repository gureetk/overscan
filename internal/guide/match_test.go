// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"strings"
	"testing"
	"unicode"

	"overscan.gureet.ca/internal/store"
)

func TestLeadingTag(t *testing.T) {
	for _, c := range []struct{ name, tag, rest string }{
		{"UK: Granite News", "UK", "Granite News"},
		{"UK | Granite News", "UK", "Granite News"},
		{"UK|Granite News", "UK", "Granite News"},
		{"|UK| Granite News", "UK", "Granite News"},
		{"[UK] Granite News", "UK", "Granite News"},
		{"(UK) Granite News", "UK", "Granite News"},
		{"UK - Granite News", "UK", "Granite News"},
		{"UK – Granite News", "UK", "Granite News"},
		{"Al-Granite News", "", "Al-Granite News"},
		{"Granite News", "", "Granite News"},
		{"[4K] Granite News", "", "[4K] Granite News"},
		{"Granite: The Channel", "", "Granite: The Channel"},
		{"VIP: Granite News", "VIP", "Granite News"},
	} {
		if tag, rest := leadingTag(c.name); tag != c.tag || rest != c.rest {
			t.Errorf("leadingTag(%q) = %q, %q; want %q, %q", c.name, tag, rest, c.tag, c.rest)
		}
	}
}

func TestNameParts(t *testing.T) {
	for _, c := range []struct{ name, key, exact, place string }{
		{"UK: Granite News HD", "granitenews", "granitenewshd", "gb"},
		{"|US| Granite News", "granitenews", "granitenews", "us"},
		{"USA - Granite News", "granitenews", "granitenews", "us"},
		{"Granite News (DE)", "granitenews", "granitenews", "de"},
		{"Granite News [fr] FHD", "granitenews", "granitenewsfhd", "fr"},
		// Not countries: the name as D51 reads it.
		{"VIP: Granite News", "granitenews", "granitenews", ""},
		{"TV: Granite News", "granitenews", "granitenews", ""},
		{"SD: Granite News", "granitenews", "granitenews", ""},
		{"Granite News +1", "granitenews+1", "granitenews+1", ""},
		{"Granite News Extra", "granitenewsextra", "granitenewsextra", ""},
		{"UK:", "", "", "gb"},
	} {
		key, exact, place := nameParts(c.name)
		if key != c.key || exact != c.exact || place != c.place {
			t.Errorf("nameParts(%q) = %q, %q, %q; want %q, %q, %q", c.name, key, exact, place, c.key, c.exact, c.place)
		}
	}
	for _, c := range []struct{ id, place string }{
		{"GraniteNews.uk", "gb"}, {"GraniteNews.uk@HD", "gb"}, {"granite.news.co.uk", "gb"}, {"GraniteNews.US", "us"},
		{"c1003.example", ""}, {"granite.tv", ""}, {"GraniteNews", ""}, {"", ""},
	} {
		if got := idCountry(c.id); got != c.place {
			t.Errorf("idCountry(%q) = %q, want %q", c.id, got, c.place)
		}
	}
	for _, c := range []struct{ category, place string }{
		{"UK | News", "gb"}, {"UK: Sport", "gb"}, {"DE", "de"}, {"|FR| Films", "fr"}, {"News", ""}, {"VIP | News", ""}, {"", ""},
	} {
		if got := tagCountry(c.category); got != c.place {
			t.Errorf("tagCountry(%q) = %q, want %q", c.category, got, c.place)
		}
	}
}

// The fictional guides' channels, with traps: the same name in two
// countries, a name that holds another, copies in another quality, and a
// country that a channel's own tag rules out.
var trapListed = []store.ListedChannel{
	{ID: 101, GuideID: 1, XMLTVID: "GraniteNews.uk", Names: []string{"Granite News"}},
	{ID: 102, GuideID: 1, XMLTVID: "GraniteNews.us", Names: []string{"Granite News"}},
	{ID: 103, GuideID: 1, XMLTVID: "GraniteNewsExtra.uk", Names: []string{"Granite News Extra"}},
	{ID: 104, GuideID: 1, XMLTVID: "WillowTV.uk", Names: []string{"Willow TV"}},
	{ID: 105, GuideID: 1, XMLTVID: "WillowTVHD.uk", Names: []string{"Willow TV HD"}},
	{ID: 106, GuideID: 1, XMLTVID: "CopperFilms.de", Names: []string{"Copper Films"}},
	{ID: 107, GuideID: 1, XMLTVID: "meadow-kids", Names: []string{"Meadow Kids", "MK"}},
	{ID: 108, GuideID: 1, XMLTVID: "Summit.One", Names: []string{"Summit One"}},
	{ID: 109, GuideID: 1, XMLTVID: "harbour-sport-1", Names: []string{"Harbour Sport"}},
	{ID: 201, GuideID: 2, XMLTVID: "harbour-sport-2", Names: []string{"Harbour Sport"}},
	{ID: 202, GuideID: 2, XMLTVID: "Summit.One", Names: []string{"Summit One (second guide)"}},
	{ID: 203, GuideID: 2, XMLTVID: "LanternMusic.fr", Names: []string{"Lantern Music"}},
	{ID: 204, GuideID: 2, XMLTVID: "LanternMusic.it", Names: []string{"Lantern Music"}},
	{ID: 301, GuideID: 3, XMLTVID: "LanternMusic.de", Names: []string{"Lantern Music"}},
}

func TestMatchTraps(t *testing.T) {
	type want struct {
		guideChannel int64
		byName       bool
	}
	for _, c := range []struct {
		name, id, category string
		want               want // zero: no match
	}{
		{name: "UK: Granite News", want: want{101, true}},
		{name: "US | Granite News", want: want{102, true}},
		{name: "|UK| Granite News HD", want: want{101, true}},
		{name: "UK - Granite News", want: want{101, true}},
		// The same name in two countries, and nothing to tell them apart.
		{name: "Granite News"},
		// Its group says which.
		{name: "Granite News", category: "UK | News", want: want{101, true}},
		{name: "Granite News", category: "US: News", want: want{102, true}},
		// A guide ID matches first, whatever its case and whatever the name
		// says.
		{name: "UK: Granite News", id: "graniteNEWS.us", want: want{102, false}},
		// A name that holds another is not that other.
		{name: "UK: Granite News Extra", want: want{103, true}},
		{name: "Granite News Extra HD", want: want{103, true}},
		{name: "Granite"},
		{name: "Granite News 2"},
		{name: "Granite News +1"},
		// Copies in another quality: the exact name chooses.
		{name: "UK: Willow TV HD", want: want{105, true}},
		{name: "UK: Willow TV", want: want{104, true}},
		{name: "Willow TV FHD"},
		// A country that the channel's tag rules out.
		{name: "DE: Copper Films", want: want{106, true}},
		{name: "FR: Copper Films"},
		{name: "Copper Films", want: want{106, true}},
		// A guide's channel of no country fills a channel of any.
		{name: "Meadow Kids", category: "US | Kids", want: want{107, true}},
		{name: "MK", want: want{107, true}},
		// The first guide in order wins.
		{name: "Harbour Sport", want: want{109, true}},
		{name: "x", id: "summit.one", want: want{108, false}},
		// Two countries in the second guide, one in the third: only a
		// country tag decides.
		{name: "Lantern Music"},
		{name: "IT: Lantern Music", want: want{204, true}},
		{name: "DE: Lantern Music", want: want{301, true}},
		{name: "ES: Lantern Music"},
		// An ID that no guide has falls back to the name.
		{name: "UK: Granite News", id: "nowhere.example", want: want{101, true}},
	} {
		got := match([]store.MatchCandidate{{ID: 1, XMLTVID: c.id, Name: c.name, Category: c.category}}, trapListed)
		switch {
		case c.want == (want{}) && len(got) != 0:
			t.Errorf("%q (id %q, group %q) matched %+v, want nothing", c.name, c.id, c.category, got)
		case c.want != (want{}) && (len(got) != 1 || got[0].GuideChannelID != c.want.guideChannel || got[0].ByName != c.want.byName):
			t.Errorf("%q (id %q, group %q) matched %+v, want %+v", c.name, c.id, c.category, got, c.want)
		}
	}
}

func TestMatchMany(t *testing.T) {
	candidates := []store.MatchCandidate{
		{ID: 1, Name: "UK: Granite News"},
		{ID: 2, Name: "Granite News HD", Category: "UK | News"},
		{ID: 3, Name: "Granite News"},
		{ID: 4, XMLTVID: "GRANITENEWS.US", Name: "Anything"},
	}
	got := match(candidates, trapListed)
	want := []store.GuideMatch{{ChannelID: 1, GuideChannelID: 101, ByName: true}, {ChannelID: 2, GuideChannelID: 101, ByName: true},
		{ChannelID: 4, GuideChannelID: 102}}
	if len(got) != len(want) {
		t.Fatalf("matched %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("match %d: %+v, want %+v", i, got[i], want[i])
		}
	}
	if got := match(candidates, nil); len(got) != 0 {
		t.Errorf("without guides: %+v", got)
	}
}

// FuzzNameParts checks that any name, from a guide or a source, gives a
// key of letters, digits and plus signs that starts its exact form, and a
// country of two letters or none.
func FuzzNameParts(f *testing.F) {
	for _, s := range []string{"UK: Granite News HD", "|US| Granite", "[DE] (FR) x", "UK – a", "- x", "|", "((", "[UK", "Al-Jazeera",
		"ÄÖ: Ünïcödé", "UK:\x00x", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		key, exact, place := nameParts(name)
		if !strings.HasPrefix(exact, key) {
			t.Errorf("nameParts(%q): key %q does not start exact %q", name, key, exact)
		}
		if strings.ContainsFunc(exact, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+' }) {
			t.Errorf("nameParts(%q): exact %q", name, exact)
		}
		if place != "" && len(place) != 2 {
			t.Errorf("nameParts(%q): country %q", name, place)
		}
		_ = tagCountry(name)
		_ = idCountry(name)
	})
}
