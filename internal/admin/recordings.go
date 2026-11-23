// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Recordings made on the server (D89): the Settings page turns recording on
// with the room it may take, and lists the recordings to come and the last
// ones made, which it can call off or delete.

// Recorder is what the admin needs of the recorder.
type Recorder interface {
	Dir() string
	Used() int64
	Cancel(id int64) bool
	// Changed says that a recording's file was deleted.
	Changed()
}

// UseRecorder gives the admin the recorder; without it, the server does not
// record.
func (a *Admin) UseRecorder(r Recorder) { a.recorder = r }

// recordingCaps are the rooms, in gigabytes, that the Settings page offers.
var recordingCaps = []int{0, 10, 25, 50, 100, 250, 500}

// recordingHours are the most hours, for a recording that runs until it is
// stopped, that the Settings page offers (D98).
var recordingHours = []int{1, 2, 3, 4, 6, 8, 12, 24}

// setRecordUntil sets what a recording that a box starts on a channel runs
// until while the guide shows a programme: its end, or until it is
// stopped (D98).
func (a *Admin) setRecordUntil(ctx context.Context, until string) error {
	if until != store.RecordUntilProgramme && until != store.RecordUntilStopped {
		return badEdit(http.StatusBadRequest, "bad_recordings_until", "A recording runs until the end of its programme, or until it is stopped.")
	}
	if err := a.store.SetRecordUntil(ctx, until); err != nil {
		return err
	}
	a.log.Info("what recordings run until set in the admin", "until", until)
	return nil
}

// setRecordingHours sets the most hours that a recording which runs until
// it is stopped may run (D98).
func (a *Admin) setRecordingHours(ctx context.Context, hours int) error {
	if hours < 1 || hours > store.MaxRecordingHours {
		return badEdit(http.StatusBadRequest, "bad_recordings_max_hours", "A recording that runs until it is stopped may run from 1 to %d hours.", store.MaxRecordingHours)
	}
	if err := a.store.SetRecordingHours(ctx, hours); err != nil {
		return err
	}
	a.log.Info("the longest recording set in the admin", "hours", hours)
	return nil
}

// recordingEndForm sets from the Settings page what a recording that a box
// starts on a channel runs until, and how long one that runs until it is
// stopped may run.
func (a *Admin) recordingEndForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	hours, err := strconv.Atoi(r.PostFormValue("hours"))
	if err != nil {
		hours = 0
	}
	err = a.setRecordUntil(ctx, r.PostFormValue("until"))
	if err == nil {
		err = a.setRecordingHours(ctx, hours)
	}
	done := fmt.Sprintf("A recording that a box starts on a channel stops at the end of its programme, or, without one in the guide, after at most %s.", hoursText(hours))
	if r.PostFormValue("until") == store.RecordUntilStopped {
		done = fmt.Sprintf("A recording that a box starts on a channel runs until it is stopped, or for at most %s.", hoursText(hours))
	}
	a.settingsDone(w, r, s, err, done)
}

// hoursText says a number of hours: "1 hour", "4 hours".
func hoursText(n int) string {
	if n == 1 {
		return "1 hour"
	}
	return strconv.Itoa(n) + " hours"
}

// recordingSourceName is the name of the folder source that recordings
// play from.
const recordingSourceName = "recordings"

// setRecordingsCap turns recording on with the room it may take, in
// gigabytes, or off with 0. Turning it on makes the recordings folder and,
// unless a source reads it already, a folder source for it, from which
// boxes play recordings on Movies.
func (a *Admin) setRecordingsCap(ctx context.Context, gb int) error {
	if gb < 0 || gb > store.MaxRecordingsCap {
		return badEdit(http.StatusBadRequest, "bad_recordings_cap", "The room for recordings is a whole number of gigabytes, from 0 to %d.", store.MaxRecordingsCap)
	}
	if gb > 0 && a.recorder == nil {
		return badEdit(http.StatusServiceUnavailable, "no_recorder", "This server cannot record.")
	}
	if gb > 0 {
		if err := a.recordingSource(ctx); err != nil {
			return err
		}
	}
	if err := a.store.SetRecordingsCap(ctx, gb); err != nil {
		return err
	}
	a.log.Info("recording set in the admin", "room_gb", gb)
	return nil
}

