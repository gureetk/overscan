// SPDX-License-Identifier: AGPL-3.0-or-later

package gpu

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"overscan.gureet.ca/internal/streamprofile"
)

// lists asks ffmpeg for its encoders and filters, unless the options give
// them.
func (t *tester) lists(ctx context.Context) error {
	var err error
	if t.o.Encoders == nil {
		if t.o.Encoders, err = t.list(ctx, "encoders"); err != nil {
			return err
		}
	}
	if t.o.Filters == nil {
		if t.o.Filters, err = t.list(ctx, "filters"); err != nil {
			return err
		}
	}
	return nil
}

// list returns the names in one of ffmpeg's lists: the second word of each
// line.
func (t *tester) list(ctx context.Context, which string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.o.FFmpeg, "-hide_banner", "-"+which)
	cmd.Env = []string{}
	cmd.Dir = "/"
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s -%s: %w", t.o.FFmpeg, which, err)
	}
	names := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 2 {
			names[f[1]] = true
		}
	}
	return names, nil
}

// has reports whether ffmpeg has every one of the filters.
func (t *tester) has(filters ...string) bool {
	for _, f := range filters {
		if !t.o.Filters[f] {
			return false
		}
	}
	return true
}

// runFFmpeg runs ffmpeg with args, with nothing from Overscan's
// environment, and returns what it wrote to standard error.
func (t *tester) runFFmpeg(ctx context.Context, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, t.o.FFmpeg, args...)
	cmd.Env = []string{}
	cmd.Dir = "/"
	var stderr bytes.Buffer
	cmd.Stderr = &limited{w: &stderr, n: 64 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		err = errors.New("it took too long")
	}
	return stderr.String(), err
}

// limited keeps at most n bytes of what is written to it.
type limited struct {
	w *bytes.Buffer
	n int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.n - l.w.Len(); room > 0 {
		l.w.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}

// source is ffmpeg's test pattern, of a size, at 25 pictures a second, for
// a number of pictures.
func source(size string, frames int) []string { return sourceAt(size, 25, frames) }

// sourceAt is ffmpeg's test pattern at a rate.
func sourceAt(size string, rate, frames int) []string {
	return []string{"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=s=%s:r=%d", size, rate), "-frames:v", fmt.Sprint(frames)}
}

// quiet makes ffmpeg say only what went wrong.
var quiet = []string{"-loglevel", "error"}

// encode finds out whether the graphics make a codec: in the usual mode,
// else in low-power mode, and in that mode whether they keep a constant
// quality and a bit rate. Each run is as the hub's would be: the codec's
// profile, from pictures that ffmpeg uploads.
func (t *tester) encode(ctx context.Context, codec string) {
	encoder := codec + "_vaapi"
	if !t.o.Encoders[encoder] {
		t.step("Encode "+Names[codec], errors.New("this ffmpeg has no "+encoder+" encoder"), "")
		return
	}
	try := func(name string, lowPower bool, rate ...string) bool {
		args := append(append(deviceArgs(t.f.Device), quiet...), source("1280x720", 25)...)
		args = append(args, "-vf", "format=nv12,hwupload", "-c:v", encoder, "-profile:v", streamprofile.VAAPIProfile(codec))
		if lowPower {
			args = append(args, "-low_power", "1")
		}
		args = append(append(args, rate...), "-f", "null", "-")
		said, err := t.o.Run(ctx, args)
		return t.step(name, err, said)
	}
	name := "Encode " + Names[codec]
	cqp := []string{"-rc_mode", "CQP", "-qp", "25"}
	var e Encode
	switch {
	case try(name, false, cqp...):
		e.OK = true
	case try(name+", low-power", true, cqp...):
		e.OK, e.LowPower = true, true
		name += ", low-power"
	default:
		t.f.Encodes[codec] = e
		return
	}
	e.ICQ = try(name+", at a constant quality (ICQ)", e.LowPower, "-rc_mode", "ICQ", "-global_quality", "25")
	e.VBR = try(name+", at a bit rate (VBR)", e.LowPower, "-rc_mode", "VBR", "-b:v", "1500k", "-maxrate", "2000k", "-bufsize", "4000k")
	t.f.Encodes[codec] = e
}

// filters finds out whether the graphics deinterlace and scale pictures
// as profiles have them do.
func (t *tester) filters(ctx context.Context) {
	run := func(name, size, filter string) bool {
		args := append(append(deviceArgs(t.f.Device), quiet...), source(size, 10)...)
		args = append(args, "-vf", filter, "-f", "null", "-")
		said, err := t.o.Run(ctx, args)
		return t.step(name, err, said)
	}
	if t.has("deinterlace_vaapi") {
		t.f.Deinterlace = run("Deinterlace", "720x576",
			"format=nv12,setfield=tff,hwupload,"+streamprofile.DeinterlaceVAAPI(true)+",hwdownload,format=nv12")
	} else {
		t.step("Deinterlace", errors.New("this ffmpeg has no deinterlace_vaapi filter"), "")
	}
	// ffmpeg's fps filter halves a fast rate where a profile has at most
	// 30 pictures a second; on pictures that stay on the graphics, it
	// must take them there.
	args := append(append(deviceArgs(t.f.Device), quiet...), sourceAt("640x360", 50, 20)...)
	args = append(args, "-vf", "format=nv12,hwupload,"+streamprofile.HalfFast+",hwdownload,format=nv12", "-f", "null", "-")
	said, err := t.o.Run(ctx, args)
	t.f.Rate = t.step("Halve a fast frame rate on the graphics", err, said)
	if t.has("scale_vaapi") {
		t.f.Scale = run("Scale", "1920x1080",
			"format=nv12,hwupload,"+streamprofile.ScaleVAAPI(720)+",hwdownload,format=nv12")
	} else {
		t.step("Scale", errors.New("this ffmpeg has no scale_vaapi filter"), "")
	}
}

