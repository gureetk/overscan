// SPDX-License-Identifier: AGPL-3.0-or-later

// Package recorder records programmes on the server (D89). At a scheduled
// recording's start, a minute early, it plays the channel through the hub,
// as a device of its own would, so that the source's connection limit, the
// repeat filter and repair apply as to any stream, and writes the stream to
// a file in the recordings folder until two minutes after its end. A
// recording that finds its source at its limit fails with that reason: it
// never ends someone's stream. While it records, its file's name starts
// with a dot, which folder sources pass over; it takes its own name when
// the recording ends.
package recorder

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"overscan.gureet.ca/internal/store"
)

// Hub is what the recorder needs of the stream hub.
type Hub interface {
	Link(deviceID int64, entry store.PlayableEntry) string
	ServeHTTP(w http.ResponseWriter, r *http.Request)
}

// Timing, which tests shorten.
var (
	// lead and tail are how much earlier than its start a recording begins,
	// and how much later than its end it stops: guides are not exact.
	lead = time.Minute
	tail = 2 * time.Minute
	// tick is how often the recorder looks for recordings to start.
	tick = 10 * time.Second
	// retry is how long the recorder waits to play the channel again when
	// its stream breaks off; the wait doubles, up to maxRetry, while the
	// channel gives nothing, so that a source that refuses is not asked
	// every few seconds.
	retry    = 5 * time.Second
	maxRetry = time.Minute
)

// keep is how long the recorder keeps recordings that failed with no file
// or were called off, for the admin to see.
const keep = 30 * 24 * time.Hour

// gigabyte is the unit of the recordings' cap.
const gigabyte = 1 << 30

// Recorder starts and ends recordings.
type Recorder struct {
	store *store.Store
	hub   Hub
	log   *slog.Logger
	dir   string
	now   func() time.Time

	mu      sync.Mutex
	running map[int64]context.CancelFunc
	wg      sync.WaitGroup
	changed func() // called when a recording's file is made or deleted
}

// OnChange sets what to call when a recording's file is made, or deleted
// in the admin: reading the folder sources that list it again.
func (r *Recorder) OnChange(f func()) {
	r.mu.Lock()
	r.changed = f
	r.mu.Unlock()
}

// Changed says that a recording's file was made or deleted.
func (r *Recorder) Changed() {
	r.mu.Lock()
	f := r.changed
	r.mu.Unlock()
	if f != nil {
		f()
	}
}

// partName is the name of a recording's file while it records.
func partName(name string) string { return "." + name }

// New returns a recorder that writes recordings into dir.
func New(st *store.Store, hub Hub, log *slog.Logger, dir string) *Recorder {
	return &Recorder{store: st, hub: hub, log: log, dir: filepath.Clean(dir), now: time.Now, running: map[int64]context.CancelFunc{}}
}

// Dir returns the recordings folder.
func (r *Recorder) Dir() string { return r.dir }

// dirOf returns the folder that rec's file is in: the one it was made in,
// or the recordings folder for one made before schema 30 kept it.
func (r *Recorder) dirOf(rec store.Recording) string {
	if rec.Dir != "" {
		return filepath.Clean(rec.Dir)
	}
	return r.dir
}

// Run starts and ends recordings until ctx is done, and then stops those
// under way, keeping what they wrote.
func (r *Recorder) Run(ctx context.Context) {
	r.recover(ctx)
	t := time.NewTicker(tick)
	defer t.Stop()
	var pruned time.Time
	for {
		r.Check(ctx)
		if now := r.now(); now.Sub(pruned) >= time.Hour {
			pruned = now
			if _, err := r.store.PruneRecordings(ctx, now.Add(-keep)); err != nil && ctx.Err() == nil {
				r.log.Error("cannot forget old recordings", "error", err)
			}
		}
		select {
		case <-ctx.Done():
			r.wg.Wait()
			return
		case <-t.C:
		}
	}
}

// recover goes on with the recordings that a stop of the server left
// under way whose programmes have not ended, and ends the others.
func (r *Recorder) recover(ctx context.Context) {
	left, err := r.store.Recordings(ctx, 0, store.RecordingRecording)
	if err == nil && len(left) == 0 {
		return
	}
	var limit int
	if err == nil {
		limit, err = r.store.RecordingsCap(ctx)
	}
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cannot list recordings", "error", err)
		}
		return
	}
	for _, rec := range left {
		r.resume(ctx, rec, int64(limit)*gigabyte)
	}
}

