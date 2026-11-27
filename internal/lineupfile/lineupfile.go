// SPDX-License-Identifier: AGPL-3.0-or-later

// Package lineupfile reads and writes the file that lineups are exported to
// and imported from (D112): one YAML file, with a version, that holds each
// lineup's groups that the administrator made or changed, its channels
// changed by hand, with their backups, and its rules, and since version 2
// whether it holds only what its groups take (D136), its groups' first
// numbers and the order of sources in its merge rules (D137). Sources are
// named,
// and channels known by their source and the key that their source gives
// them, which stays the same across refreshes. A file never holds a
// source's address, a login or anything else secret.
package lineupfile

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/lexer"
	"github.com/goccy/go-yaml/parser"
	"github.com/goccy/go-yaml/token"

	"overscan.gureet.ca/internal/rules"
	"overscan.gureet.ca/internal/streamprofile"
)

// Kind and Version name the file's format: Version is the latest that
// Overscan reads. A file of a later version is refused, so that nothing in
// it is dropped without a word. Version 2 adds a lineup's own_groups, a
// group's numbers_from, and a merge rule's sources and prefer: best;
// version 3 a channel's logo set by hand (D141), and version 4 a group's
// transcode (D147). A file is written as the
// earliest version that holds what it has, so that an Overscan that reads
// only that version imports it too.
const (
	Kind    = "overscan-lineups"
	Version = 4
)

// NoLogo is a channel's logo, set by hand, of a channel that has none.
const NoLogo = "none"

// TranscodeNever is a group's transcode that sends its channels as their
// sources send them (D147).
const TranscodeNever = "never"

// Limits on a file, as the admin has them for what it holds.
const (
	maxLogo        = 2048    // of a logo's address, as a rule's
	MaxBytes       = 2 << 20 // of a file read
	MaxLineups     = 20      // as a server has at most
	MaxBackups     = 5       // of a channel
	maxLineupName  = 50      // characters
	maxGroupName   = 100     // characters
	maxChannelName = 200     // characters
	maxKey         = 1000    // bytes of a source's name, a category's or a channel's key
	maxNumber      = 999_999
)

// File is a file of lineups.
type File struct {
	Kind    string   `yaml:"kind"`
	Version int      `yaml:"version"`
	Lineups []Lineup `yaml:"lineups"`
}

// Lineup is a lineup as a file holds it: what the administrator changed
// in it. What Overscan does by itself, such as a group for each category
// and the numbers of channels not pinned, it does again on import.
type Lineup struct {
	Name string `yaml:"name"`
	// OwnGroups is set for a lineup that holds only what its groups take,
	// and is left out for one that takes every category of every source
	// (version 2).
	OwnGroups bool         `yaml:"own_groups,omitempty"`
	Groups    []Group      `yaml:"groups,omitempty"`
	Channels  []Channel    `yaml:"channels,omitempty"`
	Rules     []rules.Rule `yaml:"rules,omitempty"`
}

// Group is a group that the administrator made or changed, with the
// categories of the sources that it holds.
type Group struct {
	Name   string `yaml:"name"`
	Hidden bool   `yaml:"hidden,omitempty"`
	Locked bool   `yaml:"locked,omitempty"`
	// NumbersFrom is its first number, 0 if it has none (version 2).
	NumbersFrom int `yaml:"numbers_from,omitempty"`
	// Transcode is what is transcoded of its channels (D147): TranscodeNever,
	// or the name of the profile that transcodes them always; "" for as
	// needed (version 4).
	Transcode  string     `yaml:"transcode,omitempty"`
	Categories []Category `yaml:"categories,omitempty"`
}

// Category is a source's live category, by name; "" names the source's
// channels without a category.
type Category struct {
	Source string `yaml:"source"`
	Name   string `yaml:"name"`
}

// Ref finds a channel: its source, by name, and the key that the source
// gives it.
type Ref struct {
	Source string `yaml:"source"`
	Key    string `yaml:"key"`
}

func (r Ref) String() string { return r.Source + " " + r.Key }

// Channel is a channel changed by hand, or given backups (D47, D55, D109).
type Channel struct {
	Ref `yaml:",inline"`
	// Title is the channel's name as its source gives it, for people who
	// read the file; an import does not use it.
	Title  string `yaml:"title,omitempty"`
	Number int    `yaml:"number,omitempty"` // pinned by hand, or 0
	Name   string `yaml:"name,omitempty"`   // given by hand, or ""
	Group  string `yaml:"group,omitempty"`  // chosen by hand, or ""
	// Hidden is set when the channel is hidden or shown by hand, and nil
	// when it follows its group.
	Hidden *bool `yaml:"hidden,omitempty"`
	Locked bool  `yaml:"locked,omitempty"`
	// Logo is its logo set by hand, an address, or NoLogo for none; ""
	// for the one that its rules, source and guide give it (version 3).
	Logo    string `yaml:"logo,omitempty"`
	Backups []Ref  `yaml:"backups,omitempty"`
}

