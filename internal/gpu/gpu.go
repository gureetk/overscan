// SPDX-License-Identifier: AGPL-3.0-or-later

// Package gpu finds out what a server's graphics can do for ffmpeg through
// VA-API (D148), so that the hub transcodes on them what they can.
//
// The test runs when the server starts, and again when the administrator
// asks. It never opens a channel: every picture it uses is one that ffmpeg
// makes, from its test pattern, or a short clip that ffmpeg makes of it in
// a directory of its own, removed afterwards. It finds the render device,
// and asks ffmpeg to open it; then, for H.264 and HEVC, whether the
// graphics encode in the usual mode or only in low-power mode, and whether
// they keep a constant quality (ICQ) and a bit rate (VBR); which codecs
// they decode, among those that live channels, films and series use; and
// whether they deinterlace, scale, and map HDR10 to SDR. What fails is
// left to software.
package gpu

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The codecs that the test asks about: those that profiles make (H264 and
// HEVC), and those that streams and films hold.
const (
	H264   = "h264"
	HEVC   = "hevc"
	HEVC10 = "hevc10" // HEVC at 10 bits
	MPEG2  = "mpeg2"
	AV1    = "av1"
	VP9    = "vp9"
)

// Encoded are the codecs that the graphics can make, in the order that the
// admin lists them.
var Encoded = []string{H264, HEVC}

// Decoded are the codecs that the test checks that the graphics decode, in
// the order that the admin lists them.
var Decoded = []string{H264, HEVC, HEVC10, MPEG2, AV1, VP9}

// Names are the codecs' names for people.
var Names = map[string]string{
	H264: "H.264", HEVC: "HEVC", HEVC10: "HEVC at 10 bits", MPEG2: "MPEG-2", AV1: "AV1", VP9: "VP9",
}

// Encode is what the graphics do to make a codec.
type Encode struct {
	OK bool // they make it
	// LowPower says that they make it only in low-power mode.
	LowPower bool
	// ICQ says that they keep a constant quality that changes with the
	// picture; without it, a constant quantiser does.
	ICQ bool
	// VBR says that they keep to a bit rate, so that a profile's highest
	// bit rate holds.
	VBR bool
}

// Step is one thing that the test tried, for the admin and the support
// bundle.
type Step struct {
	Name string // such as "Encode H.264, low-power"
	OK   bool
	Note string // what ffmpeg said, when it failed; "" otherwise
}

// Found is what the test found.
type Found struct {
	// Off says that the configuration turns the graphics off.
	Off bool
	// Device is the render device that ffmpeg opened, such as
	// /dev/dri/renderD128; "" if there is none to use.
	Device string
	// Driver is the VA-API driver, as it names itself; "" if not known.
	Driver string
	// Why says why there is no device to use, for people.
	Why string

	Encodes map[string]Encode
	Decodes map[string]bool
	// DecodeUntested are the codecs that ffmpeg could not make a clip of,
	// to test.
	DecodeUntested []string
	Deinterlace    bool // deinterlace_vaapi works
	Scale          bool // scale_vaapi works, as profiles use it
	Rate           bool // ffmpeg's fps filter takes pictures on the graphics
	ToneMap        bool // tonemap_vaapi maps HDR10 to SDR
	// ToneMapUntested says that no HDR10 clip could be made to test
	// tone mapping, since ffmpeg lacks a 10-bit libx265.
	ToneMapUntested bool

	Steps []Step
	At    time.Time
	Took  time.Duration
}

// Usable reports whether the graphics make any codec.
func (f Found) Usable() bool {
	return f.Device != "" && (f.Encodes[H264].OK || f.Encodes[HEVC].OK)
}

// Filters reports whether the graphics deinterlace and scale, so that a
// picture can stay on them from decoding to encoding.
func (f Found) Filters() bool { return f.Deinterlace && f.Scale }

// EncodedNames returns the codecs that the graphics make, for people, such
// as "H.264, HEVC (low-power)".
func (f Found) EncodedNames() string {
	var out []string
	for _, c := range Encoded {
		e := f.Encodes[c]
		if !e.OK {
			continue
		}
		name := Names[c]
		if e.LowPower {
			name += " (low-power)"
		}
		out = append(out, name)
	}
	return strings.Join(out, ", ")
}

// DecodedNames returns the codecs that the graphics decode, for people.
func (f Found) DecodedNames() string {
	var out []string
	for _, c := range Decoded {
		if f.Decodes[c] {
			out = append(out, Names[c])
		}
	}
	return strings.Join(out, ", ")
}

// Options are how to test.
type Options struct {
	// FFmpeg is the path of ffmpeg.
	FFmpeg string
	// Device is the render device to use: "" or "auto" for the first that
	// Overscan can open, "off" for none.
	Device string
	// DevDir is where render devices are; "/dev/dri" if "".
	DevDir string
	// Run runs ffmpeg with args and returns what it wrote to standard
	// error; nil runs FFmpeg. Tests set it.
	Run func(ctx context.Context, args []string) (string, error)
	// Encoders and Filters are what ffmpeg lists; nil asks it.
	Encoders, Filters map[string]bool
	// Now gives the time; nil for time.Now.
	Now func() time.Time
}

// Device settings.
const (
	Auto = "auto"
	Off  = "off"
)

// DeviceRE is what a render device's path must look like.
var DeviceRE = regexp.MustCompile(`^/dev/dri/renderD[0-9]{1,4}$`)

// runTimeout caps each of ffmpeg's runs: a driver that hangs must not
// hold up the test for long.
const runTimeout = 30 * time.Second

// noteLimit caps what a failed step keeps of ffmpeg's words.
const noteLimit = 300

// Test finds out what the graphics can do. It never fails: what it cannot
// find out, it reports as not done, and says why.
func Test(ctx context.Context, o Options) Found {
	now := time.Now
	if o.Now != nil {
		now = o.Now
	}
	t := &tester{o: o, f: Found{At: now(), Encodes: map[string]Encode{}, Decodes: map[string]bool{}}}
	if t.o.Run == nil {
		t.o.Run = t.runFFmpeg
	}
	if t.o.DevDir == "" {
		t.o.DevDir = "/dev/dri"
	}
	start := time.Now()
	t.test(ctx)
	t.f.Took = time.Since(start).Round(time.Millisecond)
	return t.f
}

type tester struct {
	o   Options
	f   Found
	dir string // the clips' directory
}

func (t *tester) step(name string, err error, said string) bool {
	s := Step{Name: name, OK: err == nil}
	if err != nil {
		s.Note = note(said, err)
	}
	t.f.Steps = append(t.f.Steps, s)
	return err == nil
}

// note keeps the last line of what ffmpeg said that tells why it failed,
// or the error.
func note(said string, err error) string {
	var last string
	for line := range strings.Lines(said) {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "libva info:") {
			last = line
		}
	}
	if last == "" {
		last = err.Error()
	}
	if len(last) > noteLimit {
		last = last[:noteLimit] + "…"
	}
	return last
}

