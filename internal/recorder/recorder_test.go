// SPDX-License-Identifier: AGPL-3.0-or-later

package recorder

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// fakeHub plays any link as packets of MPEG-TS until the request ends, or
// refuses it as a source at its limit does. With once, it plays the first
// request for a moment, and refuses the others. With cut, it ends what it
// plays and refuses the channel, as the hub does once an account no longer
// uses any of the channel's sources (D114); with end, it ends what it plays
// once, as the hub does when a backup can take over.
type fakeHub struct {
	mu       sync.Mutex
	refuse   bool
	once     bool
	cut      bool
	end      bool
	requests int
	devices  []int64
}

func (h *fakeHub) Link(deviceID int64, entry store.PlayableEntry) string {
	h.mu.Lock()
	h.devices = append(h.devices, deviceID)
	h.mu.Unlock()
	return "/live/token.m3u8"
}

func (h *fakeHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	h.requests++
	refuse := h.refuse || (h.once && h.requests > 1)
	brief := h.once
	h.mu.Unlock()
	if h.isCut() {
		http.Error(w, "this channel is not available", http.StatusNotFound)
		return
	}
	if refuse || !strings.HasSuffix(r.URL.Path, ".ts") {
		http.Error(w, "the source is at its limit", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	if brief {
		_, _ = w.Write(bytes.Repeat([]byte{0x47}, 188))
		return
	}
	for !h.ended() {
		if _, err := w.Write(bytes.Repeat([]byte{0x47}, 188)); err != nil {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func (h *fakeHub) isCut() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.cut
}

// ended reports whether the stream that plays should end: after cut, or
// once after end.
func (h *fakeHub) ended() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.end {
		h.end = false
		return true
	}
	return h.cut
}

func (h *fakeHub) asked() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.requests
}

// newRecorder returns a recorder over a store with one channel, and the
// channel's lineup entry.
func newRecorder(t *testing.T, hub Hub) (*Recorder, *store.Store, int64) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	src, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, src, []store.CatalogChannel{{IdentityKey: "1", Name: "Example News",
		StreamURL: "http://streams.example/1.ts"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cs, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil || len(cs) != 1 {
		t.Fatalf("channels %+v, %v", cs, err)
	}
	if err := st.SetRecordingsCap(ctx, 1); err != nil {
		t.Fatal(err)
	}
	l, tl := lead, tail
	lead, tail = 0, 0
	t.Cleanup(func() { lead, tail = l, tl })
	return New(st, hub, slog.New(slog.NewTextHandler(io.Discard, nil)), filepath.Join(t.TempDir(), "Recordings")), st, cs[0].EntryID
}

func schedule(t *testing.T, st *store.Store, entry int64, title string, start, stop time.Time) store.Recording {
	t.Helper()
	rec, err := st.ScheduleRecording(context.Background(), store.Recording{EntryID: entry, Channel: "Example News",
		Title: title, Start: start, Stop: stop, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

// waitState waits for a recording to reach a state, and returns it.
func waitState(t *testing.T, st *store.Store, id int64, state string) store.Recording {
	t.Helper()
	for range 400 {
		rec, err := st.RecordingByID(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if rec.State == state {
			return rec
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, _ := st.RecordingByID(context.Background(), id)
	t.Fatalf("recording %d is %s (%s), not %s", id, rec.State, rec.Error, state)
	return rec
}

func TestRecord(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	ctx := context.Background()
	now := time.Now()
	// The store keeps whole seconds.
	rec := schedule(t, st, entry, "News Hour: Late/Edition", now.Add(-time.Second), now.Add(2*time.Second))
	later := schedule(t, st, entry, "Later", now.Add(time.Hour), now.Add(2*time.Hour))
	var changed atomic.Int32
	r.OnChange(func() { changed.Add(1) })
	r.Check(ctx)
	// While it records, its file's name starts with a dot, which folder
	// sources pass over.
	recording := waitState(t, st, rec.ID, store.RecordingRecording)
	if _, err := os.Stat(filepath.Join(r.Dir(), "."+recording.File)); err != nil {
		t.Errorf("while it records: %v", err)
	}
	done := waitState(t, st, rec.ID, store.RecordingDone)
	if done.Size == 0 || done.File != FileName(rec) || !strings.HasPrefix(done.File, "News Hour Late Edition - Example News - ") {
		t.Errorf("done %+v", done)
	}
	if info, err := os.Stat(filepath.Join(r.Dir(), done.File)); err != nil || info.Size() != done.Size || info.Size()%188 != 0 {
		t.Errorf("file %v, %v", info, err)
	}
	if entries, _ := os.ReadDir(r.Dir()); len(entries) != 1 {
		t.Errorf("files %v", entries)
	}
	if n := changed.Load(); n != 1 {
		t.Errorf("%d changes", n)
	}
	if got, _ := st.RecordingByID(ctx, later.ID); got.State != store.RecordingScheduled {
		t.Errorf("a later recording %+v", got)
	}
	// The recording plays as a device of its own, never as a box.
	hub.mu.Lock()
	if len(hub.devices) == 0 || hub.devices[0] != -rec.ID {
		t.Errorf("devices %v", hub.devices)
	}
	hub.mu.Unlock()
	if r.Used() != done.Size {
		t.Errorf("used %d", r.Used())
	}
}

func TestRecordFails(t *testing.T) {
	hub := &fakeHub{refuse: true}
	r, st, entry := newRecorder(t, hub)
	ctx := context.Background()
	now := time.Now()

	// A source at its limit: the recording fails at once, and ends nobody's stream.
	busy := schedule(t, st, entry, "Busy", now.Add(-time.Second), now.Add(time.Hour))
	r.Check(ctx)
	if got := waitState(t, st, busy.ID, store.RecordingFailed); !strings.Contains(got.Error, "would not play the channel (503") || got.File != "" {
		t.Errorf("busy %+v", got)
	}
	entries, _ := os.ReadDir(r.Dir())
	if len(entries) != 0 {
		t.Errorf("files left %v", entries)
	}

	// One whose end has passed was missed.
	missed := schedule(t, st, entry, "Missed", now.Add(-2*time.Hour), now.Add(-time.Hour))
	r.Check(ctx)
	if got := waitState(t, st, missed.ID, store.RecordingFailed); got.Error != "the server was not running when it was on" {
		t.Errorf("missed %+v", got)
	}

	// With recording off, a due one fails.
	if err := st.SetRecordingsCap(ctx, 0); err != nil {
		t.Fatal(err)
	}
	off := schedule(t, st, entry, "Off", now.Add(-time.Second), now.Add(time.Hour))
	r.Check(ctx)
	if got := waitState(t, st, off.ID, store.RecordingFailed); got.Error != "recording is turned off" {
		t.Errorf("off %+v", got)
	}
}

func TestCancelAndRecover(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	ctx := context.Background()
	now := time.Now()
	rec := schedule(t, st, entry, "Long", now.Add(-time.Second), now.Add(time.Hour))
	r.Check(ctx)
	waitState(t, st, rec.ID, store.RecordingRecording)
	time.Sleep(50 * time.Millisecond)
	if !r.Cancel(rec.ID) {
		t.Fatal("not running")
	}
	if got := waitState(t, st, rec.ID, store.RecordingDone); got.Size == 0 || got.Dir != r.Dir() {
		t.Errorf("cancelled %+v", got)
	}
	if r.Cancel(rec.ID) {
		t.Error("cancelled twice")
	}

	// One that a stop of the server left under way ends when called off,
	// before a start goes on with it.
	paused := schedule(t, st, entry, "Paused", now.Add(-10*time.Minute), now.Add(time.Hour))
	if err := st.SetRecordingState(ctx, paused.ID, store.RecordingRecording, "", FileName(paused), 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), "."+FileName(paused)), make([]byte, 376), 0o600); err != nil {
		t.Fatal(err)
	}
	if !r.Cancel(paused.ID) {
		t.Error("paused: not cancelled")
	}
	if got, _ := st.RecordingByID(ctx, paused.ID); got.State != store.RecordingDone || got.Size != 376 || got.File != FileName(paused) {
		t.Errorf("paused %+v", got)
	}

	// A recording that a stop of the server left under way, and whose
	// programme has ended, ends at the next start, which keeps what it wrote
	// under its own name.
	left := schedule(t, st, entry, "Left", now.Add(-2*time.Hour), now.Add(-time.Hour))
	if err := st.SetRecordingState(ctx, left.ID, store.RecordingRecording, "", FileName(left), 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), "."+FileName(left)), make([]byte, 188), 0o600); err != nil {
		t.Fatal(err)
	}
	empty := schedule(t, st, entry, "Empty", now.Add(-90*time.Minute), now.Add(-time.Hour))
	if err := st.SetRecordingState(ctx, empty.ID, store.RecordingRecording, "", FileName(empty), 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Dir(), "."+FileName(empty)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.recover(ctx)
	if got, _ := st.RecordingByID(ctx, left.ID); got.State != store.RecordingFailed || got.Error != "the server stopped while it recorded" ||
		got.File != FileName(left) || got.Size != 188 {
		t.Errorf("left %+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), FileName(left))); err != nil {
		t.Errorf("its file: %v", err)
	}
	if got, _ := st.RecordingByID(ctx, empty.ID); got.State != store.RecordingFailed || got.File != "" {
		t.Errorf("empty %+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), "."+FileName(empty))); !os.IsNotExist(err) {
		t.Errorf("an empty file is left: %v", err)
	}

	// One left under way in a folder that is no longer the recordings
	// folder ends there at the next start, rather than going on.
	old := t.TempDir()
	moved := schedule(t, st, entry, "Moved", now.Add(-5*time.Minute), now.Add(time.Hour))
	if err := st.SetRecordingState(ctx, moved.ID, store.RecordingRecording, old, FileName(moved), 0, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "."+FileName(moved)), make([]byte, 188), 0o600); err != nil {
		t.Fatal(err)
	}
	r.recover(ctx)
	if got, _ := st.RecordingByID(ctx, moved.ID); got.State != store.RecordingFailed || got.Dir != old || got.Size != 188 ||
		!strings.Contains(got.Error, "the recordings folder changed") {
		t.Errorf("moved %+v", got)
	}
	if _, err := os.Stat(filepath.Join(old, FileName(moved))); err != nil {
		t.Errorf("its file: %v", err)
	}
}

// A source that refuses once the stream breaks off is asked less and less
// often, not every few seconds.
func TestRetryBacksOff(t *testing.T) {
	hub := &fakeHub{once: true}
	r, st, entry := newRecorder(t, hub)
	rt, mr := retry, maxRetry
	retry, maxRetry = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { retry, maxRetry = rt, mr })
	now := time.Now()
	rec := schedule(t, st, entry, "Refused", now.Add(-time.Second), now.Add(time.Hour))
	r.Check(context.Background())
	waitState(t, st, rec.ID, store.RecordingRecording)
	time.Sleep(400 * time.Millisecond)
	r.Cancel(rec.ID)
	done := waitState(t, st, rec.ID, store.RecordingDone)
	hub.mu.Lock()
	n := hub.requests
	hub.mu.Unlock()
	// Every 10 ms, it would have asked about 40 times; doubling up to 40 ms,
	// about 12.
	if n < 4 || n > 20 || done.Size != 188 {
		t.Errorf("%d requests in 400 ms, %d bytes", n, done.Size)
	}
}

func TestServerStops(t *testing.T) {
	r, st, entry := newRecorder(t, &fakeHub{})
	now := time.Now()
	rec := schedule(t, st, entry, "Cut short", now.Add(-time.Second), now.Add(time.Hour))
	old := schedule(t, st, entry, "Old", now.Add(-60*24*time.Hour), now.Add(-59*24*time.Hour))
	run := func() (stop func()) {
		ctx, cancel := context.WithCancel(context.Background())
		ran := make(chan struct{})
		go func() {
			r.Run(ctx)
			close(ran)
		}()
		return func() {
			cancel()
			<-ran
		}
	}
	stop := run()
	waitState(t, st, rec.ID, store.RecordingRecording)
	time.Sleep(50 * time.Millisecond)
	stop()
	// A stop of the server leaves it under way, with what it wrote.
	got, _ := st.RecordingByID(context.Background(), rec.ID)
	part := filepath.Join(r.Dir(), "."+got.File)
	first := sizeOf(part)
	if got.State != store.RecordingRecording || first == 0 {
		t.Errorf("stopped %+v, %d bytes", got, first)
	}
	// One that failed long ago with no file is forgotten.
	if _, err := st.RecordingByID(context.Background(), old.ID); err != store.ErrNotFound {
		t.Errorf("old %v", err)
	}

	// The next start goes on with it, adding to its file.
	stop = run()
	for range 200 {
		if sizeOf(part) > first {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !r.Cancel(rec.ID) {
		t.Fatal("not resumed")
	}
	if done := waitState(t, st, rec.ID, store.RecordingDone); done.Size <= first || done.Error != "" {
		t.Errorf("resumed %+v, from %d bytes", done, first)
	}
	stop()
}

func TestFileRoom(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "rec")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	o := &file{f: f, room: 300}
	if n, err := o.Write(make([]byte, 188)); n != 188 || err != nil {
		t.Fatalf("first %d %v", n, err)
	}
	if _, err := o.Write(make([]byte, 188)); err == nil || !o.over {
		t.Errorf("past the room: %v %v", err, o.over)
	}

	// Recordings made at the same time share the folder's room: each looks
	// at its size as it goes.
	looks, full := 0, false
	o = &file{f: f, room: 1 << 40, full: func() bool { looks++; return full }}
	chunk := make([]byte, 1<<20)
	for range 2 * checkEvery / len(chunk) {
		if _, err := o.Write(chunk); err != nil {
			t.Fatal(err)
		}
	}
	if looks != 2 {
		t.Errorf("%d looks at the folder in %d bytes", looks, o.n)
	}
	full = true
	for o.n < 3*checkEvery {
		if _, err := o.Write(chunk); err != nil {
			break
		}
	}
	if !o.over || o.n != 2*checkEvery {
		t.Errorf("a full folder: over %v at %d bytes", o.over, o.n)
	}
}

func TestFileName(t *testing.T) {
	start := time.Date(2026, 12, 4, 21, 0, 0, 0, time.Local)
	rec := store.Recording{ID: 7, Title: `A/B: "C"?`, Channel: "", Start: start.UTC()}
	if got := FileName(rec); got != "A B C - Recording - 2026-12-04 21.00 [7].ts" {
		t.Errorf("name %q", got)
	}
}

// A recording stops when its account ends, keeping what it wrote, and none
// begins after it (D95).
func TestRecordingStopsWithItsAccount(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	rec, err := st.ScheduleRecording(ctx, store.Recording{AccountID: 1, EntryID: entry, Channel: "Example News",
		Title: "Long", Start: now.Add(-time.Second), Stop: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountExpiry(ctx, 1, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	r.Check(ctx)
	waitState(t, st, rec.ID, store.RecordingRecording)
	got := waitState(t, st, rec.ID, store.RecordingFailed)
	if got.Error != "its account ended while it recorded" || got.Size == 0 || got.File == "" {
		t.Errorf("at the account's end: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), got.File)); err != nil {
		t.Errorf("what it wrote: %v", err)
	}

	later, err := st.ScheduleRecording(ctx, store.Recording{AccountID: 1, EntryID: entry, Channel: "Example News",
		Title: "Later", Start: time.Now().Add(-time.Second), Stop: time.Now().Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	r.Check(ctx)
	if got := waitState(t, st, later.ID, store.RecordingFailed); got.Error != errAccountEnded || got.File != "" {
		t.Errorf("after the account's end: %+v", got)
	}
}

// A recording stops, keeping what it wrote, once its account no longer uses
// its channel's source, rather than asking for the channel again until its
// end; and none of that channel begins for the account after it (D114).
func TestRecordingStopsWithItsSources(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	rt := retry
	retry = 10 * time.Millisecond
	t.Cleanup(func() { retry = rt })
	ctx := context.Background()
	other, err := st.AddSource(ctx, source.Settings{Name: "one", URL: "http://provider.example/one.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	rec, err := st.ScheduleRecording(ctx, store.Recording{AccountID: 1, EntryID: entry, Channel: "Example News",
		Title: "Long", Start: now.Add(-time.Second), Stop: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	r.Check(ctx)
	waitState(t, st, rec.ID, store.RecordingRecording)
	time.Sleep(50 * time.Millisecond)
	if err := st.SetAccountSources(ctx, 1, store.Sources{Only: true, IDs: []int64{other}}); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	hub.cut = true
	hub.mu.Unlock()
	got := waitState(t, st, rec.ID, store.RecordingFailed)
	if got.Error != errSources || got.Size == 0 || got.File == "" {
		t.Errorf("once its account's sources changed: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(r.Dir(), got.File)); err != nil {
		t.Errorf("what it wrote: %v", err)
	}
	if n := hub.asked(); n != 1 {
		t.Errorf("%d requests, not 1", n)
	}

	later, err := st.ScheduleRecording(ctx, store.Recording{AccountID: 1, EntryID: entry, Channel: "Example News",
		Title: "Later", Start: time.Now().Add(-time.Second), Stop: time.Now().Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	r.Check(ctx)
	if got := waitState(t, st, later.ID, store.RecordingFailed); got.Error != errSources || got.File != "" {
		t.Errorf("after the change: %+v", got)
	}
	if n := hub.asked(); n != 1 {
		t.Errorf("%d requests after the change, not 1", n)
	}
}

// A recording goes on, from the channel's backup, when its account keeps
// only the backup's source (D109, D114).
func TestRecordingGoesOnFromABackup(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	rt := retry
	retry = 10 * time.Millisecond
	t.Cleanup(func() { retry = rt })
	ctx := context.Background()
	other, err := st.AddSource(ctx, source.Settings{Name: "one", URL: "http://provider.example/one.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, other, []store.CatalogChannel{{IdentityKey: "1", Name: "Example News HD",
		StreamURL: "http://streams.example/one/1.ts"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cs, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	var backup int64
	for _, c := range cs {
		if c.SourceID == other {
			backup = c.EntryID
		}
	}
	if err := st.SetEntryBackups(ctx, store.DefaultLineupID, entry, []int64{backup}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	rec, err := st.ScheduleRecording(ctx, store.Recording{AccountID: 1, EntryID: entry, Channel: "Example News",
		Title: "Long", Start: now.Add(-time.Second), Stop: now.Add(time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	r.Check(ctx)
	waitState(t, st, rec.ID, store.RecordingRecording)
	time.Sleep(50 * time.Millisecond)
	if err := st.SetAccountSources(ctx, 1, store.Sources{Only: true, IDs: []int64{other}}); err != nil {
		t.Fatal(err)
	}
	hub.mu.Lock()
	hub.end = true
	hub.mu.Unlock()
	for range 200 {
		if hub.asked() > 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := hub.asked(); n < 2 {
		t.Fatalf("%d requests: it did not play the channel again", n)
	}
	if got, _ := st.RecordingByID(ctx, rec.ID); got.State != store.RecordingRecording {
		t.Errorf("after the change: %+v", got)
	}
	r.Cancel(rec.ID)
	if done := waitState(t, st, rec.ID, store.RecordingDone); done.Error != "" || done.Size == 0 {
		t.Errorf("done %+v", done)
	}
}

// A recording that runs until it is stopped ends at its stop, the latest it
// may run, without the tail of a programme's; and Wake starts one that a box
// has just started without waiting for the next look (D98).
func TestRecordingUntilStopped(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	tk := tick
	tick, tail = time.Hour, time.Hour
	t.Cleanup(func() { tick = tk })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.Run(ctx)
	time.Sleep(50 * time.Millisecond) // past Run's first look
	now := time.Now()
	rec, err := st.ScheduleRecording(ctx, store.Recording{EntryID: entry, Channel: "Example News", Title: "Example News",
		Start: now, Stop: now.Add(2 * time.Second), UntilStopped: true, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	r.Wake()
	waitState(t, st, rec.ID, store.RecordingRecording)
	done := waitState(t, st, rec.ID, store.RecordingDone)
	if done.Size == 0 || !done.UntilStopped {
		t.Errorf("done %+v", done)
	}
}

// TestRecordingByTime checks that a recording set by time begins at its
// start, not a minute before, and ends at its stop, not two minutes after
// (D101).
func TestRecordingByTime(t *testing.T) {
	hub := &fakeHub{}
	r, st, entry := newRecorder(t, hub)
	tk := tick
	tick, lead, tail = 20*time.Millisecond, time.Hour, time.Hour
	t.Cleanup(func() { tick = tk })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now().Truncate(time.Second).Add(2 * time.Second)
	rec, err := st.ScheduleRecording(ctx, store.Recording{EntryID: entry, Channel: "Example News", Title: "Example News",
		Start: start, Stop: start.Add(time.Second), Timed: true, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	go r.Run(ctx)
	time.Sleep(300 * time.Millisecond)
	if got, err := st.RecordingByID(ctx, rec.ID); err != nil || got.State != store.RecordingScheduled {
		t.Fatalf("before its start: %+v, %v", got, err)
	}
	waitState(t, st, rec.ID, store.RecordingRecording)
	if time.Now().Before(start) {
		t.Errorf("began %v before its start", time.Until(start))
	}
	done := waitState(t, st, rec.ID, store.RecordingDone)
	if done.Size == 0 || !done.Timed {
		t.Errorf("done %+v", done)
	}
}
