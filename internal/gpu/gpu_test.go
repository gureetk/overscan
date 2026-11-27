// SPDX-License-Identifier: AGPL-3.0-or-later

package gpu

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// everything is what an ffmpeg built with VA-API and every software
// encoder lists.
var (
	allEncoders = map[string]bool{"h264_vaapi": true, "hevc_vaapi": true, "libx264": true, "libx265": true,
		"mpeg2video": true, "libsvtav1": true, "libvpx-vp9": true}
	allFilters = map[string]bool{"hwupload": true, "hwdownload": true, "deinterlace_vaapi": true,
		"scale_vaapi": true, "tonemap_vaapi": true}
)

// devDir returns a directory with a render device that can be opened: a
// plain file in its place.
func devDir(t *testing.T) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renderD128"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// fake is an ffmpeg that fails the runs whose arguments hold any of fail's
// words, and notes every run.
type fake struct {
	fail map[string]string // a word in the arguments, and what ffmpeg says
	runs [][]string
}

func (f *fake) run(_ context.Context, args []string) (string, error) {
	f.runs = append(f.runs, args)
	joined := strings.Join(args, " ")
	for word, said := range f.fail {
		if strings.Contains(joined, word) {
			return said, errors.New("exit status 1")
		}
	}
	if slices.Contains(args, "verbose") {
		return "[AVHWDeviceContext @ 0x1] VAAPI driver: Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 ().\n", nil
	}
	return "", nil
}

func test(t *testing.T, f *fake, o Options) Found {
	t.Helper()
	if o.DevDir == "" {
		o.DevDir = devDir(t)
	}
	o.Run = f.run
	if o.Encoders == nil {
		o.Encoders = allEncoders
	}
	if o.Filters == nil {
		o.Filters = allFilters
	}
	return Test(context.Background(), o)
}

func TestEverythingWorks(t *testing.T) {
	f := &fake{}
	found := test(t, f, Options{})
	if !found.Usable() || found.Why != "" {
		t.Fatalf("found %+v", found)
	}
	if found.Driver != "Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 ()" {
		t.Errorf("driver %q", found.Driver)
	}
	if !strings.HasSuffix(found.Device, "/renderD128") {
		t.Errorf("device %q", found.Device)
	}
	for _, c := range Encoded {
		if e := found.Encodes[c]; e != (Encode{OK: true, ICQ: true, VBR: true}) {
			t.Errorf("%s: %+v", c, e)
		}
	}
	for _, c := range Decoded {
		if !found.Decodes[c] {
			t.Errorf("does not decode %s", c)
		}
	}
	if !found.Deinterlace || !found.Scale || !found.Rate || !found.ToneMap || found.ToneMapUntested || len(found.DecodeUntested) > 0 {
		t.Errorf("found %+v", found)
	}
	if got, want := found.EncodedNames(), "H.264, HEVC"; got != want {
		t.Errorf("encodes %q, want %q", got, want)
	}
	for _, s := range found.Steps {
		if !s.OK {
			t.Errorf("step %+v", s)
		}
	}
	// Every run of ffmpeg opens the device, except those that make clips
	// in software, and none reads anything but ffmpeg's test pattern and
	// its clips.
	for _, args := range f.runs {
		joined := strings.Join(args, " ")
		making := slices.Contains(args, "-y")
		if opens := strings.Contains(joined, "-init_hw_device vaapi=va:"+found.Device); opens == making {
			t.Errorf("run %q", joined)
		}
		for i, a := range args {
			if a == "-i" && !strings.HasPrefix(args[i+1], "testsrc2=") && !strings.HasSuffix(args[i+1], ".mkv") {
				t.Errorf("run reads %q", args[i+1])
			}
		}
	}
}

func TestLowPowerAndRates(t *testing.T) {
	// H.264 only in low-power mode, which keeps no bit rate; HEVC not at
	// all.
	f := &fake{fail: map[string]string{
		"-c:v h264_vaapi -profile:v high -rc_mode": "[h264_vaapi @ 0x1] No usable encoding entrypoint found for profile VAProfileH264High (7).",
		"-low_power 1 -rc_mode VBR":                "[h264_vaapi @ 0x1] Driver does not support VBR RC mode.",
		"hevc_vaapi":                               "[hevc_vaapi @ 0x1] No usable encoding entrypoint found for profile VAProfileHEVCMain (17).",
	}}
	found := test(t, f, Options{})
	if got, want := found.Encodes[H264], (Encode{OK: true, LowPower: true, ICQ: true}); got != want {
		t.Errorf("H.264: %+v, want %+v", got, want)
	}
	if found.Encodes[HEVC].OK {
		t.Errorf("HEVC: %+v", found.Encodes[HEVC])
	}
	if got, want := found.EncodedNames(), "H.264 (low-power)"; got != want {
		t.Errorf("encodes %q, want %q", got, want)
	}
	var note string
	for _, s := range found.Steps {
		if s.Name == "Encode H.264, low-power, at a bit rate (VBR)" {
			note = s.Note
		}
	}
	if note != "[h264_vaapi @ 0x1] Driver does not support VBR RC mode." {
		t.Errorf("VBR's note %q", note)
	}
}

