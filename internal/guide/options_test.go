// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

func TestLikenessWords(t *testing.T) {
	for _, c := range []struct {
		name, words, place string
	}{
		{"UK: Granite News HD", "granite news", "gb"},
		{"Granite News (UK)", "granite news", "gb"},
		{"Granite News UK", "granite news", "gb"},
		{"Granite News FHD 50fps", "granite news", ""},
		{"Copper One", "copper 1", ""},
		{"COPPER1", "copper 1", ""},
		{"Copper 4 +1", "copper 4 +1", ""},
		{"Granite", "granite", ""},
		{"US", "us", ""},
	} {
		words, place := likenessWords(c.name)
		if strings.Join(words, " ") != c.words || place != c.place {
			t.Errorf("%q: %q %q, want %q %q", c.name, words, place, c.words, c.place)
		}
	}
	for id, want := range map[string]string{
		"GraniteNews.uk":        "granite news",
		"GraniteNews.uk@HD":     "granite news",
		"copper4.co.uk":         "copper 4",
		"harbour-sport.example": "harbour sport",
		"meadow_tv":             "meadow tv",
	} {
		if got := strings.Join(idWords(id), " "); got != want {
			t.Errorf("%q: %q, want %q", id, got, want)
		}
	}
}

// testOptions are fictional guides' channels and sources' channels, with
// traps: the same name in two countries, a name that holds another, and
// words that many share.
var testOptions = []Option{
	{GuideChannelID: 1, Names: []string{"Granite News"}, XMLTVID: "GraniteNews.uk", GuideID: 1, Guide: "extra"},
	{GuideChannelID: 2, Names: []string{"Granite News"}, XMLTVID: "GraniteNews.us", GuideID: 1, Guide: "extra"},
	{GuideChannelID: 3, Names: []string{"Granite News Extra"}, XMLTVID: "GraniteNewsExtra.uk", GuideID: 1, Guide: "extra"},
	{GuideChannelID: 4, Names: []string{"Harbour Sport"}, XMLTVID: "harbour.example", GuideID: 1, Guide: "extra"},
	{GuideChannelID: 5, Names: []string{"Copper One"}, XMLTVID: "copper1.example", GuideID: 1, Guide: "extra"},
	{GuideChannelID: 6, Names: []string{"Meadow News"}, XMLTVID: "meadow.example", GuideID: 1, Guide: "extra"},
	{GuideChannelID: 7, Names: []string{"Willow News"}, XMLTVID: "willow.example", GuideID: 1, Guide: "extra"},
	{ChannelID: 20, Names: []string{"Lantern Films"}, XMLTVID: "lantern.example", SourceID: 1, Source: "main"},
	{ChannelID: 21, Names: []string{"Granite Kids"}, SourceID: 1, Source: "main"},
}

func names(options []Option) []string {
	var out []string
	for _, o := range options {
		where := o.Guide
		if o.Source != "" {
			where = o.Source
		}
		out = append(out, o.Name()+" "+strings.TrimPrefix(o.XMLTVID, "GraniteNews")+" "+where)
	}
	return out
}

