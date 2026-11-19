// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/playcheck"
	"overscan.gureet.ca/internal/store"
)

// clipMaker writes a program that stands in for ffmpeg: it has every
// encoder the check's clips use, and makes each clip as a file that names
// itself.
func clipMaker(t *testing.T) string {
	t.Helper()
	var list strings.Builder
	list.WriteString("Encoders:\n V..... = Video\n ------\n")
	for _, c := range media.CheckClips {
		for _, enc := range append(slices.Clone(c.Video), c.Audio...) {
			list.WriteString(" V....D " + enc.Name + "    an encoder\n")
		}
	}
	script := "#!/bin/sh\nif [ \"$2\" = \"-encoders\" ]; then cat <<'EOF'\n" + list.String() +
		"EOF\nexit 0\nfi\nfor last; do :; done\nprintf 'CLIP %s' \"$(basename \"$last\")\" > \"$last\"\n"
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// readyCheck asks for the check until its clips are made.
func (h *harness) readyCheck(token string) checkResponse {
	h.t.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		var c checkResponse
		if code := h.call("GET", Prefix+"check", token, "", &c); code != http.StatusOK {
			h.t.Fatalf("GET check: %d", code)
		}
		if c.State != "preparing" {
			return c
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("the clips are not made: %+v", c)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// results returns the body of PUT check for answers by clip ID: yes, no,
// "" for a clip that started and was not answered, and "-" for one that
// did not start.
func results(firmware string, answers map[string]string) string {
	req := checkRequest{Firmware: firmware}
	for _, c := range media.CheckClips {
		a, ok := answers[c.ID]
		if !ok {
			continue
		}
		r := checkResult{ID: c.ID, Answer: a, Started: true, Ms: 700, Width: c.Width, Height: c.Height, Tracks: 1, Position: 3.5,
			Video: "{frameRate:25000,pictureWidth:640,pictureHeight:360}", Audio: `[{pid:257,lang:["eng"],type:4}]`,
			Events: `"2" "7" "4"`}
		if a == "-" {
			r = checkResult{ID: c.ID, Ms: 15000}
		}
		req.Results = append(req.Results, r)
	}
	b, _ := json.Marshal(req)
	return string(b)
}

func TestCheckUnavailable(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	var c checkResponse
	if code := h.call("GET", Prefix+"check", token, "", &c); code != http.StatusOK || c.State != "unavailable" ||
		c.Total != 0 || c.Checked != 0 || c.Clips != nil {
		t.Errorf("GET check without clips: %d, %+v", code, c)
	}
	if code, e := h.errorCode("GET", Prefix+"check", ""); code != http.StatusUnauthorized || e != errUnauthorized {
		t.Errorf("GET check without a session: %d %s", code, e)
	}
	token = h.api.signClip(1, h.now.Add(time.Hour).Unix())
	if code, e := h.errorCode("GET", Prefix+"check/"+token+"/base.mkv", ""); code != http.StatusForbidden || e != errUnauthorized {
		t.Errorf("a clip without clips: %d %s", code, e)
	}
}

func TestCheck(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	clips := playcheck.New(context.Background(), t.TempDir(), clipMaker(t), slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.api.UseChecks(clips)

	c := h.readyCheck(token)
	if c.State != "ready" || c.Ready != len(media.CheckClips) || c.Total != c.Ready || len(c.Clips) != c.Ready || c.Checked != 0 {
		t.Fatalf("check %+v", c)
	}
	base := c.Clips[0]
	if base.ID != "base" || base.Label != "MKV with H.264 and AAC" || base.Ask != media.AskBoth || base.Solution != "ffmpeg" ||
		base.Width != 640 || base.Height != 360 || !strings.HasPrefix(base.URL, h.front.URL+Prefix+"check/") ||
		!strings.HasSuffix(base.URL, "/base.mkv") {
		t.Errorf("first clip %+v", base)
	}
	if last := c.Clips[len(c.Clips)-1]; last.ID != "vob" || !strings.HasSuffix(last.URL, "/vob.vob") || last.Ask != media.AskBoth {
		t.Errorf("last clip %+v", last)
	}

	// The box's player gets the clips, with ranges.
	get := func(url, ranges string) (int, string, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", url, nil)
		if ranges != "" {
			req.Header.Set("Range", ranges)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, resp.Header.Get("Content-Type"), string(body)
	}
	if code, ct, body := get(base.URL, ""); code != http.StatusOK || ct != "video/x-matroska" || !strings.HasPrefix(body, "CLIP base-") {
		t.Errorf("first clip: %d %s %q", code, ct, body)
	}
	if code, _, body := get(base.URL, "bytes=0-3"); code != http.StatusPartialContent || body != "CLIP" {
		t.Errorf("a range: %d %q", code, body)
	}
	if code, ct, _ := get(strings.TrimSuffix(base.URL, "base.mkv")+"ts.ts", ""); code != http.StatusOK || ct != "video/mp2t" {
		t.Errorf("MPEG-TS clip: %d %s", code, ct)
	}
	dir := strings.TrimSuffix(base.URL, "base.mkv")
	for path, want := range map[string]int{
		dir + "base.mp4": http.StatusNotFound,
		dir + "nope.mkv": http.StatusNotFound,
		dir + "base":     http.StatusNotFound,
		h.front.URL + Prefix + "check/" + h.api.signClip(1, h.now.Unix()) + "/base.mkv":                      http.StatusForbidden, // expired
		h.front.URL + Prefix + "check/x" + strings.TrimPrefix(dir, h.front.URL+Prefix+"check/") + "base.mkv": http.StatusForbidden,
	} {
		if code, _, _ := get(path, ""); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	h.now = h.now.Add(clipTTL)
	if code, _, _ := get(base.URL, ""); code != http.StatusForbidden {
		t.Errorf("a link past its time: %d", code)
	}
	token = h.session(macA).Token

	// What the check found is kept, and said back in the check's order.
	answers := map[string]string{"base": "yes", "dts": "no", "eac3": "yes", "opus": "", "truehd": "-",
		"hevc": "yes", "hevc-10bit": "no", "vp9": "-", "mpeg4": "no", "mp3": "yes", "avi": "no"}
	body := results("2.20.16-322", answers)
	// Room for the answers of every clip, and more than other calls take.
	body = strings.Replace(body, `"results":`, `"results":`+strings.Repeat(" ", 5000), 1)
	var saved checkSaved
	if code := h.call("PUT", Prefix+"check", token, body, &saved); code != http.StatusOK {
		t.Fatalf("PUT check: %d", code)
	}
	var labels []string
	for _, f := range saved.Findings {
		labels = append(labels, fmt.Sprintf("%s %v", f.Label, f.Plays))
	}
	want := []string{"MKV true", "H.264 true", "AAC true", "Dolby Digital Plus true", "DTS false", "Dolby TrueHD false",
		"MP3 true", "H.265 true", "H.265 at 10 bits false", "MPEG-4 Part 2 (Xvid) false", "VP9 false"}
	if saved.Checked != h.now.Unix() || !slices.Equal(labels, want) {
		t.Errorf("saved %d, %v; want %v", saved.Checked, labels, want)
	}
	d, _ := h.store.Device(context.Background(), macA)
	kept, err := h.store.PlaybackCheck(context.Background(), d.ID)
	if err != nil || kept.Model != "MAG322" || kept.Firmware != "2.20.16-322" || kept.Findings["audio:eac3"] != true ||
		!strings.Contains(string(kept.Results), `"id":"truehd","answer":"","started":false`) ||
		!strings.Contains(string(kept.Results), `"audio":"[{pid:257,lang:[\"eng\"],type:4}]"`) {
		t.Errorf("kept %+v, %v", kept, err)
	}
	if !strings.Contains(h.logs.String(), "a box's playback check found what it plays") ||
		!strings.Contains(h.logs.String(), `does_not_play="DTS, Dolby TrueHD, H.265 at 10 bits, MPEG-4 Part 2 (Xvid), VP9"`) {
		t.Errorf("log: %s", h.logs.String())
	}
	if c := h.readyCheck(token); c.Checked != h.now.Unix() {
		t.Errorf("checked %d", c.Checked)
	}

	// What the page could not have sent is refused, and keeps nothing.
	for name, body := range map[string]string{
		"not JSON":                "{",
		"an unknown field":        `{"results":[],"extra":1}`,
		"no first clip":           results("", map[string]string{"dts": "yes"}),
		"a first clip that fails": results("", map[string]string{"base": "no"}),
		"a clip twice":            strings.Replace(results("", map[string]string{"base": "yes", "dts": "no"}), `"id":"dts"`, `"id":"base"`, 1),
		"an unknown clip":         strings.Replace(results("", map[string]string{"base": "yes", "dts": "no"}), `"id":"dts"`, `"id":"wav"`, 1),
		"an answer of neither":    results("", map[string]string{"base": "yes", "dts": "maybe"}),
		"a position too far":      strings.Replace(results("", map[string]string{"base": "yes"}), `"position":3.5`, `"position":1e9`, 1),
		"a firmware not printed":  results("2.20\u0007", map[string]string{"base": "yes"}),
		"a firmware too long":     results(strings.Repeat("9", maxFirmwareLen+1), map[string]string{"base": "yes"}),
		"the player said too much": strings.Replace(results("", map[string]string{"base": "yes"}), `"video":"{`,
			`"video":"`+strings.Repeat("x", maxPlayerInfo)+`{`, 1),
		"the player said it in control characters": strings.Replace(results("", map[string]string{"base": "yes"}), `"video":"{`,
			`"video":"\u0000{`, 1),
		"too big": strings.Replace(results("", map[string]string{"base": "yes"}), `"results":`, `"results":`+strings.Repeat(" ", maxCheckBytes), 1),
	} {
		var e struct{ Error string }
		if code := h.call("PUT", Prefix+"check", token, body, &e); code != http.StatusBadRequest || e.Error != errBadRequest {
			t.Errorf("%s: %d %s", name, code, e.Error)
		}
	}
	if kept2, _ := h.store.PlaybackCheck(context.Background(), d.ID); kept2.Firmware != "2.20.16-322" {
		t.Errorf("a refused check changed what is kept: %+v", kept2)
	}
}

func TestProfileOfABox(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.approve(macA)
	h.approve(macB)
	a, _ := h.store.Device(ctx, macA)
	b, _ := h.store.Device(ctx, macB)
	a.Model, b.Model = "MAG322", "MAG322"
	if p := h.api.profile(ctx, a); p.Name != media.MAG322.Name {
		t.Errorf("a box never checked: %s", p.Name)
	}
	at := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	if err := h.store.SetPlaybackCheck(ctx, store.PlaybackCheck{DeviceID: a.ID, Model: "MAG322",
		Findings: map[string]bool{"audio:dts": true, "audio:opus": false}, CheckedAt: at}); err != nil {
		t.Fatal(err)
	}
	p := h.api.profile(ctx, a)
	if p.Name != "MAG322 (checked on 18 November 2026)" || !p.PlaysAudio("dts") || p.PlaysAudio("opus") {
		t.Errorf("a checked box: %s %v", p.Name, p.Audio)
	}
	p = h.api.profile(ctx, b)
	if p.Name != "MAG322 (as another box of the model was checked on 18 November 2026)" || !p.PlaysAudio("dts") {
		t.Errorf("a box of the same model: %s %v", p.Name, p.Audio)
	}
	b.Model = "MAG424"
	if p := h.api.profile(ctx, b); p.Name != media.MAG322.Name {
		t.Errorf("a box of another model: %s", p.Name)
	}
}
