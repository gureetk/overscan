// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"math"
	"reflect"
	"strings"
	"testing"
)

// file returns an Info with the given format and streams, numbered in
// order.
func file(format string, streams ...Stream) Info {
	for i := range streams {
		streams[i].Index = i
	}
	return Info{Format: format, Duration: 5400, Streams: streams}
}

func video(codec string, w, h, depth int) Stream {
	return Stream{Type: Video, Codec: codec, Width: w, Height: h, BitDepth: depth}
}

func sound(codec string, channels int, lang string) Stream {
	return Stream{Type: Audio, Codec: codec, Channels: channels, Language: lang}
}

const mkv, mp4 = "matroska,webm", "mov,mp4,m4a,3gp,3g2,mj2"

func TestDecide(t *testing.T) {
	hd := video("h264", 1920, 1080, 8)
	cases := []struct {
		name string
		info Info
		want Plan
	}{
		{"plays as it is", file(mkv, hd, sound("aac", 2, "eng"), sound("dts", 6, "fre")), Plan{Video: -1}},
		{"no sound", file(mp4, hd), Plan{Video: -1}},
		{"sound the box cannot play", file(mkv, hd, sound("dts", 6, "eng"), sound("aac", 2, "fre")), Plan{
			Convert: true, Why: "its sound (dts)", Video: 0,
			Tracks: []Track{
				{Index: 1, Codec: "dts", Channels: 6, Language: "eng", Convert: true},
				{Index: 2, Codec: "aac", Channels: 2, Language: "fre"},
			},
		}},
		{"the default track plays first", file(mkv, hd,
			sound("ac3", 6, "eng"),
			Stream{Type: Audio, Codec: "truehd", Channels: 8, Language: "spa", Default: true},
			sound("opus", 2, "ita")), Plan{
			Convert: true, Why: "its sound (truehd)", Video: 0,
			Tracks: []Track{
				{Index: 2, Codec: "truehd", Channels: 8, Language: "spa", Convert: true},
				{Index: 1, Codec: "ac3", Channels: 6, Language: "eng"},
				{Index: 3, Codec: "opus", Channels: 2, Language: "ita", Convert: true}, // plays, but not from MPEG-TS
			},
		}},
		{"a format the box cannot play", file("flv", hd, sound("aac", 2, "")), Plan{
			Convert: true, Why: "its format (flv)", Video: 0,
			Tracks: []Track{{Index: 1, Codec: "aac", Channels: 2}},
		}},
		{"format and sound", file("flv", hd, sound("eac3", 6, "")), Plan{
			Convert: true, Why: "its format (flv) and its sound (eac3)", Video: 0,
			Tracks: []Track{{Index: 1, Codec: "eac3", Channels: 6, Convert: true}},
		}},
		{"video too big, which nothing converts", file(mkv, video("hevc", 3840, 2160, 10), sound("dts", 6, "")), Plan{
			Why: "its sound (dts)", Video: -1, VideoNote: "hevc 3840x2160 10-bit",
		}},
		{"10-bit H.264", file(mkv, video("h264", 1280, 720, 10), sound("aac", 2, "")), Plan{
			Video: -1, VideoNote: "h264 1280x720 10-bit",
		}},
		{"video that MPEG-TS cannot carry", file("asf", video("wmv3", 720, 480, 0), sound("wmav2", 2, "")), Plan{
			Why: "its sound (wmav2)", Video: -1,
		}},
		{"sound alone", file("matroska", sound("flac", 2, "")), Plan{
			Convert: true, Why: "its sound (flac)", Video: -1,
			Tracks: []Track{{Index: 0, Codec: "flac", Channels: 2, Convert: true}},
		}},
		{"a cover picture is not the video", file(mkv, Stream{Type: Video, Codec: "mjpeg", Picture: true}, hd, sound("dts", 2, "")), Plan{
			Convert: true, Why: "its sound (dts)", Video: 1,
			Tracks: []Track{{Index: 2, Codec: "dts", Channels: 2, Convert: true}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Decide(c.info, MAG322); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Decide =\n%+v\nwant\n%+v", got, c.want)
			}
		})
	}
}