func (t *tester) test(ctx context.Context) {
	if t.o.Device == Off {
		t.f.Off = true
		t.f.Why = "The configuration turns the graphics off."
		return
	}
	if t.o.FFmpeg == "" && t.o.Run == nil {
		t.f.Why = "There is no ffmpeg to use the graphics with."
		return
	}
	dev, why := t.device()
	if dev == "" {
		t.f.Why = why
		return
	}
	if err := t.lists(ctx); err != nil {
		t.f.Why = "ffmpeg did not say what it has: " + err.Error()
		return
	}
	if !t.has("hwupload", "hwdownload") {
		t.f.Why = "This ffmpeg cannot move pictures to and from the graphics: it lacks the hwupload and hwdownload filters."
		return
	}
	if !slices.ContainsFunc(Encoded, func(c string) bool { return t.o.Encoders[c+"_vaapi"] }) {
		t.f.Why = "This ffmpeg has no VA-API encoders: it was built without VA-API."
		return
	}
	said, err := t.o.Run(ctx, append(deviceArgs(dev), "-v", "verbose",
		"-f", "lavfi", "-i", "testsrc2=s=320x240:r=25", "-frames:v", "1",
		"-vf", "format=nv12,hwupload,hwdownload,format=nv12", "-f", "null", "-"))
	if m := driverRE.FindStringSubmatch(said); m != nil {
		t.f.Driver = strings.TrimSpace(m[1])
	}
	if !t.step("Open "+dev, err, said) {
		t.f.Why = openWhy(dev, said, err)
		return
	}
	t.f.Device = dev
	for _, c := range Encoded {
		t.encode(ctx, c)
	}
	if !t.f.Usable() {
		t.f.Why = "ffmpeg opens " + dev + ", but the graphics encode neither H.264 nor HEVC through VA-API."
		t.f.Device = ""
		return
	}
	t.filters(ctx)
	dir, err := os.MkdirTemp("", "overscan-gpu-")
	if t.step("Make a directory for test clips", err, "") {
		t.dir = dir
		defer os.RemoveAll(dir)
		t.decodes(ctx)
		t.toneMap(ctx)
	}
}

// driverRE finds the driver's name in what ffmpeg says as it opens the
// device.
var driverRE = regexp.MustCompile(`VAAPI driver: ([^\n]+?)\.?\s*(\n|$)`)

