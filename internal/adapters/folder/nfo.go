// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Info is what an .nfo file beside a video says about it, in the format
// that Kodi writes and Jellyfin and Plex's agents read: a film's
// <movie>, a series' <tvshow> in tvshow.nfo, or an episode's
// <episodedetails>. What it leaves out is zero.
type Info struct {
	Title     string
	Year      int
	Plot      string
	Genres    []string
	Cast      []string // actors, in the file's order
	Directors []string
	Countries []string
	AgeRating string        // such as PG-13
	Rating    int           // out of 100
	Runtime   time.Duration // to the minute
	Premiered time.Time     // a film's or a series' first showing, an episode's airing
	Season    int           // an episode's; -1 if not given
	Episode   int           // an episode's number; -1 if not given
	TMDBID    int64         // the title's ID on TMDB, 0 if not given (D88)
}

// maxNFOBytes is the most of an .nfo file that is read.
const maxNFOBytes = 1 << 20

// nfoXML is an .nfo file's root element, whatever its name.
type nfoXML struct {
	XMLName     xml.Name
	Title       string      `xml:"title"`
	Year        string      `xml:"year"`
	Plot        string      `xml:"plot"`
	Outline     string      `xml:"outline"`
	Genres      []string    `xml:"genre"`
	Directors   []string    `xml:"director"`
	Countries   []string    `xml:"country"`
	MPAA        string      `xml:"mpaa"`
	Rating      string      `xml:"rating"`
	Ratings     []nfoRating `xml:"ratings>rating"`
	Runtime     string      `xml:"runtime"`
	Premiered   string      `xml:"premiered"`
	ReleaseDate string      `xml:"releasedate"`
	Aired       string      `xml:"aired"`
	Season      string      `xml:"season"`
	Episode     string      `xml:"episode"`
	Actors      []struct {
		Name string `xml:"name"`
	} `xml:"actor"`
	UniqueIDs []struct {
		Type  string `xml:"type,attr"`
		Value string `xml:",chardata"`
	} `xml:"uniqueid"`
	TMDBID string `xml:"tmdbid"`
}

type nfoRating struct {
	Default string `xml:"default,attr"`
	Value   string `xml:"value"`
	Max     string `xml:"max,attr"`
}

// Limits on what an .nfo file gives.
const (
	maxPeople   = 8
	maxPlotLen  = 4000
	maxShortLen = 120
)

var leadingDigits = regexp.MustCompile(`^\s*(\d{1,4})`)

