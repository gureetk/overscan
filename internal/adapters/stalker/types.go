// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Portals send numbers as strings, an empty list as false, and the same
// field in more than one type (protocol notes, section 1). These types read
// any of them, and treat what they cannot read as missing rather than
// failing the whole answer.

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

// number is a whole number: what cannot be read is 0. A number with a
// fraction is cut to its whole part.
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

// list is a list of T that a portal may send as false, null or an object
// when it is empty; what is not a list is empty. An item that cannot be
// read is left out.
type list[T any] []T

func (l *list[T]) UnmarshalJSON(b []byte) error {
	*l = nil
	b = bytes.TrimSpace(b)
	if len(b) == 0 || b[0] != '[' {
		return nil
	}
	var raw []json.RawMessage
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	for _, r := range raw {
		var v T
		if json.Unmarshal(r, &v) == nil {
			*l = append(*l, v)
		}
	}
	return nil
}

// numbers is a list of whole numbers, as an inline series lists its
// episodes: numbers or their strings. What cannot be read is left out.
type numbers []int

func (n *numbers) UnmarshalJSON(b []byte) error {
	*n = nil
	var items list[number]
	_ = items.UnmarshalJSON(b)
	for _, v := range items {
		if v > 0 && v < 100_000 {
			*n = append(*n, int(v))
		}
	}
	return nil
}
