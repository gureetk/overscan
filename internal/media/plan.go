// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/streamprofile"
)

// Profile is what a device's player plays as it is.
type Profile struct {
	// Name says where the profile comes from, for the log.
	Name string
	// Formats are the file formats it plays, as ffprobe names them. A
	// format that ffprobe names with several, such as
	// "mov,mp4,m4a,3gp,3g2,mj2", matches if one of them is here.
	Formats []string
	// Video are the video codecs it plays.
	Video []VideoCodec
	// MaxWidth and MaxHeight cap the size of the pictures it plays, for a
	// video codec without caps of its own.
	MaxWidth, MaxHeight int
	// Audio are the sound codecs it plays, as ffmpeg names them.
	Audio []string
}

// VideoCodec is a video codec that a player plays.
type VideoCodec struct {
	Codec string // as ffmpeg names it
	// MaxBitDepth caps the bits a colour sample: 8, or 10 for HDR and
	// such; 0 for any.
	MaxBitDepth int
	// MaxWidth and MaxHeight cap the size of its pictures, such as 4096×2176
	// for a codec that plays in 4K; 0 for the profile's caps.
	MaxWidth, MaxHeight int
}

// MAG322 is what the MAG322's player plays, as Infomir's datasheet lists
// it: MKV, MPEG-TS and M2TS, MPEG-PS and VOB, AVI, MOV and MP4, ASF and
// WMV; MPEG-1 and MPEG-2 video, H.264 (High profile, level 4.1), MPEG-4
// part 2 (XviD), VC-1, WMV9 and H.265; MPEG audio layers I to III, Dolby
// Digital, AAC (M4A), Ogg Vorbis and WAV, and Opus since firmware 2.20.09.
// The box shows 1080p at most. The datasheet does not list DTS, Dolby
// TrueHD, Dolby Digital Plus or FLAC, so they are converted; nor does it
// say how deep H.265's colour may be, so 10 bits are assumed to play. A
// box's playback check measures its own (D70).
var MAG322 = Profile{
	Name:    "MAG322 (datasheet)",
	Formats: []string{"matroska", "mpegts", "mpeg", "avi", "mov", "asf"},
	Video: []VideoCodec{{Codec: "h264", MaxBitDepth: 8}, {Codec: "hevc", MaxBitDepth: 10}, {Codec: "mpeg1video"},
		{Codec: "mpeg2video"}, {Codec: "mpeg4"}, {Codec: "vc1"}, {Codec: "wmv3"}},
	MaxWidth: 1920, MaxHeight: 1088,
	Audio: []string{"aac", "ac3", "mp1", "mp2", "mp3", "vorbis", "opus", "pcm_s16le"},
}

// Player is what a player of an output or a Stalker client plays, as far
// as a transcode with a profile asks (D151): every sound that MPEG-TS
// carries. Such a player is never asked what it plays, so nothing else of
// it is known.
var Player = Profile{
	Name:    "a player",
	Formats: []string{"matroska", "mpegts", "mpeg", "avi", "mov", "asf"},
	Video: []VideoCodec{{Codec: "h264"}, {Codec: "hevc"}, {Codec: "mpeg1video"}, {Codec: "mpeg2video"}, {Codec: "mpeg4"},
		{Codec: "vc1"}, {Codec: "wmv3"}, {Codec: "av1"}, {Codec: "vp9"}},
	Audio: tsAudio,
}

// ProfileFor returns the profile that a device's model starts from, by the
// model as the box reports it: the MAG322's for every one, the only model
// whose datasheet Overscan has. A box's playback check changes it (D70).
func ProfileFor(model string) Profile { return MAG322 }

// PlaysFormat reports whether p plays files in format, as ffprobe names it.
func (p Profile) PlaysFormat(format string) bool {
	for name := range strings.SplitSeq(format, ",") {
		if slices.Contains(p.Formats, name) {
			return true
		}
	}
	return false
}

// PlaysVideo reports whether p plays a video stream.
func (p Profile) PlaysVideo(s Stream) bool {
	i := slices.IndexFunc(p.Video, func(c VideoCodec) bool { return c.Codec == s.Codec })
	if i < 0 {
		return false
	}
	if max := p.Video[i].MaxBitDepth; max > 0 && s.BitDepth > max {
		return false
	}
	w, h := p.maxSize(p.Video[i])
	if w > 0 && s.Width > w || h > 0 && s.Height > h {
		return false
	}
	return true
}