// parseNFO reads an .nfo file. ok is false if it is not one: some hold only
// the address of a page about the title.
func parseNFO(data []byte) (info Info, ok bool) {
	if len(data) > maxNFOBytes {
		data = data[:maxNFOBytes]
	}
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.CharsetReader = charsetReader
	var x nfoXML
	if err := d.Decode(&x); err != nil {
		return Info{}, false
	}
	switch x.XMLName.Local {
	case "movie", "tvshow", "episodedetails", "musicvideo":
	default:
		return Info{}, false
	}
	info = Info{
		Title:     short(x.Title),
		Plot:      text(x.Plot, maxPlotLen),
		AgeRating: ageRating(x.MPAA),
		Season:    -1,
		Episode:   -1,
	}
	if info.Plot == "" {
		info.Plot = text(x.Outline, maxPlotLen)
	}
	if y, err := strconv.Atoi(strings.TrimSpace(x.Year)); err == nil && y >= 1870 && y <= 2200 {
		info.Year = y
	}
	info.Genres = names(x.Genres, 10)
	info.Directors = names(x.Directors, 4)
	info.Countries = names(x.Countries, 3)
	for _, a := range x.Actors {
		if len(info.Cast) == maxPeople {
			break
		}
		if n := short(a.Name); n != "" && !slices.Contains(info.Cast, n) {
			info.Cast = append(info.Cast, n)
		}
	}
	info.Rating = rating(x.Rating, "10")
	for _, r := range x.Ratings {
		if v := rating(r.Value, r.Max); v > 0 && (info.Rating == 0 || r.Default == "true") {
			info.Rating = v
		}
	}
	if m := leadingDigits.FindStringSubmatch(x.Runtime); m != nil {
		if n, _ := strconv.Atoi(m[1]); n > 0 && n < 2000 {
			info.Runtime = time.Duration(n) * time.Minute
		}
	}
	ids := []string{x.TMDBID}
	for _, u := range x.UniqueIDs {
		if strings.EqualFold(strings.TrimSpace(u.Type), "tmdb") {
			ids = append([]string{u.Value}, ids...)
		}
	}
	for _, id := range ids {
		if n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64); err == nil && n > 0 && n < 1e9 {
			info.TMDBID = n
			break
		}
	}
	for _, s := range []string{x.Premiered, x.ReleaseDate, x.Aired} {
		if t, err := time.Parse(time.DateOnly, strings.TrimSpace(s)); err == nil && t.Year() >= 1870 {
			info.Premiered = t
			break
		}
	}
	if info.Year == 0 && !info.Premiered.IsZero() {
		info.Year = info.Premiered.Year()
	}
	if n, err := strconv.Atoi(strings.TrimSpace(x.Season)); err == nil && n >= 0 && n < 10_000 {
		info.Season = n
	}
	if n, err := strconv.Atoi(strings.TrimSpace(x.Episode)); err == nil && n >= 0 && n < 100_000 {
		info.Episode = n
	}
	return info, true
}

// rating turns a rating out of max, 10 if not given, into one out of 100.
func rating(value, max string) int {
	v, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(value, ",", ".")), 64)
	scale, serr := strconv.ParseFloat(strings.TrimSpace(max), 64)
	if serr != nil || scale <= 0 {
		scale = 10
	}
	if err != nil || math.IsNaN(v) || v <= 0 {
		return 0
	}
	return int(math.Round(min(v/scale, 1) * 100))
}

// ageRating cleans an age rating: "Rated PG-13" and "US:PG-13" give PG-13.
func ageRating(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimSpace(strings.TrimPrefix(s, "Rated "))
	if _, after, ok := strings.Cut(s, ":"); ok {
		s = after
	}
	if len(s) > 20 {
		return ""
	}
	return short(s)
}

// names cleans a list of names, without repeats, at most n of them. A
// name with slashes, as some files write several, counts as several.
func names(list []string, n int) []string {
	var out []string
	for _, item := range list {
		for _, name := range strings.Split(item, " / ") {
			if name = short(name); name != "" && len(out) < n && !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
	}
	return out
}

// short cleans a short piece of text: one line, at most maxShortLen bytes.
func short(s string) string {
	s = strings.Join(strings.Fields(strings.ToValidUTF8(s, "")), " ")
	return truncate(s, maxShortLen)
}

// text cleans a longer piece of text, keeping its lines.
func text(s string, n int) string {
	s = strings.TrimSpace(strings.ToValidUTF8(s, ""))
	return truncate(s, n)
}

// truncate cuts s to at most n bytes, between runes.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// charsetReader lets an .nfo file declare ISO-8859-1 or Windows-1252, as
// some older tools wrote them, as well as UTF-8. Windows-1252's few letters
// beyond ISO-8859-1 come out as their ISO-8859-1 neighbours.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "utf-8", "utf8", "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "latin1", "latin-1", "iso8859-1", "windows-1252", "cp1252":
		data, err := io.ReadAll(io.LimitReader(input, maxNFOBytes))
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		for _, c := range data {
			b.WriteRune(rune(c))
		}
		return strings.NewReader(b.String()), nil
	}
	return nil, errCharset
}

// errCharset is the error for an .nfo file in another character set.
var errCharset = errors.New("folder: an .nfo file in a character set that Overscan does not read")
