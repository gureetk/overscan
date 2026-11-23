// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streaminfo"
)

// Channel health, from what happens as people watch (D134, D138): the
// channels whose streams failed to start the last time that they were
// played, which can be hidden until they play again; what each stream
// holds; and stopping a stream from the overview. Overscan never opens a
// channel to test it.

// stoppedReason is what the log says of a stream stopped in the admin's
// pages.
const stoppedReason = "stopped in the admin"

// healthData is what the page of channels that failed shows.
type healthData struct {
	Channels      []store.FailedChannel
	Total         int // as Channels, but all of them
	Failing, Held int // shown, and hidden until they play again
	Now           time.Time
	CSRF          string
}

// healthPage lists the channels that failed.
func (a *Admin) healthPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showHealth(w, r, s, http.StatusOK, view{})
}

func (a *Admin) showHealth(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	ctx := r.Context()
	d := healthData{Now: a.now(), CSRF: s.CSRFToken}
	var err error
	if d.Channels, d.Total, err = a.store.FailedChannels(ctx); err == nil {
		d.Failing, d.Held, err = a.store.HealthCounts(ctx)
	}
	if err != nil {
		a.log.Error("cannot list the channels that failed", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "Channels that failed", "home", &s, d
	a.render(w, r, status, "health", v)
}

// healthForm hides a channel that failed until it plays again, or shows
// it again; or every one at once.
func (a *Admin) healthForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	var err error
	var notice string
	switch op := r.PostFormValue("op"); op {
	case "hide-all", "show-all":
		var n int
		n, err = a.store.SetFailingHeld(ctx, op == "hide-all")
		if err == nil {
			switch {
			case op == "show-all":
				notice = fmt.Sprintf("Showed %d %s again.", n, plural(n, "channel"))
			case n == 1:
				notice = "Hid 1 channel until it plays again."
			default:
				notice = fmt.Sprintf("Hid %d channels until they play again.", n)
			}
			if n > 0 {
				a.log.Info("channels that failed "+map[bool]string{true: "hidden until they play", false: "shown again"}[op == "hide-all"]+" in the admin", "channels", n)
			}
		}
	case "hide", "show":
		id, perr := strconv.ParseInt(r.PostFormValue("channel"), 10, 64)
		if perr != nil {
			a.notFound(w, r)
			return
		}
		var name string
		name, err = a.holdChannel(ctx, id, op == "hide")
		if err == nil {
			notice = "Hid " + name + " until it plays again."
			if op == "show" {
				notice = "Showed " + name + " again."
			}
		}
	default:
		a.notFound(w, r)
		return
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		a.showHealth(w, r, s, http.StatusNotFound, view{Error: "There is no such channel."})
	case err != nil:
		a.log.Error("cannot hide or show a channel that failed", "error", err)
		a.showHealth(w, r, s, http.StatusInternalServerError, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showHealth(w, r, s, http.StatusOK, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/health", http.StatusSeeOther)
	}
}

// holdChannel hides a channel until it plays again, or shows it again, and
// logs it; it returns the channel's name as its source gives it.
func (a *Admin) holdChannel(ctx context.Context, channelID int64, held bool) (string, error) {
	if err := a.store.SetChannelsHeld(ctx, []int64{channelID}, held); err != nil {
		return "", err
	}
	name, source, err := a.store.ChannelNames(ctx, channelID)
	if err != nil {
		return "", err
	}
	what := "a channel hidden until it plays again in the admin"
	if !held {
		what = "a channel hidden until it played shown again in the admin"
	}
	a.log.Info(what, "channel", name, "source", source)
	return name, nil
}

// entryHealthForm hides a lineup entry's channel until it plays again, or
// shows it again, from its panel.
func (a *Admin) entryHealthForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	lv := parseLineupView(backValues(r))
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		a.notFound(w, r)
		return
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		a.backupsDone(w, r, s, lv, id, err, "")
		return
	}
	e, err := a.store.LineupEntry(ctx, lineup, id)
	if errors.Is(err, store.ErrNotFound) {
		a.notFound(w, r)
		return
	}
	if err != nil {
		a.backupsDone(w, r, s, lv, id, err, "")
		return
	}
	held := r.PostFormValue("held") == "1"
	_, err = a.holdChannel(ctx, e.ChannelID, held)
	notice := "Hid it until it plays again."
	if !held {
		notice = "Showed it again."
	}
	a.backupsDone(w, r, s, lv, id, err, notice)
}

// stopStream ends a stream from the overview.
func (a *Admin) stopStream(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	id := r.PathValue("id")
	rows, err := a.streamRows(r.Context())
	if err != nil {
		a.overviewDone(w, r, s, err, "")
		return
	}
	notice := "That stream had already ended."
	for _, row := range rows {
		switch {
		case row.ID != id:
		case row.DeviceID < 0:
			notice = "A recording is called off on Settings, under Recordings."
		case a.streams.StopStream(id, stoppedReason):
			notice = "Stopped " + row.Channel + " on " + row.Device + "."
		}
	}
	a.overviewDone(w, r, s, nil, notice)
}

// The API.

// apiHealth is what Overscan has seen of a channel's stream, as the API
// describes it.
type apiHealth struct {
	FailedAt *time.Time `json:"failed_at"`
	Failures int        `json:"failures"`
	PlayedAt *time.Time `json:"played_at"`
	// HiddenUntilPlayed hides the channel from devices until its stream
	// plays again.
	HiddenUntilPlayed bool              `json:"hidden_until_played"`
	Stream            *apiStreamDetails `json:"stream"`
}

// apiStreamDetails is what the hub read of a channel's stream.
type apiStreamDetails struct {
	Video        string    `json:"video"`
	Width        int       `json:"width"`
	Height       int       `json:"height"`
	Interlaced   bool      `json:"interlaced"`
	BitDepth     int       `json:"bit_depth"`
	FrameRate    float64   `json:"frame_rate"`
	DynamicRange string    `json:"dynamic_range"`
	Audio        []string  `json:"audio"`
	Summary      string    `json:"summary"`
	SeenAt       time.Time `json:"seen_at"`
}

// healthBody describes a channel's health.
func healthBody(h store.ChannelHealth) apiHealth {
	out := apiHealth{Failures: h.Failures, HiddenUntilPlayed: h.Held}
	if !h.FailedAt.IsZero() {
		at := h.FailedAt.UTC()
		out.FailedAt = &at
	}
	if !h.PlayedAt.IsZero() {
		at := h.PlayedAt.UTC()
		out.PlayedAt = &at
	}
	if !h.SeenAt.IsZero() {
		out.Stream = streamDetailsBody(h.Details, h.SeenAt)
	}
	return out
}

func streamDetailsBody(d streaminfo.Details, seen time.Time) *apiStreamDetails {
	out := &apiStreamDetails{Video: d.Video, Width: d.Width, Height: d.Height, Interlaced: d.Interlaced, BitDepth: d.BitDepth,
		FrameRate: d.FrameRate, DynamicRange: d.DynamicRange, Audio: d.Audio, Summary: d.Summary(), SeenAt: seen.UTC()}
	if out.Audio == nil {
		out.Audio = []string{}
	}
	return out
}

// apiFailedChannel is a channel that failed, as the API describes it.
type apiFailedChannel struct {
	ChannelID int64  `json:"channel_id"`
	Name      string `json:"name"`
	SourceID  int64  `json:"source_id"`
	Source    string `json:"source"`
	apiHealth
}

func (a *Admin) apiHealthList(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	list, total, err := a.store.FailedChannels(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiFailedChannel, len(list))
	for i, c := range list {
		out[i] = apiFailedChannel{ChannelID: c.ChannelID, Name: c.Name, SourceID: c.SourceID, Source: c.Source, apiHealth: healthBody(c.ChannelHealth)}
	}
	writeJSON(w, http.StatusOK, struct {
		Channels []apiFailedChannel `json:"channels"`
		Total    int                `json:"total"`
	}{out, total})
}

// apiHoldBody is the body that hides channels until they play again, or
// shows them again.
type apiHoldBody struct {
	HiddenUntilPlayed *bool `json:"hidden_until_played"`
}

func (a *Admin) readHoldBody(w http.ResponseWriter, r *http.Request) (bool, bool) {
	var body apiHoldBody
	if !readJSON(w, r, &body) {
		return false, false
	}
	if body.HiddenUntilPlayed == nil {
		apiError(w, http.StatusBadRequest, "bad_request", "Give hidden_until_played: true or false.")
		return false, false
	}
	return *body.HiddenUntilPlayed, true
}

func (a *Admin) apiSetHealth(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if a.editAnswer(w, err) {
		return
	}
	held, ok := a.readHoldBody(w, r)
	if !ok {
		return
	}
	_, err = a.holdChannel(r.Context(), id, held)
	if errors.Is(err, store.ErrNotFound) {
		err = badEdit(http.StatusNotFound, "not_found", "There is no such channel.")
	}
	if a.editAnswer(w, err) {
		return
	}
	h, err := a.store.HealthOf(r.Context(), id)
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, healthBody(h))
}

func (a *Admin) apiSetAllHealth(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	held, ok := a.readHoldBody(w, r)
	if !ok {
		return
	}
	n, err := a.store.SetFailingHeld(r.Context(), held)
	if a.editAnswer(w, err) {
		return
	}
	if n > 0 {
		a.log.Info("channels that failed "+map[bool]string{true: "hidden until they play", false: "shown again"}[held]+" through the API", "channels", n)
	}
	writeJSON(w, http.StatusOK, struct {
		Changed int `json:"changed"`
	}{n})
}

func (a *Admin) apiStopStream(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id := r.PathValue("id")
	if a.streams != nil {
		for _, s := range a.streams.Streams() {
			if s.ID == id && s.DeviceID < 0 {
				apiError(w, http.StatusConflict, "recording", "A recording's stream is not stopped: call the recording off with POST recordings/{id}/cancel.")
				return
			}
		}
	}
	if a.streams == nil || !a.streams.StopStream(id, "stopped through the API") {
		apiError(w, http.StatusNotFound, "not_found", "There is no such stream; it may have ended.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