// maxSize returns the caps of the size of v's pictures: its own, or else
// the profile's.
func (p Profile) maxSize(v VideoCodec) (width, height int) {
	if v.MaxWidth > 0 || v.MaxHeight > 0 {
		return v.MaxWidth, v.MaxHeight
	}
	return p.MaxWidth, p.MaxHeight
}

// PlaysAudio reports whether p plays a sound codec.
func (p Profile) PlaysAudio(codec string) bool { return slices.Contains(p.Audio, codec) }

// The codecs that ffmpeg copies into the MPEG-TS stream it makes, which
// MPEG-TS carries and players take there. A converted stream copies a
// sound track the player plays only if it is one of these, and converts
// the others.
var (
	tsVideo = []string{"h264", "hevc", "mpeg1video", "mpeg2video", "mpeg4", "vc1"}
	tsAudio = []string{"aac", "ac3", "eac3", "mp1", "mp2", "mp3"}
)

// maxTracks caps the sound tracks of a converted stream.
const maxTracks = 8

// Plan is how the hub plays a file for a player.
type Plan struct {
	// Convert says that the file goes through ffmpeg. Otherwise the player
	// gets it as it is.
	Convert bool
	// Why says what of the file the player cannot play, other than its
	// video, such as "its sound (dts)"; "" if nothing. A file goes as it is
	// all the same when its video cannot be copied into the stream, or
	// would not play either.
	Why string
	// Video is the index of the video stream that a converted stream
	// carries, -1 if none.
	Video int
	// Tracks are the sound tracks that a converted stream carries, in its
	// order: the one that plays first, then the others as the file has
	// them.
	Tracks []Track
	// VideoNote, when not "", says that the player may not play the
	// video, such as "hevc 3840x2160", which Overscan's ffmpeg does not
	// convert.
	VideoNote string
	// Transcode says that the source's server transcodes the video,
	// which the player cannot play, with one sound track at a time, which
	// the device picks by asking for the stream again (D71). Tracks are
	// then those it may pick from, and Convert says which the server
	// converts; Sound are the codecs it may send them in.
	Transcode bool
	Sound     []string
	// Encode says that ffmpeg transcodes the video, which the player cannot
	// play, to H.264 (D73); Convert is then true too. Width and Height are
	// the size of its pictures, 0 to keep the video's own. ToneMap says
	// that ffmpeg maps the video's high dynamic range to the standard one,
	// and FrameRate is the video's, which picks the H.264 level.
	Encode        bool
	Width, Height int
	ToneMap       bool
	FrameRate     float64
	// Profile, for a title that its setting transcodes always (D150), is
	// the profile that ffmpeg makes it with; the zero Profile for D73's
	// transcode of what a box cannot play.
	Profile streamprofile.Profile
	// Muxrate, when above 0, is the constant rate, in bits a second, of a
	// transcode that a player seeks in by bytes (D151): each byte of the
	// stream is then at a time that its place says, as MakeSeekable sets
	// it.
	Muxrate int
	// Hardware, if not nil, has the server's graphics transcode it (D148,
	// D150): the hub sets it as a transcode starts. Its ToneMap says that
	// they map the video's HDR10 to SDR.
	Hardware *streamprofile.Hardware
}

// Track is a sound track of a converted stream.
type Track struct {
	Index    int    // the stream's index in the file
	Codec    string // as the file has it
	Channels int    // as the file has it, 0 if not known
	Language string
	Title    string
	// Convert says that ffmpeg converts it to Dolby Digital (AC3);
	// otherwise it copies it.
	Convert bool
}

