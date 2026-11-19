// SPDX-License-Identifier: AGPL-3.0-or-later

package plex

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Item is a film, a show or an episode.
type Item struct {
	Key       string // the server's key for it (its rating key), digits
	Name      string
	Type      string // TypeMovie, TypeShow or TypeEpisode
	Year      int    // 0 if not known
	Rating    int    // the audience's rating out of 100, or the critics', 0 if none
	AgeRating string // such as PG-13, "" if none
	Plot      string
	Genres    []string
	Cast      []string // actors, in the server's order
	Directors []string
	Countries []string      // where it was made
	Duration  time.Duration // 0 if not known
	Added     time.Time     // when the server added it, zero if not known
	Premiere  time.Time     // zero if not known
	// Poster and Backdrop are the paths of its pictures on the server, ""
	// for none; an episode's poster is a still from it.
	Poster   string
	Backdrop string
	// Season and Number place an episode; 0 when the server does not say.
	Season     int
	Number     int
	SeasonName string
	// File is a film's or an episode's file: the first part of its first
	// version. Zero for a show, and for a title without a file.
	File File

	subtitles []Subtitle // the file's text subtitles, from the title's details
}

// File is the file of a film or an episode on the server.
type File struct {
	// Key is the path at which the server sends it, as the server gives it,
	// such as /library/parts/12/1700000000/file.mkv. The source's stream
	// address adds the token (source.Settings.StreamURL).
	Key       string
	Container string // its extension, such as mkv; "" if not known
}

type tagJSON struct {
	Tag text `json:"tag"`
}

type streamJSON struct {
	ID              number  `json:"id"`
	StreamType      number  `json:"streamType"`
	Index           *number `json:"index"`
	Codec           text    `json:"codec"`
	LanguageCode    text    `json:"languageCode"`
	Title           text    `json:"title"`
	Forced          flag    `json:"forced"`
	HearingImpaired flag    `json:"hearingImpaired"`
	Default         flag    `json:"default"`
	Key             text    `json:"key"`
}

type metadataJSON struct {
	RatingKey             text      `json:"ratingKey"`
	Type                  text      `json:"type"`
	Title                 text      `json:"title"`
	Year                  number    `json:"year"`
	Summary               text      `json:"summary"`
	ContentRating         text      `json:"contentRating"`
	AudienceRating        number    `json:"audienceRating"`
	Rating                number    `json:"rating"`
	Duration              number    `json:"duration"` // milliseconds
	OriginallyAvailableAt text      `json:"originallyAvailableAt"`
	AddedAt               number    `json:"addedAt"` // Unix seconds
	Thumb                 text      `json:"thumb"`
	Art                   text      `json:"art"`
	Index                 number    `json:"index"`
	ParentIndex           number    `json:"parentIndex"`
	ParentTitle           text      `json:"parentTitle"`
	Genre                 []tagJSON `json:"Genre"`
	Role                  []tagJSON `json:"Role"`
	Director              []tagJSON `json:"Director"`
	Country               []tagJSON `json:"Country"`
	Media                 []struct {
		Container text `json:"container"`
		Part      []struct {
			Key       text         `json:"key"`
			Container text         `json:"container"`
			Stream    []streamJSON `json:"Stream"`
		} `json:"Part"`
	} `json:"Media"`
}

// maxPeople is how many of the cast and of the directors an item keeps.
const maxPeople = 8

