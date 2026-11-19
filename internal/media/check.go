// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"slices"
	"strconv"
	"strings"
)

// A box's playback check (D70) plays short clips that ffmpeg makes on the
// server, and asks after each whether the picture moves or the tone sounds.
// What it finds is a set of capabilities, each found to play or not, which
// change the profile that the box's model starts from.

// Capabilities name what a player may or may not play: a file format, as
// ffprobe names it, such as "format:mov"; a video codec at 8 bits up to
// 1080p, such as "video:hevc"; a video codec at 10 bits or in 4K, such as
// "video:hevc:10bit" and "video:hevc:2160p"; or a sound codec, such as
// "audio:dts". Codecs are as ffmpeg names them.
const (
	capFormat = "format"
	capVideo  = "video"
	capAudio  = "audio"
	cap10bit  = "10bit"
	cap2160p  = "2160p"
)

// The largest picture of a video codec that plays in 4K, and of one that
// does not, rounded up to whole blocks of 16 lines as codecs store them.
const (
	width4K, height4K = 4096, 2176
	widthHD, heightHD = 1920, 1088
)

// capability splits a capability into its kind, its format or codec, and
// what it says of video: "", cap10bit or cap2160p.
func capability(c string) (kind, name, of string, ok bool) {
	parts := strings.Split(c, ":")
	if len(parts) < 2 || len(parts) > 3 || parts[1] == "" {
		return "", "", "", false
	}
	kind, name = parts[0], parts[1]
	if len(parts) == 3 {
		of = parts[2]
	}
	switch {
	case kind == capVideo && (of == "" || of == cap10bit || of == cap2160p):
	case (kind == capFormat || kind == capAudio) && of == "":
	default:
		return "", "", "", false
	}
	return kind, name, of, true
}

// Plays reports whether p plays a capability: a format or a sound codec it
// lists, or a video codec it lists, at 10 bits or in 4K if the capability
// says so. A capability it cannot read does not play.
func (p Profile) Plays(c string) bool {
	kind, name, of, ok := capability(c)
	if !ok {
		return false
	}
	switch kind {
	case capFormat:
		return slices.Contains(p.Formats, name)
	case capAudio:
		return p.PlaysAudio(name)
	}
	i := slices.IndexFunc(p.Video, func(v VideoCodec) bool { return v.Codec == name })
	if i < 0 {
		return false
	}
	v := p.Video[i]
	switch of {
	case cap10bit:
		return v.MaxBitDepth == 0 || v.MaxBitDepth >= 10
	case cap2160p:
		w, h := p.maxSize(v)
		return (w == 0 || w >= 3840) && (h == 0 || h >= 2160)
	}
	return true
}

// With returns p changed by what a playback check found: each capability
// found to play is added to it, and each found not to play is taken away.
// A video codec's own finding comes before what is found of it at 10 bits
// or in 4K. A codec added by the check plays at 8 bits up to 1080p unless
// it was found to play more. The name says where the findings come from.
func (p Profile) With(findings map[string]bool, name string) Profile {
	out := Profile{
		Name:     name,
		Formats:  slices.Clone(p.Formats),
		Video:    slices.Clone(p.Video),
		MaxWidth: p.MaxWidth, MaxHeight: p.MaxHeight,
		Audio: slices.Clone(p.Audio),
	}
	keys := make([]string, 0, len(findings))
	for c := range findings {
		keys = append(keys, c)
	}
	// "video:hevc" sorts before "video:hevc:10bit": a codec first, then
	// what is found of it.
	slices.Sort(keys)
	for _, c := range keys {
		kind, codec, of, ok := capability(c)
		if !ok {
			continue
		}
		plays := findings[c]
		switch kind {
		case capFormat:
			out.Formats = setIn(out.Formats, codec, plays)
		case capAudio:
			out.Audio = setIn(out.Audio, codec, plays)
		case capVideo:
			out.setVideo(codec, of, plays)
		}
	}
	return out
}

// setIn returns list with name in it or not.
func setIn(list []string, name string, in bool) []string {
	i := slices.Index(list, name)
	switch {
	case in && i < 0:
		return append(list, name)
	case !in && i >= 0:
		return slices.Delete(list, i, i+1)
	}
	return list
}

