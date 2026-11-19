// SPDX-License-Identifier: AGPL-3.0-or-later

// Package xmltv reads XMLTV guides as a stream, a channel or a programme at
// a time, so that a guide of any size takes little memory. It reads what
// Overscan shows: channels with their names, and programmes with their
// times, title, subtitle, description and first category. The format is
// the XMLTV project's (xmltv.dtd); guides in the wild bend it, so the
// reader is lenient where it can be and skips what it cannot read.
package xmltv

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits on what one element may hold. Longer text is cut.
const (
	MaxIDLen    = 200  // bytes of a channel ID
	MaxNameLen  = 200  // bytes of a name, title, subtitle or category
	MaxDescLen  = 1000 // bytes of a description
	maxNames    = 8    // display names kept for a channel
	maxElements = 64   // nested elements skipped inside one programme
)

// Channel is a channel of the guide.
type Channel struct {
	ID    string
	Names []string // its display names, in order
}

// Programme is one broadcast on a channel of the guide.
type Programme struct {
	Channel     string // the channel's ID
	Start, Stop time.Time
	Title       string
	SubTitle    string
	Desc        string
	Category    string // the first one, "" if none
}

// Handler receives what Parse reads. Returning an error stops the parse
// with that error.
type Handler struct {
	Channel   func(Channel) error
	Programme func(Programme) error
}

// Open returns a reader of a guide's XML: r as it is, or decompressed if it
// is gzip, which guides often are, whatever their address says.
func Open(r io.Reader) (io.Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	magic, err := br.Peek(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if bytes.Equal(magic, []byte{0x1f, 0x8b}) {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("the guide's gzip is not valid: %w", err)
		}
		zr.Multistream(true)
		return zr, nil
	}
	return br, nil
}

// ErrNotXMLTV is returned for a document that is not an XMLTV guide.
var ErrNotXMLTV = errors.New("the guide is not XMLTV")

// Parse reads an XMLTV guide from r, which Open has unpacked, and calls h
// for each channel and each programme with a channel, a title and a start.
// A programme without a stop, or whose stop is not after its start, ends
// when the next programme on its channel starts; Parse cannot know that,
// so it passes Stop as the zero time and the caller decides. It returns how
// many programmes it skipped because they could not be read.
func Parse(r io.Reader, h Handler) (skipped int, err error) {
	d := xml.NewDecoder(r)
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	sawTV := false
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			if !sawTV {
				return skipped, ErrNotXMLTV
			}
			return skipped, nil
		}
		if err != nil {
			if !sawTV {
				return skipped, ErrNotXMLTV
			}
			return skipped, fmt.Errorf("the guide's XML breaks off: %w", err)
		}
		start, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch start.Name.Local {
		case "tv":
			sawTV = true
		case "channel":
			if !sawTV {
				return skipped, ErrNotXMLTV
			}
			c, err := readChannel(d, start)
			if err != nil {
				return skipped, err
			}
			if c.ID != "" && h.Channel != nil {
				if err := h.Channel(c); err != nil {
					return skipped, err
				}
			}
		case "programme":
			if !sawTV {
				return skipped, ErrNotXMLTV
			}
			p, ok, err := readProgramme(d, start)
			if err != nil {
				return skipped, err
			}
			if !ok {
				skipped++
				continue
			}
			if h.Programme != nil {
				if err := h.Programme(p); err != nil {
					return skipped, err
				}
			}
		default:
			if !sawTV {
				return skipped, ErrNotXMLTV
			}
			if err := d.Skip(); err != nil {
				return skipped, fmt.Errorf("the guide's XML breaks off: %w", err)
			}
		}
	}
}

func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

func readChannel(d *xml.Decoder, start xml.StartElement) (Channel, error) {
	c := Channel{ID: Clean(attr(start, "id"), MaxIDLen)}
	err := children(d, func(e xml.StartElement) error {
		if e.Name.Local != "display-name" {
			return d.Skip()
		}
		text, err := elementText(d)
		if err != nil {
			return err
		}
		if name := Clean(text, MaxNameLen); name != "" && len(c.Names) < maxNames {
			c.Names = append(c.Names, name)
		}
		return nil
	})
	return c, err
}

