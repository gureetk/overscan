// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// M10's Done when (the plan, section 14), walked end to end with this
// tool's fictional provider, whose channels send clips that ffmpeg makes,
// a box, a Stalker client, an account's playlist and its Xtream login, on
// a server without graphics: a household that sets nothing new gets every
// stream as before; a channel set always with a profile reaches a device
// that takes channels as their sources send them in the profile's codec
// and size, while a device whose own profile transcodes keeps it, and a
// channel set never reaches every device as its source sends it; a
// channel's setting wins over its group's, and a group's over live TV's,
// in every output; the server says that it has no graphics and transcodes
// in software; and no transcode takes another connection to its source or
// goes past its number at a time.
//
// The rest is walked elsewhere: films and series set always or never in
// the hub's tests (TestFilmAlways, TestFilmNever, and on the graphics
// TestFilmOnGraphics and TestFilmGraphicsFallBack); each profile's
// arguments, in software and with VA-API, in internal/streamprofile's and
// internal/media's tests; every fallback in TestGraphicsFallBack; the
// packaging's choice in CI's package and image checks; memory in the soak
// (docs/testing.md); what real graphics do on a server with Intel graphics
// before each release; and that an upgrade changes nothing in the rest of
// this tool's work, with the household's transcoding (setUpTranscoding).
func TestTranscodingDoneWhen(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg to make the clips with")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("no ffprobe to read what is sent")
	}
	w := startWalk(t)
	p, base, a := w.p, w.base, w.a
	call := func(method, path string, body, out any) {
		t.Helper()
		w.call(t, method, path, body, out)
	}
	clip := makeClip(t, ffmpeg)
	p.usePictures(map[string][]byte{"hn": clip, "gn": clip})
	household := entriesOf(t, a, "")
	hn, gn := household["Harbour News"].ID, household["Granite News"].ID
	var groups struct {
		Groups []idName `json:"groups"`
	}
	call("GET", "lineup/groups", nil, &groups)
	var news int64
	for _, g := range groups.Groups {
		if g.Name == "News" {
			news = g.ID
		}
	}
	if news == 0 {
		t.Fatalf("no group News: %+v", groups)
	}

	// The household's account takes channels as their sources send them,
	// through its playlist, its Xtream login, a box and a Stalker client;
	// Guests' account has a profile of its own.
	call("PATCH", "settings", map[string]any{"outputs": true, "transcode_video": 1}, nil)
	call("PATCH", "accounts/1", map[string]any{"outputs": map[string]bool{"m3u": true, "xtream": true}}, nil)
	call("PUT", "accounts/1/login", map[string]any{"username": guestUser, "password": guestPassword}, nil)
	var token struct {
		Token string `json:"token"`
	}
	call("POST", "accounts/1/token", nil, &token)
	var guests idName
	call("POST", "accounts", map[string]any{"name": "Guests"}, &guests)
	call("PATCH", fmt.Sprintf("accounts/%d", guests.ID), map[string]any{"output_profile": "h264-720p", "outputs": map[string]bool{"m3u": true}}, nil)
	var guestToken struct {
		Token string `json:"token"`
	}
	call("POST", fmt.Sprintf("accounts/%d/token", guests.ID), nil, &guestToken)
	b := newBox(base, boxMAC)
	s, err := b.session()
	if err != nil {
		t.Fatal(err)
	}
	code, _ := s["code"].(string)
	call("POST", "pairings/"+code+"/approve", map[string]any{"label": "Den"}, nil)
	if _, err := b.session(); err != nil || b.token == "" {
		t.Fatalf("the box: %v", err)
	}
	client := newStalker(base, clientMAC)
	_ = client.handshake()
	call("POST", "devices/"+url.PathEscape(clientMAC)+"/approve", map[string]any{"label": "Tablet"}, nil)
	if err := client.handshake(); err != nil {
		t.Fatal(err)
	}

	// Where each output plays a channel.
	playlistLink := func(token string, entryID int64) string {
		t.Helper()
		var playlist []byte
		if err := get(base, "/playlist/"+token+".m3u", &playlist); err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(playlist), "\n")
		for i, line := range lines {
			if strings.Contains(line, fmt.Sprintf(`tvg-id="%d"`, entryID)) && i+1 < len(lines) {
				return strings.TrimSpace(lines[i+1])
			}
		}
		t.Fatalf("the playlist has no channel %d", entryID)
		return ""
	}
	outputs := map[string]func(int64) string{
		"playlist": func(id int64) string { return playlistLink(token.Token, id) },
		"xtream": func(id int64) string {
			return fmt.Sprintf("%s/live/%s/%s/%d.ts", base, guestUser, url.PathEscape(guestPassword), id)
		},
		"box": func(id int64) string {
			var pl struct {
				URL string `json:"url"`
			}
			if err := b.call("POST", fmt.Sprintf("channels/%d/play", id), nil, &pl); err != nil {
				t.Fatal(err)
			}
			return pl.URL
		},
		"stalker": func(id int64) string {
			var l struct{ Cmd, Error string }
			if err := client.call("itv", "create_link", url.Values{"cmd": {fmt.Sprintf("ffrt http://localhost/ch/%d", id)}}, &l); err != nil || l.Error != "" {
				t.Fatalf("create_link: %v %q", err, l.Error)
			}
			f := strings.Fields(l.Cmd)
			return f[len(f)-1]
		},
	}
	asSent, small, hd := "h264 1920x1080", "h264 1024x576", "h264 1280x720"

	// A household that sets nothing new gets every stream as before, and
	// each play takes one connection to the source.
	for name, link := range outputs {
		before := len(p.asked())
		if got := videoOf(t, link(hn)); got != asSent {
			t.Errorf("nothing set, %s: %s, want %s", name, got, asSent)
		}
		if n := len(p.asked()) - before; n != 1 {
			t.Errorf("nothing set, %s: %d connections to the source, want 1", name, n)
		}
	}
	waitLog(t, w.log, "no graphics for ffmpeg to transcode on, so it transcodes in software")
	var graphics struct {
		Usable bool `json:"usable"`
	}
	call("GET", "graphics", nil, &graphics)
	if graphics.Usable {
		t.Error("the API says the graphics are usable on a server without them")
	}

	// A channel always with a profile reaches a device that takes channels
	// as their sources send them in its codec and size, through one
	// connection; a device whose own profile transcodes keeps its own.
	call("POST", "transcode-profiles", map[string]any{"name": "small", "max_lines": 576, "quality": "low"}, nil)
	call("PATCH", fmt.Sprintf("lineup/channels/%d", hn), map[string]any{"transcode": map[string]any{"mode": "always", "profile": "small"}}, nil)
	before := len(p.asked())
	if got := videoOf(t, outputs["playlist"](hn)); got != small {
		t.Errorf("always: %s, want %s", got, small)
	}
	if n := len(p.asked()) - before; n != 1 {
		t.Errorf("always: %d connections to the source, want 1", n)
	}
	waitLog(t, w.log, "transcoding a stream with ffmpeg", "profile=small set_by=channel")
	if got := videoOf(t, playlistLink(guestToken.Token, hn)); got != hd {
		t.Errorf("always, with a profile of its own: %s, want %s", got, hd)
	}

	// Never reaches every device as its source sends it, the one with a
	// profile of its own too.
	call("PATCH", fmt.Sprintf("lineup/channels/%d", hn), map[string]any{"transcode": map[string]any{"mode": "never"}}, nil)
	for _, link := range []string{outputs["playlist"](hn), playlistLink(guestToken.Token, hn)} {
		if got := videoOf(t, link); got != asSent {
			t.Errorf("never: %s, want %s", got, asSent)
		}
	}

	// A channel's setting wins over its group's, and a group's over live
	// TV's, in every output.
	call("PATCH", "settings", map[string]any{"live_transcode": map[string]any{"mode": "always", "profile": "small"}}, nil)
	call("PATCH", fmt.Sprintf("lineup/groups/%d", news), map[string]any{"transcode": map[string]any{"mode": "never"}}, nil)
	call("PATCH", fmt.Sprintf("lineup/channels/%d", hn), map[string]any{"transcode": map[string]any{"mode": "as_needed"}}, nil)
	for name, link := range outputs {
		if got := videoOf(t, link(hn)); got != asSent {
			t.Errorf("the group's never over live TV's always, %s: %s, want %s", name, got, asSent)
		}
	}
	call("PATCH", fmt.Sprintf("lineup/channels/%d", hn), map[string]any{"transcode": map[string]any{"mode": "always", "profile": "small"}}, nil)
	for name, link := range outputs {
		if got := videoOf(t, link(hn)); got != small {
			t.Errorf("the channel's always over its group's never, %s: %s, want %s", name, got, small)
		}
	}

	// No transcode goes past its number at a time: with one, a second
	// channel set always plays as its source sends it while the box plays
	// the first.
	call("PATCH", fmt.Sprintf("lineup/channels/%d", gn), map[string]any{"transcode": map[string]any{"mode": "always", "profile": "small"}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := openStream(ctx, outputs["box"](hn))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(io.Discard, first, 64<<10); err != nil {
		t.Fatal(err)
	}
	if got := videoOf(t, outputs["playlist"](gn)); got != asSent {
		t.Errorf("a second transcode with one at a time: %s, want %s", got, asSent)
	}
	cancel()
	first.Close()
	waitLog(t, w.log, "this one plays as its source sends it", "most=1")
}

// makeClip has ffmpeg make 10 seconds of fictional 1080p pictures in H.264,
// with AAC, in MPEG-TS, for the provider to send again and again. The hub
// leaves out what a source sends again, so a transcode gets the clip once:
// long enough for ffmpeg to read what it holds, and to make enough to read.
func makeClip(t *testing.T, ffmpeg string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "clip.ts")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=25:duration=10",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=10",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "30", "-g", "25", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-ac", "2", "-f", "mpegts", path)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("making the clip: %v: %s", err, stderr.String())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// openStream asks for a stream, and returns its body.
func openStream(ctx context.Context, address string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", req.URL.Path, resp.Status)
	}
	return resp.Body, nil
}