func TestDecideCapsTracks(t *testing.T) {
	streams := []Stream{video("h264", 1280, 720, 8)}
	for range 12 {
		streams = append(streams, sound("dts", 6, ""))
	}
	plan := Decide(file(mkv, streams...), MAG322)
	if !plan.Convert || len(plan.Tracks) != maxTracks {
		t.Errorf("Convert %v with %d tracks, want %d", plan.Convert, len(plan.Tracks), maxTracks)
	}
}

func TestProfile(t *testing.T) {
	p := MAG322
	for format, want := range map[string]bool{mkv: true, mp4: true, "avi": true, "mpegts": true, "mpeg": true, "asf": true, "flv": false, "ogg": false, "": false} {
		if got := p.PlaysFormat(format); got != want {
			t.Errorf("PlaysFormat(%q) = %v", format, got)
		}
	}
	for _, c := range []struct {
		s    Stream
		want bool
	}{
		{video("h264", 1920, 1080, 8), true},
		{video("h264", 1920, 1088, 0), true},
		{video("h264", 1920, 1080, 10), false},
		{video("hevc", 1920, 1080, 10), true},
		{video("hevc", 3840, 2160, 8), false},
		{video("mpeg2video", 720, 576, 0), true},
		{video("av1", 1280, 720, 8), false},
		{video("vp9", 1280, 720, 8), false},
	} {
		if got := p.PlaysVideo(c.s); got != c.want {
			t.Errorf("PlaysVideo(%s) = %v", c.s.Describe(), got)
		}
	}
	for codec, want := range map[string]bool{"aac": true, "ac3": true, "mp3": true, "dts": false, "eac3": false, "truehd": false, "flac": false, "pcm_s24le": false} {
		if got := p.PlaysAudio(codec); got != want {
			t.Errorf("PlaysAudio(%q) = %v", codec, got)
		}
	}
	if ProfileFor("MAG322").Name != MAG322.Name || ProfileFor("").Name != MAG322.Name {
		t.Error("ProfileFor does not give every box the MAG322's profile")
	}
}

func TestArgs(t *testing.T) {
	plan := Plan{Convert: true, Video: 0, Tracks: []Track{
		{Index: 2, Codec: "truehd", Channels: 8, Convert: true},
		{Index: 1, Codec: "ac3", Channels: 6},
		{Index: 3, Codec: "dts", Channels: 2, Convert: true},
		{Index: 4, Codec: "flac", Convert: true},
		{Index: 5, Codec: "mp3", Channels: 1, Convert: true},
	}}
	got := strings.Join(plan.Args("http://127.0.0.1:9/in/abc", 61.4999999), " ")
	want := "-hide_banner -loglevel warning -protocol_whitelist http,tcp -format_whitelist " + formatWhitelist +
		" -nostdin -nostats -fflags +genpts -ss 61.631 -i http://127.0.0.1:9/in/abc -map 0:0 -c:v copy" +
		" -map 0:2 -c:a:0 ac3 -ac:a:0 6 -b:a:0 448k" +
		" -map 0:1 -c:a:1 copy" +
		" -map 0:3 -c:a:2 ac3 -ac:a:2 2 -b:a:2 192k" +
		" -map 0:4 -c:a:3 ac3 -ac:a:3 2 -b:a:3 192k" +
		" -map 0:5 -c:a:4 ac3 -ac:a:4 1 -b:a:4 96k" +
		" -max_muxing_queue_size 4096 -f mpegts pipe:1"
	if got != want {
		t.Errorf("Args =\n%s\nwant\n%s", got, want)
	}
	if got := strings.Join((Plan{Video: -1, Tracks: []Track{{Index: 0, Codec: "aac"}}}).Args("in", 0), " "); strings.Contains(got, "-ss") || strings.Contains(got, "-c:v") {
		t.Errorf("Args from the start, without video = %s", got)
	}
}

