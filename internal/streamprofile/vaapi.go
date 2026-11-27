// SPDX-License-Identifier: AGPL-3.0-or-later

package streamprofile

import (
	"strconv"
	"strings"
)

// Transcoding on graphics, through VA-API (D148). The graphics make the
// profile's codec; where they deinterlace and scale, the pictures go
// through those filters on them, and, where they also decode the stream,
// stay on them from decoding to encoding. What they cannot do is done in
// software, as Args does it.

// Hardware is how a stream is transcoded on the graphics, from what the
// test at start found that they do.
type Hardware struct {
	// Device is the render device, such as /dev/dri/renderD128.
	Device string
	// LowPower says that the graphics make the profile's codec only in
	// low-power mode; ICQ that they keep a constant quality; VBR that
	// they keep to a bit rate.
	LowPower, ICQ, VBR bool
	// Filters says that the graphics deinterlace and scale the pictures.
	Filters bool
	// Decode says that the graphics decode the stream, so that its
	// pictures stay on them. It needs Filters, and Rate for a profile of
	// at most 30 pictures a second.
	Decode bool
	// Rate says that ffmpeg's fps filter takes pictures on the graphics.
	Rate bool
	// ToneMap says that the graphics map the stream's HDR10 picture to
	// SDR. It needs Filters.
	ToneMap bool
}

// VAAPIProfile returns the profile of the codec that the graphics make:
// H.264's High profile, or HEVC's Main, both at 8 bits.
func VAAPIProfile(codec string) string {
	if codec == HEVC {
		return "main"
	}
	return "high"
}

// DeinterlaceVAAPI returns the filter that deinterlaces on the graphics
// where a picture is interlaced, making a picture of each field, or of
// each frame.
func DeinterlaceVAAPI(eachField bool) string {
	if eachField {
		return "deinterlace_vaapi=rate=field:auto=1"
	}
	return "deinterlace_vaapi=rate=frame:auto=1"
}

// ScaleVAAPI returns the filter that scales a picture on the graphics down
// to fit a largest size, in lines, as 16:9, never up, and makes it 8-bit;
// with 0 lines, it only makes it 8-bit.
func ScaleVAAPI(lines int) string {
	w := widthOf(lines)
	if w == 0 {
		return "scale_vaapi=format=nv12"
	}
	return "scale_vaapi=w='min(" + strconv.Itoa(w) + ",iw)':h='min(" + strconv.Itoa(lines) + ",ih)'" +
		":force_original_aspect_ratio=decrease:force_divisible_by=2:format=nv12"
}

// ToneMapVAAPI is the filter that maps an HDR10 picture to SDR on the
// graphics, with BT.709's colours.
const ToneMapVAAPI = "tonemap_vaapi=format=nv12:t=bt709:m=bt709:p=bt709"

// widthOf returns the width of a 16:9 picture of a number of lines.
func widthOf(lines int) int {
	switch lines {
	case 1080:
		return 1920
	case 720:
		return 1280
	case 576:
		return 1024
	}
	return 0
}

// AsMade returns the settings of a fixed profile that transcodes (D110) as
// a profile that the administrator could make, as asMade does.
func (p Profile) AsMade() Profile { return p.asMade() }

// asMade returns the settings of a fixed profile that transcodes (D110) as
// a profile that the administrator could make, for the graphics: H.264 of
// its size, its quality and its highest bit rate, deinterlaced a picture
// for each field, as its software arguments have it.
func (p Profile) asMade() Profile {
	if p.Made || !p.Transcodes() {
		return p
	}
	q := Medium
	if p.CRF <= 21 {
		q = High
	}
	kbps := 0
	if n, err := strconv.Atoi(strings.TrimSuffix(p.MaxRate, "M")); err == nil {
		kbps = n * 1000
	}
	return Profile{Name: p.Name, Label: p.Label, Made: true, Codec: H264, MaxLines: p.Height, Quality: q,
		MaxKbps: kbps, FrameRate: RateSource, Deinterlace: true, Sound: SoundAAC}
}

// vaapiQuality is the quantiser of a quality on the graphics, for H.264
// and HEVC alike: lower than libx264's CRF, since a constant quantiser
// lacks its tuning to what the eye sees.
func (p Profile) vaapiQuality() int {
	return map[string]int{High: 22, Medium: 25, Low: 29}[p.Quality]
}

// HardwareFilter returns the filters that the pictures go through for the
// graphics to encode them.
//
// With the graphics' filters: the pictures, uploaded unless the graphics
// decoded them, are deinterlaced on the graphics, a picture of each field
// or of each frame; kept to at most 30 a second, before the upload where
// software decoded them; mapped from HDR10 to SDR where asked; and scaled
// down to fit the largest size, as 8-bit. Without them: VideoFilter's
// filters in software, then the upload.
func (p Profile) HardwareFilter(hw Hardware) string {
	m := p.asMade()
	if !hw.Filters {
		f := m.VideoFilter()
		return strings.TrimSuffix(f, "format=yuv420p") + "format=nv12,hwupload"
	}
	decode := hw.Decode && (hw.Rate || m.FrameRate != RateAtMost)
	var f []string
	if !decode {
		if m.FrameRate == RateAtMost {
			f = append(f, HalfFast)
		}
		if hw.ToneMap {
			f = append(f, "format=p010,hwupload")
		} else {
			f = append(f, "format=nv12,hwupload")
		}
	}
	if m.Deinterlace {
		f = append(f, DeinterlaceVAAPI(m.FrameRate != RateAtMost))
	}
	if decode && m.FrameRate == RateAtMost {
		f = append(f, HalfFast)
	}
	if hw.ToneMap {
		f = append(f, ToneMapVAAPI)
	}
	return strings.Join(append(f, ScaleVAAPI(m.MaxLines)), ",")
}