func TestDecodes(t *testing.T) {
	// No VP9 encoder to make a clip with, and AV1 decodes in software
	// only, so its pictures cannot be downloaded from the graphics.
	encoders := map[string]bool{}
	for k, v := range allEncoders {
		encoders[k] = v && k != "libvpx-vp9"
	}
	f := &fake{fail: map[string]string{
		"av1.mkv -vf hwdownload": "[hwdownload @ 0x1] Invalid input format.",
	}}
	found := test(t, f, Options{Encoders: encoders})
	if found.Decodes[AV1] || found.Decodes[VP9] || !found.Decodes[HEVC10] {
		t.Errorf("decodes %v", found.Decodes)
	}
	if !slices.Equal(found.DecodeUntested, []string{VP9}) {
		t.Errorf("untested %v", found.DecodeUntested)
	}
	if got, want := found.DecodedNames(), "H.264, HEVC, HEVC at 10 bits, MPEG-2"; got != want {
		t.Errorf("decodes %q, want %q", got, want)
	}
}

func TestNoToneMapClip(t *testing.T) {
	encoders := map[string]bool{"h264_vaapi": true, "libx264": true}
	found := test(t, &fake{}, Options{Encoders: encoders})
	if found.ToneMap || !found.ToneMapUntested {
		t.Errorf("tone map %v, untested %v", found.ToneMap, found.ToneMapUntested)
	}
	if !found.Usable() || found.Encodes[HEVC].OK {
		t.Errorf("found %+v", found)
	}
}

func TestNoGraphics(t *testing.T) {
	cases := map[string]struct {
		o    Options
		fail map[string]string
		why  string
	}{
		"off":         {o: Options{Device: Off}, why: "The configuration turns the graphics off."},
		"no dri":      {o: Options{DevDir: "/nonexistent/dri"}, why: "Overscan sees no graphics"},
		"no node":     {o: Options{DevDir: "empty"}, why: "Overscan sees no render device"},
		"no device":   {o: Options{Device: "/nonexistent/renderD128"}, why: "Overscan sees no /nonexistent/renderD128."},
		"no encoders": {o: Options{Encoders: map[string]bool{"libx264": true}}, why: "This ffmpeg has no VA-API encoders"},
		"no upload":   {o: Options{Filters: map[string]bool{"scale_vaapi": true}}, why: "This ffmpeg cannot move pictures"},
		"no driver": {fail: map[string]string{"verbose": "libva info: va_openDriver() returns -1\n[AVHWDeviceContext @ 0x1] Failed to initialise VAAPI connection: -1 (unknown libva error).\n"},
			why: "no VA-API driver works with these graphics"},
		"encodes nothing": {fail: map[string]string{"_vaapi": "[h264_vaapi @ 0x1] No usable encoding entrypoint found."},
			why: "encode neither H.264 nor HEVC"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if c.o.DevDir == "empty" {
				c.o.DevDir = t.TempDir()
			}
			found := test(t, &fake{fail: c.fail}, c.o)
			if found.Usable() || found.Device != "" || !strings.Contains(found.Why, c.why) {
				t.Errorf("found %+v, want why %q", found, c.why)
			}
			if found.Off != (name == "off") {
				t.Errorf("off %v", found.Off)
			}
		})
	}
}

func TestPermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens any file")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renderD128"), nil, 0o000); err != nil {
		t.Fatal(err)
	}
	found := test(t, &fake{}, Options{DevDir: dir})
	if !strings.Contains(found.Why, "permission denied") || !strings.Contains(found.Why, "render") {
		t.Errorf("why %q", found.Why)
	}
}

// TestWithFFmpeg runs the real ffmpeg on a server whose graphics Overscan
// does not see: the test says so, and never runs ffmpeg on a device.
func TestWithFFmpeg(t *testing.T) {
	path, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	found := Test(context.Background(), Options{FFmpeg: path, DevDir: t.TempDir()})
	if found.Usable() || !strings.Contains(found.Why, "no render device") || len(found.Steps) != 0 {
		t.Errorf("found %+v", found)
	}
}
