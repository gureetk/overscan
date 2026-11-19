// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import (
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Names of files and folders: what they say about the film or the episode
// in them. The conventions are those that Kodi, Jellyfin and Plex read,
// such as "Title (2020)/Title (2020).mkv" for a film and
// "Show/Season 01/Show S01E02.mkv" for an episode.

// videoExtensions are the extensions of the files that Overscan lists.
var videoExtensions = map[string]bool{
	"mkv": true, "mp4": true, "m4v": true, "avi": true, "mov": true, "wmv": true, "mpg": true, "mpeg": true,
	"ts": true, "m2ts": true, "mts": true, "webm": true, "flv": true, "ogv": true, "3gp": true, "divx": true,
}

// pictureExtensions are those of the pictures beside them.
var pictureExtensions = []string{"jpg", "jpeg", "png"}

// extension returns a file's extension in lower case, without the dot.
func extension(name string) string {
	ext := path.Ext(name)
	if ext == "" {
		return ""
	}
	return strings.ToLower(ext[1:])
}

// base returns a file's name without its extension.
func base(name string) string {
	return strings.TrimSuffix(name, path.Ext(name))
}

// skippedFolders are folders whose videos are not films or episodes of
// their own: a title's extras, a disc's structure, and what file servers
// and operating systems keep.
var skippedFolders = map[string]bool{
	"extras": true, "extra": true, "featurettes": true, "behind the scenes": true, "deleted scenes": true,
	"interviews": true, "scenes": true, "samples": true, "sample": true, "shorts": true, "clips": true,
	"trailers": true, "other": true, "backdrops": true, "theme-music": true,
	"video_ts": true, "audio_ts": true, "bdmv": true, "certificate": true,
	"lost+found": true, "$recycle.bin": true, "system volume information": true,
}

// skipFolder reports whether a folder's videos are left out.
func skipFolder(name string) bool {
	if name == "" {
		return false
	}
	switch name[0] {
	case '.', '@', '#', '$':
		return true
	}
	return skippedFolders[strings.ToLower(name)]
}

// extraSuffixes end the names of a title's extras that sit beside it.
var extraSuffixes = []string{"-trailer", "-sample", "-scene", "-clip", "-interview", "-behindthescenes",
	"-deleted", "-deletedscene", "-featurette", "-short", "-other", "-extra"}

// isExtra reports whether a video, by its name without the extension, is a
// title's extra rather than a title.
func isExtra(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if lower == "sample" || strings.HasPrefix(lower, ".") {
		return true
	}
	for _, s := range extraSuffixes {
		if strings.HasSuffix(lower, s) || strings.HasSuffix(lower, strings.Replace(s, "-", ".", 1)) ||
			strings.HasSuffix(lower, strings.Replace(s, "-", "_", 1)) {
			return true
		}
	}
	return false
}

var (
	// episodeMark finds S01E02, s1e2, S01.E02 or S01 E02 in a name.
	episodeMark = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(s(\d{1,4})[ ._-]*e(\d{1,4}))(?:[^0-9]|$)`)
	// crossMark finds 1x02.
	crossMark = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])((\d{1,2})x(\d{1,3}))(?:[^0-9a-z]|$)`)
	// seasonFolder is a season's folder: Season 1, Series 02, S01.
	seasonFolder = regexp.MustCompile(`(?i)^(?:season|series|saison|staffel|temporada|stagione|seizoen|sezon)[ ._-]*(\d{1,4})$|^s(\d{1,4})$`)
	// specialsFolder holds a series' specials, season 0.
	specialsFolder = regexp.MustCompile(`(?i)^(?:specials?|season[ ._-]*0+)$`)
	// episodeWord finds E02, Ep 2 or Episode 2 in a name in a season's
	// folder.
	episodeWord = regexp.MustCompile(`(?i)(?:^|[^a-z0-9])(?:e|ep|episode)[ ._-]*(\d{1,4})(?:[^0-9]|$)`)
	// leadingNumber is the number that starts a name in a season's folder,
	// as in "02 - Title".
	leadingNumber = regexp.MustCompile(`^(\d{1,4})(?:[^0-9]|$)`)
	// bracketYear finds a year in brackets: (2020) or [2020].
	bracketYear = regexp.MustCompile(`[(\[]((?:19|20)\d\d)[)\]]`)
	// tagMark finds tags in square or curly brackets, such as [1080p] or
	// [tmdbid-123].
	tagMark = regexp.MustCompile(`\[[^\]]*\]|\{[^}]*\}`)
)

// tmdbTag finds a TMDB ID in a name's tags, as in [tmdbid-123] or
// {tmdb-123}.
var tmdbTag = regexp.MustCompile(`(?i)[\[{]tmdb(?:id)?[-=](\d{1,9})[\]}]`)

