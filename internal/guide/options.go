// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"

	"overscan.gureet.ca/internal/store"
)

// Choosing a channel's guide by hand (D140): what it can be chosen from,
// suggestions of close names, and a search. A guide can be chosen from
// the channels that the enabled guides of the household's own list, and
// from the channels whose own sources' guides give them programmes.
// Suggestions are only offered, never applied: unlike matching (D139),
// they take names that are alike, not only the same.

const (
	// maxSuggestions caps the suggestions for one channel.
	maxSuggestions = 5
	// maxFound caps what a search finds.
	maxFound = 50
	// optionsKept is how long the index of options stays after its last
	// use, so that a page of suggestions and the searches after it share
	// one, and a large one does not stay in memory for ever.
	optionsKept = 10 * time.Minute
	// commonWord is the share of the options that a word must be in for a
	// suggestion not to start from it, unless the channel has no other.
	commonWord = 0.05
	// closeEnough is the least likeness, from 0 to 1, that is suggested.
	closeEnough = 0.6
)

// Option is something that a channel's guide can be chosen from: a channel
// of a guide of the household's own, or a channel whose own source's guide
// gives it programmes, whose guide it would show as devices see it there.
type Option struct {
	GuideChannelID int64 // a guide's channel, 0 for a source's channel
	ChannelID      int64 // a source's channel, 0 for a guide's channel
	Names          []string
	XMLTVID        string
	GuideID        int64 // the guide of a guide's channel
	Guide          string
	SourceID       int64 // the source of a source's channel
	Source         string
	// Kept says, of a guide's channel, whether its guide holds its
	// programmes: whether a channel used it at the guide's last read.
	Kept bool
}

// Name returns the option's first name, or its guide ID if it has none.
func (o Option) Name() string {
	if len(o.Names) > 0 && o.Names[0] != "" {
		return o.Names[0]
	}
	return o.XMLTVID
}

// SuggestFor is a channel to suggest guides for.
type SuggestFor struct {
	ChannelID int64
	Name      string
	XMLTVID   string
	Category  string
}

// optionIndex holds the options, with the words of their names, for
// suggestions and search.
type optionIndex struct {
	options []Option
	words   [][][]int32        // per option, per name and its guide ID: its words
	joined  [][]string         // the same, each run together
	keys    map[string][]int32 // per name's words run together: the options
	byWord  map[int32][]int32  // per word: the options that have it
	vocab   map[string]int32
	text    []string // per word: the word
	df      []int32  // per word: how many options have it
	country []string
	used    time.Time
}

// numberWords are spelt numbers, which names write either way.
var numberWords = map[string]string{"zero": "0", "one": "1", "two": "2", "three": "3", "four": "4", "five": "5", "six": "6",
	"seven": "7", "eight": "8", "nine": "9", "ten": "10", "eleven": "11", "twelve": "12"}

// likenessWords returns the words that a name is compared by for
// suggestions, and the country that it names, if any: as nameParts reads
// it, with quality words left out wherever they are, numbers written as
// digits, and a country's code at its end taken as its country.
func likenessWords(name string) (words []string, place string) {
	tag, rest := leadingTag(name)
	if place = country(tag); place == "" {
		rest = name
		place = bracketedCountry(name)
	}
	for _, word := range nameWords(rest) {
		if qualityTags[word] {
			continue
		}
		for _, w := range splitDigits(word) {
			if d, ok := numberWords[w]; ok {
				w = d
			}
			words = append(words, w)
		}
	}
	if n := len(words); n > 1 {
		if c := country(words[n-1]); c != "" && len(words[n-1]) <= 3 {
			if place == "" {
				place = c
			}
			words = words[:n-1]
		}
	}
	return words, place
}

// splitDigits splits a word where letters meet digits, as in "bbc1".
func splitDigits(word string) []string {
	var out []string
	start := 0
	var last rune
	for i, r := range word {
		if i > 0 && (unicode.IsDigit(r) && unicode.IsLetter(last) || unicode.IsLetter(r) && unicode.IsDigit(last)) {
			out = append(out, word[start:i])
			start = i
		}
		last = r
	}
	return append(out, word[start:])
}

