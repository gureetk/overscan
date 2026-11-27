// SPDX-License-Identifier: AGPL-3.0-or-later

// Package streamprofile holds the output profiles (D110): how the hub sends
// a live channel to a device or a player, as its source sends it, or made
// anew by the hub's ffmpeg, for a device that cannot play what a source
// sends or a network too slow for it. Three are fixed, as they have been
// since D110; the administrator makes the others, of a codec, a largest
// size, a quality, a frame rate, deinterlacing, the dynamic range and the
// sound (D147).
package streamprofile

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// Source is the profile that sends a channel as its source sends it.
const Source = "source"

// The codecs of profiles that the administrator makes.
const (
	H264 = "h264"
	HEVC = "hevc"
)

// The qualities of profiles that the administrator makes.
const (
	High   = "high"
	Medium = "medium"
	Low    = "low"
)

// The frame rates of profiles that the administrator makes: as the source
// sends it, which makes a picture of each field of an interlaced picture,
// or at most 30 pictures a second, which makes one of each frame and
// halves a faster progressive rate.
const (
	RateSource = "source"
	RateAtMost = "30"
)

// The sound of profiles that the administrator makes: the first sound
// track as AAC in stereo or as Dolby Digital in up to 5.1, or every sound
// track as the source sends it.
const (
	SoundAAC  = "aac"
	SoundAC3  = "ac3"
	SoundCopy = "copy"
)

// Lines are the largest sizes that a profile the administrator makes can
// have, in lines; 0 keeps the source's.
var Lines = []int{0, 1080, 720, 576}

// Limits on a profile that the administrator makes.
const (
	MinKbps = 500
	MaxKbps = 40000
	maxName = 40
)

// Profile is an output profile.
type Profile struct {
	Name  string // as the store and the API know it
	Label string // as the admin offers it

	// For the fixed profiles that transcode (D110): the largest picture,
	// in pixels, and libx264's quality, its limit on the rate, the buffer
	// that the limit is kept over, and the level.
	Width, Height int
	CRF           int
	MaxRate       string
	BufSize       string
	Level         string

	// Made is set for a profile that the administrator made (D147), whose
	// settings follow.
	Made bool
	// Codec is H264 or HEVC.
	Codec string
	// MaxLines is its largest size, in lines: 1080, 720 or 576, or 0 for
	// the source's. A picture is never made larger than the source's.
	MaxLines int
	// Quality is High, Medium or Low.
	Quality string
	// MaxKbps is the highest bit rate of its video, in kbit/s; 0 for none.
	MaxKbps int
	// FrameRate is RateSource or RateAtMost.
	FrameRate string
	// Deinterlace says that an interlaced picture is deinterlaced.
	Deinterlace bool
	// ToneMap says that an HDR picture is made SDR, which only graphics
	// do for live channels: in software, a live HDR picture keeps its
	// dynamic range.
	ToneMap bool
	// Sound is SoundAAC, SoundAC3 or SoundCopy.
	Sound string
}

// Profiles are the fixed output profiles, in the order that the admin
// offers them.
var Profiles = []Profile{
	{Name: Source, Label: "As the source sends it"},
	{Name: "h264-720p", Label: "H.264, up to 720p", Width: 1280, Height: 720, CRF: 23, MaxRate: "3M", BufSize: "6M", Level: "4.0"},
	{Name: "h264-1080p", Label: "H.264, up to 1080p", Width: 1920, Height: 1080, CRF: 21, MaxRate: "6M", BufSize: "12M", Level: "4.1"},
}