// recordingSource makes the recordings folder, and a folder source that
// reads it unless one does. When recordings_dir has changed, the source
// named recordings reads the new folder instead of the one before.
func (a *Admin) recordingSource(ctx context.Context) error {
	dir := a.recorder.Dir()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return badEdit(http.StatusInternalServerError, "no_folder", "The recordings folder cannot be made: %v", err)
	}
	sources, err := a.store.Sources(ctx)
	if err != nil {
		return err
	}
	name := recordingSourceName
	names := map[string]bool{}
	var moved *store.Source
	var before source.Settings
	for i, s := range sources {
		names[s.Name] = true
		if s.Type != source.TypeFolder {
			continue
		}
		settings, err := a.store.SourceSettings(ctx, s.ID)
		if err != nil {
			continue
		}
		if filepath.Clean(settings.URL) == filepath.Clean(dir) {
			return nil
		}
		if s.Name == recordingSourceName {
			moved, before = &sources[i], settings
		}
	}
	if moved != nil {
		from := before.URL
		before.URL = dir
		if err := a.updateSource(ctx, moved.ID, before); err != nil {
			return err
		}
		a.log.Warn("the recordings folder changed, so its source reads the new one; recordings made before stay where they were",
			"source", moved.Name, "from", from, "to", dir)
		return nil
	}
	for i := 2; names[name]; i++ {
		name = recordingSourceName + "-" + strconv.Itoa(i)
	}
	_, err = a.addSource(ctx, source.Settings{Name: name, Type: source.TypeFolder, URL: dir}, false)
	return err
}

// PrepareRecordings makes the recordings folder and its source as the
// server starts, while recording is on, so that a recordings_dir changed
// in the configuration file takes effect at once.
func (a *Admin) PrepareRecordings(ctx context.Context) {
	if a.recorder == nil {
		return
	}
	gb, err := a.store.RecordingsCap(ctx)
	if err != nil || gb == 0 {
		if err != nil && ctx.Err() == nil {
			a.log.Error("cannot read the recordings' room", "error", err)
		}
		return
	}
	if err := a.recordingSource(ctx); err != nil && ctx.Err() == nil {
		a.log.Error("cannot make the recordings folder or its source", "error", err)
	}
}

// recordingsForm sets from the Settings page the room that recordings may
// take, or turns recording off.
func (a *Admin) recordingsForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	gb, err := strconv.Atoi(r.PostFormValue("cap"))
	if err != nil {
		gb = -1
	}
	done := "Recording is off. The recordings made stay in their folder."
	if gb > 0 {
		done = fmt.Sprintf("Boxes can record, in up to %d GB.", gb)
	}
	a.settingsDone(w, r, s, a.setRecordingsCap(r.Context(), gb), done)
}

// recordingAction calls off a recording, or deletes one with its file,
// from the Settings page.
func (a *Admin) recordingAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	switch r.PathValue("action") {
	case "cancel":
		rec, err := a.cancelRecording(ctx, r.PathValue("id"))
		done := "Called off " + rec.Title + "."
		if rec.State == store.RecordingRecording {
			done = "Stopped " + rec.Title + ". What it recorded is kept."
		}
		a.settingsDone(w, r, s, err, done)
	case "delete":
		rec, err := a.deleteRecording(ctx, r.PathValue("id"))
		a.settingsDone(w, r, s, err, "Deleted "+rec.Title+".")
	default:
		http.NotFound(w, r)
	}
}

var errNoRecording = badEdit(http.StatusNotFound, "not_found", "There is no such recording.")

// recording returns the recording whose ID is id.
func (a *Admin) recording(ctx context.Context, id string) (store.Recording, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return store.Recording{}, errNoRecording
	}
	rec, err := a.store.RecordingByID(ctx, n)
	if errors.Is(err, store.ErrNotFound) {
		return store.Recording{}, errNoRecording
	}
	return rec, err
}

