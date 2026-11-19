// SPDX-License-Identifier: AGPL-3.0-or-later

// Package playcheck makes the clips of a box's playback check (D70) with
// ffmpeg, once, and keeps them in a folder for boxes to play.
//
// The first box to ask for the check has the clips made, one after the
// other: about a minute's work, most of it the clip in 4K. They are kept
// under names that change with how ffmpeg makes them, so that a version of
// Overscan that makes them otherwise makes them again, and removes the old
// ones.
package playcheck

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/media"
)

const (
	// makeTimeout caps making one clip; the one in 4K takes longest.
	makeTimeout = 3 * time.Minute
	// encodersTimeout caps asking ffmpeg for its encoders.
	encodersTimeout = 10 * time.Second
	// maxStderr caps what is kept of ffmpeg's messages.
	maxStderr = 4 << 10
	// retryAfter is how long after a try that left clips unmade the next
	// box to ask has them made again.
	retryAfter = 5 * time.Minute
)

// ErrUnavailable says that the check cannot be made: ffmpeg cannot make
// its first clip, which every box must play.
var ErrUnavailable = errors.New("ffmpeg cannot make the playback check's clips")

// Clips makes the clips of the check and keeps them.
type Clips struct {
	dir    string
	ffmpeg string
	log    *slog.Logger
	ctx    context.Context // ends with the server
	list   []media.CheckClip

	mu      sync.Mutex
	making  bool
	tried   bool              // the clips have been made, or tried
	triedAt time.Time         // when the last try ended
	usable  []media.CheckClip // those that ffmpeg has encoders for, once known
	files   map[string]string // the clips made: ID to file name
}

// New returns the clips of the check, kept in dir and made with the ffmpeg
// program at ffmpeg. Making them stops when ctx ends.
func New(ctx context.Context, dir, ffmpeg string, log *slog.Logger) *Clips {
	return &Clips{dir: dir, ffmpeg: ffmpeg, log: log, ctx: ctx, list: media.CheckClips, files: map[string]string{}}
}

// Progress says how far making the clips got.
type Progress struct {
	// Ready of Total clips are made. Total counts those that ffmpeg has
	// encoders for, once that is known.
	Ready, Total int
	// Done says that every clip that can be made has been, or has failed.
	Done bool
	// Err is ErrUnavailable if the check cannot be made.
	Err error
}

// Prepare starts making the clips that are not made, unless that has
// started already, and says how far it got.
func (c *Clips) Prepare() Progress {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.making && (!c.tried || c.unmade() && time.Since(c.triedAt) >= retryAfter) {
		c.making = true
		go c.make()
	}
	total := len(c.list)
	if c.usable != nil {
		total = len(c.usable)
	}
	p := Progress{Ready: len(c.files), Total: total, Done: c.tried && !c.making}
	if p.Done && c.files[c.list[0].ID] == "" {
		p.Err = ErrUnavailable
	}
	return p
}

// unmade reports whether a clip that ffmpeg can make, or might, is not
// made. c.mu is held.
func (c *Clips) unmade() bool {
	return c.usable == nil || len(c.files) < len(c.usable)
}

// Made returns the clips that are made, in the check's order.
func (c *Clips) Made() []media.CheckClip {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []media.CheckClip
	for _, clip := range c.usable {
		if c.files[clip.ID] != "" {
			out = append(out, clip)
		}
	}
	return out
}

// Open opens the file of a clip that is made, and says when it was made.
// A file that has gone is made again at the next Prepare.
func (c *Clips) Open(id string) (*os.File, time.Time, error) {
	c.mu.Lock()
	name := c.files[id]
	c.mu.Unlock()
	if name == "" {
		return nil, time.Time{}, os.ErrNotExist
	}
	f, err := os.Open(filepath.Join(c.dir, name))
	if err == nil {
		var fi os.FileInfo
		if fi, err = f.Stat(); err == nil {
			return f, fi.ModTime(), nil
		}
		f.Close()
	}
	if errors.Is(err, os.ErrNotExist) {
		c.mu.Lock()
		if c.files[id] == name {
			delete(c.files, id)
			c.tried = false
		}
		c.mu.Unlock()
	}
	return nil, time.Time{}, err
}