// DecodesOnGPU reports whether HardwareArgs has the graphics decode the
// stream.
func (p Profile) DecodesOnGPU(hw Hardware) bool {
	return hw.Filters && hw.Decode && (hw.Rate || p.asMade().FrameRate != RateAtMost)
}

// HardwareArgs returns ffmpeg's arguments to transcode a live stream to the
// profile on the graphics: as Args, with the device opened for the
// filters, the stream decoded on the graphics where hw says so,
// HardwareFilter's filters, and HardwareVideo's encoder.
func (p Profile) HardwareArgs(hw Hardware) []string {
	m := p.asMade()
	args := append([]string{
		"-hide_banner", "-nostdin", "-nostats", "-loglevel", "warning",
		"-protocol_whitelist", "pipe",
		"-fflags", "+genpts",
		"-analyzeduration", "2000000", "-probesize", "4000000",
	}, p.HardwareInput(hw)...)
	args = append(args, "-f", "mpegts", "-i", "pipe:0", "-map", "0:v:0?")
	if m.Sound == SoundCopy {
		args = append(args, "-map", "0:a?")
	} else {
		args = append(args, "-map", "0:a:0?")
	}
	args = append(append(args, "-vf", p.HardwareFilter(hw)), p.HardwareVideo(hw)...)
	args = append(args, "-force_key_frames", "expr:gte(t,n_forced*2)")
	return append(args, m.soundArgs()...)
}

// HardwareInput returns ffmpeg's arguments, before its input, that open
// the graphics for the filters, and have them decode the input where hw
// says so.
func (p Profile) HardwareInput(hw Hardware) []string {
	args := []string{"-init_hw_device", "vaapi=va:" + hw.Device, "-filter_hw_device", "va"}
	if p.DecodesOnGPU(hw) {
		args = append(args, "-hwaccel", "vaapi", "-hwaccel_device", "va", "-hwaccel_output_format", "vaapi")
	}
	return args
}

// HardwareVideo returns ffmpeg's arguments for the graphics' encoder of the
// profile's codec: h264_vaapi at High profile or hevc_vaapi at Main, in
// low-power mode where hw says so. It keeps to the profile's highest bit
// rate where the graphics keep to one (VBR, averaging three quarters of
// it); otherwise it keeps a constant quality (ICQ), or else a constant
// quantiser (CQP), and a highest bit rate does not hold.
func (p Profile) HardwareVideo(hw Hardware) []string {
	m := p.asMade()
	args := []string{"-c:v", m.Codec + "_vaapi", "-profile:v", VAAPIProfile(m.Codec)}
	if hw.LowPower {
		args = append(args, "-low_power", "1")
	}
	q := strconv.Itoa(m.vaapiQuality())
	switch {
	case m.MaxKbps > 0 && hw.VBR:
		args = append(args, "-rc_mode", "VBR", "-b:v", strconv.Itoa(m.MaxKbps*3/4)+"k",
			"-maxrate", strconv.Itoa(m.MaxKbps)+"k", "-bufsize", strconv.Itoa(2*m.MaxKbps)+"k")
	case hw.ICQ:
		args = append(args, "-rc_mode", "ICQ", "-global_quality", q)
	default:
		args = append(args, "-rc_mode", "CQP", "-qp", q)
	}
	return args
}

// SoftwareVideo returns ffmpeg's arguments for the software encoder of a
// profile that the administrator made: libx264 at High profile or libx265
// at Main, at its quality, capped at its highest bit rate if it has one.
func (p Profile) SoftwareVideo() []string {
	var args []string
	switch p.Codec {
	case HEVC:
		args = []string{"-c:v", "libx265", "-preset", "veryfast", "-crf", strconv.Itoa(p.crf()),
			"-profile:v", "main", "-x265-params", "log-level=error"}
	default:
		args = []string{"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(p.crf()), "-profile:v", "high"}
	}
	if p.MaxKbps > 0 {
		args = append(args, "-maxrate", strconv.Itoa(p.MaxKbps)+"k", "-bufsize", strconv.Itoa(2*p.MaxKbps)+"k")
	}
	return args
}

// Box returns the largest picture of a profile that the administrator
// made, in pixels, as 16:9 of its largest size; 0 and 0 for the source's.
func (p Profile) Box() (width, height int) {
	m := p.asMade()
	return widthOf(m.MaxLines), m.MaxLines
}

// soundArgs returns ffmpeg's arguments for a made profile's sound, and the
// output's.
func (p Profile) soundArgs() []string {
	var args []string
	switch p.Sound {
	case SoundCopy:
		args = []string{"-c:a", "copy"}
	case SoundAC3:
		args = []string{"-c:a", "ac3", "-b:a", "384k"}
	default:
		args = []string{"-c:a", "aac", "-b:a", "128k", "-ac", "2"}
	}
	return append(args, "-max_interleave_delta", "2000000", "-f", "mpegts", "pipe:1")
}
