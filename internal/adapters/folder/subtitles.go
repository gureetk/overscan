// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Subtitles beside a video (D69), named as Kodi, Jellyfin and Plex name
// them: the video's name, then what the subtitles are, then the extension,
// such as "Film (2020).en.forced.srt", in the video's folder or in a Subs
// or Subtitles folder beside it. In such a folder, a file whose name does
// not start with the video's, such as "2_English.srt", belongs to the video
// when the video is alone in its folder.

const (
	// maxSidecars caps the subtitle files found for a video.
	maxSidecars = 32
	// MaxSidecarBytes is the most of a subtitle file that ReadSidecar
	// reads.
	MaxSidecarBytes = 8 << 20
)

// subtitleExtensions are the extensions of the subtitle files that
// Overscan reads.
var subtitleExtensions = []string{"srt", "vtt", "ass", "ssa"}

// subtitleFolders are the names of folders, beside a video, that hold its
// subtitles.
var subtitleFolders = []string{"subs", "subtitles", "sub"}

// Sidecar is a subtitle file for a video.
type Sidecar struct {
	Path     string // in the folder
	Format   string // srt, vtt, ass or ssa
	Language string // ISO 639-2, such as eng, or "" if the name does not say
	Title    string // what else the name says, such as "Commentary"
	Forced   bool
	Hearing  bool // for the deaf and hard of hearing
	Default  bool
}

// Subtitles returns the subtitle files for the video at a film's or an
// episode's key in the folder at dir, in order of their names.
func Subtitles(dir, key string) ([]Sidecar, error) {
	if !fs.ValidPath(key) || !videoExtensions[extension(key)] {
		return nil, ErrNoTitle
	}
	f, err := Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	folder, file := path.Split(key)
	folder = strings.TrimSuffix(folder, "/")
	if folder == "" {
		folder = "."
	}
	stem := base(file)
	entries, err := fs.ReadDir(f.fsys, folder)
	if err != nil {
		return nil, ErrNoTitle
	}
	alone := true
	for _, e := range entries {
		if !e.IsDir() && videoExtensions[extension(e.Name())] && e.Name() != file && !isExtra(base(e.Name())) {
			alone = false
		}
	}
	var out []Sidecar
	look := func(dir string, entries []fs.DirEntry, any bool) {
		for _, e := range entries {
			name := e.Name()
			ext := extension(name)
			if e.IsDir() || !slices.Contains(subtitleExtensions, ext) || len(out) == maxSidecars {
				continue
			}
			rest := strings.TrimSuffix(name, path.Ext(name))
			switch {
			case strings.EqualFold(rest, stem):
				rest = ""
			case len(rest) > len(stem) && strings.EqualFold(rest[:len(stem)], stem) && rest[len(stem)] == '.':
				rest = rest[len(stem)+1:]
			case !any:
				continue
			}
			s := Sidecar{Path: path.Join(dir, name), Format: ext}
			describe(&s, rest)
			out = append(out, s)
		}
	}
	look(folder, entries, false)
	for _, e := range entries {
		if e.IsDir() && slices.Contains(subtitleFolders, strings.ToLower(e.Name())) {
			sub := path.Join(folder, e.Name())
			if more, err := fs.ReadDir(f.fsys, sub); err == nil {
				look(sub, more, alone)
			}
		}
	}
	return out, nil
}

// describe fills in what a subtitle file's name says after the video's:
// its language, flags, and what is left as its title.
func describe(s *Sidecar, rest string) {
	var title []string
	for word := range strings.FieldsFuncSeq(rest, func(r rune) bool { return r == '.' || r == '_' || r == '-' || r == ' ' }) {
		w := strings.ToLower(word)
		switch {
		case w == "forced" || w == "foreign":
			s.Forced = true
		case w == "sdh" || w == "cc" || w == "hi" && s.Language != "":
			s.Hearing = true
		case w == "default":
			s.Default = true
		case s.Language == "" && languageOf(w) != "":
			s.Language = languageOf(w)
		case isNumberWord(w):
			// A track number, such as the 2 of 2_English.srt.
		default:
			title = append(title, word)
		}
	}
	s.Title = strings.Join(title, " ")
}

