// SPDX-License-Identifier: AGPL-3.0-or-later

// Package media says what a film's or an episode's file holds, what a
// device's player plays, and how the hub plays a file that the player
// cannot play as it is (D68): ffmpeg copies its video, and its sound where
// the player plays it, converts the sound where it does not, and makes one
// MPEG-TS stream of them.
package media

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// Kinds of stream.
const (
	Video    = "video"
	Audio    = "audio"
	Subtitle = "subtitle"
)

const (
	// maxStreams caps the streams kept of a file.
	maxStreams = 64
	// maxText caps a stream's title, and maxName its codec, profile and
	// language.
	maxText = 120
	maxName = 40
)

// ProbeVersion is the version of what ParseProbe reads of a file. A file
// read by an older version is read again: version 2 added a video's
// dynamic range and frame rate, which a transcode needs (D73).
const ProbeVersion = 2

// Info is what a file holds, as ffprobe reads it.
type Info struct {
	// Probed is the version of ParseProbe that read it, 0 for the first.
	Probed int `json:"probed,omitempty"`
	// Format is the file's format as ffprobe names it, such as
	// "matroska,webm", or "mov,mp4,m4a,3gp,3g2,mj2" for MP4.
	Format string `json:"format"`
	// Duration is how long it lasts, in seconds, 0 if not known.
	Duration float64 `json:"duration,omitempty"`
	// Start is the time of the file's first moment on its streams' clocks,
	// in seconds, such as -0.021 or 1.4. Times in a file, as a player
	// counts them and as Overscan keeps them, are from its start; times on
	// its streams' clocks are this much later.
	Start float64 `json:"start,omitempty"`
	// Size is the file's size in bytes, 0 if not known. With ModTime, the
	// time of a file in a folder in Unix nanoseconds, it tells whether the
	// file has changed since it was read.
	Size    int64    `json:"size,omitempty"`
	ModTime int64    `json:"mtime,omitempty"`
	Streams []Stream `json:"streams"`
}

// Stream is one of a file's streams: a video, a sound track or subtitles.
type Stream struct {
	Index    int    `json:"index"` // its index in the file, which ffmpeg's -map takes
	Type     string `json:"type"`  // Video, Audio or Subtitle
	Codec    string `json:"codec"` // as ffmpeg names it, such as h264, dts or subrip
	Profile  string `json:"profile,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	BitDepth int    `json:"bit_depth,omitempty"` // bits a colour sample, 0 if not known
	Channels int    `json:"channels,omitempty"`
	Language string `json:"language,omitempty"` // as the file tags it, such as eng
	Title    string `json:"title,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
	// Picture marks a still picture that the file carries, such as its
	// cover, which ffprobe lists as a video.
	Picture bool `json:"picture,omitempty"`
	// HDR is a video's high dynamic range: HDRPQ for HDR10 and Dolby
	// Vision's base layer, HDRHLG for hybrid log-gamma, "" for standard.
	HDR string `json:"hdr,omitempty"`
	// FrameRate is a video's pictures a second, 0 if not known.
	FrameRate float64 `json:"fps,omitempty"`
}

// High dynamic ranges, by their transfer function.
const (
	HDRPQ  = "pq"
	HDRHLG = "hlg"
)

// probeOutput is the part of ffprobe's JSON (-show_format -show_streams)
// that Info takes. Numbers that ffprobe writes as strings are strings here.
type probeOutput struct {
	Streams []struct {
		Index       int               `json:"index"`
		CodecName   string            `json:"codec_name"`
		CodecType   string            `json:"codec_type"`
		Profile     string            `json:"profile"`
		Width       int               `json:"width"`
		Height      int               `json:"height"`
		PixFmt      string            `json:"pix_fmt"`
		Transfer    string            `json:"color_transfer"`
		AvgRate     string            `json:"avg_frame_rate"`
		RawRate     string            `json:"r_frame_rate"`
		RawBits     string            `json:"bits_per_raw_sample"`
		Channels    int               `json:"channels"`
		Disposition map[string]int    `json:"disposition"`
		Tags        map[string]string `json:"tags"`
	} `json:"streams"`
	Format struct {
		Name     string `json:"format_name"`
		Start    string `json:"start_time"`
		Duration string `json:"duration"`
		Size     string `json:"size"`
	} `json:"format"`
}

