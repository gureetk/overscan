// SPDX-License-Identifier: AGPL-3.0-or-later

package playcheck

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
)

// short returns some of the check's clips, a second long, to make quickly.
func short(ids ...string) []media.CheckClip {
	var out []media.CheckClip
	for _, id := range ids {
		c, ok := media.CheckClipByID(id)
		if !ok {
			panic("no clip " + id)
		}
		c.Seconds = 1
		out = append(out, c)
	}
	return out
}

// fakeFFmpeg writes a program that lists encoders as ffmpeg does, with the
// legend first, and fails at anything else; or, given real, runs the real
// ffmpeg for that.
func fakeFFmpeg(t *testing.T, encoders []string, real string) string {
	t.Helper()
	var list strings.Builder
	list.WriteString("Encoders:\n V..... = Video\n A..... = Audio\n ...X.. = Codec is experimental\n ------\n")
	for _, e := range encoders {
		list.WriteString(" V....D " + e + "              some encoder\n")
	}
	other := "echo 'no such thing' >&2; exit 1"
	if real != "" {
		other = "exec " + real + ` "$@"`
	}
	script := "#!/bin/sh\nif [ \"$2\" = \"-encoders\" ]; then cat <<'EOF'\n" + list.String() + "EOF\nexit 0\nfi\n" + other + "\n"
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// wait prepares c until it is done.
func wait(t *testing.T, c *Clips) Progress {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		p := c.Prepare()
		if p.Done {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("the clips are not done: %+v", p)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestMakeClips(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg:", err)
	}
	dir := t.TempDir()
	for _, old := range []string{"base-00000000.mkv", "dts-12345678.mkv.part", "notes.txt"} {
		if err := os.WriteFile(filepath.Join(dir, old), []byte("old"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	list := short("base", "dts", "mp4", "vob")
	c := New(context.Background(), dir, ffmpeg, quiet())
	c.list = list
	if p := c.Prepare(); p.Done || p.Total != 4 {
		t.Errorf("at first: %+v", p)
	}
	p := wait(t, c)
	if p.Err != nil || p.Ready != 4 || p.Total != 4 {
		t.Fatalf("done: %+v", p)
	}
	var ids []string
	for _, clip := range c.Made() {
		ids = append(ids, clip.ID)
		f, _, err := c.Open(clip.ID)
		if err != nil {
			t.Fatal(err)
		}
		head := make([]byte, 4)
		if _, err := io.ReadFull(f, head); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
	if !slices.Equal(ids, []string{"base", "dts", "mp4", "vob"}) {
		t.Errorf("made %v", ids)
	}
	names, _ := filepath.Glob(filepath.Join(dir, "*"))
	for _, n := range names {
		base := filepath.Base(n)
		if strings.HasPrefix(base, "base-00000000") || strings.HasSuffix(base, ".part") {
			t.Errorf("old file %s left", base)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("another file went: %v", err)
	}
	if _, _, err := c.Open("nope"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Open(nope) = %v", err)
	}

	// Another server finds them made, without making them again.
	again := New(context.Background(), dir, fakeFFmpeg(t, []string{"libx264", "aac", "dca", "ac3", "mpeg2video"}, ""), quiet())
	again.list = list
	if p := wait(t, again); p.Ready != 4 || p.Err != nil {
		t.Errorf("found: %+v", p)
	}

	// A file that has gone is made again.
	f, _, _ := c.Open("dts")
	path := f.Name()
	f.Close()
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Open("dts"); err == nil {
		t.Fatal("opened a file that has gone")
	}
	if p := wait(t, c); p.Ready != 4 {
		t.Errorf("made again: %+v", p)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("not made again: %v", err)
	}
}

func TestEncodersLeaveClipsOut(t *testing.T) {
	// No DTS encoder, and nothing can be made: the check is unavailable,
	// and leaves out the clip that it could not make anyway.
	var logs bytes.Buffer
	c := New(context.Background(), t.TempDir(), fakeFFmpeg(t, []string{"libx264", "aac"}, ""),
		slog.New(slog.NewTextHandler(&logs, nil)))
	c.list = short("base", "dts")
	p := wait(t, c)
	if !errors.Is(p.Err, ErrUnavailable) || p.Total != 1 || p.Ready != 0 {
		t.Errorf("progress %+v", p)
	}
	if len(c.Made()) != 0 {
		t.Errorf("made %v", c.Made())
	}
	for _, want := range []string{"ffmpeg lacks an encoder", "clip=dts", "cannot make a clip", "no such thing"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, logs.String())
		}
	}
	// It is not tried again at once.
	if p := c.Prepare(); !p.Done {
		t.Errorf("tried again at once: %+v", p)
	}
	c.mu.Lock()
	c.triedAt = time.Now().Add(-retryAfter)
	c.mu.Unlock()
	if p := c.Prepare(); p.Done {
		t.Errorf("not tried again later: %+v", p)
	}
	wait(t, c)
}

func TestNoFFmpeg(t *testing.T) {
	c := New(context.Background(), t.TempDir(), filepath.Join(t.TempDir(), "ffmpeg"), quiet())
	if p := wait(t, c); !errors.Is(p.Err, ErrUnavailable) {
		t.Errorf("progress %+v", p)
	}
}

func TestIsClipFile(t *testing.T) {
	for name, want := range map[string]bool{
		"base-1234abcd.mkv": true, "vob-00ff00ff.vob.part": true, "ts-1.ts": true,
		"base.mkv": false, "notes.txt": false, "film-1.srt": false,
	} {
		if got := isClipFile(name); got != want {
			t.Errorf("isClipFile(%q) = %v", name, got)
		}
	}
}
