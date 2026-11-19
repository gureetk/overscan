// SPDX-License-Identifier: AGPL-3.0-or-later

// Package subtitles reads subtitle files, SubRip (SRT), WebVTT and
// SubStation Alpha (SSA and ASS), and writes them as SubRip in UTF-8, the
// form that the box loads from an address (D69). It can move them earlier,
// for a stream that starts part way through a film (D68).
package subtitles

import (
	"bufio"
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
)

const (
	// MaxBytes caps a subtitle file that Parse reads.
	MaxBytes = 8 << 20
	// maxCues caps the cues kept of one, and maxText each cue's text.
	maxCues = 100000
	maxText = 2000
	// maxTime caps a cue's times.
	maxTime = 100 * time.Hour
)

// Formats of subtitle files, as their extensions name them.
const (
	SRT = "srt"
	VTT = "vtt"
	ASS = "ass" // SubStation Alpha too, whose files end in .ssa
)

// Track is a subtitle track that a box can show over a film or an episode
// (D69): a file of text subtitles beside it in a folder, or one that a
// media server gives.
type Track struct {
	Language string // as its name or the server tags it, such as eng or en; "" if not known
	Title    string
	Forced   bool // only for what is not in the film's language, such as signs
	Hearing  bool // for the deaf and hard of hearing
	Default  bool
	Format   string // SRT, VTT or ASS: what it is read as, unless its text says otherwise
	// File is the path of a folder's file in the folder; URL the address of
	// a media server's, which can hold a key and never goes to a box.
	File string
	URL  string
}

// Cue is a line of subtitles: its text, shown from Start to End, in time
// from the start of the film.
type Cue struct {
	Start, End time.Duration
	Text       string // lines separated by \n
}

// ErrFormat is the error for data that is not subtitles that Parse reads.
var ErrFormat = errors.New("subtitles: not SRT, WebVTT or SubStation Alpha")

// Parse reads subtitles in one of the formats, which it tells from the data
// where it can and else takes from format (SRT, VTT or ASS), in UTF-8,
// UTF-16 with a byte order mark, or else the legacy code page of the
// subtitles' language, an ISO 639 code such as "rus" or "pl", if given. Cues
// come in order of their start.
func Parse(data []byte, format, language string) ([]Cue, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("subtitles: more than %d bytes", MaxBytes)
	}
	text := decode(data, language)
	var cues []Cue
	var err error
	switch sniff(text, format) {
	case VTT:
		cues, err = parseVTT(text)
	case ASS:
		cues, err = parseASS(text)
	default:
		cues, err = parseSRT(text)
	}
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(cues, func(a, b Cue) int { return cmp.Compare(a.Start, b.Start) })
	return cues, nil
}

// sniff returns the format of subtitles: what the text says, or format.
func sniff(text, format string) string {
	head := strings.TrimLeft(text[:min(len(text), 4096)], " \t\r\n")
	switch {
	case strings.HasPrefix(head, "WEBVTT"):
		return VTT
	case strings.HasPrefix(head, "[Script Info]") || strings.Contains(head, "[Events]") || strings.Contains(head, "[V4+ Styles]") ||
		strings.Contains(head, "[V4 Styles]"):
		return ASS
	case strings.Contains(head, "-->"):
		return SRT
	}
	switch strings.ToLower(format) {
	case VTT:
		return VTT
	case ASS, "ssa":
		return ASS
	}
	return SRT
}

// decode returns data as text: UTF-8, UTF-16 by its byte order mark, or
// else the legacy code page of language.
func decode(data []byte, language string) string {
	switch {
	case bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}):
		return strings.ToValidUTF8(string(data[3:]), "�")
	case bytes.HasPrefix(data, []byte{0xFF, 0xFE}), bytes.HasPrefix(data, []byte{0xFE, 0xFF}):
		return fromUTF16(data[2:], data[0] == 0xFE)
	case utf8.Valid(data):
		return string(data)
	}
	out, err := codePage(language).NewDecoder().Bytes(data)
	if err != nil {
		return strings.ToValidUTF8(string(data), "�")
	}
	return string(out)
}

// fromUTF16 decodes UTF-16, big-endian if big.
func fromUTF16(data []byte, big bool) string {
	units := make([]uint16, len(data)/2)
	for i := range units {
		if big {
			units[i] = uint16(data[2*i])<<8 | uint16(data[2*i+1])
		} else {
			units[i] = uint16(data[2*i+1])<<8 | uint16(data[2*i])
		}
	}
	return string(utf16.Decode(units))
}