// idWords returns the words of a guide ID, split at its dots, dashes,
// underscores, changes of case and between letters and digits, without
// what follows an "@", a country's code at its end and the parts of a
// domain: "GraniteNews.uk" gives "granite" and "news", and
// "channel4.co.uk" "channel" and "4".
func idWords(id string) []string {
	id, _, _ = strings.Cut(id, "@")
	if i := strings.LastIndexByte(id, '.'); i > 0 && country(id[i+1:]) != "" {
		id = id[:i]
	}
	var b strings.Builder
	var last rune
	for _, r := range id {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			b.WriteRune(' ')
		case unicode.IsUpper(r) && unicode.IsLower(last), unicode.IsDigit(r) && unicode.IsLetter(last),
			unicode.IsLetter(r) && unicode.IsDigit(last):
			b.WriteRune(' ')
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
		last = r
	}
	words, _ := likenessWords(b.String())
	return slices.DeleteFunc(words, func(w string) bool { return domainWords[w] })
}

// domainWords are parts of the domains that guide IDs are often written
// as, which are no part of a channel's name.
var domainWords = map[string]bool{"co": true, "com": true, "net": true, "org": true, "example": true}

// newOptionIndex indexes the options.
func newOptionIndex(options []Option) *optionIndex {
	x := &optionIndex{options: options, words: make([][][]int32, len(options)), joined: make([][]string, len(options)), keys: map[string][]int32{},
		byWord: map[int32][]int32{}, vocab: map[string]int32{}, country: make([]string, len(options))}
	for i, o := range options {
		seen := map[int32]bool{}
		add := func(words []string) {
			if len(words) == 0 {
				return
			}
			ids := make([]int32, len(words))
			for j, w := range words {
				id, ok := x.vocab[w]
				if !ok {
					id = int32(len(x.df))
					x.vocab[w] = id
					x.df = append(x.df, 0)
					x.text = append(x.text, w)
				}
				ids[j] = id
				if !seen[id] {
					seen[id] = true
					x.df[id]++
					x.byWord[id] = append(x.byWord[id], int32(i))
				}
			}
			x.words[i] = append(x.words[i], ids)
			key := strings.Join(words, "")
			x.joined[i] = append(x.joined[i], key)
			if ks := x.keys[key]; len(ks) == 0 || ks[len(ks)-1] != int32(i) {
				x.keys[key] = append(ks, int32(i))
			}
		}
		for _, name := range o.Names {
			words, place := likenessWords(name)
			if x.country[i] == "" {
				x.country[i] = place
			}
			add(words)
		}
		add(idWords(o.XMLTVID))
		if c := idCountry(o.XMLTVID); c != "" {
			x.country[i] = c
		}
	}
	return x
}

// weight is how much sharing a word says, by how few options have it. A
// number says little, however rare: channels that differ by a number
// share the rest of their names.
func (x *optionIndex) weight(id int32, known bool) float64 {
	df := 0.0
	if known {
		df = float64(x.df[id])
	}
	w := math.Log(1 + float64(len(x.options)+1)/(df+1))
	if known && isNumber(x.text[id]) {
		w = min(w, 1)
	}
	return w
}