func isNumberWord(w string) bool {
	return w != "" && strings.Trim(w, "0123456789") == ""
}

// languages are ISO 639-2 codes by the two-letter codes, the other
// three-letter codes and the English names that subtitle files are named
// with.
var languages = map[string]string{
	"en": "eng", "english": "eng",
	"fr": "fre", "fra": "fre", "french": "fre",
	"de": "ger", "deu": "ger", "german": "ger",
	"es": "spa", "spanish": "spa", "esp": "spa",
	"it": "ita", "italian": "ita",
	"pt": "por", "portuguese": "por", "pob": "por", "ptbr": "por",
	"nl": "dut", "nld": "dut", "dutch": "dut",
	"ar": "ara", "arabic": "ara",
	"hi": "hin", "hindi": "hin",
	"ur": "urd", "urdu": "urd",
	"pa": "pan", "punjabi": "pan",
	"tr": "tur", "turkish": "tur",
	"pl": "pol", "polish": "pol",
	"ru": "rus", "russian": "rus",
	"uk": "ukr", "ukrainian": "ukr",
	"zh": "chi", "zho": "chi", "chinese": "chi", "chs": "chi", "cht": "chi",
	"ja": "jpn", "japanese": "jpn",
	"ko": "kor", "korean": "kor",
	"sv": "swe", "swedish": "swe",
	"no": "nor", "nb": "nor", "nob": "nor", "norwegian": "nor",
	"da": "dan", "danish": "dan",
	"fi": "fin", "finnish": "fin",
	"el": "gre", "ell": "gre", "greek": "gre",
	"he": "heb", "hebrew": "heb",
	"cs": "cze", "ces": "cze", "czech": "cze",
	"sk": "slo", "slk": "slo", "slovak": "slo",
	"hu": "hun", "hungarian": "hun",
	"ro": "rum", "ron": "rum", "romanian": "rum",
	"bg": "bul", "bulgarian": "bul",
	"hr": "hrv", "croatian": "hrv",
	"sr": "srp", "serbian": "srp",
	"sl": "slv", "slovenian": "slv",
	"th": "tha", "thai": "tha",
	"vi": "vie", "vietnamese": "vie",
	"id": "ind", "indonesian": "ind",
	"ms": "may", "msa": "may", "malay": "may",
	"fa": "per", "fas": "per", "persian": "per",
	"ta": "tam", "tamil": "tam",
	"te": "tel", "telugu": "tel",
	"bn": "ben", "bengali": "ben",
	"et": "est", "estonian": "est",
	"lv": "lav", "latvian": "lav",
	"lt": "lit", "lithuanian": "lit",
	"is": "ice", "isl": "ice", "icelandic": "ice",
}

// languageOf returns the ISO 639-2 code of a word that names a language,
// or "".
func languageOf(w string) string {
	if code, ok := languages[w]; ok {
		return code
	}
	for _, code := range languages {
		if code == w {
			return code
		}
	}
	return ""
}

// ReadSidecar reads the subtitle file at p in the folder at dir, at most
// MaxSidecarBytes of it.
func ReadSidecar(dir, p string) ([]byte, error) {
	if !fs.ValidPath(p) || !slices.Contains(subtitleExtensions, extension(p)) {
		return nil, errors.New("not the path of a subtitle file in a folder")
	}
	f, err := Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	file, err := f.root.Open(p)
	if err != nil {
		return nil, ErrNoTitle
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxSidecarBytes+1))
	if err != nil {
		return nil, errors.New("the subtitle file cannot be read")
	}
	if len(data) > MaxSidecarBytes {
		return nil, errors.New("the subtitle file is too large")
	}
	return data, nil
}