func TestKeyframe(t *testing.T) {
	info := Info{Start: -0.021}
	got := strings.Join(info.KeyframeArgs("http://127.0.0.1:9/in/abc", 0, 300), " ")
	if !strings.Contains(got, "-select_streams 0 ") || !strings.Contains(got, "-read_intervals 299.979%+#1 ") ||
		!strings.HasSuffix(got, " http://127.0.0.1:9/in/abc") || !strings.Contains(got, "-protocol_whitelist http,tcp") {
		t.Errorf("KeyframeArgs = %s", got)
	}
	for _, c := range []struct {
		out  string
		want float64
		ok   bool
	}{
		{"296.296000,296.213000\n", 296.317, true},
		{"N/A,4.000000\n", 4.021, true},
		{"4.000000,4.000000\n6.000000,6.000000\n", 4.021, true},
		{"", 0, false},
		{"N/A,N/A\n", 0, false},
		{"-1.5,x\n", 0, false},
	} {
		got, ok := info.ParseKeyframe([]byte(c.out))
		got = math.Round(got*1000) / 1000
		if got != c.want || ok != c.ok {
			t.Errorf("ParseKeyframe(%q) = %v, %v; want %v, %v", c.out, got, ok, c.want, c.ok)
		}
	}
}

func TestTranscodePlan(t *testing.T) {
	uhd := video("hevc", 3840, 2160, 10)
	info := file(mkv, uhd, sound("truehd", 8, "eng"), Stream{Type: Audio, Codec: "ac3", Channels: 6, Language: "fre", Default: true},
		sound("opus", 2, "ita"))
	plan := TranscodePlan(info, MAG322)
	want := Plan{Transcode: true, Video: 0, Why: "its video (hevc 3840x2160 10-bit)", VideoNote: "hevc 3840x2160 10-bit",
		Sound: []string{"ac3", "aac", "mp3", "mp2"},
		Tracks: []Track{
			{Index: 2, Codec: "ac3", Channels: 6, Language: "fre"},
			{Index: 1, Codec: "truehd", Channels: 8, Language: "eng", Convert: true},
			{Index: 3, Codec: "opus", Channels: 2, Language: "ita", Convert: true},
		}}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("TranscodePlan = %+v\nwant %+v", plan, want)
	}
	if plan := TranscodePlan(file(mkv, video("av1", 1920, 1080, 8)), MAG322); plan.Tracks != nil || plan.Video != 0 || !plan.Transcode {
		t.Errorf("without sound: %+v", plan)
	}
}

func TestTranscodeAudio(t *testing.T) {
	if got := TranscodeAudio(MAG322); !reflect.DeepEqual(got, []string{"ac3", "aac", "mp3", "mp2"}) {
		t.Errorf("MAG322: %v", got)
	}
	plays := MAG322.With(map[string]bool{"audio:ac3": false, "audio:eac3": true}, "")
	if got := TranscodeAudio(plays); !reflect.DeepEqual(got, []string{"aac", "eac3", "mp3", "mp2"}) {
		t.Errorf("without Dolby Digital: %v", got)
	}
	if got := TranscodeAudio(Profile{}); !reflect.DeepEqual(got, []string{"ac3"}) {
		t.Errorf("nothing: %v", got)
	}
}

