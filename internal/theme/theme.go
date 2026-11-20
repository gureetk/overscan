// SPDX-License-Identifier: AGPL-3.0-or-later

// Package theme reads theme files (D97) and turns them into stylesheets for
// Overscan's page. A theme is data, never code: a small JSON document of
// named values, which this package checks, and from which it rewrites the
// page's own stylesheet, changing its colours, its corners, its font and
// the name beside its mark. The page's browser has no CSS variables, so a
// theme's stylesheet is the page's whole stylesheet, rewritten.
package theme

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Version is the theme files' format, which a file names as
// "overscan_theme".
const Version = 1

// MaxFileBytes caps a theme file.
const MaxFileBytes = 16 << 10

// MaxName and MaxBrand cap a theme's name and the name it shows beside the
// mark, in characters.
const (
	MaxName  = 40
	MaxBrand = 24
)

// Fonts are the fonts a theme may name: the one the page ships, and the
// box's own.
var Fonts = []string{"Instrument Sans", "Sans"}

// Corners are the corners a theme may name.
var Corners = []string{"round", "small", "square"}

// Theme is what a theme file says. Colours are #rrggbb, in lower case.
type Theme struct {
	Name string `json:"name"`
	// Background is the colour of the page's ground, and Text that of its
	// text; the greys between, of panels, lines and quieter text, are
	// mixed from the two.
	Background string `json:"background"`
	Text       string `json:"text"`
	// Accent takes the place of the default accent, teal, which progress
	// bars and the guide's line for now show.
	Accent string `json:"accent,omitempty"`
	// Focus is the fill of the item in focus, and FocusText the text on
	// it; Text and Background if not given.
	Focus     string `json:"focus,omitempty"`
	FocusText string `json:"focus_text,omitempty"`
	Font      string `json:"font,omitempty"`    // one of Fonts; the first if not given
	Corners   string `json:"corners,omitempty"` // one of Corners; round if not given
	// Brand is the name shown beside the mark in place of Overscan's.
	Brand string `json:"brand,omitempty"`
}

// Overscan is Overscan's own theme: the page as it is.
var Overscan = Theme{Name: "Overscan", Background: "#0b0b0d", Text: "#f5f5f7"}

// file is a theme file as it is written.
type file struct {
	Version *int `json:"overscan_theme"`
	Theme
}

// Error is why a theme file does not check out, in a sentence for the
// administrator.
type Error struct{ Text string }

func (e *Error) Error() string { return e.Text }

func refuse(format string, args ...any) error {
	return &Error{fmt.Sprintf(format, args...)}
}

// colourForm is a colour as a theme gives it.
var colourForm = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// brandForm is what a brand may hold: letters, digits, spaces and a few
// marks, so that it is safe in a stylesheet's string.
var brandForm = regexp.MustCompile(`^[\p{L}\p{N} '&.,!?-]+$`)

