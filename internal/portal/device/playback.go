// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/store"
)

// How a film or an episode started on the box, as the page tells it, for
// the log: how long the server took to give its link, how long the box took
// to show it moving, whether the page paused it and played it again since it
// stood still, and the player's events as the box gave them. It changes
// nothing; it is what a box test reads to see why a film did not start.

const (
	// maxReportMs caps the times in a report.
	maxReportMs = 60 * 60 * 1000
	// reportsPerMinute is how many reports a device may send in a minute;
	// the page sends a few for each film.
	reportsPerMinute = 30
)

// playbackReport is the body of POST playback.
type playbackReport struct {
	// Kind is movie or episode, and ID the film's title ID or the
	// episode's ID.
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
	// Outcome is began, not_started, waited (it stood still and went on)
	// or stalled (it stood still and did not).
	Outcome string `json:"outcome"`
	// LinkMs is how long the server took to give the link; MovedMs, how
	// long the box took to show it moving after the play command, -1 if it
	// never did; EventMs, how long the player took to say it started, -1 if
	// it never did; WaitMs, how long it stood still, for waited and stalled.
	LinkMs  int `json:"link_ms"`
	MovedMs int `json:"moved_ms"`
	EventMs int `json:"event_ms"`
	WaitMs  int `json:"wait_ms"`
	// Nudged says that the page paused it and played it again, since it
	// stood still.
	Nudged bool `json:"nudged"`
	// Converted says that it came through ffmpeg (D68).
	Converted bool `json:"converted"`
	// From is where it started, in seconds.
	From int `json:"from"`
	// Events are the player's events since the play command, as the box
	// gave them.
	Events string `json:"events"`
}

// valid reports whether r is a report the page could have sent.
func (r playbackReport) valid() bool {
	switch r.Outcome {
	case "began", "not_started", "waited", "stalled":
	default:
		return false
	}
	return (r.Kind == "movie" || r.Kind == "episode") && r.ID > 0 &&
		r.LinkMs >= 0 && r.LinkMs <= maxReportMs && r.MovedMs >= -1 && r.MovedMs <= maxReportMs &&
		r.EventMs >= -1 && r.EventMs <= maxReportMs && r.WaitMs >= 0 && r.WaitMs <= maxReportMs &&
		r.From >= 0 && r.From <= maxSeconds &&
		len(r.Events) <= maxPlayerEvents && strings.IndexFunc(r.Events, notPrintable) < 0
}

// reportLimiter counts each device's reports in the last minute.
type reportLimiter struct {
	mu   sync.Mutex
	seen map[int64][]time.Time
}

// allow reports whether a device may send another report at now.
func (l *reportLimiter) allow(deviceID int64, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.seen == nil {
		l.seen = map[int64][]time.Time{}
	}
	recent := l.seen[deviceID][:0]
	for _, t := range l.seen[deviceID] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= reportsPerMinute {
		l.seen[deviceID] = recent
		return false
	}
	l.seen[deviceID] = append(recent, now)
	return true
}

// reportPlayback answers POST playback: it logs how a film or an episode
// started on the box, or did not, and answers {}.
func (a *API) reportPlayback(w http.ResponseWriter, r *http.Request, d store.Device) {
	var req playbackReport
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil || !req.valid() {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	if !a.reports.allow(d.ID, a.now()) {
		writeError(w, http.StatusTooManyRequests, errBusy)
		return
	}
	var item store.PlayableItem
	var err error
	if req.Kind == "episode" {
		item, err = a.store.PlayableEpisode(r.Context(), req.ID)
	} else {
		item, err = a.store.PlayableMovie(r.Context(), req.ID)
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a title that played", err)
		return
	}
	args := []any{"title", item.Name, "device", d.ID, "from", req.From, "converted", req.Converted,
		"link_ms", req.LinkMs, "moved_ms", req.MovedMs, "event_ms", req.EventMs, "nudged", req.Nudged,
		"events", req.Events}
	switch req.Outcome {
	case "began":
		msg := "the box began playing a title"
		if req.Nudged {
			msg = "the box began playing a title once the page paused it and played it again"
		}
		a.log.Info(msg, args...)
	case "not_started":
		a.log.Warn("the box did not begin playing a title; the page starts it again", args...)
	case "waited":
		a.log.Info("a title stood still on the box while it played, then went on", append(args, "wait_ms", req.WaitMs)...)
	case "stalled":
		a.log.Warn("a title stood still on the box and did not go on; the page starts it again",
			append(args, "wait_ms", req.WaitMs)...)
	}
	writeJSON(w, http.StatusOK, struct{}{})
}