func TestSuggest(t *testing.T) {
	x := newOptionIndex(testOptions)
	for _, c := range []struct {
		channel SuggestFor
		want    []string
	}{
		// Its country's; not another country's.
		{SuggestFor{Name: "UK: Granite News HD"}, []string{"Granite News .uk extra", "Granite News Extra Extra.uk extra"}},
		{SuggestFor{Name: "Granite News", Category: "US | News"}, []string{"Granite News .us extra"}},
		// Of no country, both, and the one that holds its name.
		{SuggestFor{Name: "Granite News"}, []string{"Granite News .uk extra", "Granite News .us extra", "Granite News Extra Extra.uk extra"}},
		// A number tells channels apart, and so does a word of a family.
		{SuggestFor{Name: "Copper 2"}, nil},
		{SuggestFor{Name: "Granite Sport"}, nil},
		// A spelling of its own, and a typing mistake.
		{SuggestFor{Name: "COPPER 1"}, []string{"Copper One copper1.example extra"}},
		{SuggestFor{Name: "Harbor Sport"}, []string{"Harbour Sport harbour.example extra"}},
		{SuggestFor{Name: "Lantern Films HD"}, []string{"Lantern Films lantern.example main"}},
		// Only common words alike: nothing.
		{SuggestFor{Name: "Orchard News"}, nil},
		{SuggestFor{Name: "Cobalt"}, nil},
		// Not itself.
		{SuggestFor{ChannelID: 21, Name: "Granite Kids"}, nil},
	} {
		if got := names(x.suggest(c.channel, 5)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%+v: %q, want %q", c.channel, got, c.want)
		}
	}
	if got := x.suggest(SuggestFor{Name: "UK: Granite News HD"}, 1); len(got) != 1 || got[0].GuideChannelID != 1 {
		t.Errorf("one suggestion: %+v", got)
	}
}

func TestSpelt(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"harbor", "harbour", true}, {"centre", "center", false}, {"meadow", "meadows", true}, {"news", "newz", false},
		{"lantern", "lantern", false}, {"granite", "garnet", false}, {"broadcasting", "broadcastign", true}, {"copper1", "copper2", false},
	} {
		if got := spelt(c.a, c.b); got != c.want {
			t.Errorf("%q, %q: %v", c.a, c.b, got)
		}
	}
}

func TestSearch(t *testing.T) {
	x := newOptionIndex(testOptions)
	for _, c := range []struct {
		text string
		want []string
	}{
		{"granite news", []string{"Granite News .uk extra", "Granite News .us extra", "Granite News Extra Extra.uk extra"}},
		{"GRANITE", []string{"Granite News .uk extra", "Granite News .us extra", "Granite News Extra Extra.uk extra", "Granite Kids  main"}},
		{"news", []string{"Granite News .uk extra", "Granite News .us extra", "Granite News Extra Extra.uk extra", "Meadow News meadow.example extra",
			"Willow News willow.example extra"}},
		{"lantern.ex", []string{"Lantern Films lantern.example main"}},
		{"  ", nil},
		{"nothing", nil},
	} {
		if got := names(x.search(c.text, 10)); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: %q, want %q", c.text, got, c.want)
		}
	}
	if got := x.search("granite", 2); len(got) != 2 {
		t.Errorf("two found: %+v", got)
	}
}

