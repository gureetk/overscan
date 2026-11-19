// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sniff puts what a source sends into words fit for a log or a
// report: the kind of data that a body starts with, and text from the
// source without anything that looks like an address, which can hold
// credentials.
package sniff

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// lookBytes caps how much of a body Describe looks at.
const lookBytes = 4096

// Describe tells what a body that is neither MPEG-TS nor a playlist is,
// from its first bytes.
func Describe(head []byte) string {
	head = head[:min(len(head), lookBytes)]
	text := bytes.TrimSpace(head)
	switch {
	case len(head) == 0:
		return "nothing"
	case bytes.HasPrefix(head, []byte("ID3")):
		return "audio with an ID3 tag"
	case len(head) > 8 && string(head[4:8]) == "ftyp":
		return "MP4"
	case bytes.HasPrefix(head, []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "Matroska or WebM"
	case bytes.HasPrefix(head, []byte("FLV")):
		return "FLV"
	case len(text) > 0 && text[0] == '<':
		if m := titlePattern.FindSubmatch(head); m != nil {
			return "a web page titled " + strconv.Quote(Scrub(string(m[1])))
		}
		return "a web page"
	case isText(head):
		first, _, _ := strings.Cut(string(text), "\n")
		return "text starting " + strconv.Quote(Scrub(first))
	}
	return fmt.Sprintf("unknown data starting % x", head[:min(len(head), 16)])
}

var titlePattern = regexp.MustCompile(`(?is)<title[^>]*>([^<]*)</title>`)

// isText reports whether data looks like text.
func isText(data []byte) bool {
	for _, b := range data {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return true
}

// addressPattern matches what may be an address in text from a source.
var addressPattern = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://\S*|\S+@\S+|\S*[?&][a-z_]+=\S*`)

// Scrub makes text from a source fit to show: one line of at most 80
// printable characters, without anything that looks like an address.
func Scrub(s string) string {
	s = addressPattern.ReplaceAllString(s, "[address]")
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7F {
			return ' '
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len([]rune(s)) > 80 {
		s = string([]rune(s)[:79]) + "…"
	}
	return s
}
