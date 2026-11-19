// SPDX-License-Identifier: AGPL-3.0-or-later

package media

import (
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCapability(t *testing.T) {
	good := []string{"format:mov", "video:hevc", "video:hevc:10bit", "video:h264:2160p", "audio:dts"}
	for _, c := range good {
		if _, _, _, ok := capability(c); !ok {
			t.Errorf("capability(%q) is not one", c)
		}
	}
	bad := []string{"", "video", "video:", "audio:dts:10bit", "format:mov:2160p", "video:hevc:12bit", "subtitles:srt", "a:b:c:d"}
	for _, c := range bad {
		if _, _, _, ok := capability(c); ok {
			t.Errorf("capability(%q) is one", c)
		}
	}
}

func TestProfilePlays(t *testing.T) {
	cases := map[string]bool{
		"format:matroska":   true,
		"format:flv":        false,
		"audio:ac3":         true,
		"audio:dts":         false,
		"video:h264":        true,
		"video:h264:10bit":  false, // 8 bits at most
		"video:hevc:10bit":  true,
		"video:mpeg2video":  true,
		"video:mpeg4:10bit": true,  // no cap on its depth
		"video:hevc:2160p":  false, // 1920×1088 at most
		"video:av1":         false,
		"nonsense":          false,
	}
	for c, want := range cases {
		if got := MAG322.Plays(c); got != want {
			t.Errorf("MAG322.Plays(%q) = %v, want %v", c, got, want)
		}
	}
}

func TestProfileWith(t *testing.T) {
	p := MAG322.With(map[string]bool{
		"audio:dts":        true,
		"audio:opus":       false,
		"format:asf":       false,
		"format:flv":       true,
		"video:hevc:10bit": false,
		"video:hevc:2160p": true,
		"video:h264:10bit": true,
		"video:av1":        true,
		"video:vp9:10bit":  true, // a codec found only at 10 bits plays
		"video:mpeg4":      false,
		"video:vc1:2160p":  false,
		"nonsense":         true,
	}, "checked")
	if p.Name != "checked" {
		t.Errorf("name %q", p.Name)
	}
	if !p.PlaysAudio("dts") || p.PlaysAudio("opus") || !p.PlaysAudio("aac") {
		t.Errorf("audio %v", p.Audio)
	}
	if p.PlaysFormat("asf") || !p.PlaysFormat("flv") || !p.PlaysFormat("matroska,webm") {
		t.Errorf("formats %v", p.Formats)
	}
	plays := func(codec string, w, h, depth int) bool { return p.PlaysVideo(video(codec, w, h, depth)) }
	checks := []struct {
		codec       string
		w, h, depth int
		want        bool
	}{
		{"hevc", 1920, 1080, 10, false},
		{"hevc", 1920, 1080, 8, true},
		{"hevc", 3840, 2160, 8, true},
		{"h264", 1920, 1080, 10, true},
		{"h264", 3840, 2160, 8, false}, // the profile's cap
		{"av1", 1920, 1080, 8, true},
		{"av1", 1920, 1080, 10, false},
		{"vp9", 1920, 1080, 10, true},
		{"mpeg4", 720, 576, 8, false},
		{"vc1", 1920, 1080, 8, true},
		{"vc1", 3840, 2160, 8, false},
	}
	for _, c := range checks {
		if got := plays(c.codec, c.w, c.h, c.depth); got != c.want {
			t.Errorf("plays %s %dx%d at %d bits = %v, want %v", c.codec, c.w, c.h, c.depth, got, c.want)
		}
	}
	// The datasheet's profile is left as it was.
	if !MAG322.PlaysAudio("opus") || MAG322.PlaysAudio("dts") || MAG322.Video[1].MaxWidth != 0 {
		t.Error("With changed the profile it started from")
	}
}

func TestCheckClips(t *testing.T) {
	ids := map[string]bool{}
	tested := map[string]bool{}
	for i, c := range CheckClips {
		if ids[c.ID] {
			t.Errorf("clip %q twice", c.ID)
		}
		ids[c.ID] = true
		if c.Label == "" || c.Ext == "" || c.Muxer == "" || c.Width == 0 || c.Seconds == 0 || len(c.Video) == 0 || len(c.Audio) == 0 {
			t.Errorf("clip %q is missing something: %+v", c.ID, c)
		}
		if c.Ask != AskPicture && c.Ask != AskSound && c.Ask != AskBoth {
			t.Errorf("clip %q asks %q", c.ID, c.Ask)
		}
		if len(c.Tests) == 0 || (i > 0) != (len(c.Needs) > 0) {
			t.Errorf("clip %q tests %v and needs %v", c.ID, c.Tests, c.Needs)
		}
		for _, k := range append(slices.Clone(c.Tests), c.Needs...) {
			if _, _, _, ok := capability(k); !ok {
				t.Errorf("clip %q: %q is not a capability", c.ID, k)
			}
		}
		for _, k := range c.Tests {
			if tested[k] {
				t.Errorf("%q is tested twice", k)
			}
			tested[k] = true
			if CapabilityLabel(k) == "" {
				t.Errorf("%q has no label", k)
			}
		}
	}
	// What clips need is tested by others, so that a clip that does not
	// play says something.
	for _, c := range CheckClips {
		for _, k := range c.Needs {
			if !tested[k] {
				t.Errorf("clip %q needs %q, which no clip tests", c.ID, k)
			}
		}
	}
	if CheckClips[0].ID != "base" {
		t.Errorf("the first clip is %q", CheckClips[0].ID)
	}
	if got := Capabilities(); len(got) != len(tested) || got[0] != "format:matroska" {
		t.Errorf("Capabilities() = %v", got)
	}
	if c, ok := CheckClipByID("dts"); !ok || c.Tests[0] != "audio:dts" {
		t.Errorf("CheckClipByID(dts) = %+v, %v", c, ok)
	}
	if _, ok := CheckClipByID("nope"); ok {
		t.Error("CheckClipByID found a clip that is not one")
	}
}

func TestFindings(t *testing.T) {
	cases := []struct {
		name     string
		outcomes map[string]string
		want     map[string]bool
	}{
		{"the first clip", map[string]string{"base": Plays},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true}},
		{"sound that does not play", map[string]string{"base": Plays, "dts": Fails, "eac3": Plays, "opus": ""},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true, "audio:dts": false, "audio:eac3": true}},
		{"a format whose codecs play", map[string]string{"base": Plays, "mpeg4": Plays, "mp3": Plays, "avi": Fails},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true, "video:mpeg4": true,
				"audio:mp3": true, "format:avi": false}},
		{"a format whose video does not play", map[string]string{"base": Plays, "mpeg4": Fails, "mp3": Plays, "avi": Fails},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true, "video:mpeg4": false,
				"audio:mp3": true}},
		{"what a clip needs, from the datasheet", map[string]string{"base": Plays, "vob": Fails},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true, "format:mpeg": false}},
		{"what plays, whatever else was found", map[string]string{"base": Plays, "hevc": Fails, "hevc-10bit": Plays},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true, "video:hevc": true,
				"video:hevc:10bit": true}},
		{"4K without its codec", map[string]string{"base": Plays, "hevc": Fails, "hevc-2160p": Fails},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true, "video:hevc": false}},
		{"a clip the check does not have", map[string]string{"base": Plays, "nope": Plays},
			map[string]bool{"format:matroska": true, "video:h264": true, "audio:aac": true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Findings(c.outcomes, MAG322); !maps.Equal(got, c.want) {
				t.Errorf("Findings = %v, want %v", got, c.want)
			}
		})
	}
}