// Check starts the recordings that are due, and fails those that can no
// longer be made.
func (r *Recorder) Check(ctx context.Context) {
	due, err := r.store.Recordings(ctx, 0, store.RecordingScheduled)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cannot list recordings", "error", err)
		}
		return
	}
	if len(due) == 0 {
		return
	}
	limit, err := r.store.RecordingsCap(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cannot read the recordings' room", "error", err)
		}
		return
	}
	now := r.now()
	for _, rec := range due {
		switch {
		case now.Before(rec.Start.Add(-lead)):
			continue
		case !now.Before(rec.Stop):
			r.fail(ctx, rec, "the server was not running when it was on")
		case limit == 0:
			r.fail(ctx, rec, "recording is turned off")
		default:
			r.start(ctx, rec, int64(limit)*gigabyte)
		}
	}
}

// Cancel ends a recording under way now, keeping what it wrote as a
// recording that is done; it reports whether one was. One that a stop of
// the server left under way, and that no start has gone on with, ends
// too.
func (r *Recorder) Cancel(id int64) bool {
	r.mu.Lock()
	stop, ok := r.running[id]
	r.mu.Unlock()
	if ok {
		stop()
		return true
	}
	ctx := context.Background()
	rec, err := r.store.RecordingByID(ctx, id)
	if err != nil || rec.State != store.RecordingRecording {
		return false
	}
	r.finish(ctx, rec, rec.File, "")
	return true
}

func (r *Recorder) fail(ctx context.Context, rec store.Recording, why string) {
	r.log.Warn("a recording failed", "title", rec.Title, "channel", rec.Channel, "reason", why)
	if err := r.store.SetRecordingState(ctx, rec.ID, store.RecordingFailed, rec.Dir, rec.File, rec.Size, why); err != nil {
		r.log.Error("cannot record what became of a recording", "title", rec.Title, "error", err)
	}
}

