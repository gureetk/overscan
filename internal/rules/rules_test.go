// SPDX-License-Identifier: AGPL-3.0-or-later

package rules

import (
	"errors"
	"strings"
	"testing"
)

func TestCheck(t *testing.T) {
	for _, tc := range []struct {
		rule Rule
		err  string // a part of the mistake, "" for none
	}{
		{Rule{Action: "exclude"}, ""},
		{Rule{Action: " Include ", Match: Match{Group: "News*"}}, ""},
		{Rule{Action: "rename", Name: "Alpha"}, ""},
		{Rule{Action: "rename", Replace: "ZZ: ", With: ""}, ""},
		{Rule{Action: "rename", Replace: `/^(\w+) HD$/`, With: "$1"}, ""},
		{Rule{Action: "rename"}, "needs a new name"},
		{Rule{Action: "rename", Name: "a", Replace: "b"}, "not both"},
		{Rule{Action: "rename", Replace: "/(/"}, "regular expression"},
		{Rule{Action: "regroup", Group: "Kids"}, ""},
		{Rule{Action: "regroup"}, "Name the group"},
		{Rule{Action: "renumber", From: 100}, ""},
		{Rule{Action: "renumber"}, "Number from 1"},
		{Rule{Action: "renumber", From: MaxNumber + 1}, "Number from 1"},
		{Rule{Action: "logo", Logo: "https://logos.example/a.png"}, ""},
		{Rule{Action: "logo", Logo: "file:///etc/passwd"}, "http://"},
		{Rule{Action: "logo"}, "http://"},
		{Rule{Action: "delete"}, "Choose what the rule does"},
		{Rule{Action: "exclude", Match: Match{Name: "/[/"}}, "name pattern"},
		{Rule{Action: "exclude", Match: Match{Quality: "hd"}}, ""},
		{Rule{Action: "exclude", Match: Match{Quality: "shiny"}}, "A quality is one of"},
		{Rule{Action: "exclude", Match: Match{Name: "a\x00b"}}, "control characters"},
		{Rule{Action: "exclude", Match: Match{Name: strings.Repeat("a", 201)}}, "at most 200"},
		{Rule{Action: "exclude", Match: Match{Source: -1}}, "source"},
		{Rule{Action: "guide", Channel: "Alpha"}, ""},
		{Rule{Action: "guide", Replace: "+1"}, ""},
		{Rule{Action: "guide", Replace: `/\s*\+1$/`, With: ""}, ""},
		{Rule{Action: "guide"}, "Name the channel"},
		{Rule{Action: "guide", Channel: "Alpha", Replace: "+1"}, "not both"},
		{Rule{Action: "guide", Replace: "/(/"}, "regular expression"},
		{Rule{Action: "timeshift", Minutes: 60}, ""},
		{Rule{Action: "timeshift", Minutes: -MaxShift}, ""},
		{Rule{Action: "timeshift"}, "Shift the guide"},
		{Rule{Action: "timeshift", Minutes: MaxShift + 1}, "Shift the guide"},
		{Rule{Action: "merge", Copies: "guide"}, ""},
		{Rule{Action: "merge", Copies: " Name ", Prefer: "fhd"}, ""},
		{Rule{Action: "merge", Copies: "name", Replace: `/\s*\(backup\)$/`}, ""},
		{Rule{Action: "merge"}, "Say what copies have the same"},
		{Rule{Action: "merge", Copies: "colour"}, "Say what copies have the same"},
		{Rule{Action: "merge", Copies: "guide", Replace: "HD"}, "found by their names"},
		{Rule{Action: "merge", Copies: "name", Replace: "/(/"}, "regular expression"},
		{Rule{Action: "merge", Copies: "guide", Prefer: "shiny"}, "A quality to show first"},
	} {
		got, err := Check(tc.rule)
		var mistake *Error
		switch {
		case tc.err == "" && err != nil:
			t.Errorf("Check(%+v): %v", tc.rule, err)
		case tc.err != "" && (err == nil || !errors.As(err, &mistake) || !strings.Contains(err.Error(), tc.err)):
			t.Errorf("Check(%+v) = %v, want a mistake with %q", tc.rule, err, tc.err)
		case err == nil && got.Action != strings.ToLower(strings.TrimSpace(tc.rule.Action)):
			t.Errorf("Check(%+v) = %+v", tc.rule, got)
		}
	}
	// What an action does not use is dropped, and a quality spelled as
	// QualityTags spells it.
	got, err := Check(Rule{Action: "exclude", Name: "x", Group: "y", From: 3, Channel: "z", Minutes: 5, Match: Match{Quality: "4k"}})
	if err != nil || got.Name != "" || got.Group != "" || got.From != 0 || got.Channel != "" || got.Minutes != 0 ||
		got.Match.Quality != "4K" {
		t.Errorf("tidied: %+v %v", got, err)
	}
}