// A clip is a test clip that ffmpeg makes in software, for the graphics to
// decode.
type clip struct {
	codec    string
	encoders []string // the software encoders that can make it, the first that ffmpeg has
	args     func(encoder string) []string
	tenBit   bool
}

var clips = []clip{
	{codec: H264, encoders: []string{"libx264"}, args: func(string) []string {
		return []string{"-c:v", "libx264", "-pix_fmt", "yuv420p"}
	}},
	{codec: HEVC, encoders: []string{"libx265"}, args: func(string) []string {
		return []string{"-c:v", "libx265", "-pix_fmt", "yuv420p", "-x265-params", "log-level=error"}
	}},
	{codec: HEVC10, encoders: []string{"libx265"}, tenBit: true, args: func(string) []string {
		return []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le", "-x265-params", "log-level=error"}
	}},
	{codec: MPEG2, encoders: []string{"mpeg2video"}, args: func(string) []string {
		return []string{"-c:v", "mpeg2video"}
	}},
	{codec: AV1, encoders: []string{"libsvtav1", "libaom-av1", "librav1e"}, args: func(enc string) []string {
		switch enc {
		case "libsvtav1":
			return []string{"-c:v", enc, "-preset", "12"}
		case "libaom-av1":
			return []string{"-c:v", enc, "-cpu-used", "8"}
		}
		return []string{"-c:v", enc, "-speed", "10"}
	}},
	{codec: VP9, encoders: []string{"libvpx-vp9"}, args: func(enc string) []string {
		return []string{"-c:v", enc, "-deadline", "realtime", "-cpu-used", "8"}
	}},
}

// makeClip makes a clip in software, and returns its path, or "" if ffmpeg
// has no encoder for it or failed. label names it for people.
func (t *tester) makeClip(ctx context.Context, c clip, name, label string, extra ...string) string {
	var encoder string
	for _, e := range c.encoders {
		if t.o.Encoders[e] {
			encoder = e
			break
		}
	}
	if encoder == "" {
		return ""
	}
	path := filepath.Join(t.dir, name+".mkv")
	args := append([]string{"-hide_banner", "-nostdin", "-nostats"}, quiet...)
	args = append(append(args, source("320x240", 10)...), c.args(encoder)...)
	args = append(append(args, extra...), "-y", path)
	said, err := t.o.Run(ctx, args)
	if !t.step("Make a clip in software: "+label, err, said) {
		return ""
	}
	return path
}

// decodes finds out which codecs the graphics decode, from clips that
// ffmpeg makes in software. A decode passes only if its pictures stay on
// the graphics: ffmpeg decodes in software, without a word, what the
// graphics cannot, and then downloading them from the graphics fails.
func (t *tester) decodes(ctx context.Context) {
	for _, c := range clips {
		path := t.makeClip(ctx, c, c.codec, Names[c.codec])
		if path == "" {
			t.f.DecodeUntested = append(t.f.DecodeUntested, c.codec)
			continue
		}
		format := "nv12"
		if c.tenBit {
			format = "p010"
		}
		args := append(append(deviceArgs(t.f.Device), quiet...),
			"-hwaccel", "vaapi", "-hwaccel_device", "va", "-hwaccel_output_format", "vaapi",
			"-i", path, "-vf", "hwdownload,format="+format, "-f", "null", "-")
		said, err := t.o.Run(ctx, args)
		t.f.Decodes[c.codec] = t.step("Decode "+Names[c.codec], err, said)
	}
}

// hdr10 is the HDR10 picture that the clip for tone mapping claims: BT.2020
// colours and the PQ curve, with a mastering display and light levels, as
// libx265 writes them.
var hdr10 = []string{
	"-color_primaries", "bt2020", "-color_trc", "smpte2084", "-colorspace", "bt2020nc",
	"-x265-params", "log-level=error:master-display=G(13250,34500)B(7500,3000)R(34000,16000)WP(15635,16450)L(10000000,1):max-cll=1000,400",
}

// toneMap finds out whether the graphics map an HDR10 picture to SDR, from
// a clip that ffmpeg makes and decodes in software.
func (t *tester) toneMap(ctx context.Context) {
	if !t.has("tonemap_vaapi") {
		t.step("Map HDR10 to SDR", errors.New("this ffmpeg has no tonemap_vaapi filter"), "")
		return
	}
	c := clips[2] // HEVC at 10 bits
	c.args = func(string) []string { return []string{"-c:v", "libx265", "-pix_fmt", "yuv420p10le"} }
	path := t.makeClip(ctx, c, "hdr10", "HDR10", hdr10...)
	if path == "" {
		t.f.ToneMapUntested = true
		t.step("Map HDR10 to SDR", errors.New("ffmpeg cannot make an HDR10 clip to test it: it lacks libx265 at 10 bits"), "")
		return
	}
	args := append(append(deviceArgs(t.f.Device), quiet...), "-i", path,
		"-vf", "format=p010,hwupload,"+streamprofile.ToneMapVAAPI+",hwdownload,format=nv12", "-f", "null", "-")
	said, err := t.o.Run(ctx, args)
	t.f.ToneMap = t.step("Map HDR10 to SDR", err, said)
}
