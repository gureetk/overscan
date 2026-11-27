// SPDX-License-Identifier: AGPL-3.0-or-later

package streamprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestFind(t *testing.T) {
	for _, p := range Profiles {
		got, ok := Find(p.Name)
		if !ok || got.Name != p.Name || got.Label == "" || got.Transcodes() != (p.Name != Source) {
			t.Errorf("Find(%q) = %+v, %v", p.Name, got, ok)
		}
	}
	if _, ok := Find(""); ok {
		t.Error(`Find("") found a profile`)
	}
}

// Each profile that transcodes makes H.264 of an interlaced 1080i stream,
// scaled to fit it, with AAC in stereo, with the ffmpeg on this machine.
func TestArgsWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	// Four seconds of MPEG-2 at 1920×1080, interlaced, with five-channel
	// MP2 sound, as a broadcast channel might send.
	source, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "4", "-vf", "setfield=tff", "-flags", "+ilme+ildct", "-c:v", "mpeg2video", "-b:v", "8M",
		"-af", "pan=5.0|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0", "-c:a", "ac3", "-f", "mpegts", "pipe:1").Output()
	if err != nil {
		t.Fatalf("making the source: %v", err)
	}
	for _, p := range Profiles {
		if !p.Transcodes() {
			continue
		}
		cmd := exec.CommandContext(ctx, ffmpeg, p.Args()...)
		cmd.Stdin = bytes.NewReader(source)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v: %s", p.Name, err, stderr.String())
		}
		probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_streams", "-of", "json", "pipe:0")
		probe.Stdin = bytes.NewReader(out)
		data, err := probe.Output()
		if err != nil {
			t.Fatalf("%s: probing: %v", p.Name, err)
		}
		var got struct {
			Streams []struct {
				CodecType  string `json:"codec_type"`
				CodecName  string `json:"codec_name"`
				Width      int    `json:"width"`
				Height     int    `json:"height"`
				Channels   int    `json:"channels"`
				FieldOrder string `json:"field_order"`
			}
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		var video, audio bool
		for _, s := range got.Streams {
			switch s.CodecType {
			case "video":
				video = s.CodecName == "h264" && s.Width <= p.Width && s.Height <= p.Height && s.Width > 0 &&
					s.Width*9 == s.Height*16 && (s.FieldOrder == "" || s.FieldOrder == "progressive")
				if !video {
					t.Errorf("%s: video %+v", p.Name, s)
				}
			case "audio":
				audio = s.CodecName == "aac" && s.Channels == 2
				if !audio {
					t.Errorf("%s: audio %+v", p.Name, s)
				}
			}
		}
		if !video || !audio {
			t.Errorf("%s: streams %+v", p.Name, got.Streams)
		}
	}
}

func TestCheck(t *testing.T) {
	p, err := Check(Profile{Name: "kitchen"})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Made || p.Label != "kitchen" || p.Codec != H264 || p.Quality != Medium || p.FrameRate != RateSource ||
		p.Sound != SoundAAC || !p.Transcodes() || p.Encoder() != "libx264" || p.VideoCodec() != H264 {
		t.Errorf("Check filled in %+v", p)
	}
	if p, _ := Check(Profile{Name: "hevc-1080p", Codec: HEVC}); p.Encoder() != "libx265" {
		t.Errorf("HEVC's encoder is %q", p.Encoder())
	}
	for _, bad := range []Profile{
		{}, {Name: "Kitchen"}, {Name: "-kitchen"}, {Name: "kitchen-"}, {Name: "kit chen"}, {Name: "kitchen;rm"},
		{Name: strings.Repeat("a", 41)}, {Name: "source"}, {Name: "h264-720p"}, {Name: "never"},
		{Name: "a", Codec: "av1"}, {Name: "a", MaxLines: 480}, {Name: "a", Quality: "best"},
		{Name: "a", MaxKbps: 100}, {Name: "a", MaxKbps: 50000}, {Name: "a", FrameRate: "60"}, {Name: "a", Sound: "dts"},
	} {
		if _, err := Check(bad); err == nil {
			t.Errorf("Check(%+v) took it", bad)
		} else if _, ok := err.(*Error); !ok {
			t.Errorf("Check(%+v): %T", bad, err)
		}
	}
	// A fixed profile's settings never carry over.
	p, _ = Check(Profile{Name: "a", Width: 640, Height: 360, CRF: 51, MaxRate: "1", Level: "9"})
	if p.Width != 0 || p.CRF != 0 || p.MaxRate != "" || p.Level != "" {
		t.Errorf("Check kept a fixed profile's settings: %+v", p)
	}
}

