// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"overscan.gureet.ca/internal/hub"
)

// fakeFFmpeg writes a program that answers -encoders and -filters with the
// given lists, as ffmpeg prints them, and fails if fail is set.
func fakeFFmpeg(t *testing.T, encoders, filters string, fail bool) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell")
	}
	path := filepath.Join(t.TempDir(), "ffmpeg")
	script := "#!/bin/sh\n"
	if fail {
		script += "echo 'Unrecognized option' >&2\nexit 1\n"
	}
	script += "case \"$2\" in\n-encoders) cat <<'END'\n" + encoders + "END\n;;\n-filters) cat <<'END'\n" + filters + "END\n;;\nesac\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	encoderList = `Encoders:
 V..... = Video
 A..... = Audio
 ------
 V....D libx264              libx264 H.264 / AVC / MPEG-4 AVC / MPEG-4 part 10 (codec h264)
 A....D ac3                  ATSC A/52A (AC-3)
`
	filterList = `Filters:
  T.. = Timeline support
  .S. = Slice threading
  ..C = Command support
  A = Audio input/output
  V = Video input/output
 ..C scale             V->V       Scale the input video size and/or convert the image format.
 .S. tonemap           V->V       Conversion to/from different dynamic ranges.
 .SC zscale            V->V       Apply resizing, colorspace and bit depth conversion.
`
)

func TestEncoderOf(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name              string
		encoders, filters string
		fail              bool
		want              hub.Encoder
	}{
		{"everything", encoderList, filterList, false, hub.Encoder{H264: true, ToneMap: true}},
		{"no zscale", encoderList, " ..C scale  V->V  Scale\n .S. tonemap  V->V  Conversion\n", false, hub.Encoder{H264: true}},
		{"no libx264", " A....D ac3  ATSC A/52A (AC-3)\n V....D mpeg2video  MPEG-2 video\n", filterList, false, hub.Encoder{ToneMap: true}},
		{"fails", encoderList, filterList, true, hub.Encoder{}},
	} {
		got, err := encoderOf(ctx, fakeFFmpeg(t, tc.encoders, tc.filters, tc.fail))
		if got != tc.want || (err != nil) != tc.fail {
			t.Errorf("%s: %+v, %v; want %+v", tc.name, got, err, tc.want)
		}
	}

	// The real one, if there is one.
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		if e, err := encoderOf(ctx, path); err != nil {
			t.Errorf("%s: %v", path, err)
		} else {
			t.Logf("%s: %+v", path, e)
		}
	}
}