// isNumber reports whether a word is all digits.
func isNumber(w string) bool {
	return w != "" && strings.IndexFunc(w, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

// likeness compares a channel's words with an option's. One name's words
// must all be in the other's, spelt the same or a little differently, as
// "harbor" is from "harbour"; else they are not alike at all, since two
// channels of one family share most of their names ("Granite News" and
// "Granite Sport"). How alike they are is then twice the weight of the
// words that they share over the weight of both, so that sharing a rare
// word counts for more than sharing "news", and a word spelt differently
// counts for most of its weight. Names with numbers, none of them the
// same, are other channels: they count half. Where their words run
// together are the same, suggest takes them as alike as can be.
func (x *optionIndex) likeness(words []string, ids []int32, known []bool, option []int32) float64 {
	var shared, ours, theirs float64
	have := make(map[int32]bool, len(option))
	for _, id := range option {
		if !have[id] {
			have[id] = true
			theirs += x.weight(id, true)
		}
	}
	found := map[int32]bool{} // their words that ours have
	seen := map[string]bool{}
	missing := 0 // our words that theirs lack
	ourNumbers, sharedNumbers := false, false
	for i, w := range words {
		if seen[w] {
			continue
		}
		seen[w] = true
		wt := x.weight(ids[i], known[i])
		if isNumber(w) {
			wt = min(wt, 1)
			ourNumbers = true
			sharedNumbers = sharedNumbers || known[i] && have[ids[i]]
		}
		ours += wt
		if known[i] && have[ids[i]] {
			shared += wt
			found[ids[i]] = true
			continue
		}
		alike := false
		for id := range have {
			if spelt(w, x.text[id]) {
				shared += 0.8 * min(wt, x.weight(id, true))
				found[id] = true
				alike = true
				break
			}
		}
		if !alike {
			missing++
		}
	}
	if ours+theirs == 0 || missing > 0 && len(found) < len(have) {
		return 0
	}
	score := 2 * shared / (ours + theirs)
	if ourNumbers && !sharedNumbers {
		for id := range have {
			if isNumber(x.text[id]) {
				return score / 2
			}
		}
	}
	return score
}

// spelt reports whether two words of letters are the same word spelt a
// little differently: one letter added, left out or changed in words of
// five letters or more, or two in words of nine or more.
func spelt(a, b string) bool {
	if a == b || len(a) < 5 || len(b) < 5 {
		return false
	}
	allowed := 1
	if len(a) >= 9 && len(b) >= 9 {
		allowed = 2
	}
	if d := len(a) - len(b); d > allowed || -d > allowed {
		return false
	}
	for _, r := range a + b {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	// Levenshtein's distance, row by row, stopping once it is too far.
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		least := cur[0]
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			least = min(least, cur[j])
		}
		if least > allowed {
			return false
		}
		prev, cur = cur, prev
	}
	return prev[len(b)] <= allowed
}

// suggestion is an option with how alike it is.
type suggestion struct {
	option int32
	score  float64
}

// suggest returns the options whose names are close to a channel's, the
// closest first, at most limit of them.
func (x *optionIndex) suggest(c SuggestFor, limit int) []Option {
	words, place := likenessWords(c.Name)
	if place == "" {
		place = tagCountry(c.Category)
	}
	ids := make([]int32, len(words))
	known := make([]bool, len(words))
	for i, w := range words {
		ids[i], known[i] = x.vocab[w]
	}
	// The options that share a word with it, leaving out words that many
	// have unless it has no other, and those whose words run together are
	// its own.
	found := map[int32]bool{}
	rare := false
	for i := range words {
		if known[i] && float64(x.df[ids[i]]) <= commonWord*float64(len(x.options)) {
			rare = true
		}
	}
	for i := range words {
		if !known[i] || rare && float64(x.df[ids[i]]) > commonWord*float64(len(x.options)) {
			continue
		}
		for _, o := range x.byWord[ids[i]] {
			found[o] = true
		}
	}
	key := strings.Join(words, "")
	for _, o := range x.keys[key] {
		found[o] = true
	}
	var out []suggestion
	for o := range found {
		opt := x.options[o]
		if opt.ChannelID != 0 && opt.ChannelID == c.ChannelID {
			continue
		}
		best := 0.0
		for j, name := range x.words[o] {
			score := x.likeness(words, ids, known, name)
			if key != "" && x.joined[o][j] == key {
				score = 1
			}
			best = max(best, score)
		}
		switch theirs := x.country[o]; {
		case place != "" && theirs != "" && theirs != place:
			best *= 0.5
		case place != "" && theirs == place:
			best = min(1, best+0.1)
		}
		if best >= closeEnough {
			out = append(out, suggestion{o, best})
		}
	}
	slices.SortFunc(out, func(a, b suggestion) int {
		if c := cmp.Compare(b.score, a.score); c != 0 {
			return c
		}
		return cmp.Compare(a.option, b.option)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	options := make([]Option, len(out))
	for i, s := range out {
		options[i] = x.options[s.option]
	}
	return options
}

// search returns the options whose names or guide IDs hold text, whatever
// its case: those whose name is the text first, then those whose name
// starts with it, then the others, each in the options' order; at most
// limit of them.
func (x *optionIndex) search(text string, limit int) []Option {
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" {
		return nil
	}
	type hit struct {
		option int
		rank   int
	}
	var hits []hit
	for i, o := range x.options {
		rank := 4
		for _, name := range o.Names {
			n := strings.ToLower(name)
			switch {
			case n == text:
				rank = min(rank, 0)
			case strings.HasPrefix(n, text):
				rank = min(rank, 1)
			case strings.Contains(n, text):
				rank = min(rank, 2)
			}
		}
		if rank == 4 && strings.Contains(strings.ToLower(o.XMLTVID), text) {
			rank = 3
		}
		if rank < 4 {
			hits = append(hits, hit{i, rank})
		}
	}
	slices.SortStableFunc(hits, func(a, b hit) int { return cmp.Compare(a.rank, b.rank) })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Option, len(hits))
	for i, h := range hits {
		out[i] = x.options[h.option]
	}
	return out
}

// loadOptions reads what a channel's guide can be chosen from: the
// enabled guides' channels, in the guides' order, then the channels whose
// own sources' guides give them programmes, in the sources' order.
func loadOptions(ctx context.Context, st *store.Store) ([]Option, error) {
	guides, err := st.Guides(ctx)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	for _, g := range guides {
		names[g.ID] = g.Name
	}
	listed, err := st.ListedChannels(ctx)
	if err != nil {
		return nil, err
	}
	guided, err := st.GuidedChannels(ctx)
	if err != nil {
		return nil, err
	}
	options := make([]Option, 0, len(listed)+len(guided))
	for _, l := range listed {
		options = append(options, Option{GuideChannelID: l.ID, Names: l.Names, XMLTVID: l.XMLTVID, GuideID: l.GuideID,
			Guide: names[l.GuideID], Kept: l.Kept})
	}
	for _, c := range guided {
		options = append(options, Option{ChannelID: c.ID, Names: []string{c.Name}, XMLTVID: c.XMLTVID, SourceID: c.SourceID,
			Source: c.Source})
	}
	return options, nil
}

// optionIndex returns the index of options, building it if it was
// forgotten.
func (o *Own) optionIndex(ctx context.Context) (*optionIndex, error) {
	o.choicesMu.Lock()
	defer o.choicesMu.Unlock()
	now := o.now()
	if o.choices != nil && now.Sub(o.choices.used) < optionsKept {
		o.choices.used = now
		return o.choices, nil
	}
	options, err := loadOptions(ctx, o.store)
	if err != nil {
		return nil, err
	}
	o.choices = newOptionIndex(options)
	o.choices.used = now
	return o.choices, nil
}

// forgetOptions drops the index of options, which the guides or sources
// changed.
func (o *Own) forgetOptions() {
	o.choicesMu.Lock()
	o.choices = nil
	o.choicesMu.Unlock()
}

// Suggest returns, for each channel, the options whose names are close to
// its own, the closest first, at most limit of them (5 if limit is 0).
func (o *Own) Suggest(ctx context.Context, channels []SuggestFor, limit int) (map[int64][]Option, error) {
	if limit <= 0 || limit > maxSuggestions {
		limit = maxSuggestions
	}
	x, err := o.optionIndex(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[int64][]Option, len(channels))
	for _, c := range channels {
		out[c.ChannelID] = x.suggest(c, limit)
	}
	return out, nil
}

// Search returns the options whose names or guide IDs hold text, at most
// limit of them (50 if limit is 0).
func (o *Own) Search(ctx context.Context, text string, limit int) ([]Option, error) {
	if limit <= 0 || limit > maxFound {
		limit = maxFound
	}
	x, err := o.optionIndex(ctx)
	if err != nil {
		return nil, err
	}
	return x.search(text, limit), nil
}