// ParseProbe reads what ffprobe says of a file, given -show_format,
// -show_streams and -print_format json. It keeps the video, sound and
// subtitle streams, at most 64 of them, and leaves out the rest, such as
// fonts.
func ParseProbe(data []byte) (Info, error) {
	var out probeOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return Info{}, fmt.Errorf("media: ffprobe's answer: %w", err)
	}
	info := Info{Probed: ProbeVersion, Format: clean(out.Format.Name, maxName)}
	if info.Format == "" {
		return Info{}, fmt.Errorf("media: ffprobe names no format")
	}
	if d, err := strconv.ParseFloat(out.Format.Duration, 64); err == nil && d > 0 && d < 1e7 && !math.IsNaN(d) {
		info.Duration = d
	}
	if t, err := strconv.ParseFloat(out.Format.Start, 64); err == nil && math.Abs(t) < 1e7 {
		info.Start = t
	}
	if n, err := strconv.ParseInt(out.Format.Size, 10, 64); err == nil && n > 0 {
		info.Size = n
	}
	for _, s := range out.Streams {
		switch s.CodecType {
		case Video, Audio, Subtitle:
		default:
			continue
		}
		if len(info.Streams) == maxStreams {
			break
		}
		if s.Index < 0 || s.Index > 1<<16 {
			continue
		}
		st := Stream{
			Index:    s.Index,
			Type:     s.CodecType,
			Codec:    clean(s.CodecName, maxName),
			Profile:  clean(s.Profile, maxName),
			Language: clean(tag(s.Tags, "language"), maxName),
			Title:    clean(tag(s.Tags, "title"), maxText),
			Default:  s.Disposition["default"] == 1,
			Forced:   s.Disposition["forced"] == 1,
		}
		if st.Language == "und" {
			st.Language = ""
		}
		switch s.CodecType {
		case Video:
			st.Width, st.Height = bounded(s.Width), bounded(s.Height)
			st.BitDepth = bitDepth(s.RawBits, s.PixFmt)
			st.Picture = s.Disposition["attached_pic"] == 1
			switch s.Transfer {
			case "smpte2084":
				st.HDR = HDRPQ
			case "arib-std-b67":
				st.HDR = HDRHLG
			}
			if st.FrameRate = frameRate(s.AvgRate); st.FrameRate == 0 {
				st.FrameRate = frameRate(s.RawRate)
			}
		case Audio:
			if s.Channels > 0 && s.Channels <= 64 {
				st.Channels = s.Channels
			}
		}
		info.Streams = append(info.Streams, st)
	}
	return info, nil
}

// frameRate reads a frame rate as ffprobe writes it, such as 24000/1001;
// 0 if it is not one.
func frameRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	d := 1.0
	var err2 error
	if ok {
		d, err2 = strconv.ParseFloat(den, 64)
	}
	if err1 != nil || err2 != nil || d <= 0 || n <= 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0
	}
	if r := n / d; r >= 1 && r <= 300 {
		return math.Round(r*1000) / 1000
	}
	return 0
}

// tag returns a stream's tag by its name, whatever its case.
func tag(tags map[string]string, name string) string {
	if v, ok := tags[name]; ok {
		return v
	}
	for k, v := range tags {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// bounded returns a picture's width or height, 0 if it makes no sense.
func bounded(n int) int {
	if n <= 0 || n > 1<<15 {
		return 0
	}
	return n
}

// pixDepth matches the bits a sample of a pixel format such as yuv420p10le
// or p010le.
var pixDepth = regexp.MustCompile(`p0?(\d{1,2})(?:le|be)?$`)

// bitDepth returns the bits a colour sample of a video, from ffprobe's
// bits_per_raw_sample or else its pixel format; 0 if neither says.
func bitDepth(raw, pixFmt string) int {
	if n, err := strconv.Atoi(raw); err == nil && n >= 6 && n <= 16 {
		return n
	}
	if m := pixDepth.FindStringSubmatch(pixFmt); m != nil {
		if n, _ := strconv.Atoi(m[1]); n >= 6 && n <= 16 {
			return n
		}
	}
	switch pixFmt {
	case "yuv420p", "yuvj420p", "yuv422p", "yuvj422p", "yuv444p", "yuvj444p", "nv12", "nv21", "yuv411p", "yuv410p", "gray":
		return 8
	}
	return 0
}

// clean returns text from a file's tags fit to keep and show: valid UTF-8,
// without control characters, trimmed, at most max bytes.
func clean(s string, max int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		cut := max
		for cut > 0 && !utf8Start(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut])
	}
	return s
}

// utf8Start reports whether b starts a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// First returns the first stream of a kind that is not a picture, or nil.
func (i Info) First(kind string) *Stream {
	for n := range i.Streams {
		if s := &i.Streams[n]; s.Type == kind && !s.Picture {
			return s
		}
	}
	return nil
}

// Of returns the streams of a kind, pictures left out.
func (i Info) Of(kind string) []Stream {
	var out []Stream
	for _, s := range i.Streams {
		if s.Type == kind && !s.Picture {
			out = append(out, s)
		}
	}
	return out
}

// Summary describes a file for the log, such as "matroska: h264
// 1920x1080 8-bit; sound dts 6ch eng, aac 2ch; subtitles subrip eng".
func (i Info) Summary() string {
	var b strings.Builder
	b.WriteString(strings.SplitN(i.Format, ",", 2)[0])
	b.WriteString(":")
	if v := i.First(Video); v != nil {
		b.WriteString(" " + v.Describe())
	} else {
		b.WriteString(" no video")
	}
	for _, kind := range []string{Audio, Subtitle} {
		list := i.Of(kind)
		if len(list) == 0 {
			continue
		}
		if kind == Audio {
			b.WriteString("; sound ")
		} else {
			b.WriteString("; subtitles ")
		}
		for n, s := range list {
			if n > 0 {
				b.WriteString(", ")
			}
			b.WriteString(s.Describe())
		}
	}
	return b.String()
}

// Describe names a stream for the log, such as "h264 1920x1080 10-bit" or
// "dts 6ch eng".
func (s Stream) Describe() string {
	d := s.Codec
	if d == "" {
		d = "unknown"
	}
	switch s.Type {
	case Video:
		if s.Width > 0 && s.Height > 0 {
			d += " " + strconv.Itoa(s.Width) + "x" + strconv.Itoa(s.Height)
		}
		if s.BitDepth > 0 {
			d += " " + strconv.Itoa(s.BitDepth) + "-bit"
		}
		if s.HDR != "" {
			d += " HDR"
		}
	case Audio:
		if s.Channels > 0 {
			d += " " + strconv.Itoa(s.Channels) + "ch"
		}
	}
	if s.Language != "" {
		d += " " + s.Language
	}
	return d
}