// device returns the render device to test, or why there is none.
func (t *tester) device() (string, string) {
	if d := t.o.Device; d != "" && d != Auto {
		if err := canOpen(d); err != nil {
			return "", cannotOpen(d, err)
		}
		return d, ""
	}
	entries, err := os.ReadDir(t.o.DevDir)
	if err != nil {
		return "", "Overscan sees no graphics: " + t.o.DevDir + " is not there. A server in a container or a systemd sandbox must be given its render device."
	}
	var nodes []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			nodes = append(nodes, filepath.Join(t.o.DevDir, e.Name()))
		}
	}
	if len(nodes) == 0 {
		return "", "Overscan sees no render device in " + t.o.DevDir + "."
	}
	slices.Sort(nodes)
	var first error
	for _, n := range nodes {
		err := canOpen(n)
		if err == nil {
			return n, ""
		}
		if first == nil {
			first = err
		}
	}
	return "", cannotOpen(nodes[0], first)
}

// canOpen checks that Overscan can open a render device to read and write.
func canOpen(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	return f.Close()
}

func cannotOpen(path string, err error) string {
	if errors.Is(err, fs.ErrPermission) {
		return "Overscan cannot open " + path + ": permission denied. The user that runs Overscan must be in the group that owns it, usually render."
	}
	if errors.Is(err, fs.ErrNotExist) {
		return "Overscan sees no " + path + "."
	}
	return "Overscan cannot open " + path + ": " + err.Error()
}

// openWhy says why ffmpeg could not open a device that Overscan can.
func openWhy(dev, said string, err error) string {
	switch {
	case strings.Contains(said, "va_openDriver() returns") || strings.Contains(said, "Failed to initialise VAAPI connection"):
		return "ffmpeg cannot open " + dev + " through VA-API: no VA-API driver works with these graphics. Intel graphics need intel-media-va-driver (Broadwell and later), AMD graphics mesa-va-drivers."
	default:
		return "ffmpeg cannot open " + dev + " through VA-API: " + note(said, err)
	}
}

// deviceArgs returns ffmpeg's arguments that open dev, named va, for the
// filters.
func deviceArgs(dev string) []string {
	return []string{"-hide_banner", "-nostdin", "-nostats",
		"-init_hw_device", "vaapi=va:" + dev, "-filter_hw_device", "va"}
}

// Report writes what the test found, for people: the support bundle's,
// and the gpu command's.
func (f Found) Report(w io.Writer) {
	if f.At.IsZero() {
		fmt.Fprintln(w, "The graphics have not been tested yet.")
		return
	}
	if f.Usable() {
		fmt.Fprintf(w, "Device: %s\n", f.Device)
		if f.Driver != "" {
			fmt.Fprintf(w, "Driver: %s\n", f.Driver)
		}
		fmt.Fprintf(w, "Encodes: %s\n", f.EncodedNames())
		var icq, vbr []string
		for _, c := range Encoded {
			if e := f.Encodes[c]; e.OK && e.ICQ {
				icq = append(icq, Names[c])
			}
			if e := f.Encodes[c]; e.OK && e.VBR {
				vbr = append(vbr, Names[c])
			}
		}
		fmt.Fprintf(w, "Keeps a constant quality (ICQ): %s\n", orNone(icq))
		fmt.Fprintf(w, "Keeps to a bit rate (VBR): %s\n", orNone(vbr))
		fmt.Fprintf(w, "Decodes: %s\n", orNone(strings.Split(f.DecodedNames(), ", ")))
		if len(f.DecodeUntested) > 0 {
			var names []string
			for _, c := range f.DecodeUntested {
				names = append(names, Names[c])
			}
			fmt.Fprintf(w, "Not tested, since ffmpeg cannot make a clip of it: %s\n", strings.Join(names, ", "))
		}
		yes := map[bool]string{true: "yes", false: "no"}
		fmt.Fprintf(w, "Deinterlaces: %s; scales: %s; halves a frame rate: %s; maps HDR10 to SDR: %s\n",
			yes[f.Deinterlace], yes[f.Scale], yes[f.Rate], map[bool]string{true: "not tested", false: yes[f.ToneMap]}[f.ToneMapUntested])
	} else {
		fmt.Fprintf(w, "No graphics to transcode on: %s\n", f.Why)
	}
	fmt.Fprintf(w, "Tested: %s", f.At.UTC().Format(time.RFC3339))
	if f.Took >= time.Millisecond {
		fmt.Fprintf(w, ", in %s", f.Took)
	}
	fmt.Fprintln(w)
	if len(f.Steps) > 0 {
		fmt.Fprintln(w, "Steps:")
		for _, s := range f.Steps {
			mark := "ok  "
			if !s.OK {
				mark = "FAIL"
			}
			fmt.Fprintf(w, "  %s %s", mark, s.Name)
			if s.Note != "" {
				fmt.Fprintf(w, ": %s", s.Note)
			}
			fmt.Fprintln(w)
		}
	}
}

func orNone(names []string) string {
	names = slices.DeleteFunc(names, func(s string) bool { return s == "" })
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}
