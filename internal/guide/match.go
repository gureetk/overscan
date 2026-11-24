// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"strings"
	"unicode"

	"overscan.gureet.ca/internal/store"
)

// Matching channels with the guides of the household's own (D139). A
// channel that its own source gives no programme takes those of a guide's
// channel: the one with its guide ID, in the first guide that lists that
// ID; else the one whose name is its name, compared as NameKey compares
// names. A country tag tells apart the same name in two countries, and a
// name that still ties matches nothing: no likeness looser than the same
// name matches on its own, since a wrong guide is worse than none.

// countries are the countries that a tag can name: ISO 3166-1's two-letter
// codes, by which guides and providers tag their channels, and a few other
// names that providers use for some of them. TV and FM, which name
// Tuvalu and Micronesia, are left out, since names and guide IDs use them
// for television and radio, and SD, Sudan's, which is a quality.
var countries = func() map[string]string {
	m := map[string]string{"uk": "gb", "usa": "us", "uae": "ae", "ksa": "sa"}
	for _, code := range strings.Fields(`
		ad ae af ag ai al am ao aq ar as at au aw ax az ba bb bd be bf bg bh bi bj bl bm bn bo bq br bs bt bv bw by bz
		ca cc cd cf cg ch ci ck cl cm cn co cr cu cv cw cx cy cz de dj dk dm do dz ec ee eg eh er es et fi fj fk fo fr
		ga gb gd ge gf gg gh gi gl gm gn gp gq gr gs gt gu gw gy hk hm hn hr ht hu id ie il im in io iq ir is it
		je jm jo jp ke kg kh ki km kn kp kr kw ky kz la lb lc li lk lr ls lt lu lv ly
		ma mc md me mf mg mh mk ml mm mn mo mp mq mr ms mt mu mv mw mx my mz na nc ne nf ng ni nl no np nr nu nz om
		pa pe pf pg ph pk pl pm pn pr ps pt pw py qa re ro rs ru rw sa sb sc se sg sh si sj sk sl sm sn so sr ss st sv sx sy sz
		tc td tf tg th tj tk tl tm tn to tr tt tw tz ua ug um us uy uz va vc ve vg vi vn vu wf ws ye yt za zm zw`) {
		m[code] = code
	}
	return m
}()

// country returns the country that a tag names, "" if none.
func country(tag string) string { return countries[strings.ToLower(strings.TrimSpace(tag))] }

// leadingTag splits a short tag of letters off the start of a name: one
// before a colon or a bar ("UK: Granite News", "UK | Granite News"), before
// a dash with a space beside it ("UK - Granite News"), or between bars or
// brackets ("|UK| Granite News", "[UK] Granite News"). It returns the tag,
// and the name after it; "" and the name itself if it has none.
func leadingTag(name string) (tag, rest string) {
	s := strings.TrimSpace(name)
	letters := func(t string) bool {
		t = strings.TrimSpace(t)
		return t != "" && len(t) <= 5 && strings.IndexFunc(t, func(r rune) bool { return !unicode.IsLetter(r) }) < 0
	}
	if s != "" && strings.ContainsRune("|[(", rune(s[0])) {
		closing := map[byte]byte{'|': '|', '[': ']', '(': ')'}[s[0]]
		if j := strings.IndexByte(s[1:], closing); j > 0 && letters(s[1:1+j]) {
			return strings.TrimSpace(s[1 : 1+j]), strings.TrimSpace(s[2+j:])
		}
	}
	if i := strings.IndexAny(s, ":|"); i > 0 && i <= 5 && letters(s[:i]) {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+1:])
	}
	for _, dash := range []string{" - ", " -", "- ", " – ", "– ", " –"} {
		if i := strings.Index(s, dash); i > 0 && i <= 5 && letters(s[:i]) {
			return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+len(dash):])
		}
	}
	return "", s
}

// nameParts returns what a name is matched by: its key, as NameKey makes
// it once a leading country tag is left out; its exact form, the same
// with its quality words kept, to choose between names that tie; and the
// country that a tag at its start, or one in brackets, names, "" if none.
func nameParts(name string) (key, exact, place string) {
	tag, rest := leadingTag(name)
	if place = country(tag); place == "" {
		// Not a country: the name as NameKey reads it, which leaves out a
		// tag before a colon or a bar.
		rest = name
		place = bracketedCountry(name)
	}
	words := nameWords(rest)
	exact = strings.Join(words, "")
	for len(words) > 1 && qualityTags[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, ""), exact, place
}

