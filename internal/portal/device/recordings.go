// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Recordings (D89, D98, D101): a box schedules a programme from the guide,
// records a channel now or at a time it sets, sees the household's
// recordings to come and under way, and calls one off or stops it.

// Recorder is what the device API needs of the recorder: starting the
// recordings that are due at once, and ending one under way.
type Recorder interface {
	Cancel(id int64) bool
	Wake()
}

// UseRecorder lets the API schedule recordings, and end those under way;
// without it, boxes cannot record.
func (a *API) UseRecorder(r Recorder) { a.recorder = r }

// recording is a recording as the API shows it. Channel is the lineup
// entry's ID; Start and Stop are Unix seconds.
// UntilStopped says that it runs until it is stopped, at the latest until
// Stop (D98).
// Size is the bytes of the file of one that is done or that failed after
// it began (D100).
// Timed says that a box set it by time, to run exactly from Start to
// Stop (D101).
type recording struct {
	ID           int64  `json:"id"`
	Channel      int64  `json:"channel"`
	Title        string `json:"title"`
	Start        int64  `json:"start"`
	Stop         int64  `json:"stop"`
	UntilStopped bool   `json:"until_stopped,omitempty"`
	Timed        bool   `json:"timed,omitempty"`
	State        string `json:"state"`
	Error        string `json:"error,omitempty"`
	Size         int64  `json:"size,omitempty"`
}

func recordingOf(r store.Recording) recording {
	return recording{ID: r.ID, Channel: r.EntryID, Title: r.Title, Start: r.Start.Unix(), Stop: r.Stop.Unix(),
		UntilStopped: r.UntilStopped, Timed: r.Timed, State: r.State, Error: r.Error, Size: r.Size}
}

// What GET recordings lists of the recordings that ended (D100): those
// that began in the last day, done or failed, at most recentCount of them.
const (
	recentFor   = 24 * time.Hour
	recentCount = 10
)

// recordings answers GET recordings: whether the server records; how a
// recording started on a channel ends for a box that keeps the household's
// setting, and the most hours one that runs until stopped may run (D99);
// the household's recordings that are scheduled or under way, by start;
// and those of the last day that are done or failed, the latest first
// (D100).
func (a *API) recordings(w http.ResponseWriter, r *http.Request, d store.Device) {
	ctx := r.Context()
	gb, err := a.store.RecordingsCap(ctx)
	if err != nil {
		a.internalError(w, "read the recordings' room", err)
		return
	}
	until, err := a.store.RecordUntil(ctx)
	if err != nil {
		a.internalError(w, "read how long recordings run", err)
		return
	}
	hours, err := a.store.RecordingHours(ctx)
	if err != nil {
		a.internalError(w, "read how long recordings run", err)
		return
	}
	list, err := a.store.Recordings(ctx, 0, store.RecordingScheduled, store.RecordingRecording)
	if err != nil {
		a.internalError(w, "list recordings", err)
		return
	}
	ended, err := a.store.EndedRecordings(ctx, d.AccountID, a.now().Add(-recentFor), recentCount)
	if err != nil {
		a.internalError(w, "list recordings", err)
		return
	}
	out := struct {
		On         bool        `json:"on"`
		Until      string      `json:"until"`
		MaxHours   int         `json:"max_hours"`
		Recordings []recording `json:"recordings"`
		Recent     []recording `json:"recent"`
	}{On: gb > 0 && a.recorder != nil, Until: until, MaxHours: hours, Recordings: []recording{}, Recent: []recording{}}
	for _, rec := range list {
		if rec.AccountID == d.AccountID {
			out.Recordings = append(out.Recordings, recordingOf(rec))
		}
	}
	for _, rec := range ended {
		out.Recent = append(out.Recent, recordingOf(rec))
	}
	writeJSON(w, http.StatusOK, out)
}

// maxUpcoming is the most recordings to come or under way that a household
// may have.
const maxUpcoming = 100

// recordRequest is the body of POST recordings: the channel, and when the
// programme begins, as the guide gives it; or now, to record the channel
// from now on (D98); or a start and a stop, to record it at a time (D101);
// with the parental PIN for a locked channel (D55).
type recordRequest struct {
	Channel int64  `json:"channel"`
	Start   int64  `json:"start"`
	Stop    int64  `json:"stop"`
	Now     bool   `json:"now"`
	PIN     string `json:"pin"`
}

// Limits of a recording set by time (D101): it begins within a week, and
// runs for at least a minute and at most the most hours any recording may.
const (
	timedAhead = 7 * 24 * time.Hour
	timedLeast = time.Minute
)