func TestGuideName(t *testing.T) {
	for _, tc := range []struct {
		rule       Rule
		name, want string
	}{
		{Rule{Action: Guide, Channel: "Alpha"}, "Alpha +1", "Alpha"},
		{Rule{Action: Guide, Replace: "+1"}, "Alpha +1", "Alpha"},
		{Rule{Action: Guide, Replace: `/\s*\(east\)$/`, With: " (West)"}, "Alpha (East)", "Alpha (West)"},
		{Rule{Action: Guide, Replace: "+1"}, "Alpha", "Alpha"},
	} {
		r, err := Check(tc.rule)
		if err != nil {
			t.Fatal(err)
		}
		fn, err := CompileGuide(r)
		if err != nil {
			t.Fatal(err)
		}
		if got := fn(tc.name); got != tc.want {
			t.Errorf("%+v on %q = %q, want %q", tc.rule, tc.name, got, tc.want)
		}
	}
}

func TestMatch(t *testing.T) {
	ch := Channel{Source: 2, Category: "ZZ | News", Name: "ZZ: Alpha News HD", Guide: "alpha.zz"}
	for _, tc := range []struct {
		match Match
		want  bool
	}{
		{Match{}, true},
		{Match{Source: 2}, true},
		{Match{Source: 3}, false},
		{Match{Group: "zz | news"}, true},
		{Match{Group: "ZZ*"}, true},
		{Match{Group: "News"}, false},
		{Match{Group: "*News"}, true},
		{Match{Name: "*alpha*"}, true},
		{Match{Name: "Alpha*"}, false},
		{Match{Name: "ZZ: Alpha News ??"}, true},
		{Match{Name: "/alpha news/"}, true},
		{Match{Name: "/^alpha/"}, false},
		{Match{Guide: "*.zz"}, true},
		{Match{Guide: "*.de"}, false},
		{Match{Quality: "HD"}, true},
		{Match{Quality: "FHD"}, false},
		{Match{Source: 2, Group: "ZZ*", Quality: "HD"}, true},
		{Match{Source: 2, Group: "ZZ*", Quality: "SD"}, false},
		{Match{Name: "a.b"}, false}, // a dot is a dot
	} {
		m, err := Compile(tc.match)
		if err != nil {
			t.Fatal(err)
		}
		if got := m.Matches(ch); got != tc.want {
			t.Errorf("%+v matches %+v: %v, want %v", tc.match, ch, got, tc.want)
		}
	}
	// An empty guide ID matches only a pattern that takes it.
	m, _ := Compile(Match{Guide: "*"})
	if !m.Matches(Channel{}) {
		t.Error("* does not match an empty guide ID")
	}
}

