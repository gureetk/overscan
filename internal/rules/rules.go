// SPDX-License-Identifier: AGPL-3.0-or-later

// Package rules holds a lineup's rules (D107): what each matches and what it
// does, checked when it is written and applied to a channel when the store
// runs a lineup's rules. It knows nothing of the store: a rule is matched
// against a Channel, which the store fills in.
package rules

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Actions.
const (
	Include  = "include"  // shows the channels, even in a hidden group
	Exclude  = "exclude"  // hides them
	Rename   = "rename"   // names them anew, or replaces text in their names
	Regroup  = "regroup"  // moves them to a group, made if the lineup has none of that name
	Renumber = "renumber" // numbers them from a number, in their order
	Logo     = "logo"     // gives them a logo from an address
)

// Actions lists the actions in the order that the admin offers them.
var Actions = []string{Include, Exclude, Rename, Regroup, Renumber, Logo}

// Limits.
const (
	MaxRules   = 200 // in a lineup
	maxPattern = 200 // characters
	maxText    = 200 // characters, of a name, a group or a replacement
	maxLogo    = 2048
	MaxNumber  = 999_999 // the highest number that a rule numbers from
)

// Rule is one rule of a lineup.
type Rule struct {
	Match  Match  `json:"match" yaml:"match"`
	Action string `json:"action" yaml:"action"`
	// Off keeps a rule that does nothing until it is on again.
	Off bool `json:"off,omitempty" yaml:"off,omitempty"`

	// For rename: a new name, or text to replace in the name and what
	// replaces it. Text between slashes is a regular expression, and $1
	// and so on in With are its groups.
	Name    string `json:"name,omitempty" yaml:"name,omitempty"`
	Replace string `json:"replace,omitempty" yaml:"replace,omitempty"`
	With    string `json:"with,omitempty" yaml:"with,omitempty"`
	// For regroup: the group's name.
	Group string `json:"group,omitempty" yaml:"group,omitempty"`
	// For renumber: the first number.
	From int `json:"from,omitempty" yaml:"from,omitempty"`
	// For logo: the logo's address, http or https.
	Logo string `json:"logo,omitempty" yaml:"logo,omitempty"`
}

// Match says which channels a rule acts on: those that meet every
// condition it has. A rule without any matches every channel.
type Match struct {
	// Source is the ID of the channel's source; 0 for any. Exports name
	// the source instead.
	Source int64 `json:"source,omitempty" yaml:"-"`
	// Group is a pattern for the channel's category in its source, Name
	// one for its name in its source, and Guide one for its guide ID.
	Group string `json:"group,omitempty" yaml:"group,omitempty"`
	Name  string `json:"name,omitempty" yaml:"name,omitempty"`
	Guide string `json:"guide,omitempty" yaml:"guide,omitempty"`
	// Quality is a word that the channel's name holds, such as HD or 4K.
	Quality string `json:"quality,omitempty" yaml:"quality,omitempty"`
}

// QualityTags are the words a rule can match a channel's quality by.
var QualityTags = []string{"SD", "HD", "FHD", "UHD", "4K", "8K", "HEVC", "H264", "H265", "1080p", "720p", "50fps", "60fps"}

// Channel is what a rule is matched against: a channel as its source
// lists it.
type Channel struct {
	Source   int64
	Category string // "" if none
	Name     string
	Guide    string // its guide ID, "" if none
}

// Error is a mistake in a rule, which its writer can put right.
type Error struct{ Text string }

func (e *Error) Error() string { return e.Text }

