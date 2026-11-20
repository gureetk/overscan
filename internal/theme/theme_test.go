// SPDX-License-Identifier: AGPL-3.0-or-later

package theme

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"overscan.gureet.ca/web"
)

// evergreen is a fictional theme.
const evergreen = `{
  "overscan_theme": 1,
  "name": "Evergreen",
  "background": "#0E1512",
  "text": "#eef4f0",
  "accent": "#5bd69c",
  "focus": "#f4d35e",
  "focus_text": "#1a1405",
  "font": "Sans",
  "corners": "small",
  "brand": "Hill House"
}`

func TestParse(t *testing.T) {
	th, err := Parse([]byte(evergreen))
	if err != nil {
		t.Fatal(err)
	}
	want := Theme{Name: "Evergreen", Background: "#0e1512", Text: "#eef4f0", Accent: "#5bd69c", Focus: "#f4d35e",
		FocusText: "#1a1405", Font: "Sans", Corners: "small", Brand: "Hill House"}
	if th != want {
		t.Errorf("got %+v", th)
	}
	// What Overscan keeps reads back as the same theme.
	again, err := Parse(th.JSON())
	if err != nil || again != th {
		t.Errorf("again %+v, %v", again, err)
	}
	if short, err := Parse([]byte(`{"overscan_theme": 1, "name": " Plain ", "background": "#000", "text": "#FFF"}`)); err != nil ||
		short != (Theme{Name: "Plain", Background: "#000000", Text: "#ffffff"}) {
		t.Errorf("short colours: %+v, %v", short, err)
	}
}

func TestParseRefuses(t *testing.T) {
	ok := `"overscan_theme": 1, "name": "Plain", "background": "#101010", "text": "#f0f0f0"`
	for in, want := range map[string]string{
		`not json`: "is not JSON",
		`{"name": "Plain", "background": "#101010", "text": "#f0f0f0"}`:                                                                 `does not say "overscan_theme": 1`,
		`{"overscan_theme": 2, "name": "Plain", "background": "#101010", "text": "#f0f0f0"}`:                                            "format 2",
		`{` + ok + `, "logo": "x.png"}`:                                                                                                 `A theme has no "logo"`,
		`{` + ok + `, "font": "Comic"}`:                                                                                                 `"font" must be one of "Instrument Sans" or "Sans"`,
		`{` + ok + `, "corners": "wavy"}`:                                                                                               `"corners" must be one of`,
		`{` + ok + `, "accent": "teal"}`:                                                                                                `"accent" must be a colour`,
		`{` + ok + `, "brand": "<b>x</b>"}`:                                                                                             `"brand" has 1 to 24`,
		`{` + ok + `, "brand": "a\"; } body { color: red"}`:                                                                             `"brand" has 1 to 24`,
		`{"overscan_theme": 1, "name": "", "background": "#101010", "text": "#f0f0f0"}`:                                                 "name has 1 to 40",
		`{"overscan_theme": 1, "name": "overscan", "background": "#101010", "text": "#f0f0f0"}`:                                         "Overscan's own theme",
		`{"overscan_theme": 1, "name": "Plain", "background": "#101010", "text": "#303030"}`:                                            "hard to read on the background",
		`{` + ok + `, "focus": "#f0f0f0", "focus_text": "#e0e0e0"}`:                                                                     "item in focus would be hard to read",
		`{` + ok + `, "focus": "#141414", "focus_text": "#f0f0f0"}`:                                                                     "hardly stand out",
		`{` + ok + `, "accent": "#121212"}`:                                                                                             "accent would hardly show",
		`{` + ok + `}{` + ok + `}`:                                                                                                      "more than one theme",
		`{"overscan_theme": 1, "name": 5, "background": "#101010", "text": "#f0f0f0"}`:                                                  `"name" must be text`,
		`{"overscan_theme": 1, "name": "Plain", "background": "#101010", "text": "#f0f0f0", ` + strings.Repeat(" ", MaxFileBytes) + `}`: "larger than",
	} {
		_, err := Parse([]byte(in))
		var e *Error
		if !errors.As(err, &e) || !strings.Contains(e.Text, want) {
			t.Errorf("%.60q: %v, want %q", in, err, want)
		}
	}
}

