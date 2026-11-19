// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tmdb"
)

// The Settings page gathers what applies to the whole server: stream
// repair's default and whether it reaches HLS, whether films are converted
// and transcoded and how many at once ffmpeg may transcode (D78), the
// parental PIN and what sources' marks for adults do (D86), where boxes
// show radio stations (D87), TMDB's key for the titles in folders (D88),
// recordings (D89), the administrator's account, and facts about the
// server, its backups and the support bundle (D59).

// settingsData is what the Settings page shows.
type settingsData struct {
	RepairDefault    bool              // channels are repaired unless they say otherwise (D58)
	RepairHLS        bool              // repaired channels whose address is HLS are made one stream (D61)
	CanRepair        bool              // there is an ffmpeg to repair them with (D57)
	FFmpeg           string            // which ffmpeg, as the server found it
	ConvertSound     bool              // films whose sound or file a box cannot play go through ffmpeg (D68, D78)
	ServerTranscodes bool              // films whose video a box cannot play go through their source's server (D71, D78)
	Transcodes       int               // films whose video ffmpeg may transcode at a time, 0 for none (D73)
	CanConvert       bool              // there are ffmpeg and ffprobe to convert films with (D68)
	CanTranscode     bool              // ffmpeg can transcode them
	ToneMap          bool              // and map a high dynamic range to the standard one
	Missing          string            // what ffmpeg lacks to transcode, if it cannot
	PINSet           bool              // the household has a parental PIN (D55)
	Locks            int               // locked groups, and channels locked one by one
	LibraryLocks     int               // locked shelves of films and series, and titles locked one by one (D85)
	Adult            string            // what sources' marks for adults do (D86)
	RadioApart       bool              // boxes show radio stations on their Radio screen alone (D87)
	RadioStations    int               // radio stations that the sources list
	TMDBKeySet       bool              // the administrator gave a key for TMDB (D88)
	TMDBLanguage     string            // the language TMDB is asked in, "" for its own
	TMDBFound        int               // folders' titles that TMDB found
	TMDBMissed       int               // and that it had no match for
	TMDBNotice       string            // the notice TMDB asks applications to show
	CanRecord        bool              // this server records programmes (D89)
	RecordingsCap    int               // the room recordings may take, in gigabytes; 0 while recording is off
	Timeshift        int               // the minutes of a box's live channel the server keeps; 0 while timeshift is off (D92)
	RecordingsDir    string            // the folder they are written in
	RecordingsUsed   int64             // the room they take, in bytes
	Upcoming         []store.Recording // recordings to come and under way, the soonest first
	Recent           []store.Recording // the last that were made, failed or were called off
	Facts            []Fact
	Started          time.Time
	Portal           string // the address a MAG box loads
	StalkerPortal    string // the address a Stalker client is given
}

// settingsPage shows the Settings page.
func (a *Admin) settingsPage(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	a.showSettings(w, r, s, http.StatusOK, view{})
}