// The fixed profiles' arguments are what they have been since D110.
func TestFixedArgs(t *testing.T) {
	p, _ := Find("h264-720p")
	got := strings.Join(p.Args(), " ")
	want := "-hide_banner -nostdin -nostats -loglevel warning -protocol_whitelist pipe -fflags +genpts " +
		"-analyzeduration 2000000 -probesize 4000000 -f mpegts -i pipe:0 -map 0:v:0? -map 0:a:0? " +
		"-vf bwdif=deint=interlaced,scale=w='min(1280,iw)':h='min(720,ih)':force_original_aspect_ratio=decrease:force_divisible_by=2,format=yuv420p " +
		"-c:v libx264 -preset veryfast -crf 23 -maxrate 3M -bufsize 6M -profile:v high -level:v 4.0 " +
		"-force_key_frames expr:gte(t,n_forced*2) -c:a aac -b:a 128k -ac 2 -max_interleave_delta 2000000 -f mpegts pipe:1"
	if got != want {
		t.Errorf("h264-720p's arguments changed:\n got %s\nwant %s", got, want)
	}
}

func TestMadeArgs(t *testing.T) {
	for _, c := range []struct {
		p    Profile
		want []string
	}{
		{Profile{Name: "a", Codec: H264, MaxLines: 720, Quality: High, Deinterlace: true, Sound: SoundAAC},
			[]string{"-map 0:a:0?", "-vf bwdif=mode=send_field:deint=interlaced,scale=w='min(1280,iw)':h='min(720,ih)'",
				"-c:v libx264 -preset veryfast -crf 20 -profile:v high", "-c:a aac -b:a 128k -ac 2"}},
		{Profile{Name: "b", Codec: HEVC, MaxLines: 1080, Quality: Low, MaxKbps: 8000, FrameRate: RateAtMost, Deinterlace: true, Sound: SoundAC3},
			[]string{"-vf bwdif=mode=send_frame:deint=interlaced," + halfFast + ",scale=w='min(1920,iw)':h='min(1080,ih)'",
				"-c:v libx265 -preset veryfast -crf 31 -profile:v main", "-maxrate 8000k -bufsize 16000k", "-c:a ac3 -b:a 384k"}},
		{Profile{Name: "c", Codec: H264, Quality: Medium, FrameRate: RateAtMost, Sound: SoundCopy},
			[]string{"-map 0:a?", "-vf " + halfFast + ",format=yuv420p ", "-crf 23", "-c:a copy"}},
		{Profile{Name: "d", Codec: H264, Quality: Medium, Sound: SoundAAC},
			[]string{"-vf format=yuv420p "}},
	} {
		p, err := Check(c.p)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(p.Args(), " ")
		for _, w := range c.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: %q not in %s", p.Name, w, got)
			}
		}
		if !strings.HasPrefix(got, "-hide_banner -nostdin -nostats -loglevel warning -protocol_whitelist pipe ") ||
			!strings.Contains(got, " -force_key_frames expr:gte(t,n_forced*2) ") ||
			!strings.HasSuffix(got, " -max_interleave_delta 2000000 -f mpegts pipe:1") {
			t.Errorf("%s: %s", p.Name, got)
		}
	}
}