// cancelRecording calls off a recording to come, or ends one under way,
// which keeps what it has written.
func (a *Admin) cancelRecording(ctx context.Context, id string) (store.Recording, error) {
	rec, err := a.recording(ctx, id)
	if err != nil {
		return rec, err
	}
	switch rec.State {
	case store.RecordingScheduled:
		err = a.store.SetRecordingState(ctx, rec.ID, store.RecordingCancelled, "", "", 0, "")
		if err == nil {
			a.log.Info("a recording called off in the admin", "title", rec.Title, "channel", rec.Channel)
		}
	case store.RecordingRecording:
		if a.recorder != nil {
			a.recorder.Cancel(rec.ID)
		}
		a.log.Info("a recording stopped in the admin, which keeps what it recorded", "title", rec.Title, "channel", rec.Channel)
	default:
		return rec, badEdit(http.StatusConflict, "not_to_come", "It is no longer to come.")
	}
	return rec, err
}

// deleteRecording deletes a recording that is no longer to come, with its
// file.
func (a *Admin) deleteRecording(ctx context.Context, id string) (store.Recording, error) {
	rec, err := a.recording(ctx, id)
	if err != nil {
		return rec, err
	}
	if rec.State == store.RecordingScheduled || rec.State == store.RecordingRecording {
		return rec, badEdit(http.StatusConflict, "to_come", "It is still to come or under way: call it off first.")
	}
	if rec.File != "" && a.recorder != nil {
		// Only a name, in the folder it was made in, else in the recordings
		// folder, where it may have been moved since.
		for _, dir := range recordingDirs(rec, a.recorder.Dir()) {
			err := os.Remove(filepath.Join(dir, filepath.Base(rec.File)))
			if err == nil {
				break
			}
			if !errors.Is(err, os.ErrNotExist) {
				return rec, badEdit(http.StatusInternalServerError, "no_delete", "Its file cannot be deleted: %v", err)
			}
		}
		a.recorder.Changed()
	}
	if err := a.store.DeleteRecording(ctx, rec.ID); err != nil {
		return rec, err
	}
	a.log.Info("a recording deleted in the admin", "title", rec.Title, "channel", rec.Channel)
	return rec, nil
}

// recordingDirs returns where rec's file may be: the folder it was made in,
// then the recordings folder now, dir.
func recordingDirs(rec store.Recording, dir string) []string {
	if rec.Dir == "" || filepath.Clean(rec.Dir) == filepath.Clean(dir) {
		return []string{dir}
	}
	return []string{filepath.Clean(rec.Dir), dir}
}

// apiRecording is a recording, as the API describes it.
type apiRecording struct {
	ID       int64     `json:"id"`
	DeviceID int64     `json:"device_id"`
	EntryID  int64     `json:"entry_id"`
	Channel  string    `json:"channel"`
	Title    string    `json:"title"`
	Start    time.Time `json:"start"`
	Stop     time.Time `json:"stop"`
	// UntilStopped says that it runs until it is stopped, at the latest
	// until Stop (D98).
	UntilStopped bool `json:"until_stopped,omitempty"`
	// Timed says that a box set it by time, to run exactly from Start to
	// Stop (D101).
	Timed bool   `json:"timed,omitempty"`
	State string `json:"state"`
	File  string `json:"file,omitempty"`
	Bytes int64  `json:"bytes"`
	Error string `json:"error,omitempty"`
}

func apiRecordingOf(r store.Recording) apiRecording {
	return apiRecording{ID: r.ID, DeviceID: r.DeviceID, EntryID: r.EntryID, Channel: r.Channel, Title: r.Title,
		Start: r.Start.UTC(), Stop: r.Stop.UTC(), UntilStopped: r.UntilStopped, Timed: r.Timed, State: r.State, File: r.File,
		Bytes: r.Size, Error: r.Error}
}

