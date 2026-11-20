// SPDX-License-Identifier: AGPL-3.0-or-later

package theme

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// The page's own colours, whose places a theme's take.
var (
	ground0 = parseRGB(Overscan.Background)
	text0   = parseRGB(Overscan.Text)
)

// accent0 is the default accent, teal.
const accent0 = "#3dd9c1"

var (
	ruleForm    = regexp.MustCompile(`([^{}]*)\{([^{}]*)\}`)
	commentForm = regexp.MustCompile(`(?s)/\*.*?\*/`)
	hexForm     = regexp.MustCompile(`#[0-9a-fA-F]{6}\b`)
	rgbaForm    = regexp.MustCompile(`rgba\(\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,`)
	radiusForm  = regexp.MustCompile(`(border(?:-[a-z]+)*-radius\s*:\s*)([^;}]+)`)
	lengthForm  = regexp.MustCompile(`(\d*\.?\d+)(rem|px|em)\b`)
	fontForm    = regexp.MustCompile(`'Instrument Sans',\s*`)
)

// axis maps the page's greys, which lie between its ground and its text,
// to a theme's: each keeps its place between the two, and the slight tint
// it has.
type axis struct{ ground, text rgb }

func mean(c rgb) float64 { return (c[0] + c[1] + c[2]) / 3 }

// grey reports whether c is one of the page's greys: near enough neutral,
// and between its ground and its text. Pure black and white, as shadows
// and highlights use, are not.
func grey(c rgb) bool {
	lo, hi := math.Min(c[0], math.Min(c[1], c[2])), math.Max(c[0], math.Max(c[1], c[2]))
	m := mean(c)
	return hi-lo <= 12 && m >= mean(ground0)-0.5 && m <= mean(text0)+0.5
}

// mapped returns where the grey c of the page lies on the axis.
func (a axis) mapped(c rgb) rgb {
	t := (mean(c) - mean(ground0)) / (mean(text0) - mean(ground0))
	var out rgb
	for i := range 3 {
		own := ground0[i] + (text0[i]-ground0[i])*t
		v := a.ground[i] + (a.text[i]-a.ground[i])*t + (c[i] - own)
		out[i] = math.Max(0, math.Min(255, math.Round(v)))
	}
	return out
}

func hexOf(c rgb) string {
	b := []byte{'#'}
	for _, v := range c {
		b = append(b, "0123456789abcdef"[int(v)>>4], "0123456789abcdef"[int(v)&15])
	}
	return string(b)
}

// recolour rewrites the colours of a rule's declarations.
func recolour(body string, a axis, accent string) string {
	body = hexForm.ReplaceAllStringFunc(body, func(h string) string {
		low := strings.ToLower(h)
		if low == accent0 && accent != "" {
			return accent
		}
		c := parseRGB(low)
		if !grey(c) {
			return h
		}
		if m := hexOf(a.mapped(c)); m != low {
			return m
		}
		return h
	})
	return rgbaForm.ReplaceAllStringFunc(body, func(s string) string {
		m := rgbaForm.FindStringSubmatch(s)
		var c rgb
		for i := range 3 {
			n, err := strconv.Atoi(m[1+i])
			if err != nil || n > 255 {
				return s
			}
			c[i] = float64(n)
		}
		if !grey(c) {
			return s
		}
		to := a.mapped(c)
		if to == c {
			return s
		}
		return "rgba(" + strconv.Itoa(int(to[0])) + ", " + strconv.Itoa(int(to[1])) + ", " + strconv.Itoa(int(to[2])) + ","
	})
}

// corners rewrites the radii of a rule's corners: halved for small ones,
// none for square ones. Circles, at 50%, stay.
func corners(body, how string) string {
	if how != "small" && how != "square" {
		return body
	}
	return radiusForm.ReplaceAllStringFunc(body, func(decl string) string {
		m := radiusForm.FindStringSubmatch(decl)
		return m[1] + lengthForm.ReplaceAllStringFunc(m[2], func(l string) string {
			if how == "square" {
				return "0"
			}
			n := lengthForm.FindStringSubmatch(l)
			v, err := strconv.ParseFloat(n[1], 64)
			if err != nil {
				return l
			}
			return strconv.FormatFloat(v/2, 'f', -1, 64) + n[2]
		})
	})
}

// Render returns the page's stylesheet, base, rewritten for the theme: its
// greys mixed from the theme's ground and text, the item in focus filled
// with the theme's focus colours, the default accent replaced, and its
// corners, font and brand as the theme says. Overscan's own theme returns
// base as it is.
func Render(base string, t Theme) string {
	focus, focusText := t.focusColours()
	plain := axis{ground: parseRGB(t.Background), text: parseRGB(t.Text)}
	inFocus := axis{ground: parseRGB(focusText), text: parseRGB(focus)}
	out := ruleForm.ReplaceAllStringFunc(base, func(rule string) string {
		m := ruleForm.FindStringSubmatch(rule)
		sel, body := m[1], m[2]
		bare := commentForm.ReplaceAllString(sel, "")
		a := plain
		if strings.Contains(bare, ".focus") {
			a = inFocus
		}
		body = recolour(body, a, t.Accent)
		if !strings.Contains(bare, "@font-face") {
			if t.Font == "Sans" {
				body = fontForm.ReplaceAllString(body, "")
			}
			body = corners(body, t.Corners)
		}
		return sel + "{" + body + "}"
	})
	if t.Brand != "" {
		// The brand passed Parse: letters, digits, spaces and a few marks,
		// none of which ends a string in a stylesheet.
		out += "\n/* The theme's brand, in place of the word beside the mark. */\n" +
			".brand .word {\n  display: none;\n}\n\n" +
			".brand:after {\n  content: \"" + t.Brand + "\";\n  font-size: 1.3125rem;\n  font-weight: 600;\n  letter-spacing: -0.03rem;\n}\n"
	}
	return out
}