// Profiles that the administrator makes transcode an interlaced 1080i
// stream with the ffmpeg on this machine as their settings say.
func TestMadeArgsWithFFmpeg(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// Four seconds of H.264 at 1920×1080, interlaced at 25 frames a
	// second, with five-channel Dolby Digital and a second, stereo MP2
	// track, as a broadcast channel might send.
	source, err := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=25", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-t", "4", "-map", "0:v", "-map", "1:a", "-map", "1:a", "-c:v", "libx264", "-preset", "ultrafast",
		"-flags", "+ildct+ilme", "-x264-params", "tff=1", "-pix_fmt", "yuv420p",
		"-filter:a:0", "pan=5.0|c0=c0|c1=c0|c2=c0|c3=c0|c4=c0", "-c:a:0", "ac3", "-c:a:1", "mp2", "-ac:a:1", "2",
		"-f", "mpegts", "pipe:1").Output()
	if err != nil {
		t.Fatalf("making the source: %v", err)
	}
	hasX265 := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-h", "encoder=libx265").Run() == nil
	for _, c := range []struct {
		p             Profile
		codec         string
		width, height int
		rate          string
		sound         []string
		channels      []int
		needsHEVC     bool
	}{
		{Profile{Name: "small", MaxLines: 720, Quality: Low, Deinterlace: true}, "h264", 1280, 720, "50/1", []string{"aac"}, []int{2}, false},
		{Profile{Name: "steady", MaxLines: 576, FrameRate: RateAtMost, Deinterlace: true, Sound: SoundAC3, MaxKbps: 2000}, "h264", 1024, 576, "25/1", []string{"ac3"}, []int{5}, false},
		{Profile{Name: "tracks", Deinterlace: true, Sound: SoundCopy, FrameRate: RateAtMost}, "h264", 1920, 1080, "25/1", []string{"ac3", "mp2"}, []int{5, 2}, false},
		{Profile{Name: "hevc", Codec: HEVC, MaxLines: 720, Quality: High, FrameRate: RateAtMost, Deinterlace: true}, "hevc", 1280, 720, "25/1", []string{"aac"}, []int{2}, true},
	} {
		if c.needsHEVC && !hasX265 {
			t.Logf("%s: no libx265", c.p.Name)
			continue
		}
		p, err := Check(c.p)
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.CommandContext(ctx, ffmpeg, p.Args()...)
		cmd.Stdin = bytes.NewReader(source)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v: %s", p.Name, err, stderr.String())
		}
		probe := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_streams", "-of", "json", "pipe:0")
		probe.Stdin = bytes.NewReader(out)
		data, err := probe.Output()
		if err != nil {
			t.Fatalf("%s: probing: %v", p.Name, err)
		}
		var got struct {
			Streams []struct {
				CodecType  string `json:"codec_type"`
				CodecName  string `json:"codec_name"`
				Width      int    `json:"width"`
				Height     int    `json:"height"`
				Channels   int    `json:"channels"`
				FieldOrder string `json:"field_order"`
				Rate       string `json:"r_frame_rate"`
			}
		}
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatal(err)
		}
		var sound []string
		var channels []int
		for _, s := range got.Streams {
			switch s.CodecType {
			case "video":
				if s.CodecName != c.codec || s.Width != c.width || s.Height != c.height || s.Rate != c.rate ||
					(s.FieldOrder != "" && s.FieldOrder != "progressive") {
					t.Errorf("%s: video %+v", p.Name, s)
				}
			case "audio":
				sound = append(sound, s.CodecName)
				channels = append(channels, s.Channels)
			}
		}
		if !slices.Equal(sound, c.sound) || !slices.Equal(channels, c.channels) {
			t.Errorf("%s: sound %v %v, want %v %v", p.Name, sound, channels, c.sound, c.channels)
		}
	}
}
