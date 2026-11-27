// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
)

// The overview, what is playing, and the capture log of requests that
// Overscan does not support yet (D13).

// Streams lists what is playing, and says whether it can repair streams,
// convert films and episodes that a box cannot play, and transcode their
// video, and stops what players play; the stream hub does.
type Streams interface {
	Streams() []hub.Stream
	CanRepair() bool
	CanConvert() bool
	Encoder() hub.Encoder
	// StopPlayers ends the streams of the players that match (D104).
	StopPlayers(match func(hub.Player) bool, reason string) int
	// StopSources ends what an account plays from sources it no longer
	// uses (D114).
	StopSources(accountID int64, u store.Sources, reason string) int
	// StopStream ends the stream that Stream.ID names, and reports
	// whether there was one (D138).
	StopStream(id, reason string) bool
	// StopCut ends the HLS that the hub cuts for an account's players
	// (D153).
	StopCut(accountID int64, reason string) int
}

// UseStreams gives the admin what lists the streams playing. Without it, the
// overview shows none, and the Lineup page says that streams cannot be
// repaired.
func (a *Admin) UseStreams(s Streams) { a.streams = s }

// canRepair reports whether channels set to be repaired go through ffmpeg.
func (a *Admin) canRepair() bool { return a.streams != nil && a.streams.CanRepair() }

// transcoder says whether this server's ffmpeg can transcode the video of
// films and episodes that a box cannot play (D73), and map a high dynamic
// range to the standard one; if it cannot, missing names what it lacks.
func (a *Admin) transcoder() (can, toneMap bool, missing string) {
	switch {
	case a.streams == nil || !a.streams.CanConvert():
		return false, false, "ffmpeg and ffprobe"
	case !a.streams.Encoder().H264:
		return false, false, "libx264"
	}
	return true, a.streams.Encoder().ToneMap, ""
}

// deviceNames returns each device's name, its label or else its MAC
// address, by ID.
func (a *Admin) deviceNames(ctx context.Context) (map[int64]string, error) {
	devices, err := a.store.Devices(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(devices))
	for _, d := range devices {
		names[d.ID] = d.MAC
		if d.Label != "" {
			names[d.ID] = d.Label
		}
	}
	return names, nil
}

// streamRow is a stream as the overview shows it.
type streamRow struct {
	hub.Stream
	Device  string
	Model   string // the device's model, "" if unknown
	Account string // the account's name, while there is more than one (D102)
	Number  int    // the channel's number in the lineup, 0 if it has none
	Now     time.Time
}

// Since says how long ago it started.
func (r streamRow) Since() string { return ago(r.Started, r.Now) }

// SpeedText says how fast a live channel's transcode goes, such as
// "1.00x", "" until known (D147).
func (r streamRow) SpeedText() string {
	if r.Speed <= 0 {
		return ""
	}
	return fmt.Sprintf("%.2fx", r.Speed)
}

// Received says how much it has sent.
func (r streamRow) Received() string { return amount(r.Bytes) }

// amount writes a number of bytes as people read it.
func amount(n int64) string {
	const mb = 1 << 20
	switch {
	case n < 1<<10:
		return fmt.Sprintf("%d bytes", n)
	case n < mb:
		return fmt.Sprintf("%d KB", n>>10)
	case n < 10*mb:
		return fmt.Sprintf("%.1f MB", float64(n)/mb)
	case n < 1<<30:
		return fmt.Sprintf("%d MB", n/mb)
	}
	return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
}

// streamRows returns what is playing, with the devices' names and models
// and the channels' numbers.
func (a *Admin) streamRows(ctx context.Context) ([]streamRow, error) {
	if a.streams == nil {
		return nil, nil
	}
	devices, err := a.store.Devices(ctx)
	if err != nil {
		return nil, err
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return nil, err
	}
	return a.streamRowsOf(ctx, devices, lineup)
}