// Decide returns how the hub plays a file that info describes for a player
// that plays p. It goes as it is if the player plays its format, its video
// and the sound track that plays first: the file's default, or else its
// first. Otherwise ffmpeg copies the video into MPEG-TS with every sound
// track, converting those the player cannot play. Video that the player
// cannot play is not converted: the file goes as it is, with a note.
func Decide(info Info, p Profile) Plan {
	plan := Plan{Video: -1}
	video := info.First(Video)
	if video != nil && !p.PlaysVideo(*video) {
		plan.VideoNote = video.Describe()
	}
	order := soundOrder(info)
	var why []string
	if !p.PlaysFormat(info.Format) {
		why = append(why, "its format ("+strings.SplitN(info.Format, ",", 2)[0]+")")
	}
	if len(order) > 0 && !p.PlaysAudio(order[0].Codec) {
		why = append(why, "its sound ("+order[0].Codec+")")
	}
	plan.Why = strings.Join(why, " and ")
	if len(why) == 0 || plan.VideoNote != "" || video != nil && !slices.Contains(tsVideo, video.Codec) {
		// Nothing to convert, or the video would not play anyway, or
		// cannot be copied: the file goes as it is.
		return plan
	}
	plan.Convert = true
	if video != nil {
		plan.Video = video.Index
	}
	plan.Tracks = streamTracks(order, p)
	return plan
}

// soundOrder returns the sound tracks of a file in the order a stream made
// of it carries them: the one that plays first, its default or else its
// first, then the others as the file has them.
func soundOrder(info Info) []Stream {
	tracks := info.Of(Audio)
	first := 0
	for i, t := range tracks {
		if t.Default {
			first = i
			break
		}
	}
	order := make([]Stream, 0, len(tracks))
	if len(tracks) > 0 {
		order = append(order, tracks[first])
		order = append(order, tracks[:first]...)
		order = append(order, tracks[first+1:]...)
	}
	return order
}

// streamTracks returns the sound tracks of a stream for a player that
// plays p, at most maxTracks of them, each converted unless the player
// plays it from MPEG-TS.
func streamTracks(order []Stream, p Profile) []Track {
	var out []Track
	for _, t := range order[:min(len(order), maxTracks)] {
		out = append(out, Track{
			Index: t.Index, Codec: t.Codec, Channels: t.Channels, Language: t.Language, Title: t.Title,
			Convert: !p.PlaysAudio(t.Codec) || !slices.Contains(tsAudio, t.Codec),
		})
	}
	return out
}

// TranscodePlan returns how the hub plays, through the source's server
// (D71), a file whose video a player that plays p cannot play: the server
// makes H.264 of its video, in MPEG-TS, with one of its sound tracks, as
// Decide orders them, copied if the player plays it from MPEG-TS, or else
// converted.
func TranscodePlan(info Info, p Profile) Plan {
	plan := Plan{Transcode: true, Video: -1, Why: "its video", Sound: TranscodeAudio(p)}
	if video := info.First(Video); video != nil {
		plan.Video = video.Index
		plan.VideoNote = video.Describe()
		plan.Why = "its video (" + plan.VideoNote + ")"
	}
	plan.Tracks = streamTracks(soundOrder(info), p)
	return plan
}

// The largest pictures that Overscan's own transcodes make (D73).
const (
	maxEncodeWidth  = 1920
	maxEncodeHeight = 1080
)

// EncodePlan returns how the hub plays a file whose video a player that
// plays p cannot play, when Overscan's own ffmpeg transcodes it (D73):
// ffmpeg makes H.264 of the video, at most 1920×1080 at 8 bits, mapped to
// the standard dynamic range when the video's is high and toneMap says
// that ffmpeg can, in MPEG-TS with every sound track that Decide's
// converted stream carries. It converts every one of them: ffmpeg starts a
// copied track at the picture before the time asked, and the transcoded
// video at that very time. A file without a video plays as Decide says.
func EncodePlan(info Info, p Profile, toneMap bool) Plan {
	plan := Decide(info, p)
	video := info.First(Video)
	if video == nil {
		return plan
	}
	plan.VideoNote = video.Describe()
	why := "its video (" + plan.VideoNote + ")"
	if plan.Why != "" {
		why += " and " + plan.Why
	}
	plan.Why = why
	plan.Convert, plan.Encode = true, true
	plan.Video = video.Index
	plan.Width, plan.Height = fit(video.Width, video.Height, maxEncodeWidth, maxEncodeHeight)
	plan.ToneMap = toneMap && video.HDR != ""
	plan.FrameRate = video.FrameRate
	plan.Tracks = streamTracks(soundOrder(info), p)
	for i := range plan.Tracks {
		plan.Tracks[i].Convert = true
	}
	return plan
}

