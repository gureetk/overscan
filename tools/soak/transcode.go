// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The soak's transcodes (D147): channels that stream pictures that ffmpeg
// makes, live TV transcoded always with a profile, and the server's
// ffmpegs measured as it runs them.

// pictureSource makes the pictures of the first n channels: for each
// request, an ffmpeg of the soak's own makes ffmpeg's test pattern of size
// at the pace that it plays, in H.264 at bitrate with AAC, in MPEG-TS, for
// as long as the request lasts.
type pictureSource struct {
	n       int
	size    string
	bitrate int
	ffmpeg  string
}

func (p pictureSource) serve(w http.ResponseWriter, r *http.Request) {
	kbps := strconv.Itoa(p.bitrate/1000) + "k"
	cmd := exec.CommandContext(r.Context(), p.ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin", "-re",
		"-f", "lavfi", "-i", "testsrc2=size="+p.size+":rate=25",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000",
		"-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "50", "-pix_fmt", "yuv420p",
		"-b:v", kbps, "-maxrate", kbps, "-bufsize", kbps,
		"-c:a", "aac", "-b:a", "128k", "-f", "mpegts", "pipe:1")
	out, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := cmd.Start(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer cmd.Wait()
	rc := http.NewResponseController(w)
	buf := make([]byte, 188*64)
	for {
		n, err := out.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

// madeProfiles are the profiles that -transcode can name beyond the fixed
// ones, which the soak makes.
var madeProfiles = map[string]map[string]any{
	"small":     {"name": "small", "codec": "h264", "max_lines": 576, "quality": "low"},
	"hevc-720p": {"name": "hevc-720p", "codec": "hevc", "max_lines": 720, "quality": "medium"},
}

// transcoding sets live TV to be transcoded always with o.transcode, making
// the profile first where the soak makes it, and lets the server transcode
// as many streams as there are boxes, up to its most.
func (s *server) transcoding(o options) error {
	a, err := s.signIn()
	if err != nil {
		return err
	}
	if p, ok := madeProfiles[o.transcode]; ok {
		if err := a.call("POST", "transcode-profiles", p, nil); err != nil {
			return err
		}
	}
	// A channel's HLS players share one transcode (D155), beside each
	// box's own; the server allows three at most.
	limit := min(o.boxes+min(o.hls, o.pictures), 3)
	if err := a.call("PATCH", "settings", map[string]any{"transcode_video": limit,
		"live_transcode": map[string]any{"mode": "always", "profile": o.transcode}}, nil); err != nil {
		return err
	}
	fmt.Printf("live TV transcoded always with %s, %d at a time; the first %d channels stream pictures of %s\n",
		o.transcode, limit, o.pictures, o.pictureSize)
	return nil
}

// transcodes measures the server's ffmpegs, the children it runs to
// transcode, and how fast its transcodes go, as the admin says.
type transcodes struct {
	seen   int                // ffmpegs seen
	pids   map[int]procSample // each running ffmpeg's last sample
	peakKB int64              // the most resident memory of one ffmpeg
	most   int                // the most ffmpegs at once
	ticks  int64              // their CPU time, in clock ticks, between samples
	busy   float64            // seconds that they ran, summed over them
	speed  []float64          // each transcode's speed, as the admin gave it at each sample
	behind int                // samples in which a transcode fell behind
	errs   int                // failed reads of the admin's streams
}

type procSample struct {
	at   time.Time
	tick int64
}

// sample reads the server's ffmpegs from /proc.
func (t *transcodes) sample(server int, at time.Time) {
	if t.pids == nil {
		t.pids = map[int]procSample{}
	}
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	running := map[int]bool{}
	for _, d := range dirs {
		pid, err := strconv.Atoi(filepath.Base(d))
		if err != nil {
			continue
		}
		stat, err := os.ReadFile(d + "/stat")
		if err != nil {
			continue
		}
		open, close := bytes.IndexByte(stat, '('), bytes.LastIndexByte(stat, ')')
		if open < 0 || close < open || string(stat[open+1:close]) != "ffmpeg" {
			continue
		}
		f := strings.Fields(string(stat[close+2:]))
		if len(f) < 13 {
			continue
		}
		if ppid, _ := strconv.Atoi(f[1]); ppid != server {
			continue
		}
		utime, _ := strconv.ParseInt(f[11], 10, 64)
		stime, _ := strconv.ParseInt(f[12], 10, 64)
		tick := utime + stime
		running[pid] = true
		if last, ok := t.pids[pid]; ok {
			t.ticks += tick - last.tick
			t.busy += at.Sub(last.at).Seconds()
		} else {
			t.seen++
		}
		t.pids[pid] = procSample{at: at, tick: tick}
		if status, err := os.ReadFile(d + "/status"); err == nil {
			for line := range strings.Lines(string(status)) {
				if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
					kb, _ := strconv.ParseInt(strings.Fields(v)[0], 10, 64)
					t.peakKB = max(t.peakKB, kb)
				}
			}
		}
	}
	for pid := range t.pids {
		if !running[pid] {
			delete(t.pids, pid)
		}
	}
	t.most = max(t.most, len(running))
}

// speeds reads how fast the server's transcodes go, from the admin's list
// of streams.
func (t *transcodes) speeds(s *server) {
	var list struct {
		Streams []struct {
			Profile string  `json:"profile"`
			Speed   float64 `json:"speed"`
			Behind  bool    `json:"behind"`
		} `json:"streams"`
	}
	if s.admin == nil || s.admin.call("GET", "streams", nil, &list) != nil {
		t.errs++
		return
	}
	for _, st := range list.Streams {
		if st.Profile == "" {
			continue
		}
		if st.Speed > 0 {
			t.speed = append(t.speed, st.Speed)
		}
		if st.Behind {
			t.behind++
		}
	}
}

// report prints what the transcodes took.
func (t *transcodes) report(o options) {
	cpu := 0.0
	if t.busy > 0 {
		cpu = float64(t.ticks) / clockTicks / t.busy * 100
	}
	fmt.Printf("Transcodes with %s from %s: %d ffmpegs, at most %d at once; one took %.1f MB at the most and %.0f%% of one core on average\n",
		o.transcode, o.pictureSize, t.seen, t.most, mb(t.peakKB), cpu)
	if len(t.speed) > 0 {
		sorted := slices.Sorted(slices.Values(t.speed))
		sum := 0.0
		for _, v := range sorted {
			sum += v
		}
		fmt.Printf("Their speed, as the admin gave it: %.2fx on average, %.2fx at the least, in %d readings; behind in %d\n",
			sum/float64(len(sorted)), sorted[0], len(sorted), t.behind)
	} else {
		fmt.Printf("Their speed: the admin gave none, in %d readings (%d failed)\n", len(t.speed), t.errs)
	}
}