// bracketedCountry returns the country that a tag in brackets names
// anywhere in a name, such as "(UK)" or "[UK]", "" if none.
func bracketedCountry(name string) string {
	for i := 0; i < len(name); i++ {
		closing := map[byte]byte{'(': ')', '[': ']'}[name[i]]
		if closing == 0 {
			continue
		}
		if j := strings.IndexByte(name[i+1:], closing); j > 0 && j <= 4 {
			if c := country(name[i+1 : i+1+j]); c != "" {
				return c
			}
		}
	}
	return ""
}

// tagCountry returns the country that a tag at the start of a category's
// name names, as "UK | News" does, or that the name is, as "UK" is; ""
// if neither.
func tagCountry(category string) string {
	if c := country(category); c != "" {
		return c
	}
	tag, _ := leadingTag(category)
	return country(tag)
}

// idCountry returns the country that the end of a guide's ID for a
// channel names, as in GraniteNews.uk or GraniteNews.uk@HD; "" if none.
func idCountry(id string) string {
	id, _, _ = strings.Cut(id, "@")
	if i := strings.LastIndexByte(id, '.'); i >= 0 {
		return country(id[i+1:])
	}
	return ""
}

// listing is a guide's channel, as matching reads it.
type listing struct {
	id, guide int64
	country   string
	exact     []string // the exact forms of its names
}

// match works out which of the guides' channels gives each candidate its
// programmes, with listed in the order of the guides, as D139 says. A
// candidate whose guide ID the guides list takes the first guide's channel
// with that ID, whatever its case. Any other takes the guides' channel
// whose name is its name, as nameParts reads both: where several are,
// those of another country than the one that the candidate's name, or else
// its category's name, gives are left out, and so are those of none if
// some are of that one; where those left still differ in country, it
// takes none; else those whose exact names are its own, if any are, and
// then the first guide's, if it has one of them alone.
func match(candidates []store.MatchCandidate, listed []store.ListedChannel) []store.GuideMatch {
	ls := make([]listing, len(listed))
	byID := map[string]int{}
	byKey := map[string][]int{}
	for i, c := range listed {
		l := listing{id: c.ID, guide: c.GuideID, country: idCountry(c.XMLTVID)}
		keys := map[string]bool{}
		for _, name := range c.Names {
			key, exact, place := nameParts(name)
			if key == "" {
				continue
			}
			if l.country == "" {
				l.country = place
			}
			l.exact = append(l.exact, exact)
			if !keys[key] {
				keys[key] = true
				byKey[key] = append(byKey[key], i)
			}
		}
		ls[i] = l
		if id := strings.ToLower(strings.TrimSpace(c.XMLTVID)); id != "" {
			if _, ok := byID[id]; !ok {
				byID[id] = i
			}
		}
	}
	var out []store.GuideMatch
	for _, c := range candidates {
		if id := strings.ToLower(strings.TrimSpace(c.XMLTVID)); id != "" {
			if i, ok := byID[id]; ok {
				out = append(out, store.GuideMatch{ChannelID: c.ID, GuideChannelID: ls[i].id})
				continue
			}
		}
		key, exact, place := nameParts(c.Name)
		if key == "" {
			continue
		}
		if place == "" {
			place = tagCountry(c.Category)
		}
		if i, ok := pick(ls, byKey[key], exact, place); ok {
			out = append(out, store.GuideMatch{ChannelID: c.ID, GuideChannelID: ls[i].id, ByName: true})
		}
	}
	return out
}

// pick chooses one of the guides' channels found by a candidate's name, as
// match says, and reports whether it could.
func pick(ls []listing, found []int, exact, place string) (int, bool) {
	keep := func(ok func(listing) bool) []int {
		var out []int
		for _, i := range found {
			if ok(ls[i]) {
				out = append(out, i)
			}
		}
		return out
	}
	if place != "" {
		if same := keep(func(l listing) bool { return l.country == place }); len(same) > 0 {
			found = same
		} else {
			found = keep(func(l listing) bool { return l.country == "" })
		}
	}
	if len(found) == 0 {
		return 0, false
	}
	places := map[string]bool{}
	for _, i := range found {
		if ls[i].country != "" {
			places[ls[i].country] = true
		}
	}
	if len(places) > 1 {
		return 0, false
	}
	same := keep(func(l listing) bool {
		for _, e := range l.exact {
			if e == exact {
				return true
			}
		}
		return false
	})
	if len(same) > 0 {
		found = same
	}
	first := ls[found[0]].guide
	if n := len(keep(func(l listing) bool { return l.guide == first })); n != 1 {
		return 0, false
	}
	return found[0], true
}