// ProfilePlan returns how the hub plays a file whose title's setting
// transcodes it always with prof (D150), whether or not a player that
// plays p would play it as it is: ffmpeg makes prof's codec of its video,
// scaled down to fit prof's largest size, with prof's frame rate and
// deinterlacing, mapped to the standard dynamic range where prof says so
// and toneMap says that ffmpeg can, in MPEG-TS with every sound track that
// Decide's converted stream carries: each converted to AAC in stereo where
// prof says so and the box plays AAC, to Dolby Digital where prof says so
// or the box does not play AAC, or, where prof keeps the source's sound,
// copied as Decide's converted stream copies it. A file without a video
// plays as Decide says.
func ProfilePlan(info Info, p Profile, prof streamprofile.Profile, toneMap bool) Plan {
	plan := Decide(info, p)
	video := info.First(Video)
	if video == nil {
		return plan
	}
	why := "its setting (always, with " + prof.Name + ")"
	if plan.Why != "" {
		why += " and " + plan.Why
	}
	plan.Why = why
	plan.Convert, plan.Encode = true, true
	plan.Video = video.Index
	plan.Width, plan.Height = 0, 0
	if w, h := prof.Box(); w > 0 {
		plan.Width, plan.Height = fit(video.Width, video.Height, w, h)
	}
	plan.ToneMap = toneMap && prof.ToneMap && video.HDR != ""
	plan.FrameRate = video.FrameRate
	// The profile's sound, as far as the box plays it: AAC in stereo where
	// it plays AAC, and else Dolby Digital; as the source sends it where it
	// plays that from MPEG-TS, as Decide says, and else Dolby Digital.
	if prof.Sound == streamprofile.SoundAAC && !p.PlaysAudio("aac") {
		prof.Sound = streamprofile.SoundAC3
	}
	plan.Profile = prof
	plan.Tracks = streamTracks(soundOrder(info), p)
	if prof.Sound != streamprofile.SoundCopy {
		for i := range plan.Tracks {
			plan.Tracks[i].Convert = true
		}
	}
	return plan
}

// D73Profile is D73's transcode of what a box cannot play as a profile, for
// the graphics: H.264 up to 1080p, at a high quality and at most 8 Mbit/s,
// with every sound track as Dolby Digital.
var D73Profile = streamprofile.Profile{Name: "D73", Made: true, Codec: streamprofile.H264, MaxLines: 1080,
	Quality: streamprofile.High, MaxKbps: 8000, FrameRate: streamprofile.RateSource, Deinterlace: true,
	ToneMap: true, Sound: streamprofile.SoundAC3}

// EncodeProfile returns the profile that a transcode makes: Profile, or
// D73Profile for D73's.
func (p Plan) EncodeProfile() streamprofile.Profile {
	if p.Profile.Made {
		return p.Profile
	}
	return D73Profile
}

// fit returns the size of a picture of width by height that fits within
// maxWidth by maxHeight, keeping its shape, in even numbers as H.264 at
// 4:2:0 needs; 0 and 0 if the size is not known.
func fit(width, height, maxWidth, maxHeight int) (int, int) {
	if width <= 0 || height <= 0 {
		return 0, 0
	}
	w, h := float64(width), float64(height)
	if scale := math.Min(float64(maxWidth)/w, float64(maxHeight)/h); scale < 1 {
		w, h = w*scale, h*scale
	}
	even := func(f float64) int { return max(2, int(math.Round(f/2))*2) }
	return min(even(w), maxWidth), min(even(h), maxHeight)
}

