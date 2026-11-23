// SPDX-License-Identifier: AGPL-3.0-or-later

package lineupfile

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"overscan.gureet.ca/internal/rules"
)

func sample() File {
	hidden, shown := true, false
	return File{Lineups: []Lineup{{
		Name: "Kids",
		Groups: []Group{
			{Name: "Cartoons", Locked: true, Categories: []Category{{Source: "main", Name: "Kids: Cartoons"}}},
			{Name: "Late", Hidden: true},
		},
		Channels: []Channel{
			{Ref: Ref{"main", "id:granite.one"}, Title: "Granite One HD", Number: 101, Name: "Granite One", Group: "Cartoons",
				Hidden: &shown, Backups: []Ref{{"one", "xtream:1001"}, {"two", "name:granite one|kids #2"}}},
			{Ref: Ref{"one", "xtream:7"}, Hidden: &hidden, Locked: true},
		},
		Rules: []rules.Rule{
			{Match: rules.Match{SourceName: "main", Name: "*Backup*"}, Action: rules.Exclude},
			{Match: rules.Match{Group: "/^news/", Quality: "HD"}, Action: rules.Rename, Replace: " HD", With: ""},
			{Match: rules.Match{Name: "Harbour +1"}, Action: rules.Guide, Channel: "Harbour", Off: true},
			{Match: rules.Match{Name: "Harbour +1"}, Action: rules.Shift, Minutes: 60},
		},
	}}}
}

func TestRoundTrip(t *testing.T) {
	out, err := Marshal(sample(), "Overscan's lineups.\nImport adds them as new lineups.")
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	for _, want := range []string{"# Overscan's lineups.\n# Import adds them as new lineups.\n", "kind: overscan-lineups\n", "version: 1\n",
		"source: main", "key: id:granite.one", "hidden: false", "number: 101"} {
		if !strings.Contains(text, want) {
			t.Errorf("the file lacks %q:\n%s", want, text)
		}
	}
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("%v in\n%s", err, text)
	}
	want := sample()
	want.Kind, want.Version = Kind, Version
	if !reflect.DeepEqual(got, want) {
		t.Errorf("read back\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseRefuses(t *testing.T) {
	head := "kind: overscan-lineups\nversion: 1\n"
	for name, c := range map[string]struct{ text, says string }{
		"empty":         {"", "empty"},
		"not a file":    {"lineups: []\n", "not a file of Overscan's lineups"},
		"later":         {"kind: overscan-lineups\nversion: 2\nlineups: []\n", "Upgrade Overscan"},
		"no version":    {"kind: overscan-lineups\nlineups: []\n", "version"},
		"unknown field": {head + "lineups:\n  - name: Kids\n    colour: red\n", "does not"},
		"no lineups":    {head + "lineups: []\n", "no lineup"},
		"anchor":        {head + "lineups:\n  - &a\n    name: Kids\n  - *a\n", "anchors"},
		"merge":         {head + "lineups:\n  - name: Kids\n    groups:\n      - <<: {name: X}\n", "anchors, aliases or merges"},
		"tag":           {head + "lineups:\n  - name: !!str Kids\n", "tags"},
		"empty tag":     {head + "lineups:\n  - name: A\n    channels:\n      ! #0\n", "tags"},
		"two documents": {head + "lineups:\n  - name: Kids\n---\n" + head, "2 YAML documents"},
		"duplicate key": {head + "lineups:\n  - name: Kids\n    name: Lounge\n", "Overscan reads"},
		"bad name":      {head + "lineups:\n  - name: \"Kids\\u0007\"\n", "control characters"},
		"same group": {head + "lineups:\n  - name: Kids\n    groups:\n      - name: News\n      - name: News\n",
			"the group News is there twice"},
		"same channel": {head + "lineups:\n  - name: Kids\n    channels:\n      - {source: main, key: a}\n      - {source: main, key: a}\n",
			"the channel main a is there twice"},
		"same number": {head + "lineups:\n  - name: Kids\n    channels:\n      - {source: main, key: a, number: 5}\n      - {source: main, key: b, number: 5}\n",
			"both have the number 5"},
		"own backup": {head + "lineups:\n  - name: Kids\n    channels:\n      - {source: main, key: a, backups: [{source: main, key: a}]}\n",
			"as its own"},
		"no key":   {head + "lineups:\n  - name: Kids\n    channels:\n      - {source: main}\n", "key is missing"},
		"bad rule": {head + "lineups:\n  - name: Kids\n    rules:\n      - {match: {name: \"/(/\"}, action: exclude}\n", "rule 1"},
		"rule source by ID": {head + "lineups:\n  - name: Kids\n    rules:\n      - {match: {source: 3}, action: exclude}\n",
			""}, // a number is a name here, which the import looks for
		"too large": {head + strings.Repeat("#", MaxBytes), "larger than"},
		"too dense": {head + "lineups: [" + strings.Repeat(",", maxTokens) + "]\n", "more than a lineup file"},
		"too deep":  {head + "lineups:\n" + strings.Repeat("- ", maxNesting+1) + "x\n", "nests more deeply"},
		"deep flow": {head + "lineups: " + strings.Repeat("[", maxNesting) + strings.Repeat("]", maxNesting) + "\n", "nests more deeply"},
		"stairs": {head + "lineups:\n" + func() string {
			var b strings.Builder
			for i := range maxNesting + 1 {
				b.WriteString(strings.Repeat(" ", i) + "a:\n")
			}
			return b.String()
		}(), "nests more deeply"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(c.text))
			if c.says == "" {
				if err != nil {
					t.Errorf("refused: %v", err)
				}
				return
			}
			if err == nil || !IsError(err) || !strings.Contains(err.Error(), c.says) {
				t.Errorf("got %v, want an error that says %q", err, c.says)
			}
		})
	}
}