// setVideo changes what p plays of a video codec: the codec itself, or the
// codec at 10 bits or in 4K, which needs the codec.
func (p *Profile) setVideo(codec, of string, plays bool) {
	i := slices.IndexFunc(p.Video, func(v VideoCodec) bool { return v.Codec == codec })
	if of == "" {
		switch {
		case plays && i < 0:
			p.Video = append(p.Video, VideoCodec{Codec: codec, MaxBitDepth: 8})
		case !plays && i >= 0:
			p.Video = slices.Delete(p.Video, i, i+1)
		}
		return
	}
	if i < 0 {
		if !plays {
			return
		}
		p.Video = append(p.Video, VideoCodec{Codec: codec, MaxBitDepth: 8})
		i = len(p.Video) - 1
	}
	v := &p.Video[i]
	switch of {
	case cap10bit:
		switch {
		case plays && v.MaxBitDepth != 0 && v.MaxBitDepth < 10:
			v.MaxBitDepth = 10
		case !plays && (v.MaxBitDepth == 0 || v.MaxBitDepth > 8):
			v.MaxBitDepth = 8
		}
	case cap2160p:
		if plays {
			v.MaxWidth, v.MaxHeight = width4K, height4K
		} else {
			v.MaxWidth, v.MaxHeight = widthHD, heightHD
		}
	}
}

// What the page asks once a clip of the check plays.
const (
	AskPicture = "picture" // whether the picture moves
	AskSound   = "sound"   // whether the tone sounds
	AskBoth    = "both"    // both
)

// CheckClip is a clip of the playback check: what it tests, what it holds,
// and how ffmpeg makes it.
type CheckClip struct {
	// ID names the clip, and its file with Ext.
	ID string
	// Label says what it tests, for people, such as "H.265 at 10 bits".
	Label string
	// Ask is what the page asks once it plays: AskPicture, AskSound or
	// AskBoth.
	Ask string
	// Tests are the capabilities it tests.
	Tests []string
	// Needs are the capabilities that it holds besides, which a box must
	// play for the clip to say anything of Tests when it does not play.
	Needs []string
	// Ext is its file's extension, such as mkv.
	Ext string
	// Width and Height are the size of its picture.
	Width, Height int
	// Seconds is how long it plays.
	Seconds int
	// Video and Audio are how ffmpeg encodes its picture and its sound.
	Video, Audio Encoding
	// Muxer is ffmpeg's name for its format.
	Muxer string
	// Extra are ffmpeg's arguments for the format, if any.
	Extra []string
}

// Encoding is how ffmpeg encodes a stream: with the first of the encoders
// that it has.
type Encoding []Encoder

// Encoder is one of ffmpeg's encoders, with the arguments that go with it.
type Encoder struct {
	Name string
	Args []string
}

// Pick returns the first encoder of e that ffmpeg has, as has says.
func (e Encoding) Pick(has func(name string) bool) (Encoder, bool) {
	for _, enc := range e {
		if has(enc.Name) {
			return enc, true
		}
	}
	return Encoder{}, false
}