// Parse reads and checks a theme file. It refuses, with an *Error, a file
// that is too large, is not JSON of this form, names something it does
// not know, or gives a value that cannot be one: so that what boxes show
// stays readable.
func Parse(data []byte) (Theme, error) {
	if len(data) > MaxFileBytes {
		return Theme{}, refuse("The file is larger than %d KB: a theme file is a few lines of text.", MaxFileBytes>>10)
	}
	if !utf8.Valid(data) {
		return Theme{}, refuse("The file is not text in UTF-8.")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f file
	if err := dec.Decode(&f); err != nil {
		var syntax *json.SyntaxError
		var typ *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntax):
			return Theme{}, refuse("The file is not JSON: %s at byte %d.", syntax.Error(), syntax.Offset)
		case errors.As(err, &typ):
			return Theme{}, refuse("%q must be text.", typ.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return Theme{}, refuse("A theme has no %s.", strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return Theme{}, refuse("The file is not a theme: %s.", err)
	}
	if dec.More() {
		return Theme{}, refuse("The file holds more than one theme.")
	}
	if f.Version == nil {
		return Theme{}, refuse("The file does not say \"overscan_theme\": %d, so it is not a theme file.", Version)
	}
	if *f.Version != Version {
		return Theme{}, refuse("The file is a theme of format %d; this Overscan reads format %d.", *f.Version, Version)
	}
	t := f.Theme
	t.Name = strings.TrimSpace(t.Name)
	if t.Name == "" || utf8.RuneCountInString(t.Name) > MaxName || strings.ContainsFunc(t.Name, unicode.IsControl) {
		return Theme{}, refuse("A theme's name has 1 to %d characters.", MaxName)
	}
	if strings.EqualFold(t.Name, Overscan.Name) {
		return Theme{}, refuse("%q is the name of Overscan's own theme: give the theme another.", Overscan.Name)
	}
	for _, c := range []struct {
		name     string
		value    *string
		required bool
	}{{"background", &t.Background, true}, {"text", &t.Text, true}, {"accent", &t.Accent, false},
		{"focus", &t.Focus, false}, {"focus_text", &t.FocusText, false}} {
		if *c.value == "" && !c.required {
			continue
		}
		v, ok := normalColour(*c.value)
		if !ok {
			return Theme{}, refuse("%q must be a colour such as #1f6feb, not %q.", c.name, truncate(*c.value, 20))
		}
		*c.value = v
	}
	if t.Font != "" && !contains(Fonts, t.Font) {
		return Theme{}, refuse("\"font\" must be one of %s.", list(Fonts))
	}
	if t.Corners != "" && !contains(Corners, t.Corners) {
		return Theme{}, refuse("\"corners\" must be one of %s.", list(Corners))
	}
	t.Brand = strings.TrimSpace(t.Brand)
	if t.Brand != "" && (utf8.RuneCountInString(t.Brand) > MaxBrand || !brandForm.MatchString(t.Brand)) {
		return Theme{}, refuse("\"brand\" has 1 to %d letters, digits, spaces and the marks ' & . , ! ? -.", MaxBrand)
	}
	// What the boxes show must stay readable.
	if r := contrast(t.Text, t.Background); r < 4.5 {
		return Theme{}, refuse("The text would be hard to read on the background: their contrast is %.1f to 1, and must be at least 4.5 to 1.", r)
	}
	focus, focusText := t.focusColours()
	if r := contrast(focusText, focus); r < 4.5 {
		return Theme{}, refuse("The item in focus would be hard to read: its text and fill have a contrast of %.1f to 1, and must have at least 4.5 to 1.", r)
	}
	if r := contrast(focus, t.Background); r < 1.5 {
		return Theme{}, refuse("The item in focus would hardly stand out from the background: their contrast is %.1f to 1, and must be at least 1.5 to 1.", r)
	}
	if t.Accent != "" {
		if r := contrast(t.Accent, t.Background); r < 2 {
			return Theme{}, refuse("The accent would hardly show on the background: their contrast is %.1f to 1, and must be at least 2 to 1.", r)
		}
	}
	return t, nil
}

// JSON returns the theme as a file, in the form Overscan keeps it.
func (t Theme) JSON() []byte {
	v := Version
	b, err := json.MarshalIndent(file{Version: &v, Theme: t}, "", "  ")
	if err != nil {
		panic(err) // strings only
	}
	return append(b, '\n')
}

// focusColours returns the fill of the item in focus and its text.
func (t Theme) focusColours() (string, string) {
	focus, text := t.Focus, t.FocusText
	if focus == "" {
		focus = t.Text
	}
	if text == "" {
		text = t.Background
	}
	return focus, text
}

// normalColour returns a colour given as #rgb or #rrggbb as #rrggbb in lower
// case.
func normalColour(s string) (string, bool) {
	if !colourForm.MatchString(s) {
		return "", false
	}
	s = strings.ToLower(s)
	if len(s) == 4 {
		s = "#" + strings.Repeat(s[1:2], 2) + strings.Repeat(s[2:3], 2) + strings.Repeat(s[3:4], 2)
	}
	return s, true
}

// rgb is a colour's channels.
type rgb [3]float64

func parseRGB(hex string) rgb {
	var c rgb
	for i := range 3 {
		n, _ := strconv.ParseUint(hex[1+2*i:3+2*i], 16, 8)
		c[i] = float64(n)
	}
	return c
}

// luminance is a colour's relative luminance, as WCAG defines it.
func luminance(c rgb) float64 {
	var l [3]float64
	for i, v := range c {
		v /= 255
		if v <= 0.03928 {
			l[i] = v / 12.92
		} else {
			l[i] = math.Pow((v+0.055)/1.055, 2.4)
		}
	}
	return 0.2126*l[0] + 0.7152*l[1] + 0.0722*l[2]
}

// contrast is the contrast ratio of two colours, as WCAG defines it.
func contrast(a, b string) float64 {
	la, lb := luminance(parseRGB(a)), luminance(parseRGB(b))
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// list writes choices for a sentence: "a", "b" or "c".
func list(choices []string) string {
	q := make([]string, len(choices))
	for i, c := range choices {
		q[i] = strconv.Quote(c)
	}
	if len(q) == 1 {
		return q[0]
	}
	return strings.Join(q[:len(q)-1], ", ") + " or " + q[len(q)-1]
}

// truncate shortens s to at most n characters.
func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