// Every field of a rule reaches the file and comes back, with its source
// by name.
func TestRuleFields(t *testing.T) {
	var r rules.Rule
	var fill func(v reflect.Value)
	fill = func(v reflect.Value) {
		for i := range v.NumField() {
			switch f := v.Field(i); f.Kind() {
			case reflect.Struct:
				fill(f)
			case reflect.String:
				f.SetString("x" + strconv.Itoa(i))
			case reflect.Int, reflect.Int64:
				f.SetInt(int64(i + 1))
			case reflect.Bool:
				f.SetBool(true)
			default:
				t.Fatalf("a rule has a field of kind %v", f.Kind())
			}
		}
	}
	fill(reflect.ValueOf(&r).Elem())
	r.Match.Source = 0 // a file names the source instead
	out, err := Marshal(File{Lineups: []Lineup{{Name: "Kids", Rules: []rules.Rule{r}}}}, "")
	if err != nil {
		t.Fatal(err)
	}
	var back File
	if err := yaml.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if len(back.Lineups) != 1 || len(back.Lineups[0].Rules) != 1 || !reflect.DeepEqual(back.Lineups[0].Rules[0], r) {
		t.Errorf("a rule's fields do not all reach the file and come back:\n%s", out)
	}
}

// Whatever Parse reads, it either refuses with an Error, or reads a file
// that Marshal writes and Parse reads back the same.
func FuzzParse(f *testing.F) {
	out, err := Marshal(sample(), "A seed.")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(out)
	f.Add([]byte("kind: overscan-lineups\nversion: 1\nlineups:\n  - name: a\n    channels:\n      - {source: s, key: k, hidden: true}\n"))
	f.Add([]byte("kind: overscan-lineups\nversion: 1\nlineups:\n  - &a {name: a}\n  - *a\n"))
	f.Add([]byte("kind: overscan-lineups\nversion: 1\nlineups: [{name: \"~\", rules: [{match: {name: \"/a+/\"}, action: rename, replace: x, with: \"null\"}]}]\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		got, err := Parse(data)
		if err != nil {
			if !IsError(err) {
				t.Fatalf("an error that is not a file's: %v", err)
			}
			return
		}
		first, err := Marshal(got, "")
		if err != nil {
			t.Fatal(err)
		}
		again, err := Parse(first)
		if err != nil {
			t.Fatalf("a file that was read does not read again: %v\n%s", err, first)
		}
		second, err := Marshal(again, "")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Fatalf("a file reads back differently:\n%s\n---\n%s", first, second)
		}
	})
}