// streamRowsOf is streamRows with the devices and the lineup already read.
func (a *Admin) streamRowsOf(ctx context.Context, devices []store.Device, lineup int64) ([]streamRow, error) {
	if a.streams == nil {
		return nil, nil
	}
	streams := a.streams.Streams()
	if len(streams) == 0 {
		return nil, nil
	}
	byID := make(map[int64]store.Device, len(devices))
	for _, d := range devices {
		byID[d.ID] = d
	}
	accounts, err := a.store.Accounts(ctx)
	if err != nil {
		return nil, err
	}
	accountNames := map[int64]string{}
	lineupOf := map[int64]int64{}
	for _, acct := range accounts {
		lineupOf[acct.ID] = acct.LineupID
		if len(accounts) > 1 {
			accountNames[acct.ID] = acct.Name
		}
	}
	now := a.now()
	rows := make([]streamRow, len(streams))
	for i, s := range streams {
		rows[i] = streamRow{Stream: s, Device: "a removed device", Account: accountNames[s.AccountID], Now: now}
		switch {
		case s.DeviceID == 0:
			rows[i].Device = "the debug playlist" // which plays as no device
		case s.Player != "":
			// A player that an output serves, as the account (D103).
			rows[i].Device, rows[i].Model = s.Player, "A player"
		case s.DeviceID < 0:
			// A recording plays as a device of its own (D89).
			rows[i].Device = "a recording on this server"
			if rec, err := a.store.RecordingByID(ctx, -s.DeviceID); err == nil {
				rows[i].Model = rec.Title
			}
		}
		if d, ok := byID[s.DeviceID]; ok {
			rows[i].Device, rows[i].Model = d.MAC, d.Model
			if d.Label != "" {
				rows[i].Device = d.Label
			}
		}
		if s.Kind == hub.PlaysMovie || s.Kind == hub.PlaysEpisode {
			continue // which has no number
		}
		// The channel's number in the lineup that the stream's account
		// sees (D106), or the household's.
		in := lineup
		if id, ok := lineupOf[s.AccountID]; ok {
			in = id
		}
		e, err := a.store.LineupEntry(ctx, in, s.EntryID)
		switch {
		case err == nil:
			rows[i].Number = e.Number
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		}
	}
	return rows, nil
}

// overviewData is what the overview shows.
type overviewData struct {
	Streams                    []streamRow
	Sources                    []sourceRow
	Waiting                    []deviceRow // pending, or asking to pair again
	Approved, Pending, Blocked int
	Shown, Channels, Groups    int
	Captured                   int // kinds of request in the capture log
	// Failing counts the channels that failed to start the last time that
	// they were played, and Held those hidden until they play again (D138).
	Failing, Held            int
	PINSet                   bool // the household has a parental PIN (D55)
	Locks                    int  // locked groups, and channels locked one by one
	RepairDefault, CanRepair bool // every channel is repaired; there is an ffmpeg to do it
	// Ended is when the household's account ended, if it has (D95).
	Ended        time.Time
	Started, Now time.Time
}

// Repaired counts the streams that go through ffmpeg.
func (d overviewData) Repaired() int {
	n := 0
	for _, s := range d.Streams {
		if s.Repaired {
			n++
		}
	}
	return n
}

// FromHLS counts the streams whose source sends HLS.
func (d overviewData) FromHLS() int {
	n := 0
	for _, s := range d.Streams {
		if s.HLS {
			n++
		}
	}
	return n
}

// SourcesOff counts the sources that are turned off.
func (d overviewData) SourcesOff() int {
	n := 0
	for _, s := range d.Sources {
		if !s.Enabled {
			n++
		}
	}
	return n
}

// SourceTroubles lists the sources that are on but cannot be read as they
// are: their address cannot be read, there is none, or the last read failed.
func (d overviewData) SourceTroubles() []sourceRow {
	var out []sourceRow
	for _, s := range d.Sources {
		if s.Enabled && (s.AddressLocked() || s.AddressMissing() || s.RefreshError != "") {
			out = append(out, s)
		}
	}
	return out
}