func bad(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Check returns the rule, tidied, or the first mistake in it. A checked
// rule compiles.
func Check(r Rule) (Rule, error) {
	r.Action = strings.ToLower(strings.TrimSpace(r.Action))
	r.Match.Group = strings.TrimSpace(r.Match.Group)
	r.Match.Name = strings.TrimSpace(r.Match.Name)
	r.Match.Guide = strings.TrimSpace(r.Match.Guide)
	r.Match.Quality = strings.TrimSpace(r.Match.Quality)
	r.Name, r.Group, r.Logo = strings.TrimSpace(r.Name), strings.TrimSpace(r.Group), strings.TrimSpace(r.Logo)
	if r.Match.Source < 0 {
		return Rule{}, bad("Choose a source, or any.")
	}
	for _, p := range []struct{ what, text string }{{"group", r.Match.Group}, {"name", r.Match.Name}, {"guide ID", r.Match.Guide}} {
		if err := checkText(p.what+" pattern", p.text, maxPattern); err != nil {
			return Rule{}, err
		}
		if _, err := compile(p.text); err != nil {
			return Rule{}, bad("The %s pattern is not a regular expression that Overscan reads: %v.", p.what, err)
		}
	}
	if r.Match.Quality != "" {
		tag, ok := qualityTag(r.Match.Quality)
		if !ok {
			return Rule{}, bad("A quality is one of %s.", strings.Join(QualityTags, ", "))
		}
		r.Match.Quality = tag
	}
	keep := Rule{Match: r.Match, Action: r.Action, Off: r.Off}
	switch r.Action {
	case Include, Exclude:
	case Rename:
		switch {
		case r.Name != "" && r.Replace != "":
			return Rule{}, bad("A rename gives a new name or replaces text, not both.")
		case r.Name != "":
			if err := checkText("new name", r.Name, maxText); err != nil {
				return Rule{}, err
			}
			keep.Name = r.Name
		case r.Replace != "":
			if err := checkText("text to replace", r.Replace, maxPattern); err != nil {
				return Rule{}, err
			}
			if err := checkText("replacement", r.With, maxText); err != nil {
				return Rule{}, err
			}
			if _, err := replacer(r.Replace, r.With); err != nil {
				return Rule{}, bad("The text to replace is not a regular expression that Overscan reads: %v.", err)
			}
			keep.Replace, keep.With = r.Replace, r.With
		default:
			return Rule{}, bad("A rename needs a new name, or text to replace.")
		}
	case Regroup:
		if r.Group == "" {
			return Rule{}, bad("Name the group to move the channels to.")
		}
		if err := checkText("group's name", r.Group, 100); err != nil {
			return Rule{}, err
		}
		keep.Group = r.Group
	case Renumber:
		if r.From < 1 || r.From > MaxNumber {
			return Rule{}, bad("Number from 1 to %d.", MaxNumber)
		}
		keep.From = r.From
	case Logo:
		u, err := url.Parse(r.Logo)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(r.Logo) > maxLogo {
			return Rule{}, bad("A logo's address starts with http:// or https://.")
		}
		keep.Logo = r.Logo
	default:
		return Rule{}, bad("Choose what the rule does: %s.", strings.Join(Actions, ", "))
	}
	return keep, nil
}

// checkText checks a text that a rule holds.
func checkText(what, text string, limit int) error {
	switch {
	case !utf8.ValidString(text):
		return bad("The %s must be text.", what)
	case utf8.RuneCountInString(text) > limit:
		return bad("The %s can have at most %d characters.", what, limit)
	case strings.ContainsFunc(text, func(r rune) bool { return !unicode.IsPrint(r) }):
		return bad("The %s cannot hold control characters.", what)
	}
	return nil
}

// Matcher is a rule's match, compiled.
type Matcher struct {
	source             int64
	group, name, guide *regexp.Regexp
	quality            string
}

// Compile returns a checked rule's match, compiled.
func Compile(m Match) (Matcher, error) {
	var out Matcher
	var err error
	out.source = m.Source
	if out.group, err = compile(m.Group); err != nil {
		return Matcher{}, err
	}
	if out.name, err = compile(m.Name); err != nil {
		return Matcher{}, err
	}
	if out.guide, err = compile(m.Guide); err != nil {
		return Matcher{}, err
	}
	out.quality = strings.ToLower(m.Quality)
	return out, nil
}

// Matches reports whether a channel meets every condition.
func (m Matcher) Matches(c Channel) bool {
	switch {
	case m.source != 0 && c.Source != m.source:
		return false
	case m.group != nil && !m.group.MatchString(c.Category):
		return false
	case m.name != nil && !m.name.MatchString(c.Name):
		return false
	case m.guide != nil && !m.guide.MatchString(c.Guide):
		return false
	case m.quality != "" && !hasWord(c.Name, m.quality):
		return false
	}
	return true
}

// compile turns a pattern into a regular expression: "" matches anything
// (nil); text between slashes is a regular expression, which matches
// anywhere in the text; other text is matched whole, with * for any
// characters and ? for one. Either way, case does not count.
func compile(pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	if re, ok := slashed(pattern); ok {
		return regexp.Compile("(?i)" + re)
	}
	var b strings.Builder
	b.WriteString("(?is)^")
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
}

// slashed returns the regular expression of a pattern written between
// slashes.
func slashed(pattern string) (string, bool) {
	if len(pattern) >= 3 && strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") {
		return pattern[1 : len(pattern)-1], true
	}
	return "", false
}

// qualityTag returns a quality tag as QualityTags spells it.
func qualityTag(tag string) (string, bool) {
	for _, t := range QualityTags {
		if strings.EqualFold(t, tag) {
			return t, true
		}
	}
	return "", false
}

// hasWord reports whether a name holds a word, whatever its case: words are
// runs of letters and digits.
func hasWord(name, word string) bool {
	for _, w := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if strings.EqualFold(w, word) {
			return true
		}
	}
	return false
}

// Renamer is a rename rule, compiled: it gives a channel its new name from
// the name it has so far.
type Renamer func(name string) string

// CompileRename returns a checked rename rule's renamer.
func CompileRename(r Rule) (Renamer, error) {
	if r.Name != "" {
		return func(string) string { return r.Name }, nil
	}
	return replacer(r.Replace, r.With)
}

// replacer replaces text, or what a regular expression between slashes
// matches, whatever its case, and tidies the spaces of what is left; a
// name that would be empty stays as it was.
func replacer(replace, with string) (Renamer, error) {
	var re *regexp.Regexp
	var err error
	if expr, ok := slashed(replace); ok {
		re, err = regexp.Compile("(?i)" + expr)
	} else {
		re, err = regexp.Compile("(?i)" + regexp.QuoteMeta(replace))
		with = strings.ReplaceAll(with, "$", "$$")
	}
	if err != nil {
		return nil, err
	}
	return func(name string) string {
		out := strings.Join(strings.Fields(re.ReplaceAllString(name, with)), " ")
		if out == "" {
			return name
		}
		return out
	}, nil
}

// ErrTooMany is the error for a lineup with as many rules as it may have.
var ErrTooMany = errors.New("rules: a lineup has as many rules as it may")