// codePage returns the Windows code page that subtitles in a language were
// most often written in before UTF-8: Western European unless the
// language says otherwise.
func codePage(language string) encoding.Encoding {
	switch strings.ToLower(language) {
	case "rus", "ru", "ukr", "uk", "bul", "bg", "bel", "be", "mkd", "mac", "mk", "srp", "sr":
		return charmap.Windows1251
	case "pol", "pl", "ces", "cze", "cs", "slk", "slo", "sk", "hun", "hu", "hrv", "hr", "slv", "sl", "ron", "rum", "ro",
		"bos", "bs", "sqi", "alb", "sq":
		return charmap.Windows1250
	case "ell", "gre", "el":
		return charmap.Windows1253
	case "tur", "tr", "aze", "az":
		return charmap.Windows1254
	case "heb", "he":
		return charmap.Windows1255
	case "ara", "ar", "fas", "per", "fa", "urd", "ur":
		return charmap.Windows1256
	case "lit", "lt", "lav", "lv", "est", "et":
		return charmap.Windows1257
	case "vie", "vi":
		return charmap.Windows1258
	case "tha", "th":
		return charmap.Windows874
	}
	return charmap.Windows1252
}

// srtTime matches a time in SubRip, and WebVTT's, which has a full stop
// before the milliseconds, and may leave out the hours.
var srtTime = regexp.MustCompile(`^(?:(\d{1,3}):)?(\d{1,2}):(\d{1,2})(?:[,.](\d{1,3}))?$`)

// parseTime reads a time such as 01:02:03,456 or 02:03.456.
func parseTime(s string) (time.Duration, bool) {
	m := srtTime.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	ms := 0
	if m[4] != "" {
		ms, _ = strconv.Atoi((m[4] + "00")[:3])
	}
	d := time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(sec)*time.Second +
		time.Duration(ms)*time.Millisecond
	return d, d <= maxTime
}

// timing reads a line such as "00:00:01,000 --> 00:00:03,500", with
// WebVTT's settings after it left out.
func timing(line string) (start, end time.Duration, ok bool) {
	from, to, found := strings.Cut(line, "-->")
	if !found {
		return 0, 0, false
	}
	if f := strings.Fields(to); len(f) > 0 {
		to = f[0]
	}
	start, ok1 := parseTime(from)
	end, ok2 := parseTime(to)
	return start, end, ok1 && ok2
}

// lines splits text into lines, whatever ends them.
func lines(text string) *bufio.Scanner {
	sc := bufio.NewScanner(strings.NewReader(strings.ReplaceAll(text, "\r\n", "\n")))
	sc.Buffer(make([]byte, 64<<10), MaxBytes)
	return sc
}

// add appends a cue with the text of lines, if it has any and room.
func add(cues []Cue, start, end time.Duration, text []string) []Cue {
	t := strings.TrimSpace(strings.Join(text, "\n"))
	if t == "" || end <= start || len(cues) >= maxCues {
		return cues
	}
	if len(t) > maxText {
		t = t[:maxText]
		for !utf8.ValidString(t) {
			t = t[:len(t)-1]
		}
	}
	return append(cues, Cue{Start: start, End: end, Text: t})
}

// parseSRT reads SubRip: blocks of an optional number, a timing line and
// the text, separated by blank lines.
func parseSRT(text string) ([]Cue, error) {
	var cues []Cue
	sc := lines(text)
	var (
		in         bool
		start, end time.Duration
		body       []string
		any        bool
	)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if s, e, ok := timing(line); ok {
			if in {
				cues = add(cues, start, end, dropNumber(body))
			}
			in, start, end, body, any = true, s, e, nil, true
			continue
		}
		if !in {
			continue
		}
		body = append(body, line)
	}
	if in {
		cues = add(cues, start, end, body)
	}
	if !any {
		return nil, ErrFormat
	}
	for i := range cues {
		cues[i].Text = strings.TrimSpace(stripBraces(cues[i].Text))
	}
	return cues, sc.Err()
}

// dropNumber returns the text of a cue without the next cue's number,
// which ends it: a last line of digits after a blank line.
func dropNumber(body []string) []string {
	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}
	if n := len(body); n >= 2 && isNumber(body[n-1]) && strings.TrimSpace(body[n-2]) == "" {
		return body[:n-2]
	}
	if n := len(body); n == 1 && isNumber(body[0]) {
		return nil
	}
	return body
}

func isNumber(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// braces matches a SubStation override block, such as {\an8}, which some
// SubRip files carry.
var braces = regexp.MustCompile(`\{\\[^}]*\}`)

func stripBraces(s string) string { return braces.ReplaceAllString(s, "") }

// vttTag matches WebVTT's tags inside a cue: classes, voices, languages,
// ruby and timestamps; <i>, <b> and <u> stay, as SubRip has them.
var vttTag = regexp.MustCompile(`</?(?:c|v|lang|ruby|rt)(?:[.\s][^>]*)?>|<\d{1,3}:\d{2}(?::\d{2})?\.\d{3}>`)

// parseVTT reads WebVTT: after its header, blocks that are cues, with an
// optional identifier, or notes, styles and regions, which are left out.
func parseVTT(text string) ([]Cue, error) {
	var cues []Cue
	sc := lines(text)
	var (
		in, skip   bool
		start, end time.Duration
		body       []string
	)
	first := true
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), " \t\r")
		if first {
			first = false
			if !strings.HasPrefix(strings.TrimPrefix(line, "\uFEFF"), "WEBVTT") {
				return nil, ErrFormat
			}
			skip = true // the header's own block
			continue
		}
		if line == "" {
			if in {
				cues = add(cues, start, end, body)
			}
			in, skip, body = false, false, nil
			continue
		}
		if skip {
			continue
		}
		if in {
			body = append(body, vttTag.ReplaceAllString(line, ""))
			continue
		}
		if s, e, ok := timing(line); ok {
			in, start, end = true, s, e
			continue
		}
		if strings.HasPrefix(line, "NOTE") || strings.HasPrefix(line, "STYLE") || strings.HasPrefix(line, "REGION") {
			skip = true
		}
		// Otherwise a cue's identifier, before its timing.
	}
	if in {
		cues = add(cues, start, end, body)
	}
	return cues, sc.Err()
}

