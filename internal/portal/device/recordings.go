// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Recordings (D89): a box schedules a programme from the guide, sees the
// household's recordings to come, and calls one off.

// Recorder is what the device API needs of the recorder: ending a
// recording under way.
type Recorder interface {
	Cancel(id int64) bool
}

// UseRecorder lets the API schedule recordings, and end those under way;
// without it, boxes cannot record.
func (a *API) UseRecorder(r Recorder) { a.recorder = r }

// recording is a recording as the API shows it. Channel is the lineup
// entry's ID; Start and Stop are Unix seconds.
type recording struct {
	ID      int64  `json:"id"`
	Channel int64  `json:"channel"`
	Title   string `json:"title"`
	Start   int64  `json:"start"`
	Stop    int64  `json:"stop"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
}

func recordingOf(r store.Recording) recording {
	return recording{ID: r.ID, Channel: r.EntryID, Title: r.Title, Start: r.Start.Unix(), Stop: r.Stop.Unix(),
		State: r.State, Error: r.Error}
}

// recordings answers GET recordings: whether the server records, and the
// household's recordings that are scheduled or under way, by start.
func (a *API) recordings(w http.ResponseWriter, r *http.Request, d store.Device) {
	ctx := r.Context()
	gb, err := a.store.RecordingsCap(ctx)
	if err != nil {
		a.internalError(w, "read the recordings' room", err)
		return
	}
	list, err := a.store.Recordings(ctx, 0, store.RecordingScheduled, store.RecordingRecording)
	if err != nil {
		a.internalError(w, "list recordings", err)
		return
	}
	out := struct {
		On         bool        `json:"on"`
		Recordings []recording `json:"recordings"`
	}{On: gb > 0 && a.recorder != nil, Recordings: []recording{}}
	for _, rec := range list {
		if rec.AccountID == d.AccountID {
			out.Recordings = append(out.Recordings, recordingOf(rec))
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// recordRequest is the body of POST recordings: the channel, and when the
// programme begins, as the guide gives it, with the parental PIN for a
// locked channel (D55).
type recordRequest struct {
	Channel int64  `json:"channel"`
	Start   int64  `json:"start"`
	PIN     string `json:"pin"`
}

// record answers POST recordings: it schedules a programme that has not
// ended, which the server records from a minute before its start to two
// minutes after its end.
func (a *API) record(w http.ResponseWriter, r *http.Request, d store.Device) {
	var req recordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Channel <= 0 || req.Start <= 0 || req.Start > 1<<40 {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	ctx := r.Context()
	gb, err := a.store.RecordingsCap(ctx)
	if err != nil {
		a.internalError(w, "read the recordings' room", err)
		return
	}
	if gb == 0 || a.recorder == nil {
		writeError(w, http.StatusConflict, errRecordingOff)
		return
	}
	acct, err := a.store.Account(ctx, d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	entry, err := a.store.PlayableEntry(ctx, req.Channel)
	if errors.Is(err, store.ErrNotFound) || err == nil && entry.LineupID != acct.LineupID {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a channel", err)
		return
	}
	p, err := a.store.ProgrammeAt(ctx, acct.LineupID, req.Channel, time.Unix(req.Start, 0))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a programme", err)
		return
	}
	if !p.Stop.After(a.now()) {
		writeError(w, http.StatusConflict, errEnded)
		return
	}
	if entry.Locked && acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
		return
	}
	rec, err := a.store.ScheduleRecording(ctx, store.Recording{AccountID: acct.ID, DeviceID: d.ID, EntryID: req.Channel,
		Channel: entry.Name, Title: p.Title, Start: p.Start, Stop: p.Stop, CreatedAt: a.now()})
	if err != nil {
		a.internalError(w, "schedule a recording", err)
		return
	}
	if rec.State == store.RecordingDone {
		// Called off while it recorded, which kept what it had written.
		writeError(w, http.StatusConflict, errRecorded)
		return
	}
	a.log.Info("a box scheduled a recording", "title", rec.Title, "channel", rec.Channel,
		"start", rec.Start.UTC().Format(time.DateTime), "device", d.ID)
	writeJSON(w, http.StatusOK, recordingOf(rec))
}

// unrecord answers DELETE recordings/{id}: a scheduled recording of the
// household's is called off, and one under way ends now, keeping what it
// wrote.
func (a *API) unrecord(w http.ResponseWriter, r *http.Request, d store.Device) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	ctx := r.Context()
	rec, err := a.store.RecordingByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && rec.AccountID != d.AccountID {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a recording", err)
		return
	}
	switch rec.State {
	case store.RecordingScheduled:
		if err := a.store.SetRecordingState(ctx, id, store.RecordingCancelled, "", 0, ""); err != nil {
			a.internalError(w, "call off a recording", err)
			return
		}
		rec.State = store.RecordingCancelled
	case store.RecordingRecording:
		if a.recorder != nil {
			a.recorder.Cancel(id)
		}
	}
	a.log.Info("a box called off a recording", "title", rec.Title, "channel", rec.Channel, "device", d.ID)
	writeJSON(w, http.StatusOK, recordingOf(rec))
}
