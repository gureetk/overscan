// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Servers send the same field as a number, a string, true or false, or null
// (docs/protocol/xtream-api-notes.md, section 8). These types read any of
// them, and treat what they cannot read as missing rather than failing the
// whole answer.

// number is a whole number: what cannot be read is 0.
type number int64

func (n *number) UnmarshalJSON(b []byte) error {
	*n = 0
	var t text
	_ = t.UnmarshalJSON(b)
	s := strings.TrimSpace(string(t))
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		*n = number(v)
	} else if f, err := strconv.ParseFloat(s, 64); err == nil && math.Abs(f) < 1<<53 {
		*n = number(f)
	}
	return nil
}

// decimal is a number that may have a fraction, such as a rating: what
// cannot be read is 0. A comma may stand for the point.
type decimal float64

func (d *decimal) UnmarshalJSON(b []byte) error {
	*d = 0
	var t text
	_ = t.UnmarshalJSON(b)
	s := strings.TrimSpace(string(t))
	if !strings.Contains(s, ".") {
		s = strings.Replace(s, ",", ".", 1)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		*d = decimal(f)
	}
	return nil
}

// text is a string, or the literal text of a number, true or false; null,
// an object or an array is "".
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	*t = ""
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
	case b[0] == '"':
		var s string
		if json.Unmarshal(b, &s) == nil {
			*t = text(s)
		}
	case b[0] == '{', b[0] == '[', bytes.Equal(b, []byte("null")):
	default:
		*t = text(b)
	}
	return nil
}

// flag is a yes or no: 1, true and their strings are yes, anything else no.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	var t text
	_ = t.UnmarshalJSON(b)
	switch strings.ToLower(strings.TrimSpace(string(t))) {
	case "1", "true", "yes":
		*f = true
	default:
		*f = false
	}
	return nil
}

// textList is a list of strings, which a server may send as one string.
type textList []string

func (l *textList) UnmarshalJSON(b []byte) error {
	*l = nil
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '[' {
		var items []text
		if json.Unmarshal(b, &items) == nil {
			for _, it := range items {
				*l = append(*l, string(it))
			}
		}
		return nil
	}
	var t text
	_ = t.UnmarshalJSON(b)
	if t != "" {
		*l = textList{string(t)}
	}
	return nil
}