// assTime matches a SubStation time, such as 0:01:02.34.
var assTime = regexp.MustCompile(`^(\d{1,2}):(\d{1,2}):(\d{1,2})(?:\.(\d{1,3}))?$`)

func parseASSTime(s string) (time.Duration, bool) {
	m := assTime.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mi, _ := strconv.Atoi(m[2])
	sec, _ := strconv.Atoi(m[3])
	ms := 0
	if m[4] != "" {
		ms, _ = strconv.Atoi((m[4] + "00")[:3])
	}
	return time.Duration(h)*time.Hour + time.Duration(mi)*time.Minute + time.Duration(sec)*time.Second +
		time.Duration(ms)*time.Millisecond, true
}

// override matches a SubStation override block, and drawing switches a
// drawing on (\p1 and up) or off (\p0).
var (
	override = regexp.MustCompile(`\{[^}]*\}`)
	drawing  = regexp.MustCompile(`\\p(\d)`)
)

// assText returns the text of a SubStation dialogue line as SubRip's: line
// breaks kept, override blocks and drawings left out.
func assText(s string) string {
	var b strings.Builder
	draw := false
	for s != "" {
		loc := override.FindStringIndex(s)
		if loc == nil {
			if !draw {
				b.WriteString(s)
			}
			break
		}
		if !draw {
			b.WriteString(s[:loc[0]])
		}
		if m := drawing.FindAllStringSubmatch(s[loc[0]:loc[1]], -1); m != nil {
			draw = m[len(m)-1][1] != "0"
		}
		s = s[loc[1]:]
	}
	t := b.String()
	t = strings.NewReplacer(`\N`, "\n", `\n`, "\n", `\h`, " ").Replace(t)
	var out []string
	for _, l := range strings.Split(t, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}

// parseASS reads the dialogue of SubStation Alpha: the [Events] section,
// its Format line naming the fields, and its Dialogue lines, whose text is
// the last field and may hold commas.
func parseASS(text string) ([]Cue, error) {
	var cues []Cue
	sc := lines(text)
	events := false
	fields := []string{"layer", "start", "end", "style", "name", "marginl", "marginr", "marginv", "effect", "text"}
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\uFEFF"))
		if strings.HasPrefix(line, "[") {
			events = strings.EqualFold(line, "[Events]")
			continue
		}
		if !events {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "format":
			fields = fields[:0]
			for f := range strings.SplitSeq(value, ",") {
				fields = append(fields, strings.ToLower(strings.TrimSpace(f)))
			}
		case "dialogue":
			if len(fields) == 0 || fields[len(fields)-1] != "text" {
				continue
			}
			parts := strings.SplitN(value, ",", len(fields))
			if len(parts) != len(fields) {
				continue
			}
			var start, end time.Duration
			var okStart, okEnd bool
			for i, f := range fields {
				switch f {
				case "start":
					start, okStart = parseASSTime(parts[i])
				case "end":
					end, okEnd = parseASSTime(parts[i])
				}
			}
			if okStart && okEnd {
				cues = add(cues, start, end, []string{assText(parts[len(parts)-1])})
			}
		}
	}
	return cues, sc.Err()
}

// Shift returns cues moved earlier by d, for a stream that starts d into
// the film: those that end by then are left out, and one shown across it
// starts at 0.
func Shift(cues []Cue, d time.Duration) []Cue {
	if d <= 0 {
		return cues
	}
	out := make([]Cue, 0, len(cues))
	for _, c := range cues {
		if c.End <= d {
			continue
		}
		c.Start, c.End = max(c.Start-d, 0), c.End-d
		out = append(out, c)
	}
	return out
}

// WriteSRT writes cues as SubRip, with CRLF line ends.
func WriteSRT(w io.Writer, cues []Cue) error {
	bw := bufio.NewWriter(w)
	for i, c := range cues {
		text := strings.ReplaceAll(c.Text, "\n", "\r\n")
		fmt.Fprintf(bw, "%d\r\n%s --> %s\r\n%s\r\n\r\n", i+1, srtStamp(c.Start), srtStamp(c.End), text)
	}
	return bw.Flush()
}

// srtStamp writes a time as SubRip does, such as 01:02:03,456.
func srtStamp(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