// The encodings of the clips. The picture is ffmpeg's moving test pattern
// and the sound a tone with a beep each second, both easy to tell apart
// from a still or silent player.
var (
	encH264 = Encoding{{"libx264", []string{"-preset", "veryfast", "-profile:v", "high", "-pix_fmt", "yuv420p",
		"-b:v", "800k", "-g", "50"}}}
	encH264x10 = Encoding{{"libx264", []string{"-preset", "veryfast", "-profile:v", "high10", "-pix_fmt", "yuv420p10le",
		"-b:v", "800k", "-g", "50"}}}
	encHEVC = Encoding{{"libx265", []string{"-preset", "ultrafast", "-pix_fmt", "yuv420p", "-b:v", "600k",
		"-tag:v", "hvc1", "-x265-params", "log-level=error:keyint=50"}}}
	encHEVCx10 = Encoding{{"libx265", []string{"-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-b:v", "600k",
		"-tag:v", "hvc1", "-x265-params", "log-level=error:keyint=50"}}}
	encHEVC4K = Encoding{{"libx265", []string{"-preset", "ultrafast", "-pix_fmt", "yuv420p", "-b:v", "4M",
		"-tag:v", "hvc1", "-x265-params", "log-level=error:keyint=50"}}}
	encMPEG2 = Encoding{{"mpeg2video", []string{"-b:v", "2M", "-g", "15"}}}
	encXvid  = Encoding{{"mpeg4", []string{"-vtag", "XVID", "-q:v", "4", "-bf", "2", "-g", "50"}}}
	encMPEG4 = Encoding{{"mpeg4", []string{"-q:v", "4", "-bf", "2", "-g", "50"}}}
	encVP9   = Encoding{{"libvpx-vp9", []string{"-deadline", "realtime", "-cpu-used", "8", "-b:v", "600k", "-g", "50"}}}
	encAV1   = Encoding{
		{"libaom-av1", []string{"-usage", "realtime", "-cpu-used", "8", "-b:v", "600k", "-g", "50"}},
		{"libsvtav1", []string{"-preset", "12", "-b:v", "600k", "-g", "50"}},
		{"librav1e", []string{"-speed", "10", "-b:v", "600k", "-g", "50"}},
	}

	encAAC    = Encoding{{"aac", []string{"-b:a", "128k", "-ac", "2"}}}
	encAC3    = Encoding{{"ac3", []string{"-b:a", "192k", "-ac", "2"}}}
	encEAC3   = Encoding{{"eac3", []string{"-b:a", "192k", "-ac", "2"}}}
	encMP2    = Encoding{{"mp2", []string{"-b:a", "192k", "-ac", "2"}}}
	encMP3    = Encoding{{"libmp3lame", []string{"-b:a", "128k", "-ac", "2"}}}
	encVorbis = Encoding{{"libvorbis", []string{"-q:a", "4", "-ac", "2"}}}
	encOpus   = Encoding{{"libopus", []string{"-b:a", "128k", "-ac", "2"}}}
	encFLAC   = Encoding{{"flac", []string{"-ac", "2"}}}
	encPCM    = Encoding{{"pcm_s16le", []string{"-ac", "2"}}}
	encDTS    = Encoding{{"dca", []string{"-strict", "-2", "-b:a", "768k", "-ac", "2"}}}
	encTrueHD = Encoding{{"truehd", []string{"-strict", "-2", "-ac", "2"}}}
)

// Clips of the check, other than its first, test one capability each and
// hold what the first found to play besides.
var (
	withSound   = []string{"format:matroska", "audio:aac"}
	withPicture = []string{"format:matroska", "video:h264"}
)