// TranscodeAudio returns the sound codecs, as ffmpeg names them, in which
// the source's server sends a transcoded stream's sound for a player that
// plays p: a track in one of them is copied, and others are converted to
// the first, Dolby Digital if the player plays it.
func TranscodeAudio(p Profile) []string {
	var out []string
	for _, c := range []string{"ac3", "aac", "eac3", "mp3", "mp2"} {
		if p.PlaysAudio(c) && slices.Contains(tsAudio, c) {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		out = []string{"ac3"}
	}
	return out
}

// formatWhitelist names the demuxers that ffmpeg and ffprobe may use: those
// of video files. A file that is in truth a playlist, which would make them
// open other addresses, is refused.
const formatWhitelist = "matroska,mov,avi,mpegts,mpeg,asf,flv,ogg"

// common are the arguments that start every run of ffmpeg and ffprobe: the
// file comes from the hub over HTTP, and nothing else may be opened.
func common(loglevel string) []string {
	return []string{"-hide_banner", "-loglevel", loglevel,
		"-protocol_whitelist", "http,tcp", "-format_whitelist", formatWhitelist}
}

// ProbeArgs are ffprobe's arguments to read what the file at input holds,
// as JSON for ParseProbe.
func ProbeArgs(input string) []string {
	return append(common("error"),
		"-probesize", "10000000", "-analyzeduration", "10000000",
		"-show_format", "-show_streams", "-print_format", "json", input)
}

// KeyframeArgs are ffprobe's arguments to find where a converted stream of
// the file at input that is asked to start at the time at truly starts: at
// the picture of video stream video at or before it, from which ffmpeg
// copies. Times are in seconds from the file's start. ParseKeyframe reads
// what ffprobe answers.
func (i Info) KeyframeArgs(input string, video int, at float64) []string {
	return append(common("error"),
		"-select_streams", strconv.Itoa(video),
		"-show_entries", "packet=pts_time,dts_time",
		"-read_intervals", Seconds(at+i.Start)+"%+#1",
		"-print_format", "csv=p=0", input)
}

// ParseKeyframe returns the time that KeyframeArgs found, in seconds from
// the file's start.
func (i Info) ParseKeyframe(out []byte) (float64, bool) {
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	for field := range strings.SplitSeq(strings.TrimSpace(line), ",") {
		if t, err := strconv.ParseFloat(field, 64); err == nil && t-i.Start >= 0 && t < 1e7 {
			return t - i.Start, true
		}
	}
	return 0, false
}

// Args returns ffmpeg's arguments to make the stream that p says, as
// MPEG-TS on standard output, of the file at input from the time from, in
// seconds from the file's start. ffmpeg copies from the picture at or
// before it, which is the stream's time 0: from a time that ParseKeyframe
// gave, the stream starts there.
func (p Plan) Args(input string, from float64) []string {
	args := append(common("warning"), "-nostdin", "-nostats", "-fflags", "+genpts")
	if p.Encode {
		args = p.encodeArgs(args, input, from)
		if p.Muxrate > 0 {
			// At a constant rate, its timestamps from the time asked, so
			// that the stream from there is the stream from the start from
			// that byte on (D151). The output is the last three arguments.
			out := slices.Clone(args[len(args)-3:])
			args = append(args[:len(args)-3], "-muxrate", strconv.Itoa(p.Muxrate), "-output_ts_offset", Seconds(from))
			args = append(args, out...)
		}
		return args
	}
	if from > 0 {
		// To the millisecond after, so that rounding does not take ffmpeg
		// to the picture before; and later by seekEarly, which ffmpeg takes
		// off in a file whose video has B-frames.
		args = append(args, "-ss", Seconds(math.Ceil(from*1000-1e-6)/1000+seekEarly))
	}
	args = append(args, "-i", input)
	if p.Video >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(p.Video), "-c:v", "copy")
	}
	return p.soundArgs(args)
}

// encodeArgs returns, after args, ffmpeg's arguments to transcode the
// video (D73). With -ss before its input, ffmpeg decodes from the picture
// before the time asked and leaves out what comes before it, so that the
// stream starts at that very time.
func (p Plan) encodeArgs(args []string, input string, from float64) []string {
	if p.Hardware != nil {
		return p.hardwareArgs(args, input, from)
	}
	if from > 0 {
		args = append(args, "-ss", Seconds(from))
	}
	args = append(args, "-i", input)
	if p.Video >= 0 && p.Profile.Made {
		args = append(args, "-map", "0:"+strconv.Itoa(p.Video), "-vf", p.profileFilter())
		args = append(args, p.Profile.SoftwareVideo()...)
		if p.ToneMap {
			args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
		}
		return p.soundArgs(args)
	}
	if p.Video >= 0 {
		level := "4.1"
		if p.FrameRate > 30.5 {
			level = "4.2" // 1080p at 50 or 60 pictures a second
		}
		args = append(args, "-map", "0:"+strconv.Itoa(p.Video), "-c:v", "libx264", "-preset", "veryfast",
			"-crf", "21", "-maxrate", "8M", "-bufsize", "16M", "-profile:v", "high", "-level:v", level,
			"-vf", p.videoFilter())
		if p.ToneMap {
			args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
		}
	}
	return p.soundArgs(args)
}