// apiRecordings lists the recordings to come and under way, and the last
// made, with the room they take.
func (a *Admin) apiRecordings(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	ctx := r.Context()
	upcoming, err := a.store.Recordings(ctx, 0, store.RecordingScheduled, store.RecordingRecording)
	if a.editAnswer(w, err) {
		return
	}
	recent, err := a.store.RecentRecordings(ctx, 100)
	if a.editAnswer(w, err) {
		return
	}
	room, err := a.store.RecordingsCap(ctx)
	if a.editAnswer(w, err) {
		return
	}
	out := struct {
		CapGB     int            `json:"cap_gb"`
		UsedBytes int64          `json:"used_bytes"`
		Upcoming  []apiRecording `json:"upcoming"`
		Recent    []apiRecording `json:"recent"`
	}{CapGB: room, Upcoming: []apiRecording{}, Recent: []apiRecording{}}
	if a.recorder != nil {
		out.UsedBytes = a.recorder.Used()
	}
	for _, rec := range upcoming {
		out.Upcoming = append(out.Upcoming, apiRecordingOf(rec))
	}
	for _, rec := range recent {
		out.Recent = append(out.Recent, apiRecordingOf(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

// apiSchedule asks to record a programme of a channel in the household's
// lineup, named by its start, as a box does from its guide.
func (a *Admin) apiSchedule(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		EntryID int64     `json:"entry_id"`
		Start   time.Time `json:"start"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	rec, err := a.schedule(r.Context(), body.EntryID, body.Start)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, apiRecordingOf(rec))
}

// schedule records a programme of a lineup entry that has not ended.
func (a *Admin) schedule(ctx context.Context, entryID int64, start time.Time) (store.Recording, error) {
	gb, err := a.store.RecordingsCap(ctx)
	if err != nil {
		return store.Recording{}, err
	}
	if gb == 0 || a.recorder == nil {
		return store.Recording{}, badEdit(http.StatusConflict, "recording_off", "Recording is off: give recordings room first.")
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return store.Recording{}, err
	}
	noSuch := badEdit(http.StatusNotFound, "not_found", "The lineup has no such channel, or its guide no programme that starts then.")
	entry, err := a.store.PlayableEntry(ctx, entryID)
	if errors.Is(err, store.ErrNotFound) || err == nil && entry.LineupID != lineup {
		return store.Recording{}, noSuch
	}
	if err != nil {
		return store.Recording{}, err
	}
	p, err := a.store.ProgrammeAt(ctx, lineup, entryID, start)
	if errors.Is(err, store.ErrNotFound) {
		return store.Recording{}, noSuch
	}
	if err != nil {
		return store.Recording{}, err
	}
	if !p.Stop.After(a.now()) {
		return store.Recording{}, badEdit(http.StatusConflict, "programme_ended", "The programme has ended.")
	}
	rec, err := a.store.ScheduleRecording(ctx, store.Recording{AccountID: householdAccount, EntryID: entryID,
		Channel: entry.Name, Title: p.Title, Start: p.Start, Stop: p.Stop, CreatedAt: a.now()})
	if err != nil {
		return rec, err
	}
	if rec.State == store.RecordingDone {
		return rec, badEdit(http.StatusConflict, "recorded", "The programme has been recorded already.")
	}
	a.log.Info("a recording scheduled in the admin", "title", rec.Title, "channel", rec.Channel,
		"start", rec.Start.UTC().Format(time.DateTime))
	return rec, nil
}

func (a *Admin) apiCancelRecording(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	rec, err := a.cancelRecording(r.Context(), r.PathValue("id"))
	if a.editAnswer(w, err) {
		return
	}
	if rec, err = a.store.RecordingByID(r.Context(), rec.ID); a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, apiRecordingOf(rec))
}

func (a *Admin) apiDeleteRecording(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if _, err := a.deleteRecording(r.Context(), r.PathValue("id")); a.editAnswer(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sizeText says a size in megabytes or gigabytes.
func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n > 0:
		return fmt.Sprintf("%d MB", (n+(1<<20)-1)>>20)
	}
	return "0 MB"
}
