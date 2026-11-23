// SPDX-License-Identifier: AGPL-3.0-or-later

// Package natural orders names as people do (D137): letters without
// regard to case, and each run of digits by the number it writes, so that
// "Channel 2" comes before "Channel 10".
package natural

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Compare returns -1, 0 or +1 as a comes before, with or after b. Names
// that differ only in case, or in the zeros before a number, are ordered
// by their text, so that the order is total.
func Compare(a, b string) int {
	if c := compare(a, b); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func compare(a, b string) int {
	for a != "" && b != "" {
		ra, na := utf8.DecodeRuneInString(a)
		rb, nb := utf8.DecodeRuneInString(b)
		if isDigit(ra) && isDigit(rb) {
			da, restA := digits(a)
			db, restB := digits(b)
			if c := compareNumbers(da, db); c != 0 {
				return c
			}
			a, b = restA, restB
			continue
		}
		la, lb := unicode.ToLower(ra), unicode.ToLower(rb)
		if la != lb {
			if la < lb {
				return -1
			}
			return 1
		}
		a, b = a[na:], b[nb:]
	}
	switch {
	case a == "" && b == "":
		return 0
	case a == "":
		return -1
	}
	return 1
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// digits splits off the run of ASCII digits at the start of s.
func digits(s string) (run, rest string) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i], s[i:]
}

// compareNumbers compares two runs of digits by the numbers they write,
// however long.
func compareNumbers(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	}
	return strings.Compare(a, b)
}