// videoFilter returns the filters that make the pictures of a transcode:
// scaled to fit, mapped from a high dynamic range to the standard one when
// ToneMap says, and 8-bit 4:2:0. The pictures are scaled before they are
// mapped, which takes far less time on 4K video.
func (p Plan) videoFilter() string {
	size := ""
	if p.Width > 0 && p.Height > 0 {
		size = strconv.Itoa(p.Width) + ":" + strconv.Itoa(p.Height)
	}
	if !p.ToneMap {
		if size == "" {
			return "format=yuv420p"
		}
		return "scale=" + size + ",format=yuv420p"
	}
	scale := ""
	if size != "" {
		w, h, _ := strings.Cut(size, ":")
		scale = "zscale=w=" + w + ":h=" + h + ":f=bilinear,"
	}
	return scale + "zscale=t=linear:npl=100:p=bt709,format=gbrpf32le," +
		"tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p"
}

// profileFilter returns the filters that make the pictures of a transcode
// with a profile in software (D150): deinterlaced and kept to at most 30
// pictures a second as the profile says, then as videoFilter makes them.
func (p Plan) profileFilter() string {
	var f []string
	if p.Profile.Deinterlace {
		mode := "send_field"
		if p.Profile.FrameRate == streamprofile.RateAtMost {
			mode = "send_frame"
		}
		f = append(f, "bwdif=mode="+mode+":deint=interlaced")
	}
	if p.Profile.FrameRate == streamprofile.RateAtMost {
		f = append(f, streamprofile.HalfFast)
	}
	return strings.Join(append(f, p.videoFilter()), ",")
}

