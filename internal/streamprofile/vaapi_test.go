// SPDX-License-Identifier: AGPL-3.0-or-later

package streamprofile

import (
	"strings"
	"testing"
)

func TestHardwareArgs(t *testing.T) {
	small := Profile{Name: "small", Made: true, Codec: HEVC, MaxLines: 720, Quality: Low, MaxKbps: 3000,
		FrameRate: RateAtMost, Deinterlace: true, ToneMap: true, Sound: SoundAC3}
	steady := Profile{Name: "steady", Made: true, Codec: H264, Quality: High, FrameRate: RateSource,
		Deinterlace: true, Sound: SoundCopy}
	dev := "/dev/dri/renderD128"
	all := Hardware{Device: dev, ICQ: true, VBR: true, Filters: true, Decode: true, Rate: true}
	fixed, _ := Find("h264-720p")
	cases := []struct {
		name   string
		p      Profile
		hw     Hardware
		want   []string
		absent []string
	}{
		{"all on the graphics", small, all,
			[]string{"-init_hw_device vaapi=va:/dev/dri/renderD128 -filter_hw_device va -hwaccel vaapi -hwaccel_device va -hwaccel_output_format vaapi -f mpegts -i pipe:0 -map 0:v:0? -map 0:a:0? ",
				"-vf deinterlace_vaapi=rate=frame:auto=1," + HalfFast + ",scale_vaapi=w='min(1280,iw)':h='min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2:format=nv12 ",
				"-c:v hevc_vaapi -profile:v main -rc_mode VBR -b:v 2250k -maxrate 3000k -bufsize 6000k -force_key_frames expr:gte(t,n_forced*2) -c:a ac3 -b:a 384k ",
				"-f mpegts pipe:1"},
			[]string{"tonemap", "low_power", "libx26"}},
		{"no fps on the graphics", small, Hardware{Device: dev, LowPower: true, ICQ: true, Filters: true, Decode: true},
			[]string{"-vf " + HalfFast + ",format=nv12,hwupload,deinterlace_vaapi=rate=frame:auto=1,scale_vaapi=w='min(1280,iw)'",
				"-c:v hevc_vaapi -profile:v main -low_power 1 -rc_mode ICQ -global_quality 29 "},
			[]string{"-hwaccel", "VBR", "maxrate"}},
		{"HDR10 to SDR", small, Hardware{Device: dev, Filters: true, Decode: true, Rate: true, ToneMap: true},
			[]string{"-hwaccel vaapi ", "-vf deinterlace_vaapi=rate=frame:auto=1," + HalfFast + "," + ToneMapVAAPI + ",scale_vaapi=",
				"-rc_mode CQP -qp 29 "},
			nil},
		{"decoded in software", small, Hardware{Device: dev, Filters: true, ToneMap: true},
			[]string{"-vf " + HalfFast + ",format=p010,hwupload,deinterlace_vaapi=rate=frame:auto=1," + ToneMapVAAPI + ",scale_vaapi="},
			[]string{"-hwaccel"}},
		{"filters in software", small, Hardware{Device: dev, Decode: true, Rate: true},
			[]string{"-vf bwdif=mode=send_frame:deint=interlaced," + HalfFast + ",scale=w='min(1280,iw)':h='min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,format=nv12,hwupload "},
			[]string{"-hwaccel", "_vaapi=", "tonemap"}},
		{"the source's size and rate", steady, all,
			[]string{"-map 0:a? ", "-vf deinterlace_vaapi=rate=field:auto=1,scale_vaapi=format=nv12 ",
				"-c:v h264_vaapi -profile:v high -rc_mode ICQ -global_quality 22 ", "-c:a copy "},
			[]string{"fps=", "VBR"}},
		{"a fixed profile", fixed, all,
			[]string{"-vf deinterlace_vaapi=rate=field:auto=1,scale_vaapi=w='min(1280,iw)':h='min(720,ih)'",
				"-c:v h264_vaapi -profile:v high -rc_mode VBR -b:v 2250k -maxrate 3000k -bufsize 6000k ",
				"-c:a aac -b:a 128k -ac 2 "},
			[]string{"fps=", "libx264", "-level"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(c.p.HardwareArgs(c.hw), " ")
			for _, w := range c.want {
				if !strings.Contains(got, w) {
					t.Errorf("args lack %q:\n%s", w, got)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(got, a) {
					t.Errorf("args hold %q:\n%s", a, got)
				}
			}
			if !strings.HasPrefix(got, "-hide_banner -nostdin -nostats -loglevel warning -protocol_whitelist pipe ") {
				t.Errorf("args start %q", got)
			}
		})
	}
}

func TestDecodesOnGPU(t *testing.T) {
	at30 := Profile{Made: true, Codec: H264, FrameRate: RateAtMost}
	steady := Profile{Made: true, Codec: H264, FrameRate: RateSource}
	for _, c := range []struct {
		p    Profile
		hw   Hardware
		want bool
	}{
		{steady, Hardware{Filters: true, Decode: true}, true},
		{steady, Hardware{Decode: true}, false},
		{at30, Hardware{Filters: true, Decode: true}, false},
		{at30, Hardware{Filters: true, Decode: true, Rate: true}, true},
	} {
		if got := c.p.DecodesOnGPU(c.hw); got != c.want {
			t.Errorf("%+v with %+v: %v", c.p, c.hw, got)
		}
	}
}