// record answers POST recordings: it schedules a programme that has not
// ended, which the server records from a minute before its start to two
// minutes after its end; or it records a channel from now on, until the
// end of the programme that the guide shows on it or until it is stopped
// (D98); or at a time that the box set (D101).
func (a *API) record(w http.ResponseWriter, r *http.Request, d store.Device) {
	var req recordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Channel <= 0 ||
		!req.Now && (req.Start <= 0 || req.Start > 1<<40) || req.Stop < 0 || req.Stop > 1<<40 {
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
	// A recording plays the channel as the account does, from the sources
	// that it uses (D114).
	if _, ok := acct.Sources.For(entry); !ok {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if req.Now {
		a.recordNow(w, r, d, acct, entry, req)
		return
	}
	if req.Stop > 0 {
		a.recordTimed(w, r, d, acct, entry, req)
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
	now := a.now()
	if !p.Start.After(now) {
		// A programme on now that the channel already records.
		if rec, err := a.store.ActiveRecording(ctx, req.Channel, now); err == nil {
			writeJSON(w, http.StatusOK, recordingOf(rec))
			return
		} else if !errors.Is(err, store.ErrNotFound) {
			a.internalError(w, "look up a recording", err)
			return
		}
	}
	if !a.roomForOneMore(w, ctx, acct.ID) {
		return
	}
	rec, err := a.store.ScheduleRecording(ctx, store.Recording{AccountID: acct.ID, DeviceID: d.ID, EntryID: req.Channel,
		Channel: entry.Name, Title: p.Title, Start: p.Start, Stop: p.Stop, CreatedAt: now})
	if err != nil {
		a.internalError(w, "schedule a recording", err)
		return
	}
	if rec.State == store.RecordingDone {
		// Stopped while it recorded, which kept what it had written: the
		// rest of a programme on now is a recording of its own, from now.
		if p.Start.After(now) {
			writeError(w, http.StatusConflict, errRecorded)
			return
		}
		rec, err = a.store.ScheduleRecording(ctx, store.Recording{AccountID: acct.ID, DeviceID: d.ID, EntryID: req.Channel,
			Channel: entry.Name, Title: p.Title, Start: now, Stop: p.Stop, CreatedAt: now})
		if err != nil {
			a.internalError(w, "schedule a recording", err)
			return
		}
	}
	if a.recorder != nil && !rec.Start.After(now) {
		a.recorder.Wake()
	}
	a.log.Info("a box scheduled a recording", "title", rec.Title, "channel", rec.Channel,
		"start", rec.Start.UTC().Format(time.DateTime), "device", d.ID)
	writeJSON(w, http.StatusOK, recordingOf(rec))
}

// roomForOneMore checks that a household may have one more recording to
// come or under way, and otherwise answers so.
func (a *API) roomForOneMore(w http.ResponseWriter, ctx context.Context, accountID int64) bool {
	upcoming, err := a.store.UpcomingRecordings(ctx, accountID)
	if err != nil {
		a.internalError(w, "count recordings", err)
		return false
	}
	if upcoming >= maxUpcoming {
		writeError(w, http.StatusConflict, errTooMany)
		return false
	}
	return true
}

// recordUntil returns how a recording that box d starts on a channel ends,
// store.RecordUntilProgramme or store.RecordUntilStopped: as the box chose,
// or as the household's setting says for a box that keeps it (D99).
func (a *API) recordUntil(r *http.Request, d store.Device) (string, error) {
	s, err := a.deviceSettings(r, d)
	if err != nil {
		return "", err
	}
	if s.Record == store.RecordUntilProgramme || s.Record == store.RecordUntilStopped {
		return s.Record, nil
	}
	return a.store.RecordUntil(r.Context())
}

// recordNow records a channel from now on (D98): until the end of the
// programme that the guide shows on it, unless the box, or the household
// for a box that keeps its setting, records until stopped (D99); with no
// programme, until it is stopped. One that runs until stopped ends after
// the household's most hours. A channel that records already gives the
// recording it has.
func (a *API) recordNow(w http.ResponseWriter, r *http.Request, d store.Device, acct store.Account, entry store.PlayableEntry, req recordRequest) {
	ctx := r.Context()
	if entry.Locked && acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
		return
	}
	now := a.now()
	if rec, err := a.store.ActiveRecording(ctx, req.Channel, now); err == nil {
		writeJSON(w, http.StatusOK, recordingOf(rec))
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.internalError(w, "look up a recording", err)
		return
	}
	if !a.roomForOneMore(w, ctx, acct.ID) {
		return
	}
	until, err := a.recordUntil(r, d)
	if err != nil {
		a.internalError(w, "read how long recordings run", err)
		return
	}
	hours, err := a.store.RecordingHours(ctx)
	if err != nil {
		a.internalError(w, "read how long recordings run", err)
		return
	}
	rec := store.Recording{AccountID: acct.ID, DeviceID: d.ID, EntryID: req.Channel, Channel: entry.Name, Title: entry.Name,
		Start: now, Stop: now.Add(time.Duration(hours) * time.Hour), UntilStopped: true, CreatedAt: now}
	ps, err := a.store.EntryProgrammes(ctx, acct.LineupID, req.Channel, now, now.Add(time.Second))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.internalError(w, "look up a programme", err)
		return
	}
	if len(ps) > 0 && ps[0].Stop.After(now) {
		rec.Title = ps[0].Title
		if until == store.RecordUntilProgramme {
			rec.Stop, rec.UntilStopped = ps[0].Stop, false
		}
	}
	rec, err = a.store.ScheduleRecording(ctx, rec)
	if err != nil {
		a.internalError(w, "start a recording", err)
		return
	}
	if a.recorder != nil {
		a.recorder.Wake()
	}
	a.log.Info("a box started a recording", "title", rec.Title, "channel", rec.Channel,
		"until", rec.Stop.UTC().Format(time.DateTime), "until_stopped", rec.UntilStopped, "device", d.ID)
	writeJSON(w, http.StatusOK, recordingOf(rec))
}

// recordTimed records a channel at a time that the box set (D101): exactly
// from its start, or from now if that has passed, to its stop. It takes the
// title of a programme that the guide shows for at least half of that time,
// or else the channel's name. A time that overlaps a recording of the
// channel to come or under way is refused.
func (a *API) recordTimed(w http.ResponseWriter, r *http.Request, d store.Device, acct store.Account, entry store.PlayableEntry, req recordRequest) {
	ctx := r.Context()
	now := a.now()
	start, stop := time.Unix(req.Start, 0), time.Unix(req.Stop, 0)
	if !stop.After(now) {
		writeError(w, http.StatusConflict, errEnded)
		return
	}
	if start.Before(now) {
		start = now
	}
	if length := stop.Sub(start); length < timedLeast || length > time.Duration(store.MaxRecordingHours)*time.Hour ||
		start.Sub(now) > timedAhead {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	if entry.Locked && acct.PINHash != "" && !a.pinOK(w, r, d, acct, req.PIN) {
		return
	}
	if _, err := a.store.OverlappingRecording(ctx, req.Channel, start, stop); err == nil {
		writeError(w, http.StatusConflict, errOverlaps)
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		a.internalError(w, "look up a recording", err)
		return
	}
	if !a.roomForOneMore(w, ctx, acct.ID) {
		return
	}
	title := entry.Name
	ps, err := a.store.EntryProgrammes(ctx, acct.LineupID, req.Channel, start, stop)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.internalError(w, "look up a programme", err)
		return
	}
	for _, p := range ps {
		from, to := p.Start, p.Stop
		if from.Before(start) {
			from = start
		}
		if to.After(stop) {
			to = stop
		}
		if 2*to.Sub(from) >= stop.Sub(start) {
			title = p.Title
			break
		}
	}
	rec, err := a.store.ScheduleRecording(ctx, store.Recording{AccountID: acct.ID, DeviceID: d.ID, EntryID: req.Channel,
		Channel: entry.Name, Title: title, Start: start, Stop: stop, Timed: true, CreatedAt: now})
	if err != nil {
		a.internalError(w, "schedule a recording", err)
		return
	}
	if rec.State != store.RecordingScheduled {
		// One that began at the same second and is done already.
		writeError(w, http.StatusConflict, errRecorded)
		return
	}
	if a.recorder != nil && !start.After(now) {
		a.recorder.Wake()
	}
	a.log.Info("a box set a recording by time", "title", rec.Title, "channel", rec.Channel,
		"start", rec.Start.UTC().Format(time.DateTime), "stop", rec.Stop.UTC().Format(time.DateTime), "device", d.ID)
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
		if err := a.store.SetRecordingState(ctx, id, store.RecordingCancelled, "", "", 0, ""); err != nil {
			a.internalError(w, "call off a recording", err)
			return
		}
		rec.State = store.RecordingCancelled
		a.log.Info("a box called off a recording", "title", rec.Title, "channel", rec.Channel, "device", d.ID)
	case store.RecordingRecording:
		if a.recorder != nil {
			a.recorder.Cancel(id)
		}
		a.log.Info("a box stopped a recording, which keeps what it recorded", "title", rec.Title, "channel", rec.Channel,
			"device", d.ID)
	}
	writeJSON(w, http.StatusOK, recordingOf(rec))
}