// Find returns the fixed profile with a name.
func Find(name string) (Profile, bool) {
	for _, p := range Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Transcodes reports whether the profile makes the stream anew.
func (p Profile) Transcodes() bool { return p.Width > 0 || p.Made }

// VideoCodec returns the codec that the profile makes, "" for one that does
// not transcode.
func (p Profile) VideoCodec() string {
	switch {
	case p.Made:
		return p.Codec
	case p.Transcodes():
		return H264
	}
	return ""
}

// Encoder returns the name of the ffmpeg encoder that the profile needs in
// software: libx264 or libx265; "" for one that does not transcode.
func (p Profile) Encoder() string {
	switch p.VideoCodec() {
	case H264:
		return "libx264"
	case HEVC:
		return "libx265"
	}
	return ""
}

// reserved are the names that a profile the administrator makes cannot
// have: the fixed profiles', and the words that settings use beside them.
var reserved = []string{Source, "h264-720p", "h264-1080p", "never", "always", "as-needed", "as_needed", "none", "default"}

var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Error is what is wrong with a profile, which the administrator can put
// right.
type Error struct{ Text string }

func (e *Error) Error() string { return e.Text }

func bad(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// CheckName checks the name of a profile that the administrator makes:
// lower-case letters, digits and hyphens, at most 40, not a fixed
// profile's name or a word that settings use.
func CheckName(name string) error {
	switch {
	case name == "":
		return bad("Give the profile a name.")
	case len(name) > maxName:
		return bad("A profile's name has at most %d characters.", maxName)
	case !nameRE.MatchString(name):
		return bad("A profile's name has lower-case letters, digits and hyphens, such as hevc-1080p, and starts and ends with a letter or a digit.")
	case slices.Contains(reserved, name):
		return bad("%s is a name that Overscan uses itself; choose another.", name)
	}
	return nil
}

// Check checks a profile that the administrator makes, and fills in what
// it leaves out: H.264, the source's size, medium quality, the source's
// frame rate, deinterlacing, HDR made SDR, and AAC in stereo.
func Check(p Profile) (Profile, error) {
	if err := CheckName(p.Name); err != nil {
		return Profile{}, err
	}
	p.Made = true
	p.Label = p.Name
	p.Width, p.Height, p.CRF, p.MaxRate, p.BufSize, p.Level = 0, 0, 0, "", "", ""
	if p.Codec == "" {
		p.Codec = H264
	}
	if p.Quality == "" {
		p.Quality = Medium
	}
	if p.FrameRate == "" {
		p.FrameRate = RateSource
	}
	if p.Sound == "" {
		p.Sound = SoundAAC
	}
	switch {
	case p.Codec != H264 && p.Codec != HEVC:
		return Profile{}, bad("A profile's codec is h264 or hevc.")
	case !slices.Contains(Lines, p.MaxLines):
		return Profile{}, bad("A profile's largest size is 1080, 720 or 576 lines, or 0 for the source's.")
	case p.Quality != High && p.Quality != Medium && p.Quality != Low:
		return Profile{}, bad("A profile's quality is high, medium or low.")
	case p.MaxKbps != 0 && (p.MaxKbps < MinKbps || p.MaxKbps > MaxKbps):
		return Profile{}, bad("A profile's highest bit rate is from %d to %d kbit/s, or 0 for none.", MinKbps, MaxKbps)
	case p.FrameRate != RateSource && p.FrameRate != RateAtMost:
		return Profile{}, bad("A profile's frame rate is source or 30.")
	case p.Sound != SoundAAC && p.Sound != SoundAC3 && p.Sound != SoundCopy:
		return Profile{}, bad("A profile's sound is aac, ac3 or copy.")
	}
	return p, nil
}

// width returns the width that fits a 16:9 picture of the profile's
// largest size.
func (p Profile) width() int { return widthOf(p.MaxLines) }

// Describe says what a profile makes, such as "HEVC, up to 1080p, high
// quality, at most 8 Mbit/s, at most 30 pictures a second, AAC in stereo".
func (p Profile) Describe() string {
	if !p.Made {
		if !p.Transcodes() {
			return "as the source sends it"
		}
		return p.Label + ", with AAC in stereo"
	}
	parts := []string{map[string]string{H264: "H.264", HEVC: "HEVC"}[p.Codec]}
	if p.MaxLines > 0 {
		parts = append(parts, "up to "+strconv.Itoa(p.MaxLines)+"p")
	} else {
		parts = append(parts, "at the source's size")
	}
	parts = append(parts, p.Quality+" quality")
	if p.MaxKbps > 0 {
		parts = append(parts, "at most "+kbps(p.MaxKbps))
	}
	if p.FrameRate == RateAtMost {
		parts = append(parts, "at most 30 pictures a second")
	}
	if !p.Deinterlace {
		parts = append(parts, "not deinterlaced")
	}
	if !p.ToneMap {
		parts = append(parts, "HDR kept")
	}
	parts = append(parts, map[string]string{SoundAAC: "AAC in stereo", SoundAC3: "Dolby Digital", SoundCopy: "its sound as the source sends it"}[p.Sound])
	return strings.Join(parts, ", ")
}

// kbps writes a bit rate for people, such as "8 Mbit/s" or "1.5 Mbit/s".
func kbps(n int) string {
	if n%1000 == 0 {
		return strconv.Itoa(n/1000) + " Mbit/s"
	}
	if n >= 1000 {
		return strconv.FormatFloat(float64(n)/1000, 'f', -1, 64) + " Mbit/s"
	}
	return strconv.Itoa(n) + " kbit/s"
}

// crf is the constant rate factor of a quality: libx264's, or libx265's,
// whose scale gives about the same picture at about 4 more.
func (p Profile) crf() int {
	n := map[string]int{High: 20, Medium: 23, Low: 27}[p.Quality]
	if p.Codec == HEVC {
		n += 4
	}
	return n
}

// Args returns ffmpeg's arguments to transcode a live stream to the
// profile: MPEG-TS from standard input, with pipes the only protocol
// allowed, as stream repair reads it (D57), and MPEG-TS on standard output.
//
// A fixed profile (D110) deinterlaces its first video track where it is
// interlaced, scales it down to fit the profile's size, never up, and
// makes H.264 of it with libx264, with a picture that starts anew every two
// seconds, so that a player can start soon; it makes its first sound track
// AAC in stereo. A profile that the administrator made does as its
// settings say (madeArgs).
func (p Profile) Args() []string {
	if p.Made {
		return p.madeArgs()
	}
	fit := "scale=w='min(" + strconv.Itoa(p.Width) + ",iw)':h='min(" + strconv.Itoa(p.Height) + ",ih)'" +
		":force_original_aspect_ratio=decrease:force_divisible_by=2"
	return []string{
		"-hide_banner", "-nostdin", "-nostats", "-loglevel", "warning",
		"-protocol_whitelist", "pipe",
		"-fflags", "+genpts",
		"-analyzeduration", "2000000", "-probesize", "4000000",
		"-f", "mpegts", "-i", "pipe:0",
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-vf", "bwdif=deint=interlaced," + fit + ",format=yuv420p",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(p.CRF),
		"-maxrate", p.MaxRate, "-bufsize", p.BufSize, "-profile:v", "high", "-level:v", p.Level,
		"-force_key_frames", "expr:gte(t,n_forced*2)",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2",
		"-max_interleave_delta", "2000000",
		"-f", "mpegts", "pipe:1",
	}
}

// HalfFast is the fps filter's rate that keeps a rate of up to 30 pictures
// a second and halves a faster one, so that 50 becomes 25 and 59.94
// becomes 29.97: every second picture, evenly.
const HalfFast = "fps=fps='if(gt(source_fps,30),source_fps/2,source_fps)'"

// VideoFilter returns the filters that a profile that the administrator
// made runs on the pictures in software: deinterlacing, a picture of each
// field or of each frame, where the picture is interlaced; the frame rate
// kept to at most 30 a second, steadily, where it says so; the picture
// scaled down to fit its largest size, never up; and 8-bit 4:2:0.
func (p Profile) VideoFilter() string {
	var f []string
	if p.Deinterlace {
		mode := "send_field"
		if p.FrameRate == RateAtMost {
			mode = "send_frame"
		}
		f = append(f, "bwdif=mode="+mode+":deint=interlaced")
	}
	if p.FrameRate == RateAtMost {
		f = append(f, HalfFast)
	}
	if w := p.width(); w > 0 {
		f = append(f, "scale=w='min("+strconv.Itoa(w)+",iw)':h='min("+strconv.Itoa(p.MaxLines)+",ih)'"+
			":force_original_aspect_ratio=decrease:force_divisible_by=2")
	}
	return strings.Join(append(f, "format=yuv420p"), ",")
}

// madeArgs returns ffmpeg's arguments for a profile that the administrator
// made: its first video track through VideoFilter, made H.264 with
// libx264 or HEVC with libx265 at its quality, capped at its highest bit
// rate if it has one, with a picture that starts anew every two seconds;
// and its sound as it says.
func (p Profile) madeArgs() []string {
	args := []string{
		"-hide_banner", "-nostdin", "-nostats", "-loglevel", "warning",
		"-protocol_whitelist", "pipe",
		"-fflags", "+genpts",
		"-analyzeduration", "2000000", "-probesize", "4000000",
		"-f", "mpegts", "-i", "pipe:0",
		"-map", "0:v:0?",
	}
	if p.Sound == SoundCopy {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(args, "-vf", p.VideoFilter())
	switch p.Codec {
	case HEVC:
		args = append(args, "-c:v", "libx265", "-preset", "veryfast", "-crf", strconv.Itoa(p.crf()),
			"-profile:v", "main", "-x265-params", "log-level=error")
	default:
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(p.crf()), "-profile:v", "high")
	}
	if p.MaxKbps > 0 {
		args = append(args, "-maxrate", strconv.Itoa(p.MaxKbps)+"k", "-bufsize", strconv.Itoa(2*p.MaxKbps)+"k")
	}
	args = append(args, "-force_key_frames", "expr:gte(t,n_forced*2)")
	return append(args, p.soundArgs()...)
}