func TestEncodePlan(t *testing.T) {
	uhd := video("hevc", 3840, 2160, 10)
	uhd.HDR, uhd.FrameRate = HDRPQ, 23.976
	info := file("flv", uhd, sound("dts", 6, "eng"), sound("aac", 2, "fre"))
	plan := EncodePlan(info, MAG322, true)
	want := Plan{Convert: true, Encode: true, Video: 0,
		Why: "its video (hevc 3840x2160 10-bit HDR) and its format (flv) and its sound (dts)", VideoNote: "hevc 3840x2160 10-bit HDR",
		Width: 1920, Height: 1080, ToneMap: true, FrameRate: 23.976,
		Tracks: []Track{
			{Index: 1, Codec: "dts", Channels: 6, Language: "eng", Convert: true},
			{Index: 2, Codec: "aac", Channels: 2, Language: "fre", Convert: true},
		}}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("EncodePlan = %+v\nwant %+v", plan, want)
	}
	// Without a filter to map HDR, the video is transcoded as it is.
	if plan := EncodePlan(info, MAG322, false); plan.ToneMap || !plan.Encode {
		t.Errorf("without tone mapping: %+v", plan)
	}
	// A small video keeps its size; a file without video plays as Decide says.
	if plan := EncodePlan(file(mkv, video("av1", 1280, 720, 8)), MAG322, true); plan.Width != 1280 || plan.Height != 720 ||
		plan.ToneMap || plan.Why != "its video (av1 1280x720 8-bit)" {
		t.Errorf("720p: %+v", plan)
	}
	if plan := EncodePlan(file(mkv, sound("dts", 6, "eng")), MAG322, true); plan.Encode || !plan.Convert {
		t.Errorf("no video: %+v", plan)
	}
	for _, c := range []struct{ w, h, ww, wh int }{
		{3840, 2160, 1920, 1080}, {3840, 1600, 1920, 800}, {1440, 1080, 1440, 1080}, {4096, 1716, 1920, 804},
		{1921, 1081, 1920, 1080}, {853, 481, 854, 482}, {2160, 3840, 608, 1080}, {0, 1080, 0, 0},
	} {
		if w, h := fit(c.w, c.h, 1920, 1080); w != c.ww || h != c.wh {
			t.Errorf("fit(%d, %d) = %d×%d, want %d×%d", c.w, c.h, w, h, c.ww, c.wh)
		}
	}
}

func TestEncodeArgs(t *testing.T) {
	plan := Plan{Convert: true, Encode: true, Video: 0, Width: 1920, Height: 1080, FrameRate: 23.976,
		Tracks: []Track{{Index: 1, Codec: "dts", Channels: 6, Convert: true}}}
	got := strings.Join(plan.Args("http://127.0.0.1:9/in/abc", 61.5), " ")
	want := "-hide_banner -loglevel warning -protocol_whitelist http,tcp -format_whitelist " + formatWhitelist +
		" -nostdin -nostats -fflags +genpts -ss 61.500 -i http://127.0.0.1:9/in/abc" +
		" -map 0:0 -c:v libx264 -preset veryfast -crf 21 -maxrate 8M -bufsize 16M -profile:v high -level:v 4.1" +
		" -vf scale=1920:1080,format=yuv420p" +
		" -map 0:1 -c:a:0 ac3 -ac:a:0 6 -b:a:0 448k -max_muxing_queue_size 4096 -f mpegts pipe:1"
	if got != want {
		t.Errorf("Args =\n%s\nwant\n%s", got, want)
	}
	plan.ToneMap, plan.FrameRate = true, 59.94
	got = strings.Join(plan.Args("in", 0), " ")
	if strings.Contains(got, "-ss") || !strings.Contains(got, "-level:v 4.2") ||
		!strings.Contains(got, " -vf zscale=w=1920:h=1080:f=bilinear,zscale=t=linear:npl=100:p=bt709,format=gbrpf32le,"+
			"tonemap=tonemap=hable:desat=0,zscale=t=bt709:m=bt709:r=tv,format=yuv420p ") ||
		!strings.Contains(got, "-color_primaries bt709 -color_trc bt709 -colorspace bt709") {
		t.Errorf("HDR at 60 pictures a second: %s", got)
	}
	plan.ToneMap, plan.Width, plan.Height = false, 0, 0
	if got := strings.Join(plan.Args("in", 0), " "); !strings.Contains(got, " -vf format=yuv420p ") {
		t.Errorf("without a size: %s", got)
	}
}
