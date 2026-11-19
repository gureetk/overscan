// SPDX-License-Identifier: AGPL-3.0-or-later

// Package m3u reads extended M3U playlists, the format IPTV providers and
// tools publish: an #EXTM3U header, then for each stream an #EXTINF line with
// attributes and a title, followed by the stream's address.
package m3u

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxLineLen is the longest line Parse accepts, in bytes.
const MaxLineLen = 64 << 10

// ErrNotPlaylist is returned for input that does not start with #EXTM3U,
// such as an error page served in place of a playlist.
var ErrNotPlaylist = errors.New("m3u: not an extended M3U playlist")

// Header holds the attributes of the #EXTM3U line, such as url-tvg.
type Header struct {
	Attrs map[string]string
}

// Entry is one stream of a playlist.
type Entry struct {
	// Title is the text after the last comma of the #EXTINF line.
	Title string
	// Attrs are the #EXTINF attributes, with lower-case names: tvg-id,
	// tvg-name, tvg-logo, tvg-chno, group-title, catchup-days and so on.
	Attrs map[string]string
	// Group is group-title, or else the #EXTGRP line before the address.
	Group string
	// URL is the stream's address, as written.
	URL string
	// Line is the line number of the #EXTINF line, for messages.
	Line int
}

// Parse reads a playlist from r and calls fn for each entry, in order. A
// stream address without an #EXTINF line before it is skipped. Parse stops
// at the first error fn returns, and returns it.
func Parse(r io.Reader, fn func(Entry) error) (Header, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxLineLen)

	var header Header
	var pending *Entry
	n := 0
	sawHeader := false
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, "\ufeff")
		}
		if line == "" {
			continue
		}
		if !sawHeader {
			if !hasTag(line, "#EXTM3U") {
				return Header{}, ErrNotPlaylist
			}
			_, header.Attrs = parseAttrs(line[len("#EXTM3U"):])
			sawHeader = true
			continue
		}
		switch {
		case hasTag(line, "#EXTINF:"):
			title, attrs := parseAttrs(line[len("#EXTINF:"):])
			pending = &Entry{Title: title, Attrs: attrs, Group: attrs["group-title"], Line: n}
		case hasTag(line, "#EXTGRP:"):
			if pending != nil && pending.Group == "" {
				pending.Group = strings.TrimSpace(line[len("#EXTGRP:"):])
			}
		case strings.HasPrefix(line, "#"):
			// Other tags (#EXTVLCOPT, #KODIPROP) and comments.
		default:
			if pending == nil {
				continue
			}
			pending.URL = line
			if err := fn(*pending); err != nil {
				return header, err
			}
			pending = nil
		}
	}
	if err := sc.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return header, fmt.Errorf("m3u: line %d is longer than %d bytes", n+1, MaxLineLen)
		}
		return header, fmt.Errorf("m3u: %w", err)
	}
	if !sawHeader {
		return Header{}, ErrNotPlaylist
	}
	return header, nil
}

// hasTag reports whether line starts with tag, ignoring case.
func hasTag(line, tag string) bool {
	return len(line) >= len(tag) && strings.EqualFold(line[:len(tag)], tag)
}

// parseAttrs parses what follows "#EXTINF:" (or "#EXTM3U"): a duration, then
// name=value attributes with values in double quotes or bare, then a comma
// and the title. Commas inside quotes belong to the value.
func parseAttrs(s string) (title string, attrs map[string]string) {
	attrs = map[string]string{}
	i := 0
	// The duration, such as -1 or 0, up to the first space or comma.
	for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != ',' {
		i++
	}
	for i < len(s) {
		switch s[i] {
		case ' ', '\t':
			i++
			continue
		case ',':
			return strings.TrimSpace(s[i+1:]), attrs
		}
		// A name, up to '=', a space or a comma.
		start := i
		for i < len(s) && s[i] != '=' && s[i] != ' ' && s[i] != '\t' && s[i] != ',' {
			i++
		}
		name := strings.ToLower(s[start:i])
		if i >= len(s) || s[i] != '=' {
			continue // a bare word: not an attribute
		}
		i++ // '='
		var value string
		if i < len(s) && s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				value, i = s[i+1:], len(s) // unterminated: the rest of the line
			} else {
				value, i = s[i+1:i+1+end], i+2+end
			}
		} else {
			start := i
			for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != ',' {
				i++
			}
			value = s[start:i]
		}
		if name != "" {
			attrs[name] = strings.TrimSpace(value)
		}
	}
	return "", attrs
}
