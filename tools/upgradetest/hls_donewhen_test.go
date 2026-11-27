// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// M11's Done when (the plan, section 14), as far as its first phase goes,
// walked end to end with this tool's fictional provider, whose channel
// sends a clip that ffmpeg makes, through an account's Xtream login and its
// playlist: an account whose HLS for players is off gets every stream as
// before; with it on, a channel whose source sends MPEG-TS plays as HLS,
// from segments that start with the PAT and the PMT, that ffprobe reads as
// the channel's pictures; two players of the channel's HLS take one
// connection to the source while the shared tuner is off; no address shows
// the source; and turning it off puts everything back.
//
// The rest is walked elsewhere: the playlist's rules and the cutting in
// internal/hub's TestCutPlaylistRules and internal/hlscut's tests; ffmpeg
// playing the HLS as a player, and the sessions' end, in TestCutHLS; a
// player's channel change at a full source in TestCutHLSLimits; and an
// upgrade, with the guests' HLS on, in this tool's household.
func TestHLSDoneWhen(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to make the clip with")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffprobe to read what is sent")
	}
	w := startWalk(t)
	p, base := w.p, w.base
	call := func(method, path string, body, out any) {
		t.Helper()
		w.call(t, method, path, body, out)
	}
	p.usePictures(map[string][]byte{"hn": makeClip(t, ffmpeg)})
	hn := entriesOf(t, w.a, "")["Harbour News"].ID

	call("PATCH", "settings", map[string]any{"outputs": true}, nil)
	call("PATCH", "accounts/1", map[string]any{"outputs": map[string]bool{"m3u": true, "xtream": true}}, nil)
	call("PUT", "accounts/1/login", map[string]any{"username": guestUser, "password": guestPassword}, nil)
	var token struct {
		Token string `json:"token"`
	}
	call("POST", "accounts/1/token", nil, &token)
	login := "username=" + guestUser + "&password=" + url.QueryEscape(guestPassword)
	xtream := fmt.Sprintf("%s/live/%s/%s/%d", base, guestUser, url.PathEscape(guestPassword), hn)
	fetch := func(agent, address string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest("GET", address, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("User-Agent", agent)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, b
	}
	formats := func() []string {
		var info struct {
			User struct {
				Formats []string `json:"allowed_output_formats"`
			} `json:"user_info"`
		}
		if err := get(base, "/player_api.php?"+login, &info); err != nil {
			t.Fatal(err)
		}
		return info.User.Formats
	}
	playlist := func(path string) string {
		t.Helper()
		var text []byte
		if err := get(base, path, &text); err != nil {
			t.Fatal(err)
		}
		return string(text)
	}

	// Off: as before.
	if f := formats(); strings.Join(f, ",") != "ts" {
		t.Errorf("formats while off: %v", f)
	}
	if resp, _ := fetch("VLC/3.0.21 LibVLC/3.0.21", xtream+".m3u8"); resp.StatusCode != http.StatusNotFound {
		t.Errorf(".m3u8 while off: %s", resp.Status)
	}
	if text := playlist("/get.php?" + login + "&type=m3u_plus&output=m3u8"); strings.Contains(text, ".m3u8") {
		t.Errorf("get.php while off gives HLS:\n%s", text)
	}
	if got := videoOf(t, xtream+".ts"); got != "h264 1920x1080" {
		t.Errorf(".ts while off: %s", got)
	}

	// On: HLS of the channel's one stream, for two players.
	call("PATCH", "accounts/1", map[string]any{"outputs": map[string]bool{"m3u": true, "xtream": true, "hls": true}}, nil)
	if f := formats(); strings.Join(f, ",") != "ts,m3u8" {
		t.Errorf("formats while on: %v", f)
	}
	if text := playlist("/get.php?" + login + "&type=m3u_plus&output=m3u8"); !strings.Contains(text, fmt.Sprintf("/%d.m3u8\n", hn)) {
		t.Errorf("get.php with output=m3u8:\n%s", text)
	}
	if text := playlist("/playlist/" + token.Token + ".m3u?output=m3u8"); !strings.Contains(text, fmt.Sprintf("/%d.m3u8\n", hn)) {
		t.Errorf("the playlist with output=m3u8:\n%s", text)
	}
	before := len(p.asked())
	for i, agent := range []string{"VLC/3.0.21 LibVLC/3.0.21", "TiviMate/5.1.6"} {
		resp, master := fetch(agent, xtream+".m3u8")
		lines := strings.Split(strings.TrimSpace(string(master)), "\n")
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/vnd.apple.mpegurl" ||
			len(lines) != 4 || !strings.HasPrefix(lines[2], "#EXT-X-STREAM-INF:BANDWIDTH=") {
			t.Fatalf("player %d's playlist of renditions: %s %q", i, resp.Status, master)
		}
		rendition := lines[3]
		if strings.Contains(rendition, p.url) || strings.Contains(rendition, guestPassword) {
			t.Errorf("the rendition's address shows the source or the login: %s", rendition)
		}
		// The clip's keyframes come about every second, so segments end
		// at the first after 4 s.
		resp, media := fetch(agent, base+rendition)
		target := 0
		for _, l := range strings.Split(string(media), "\n") {
			if v, ok := strings.CutPrefix(l, "#EXT-X-TARGETDURATION:"); ok {
				target, _ = strconv.Atoi(v)
			}
		}
		if resp.StatusCode != http.StatusOK || target < 4 || target > 5 || !strings.Contains(string(media), "\n0.ts\n") {
			t.Fatalf("player %d's media playlist: %s %q", i, resp.Status, media)
		}
		resp, seg := fetch(agent, base+strings.TrimSuffix(rendition, "hls.m3u8")+"0.ts")
		if resp.StatusCode != http.StatusOK || len(seg) < 3*188 || seg[0] != 0x47 || seg[1]&0x1f != 0 || seg[2] != 0 {
			t.Fatalf("player %d's first segment: %s, %d bytes", i, resp.Status, len(seg))
		}
		path := filepath.Join(t.TempDir(), "0.ts")
		if err := os.WriteFile(path, seg, 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries",
			"stream=codec_name,width,height", "-of", "csv=p=0", path).Output()
		if first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n"); err != nil || first != "h264,1920,1080" {
			t.Errorf("player %d's first segment holds %q, %v", i, out, err)
		}
	}
	if n := len(p.asked()) - before; n != 1 {
		t.Errorf("two players of the channel's HLS: %d connections to the source, want 1", n)
	}
	waitLog(t, w.log, "a channel is cut into HLS segments for players, from its shared tuner", "channel=\"Harbour News\"")
	waitLog(t, w.log, "a player plays a channel as HLS that the hub cuts from its shared tuner")
	var streams struct {
		Streams []struct {
			HLSOut bool `json:"hls_out"`
		} `json:"streams"`
	}
	call("GET", "streams", nil, &streams)
	// The .ts played while off waits a while for its player to come back.
	cut := 0
	for _, s := range streams.Streams {
		if s.HLSOut {
			cut++
		}
	}
	if cut != 2 {
		t.Errorf("the streams: %+v", streams)
	}

	// Off again: the players' HLS ends, and .m3u8 is as before.
	call("PATCH", "accounts/1", map[string]any{"outputs": map[string]bool{"m3u": true, "xtream": true, "hls": false}}, nil)
	waitLog(t, w.log, "the account's HLS for players was turned off")
	if resp, _ := fetch("VLC/3.0.21 LibVLC/3.0.21", xtream+".m3u8"); resp.StatusCode != http.StatusNotFound {
		t.Errorf(".m3u8 once off again: %s", resp.Status)
	}
	if f := formats(); strings.Join(f, ",") != "ts" {
		t.Errorf("formats once off again: %v", f)
	}
}