// make makes the clips that are not made, one after the other.
func (c *Clips) make() {
	defer func() {
		c.mu.Lock()
		c.making, c.tried, c.triedAt = false, true, time.Now()
		c.mu.Unlock()
	}()
	start := time.Now()
	if err := os.MkdirAll(c.dir, 0o750); err != nil {
		c.log.Error("cannot make the folder of the playback check's clips", "dir", c.dir, "error", err)
		return
	}
	has, err := c.encoders()
	if err != nil {
		c.log.Error("cannot ask ffmpeg for its encoders, for the playback check", "error", err)
		return
	}

	// Which clips ffmpeg can make, and under what names.
	names := map[string]string{}
	var usable []media.CheckClip
	for _, clip := range c.list {
		args, ok := clip.Args(has, "OUT")
		if !ok {
			c.log.Info("ffmpeg lacks an encoder for a clip of the playback check, which leaves it out",
				"clip", clip.ID, "what", clip.Label)
			continue
		}
		sum := sha256.Sum256([]byte(strings.Join(args, "\x00")))
		names[clip.ID] = clip.ID + "-" + hex.EncodeToString(sum[:4]) + "." + clip.Ext
		usable = append(usable, clip)
	}
	c.mu.Lock()
	c.usable = usable
	for _, clip := range usable {
		if _, err := os.Stat(filepath.Join(c.dir, names[clip.ID])); err == nil {
			c.files[clip.ID] = names[clip.ID]
		}
	}
	c.mu.Unlock()
	c.removeOld(names)

	made := 0
	for _, clip := range usable {
		c.mu.Lock()
		done := c.files[clip.ID] != ""
		c.mu.Unlock()
		if done {
			continue
		}
		if err := c.makeClip(clip, names[clip.ID], has); err != nil {
			if c.ctx.Err() != nil {
				return
			}
			c.log.Warn("cannot make a clip of the playback check", "clip", clip.ID, "error", err)
			continue
		}
		made++
		c.mu.Lock()
		c.files[clip.ID] = names[clip.ID]
		c.mu.Unlock()
	}
	if made > 0 {
		c.log.Info("made the playback check's clips", "made", made, "of", len(usable), "dir", c.dir,
			"took", time.Since(start).Round(time.Second))
	}
}

// encoders asks ffmpeg which encoders it has.
func (c *Clips) encoders() (func(string) bool, error) {
	ctx, cancel := context.WithTimeout(c.ctx, encodersTimeout)
	defer cancel()
	cmd := c.command(ctx, "-hide_banner", "-encoders")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for line := range strings.SplitSeq(string(out), "\n") {
		// Such as " V....D libx264  libx264 H.264 / AVC ...", after a
		// legend of lines that start the same way but name no encoder.
		fields := strings.Fields(line)
		if len(fields) >= 2 && len(fields[0]) == 6 && strings.Trim(fields[0], "VASFXBD.") == "" && fields[1] != "=" {
			set[fields[1]] = true
		}
	}
	return func(name string) bool { return set[name] }, nil
}

// makeClip makes a clip under name, through a file that it renames once
// ffmpeg is done.
func (c *Clips) makeClip(clip media.CheckClip, name string, has func(string) bool) error {
	part := filepath.Join(c.dir, name+".part")
	args, _ := clip.Args(has, part)
	ctx, cancel := context.WithTimeout(c.ctx, makeTimeout)
	defer cancel()
	cmd := c.command(ctx, args...)
	stderr := &tail{max: maxStderr}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		os.Remove(part)
		msg := strings.TrimSpace(stderr.buf.String())
		if i := strings.LastIndexByte(msg, '\n'); i >= 0 {
			msg = msg[i+1:]
		}
		return fmt.Errorf("ffmpeg: %w: %s", err, msg)
	}
	return os.Rename(part, filepath.Join(c.dir, name))
}

// command returns ffmpeg with args, run apart from Overscan's environment.
func (c *Clips) command(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.ffmpeg, args...)
	cmd.Env = []string{} // nothing from Overscan's environment
	cmd.Dir = "/"
	cmd.WaitDelay = time.Second
	keepChild(cmd)
	return cmd
}

// removeOld removes the files of clips that are not named in names: made
// otherwise by another version, or left part made.
func (c *Clips) removeOld(names map[string]string) {
	current := map[string]bool{}
	for _, n := range names {
		current[n] = true
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && !current[n] && isClipFile(n) {
			if err := os.Remove(filepath.Join(c.dir, n)); err == nil {
				c.log.Debug("removed an old clip of the playback check", "file", n)
			}
		}
	}
}

// isClipFile reports whether a file name is one that Clips writes.
func isClipFile(name string) bool {
	name = strings.TrimSuffix(name, ".part")
	switch filepath.Ext(name) {
	case ".mkv", ".mp4", ".ts", ".avi", ".vob":
		return strings.Contains(name, "-")
	}
	return false
}

// tail keeps the last max bytes written to it.
type tail struct {
	max int
	buf bytes.Buffer
}

func (t *tail) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if over := t.buf.Len() - t.max; over > 0 {
		t.buf.Next(over)
	}
	return len(p), nil
}