// TestOwnGuideChosenByHand checks that a guide's channel chosen by hand
// fills a channel, over the guides' matches, and that its guide is read
// for it at once (D140); and that suggestions and search come from the
// enabled guides and the channels with programmes of their own.
func TestOwnGuideChosenByHand(t *testing.T) {
	h := newOwnHarness(t)
	ctx := context.Background()
	id := h.addGuide("extra", h.address())
	h.o.Changed()
	h.o.step(ctx)
	listed, err := h.st.ListedChannels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]int64{}
	for _, l := range listed {
		byID[l.XMLTVID] = l.ID
	}

	// Suggestions for the Granite News that ties, and a search.
	sugg, err := h.o.Suggest(ctx, []SuggestFor{{ChannelID: h.chans["Granite News"], Name: "Granite News"}}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := names(sugg[h.chans["Granite News"]]); len(got) < 3 || !strings.HasPrefix(got[0], "Granite News ") {
		t.Errorf("suggestions %q", got)
	}
	found, err := h.o.Search(ctx, "covered", 0)
	if err != nil {
		t.Fatal(err)
	}
	// The guide's Covered, and the source's, whose own guide fills it.
	if got := names(found); !reflect.DeepEqual(got, []string{"Covered covered.example extra", "Covered  main"}) {
		t.Errorf("found %q", got)
	}

	// The Granite News that tied takes the UK's by hand; Granite, which
	// matched nothing, takes the Spare Channel's, which the last read did
	// not keep, so the guide is read again at once; Harbour takes none.
	fetches := h.fetches()
	for _, c := range []store.GuideChoice{
		{ChannelID: h.chans["Granite News"], GuideChannelID: byID["GraniteNews.uk"]},
		{ChannelID: h.chans["Granite"], GuideChannelID: byID["spare.example"]},
		{ChannelID: h.chans["Harbour"]},
	} {
		if err := h.st.SetGuideChoice(ctx, c, now); err != nil {
			t.Fatal(err)
		}
	}
	h.o.Changed()
	h.o.step(ctx)
	if h.fetches() != fetches+1 {
		t.Errorf("%d fetches, want %d", h.fetches(), fetches+1)
	}
	want := map[string]string{
		"UK: Granite News HD": "UK Evening, UK Late",
		// Both channels named Granite News show the UK's: the one whose
		// group says US by its match, the other by hand.
		"Granite News": "US Evening, UK Evening, UK Late",
		"Granite":      "Spare Evening",
		"Covered":      "Its own",
	}
	if got := h.shown(); !reflect.DeepEqual(got, want) {
		t.Errorf("shown %v, want %v", got, want)
	}
	if g, _ := h.st.Guide(ctx, id); g.Matched != 4 || g.ByHand != 2 || g.ByName != 2 || g.Waiting != 0 {
		t.Errorf("the guide %+v", g)
	}
	if log := h.logs.String(); !strings.Contains(log, "by_hand=2") {
		t.Errorf("the log: %s", log)
	}

	// The choices stay as the guides are matched again.
	h.o.SourcesChanged()
	h.o.step(ctx)
	if got := h.shown(); !reflect.DeepEqual(got, want) {
		t.Errorf("after matching again: %v", got)
	}

	// Taken back, Harbour has its match again.
	if _, err := h.st.ClearGuideChoice(ctx, h.chans["Harbour"]); err != nil {
		t.Fatal(err)
	}
	h.o.Changed()
	h.o.step(ctx)
	if got := h.shown()["Harbour"]; got != "Harbour Match" {
		t.Errorf("Harbour taken back shows %q", got)
	}

	// The index of options is built again after a change.
	h.set(func(h *ownHarness) { h.now = now.Add(time.Minute) })
	if err := h.st.SetGuideEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	h.o.Changed()
	found, err = h.o.Search(ctx, "covered", 0)
	if err != nil || len(found) != 1 || found[0].ChannelID == 0 {
		t.Errorf("found with the guide off: %+v, %v", found, err)
	}
}

// FuzzLikeness checks that any names are read for suggestions without
// failing, that a spelling is alike both ways, and that a channel's
// suggestions are among the options.
func FuzzLikeness(f *testing.F) {
	for _, seed := range [][2]string{{"UK: Granite News HD", "GraniteNews.uk"}, {"Harbor Sport", "harbour-sport.example"},
		{"Copper One", "copper1.co.uk@HD"}, {"|DE| ", "."}, {"(((", "@@"}, {"Ünïcödé 4K", "ÜnïcödéTV.de"}} {
		f.Add(seed[0], seed[1])
	}
	f.Fuzz(func(t *testing.T, name, id string) {
		words, _ := likenessWords(name)
		for _, w := range words {
			if w == "" {
				t.Fatalf("%q: an empty word", name)
			}
		}
		_ = idWords(id)
		if spelt(name, id) != spelt(id, name) {
			t.Fatalf("%q and %q are alike one way only", name, id)
		}
		x := newOptionIndex(append([]Option{{GuideChannelID: 99, Names: []string{name}, XMLTVID: id}}, testOptions...))
		for _, o := range x.suggest(SuggestFor{Name: name}, 5) {
			if o.GuideChannelID == 0 && o.ChannelID == 0 {
				t.Fatalf("%q: a suggestion that is no option: %+v", name, o)
			}
		}
		_ = x.search(name, 5)
	})
}