func TestRename(t *testing.T) {
	for _, tc := range []struct {
		rule       Rule
		name, want string
	}{
		{Rule{Action: Rename, Name: "Alpha"}, "ZZ: Alpha HD", "Alpha"},
		{Rule{Action: Rename, Replace: "zz: "}, "ZZ: Alpha HD", "Alpha HD"},
		{Rule{Action: Rename, Replace: " HD", With: ""}, "ZZ: Alpha HD", "ZZ: Alpha"},
		{Rule{Action: Rename, Replace: "HD", With: "$1"}, "Alpha HD", "Alpha $1"},
		{Rule{Action: Rename, Replace: `/^(\w+): (.*)$/`, With: "$2 ($1)"}, "ZZ: Alpha HD", "Alpha HD (ZZ)"},
		{Rule{Action: Rename, Replace: "/.*/", With: ""}, "Alpha", "Alpha"}, // never empty
		{Rule{Action: Rename, Replace: "a", With: "  "}, "Banana", "B n n"},
	} {
		r, err := Check(tc.rule)
		if err != nil {
			t.Fatal(err)
		}
		fn, err := CompileRename(r)
		if err != nil {
			t.Fatal(err)
		}
		if got := fn(tc.name); got != tc.want {
			t.Errorf("%+v on %q = %q, want %q", tc.rule, tc.name, got, tc.want)
		}
	}
}

func FuzzPattern(f *testing.F) {
	for _, s := range []string{"", "*", "a?c", "/a(b)/", "/(/", "ZZ*", "[x]"} {
		f.Add(s, "ZZ: Alpha")
	}
	f.Fuzz(func(t *testing.T, pattern, text string) {
		re, err := compile(pattern)
		if err != nil {
			if _, ok := slashed(pattern); !ok {
				t.Errorf("a pattern without slashes fails: %q: %v", pattern, err)
			}
			return
		}
		if re != nil {
			re.MatchString(text)
		}
		if _, ok := slashed(pattern); !ok && re != nil && !strings.ContainsAny(pattern, "*?") && !re.MatchString(pattern) {
			t.Errorf("%q does not match itself", pattern)
		}
	})
}

// A merge rule finds copies by guide ID, or by name without quality words,
// changed as it says, and prefers those of a quality (D113).
func TestMerge(t *testing.T) {
	byGuide, err := CompileMerge(Rule{Action: Merge, Copies: ByGuide})
	if err != nil {
		t.Fatal(err)
	}
	if a, b := byGuide.Key(Channel{Guide: " granite.one "}), byGuide.Key(Channel{Guide: "Granite.One"}); a != b || a == "" {
		t.Errorf("by guide: %q and %q", a, b)
	}
	if k := byGuide.Key(Channel{Name: "Granite One"}); k != "" {
		t.Errorf("a channel without a guide ID has the key %q", k)
	}
	byName, err := CompileMerge(Rule{Action: Merge, Copies: ByName, Replace: `/\s*\(backup\)$/`, Prefer: "FHD"})
	if err != nil {
		t.Fatal(err)
	}
	want := "granite one"
	for _, name := range []string{"Granite One HD", "granite one (FHD)", "GRANITE-ONE", "Granite One 4K (Backup)", "Granite One"} {
		if k := byName.Key(Channel{Name: name}); k != want {
			t.Errorf("by name: %q has the key %q, want %q", name, k, want)
		}
	}
	for _, name := range []string{"Granite One 2", "Granite Two HD", "Granite One Backup"} {
		if k := byName.Key(Channel{Name: name}); k == want {
			t.Errorf("by name: %q is a copy, and should not be", name)
		}
	}
	if !byName.Prefers(Channel{Name: "Granite One FHD"}) || byName.Prefers(Channel{Name: "Granite One HD"}) {
		t.Error("prefers the wrong copies")
	}
	got, err := Check(Rule{Action: "merge", Copies: "Name", Prefer: "fhd", Name: "dropped", Replace: "x", With: "y"})
	if err != nil || got.Copies != ByName || got.Prefer != "FHD" || got.Name != "" || got.Replace != "x" || got.With != "y" {
		t.Errorf("tidied: %+v %v", got, err)
	}
}