// showSettings renders the Settings page with v's alerts.
func (a *Admin) showSettings(w http.ResponseWriter, r *http.Request, s store.AdminSession, status int, v view) {
	d, err := a.settings(r)
	if err != nil {
		a.log.Error("cannot read the settings", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Settings", "Something went wrong; the server's log says what.", &s)
		return
	}
	v.Title, v.Section, v.Session, v.Data = "Settings", "settings", &s, d
	a.render(w, r, status, "settings", v)
}

// settings reads what the Settings page shows.
func (a *Admin) settings(r *http.Request) (settingsData, error) {
	ctx := r.Context()
	d := settingsData{CanRepair: a.canRepair(), Facts: a.support.Facts, Started: a.started}
	for _, f := range d.Facts {
		if f.Name == "ffmpeg" {
			d.FFmpeg = f.Value
		}
	}
	var err error
	if d.RepairDefault, err = a.store.RepairDefault(ctx); err != nil {
		return d, err
	}
	if d.RepairHLS, err = a.store.RepairHLS(ctx); err != nil {
		return d, err
	}
	d.CanConvert = a.streams != nil && a.streams.CanConvert()
	d.CanTranscode, d.ToneMap, d.Missing = a.transcoder()
	if d.Transcodes, err = a.store.Transcodes(ctx); err != nil {
		return d, err
	}
	if d.ConvertSound, err = a.store.ConvertsSound(ctx); err != nil {
		return d, err
	}
	if d.ServerTranscodes, err = a.store.ServerTranscodes(ctx); err != nil {
		return d, err
	}
	if d.PINSet, err = a.pinSet(ctx); err != nil {
		return d, err
	}
	if d.Locks, err = a.lockCount(ctx); err != nil {
		return d, err
	}
	if d.LibraryLocks, err = a.store.LibraryLocks(ctx); err != nil {
		return d, err
	}
	if d.Adult, err = a.store.AdultMarks(ctx); err != nil {
		return d, err
	}
	if d.RadioApart, err = a.store.RadioApart(ctx); err != nil {
		return d, err
	}
	if d.RadioStations, err = a.store.RadioStations(ctx); err != nil {
		return d, err
	}
	key, err := a.store.TMDBKey(ctx)
	if err != nil {
		return d, err
	}
	d.TMDBKeySet, d.TMDBNotice = key != "", tmdb.Notice
	if d.TMDBLanguage, err = a.store.TMDBLanguage(ctx); err != nil {
		return d, err
	}
	if d.TMDBFound, d.TMDBMissed, err = a.store.TMDBCounts(ctx); err != nil {
		return d, err
	}
	if d.RecordingsCap, err = a.store.RecordingsCap(ctx); err != nil {
		return d, err
	}
	if d.Timeshift, err = a.store.Timeshift(ctx); err != nil {
		return d, err
	}
	if a.recorder != nil {
		d.CanRecord, d.RecordingsDir, d.RecordingsUsed = true, a.recorder.Dir(), a.recorder.Used()
	}
	if d.Upcoming, err = a.store.Recordings(ctx, shownRecordings, store.RecordingScheduled, store.RecordingRecording); err != nil {
		return d, err
	}
	if d.Recent, err = a.store.RecentRecordings(ctx, shownRecordings); err != nil {
		return d, err
	}
	d.Portal, d.StalkerPortal = portalAddresses(r.Host, a.support.Listen)
	return d, nil
}

// lockCount counts the locked groups of the household's lineup, and its
// channels locked one by one.
func (a *Admin) lockCount(ctx context.Context) (int, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return 0, err
	}
	n, err := a.store.LockedEntries(ctx, lineup)
	if err != nil {
		return 0, err
	}
	groups, err := a.store.LineupGroups(ctx, lineup)
	if err != nil {
		return 0, err
	}
	for _, g := range groups {
		if g.Locked {
			n++
		}
	}
	return n, nil
}

// portalAddresses returns the addresses that a MAG box and a Stalker client
// are given, on the devices' port at the host this browser reached, or ""
// when the devices' address is unknown. listen is the address the server
// listens on for devices, such as ":8080".
func portalAddresses(host, listen string) (mag, stalker string) {
	listenHost, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return "", ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if listenHost != "" && listenHost != "0.0.0.0" && listenHost != "::" {
		host = listenHost
	}
	if host == "" {
		return "", ""
	}
	base := "http://" + net.JoinHostPort(host, port)
	return base + "/c/", base + "/stalker_portal/c/"
}

// settingsDone answers a change made on the Settings page: the page again
// with what happened, or for a plain form, a move back to it.
func (a *Admin) settingsDone(w http.ResponseWriter, r *http.Request, s store.AdminSession, err error, notice string) {
	var bad *editError
	switch {
	case errors.As(err, &bad):
		a.showSettings(w, r, s, bad.status, view{Error: bad.text})
	case err != nil:
		a.log.Error("cannot change a setting", "error", err)
		a.showSettings(w, r, s, http.StatusInternalServerError, view{Error: "Something went wrong; the server's log says what."})
	case r.Header.Get("HX-Request") == "true":
		a.showSettings(w, r, s, http.StatusOK, view{Notice: notice})
	default:
		http.Redirect(w, r, "/admin/settings", http.StatusSeeOther)
	}
}

// shownRecordings is how many recordings to come, and how many made, the
// Settings page lists.
const shownRecordings = 20

// RecordingCaps returns the rooms for recordings that the Settings page
// offers, with the one set if it is another.
func (d settingsData) RecordingCaps() []int {
	caps := recordingCaps
	if !slices.Contains(caps, d.RecordingsCap) {
		caps = append(slices.Clone(caps), d.RecordingsCap)
		slices.Sort(caps)
	}
	return caps
}

// RecordingRows returns the recordings that the Settings page lists: those
// to come, then those made.
func (d settingsData) RecordingRows() []store.Recording {
	return append(slices.Clip(d.Upcoming), d.Recent...)
}

// RecordingsUsedText says how much room the recordings take.
func (d settingsData) RecordingsUsedText() string { return sizeText(d.RecordingsUsed) }

// RecordingsFull reports whether the recordings take all their room.
func (d settingsData) RecordingsFull() bool {
	return d.RecordingsCap > 0 && d.RecordingsUsed >= int64(d.RecordingsCap)<<30
}

// AllLocks counts every lock: on groups and channels, and on shelves and
// titles.
func (d settingsData) AllLocks() int { return d.Locks + d.LibraryLocks }