// LocksWait reports whether the lineup has locks but no PIN, so that the
// locks do nothing.
func (d overviewData) LocksWait() bool { return d.Locks > 0 && !d.PINSet }

// RepairWaits reports whether every channel is to be repaired but there is
// no ffmpeg to do it.
func (d overviewData) RepairWaits() bool { return d.RepairDefault && !d.CanRepair }

// ToDo counts what the overview's To do list holds.
func (d overviewData) ToDo() int {
	n := len(d.Waiting) + len(d.SourceTroubles())
	for _, b := range []bool{d.LocksWait(), d.RepairWaits(), len(d.Sources) == 0, !d.Ended.IsZero()} {
		if b {
			n++
		}
	}
	return n
}

// Uptime says how long the server has run.
func (d overviewData) Uptime() string { return uptime(d.Started, d.Now) }

// uptime says how long it is from started to now, roughly.
func uptime(started, now time.Time) string {
	t := now.Sub(started)
	switch {
	case t < time.Minute:
		return "less than a minute"
	case t < time.Hour:
		n := int(t / time.Minute)
		return fmt.Sprintf("%d %s", n, plural(n, "minute"))
	case t < 48*time.Hour:
		n := int(t / time.Hour)
		return fmt.Sprintf("%d %s", n, plural(n, "hour"))
	}
	return fmt.Sprintf("%d days", int(t/(24*time.Hour)))
}

func (a *Admin) overview(ctx context.Context) (overviewData, error) {
	d := overviewData{Started: a.started, Now: a.now(), CanRepair: a.canRepair()}
	devices, err := a.store.Devices(ctx)
	if err != nil {
		return d, err
	}
	for _, dev := range devices {
		switch dev.State {
		case store.DeviceApproved:
			d.Approved++
		case store.DevicePending:
			d.Pending++
		case store.DeviceBlocked:
			d.Blocked++
		}
		if dev.State == store.DevicePending || dev.PairingCode != "" {
			d.Waiting = append(d.Waiting, deviceRow{Device: dev})
		}
	}
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return d, err
	}
	if d.Streams, err = a.streamRowsOf(ctx, devices, lineup); err != nil {
		return d, err
	}
	srcs, err := a.store.Sources(ctx)
	if err != nil {
		return d, err
	}
	for _, src := range srcs {
		d.Sources = append(d.Sources, sourceRow{Source: src, Now: d.Now})
	}
	groups, err := a.store.LineupGroups(ctx, lineup)
	if err != nil {
		return d, err
	}
	for _, g := range groups {
		d.Shown += g.Shown
		d.Channels += g.Channels
		if g.ID != 0 {
			d.Groups++
		}
	}
	captures, err := a.store.Captures(ctx)
	if err != nil {
		return d, err
	}
	d.Captured = len(captures)
	if d.Failing, d.Held, err = a.store.HealthCounts(ctx); err != nil {
		return d, err
	}
	if d.PINSet, err = a.pinSet(ctx); err != nil {
		return d, err
	}
	if d.Locks, err = a.lockCount(ctx); err != nil {
		return d, err
	}
	if d.RepairDefault, err = a.store.RepairDefault(ctx); err != nil {
		return d, err
	}
	acct, err := a.store.Account(ctx, householdAccount)
	if err != nil {
		return d, err
	}
	if acct.Ended(d.Now) {
		d.Ended = acct.ExpiresAt
	}
	return d, nil
}

// home is the admin's first page: what needs doing, what is playing, and
// how things stand.
func (a *Admin) home(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showOverview(w, r, s, http.StatusOK, view{})
}