// hardwareArgs returns, after args, ffmpeg's arguments to transcode the
// video on the graphics (D148, D150): the graphics opened, and decoding
// the file where Hardware says so; from the time asked, as encodeArgs
// does; the profile's filters and encoder on the graphics; and the sound
// as in software.
func (p Plan) hardwareArgs(args []string, input string, from float64) []string {
	prof, hw := p.EncodeProfile(), *p.Hardware
	args = append(args, prof.HardwareInput(hw)...)
	if from > 0 {
		args = append(args, "-ss", Seconds(from))
	}
	args = append(args, "-i", input)
	if p.Video >= 0 {
		args = append(args, "-map", "0:"+strconv.Itoa(p.Video), "-vf", prof.HardwareFilter(hw))
		args = append(args, prof.HardwareVideo(hw)...)
		if hw.ToneMap {
			args = append(args, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
		}
	}
	return p.soundArgs(args)
}

// MakeSeekable makes a transcode with a profile one that a player seeks in
// by bytes (D151), and reports whether it could: a file whose length is
// not known cannot be. Every sound track is converted, so that its bit rate
// is known; the video is held to the profile's highest bit rate, or for a
// profile without one to one that its size and codec make; and the stream
// is muxed at a constant rate above both, so that each byte is at the time
// that its place says.
func (p *Plan) MakeSeekable(info Info) bool {
	video := info.First(Video)
	if !p.Encode || !p.Profile.Made || info.Duration <= 0 || video == nil {
		return false
	}
	lines := video.Height
	if p.Height > 0 {
		lines = p.Height
	}
	if p.Profile.MaxKbps == 0 {
		p.Profile.MaxKbps = seekableKbps(lines, p.Profile.Codec)
	}
	kbps := p.Profile.MaxKbps
	aac := p.SoundCodec() == "aac"
	for i := range p.Tracks {
		t := &p.Tracks[i]
		t.Convert = true
		switch {
		case aac:
			kbps += 128
		default:
			r, _ := strconv.Atoi(strings.TrimSuffix(ac3Rate(min(max(t.Channels, 2), 6)), "k"))
			kbps += r
		}
	}
	// Room for MPEG-TS's own packets and for a rate control that keeps to
	// its highest bit rate only over a second or two.
	p.Muxrate = (kbps*115/100 + 64) * 1000
	return true
}

// seekableKbps is the highest bit rate of a seekable transcode's video
// whose profile has none, in kbit/s, by its lines: about what such a
// picture needs at a good quality, and less for HEVC.
func seekableKbps(lines int, codec string) int {
	kbps := 16000
	for _, c := range []struct{ lines, kbps int }{{360, 1000}, {480, 1500}, {576, 2000}, {720, 3500}, {1080, 6000}, {1440, 10000}} {
		if lines <= c.lines {
			kbps = c.kbps
			break
		}
	}
	if codec == streamprofile.HEVC {
		kbps = kbps * 7 / 10
	}
	return kbps
}

// SeekableLength is how many bytes a seekable transcode of a file that
// lasts duration says it holds: whole MPEG-TS packets, at its rate, for as
// long as the file. ffmpeg makes about that many: what it makes beyond is
// left out, and what it makes short of it is padding, so that the stream's
// last timestamps are near its end, where players look for them.
func (p Plan) SeekableLength(duration float64) int64 {
	packets := math.Ceil(duration * float64(p.Muxrate) / 8 / tsPacket)
	return int64(packets) * tsPacket
}

// SeekableTail is where the end of a seekable transcode starts, which a
// player reads for the stream's length, and searches in small steps for
// its last timestamp before it seeks: the last 2 seconds, or the last 512
// KiB where those are fewer bytes, from a packet's start.
func (p Plan) SeekableTail(duration float64) int64 {
	length := p.SeekableLength(duration)
	tail := max(int64(2*p.Muxrate/8), 512<<10)
	return max(length-tail, 0) / tsPacket * tsPacket
}

// SeekableTime is the time of the packet that holds the byte at offset in
// a seekable transcode, in seconds from the file's start.
func (p Plan) SeekableTime(offset int64) float64 {
	return float64(offset-offset%tsPacket) * 8 / float64(p.Muxrate)
}

// tsPacket is the size of an MPEG-TS packet.
const tsPacket = 188

// SoundCodec names, as ffmpeg does, what the plan converts sound tracks
// to: AAC for a transcode with a profile of AAC in stereo (D150), and else
// Dolby Digital.
func (p Plan) SoundCodec() string {
	if p.Profile.Made && p.Profile.Sound == streamprofile.SoundAAC {
		return "aac"
	}
	return "ac3"
}

// soundArgs returns, after args, ffmpeg's arguments for a stream's sound
// tracks and its MPEG-TS output. A track converted by a transcode with a
// profile of AAC in stereo (D150) is AAC in stereo; others are Dolby
// Digital.
func (p Plan) soundArgs(args []string) []string {
	aac := p.SoundCodec() == "aac"
	for i, t := range p.Tracks {
		n := strconv.Itoa(i)
		args = append(args, "-map", "0:"+strconv.Itoa(t.Index))
		if !t.Convert {
			args = append(args, "-c:a:"+n, "copy")
			continue
		}
		if aac {
			args = append(args, "-c:a:"+n, "aac", "-ac:a:"+n, "2", "-b:a:"+n, "128k")
			continue
		}
		ch := t.Channels
		switch {
		case ch <= 0:
			ch = 2
		case ch > 6:
			ch = 6
		}
		args = append(args, "-c:a:"+n, "ac3", "-ac:a:"+n, strconv.Itoa(ch), "-b:a:"+n, ac3Rate(ch))
	}
	return append(args, "-max_muxing_queue_size", "4096", "-f", "mpegts", "pipe:1")
}

// seekEarly is how much earlier than asked ffmpeg 6 seeks, with -ss before
// its input, in a file whose video has B-frames and whose format does not
// seek by presentation time, such as Matroska: 3/23 of a second, which
// would take it to the picture before the one asked for. Asking that much
// later lands on it in such a file, and in others still before the next
// picture to start from, which is always further on.
const seekEarly = 0.131

// ac3Rate is the bit rate of Dolby Digital for a number of channels.
func ac3Rate(channels int) string {
	switch {
	case channels <= 1:
		return "96k"
	case channels == 2:
		return "192k"
	}
	return "448k"
}

// Seconds writes a time in seconds for ffmpeg, to the millisecond.
func Seconds(t float64) string { return strconv.FormatFloat(t, 'f', 3, 64) }