// CheckClips are the clips of the playback check, in the order the page
// plays them. The first holds what every box plays: a box that does not
// play it cannot be checked.
var CheckClips = []CheckClip{
	{ID: "base", Label: "MKV with H.264 and AAC", Ask: AskBoth,
		Tests: []string{"format:matroska", "video:h264", "audio:aac"}, Video: encH264, Audio: encAAC},

	// Sound, which decides most often whether a film is converted.
	{ID: "ac3", Label: "Dolby Digital (AC3)", Ask: AskSound, Tests: []string{"audio:ac3"}, Needs: withPicture, Video: encH264, Audio: encAC3},
	{ID: "eac3", Label: "Dolby Digital Plus (E-AC3)", Ask: AskSound, Tests: []string{"audio:eac3"}, Needs: withPicture, Video: encH264, Audio: encEAC3},
	{ID: "dts", Label: "DTS", Ask: AskSound, Tests: []string{"audio:dts"}, Needs: withPicture, Video: encH264, Audio: encDTS},
	{ID: "truehd", Label: "Dolby TrueHD", Ask: AskSound, Tests: []string{"audio:truehd"}, Needs: withPicture, Video: encH264, Audio: encTrueHD},
	{ID: "mp3", Label: "MP3", Ask: AskSound, Tests: []string{"audio:mp3"}, Needs: withPicture, Video: encH264, Audio: encMP3},
	{ID: "mp2", Label: "MPEG audio (MP2)", Ask: AskSound, Tests: []string{"audio:mp2"}, Needs: withPicture, Video: encH264, Audio: encMP2},
	{ID: "flac", Label: "FLAC", Ask: AskSound, Tests: []string{"audio:flac"}, Needs: withPicture, Video: encH264, Audio: encFLAC},
	{ID: "opus", Label: "Opus", Ask: AskSound, Tests: []string{"audio:opus"}, Needs: withPicture, Video: encH264, Audio: encOpus},
	{ID: "vorbis", Label: "Vorbis", Ask: AskSound, Tests: []string{"audio:vorbis"}, Needs: withPicture, Video: encH264, Audio: encVorbis},
	{ID: "pcm", Label: "PCM (uncompressed)", Ask: AskSound, Tests: []string{"audio:pcm_s16le"}, Needs: withPicture, Video: encH264, Audio: encPCM},

	// Pictures.
	{ID: "hevc", Label: "H.265 (HEVC)", Ask: AskPicture, Tests: []string{"video:hevc"}, Needs: withSound, Video: encHEVC, Audio: encAAC},
	{ID: "hevc-10bit", Label: "H.265 at 10 bits", Ask: AskPicture, Tests: []string{"video:hevc:10bit"},
		Needs: append(slices.Clone(withSound), "video:hevc"), Video: encHEVCx10, Audio: encAAC},
	{ID: "hevc-2160p", Label: "H.265 in 4K", Ask: AskPicture, Tests: []string{"video:hevc:2160p"},
		Needs: append(slices.Clone(withSound), "video:hevc"), Width: 3840, Height: 2160, Seconds: 6, Video: encHEVC4K, Audio: encAAC},
	{ID: "h264-10bit", Label: "H.264 at 10 bits", Ask: AskPicture, Tests: []string{"video:h264:10bit"},
		Needs: append(slices.Clone(withSound), "video:h264"), Video: encH264x10, Audio: encAAC},
	{ID: "mpeg2", Label: "MPEG-2 video", Ask: AskPicture, Tests: []string{"video:mpeg2video"}, Needs: withSound, Video: encMPEG2, Audio: encAAC},
	{ID: "mpeg4", Label: "MPEG-4 Part 2 (Xvid)", Ask: AskPicture, Tests: []string{"video:mpeg4"}, Needs: withSound, Video: encMPEG4, Audio: encAAC},
	{ID: "vp9", Label: "VP9", Ask: AskPicture, Tests: []string{"video:vp9"}, Needs: withSound, Video: encVP9, Audio: encAAC},
	{ID: "av1", Label: "AV1", Ask: AskPicture, Tests: []string{"video:av1"}, Needs: withSound, Video: encAV1, Audio: encAAC},

	// Formats, each with the codecs that such files have.
	{ID: "mp4", Label: "MP4", Ask: AskBoth, Tests: []string{"format:mov"}, Needs: []string{"video:h264", "audio:aac"},
		Ext: "mp4", Muxer: "mp4", Extra: []string{"-movflags", "+faststart"}, Video: encH264, Audio: encAAC},
	{ID: "ts", Label: "MPEG-TS", Ask: AskBoth, Tests: []string{"format:mpegts"}, Needs: []string{"video:h264", "audio:aac"},
		Ext: "ts", Muxer: "mpegts", Video: encH264, Audio: encAAC},
	{ID: "avi", Label: "AVI with Xvid and MP3", Ask: AskBoth, Tests: []string{"format:avi"}, Needs: []string{"video:mpeg4", "audio:mp3"},
		Ext: "avi", Muxer: "avi", Video: encXvid, Audio: encMP3},
	{ID: "vob", Label: "DVD video (MPEG-PS)", Ask: AskBoth, Tests: []string{"format:mpeg"}, Needs: []string{"video:mpeg2video", "audio:ac3"},
		Ext: "vob", Muxer: "vob", Video: encMPEG2, Audio: encAC3},
}

func init() {
	// Fill in what most clips share: an MKV file, 640×360, 10 seconds.
	for i := range CheckClips {
		c := &CheckClips[i]
		if c.Ext == "" {
			c.Ext, c.Muxer = "mkv", "matroska"
		}
		if c.Width == 0 {
			c.Width, c.Height = 640, 360
		}
		if c.Seconds == 0 {
			c.Seconds = 10
		}
	}
}