// showOverview renders the overview with v's alerts.
func (a *Admin) showOverview(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	d, err := a.overview(r.Context())
	if err != nil {
		a.log.Error("cannot show the overview", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "Overview", "home", &s, d
	a.render(w, r, status, "home", v)
}

// overviewDone answers an action taken from the overview, such as
// approving a box: the overview again with what happened, or for a plain
// form, a move back to it.
func (a *Admin) overviewDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string) {
	var bad *deviceError
	switch {
	case errors.As(err, &bad):
		a.showOverview(w, r, s, bad.status, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change a device", "error", err)
		a.showOverview(w, r, s, http.StatusInternalServerError, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showOverview(w, r, s, http.StatusOK, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/", http.StatusSeeOther)
	}
}

// captureRow is a kind of request in the capture log, as the page shows it.
type captureRow struct {
	store.Capture
	Device string
	Now    time.Time
}

// Params lists the request's parameter names.
func (r captureRow) ParamList() []string {
	if r.Params == "" {
		return nil
	}
	return strings.Split(r.Params, ",")
}

// LastSeenAgo says when it was last asked for.
func (r captureRow) LastSeenAgo() string { return ago(r.LastSeen, r.Now) }

// FirstSeenAgo says when it was first asked for.
func (r captureRow) FirstSeenAgo() string { return ago(r.FirstSeen, r.Now) }

func (a *Admin) captureRows(ctx context.Context) ([]captureRow, error) {
	captures, err := a.store.Captures(ctx)
	if err != nil {
		return nil, err
	}
	names, err := a.deviceNames(ctx)
	if err != nil {
		return nil, err
	}
	now := a.now()
	rows := make([]captureRow, len(captures))
	for i, c := range captures {
		rows[i] = captureRow{Capture: c, Device: names[c.DeviceID], Now: now}
	}
	return rows, nil
}

// capturePage shows the capture log.
func (a *Admin) capturePage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showCapture(w, r, s, http.StatusOK, view{})
}

func (a *Admin) showCapture(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	rows, err := a.captureRows(r.Context())
	if err != nil {
		a.log.Error("cannot show the capture log", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "Capture log", "capture", &s, rows
	a.render(w, r, status, "capture", v)
}

// clearCapture empties the capture log.
func (a *Admin) clearCapture(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	n, err := a.store.ClearCaptures(r.Context())
	if err != nil {
		a.log.Error("cannot clear the capture log", "error", err)
		a.showCapture(w, r, s, http.StatusInternalServerError, view{Error: "Something went wrong; the server's log says what."})
		return
	}
	a.log.Info("capture log cleared in the admin", "kinds", n)
	if r.Header.Get("HX-Request") != "true" {
		http.Redirect(w, r, "/admin/capture", http.StatusSeeOther)
		return
	}
	a.showCapture(w, r, s, http.StatusOK, view{Notice: fmt.Sprintf("Cleared the log of %d %s of request.", n, plural(n, "kind"))})
}

// The API.

// apiStream is a stream as the API describes it.
type apiStream struct {
	// ID names the stream, to stop it (D138).
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	DeviceID  int64     `json:"device_id"`
	Device    string    `json:"device"`
	AccountID int64     `json:"account_id"` // 0 for none (D102)
	EntryID   int64     `json:"entry_id"`
	ItemID    int64     `json:"item_id"`
	Channel   string    `json:"channel"`
	SourceID  int64     `json:"source_id"`
	Source    string    `json:"source"`
	StartedAt time.Time `json:"started_at"`
	Bytes     int64     `json:"bytes"`
	HLS       bool      `json:"hls"`
	Repaired  bool      `json:"repaired"`
	Converted bool      `json:"converted"`
	// Transcoded is a film or an episode whose video is transcoded, by its
	// source's server (D71) or by this server's ffmpeg (D73), as
	// TranscodedBy says: "source" or "overscan".
	Transcoded   bool   `json:"transcoded"`
	TranscodedBy string `json:"transcoded_by,omitempty"`
	Playing      bool   `json:"playing"`
	// Timeshift is a box's channel that plays through a timeshift buffer
	// (D92).
	Timeshift bool `json:"timeshift,omitempty"`
	// Shared is a channel that plays from a shared tuner, whose one
	// connection to the source serves every screen on it (D93).
	Shared bool `json:"shared,omitempty"`
	// HLSOut is a channel that a player gets as HLS that the hub cuts
	// (D153).
	HLSOut bool `json:"hls_out,omitempty"`
	// Programme and AiredAt are, for a past programme of the channel that
	// plays from its source's archive (D81), its title and when it began.
	Programme string     `json:"programme,omitempty"`
	AiredAt   *time.Time `json:"aired_at,omitempty"`
	// Details names what a channel's stream holds, as the hub read it
	// passing through, such as "1080i H.264, AC-3" (D138); "" until known.
	Details string `json:"details"`
	// Profile names the profile that a live channel is transcoded with,
	// "" if it is not; Speed is how fast the transcode went lately, in
	// seconds of video a second, 0 until known; Behind says that it fell
	// behind its stream, which stutters on the screen (D147).
	Profile string  `json:"profile,omitempty"`
	Speed   float64 `json:"speed,omitempty"`
	Behind  bool    `json:"behind,omitempty"`
	// Graphics says that the server's graphics transcode it (D148).
	Graphics bool `json:"graphics,omitempty"`
}

func (a *Admin) apiStreams(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	rows, err := a.streamRows(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiStream, len(rows))
	for i, s := range rows {
		out[i] = apiStream{ID: s.ID, Details: s.Details, Kind: s.Kind, DeviceID: s.DeviceID, Device: s.Device, AccountID: s.AccountID, EntryID: s.EntryID, ItemID: s.ItemID,
			Channel: s.Channel, SourceID: s.SourceID, Source: s.Source, StartedAt: s.Started.UTC().Truncate(time.Second),
			Bytes: s.Bytes, HLS: s.HLS, Repaired: s.Repaired, Converted: s.Converted, Transcoded: s.Transcoded,
			TranscodedBy: s.TranscodedBy, Playing: s.Playing, Timeshift: s.Timeshift, Shared: s.Shared, HLSOut: s.Cut, Programme: s.Programme,
			Profile: s.Profile, Speed: s.Speed, Behind: s.Behind, Graphics: s.Graphics}
		if !s.Aired.IsZero() {
			aired := s.Aired.UTC()
			out[i].AiredAt = &aired
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Streams []apiStream `json:"streams"`
	}{out})
}

// apiCapture is a kind of request in the capture log, as the API describes
// it.
type apiCapture struct {
	Module    string    `json:"module"`
	Action    string    `json:"action"`
	Params    []string  `json:"params"`
	Example   string    `json:"example"`
	UserAgent string    `json:"user_agent"`
	DeviceID  *int64    `json:"device_id"`
	Count     int64     `json:"count"`
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`
}

func (a *Admin) apiCaptures(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	rows, err := a.captureRows(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	out := make([]apiCapture, len(rows))
	for i, c := range rows {
		out[i] = apiCapture{Module: c.Module, Action: c.Action, Params: c.ParamList(), Example: c.Example, UserAgent: c.UserAgent,
			Count: c.Count, FirstSeen: c.FirstSeen.UTC(), LastSeen: c.LastSeen.UTC()}
		if out[i].Params == nil {
			out[i].Params = []string{}
		}
		if c.DeviceID != 0 {
			id := c.DeviceID
			out[i].DeviceID = &id
		}
	}
	writeJSON(w, http.StatusOK, struct {
		Requests []apiCapture `json:"requests"`
	}{out})
}

func (a *Admin) apiClearCaptures(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	n, err := a.store.ClearCaptures(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	a.log.Info("capture log cleared through the API", "kinds", n)
	w.WriteHeader(http.StatusNoContent)
}