// start begins a recording, unless the folder has no room left or the
// channel has gone.
func (r *Recorder) start(ctx context.Context, rec store.Recording, limit int64) {
	entry, used, why, err := r.ready(ctx, rec, limit)
	if err != nil {
		r.log.Error("cannot look up a recording's channel", "title", rec.Title, "error", err)
		return
	}
	if why != "" {
		r.fail(ctx, rec, why)
		return
	}
	name := FileName(rec)
	f, err := os.OpenFile(filepath.Join(r.dir, partName(name)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		r.fail(ctx, rec, "its file cannot be made: "+err.Error())
		return
	}
	if err := r.store.SetRecordingState(ctx, rec.ID, store.RecordingRecording, r.dir, name, 0, ""); err != nil {
		r.log.Error("cannot record that a recording began", "title", rec.Title, "error", err)
		_ = f.Close()
		_ = os.Remove(f.Name())
		return
	}
	rec.File, rec.Dir = name, r.dir
	r.log.Info("a recording began", "title", rec.Title, "channel", rec.Channel,
		"until", rec.Stop.Add(tail).UTC().Format(time.DateTime))
	r.run(ctx, rec, entry, r.fileFor(f, limit, used), false)
}

// resume goes on with a recording that a stop of the server cut short, if
// its programme has not ended, adding to its file. Otherwise it ends it,
// keeping what it wrote.
func (r *Recorder) resume(ctx context.Context, rec store.Recording, limit int64) {
	const stopped = "the server stopped while it recorded"
	if rec.File == "" || limit == 0 || !r.now().Before(rec.Stop.Add(tail)) {
		r.finish(ctx, rec, rec.File, stopped)
		return
	}
	if r.dirOf(rec) != r.dir {
		r.finish(ctx, rec, rec.File, "the server stopped while it recorded, and the recordings folder changed")
		return
	}
	entry, used, why, err := r.ready(ctx, rec, limit)
	if err != nil || why != "" {
		if why == "" {
			why = stopped
		}
		r.finish(ctx, rec, rec.File, why)
		return
	}
	name := filepath.Base(rec.File)
	part, own := filepath.Join(r.dir, partName(name)), filepath.Join(r.dir, name)
	rec.Dir = r.dir
	if _, err := os.Stat(part); errors.Is(err, fs.ErrNotExist) {
		// It stopped as it took its own name.
		_ = os.Rename(own, part)
	}
	f, err := os.OpenFile(part, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o640)
	if err != nil {
		r.finish(ctx, rec, rec.File, stopped)
		return
	}
	r.log.Info("a recording goes on after the server's restart", "title", rec.Title, "channel", rec.Channel,
		"until", rec.Stop.Add(tail).UTC().Format(time.DateTime))
	r.run(ctx, rec, entry, r.fileFor(f, limit, used), true)
}

// ready checks that a recording can be made: that its channel is still in
// the lineup, and that the folder has room. It returns the channel, the
// room the folder's files take, and why the recording cannot be made, or
// an error that a later look may not have.
func (r *Recorder) ready(ctx context.Context, rec store.Recording, limit int64) (store.PlayableEntry, int64, string, error) {
	entry, err := r.store.PlayableEntry(ctx, rec.EntryID)
	if errors.Is(err, store.ErrNotFound) {
		return entry, 0, "the channel is no longer in the lineup", nil
	}
	if err != nil {
		return entry, 0, "", err
	}
	if err := os.MkdirAll(r.dir, 0o750); err != nil {
		return entry, 0, "the recordings folder cannot be made: " + err.Error(), nil
	}
	used, err := folderSize(r.dir)
	if err != nil {
		return entry, 0, "the recordings folder cannot be read: " + err.Error(), nil
	}
	if used >= limit {
		return entry, used, "the recordings folder is full", nil
	}
	return entry, used, "", nil
}

// fileFor returns f as a recording's file, which stops taking bytes once
// the folder's files take limit.
func (r *Recorder) fileFor(f *os.File, limit, used int64) *file {
	return &file{f: f, room: limit - used, full: func() bool {
		n, err := folderSize(r.dir)
		return err == nil && n >= limit
	}}
}

// run records in the background until the recording's end, or until it is
// called off. A stop of the server leaves it under way, for the next start
// to go on with or to end.
func (r *Recorder) run(ctx context.Context, rec store.Recording, entry store.PlayableEntry, out *file, resumed bool) {
	stop := rec.Stop.Add(tail)
	rctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), stop)
	r.mu.Lock()
	r.running[rec.ID] = cancel
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer cancel()
		// The server's stop ends it too.
		go func() {
			select {
			case <-ctx.Done():
				cancel()
			case <-rctx.Done():
			}
		}()
		why := r.record(rctx, rec, entry, out)
		_ = out.f.Close()
		r.mu.Lock()
		delete(r.running, rec.ID)
		r.mu.Unlock()
		if ctx.Err() != nil && r.now().Before(stop) {
			r.log.Info("a recording stops with the server; the next start goes on with it", "title", rec.Title,
				"channel", rec.Channel)
			return
		}
		if resumed && why == errNothing {
			why = "the server stopped while it recorded"
		}
		r.finish(context.WithoutCancel(ctx), rec, rec.File, why)
	}()
}

