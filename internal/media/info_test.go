// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// probed is what ffprobe 6.1 says of a test file that ffmpeg made: a test
// pattern in H.264, a 5.1 DTS track and a stereo AAC one, two subtitle
// tracks, a cover picture and a font, cut down to the fields that matter.
const probed = `{
    "streams": [
        {"index": 0, "codec_name": "h264", "profile": "High 10", "codec_type": "video", "width": 1920, "height": 1080,
         "pix_fmt": "yuv420p10le", "r_frame_rate": "24000/1001", "avg_frame_rate": "24000/1001",
         "disposition": {"default": 1, "attached_pic": 0}, "tags": {"DURATION": "00:00:12.000000000"}},
        {"index": 1, "codec_name": "dts", "profile": "DTS-HD MA", "codec_type": "audio", "sample_rate": "48000", "channels": 6,
         "channel_layout": "5.1(side)", "disposition": {"default": 1, "forced": 0}, "tags": {"language": "eng", "title": "Surround"}},
        {"index": 2, "codec_name": "aac", "profile": "LC", "codec_type": "audio", "channels": 2,
         "disposition": {"default": 0}, "tags": {"LANGUAGE": "fre"}},
        {"index": 3, "codec_name": "subrip", "codec_type": "subtitle", "disposition": {"default": 0, "forced": 0}, "tags": {"language": "eng"}},
        {"index": 4, "codec_name": "subrip", "codec_type": "subtitle", "disposition": {"default": 0, "forced": 1},
         "tags": {"language": "und", "title": "Signs\u0007 and  songs"}},
        {"index": 5, "codec_name": "mjpeg", "codec_type": "video", "width": 600, "height": 900, "pix_fmt": "yuvj420p",
         "bits_per_raw_sample": "8", "disposition": {"attached_pic": 1}, "tags": {"filename": "cover.jpg"}},
        {"index": 6, "codec_name": "ttf", "codec_type": "attachment", "tags": {"filename": "font.ttf"}}
    ],
    "format": {"filename": "a.mkv", "nb_streams": 7, "format_name": "matroska,webm", "start_time": "-0.021000",
               "duration": "12.021000", "size": "5062717", "bit_rate": "3369248", "probe_score": 100}
}`

func TestParseProbe(t *testing.T) {
	info, err := ParseProbe([]byte(probed))
	if err != nil {
		t.Fatal(err)
	}
	want := Info{
		Probed: ProbeVersion, Format: "matroska,webm", Duration: 12.021, Start: -0.021, Size: 5062717,
		Streams: []Stream{
			{Index: 0, Type: Video, Codec: "h264", Profile: "High 10", Width: 1920, Height: 1080, BitDepth: 10, Default: true,
				FrameRate: 23.976},
			{Index: 1, Type: Audio, Codec: "dts", Profile: "DTS-HD MA", Channels: 6, Language: "eng", Title: "Surround", Default: true},
			{Index: 2, Type: Audio, Codec: "aac", Profile: "LC", Channels: 2, Language: "fre"},
			{Index: 3, Type: Subtitle, Codec: "subrip", Language: "eng"},
			{Index: 4, Type: Subtitle, Codec: "subrip", Title: "Signs and songs", Forced: true},
			{Index: 5, Type: Video, Codec: "mjpeg", Width: 600, Height: 900, BitDepth: 8, Picture: true},
		},
	}
	if !reflect.DeepEqual(info, want) {
		t.Errorf("ParseProbe =\n%+v\nwant\n%+v", info, want)
	}
	if v := info.First(Video); v == nil || v.Index != 0 {
		t.Errorf("First(Video) = %+v", v)
	}
	if got := len(info.Of(Video)); got != 1 {
		t.Errorf("Of(Video) has %d streams, want the one that is not a picture", got)
	}
	if got, want := info.Summary(), "matroska: h264 1920x1080 10-bit; sound dts 6ch eng, aac 2ch fre; subtitles subrip eng, subrip"; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}

	// What is kept survives a round trip through the store's JSON.
	b, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	var back Info
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, info) {
		t.Errorf("round trip: %+v, %v", back, err)
	}
}