func TestCheckClipArgs(t *testing.T) {
	all := func(string) bool { return true }
	c, _ := CheckClipByID("av1")
	args, ok := c.Args(all, "/tmp/av1.part")
	if !ok || !slices.Contains(args, "libaom-av1") || args[len(args)-1] != "/tmp/av1.part" || !slices.Contains(args, "matroska") {
		t.Errorf("args %v", args)
	}
	// The next AV1 encoder, when ffmpeg lacks the first.
	args, ok = c.Args(func(e string) bool { return e != "libaom-av1" }, "out")
	if !ok || !slices.Contains(args, "libsvtav1") || slices.Contains(args, "libaom-av1") {
		t.Errorf("args without libaom-av1: %v", args)
	}
	if _, ok := c.Args(func(e string) bool { return e == "aac" }, "out"); ok {
		t.Error("a clip without an AV1 encoder can be made")
	}
	c, _ = CheckClipByID("dts")
	args, _ = c.Args(all, "out")
	line := strings.Join(args, " ")
	for _, want := range []string{"-c:a dca -strict -2", "testsrc2=size=640x360:rate=25:duration=10", "-f lavfi", "-map 1:a"} {
		if !strings.Contains(line, want) {
			t.Errorf("args lack %q: %s", want, line)
		}
	}
}

// TestMakeCheckClips makes each clip, a second long, with ffmpeg, and checks
// with ffprobe that it holds what it tests.
func TestMakeCheckClips(t *testing.T) {
	ffmpeg, ffprobe := tools(t)
	if testing.Short() {
		t.Skip("makes 23 clips")
	}
	out := run(t, ffmpeg, "-hide_banner", "-encoders")
	has := func(name string) bool { return strings.Contains(string(out), " "+name+" ") }
	dir := t.TempDir()
	for _, c := range CheckClips {
		c.Seconds = 1
		path := filepath.Join(dir, c.ID+"."+c.Ext)
		args, ok := c.Args(has, path)
		if !ok {
			t.Logf("ffmpeg cannot make %s", c.ID)
			continue
		}
		run(t, ffmpeg, args...)
		var probe struct {
			Format struct {
				Name string `json:"format_name"`
			} `json:"format"`
			Streams []struct {
				Type   string `json:"codec_type"`
				Codec  string `json:"codec_name"`
				Pix    string `json:"pix_fmt"`
				Width  int    `json:"width"`
				Height int    `json:"height"`
			} `json:"streams"`
		}
		data := run(t, ffprobe, "-v", "error", "-show_format", "-show_streams", "-of", "json", path)
		if err := json.Unmarshal(data, &probe); err != nil {
			t.Fatal(err)
		}
		got := []string{"format:" + strings.Split(probe.Format.Name, ",")[0]}
		for _, s := range probe.Streams {
			got = append(got, s.Type+":"+s.Codec)
			if s.Type == "video" {
				if strings.Contains(s.Pix, "10le") {
					got = append(got, "video:"+s.Codec+":10bit")
				}
				if s.Height >= 2160 {
					got = append(got, "video:"+s.Codec+":2160p")
				}
			}
		}
		for _, k := range append(slices.Clone(c.Tests), c.Needs...) {
			if !slices.Contains(got, k) {
				t.Errorf("clip %s holds %v, not %s", c.ID, got, k)
			}
		}
	}
}