// record plays the channel until ctx ends, writing its stream to out, and
// plays it again when it breaks off. It returns why it failed, or "".
func (r *Recorder) record(ctx context.Context, rec store.Recording, entry store.PlayableEntry, out *file) string {
	wait := retry
	for attempt := 0; ctx.Err() == nil; attempt++ {
		before := out.n
		// The link that plays the channel as one continuous stream, even
		// one whose address is HLS. The device is the recording's own.
		link := strings.TrimSuffix(r.hub.Link(-rec.ID, entry), ".m3u8")
		if !strings.HasSuffix(link, ".ts") {
			link += ".ts"
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://recorder"+link, nil)
		if err != nil {
			return "its stream cannot be asked for"
		}
		w := &writer{out: out, header: http.Header{}}
		r.hub.ServeHTTP(w, req)
		switch {
		case out.over:
			return "the recordings folder is full"
		case out.err != nil:
			return "its file cannot be written: " + out.err.Error()
		case w.status != 0 && w.status != http.StatusOK && out.n == 0 && attempt == 0:
			// Its source would not play it, most often because it plays
			// as many streams as it may.
			return fmt.Sprintf("its source would not play the channel (%d %s)", w.status, http.StatusText(w.status))
		}
		if ctx.Err() != nil {
			break
		}
		if out.n > before {
			wait = retry
			r.log.Warn("a recording's stream broke off; it plays the channel again", "title", rec.Title, "channel", rec.Channel)
		} else if attempt > 0 {
			wait = min(2*wait, maxRetry)
		}
		select {
		case <-ctx.Done():
		case <-time.After(wait):
		}
	}
	if out.n == 0 {
		return errNothing
	}
	return ""
}

// errNothing is why a recording that wrote nothing failed.
const errNothing = "nothing came from its source"

// finish records how a recording ended, and gives its file its own name,
// or removes it if it is empty.
func (r *Recorder) finish(ctx context.Context, rec store.Recording, name, why string) {
	var size int64
	dir := r.dirOf(rec)
	if name != "" {
		part := filepath.Join(dir, partName(filepath.Base(name)))
		if size = sizeOf(part); size > 0 {
			if err := os.Rename(part, filepath.Join(dir, filepath.Base(name))); err != nil {
				r.log.Error("cannot give a recording's file its name", "title", rec.Title, "error", err)
			}
		} else {
			_ = os.Remove(part)
			size = sizeOf(filepath.Join(dir, filepath.Base(name)))
		}
		if size == 0 {
			name = ""
		}
	}
	state := store.RecordingDone
	if why == "" && size == 0 {
		why = errNothing
	}
	if why != "" {
		state = store.RecordingFailed
		r.log.Warn("a recording failed", "title", rec.Title, "channel", rec.Channel, "reason", why, "bytes", size)
	} else {
		r.log.Info("a recording ended", "title", rec.Title, "channel", rec.Channel, "bytes", size)
	}
	if name == "" {
		dir = ""
	}
	if err := r.store.SetRecordingState(ctx, rec.ID, state, dir, name, size, why); err != nil {
		r.log.Error("cannot record what became of a recording", "title", rec.Title, "error", err)
	}
	if name != "" {
		r.Changed()
	}
}

// file is a recording's file, which stops taking bytes once the folder's
// room is used: the room it had at its start, and, as recordings made at
// the same time share it, the folder's size each checkEvery bytes.
type file struct {
	f     *os.File
	room  int64
	full  func() bool // whether the folder has no room left; nil in tests
	n     int64
	check int64 // when to look at the folder's size again
	over  bool
	err   error
}

// checkEvery is how many bytes a recording writes between looks at the
// folder's size.
const checkEvery = 64 << 20

func (o *file) Write(p []byte) (int, error) {
	if o.over || o.err != nil {
		return 0, errors.New("the recording stopped")
	}
	if o.n+int64(len(p)) > o.room || (o.full != nil && o.n >= o.check && o.full()) {
		o.over = true
		return 0, errors.New("the recordings folder is full")
	}
	if o.n >= o.check {
		o.check = o.n + checkEvery
	}
	n, err := o.f.Write(p)
	o.n += int64(n)
	if err != nil {
		o.err = err
	}
	return n, err
}

// writer takes the hub's answer as a device would, writing its body to the
// recording's file.
type writer struct {
	out    *file
	header http.Header
	status int
}

func (w *writer) Header() http.Header { return w.header }

func (w *writer) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *writer) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	if w.status != http.StatusOK {
		return len(p), nil
	}
	return w.out.Write(p)
}

func (w *writer) Flush() {}

// FileName returns a recording's file's name: its title, its channel, the
// day and time it began in the server's time zone, without the characters
// that file systems refuse, and its number in square brackets, which keeps
// it apart from another and which a folder source leaves out of the title,
// as in "News Hour - Example News - 2026-12-04 21.00 [7].ts".
func FileName(rec store.Recording) string {
	name := clean(rec.Title) + " - " + clean(rec.Channel) + " - " + rec.Start.Local().Format("2006-01-02 15.04")
	return fmt.Sprintf("%s [%d].ts", name, rec.ID)
}

// clean keeps a name's letters, digits, spaces and common punctuation, and
// cuts it to 80 bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			return r
		case strings.ContainsRune(" .,'!&+-_()", r):
			return r
		default:
			return ' '
		}
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	s = strings.Trim(s, ". ")
	if s == "" {
		s = "Recording"
	}
	if len(s) > 80 {
		s = strings.TrimSpace(strings.ToValidUTF8(s[:80], ""))
	}
	return s
}

// folderSize adds up the sizes of the files in the recordings folder.
func folderSize(dir string) (int64, error) {
	var n int64
	err := filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n, err
}

// Used returns how much room the recordings take.
func (r *Recorder) Used() int64 {
	n, _ := folderSize(r.dir)
	return n
}

// sizeOf returns a file's size, or 0.
func sizeOf(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.Size()
}