// videoOf reads the first part of a stream, up to 256 KiB or for 20
// seconds, and says what its video is, as ffprobe reads it, such as
// "h264 1920x1080".
func videoOf(t *testing.T, address string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	body, err := openStream(ctx, address)
	if err != nil {
		t.Fatal(err)
	}
	var head bytes.Buffer
	_, err = io.CopyN(&head, body, 256<<10)
	cancel()
	body.Close()
	if err != nil && head.Len() < 32<<10 {
		t.Fatalf("reading %s: %v, after %d bytes", address, err, head.Len())
	}
	path := filepath.Join(t.TempDir(), "head.ts")
	if err := os.WriteFile(path, head.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height",
		"-of", "csv=p=0", path).Output()
	if err != nil {
		t.Fatalf("ffprobe: %v", err)
	}
	first, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	f := strings.Split(strings.TrimSpace(first), ",")
	if len(f) != 3 {
		return first
	}
	return f[0] + " " + f[1] + "x" + f[2]
}

// waitLog waits up to ten seconds for a line of the server's log that
// holds every one of parts.
func waitLog(t *testing.T, path string, parts ...string) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); ; {
		b, _ := os.ReadFile(path)
		for line := range strings.Lines(string(b)) {
			found := true
			for _, part := range parts {
				found = found && strings.Contains(line, part)
			}
			if found {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Errorf("the log has no line with %q", parts)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}
