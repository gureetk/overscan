// SPDX-License-Identifier: AGPL-3.0-or-later

package streamprofile

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
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