func TestParseProbeHDR(t *testing.T) {
	for transfer, want := range map[string]string{"smpte2084": HDRPQ, "arib-std-b67": HDRHLG, "bt709": "", "": ""} {
		raw := `{"streams": [{"index": 0, "codec_name": "hevc", "codec_type": "video", "width": 3840, "height": 2160,
			"pix_fmt": "yuv420p10le", "color_transfer": "` + transfer + `", "avg_frame_rate": "0/0", "r_frame_rate": "50/1"}],
			"format": {"format_name": "matroska,webm"}}`
		info, err := ParseProbe([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		v := info.First(Video)
		if v.HDR != want || v.FrameRate != 50 {
			t.Errorf("%q: HDR %q, %v pictures a second", transfer, v.HDR, v.FrameRate)
		}
		if d := v.Describe(); (want != "") != strings.HasSuffix(d, " HDR") {
			t.Errorf("%q: described as %q", transfer, d)
		}
	}
	for in, want := range map[string]float64{"25/1": 25, "30000/1001": 29.97, "60": 60, "0/0": 0, "1/0": 0, "x": 0,
		"1000000/1": 0, "-25/1": 0, "": 0} {
		if got := frameRate(in); got != want {
			t.Errorf("frameRate(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseProbeRefuses(t *testing.T) {
	for _, in := range []string{``, `[]`, `{"streams": []}`, `{"format": {"format_name": ""}}`, `{"streams": "x", "format": {"format_name": "avi"}}`} {
		if _, err := ParseProbe([]byte(in)); err == nil {
			t.Errorf("ParseProbe(%q) took it", in)
		}
	}
}

func TestParseProbeBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString(`{"format": {"format_name": "mpegts", "duration": "N/A", "size": "-4"}, "streams": [`)
	for i := range 100 {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"index": ` + itoa(i) + `, "codec_type": "audio", "codec_name": "aac", "channels": 999}`)
	}
	b.WriteString(`, {"index": -3, "codec_type": "video", "codec_name": "` + strings.Repeat("x", 300) + `", "width": 99999}]}`)
	info, err := ParseProbe([]byte(b.String()))
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration != 0 || info.Size != 0 {
		t.Errorf("duration %v and size %d, want 0 for both", info.Duration, info.Size)
	}
	if len(info.Streams) != maxStreams {
		t.Errorf("kept %d streams, want %d", len(info.Streams), maxStreams)
	}
	if info.Streams[0].Channels != 0 {
		t.Errorf("channels %d, want 0 for a count that makes no sense", info.Streams[0].Channels)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestBitDepth(t *testing.T) {
	for _, c := range []struct {
		raw, pix string
		want     int
	}{
		{"10", "yuv420p", 10},
		{"", "yuv420p10le", 10},
		{"", "yuv420p12be", 12},
		{"", "p010le", 10},
		{"", "yuv420p", 8},
		{"", "nv12", 8},
		{"N/A", "rgb24", 0},
		{"", "", 0},
		{"99", "yuv420p", 8},
	} {
		if got := bitDepth(c.raw, c.pix); got != c.want {
			t.Errorf("bitDepth(%q, %q) = %d, want %d", c.raw, c.pix, got, c.want)
		}
	}
}

func TestClean(t *testing.T) {
	for _, c := range []struct {
		in   string
		max  int
		want string
	}{
		{"  Director's\tcut \n", 40, "Director's cut"},
		{"\xff\xfeEnglish", 40, "English"},
		{"Ελληνικά", 7, "Ελλ"},
		{"abc", 3, "abc"},
	} {
		if got := clean(c.in, c.max); got != c.want {
			t.Errorf("clean(%q, %d) = %q, want %q", c.in, c.max, got, c.want)
		}
	}
}

func FuzzParseProbe(f *testing.F) {
	f.Add([]byte(probed))
	f.Add([]byte(`{"format": {"format_name": "avi"}, "streams": [{"codec_type": "video", "pix_fmt": "yuv420p9"}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := ParseProbe(data)
		if err != nil {
			return
		}
		if info.Format == "" || len(info.Streams) > maxStreams || info.Duration < 0 || info.Size < 0 {
			t.Fatalf("ParseProbe gave %+v", info)
		}
		for _, s := range info.Streams {
			if len(s.Title) > maxText || len(s.Codec) > maxName || s.Width < 0 || s.Channels < 0 {
				t.Fatalf("stream %+v", s)
			}
		}
		_ = info.Summary()
		_ = Decide(info, MAG322).Args("http://127.0.0.1:1/in/x", 1)
	})
}