// decodeItem decodes one entry of a list or of a title's details, and
// reports whether it is a title: one with a key and a name.
func decodeItem(raw json.RawMessage) (Item, bool) {
	var m metadataJSON
	if err := json.Unmarshal(raw, &m); err != nil {
		return Item{}, false
	}
	it := Item{
		Key:        clean(string(m.RatingKey), 20),
		Name:       clean(string(m.Title), 200),
		Type:       strings.ToLower(clean(string(m.Type), 20)),
		Year:       int(m.Year),
		AgeRating:  clean(string(m.ContentRating), 20),
		Plot:       strings.TrimSpace(strings.ToValidUTF8(string(m.Summary), "")),
		Genres:     tags(m.Genre, 10),
		Cast:       tags(m.Role, maxPeople),
		Directors:  tags(m.Director, maxPeople),
		Countries:  tags(m.Country, 10),
		Season:     int(m.ParentIndex),
		Number:     int(m.Index),
		SeasonName: clean(string(m.ParentTitle), 120),
	}
	if !validKey(it.Key) || it.Name == "" {
		return Item{}, false
	}
	if len(it.Plot) > 4000 {
		it.Plot = strings.ToValidUTF8(it.Plot[:4000], "")
	}
	if it.Year < 1800 || it.Year > 3000 {
		it.Year = 0
	}
	if it.Season < 0 || it.Season > 10000 {
		it.Season = 0
	}
	if it.Number < 0 || it.Number > 100000 {
		it.Number = 0
	}
	it.Rating = rating(float64(m.AudienceRating))
	if it.Rating == 0 {
		it.Rating = rating(float64(m.Rating))
	}
	if ms := int64(m.Duration); ms > 0 && ms < int64(1000*time.Hour/time.Millisecond) {
		it.Duration = time.Duration(ms) * time.Millisecond
	}
	if s := int64(m.AddedAt); s > 0 && s < 1e11 {
		it.Added = time.Unix(s, 0).UTC()
	}
	if t, err := time.Parse(time.DateOnly, strings.TrimSpace(string(m.OriginallyAvailableAt))); err == nil {
		it.Premiere = t
	}
	if p := string(m.Thumb); validPath(p) {
		it.Poster = p
	}
	if p := string(m.Art); validPath(p) {
		it.Backdrop = p
	}
	if it.Type == TypeMovie || it.Type == TypeEpisode {
		for _, media := range m.Media {
			if len(media.Part) == 0 {
				continue
			}
			part := media.Part[0]
			key := string(part.Key)
			if !strings.HasPrefix(key, "/library/parts/") || !validPath(key) {
				continue
			}
			container := string(part.Container)
			if container == "" {
				container = string(media.Container)
			}
			it.File = File{Key: key, Container: Extension(container)}
			it.subtitles = subtitles(part.Stream)
			break
		}
	}
	return it, true
}

// rating returns a rating out of 10 as one out of 100, 0 if none.
func rating(r float64) int {
	if r <= 0 || r > 10 {
		return 0
	}
	return int(r*10 + 0.5)
}

// tags returns the names of a list of tags, at most n.
func tags(list []tagJSON, n int) []string {
	var out []string
	for _, t := range list {
		if name := clean(string(t.Tag), 120); name != "" && len(out) < n {
			out = append(out, name)
		}
	}
	return out
}

// subtitles returns the text subtitle tracks among a file's streams.
func subtitles(streams []streamJSON) []Subtitle {
	var out []Subtitle
	for _, s := range streams {
		if int(s.StreamType) != 3 || len(out) == 64 {
			continue
		}
		codec := strings.ToLower(clean(string(s.Codec), 40))
		if !textCodec(codec) {
			continue
		}
		sub := Subtitle{ID: int64(s.ID), Index: -1, Codec: codec, Language: clean(string(s.LanguageCode), 40),
			Title: clean(string(s.Title), 120), Forced: bool(s.Forced), Hearing: bool(s.HearingImpaired),
			Default: bool(s.Default)}
		if sub.Language == "und" {
			sub.Language = ""
		}
		if key := string(s.Key); key != "" {
			// Only a file beside the video has a key of its own.
			sub.External = true
		} else if s.Index != nil && *s.Index >= 0 {
			sub.Index = int(*s.Index)
		} else {
			continue
		}
		out = append(out, sub)
	}
	return out
}

// textCodec reports whether subtitles of a codec, as the server names it,
// are text: not pictures, as Blu-ray's, DVD's and DVB's are.
func textCodec(codec string) bool {
	for _, picture := range []string{"pgs", "dvdsub", "dvd_subtitle", "vobsub", "dvbsub", "dvb_subtitle", "xsub", "sup"} {
		if strings.Contains(codec, picture) {
			return false
		}
	}
	return codec != "" && codec != "sub" && codec != "idx"
}

var extensionPattern = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// Extension returns the file extension for a container as the server names
// it, such as mkv, mp4 or mpegts. "" if it cannot tell.
func Extension(container string) string {
	c := strings.ToLower(strings.TrimSpace(container))
	switch c {
	case "mpegts":
		return "ts"
	case "matroska":
		return "mkv"
	}
	if extensionPattern.MatchString(c) {
		return c
	}
	return ""
}

// text is a string in an answer that a server may also give as a number.
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	switch {
	case bytes.Equal(b, []byte("null")):
		*t = ""
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*t = text(s)
	case len(b) > 0 && (b[0] == '-' || b[0] >= '0' && b[0] <= '9'):
		*t = text(b)
	default:
		*t = ""
	}
	return nil
}

// number is a number in an answer that a server may also give as a string;
// 0 for anything else.
type number float64

func (n *number) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		b = []byte(strings.TrimSpace(s))
	}
	f, err := strconv.ParseFloat(string(b), 64)
	if err != nil || f != f || f > 1e15 || f < -1e15 {
		f = 0
	}
	*n = number(f)
	return nil
}

// flag is a yes or no in an answer, as true or false, 1 or 0, or the same
// in a string.
type flag bool

func (f *flag) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(bytes.TrimSpace(b)), `"`)
	*f = s == "true" || s == "1"
	return nil
}