// basePage returns the page's own stylesheet, as the build made it.
func basePage(t *testing.T) string {
	t.Helper()
	names, err := fs.Glob(web.MAG, "mag/dist/app.*.css")
	if err != nil || len(names) != 1 {
		t.Fatalf("stylesheets %v, %v", names, err)
	}
	b, err := fs.ReadFile(web.MAG, names[0])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Overscan's own colours give back the page's own stylesheet, byte for
// byte: every grey keeps its place and its tint.
func TestRenderOverscan(t *testing.T) {
	base := basePage(t)
	if got := Render(base, Theme{Name: "Same", Background: Overscan.Background, Text: Overscan.Text}); got != base {
		for i := range min(len(got), len(base)) {
			if got[i] != base[i] {
				t.Fatalf("differs at byte %d: %q, want %q", i, got[max(0, i-40):min(len(got), i+40)], base[max(0, i-40):min(len(base), i+40)])
			}
		}
		t.Fatalf("lengths %d, want %d", len(got), len(base))
	}
}

func TestRender(t *testing.T) {
	base := basePage(t)
	th, err := Parse([]byte(evergreen))
	if err != nil {
		t.Fatal(err)
	}
	got := Render(base, th)
	for _, want := range []string{
		"background: #0e1512;",                      // the ground
		"color: #eef4f0;",                           // the text
		".big-tile.focus {\n  background: #f4d35e;", // the item in focus
		"#5bd69c",                            // the accent
		`content: "Hill House";`,             // the brand
		"font-family: Roboto, 'DejaVu Sans'", // the box's own font
		"border-radius: 0.28125rem",          // small corners
		"border-radius: 50%",                 // circles stay
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the stylesheet lacks %q", want)
		}
	}
	for _, gone := range []string{"#0b0b0d", "#f5f5f7", "#3dd9c1", "rgba(11, 11, 13,"} {
		if strings.Contains(got, gone) {
			t.Errorf("the stylesheet still has %q", gone)
		}
	}
	// Every rule survives, and the fonts still load.
	rules := regexp.MustCompile(`\{`)
	if n, m := len(rules.FindAllString(got, -1)), len(rules.FindAllString(base, -1)); n != m+2 {
		t.Errorf("%d rules, want %d", n, m+2)
	}
	if strings.Count(got, "font-family: 'Instrument Sans';") != 4 {
		t.Error("the font faces changed")
	}
	// The key colours stay: they are the remote's.
	for _, keep := range []string{"#e5484d", "#3dd68c", "#ffd60a", "#3b82f6"} {
		if strings.Contains(base, keep) && !strings.Contains(got, keep) {
			t.Errorf("the key colour %s went", keep)
		}
	}

	square := Render(base, Theme{Name: "Square", Background: "#101010", Text: "#f0f0f0", Corners: "square"})
	if strings.Contains(square, "border-radius: 0.5rem") || !strings.Contains(square, "border-radius: 0;") {
		t.Error("square corners are not square")
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(evergreen))
	f.Add([]byte(`{"overscan_theme": 1}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		th, err := Parse(data)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("an error that is not a refusal: %v", err)
			}
			return
		}
		// A theme that checks out reads back the same, and its brand
		// cannot end a string.
		if again, err := Parse(th.JSON()); err != nil || again != th {
			t.Fatalf("again %+v, %v", again, err)
		}
		if strings.ContainsAny(th.Brand, "\"\\\n{}<>;") {
			t.Fatalf("brand %q", th.Brand)
		}
	})
}

// The guide's examples check out, and the guide shows the first as it is.
func TestTheGuidesExamples(t *testing.T) {
	guide, err := os.ReadFile("../../docs/themes.md")
	if err != nil {
		t.Fatal(err)
	}
	names, err := filepath.Glob("../../docs/themes/*.json")
	if err != nil || len(names) != 2 {
		t.Fatalf("examples %q, %v", names, err)
	}
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		th, err := Parse(data)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !bytes.Equal(th.JSON(), data) {
			t.Errorf("%s is not in the form Overscan keeps:\n%s", name, th.JSON())
		}
		if !strings.Contains(string(guide), "(themes/"+filepath.Base(name)+")") {
			t.Errorf("the guide does not link %s", filepath.Base(name))
		}
		if filepath.Base(name) == "evergreen.json" && !strings.Contains(string(guide), "```json\n"+string(data)+"```") {
			t.Errorf("the guide does not show %s as it is", name)
		}
	}
}