// Error is what is wrong with a file, which its writer can put right.
type Error struct{ Text string }

func (e *Error) Error() string { return e.Text }

func bad(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Needs returns the version of the file format that f needs: the latest
// version that added something that it holds, else 1.
func (f File) Needs() int {
	for _, l := range f.Lineups {
		for _, g := range l.Groups {
			if g.Transcode != "" {
				return 4
			}
		}
	}
	for _, l := range f.Lineups {
		for _, c := range l.Channels {
			if c.Logo != "" {
				return 3
			}
		}
	}
	for _, l := range f.Lineups {
		if l.OwnGroups {
			return 2
		}
		for _, g := range l.Groups {
			if g.NumbersFrom != 0 {
				return 2
			}
		}
		for _, r := range l.Rules {
			if len(r.SourceNames) > 0 || len(r.Sources) > 0 || r.Prefer == rules.Best {
				return 2
			}
		}
	}
	return 1
}

// Marshal writes a file, with a comment on top that says what it is, as
// the version that it needs.
func Marshal(f File, header string) ([]byte, error) {
	f.Kind, f.Version = Kind, f.Needs()
	out, err := yaml.MarshalWithOptions(f, yaml.Indent(2), yaml.IndentSequence(true))
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	for line := range strings.SplitSeq(strings.TrimSpace(header), "\n") {
		b.WriteString(strings.TrimRight("# "+line, " ") + "\n")
	}
	b.Write(out)
	return b.Bytes(), nil
}

// Parse reads a file, and checks what it holds as the admin checks what
// it is given: an error says what is wrong, and where.
func Parse(data []byte) (f File, err error) {
	// A mistake in the YAML library that a file finds is that file's
	// mistake too.
	defer func() {
		if p := recover(); p != nil {
			f, err = File{}, bad("The file is not YAML that Overscan reads.")
		}
	}()
	if len(data) > MaxBytes {
		return File{}, bad("The file is larger than %d MB, which an import takes at most: split it into files of fewer lineups.", MaxBytes>>20)
	}
	if !utf8.Valid(data) {
		return File{}, bad("The file is not text.")
	}
	// YAML's lexer and parser take a few hundred bytes of memory for each
	// token, and the parser time and memory that grow with the square of
	// how deeply a file nests, so a file is measured before each reads it.
	if roughTokens(data) > maxTokens {
		return File{}, bad("The file holds more than a lineup file does: split it into files of fewer lineups.")
	}
	tokens := lexer.Tokenize(string(data))
	if nesting(tokens) > maxNesting {
		return File{}, bad("The file nests more deeply than a lineup file does.")
	}
	doc, err := parser.Parse(tokens, 0)
	if err != nil {
		return File{}, bad("The file is not YAML that Overscan reads: %s", firstLine(err))
	}
	switch {
	case len(doc.Docs) == 0 || doc.Docs[0].Body == nil:
		return File{}, bad("The file is empty.")
	case len(doc.Docs) > 1:
		return File{}, bad("The file holds %d YAML documents; a lineup file is one.", len(doc.Docs))
	}
	body := doc.Docs[0].Body
	// An exported file has no anchors, aliases or merges, and refusing them
	// keeps a small file from growing into a huge one as it is read. Nor
	// does it have tags, which the YAML library reads less carefully.
	var shared error
	ast.Walk(visitor(func(n ast.Node) {
		switch n.Type() {
		case ast.AnchorType, ast.AliasType, ast.MergeKeyType:
			if shared == nil {
				shared = bad("The file uses YAML's anchors, aliases or merges, which a lineup file never has.")
			}
		case ast.TagType:
			if shared == nil {
				shared = bad("The file uses YAML's tags, such as !!str, which a lineup file never has.")
			}
		}
	}), body)
	if shared != nil {
		return File{}, shared
	}
	var head struct {
		Kind    string `yaml:"kind"`
		Version int    `yaml:"version"`
	}
	if err := yaml.NodeToValue(body, &head); err != nil || head.Kind != Kind {
		return File{}, bad("This is not a file of Overscan's lineups: it does not start with kind: %s.", Kind)
	}
	switch {
	case head.Version > Version:
		return File{}, bad("The file is of version %d of Overscan's lineup files, which a later Overscan writes. Upgrade Overscan to import it.", head.Version)
	case head.Version < 1:
		return File{}, bad("The file does not say its version, as a lineup file does.")
	}
	if err := yaml.NodeToValue(body, &f, yaml.DisallowUnknownField()); err != nil {
		return File{}, bad("The file holds something that a lineup file does not: %s", firstLine(err))
	}
	if need := f.Needs(); need > f.Version {
		what := "own_groups, numbers_from, or a merge's sources or prefer: best"
		switch need {
		case 3:
			what = "a channel's logo"
		case 4:
			what = "a group's transcode"
		}
		return File{}, bad("The file says version %d, but holds %s, which version %d of lineup files added: change its version to %d.",
			f.Version, what, need, need)
	}
	return f, f.Check()
}

// maxNesting is how deeply a file may nest: an exported one nests 7 or 8
// levels deep.
const maxNesting = 32

// maxTokens caps roughTokens of a file: an exported file of 2 MB has some
// 250,000 tokens, which it counts as fewer than 400,000.
const maxTokens = 400_000

// Fits reports whether an import takes a file of this size: one of at
// most MaxBytes, which holds at most some 14,000 channels.
func Fits(data []byte) bool {
	return len(data) <= MaxBytes && roughTokens(data) <= maxTokens
}

// roughTokens counts what can be a token for YAML's lexer, from above: each
// of YAML's marks, and each run of other characters between them and
// spaces.
func roughTokens(data []byte) int {
	n, word := 0, false
	for _, c := range data {
		switch c {
		case ' ', '\t', '\r', '\n':
			word = false
		case '[', ']', '{', '}', ',', ':', '-', '?', '#', '&', '*', '!', '|', '>', '\'', '"', '%', '@', '`':
			n++
			word = false
		default:
			if !word {
				n++
				word = true
			}
		}
	}
	return n
}

// nesting measures how deeply YAML's tokens nest: flow collections by
// their brackets, and blocks by the columns at which their lines and their
// sequences' entries start.
func nesting(tokens token.Tokens) int {
	var blocks []int // the columns of the blocks open
	flow, deepest, line := 0, 0, 0
	for _, t := range tokens {
		if t.Type == token.CommentType {
			continue
		}
		switch t.Type {
		case token.SequenceStartType, token.MappingStartType:
			flow++
		case token.SequenceEndType, token.MappingEndType:
			flow = max(flow-1, 0)
		}
		first := t.Position.Line != line
		line = t.Position.Line
		if flow == 0 && (first || t.Type == token.SequenceEntryType) {
			col := t.Position.Column
			for len(blocks) > 0 && blocks[len(blocks)-1] > col {
				blocks = blocks[:len(blocks)-1]
			}
			if len(blocks) == 0 || blocks[len(blocks)-1] < col {
				blocks = append(blocks, col)
			}
		}
		deepest = max(deepest, len(blocks)+flow)
	}
	return deepest
}

// visitor walks a YAML document's nodes.
type visitor func(ast.Node)

func (v visitor) Visit(n ast.Node) ast.Visitor {
	v(n)
	return v
}

// firstLine returns the first line of an error, which the YAML library
// follows with the text around the mistake.
func firstLine(err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	return strings.TrimSpace(line)
}

// Check checks what a file holds. Its rules are checked as the admin
// checks them, apart from their sources, which only the server that
// imports them knows.
func (f File) Check() error {
	switch {
	case len(f.Lineups) == 0:
		return bad("The file holds no lineup.")
	case len(f.Lineups) > MaxLineups:
		return bad("The file holds %d lineups; a server has at most %d.", len(f.Lineups), MaxLineups)
	}
	for i, l := range f.Lineups {
		if err := checkName(l.Name, "lineup", maxLineupName); err != nil {
			return bad("Lineup %d: %v", i+1, err)
		}
		if err := l.check(); err != nil {
			return bad("The lineup %s: %v", l.Name, err)
		}
	}
	return nil
}

func (l Lineup) check() error {
	groups := map[string]bool{}
	for _, g := range l.Groups {
		if err := checkName(g.Name, "group", maxGroupName); err != nil {
			return err
		}
		if groups[g.Name] {
			return bad("the group %s is there twice.", g.Name)
		}
		groups[g.Name] = true
		if g.NumbersFrom < 0 || g.NumbersFrom > maxNumber {
			return bad("the group %s: a first number is from 1 to %d.", g.Name, maxNumber)
		}
		if t := g.Transcode; t != "" && t != TranscodeNever {
			if p, fixed := streamprofile.Find(t); fixed && !p.Transcodes() || !fixed && streamprofile.CheckName(t) != nil {
				return bad("the group %s: transcode is never, or the name of a profile that transcodes.", g.Name)
			}
		}
		for _, c := range g.Categories {
			if err := checkKey(c.Source, "a category's source"); err != nil {
				return bad("the group %s: %v", g.Name, err)
			}
			if c.Name == "" {
				continue // the channels without a category
			}
			if err := checkKey(c.Name, "a category's name"); err != nil {
				return bad("the group %s: %v", g.Name, err)
			}
		}
	}
	channels := map[Ref]bool{}
	numbers := map[int]Ref{}
	for _, c := range l.Channels {
		if err := c.Ref.check(); err != nil {
			return err
		}
		if channels[c.Ref] {
			return bad("the channel %s is there twice.", c.Ref)
		}
		channels[c.Ref] = true
		if c.Number != 0 {
			if c.Number < 1 || c.Number > maxNumber {
				return bad("the channel %s: a number is from 1 to %d.", c.Ref, maxNumber)
			}
			if other, ok := numbers[c.Number]; ok {
				return bad("the channels %s and %s both have the number %d.", other, c.Ref, c.Number)
			}
			numbers[c.Number] = c.Ref
		}
		if c.Name != "" {
			if err := checkName(c.Name, "channel", maxChannelName); err != nil {
				return bad("the channel %s: %v", c.Ref, err)
			}
		}
		if c.Group != "" {
			if err := checkName(c.Group, "group", maxGroupName); err != nil {
				return bad("the channel %s: %v", c.Ref, err)
			}
		}
		if c.Logo != "" && c.Logo != NoLogo {
			if u, err := url.Parse(c.Logo); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(c.Logo) > maxLogo {
				return bad("the channel %s: a logo is an address that starts with http:// or https://, or %s.", c.Ref, NoLogo)
			}
		}
		if len(c.Title) > maxKey {
			return bad("the channel %s: its title is too long.", c.Ref)
		}
		if len(c.Backups) > MaxBackups {
			return bad("the channel %s has %d backups; a channel has at most %d.", c.Ref, len(c.Backups), MaxBackups)
		}
		seen := map[Ref]bool{c.Ref: true}
		for _, b := range c.Backups {
			if err := b.check(); err != nil {
				return bad("a backup of %s: %v", c.Ref, err)
			}
			if seen[b] {
				return bad("the channel %s has %s as a backup twice, or as its own.", c.Ref, b)
			}
			seen[b] = true
		}
	}
	if len(l.Rules) > rules.MaxRules {
		return bad("it has %d rules; a lineup has at most %d.", len(l.Rules), rules.MaxRules)
	}
	for i, r := range l.Rules {
		if r.Match.Source != 0 {
			return bad("rule %d names its source by an ID; a lineup file names it.", i+1)
		}
		if r.Match.SourceName != "" {
			if err := checkKey(r.Match.SourceName, "a source"); err != nil {
				return bad("rule %d: %v", i+1, err)
			}
		}
		if len(r.Sources) > 0 {
			return bad("rule %d names its sources by their IDs; a lineup file names them.", i+1)
		}
		for _, name := range r.SourceNames {
			if err := checkKey(name, "a source"); err != nil {
				return bad("rule %d: %v", i+1, err)
			}
		}
		if _, err := rules.Check(r); err != nil {
			return bad("rule %d: %v", i+1, err)
		}
	}
	return nil
}

func (r Ref) check() error {
	if err := checkKey(r.Source, "a channel's source"); err != nil {
		return err
	}
	return checkKey(r.Key, "a channel's key")
}

// checkName checks a name that the file gives, as the admin checks one.
func checkName(name, what string, limit int) error {
	switch {
	case strings.TrimSpace(name) == "":
		return bad("a %s needs a name.", what)
	case name != strings.TrimSpace(name):
		return bad("the name of the %s %q starts or ends with a space.", what, name)
	case utf8.RuneCountInString(name) > limit:
		return bad("a %s's name can have at most %d characters.", what, limit)
	case strings.ContainsFunc(name, func(r rune) bool { return !unicode.IsPrint(r) }):
		return bad("a %s's name cannot hold control characters.", what)
	}
	return nil
}

// checkKey checks a name or a key that only finds what it names.
func checkKey(s, what string) error {
	switch {
	case s == "":
		return bad("%s is missing.", what)
	case len(s) > maxKey:
		return bad("%s is too long.", what)
	case strings.ContainsFunc(s, unicode.IsControl):
		return bad("%s cannot hold control characters.", what)
	}
	return nil
}

// IsError reports whether err is a mistake in a file.
func IsError(err error) bool {
	var e *Error
	return errors.As(err, &e)
}