// CheckClipByID returns the clip of the check with an ID.
func CheckClipByID(id string) (CheckClip, bool) {
	i := slices.IndexFunc(CheckClips, func(c CheckClip) bool { return c.ID == id })
	if i < 0 {
		return CheckClip{}, false
	}
	return CheckClips[i], true
}

// Args returns ffmpeg's arguments to make the clip, with the encoders that
// has says ffmpeg has, at out; false if ffmpeg has none for its picture or
// its sound.
func (c CheckClip) Args(has func(encoder string) bool, out string) ([]string, bool) {
	v, ok := c.Video.Pick(has)
	if !ok {
		return nil, false
	}
	a, ok := c.Audio.Pick(has)
	if !ok {
		return nil, false
	}
	secs := strconv.Itoa(c.Seconds)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-nostats", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=" + strconv.Itoa(c.Width) + "x" + strconv.Itoa(c.Height) + ":rate=25:duration=" + secs,
		"-f", "lavfi", "-i", "sine=frequency=440:beep_factor=4:sample_rate=48000:duration=" + secs,
		"-map", "0:v", "-map", "1:a", "-c:v", v.Name}
	args = append(args, v.Args...)
	args = append(args, "-c:a", a.Name)
	args = append(args, a.Args...)
	args = append(args, "-metadata", "title=Overscan's playback check: "+c.Label)
	args = append(args, c.Extra...)
	return append(args, "-f", c.Muxer, out), true
}

// Outcomes of a clip of the check.
const (
	Plays = "plays" // the box played it
	Fails = "fails" // it did not
)

// Findings returns what a check found from the outcome of each of its
// clips, by clip ID: Plays, Fails, or "" when the check could not tell.
// Each capability of a clip that plays plays, and so does each that it
// needs. Those that a clip tests do not play when the clip does not, if
// the box plays all that the clip needs besides, as other clips found or
// else as base says. A capability that one clip found to play plays,
// whatever another found.
func Findings(outcomes map[string]string, base Profile) map[string]bool {
	found := map[string]bool{}
	for _, c := range CheckClips {
		if outcomes[c.ID] != Plays {
			continue
		}
		for _, k := range append(slices.Clone(c.Tests), c.Needs...) {
			found[k] = true
		}
	}
	for _, c := range CheckClips {
		if outcomes[c.ID] != Fails {
			continue
		}
		known := true
		for _, need := range c.Needs {
			plays, ok := found[need]
			if !ok {
				plays = base.Plays(need)
			}
			known = known && plays
		}
		if !known {
			continue
		}
		for _, k := range c.Tests {
			if !found[k] {
				found[k] = false
			}
		}
	}
	return found
}

// CapabilityLabel names a capability for people, such as "H.265 at 10
// bits"; "" for one the check does not test.
func CapabilityLabel(c string) string { return capabilityLabels[c] }

var capabilityLabels = map[string]string{
	"format:matroska": "MKV", "format:mov": "MP4", "format:mpegts": "MPEG-TS", "format:avi": "AVI",
	"format:mpeg": "DVD video (MPEG-PS)",
	"video:h264":  "H.264", "video:h264:10bit": "H.264 at 10 bits", "video:hevc": "H.265",
	"video:hevc:10bit": "H.265 at 10 bits", "video:hevc:2160p": "H.265 in 4K", "video:mpeg2video": "MPEG-2 video",
	"video:mpeg4": "MPEG-4 Part 2 (Xvid)", "video:vp9": "VP9", "video:av1": "AV1",
	"audio:aac": "AAC", "audio:ac3": "Dolby Digital", "audio:eac3": "Dolby Digital Plus", "audio:dts": "DTS",
	"audio:truehd": "Dolby TrueHD", "audio:mp3": "MP3", "audio:mp2": "MP2", "audio:flac": "FLAC",
	"audio:opus": "Opus", "audio:vorbis": "Vorbis", "audio:pcm_s16le": "PCM",
}

// Capabilities returns the capabilities that the check tests, in the order
// its clips test them.
func Capabilities() []string {
	var out []string
	for _, c := range CheckClips {
		for _, k := range c.Tests {
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	return out
}