// tmdbIDOf returns the first TMDB ID that the names' tags give, or 0.
func tmdbIDOf(names ...string) int64 {
	for _, name := range names {
		if m := tmdbTag.FindStringSubmatch(name); m != nil {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

// seasonOfFolder returns the season that a folder's name says it holds.
func seasonOfFolder(name string) (int, bool) {
	name = strings.TrimSpace(name)
	if specialsFolder.MatchString(name) {
		return 0, true
	}
	m := seasonFolder.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	n := m[1]
	if n == "" {
		n = m[2]
	}
	season, err := strconv.Atoi(n)
	return season, err == nil
}

// episodeOf returns the season and the number that a video's name, without
// its extension, gives an episode, the name's part before them, which may
// name the series, and the part after them, which may name the episode.
func episodeOf(name string) (season, number int, before, after string, ok bool) {
	for _, re := range []*regexp.Regexp{episodeMark, crossMark} {
		if m := re.FindStringSubmatchIndex(name); m != nil {
			season, _ = strconv.Atoi(name[m[4]:m[5]])
			number, _ = strconv.Atoi(name[m[6]:m[7]])
			return season, number, name[:m[2]], name[m[7]:], true
		}
	}
	return 0, 0, "", "", false
}

// numberInSeason returns the number that a video's name gives an episode
// in a season's folder, where the season is known: E02, Episode 2, or a
// number that starts the name; and the part of the name after it.
func numberInSeason(name string) (number int, after string, ok bool) {
	for _, re := range []*regexp.Regexp{episodeWord, leadingNumber} {
		if m := re.FindStringSubmatchIndex(name); m != nil {
			n, err := strconv.Atoi(name[m[2]:m[3]])
			return n, name[m[3]:], err == nil
		}
	}
	return 0, "", false
}

// episodeName cleans the part of an episode's name after its number into
// the episode's own name: "Coastline S01E02 - Rocks" gives "Rocks", and
// "Coastline.S01E02.1080p" none.
func episodeName(after string) string {
	after = strings.TrimLeft(multiEpisode.ReplaceAllString(after, ""), " ._-")
	first := strings.FieldsFunc(after, func(r rune) bool { return r == ' ' || r == '.' || r == '_' })
	if len(first) == 0 || qualityWords[strings.ToLower(first[0])] {
		return ""
	}
	name, _ := cleanName(after)
	return name
}

// multiEpisode is the rest of a mark for several episodes, such as the -E02
// of S01E01-E02.
var multiEpisode = regexp.MustCompile(`(?i)^(?:[ ._-]*e\d{1,4})+`)

// qualityWords end the useful part of a release's name: what follows them
// says how it was made, not what it is.
var qualityWords = map[string]bool{
	"2160p": true, "1080p": true, "1080i": true, "720p": true, "576p": true, "480p": true, "4k": true, "uhd": true,
	"bluray": true, "blu-ray": true, "bdrip": true, "brrip": true, "bdremux": true, "remux": true, "webrip": true,
	"web-dl": true, "webdl": true, "web": true, "hdtv": true, "dvdrip": true, "dvd": true, "hdrip": true,
	"x264": true, "x265": true, "h264": true, "h265": true, "h.264": true, "h.265": true, "hevc": true,
	"avc": true, "xvid": true, "divx": true, "hdr": true, "hdr10": true, "dv": true, "10bit": true,
	"dts": true, "ac3": true, "aac": true, "dd5.1": true, "ddp5.1": true, "atmos": true, "truehd": true,
	"proper": true, "repack": true, "internal": true, "multi": true, "subbed": true, "dubbed": true,
}

// cleanName turns the name of a file, without its extension, or of a
// folder into a title and a year: "The.Long.Field.2024.1080p.WEB" and
// "The Long Field (2024)" both give "The Long Field" and 2024. A year in
// brackets ends the title. In a release's name, which has dots or
// underscores for spaces, so does the last year after the first word,
// unless it is still to come: "Blade.Runner.2049.2017.1080p" is a 2017
// film. In other names a year without brackets is part of the title. The
// year is 0 if the name gives none.
func cleanName(name string) (string, int) {
	name = tagMark.ReplaceAllString(name, " ")
	release := !strings.Contains(strings.TrimSpace(name), " ")
	if release {
		name = strings.NewReplacer(".", " ", "_", " ").Replace(name)
	} else {
		name = strings.ReplaceAll(name, "_", " ")
	}
	words := strings.Fields(name)
	tagged := false
	for i, w := range words {
		if i > 0 && qualityWords[strings.ToLower(strings.Trim(w, "()[]-"))] {
			words, tagged = words[:i], true
			break
		}
	}
	name = strings.Join(words, " ")
	year := 0
	if ms := bracketYear.FindAllStringSubmatchIndex(name, -1); ms != nil {
		m := ms[len(ms)-1]
		if before := strings.TrimSpace(name[:m[0]]); before != "" {
			year, _ = strconv.Atoi(name[m[2]:m[3]])
			name = before
		}
	}
	if year == 0 && release {
		for i := len(words) - 1; i > 0; i-- {
			y, err := strconv.Atoi(words[i])
			if err == nil && len(words[i]) == 4 && y >= 1900 && y <= latestYear && (tagged || i == len(words)-1) {
				year, name = y, strings.Join(words[:i], " ")
				break
			}
		}
	}
	name = strings.TrimRight(strings.TrimSpace(name), " -([.,")
	name = strings.TrimSpace(strings.TrimLeft(name, " -)]."))
	return name, year
}

// latestYear is the last year that a release's name may give a film: a
// later number is part of its title.
var latestYear = time.Now().Year() + 2

// seriesName cleans the part of an episode's name before its season and
// number into a series' name: "The.Long.Field.S01E02" gives "The Long
// Field".
func seriesName(before string) string {
	name, year := cleanName(strings.TrimRight(before, " ._-"))
	if name == "" && year > 0 {
		return strconv.Itoa(year)
	}
	return name
}

// sameName reports whether two names are the same title, leaving out case,
// punctuation, a year and tags: "the.office.us" and "The Office (US)
// (2005)" are.
func sameName(a, b string) bool {
	return normal(a) != "" && normal(a) == normal(b)
}

// normal returns a name in lower case, with only its letters and digits, a
// year at its end left out.
func normal(name string) string {
	name, _ = cleanName(name)
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}