// readProgramme reads a programme, and reports whether it has what a guide
// needs: a channel, a start and a title.
func readProgramme(d *xml.Decoder, start xml.StartElement) (Programme, bool, error) {
	p := Programme{Channel: Clean(attr(start, "channel"), MaxIDLen)}
	startAt, okStart := ParseTime(attr(start, "start"))
	stopAt, okStop := ParseTime(attr(start, "stop"))
	p.Start = startAt
	if okStop && stopAt.After(startAt) {
		p.Stop = stopAt
	}
	elements := 0
	err := children(d, func(e xml.StartElement) error {
		if elements++; elements > maxElements {
			return d.Skip()
		}
		var dst *string
		limit := MaxNameLen
		switch e.Name.Local {
		case "title":
			dst = &p.Title
		case "sub-title":
			dst = &p.SubTitle
		case "desc":
			dst, limit = &p.Desc, MaxDescLen
		case "category":
			dst = &p.Category
		default:
			return d.Skip()
		}
		text, err := elementText(d)
		if err != nil {
			return err
		}
		if *dst == "" {
			*dst = Clean(text, limit)
		}
		return nil
	})
	if err != nil {
		return p, false, err
	}
	return p, p.Channel != "" && okStart && p.Title != "", nil
}

// children calls fn for each child element of the element just started,
// until its end. fn must consume the child, by reading it or skipping it.
func children(d *xml.Decoder, fn func(xml.StartElement) error) error {
	for {
		tok, err := d.Token()
		if err != nil {
			return fmt.Errorf("the guide's XML breaks off: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if err := fn(t); err != nil {
				return err
			}
		case xml.EndElement:
			return nil
		}
	}
}

// elementText returns the text of the element just started, its children's
// included, up to its end.
func elementText(d *xml.Decoder) (string, error) {
	var b strings.Builder
	depth := 1
	for depth > 0 {
		tok, err := d.Token()
		if err != nil {
			return "", fmt.Errorf("the guide's XML breaks off: %w", err)
		}
		switch t := tok.(type) {
		case xml.CharData:
			if b.Len() < 4*MaxDescLen {
				b.Write(t)
			}
		case xml.StartElement:
			depth++
		case xml.EndElement:
			depth--
		}
	}
	return b.String(), nil
}

// Clean makes text fit to show and store: valid UTF-8 on one line, without
// control characters, spaces collapsed, cut to at most n bytes.
func Clean(s string, n int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r < ' ' || r == 0x7f || r == 0xa0
	}), " ")
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
}

// ParseTime reads an XMLTV time: YYYYMMDDhhmmss, or a shorter start of it
// down to the minute, then an optional zone as an offset such as +0100 or
// -05:30, or UTC or GMT. Without a zone, the time is UTC. A named zone
// other than UTC and GMT is not read.
func ParseTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	digits := 0
	for digits < len(s) && digits < 14 && s[digits] >= '0' && s[digits] <= '9' {
		digits++
	}
	if digits != 12 && digits != 14 {
		return time.Time{}, false
	}
	layout := "200601021504"
	if digits == 14 {
		layout = "20060102150405"
	}
	t, err := time.Parse(layout, s[:digits])
	if err != nil {
		return time.Time{}, false
	}
	zone := strings.TrimSpace(s[digits:])
	switch {
	case zone == "", strings.EqualFold(zone, "UTC"), strings.EqualFold(zone, "GMT"), zone == "Z":
		return t, true
	case len(zone) >= 5 && (zone[0] == '+' || zone[0] == '-'):
		hhmm := strings.Replace(zone[1:], ":", "", 1)
		if len(hhmm) != 4 || strings.Trim(hhmm, "0123456789") != "" {
			return time.Time{}, false
		}
		h := int(hhmm[0]-'0')*10 + int(hhmm[1]-'0')
		m := int(hhmm[2]-'0')*10 + int(hhmm[3]-'0')
		if h > 14 || m > 59 {
			return time.Time{}, false
		}
		offset := time.Duration(h)*time.Hour + time.Duration(m)*time.Minute
		if zone[0] == '+' {
			return t.Add(-offset), true
		}
		return t.Add(offset), true
	}
	return time.Time{}, false
}

// charsetReader reads the encodings other than UTF-8 that guides declare
// often enough to matter: ISO-8859-1 and US-ASCII.
func charsetReader(charset string, r io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1":
		return &latin1Reader{r: r}, nil
	case "us-ascii", "ascii", "utf8":
		return r, nil
	}
	return nil, fmt.Errorf("the guide is in %q, which Overscan cannot read", Clean(charset, 40))
}

// latin1Reader turns ISO-8859-1 into UTF-8.
type latin1Reader struct {
	r   io.Reader
	buf []byte
}

func (l *latin1Reader) Read(p []byte) (int, error) {
	if len(l.buf) == 0 {
		var chunk [4096]byte
		n, err := l.r.Read(chunk[:])
		if n == 0 {
			return 0, err
		}
		l.buf = l.buf[:0]
		for _, b := range chunk[:n] {
			l.buf = utf8.AppendRune(l.buf, rune(b))
		}
	}
	n := copy(p, l.buf)
	l.buf = l.buf[n:]
	return n, nil
}
