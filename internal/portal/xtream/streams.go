// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/portal/m3u"
	"overscan.gureet.ca/internal/portal/output"
	"overscan.gureet.ca/internal/store"
)

// longestShift is the longest past programme that a timeshift address
// plays, as the playlist's archive addresses (D103).
const longestShift = 12 * time.Hour

// shiftStart is how a timeshift address writes its start, in the time
// zone that server_info names: UTC.
const shiftStart = "2006-01-02:15-04"

// streamLogin returns the account whose login a stream's, a playlist's or
// a guide's address carries, if it may play; otherwise it answers the
// request and returns false.
func (o *Output) streamLogin(w http.ResponseWriter, r *http.Request, username, password string) (store.Account, bool) {
	acct, refusal, err := o.login(r.Context(), r, username, password)
	if refusal != nil || err != nil {
		o.refuse(w, refusal, err)
		return store.Account{}, false
	}
	if acct.Ended(o.now()) {
		(&output.Refusal{Status: http.StatusForbidden, Text: "This account has ended."}).Refuse(w)
		return store.Account{}, false
	}
	return acct, true
}

// prefix is the start of the stream addresses of a login: the server, the
// kind, the username and the password, escaped.
func prefix(r *http.Request, kind, username, password string) string {
	return output.Base(r) + "/" + kind + "/" + url.PathEscape(username) + "/" + url.PathEscape(password) + "/"
}

// playlist serves GET /get.php: the account's channels as an M3U playlist,
// as /playlist/<token>.m3u lists them, with the stream addresses of the
// login (notes, section 10). Films and series are left out: a playlist of
// every episode would open every series at its source.
func (o *Output) playlist(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	username, password := q.Get("username"), q.Get("password")
	acct, ok := o.streamLogin(w, r, username, password)
	if !ok {
		return
	}
	list, err := output.Channels(r.Context(), o.store, acct)
	if err != nil {
		o.failed(w, "list an account's channels", err)
		return
	}
	live, shift := prefix(r, "live", username, password), prefix(r, "timeshift", username, password)
	guide := output.Base(r) + "/xmltv.php?" + url.Values{"username": {username}, "password": {password}}.Encode()
	o.log.Info("a player read its playlist", "account", acct.Name, "player", output.ClientAddress(r), "channels", len(list),
		"output", output.Xtream)
	m3u.WritePlaylist(w, list, output.Base(r), guide, m3u.Links{
		Stream:  func(entryID int64) string { return live + strconv.FormatInt(entryID, 10) + ".ts" },
		Archive: func(entryID int64) string { return shift + strconv.FormatInt(entryID, 10) + ".ts" },
	})
}

// guide serves GET /xmltv.php: the account's guide, as /epg/<token>.xml
// gives it.
func (o *Output) guide(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	acct, ok := o.streamLogin(w, r, q.Get("username"), q.Get("password"))
	if !ok {
		return
	}
	ctx := r.Context()
	list, err := output.Channels(ctx, o.store, acct)
	if err != nil {
		o.failed(w, "list an account's channels", err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	bw := bufio.NewWriterSize(w, 64<<10)
	n, err := m3u.WriteGuide(ctx, bw, o.store, acct.LineupID, list, output.Base(r), o.now())
	if err == nil {
		err = bw.Flush()
	}
	if err != nil {
		// The answer has begun: all that can be done is to stop it.
		if !errors.Is(err, context.Canceled) {
			o.log.Warn("an output's guide stopped part way", "account", acct.Name, "error", err)
		}
		return
	}
	o.log.Info("a player read its guide", "account", acct.Name, "player", output.ClientAddress(r), "channels", len(list),
		"programmes", n, "output", output.Xtream)
}

// streamFile reads a stream's file name, such as 123.ts: its ID, and its
// extension, "" if it has none.
func streamFile(file string) (int64, string, bool) {
	digits, ext, dot := strings.Cut(file, ".")
	if digits == "" || len(digits) > 18 || dot && ext == "" || len(ext) > 8 || strings.Contains(ext, ".") {
		return 0, "", false
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != digits {
		return 0, "", false
	}
	return id, ext, true
}

// entry returns the lineup entry that an ID names, if the account's
// players may play it, with only its streams from the sources that the
// account uses (D114); otherwise it answers 404 and returns false.
func (o *Output) entry(w http.ResponseWriter, r *http.Request, acct store.Account, id int64) (store.PlayableEntry, bool) {
	entry, err := o.store.PlayableEntry(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return store.PlayableEntry{}, false
	case err != nil:
		o.failed(w, "look up a channel", err)
		return store.PlayableEntry{}, false
	case entry.LineupID != acct.LineupID, entry.Locked && acct.PINHash != "":
		http.NotFound(w, r)
		return store.PlayableEntry{}, false
	}
	entry, ok := acct.Sources.For(entry)
	if !ok {
		http.NotFound(w, r)
	}
	return entry, ok
}

// live serves GET /live/<username>/<password>/<stream_id>.ts, or .m3u8,
// or with no extension: the channel, through the hub, as the account's
// player. A .ts address plays one continuous MPEG-TS stream, as the
// playlist's do (D103); an .m3u8 address plays HLS for a channel whose
// source sends it, and nothing for one whose source sends MPEG-TS, which
// allowed_output_formats does not offer (notes, section 10).
//
// The hub's own resources of an HLS session are at
// /live/<token>/r/<n>.<ext>, which this pattern matches too: they go to
// the hub.
func (o *Output) live(w http.ResponseWriter, r *http.Request) {
	username, password, file := r.PathValue("user"), r.PathValue("pass"), r.PathValue("file")
	if password == "r" && o.hub.IsToken(username) {
		o.hub.ServeHTTP(w, r)
		return
	}
	acct, ok := o.streamLogin(w, r, username, password)
	if !ok {
		return
	}
	id, ext, ok := streamFile(file)
	if !ok || ext != "" && ext != "ts" && ext != "m3u8" {
		http.NotFound(w, r)
		return
	}
	entry, ok := o.entry(w, r, acct, id)
	if !ok {
		return
	}
	player := output.Player(o.hub, acct.ID, output.Xtream, r)
	link := o.hub.Link(player, entry)
	if ext != "m3u8" {
		link = output.Continuous(link)
	} else if !strings.HasSuffix(link, ".m3u8") {
		(&output.Refusal{Status: http.StatusNotFound, Text: "This channel plays as .ts."}).Refuse(w)
		return
	}
	o.hub.ServeLink(w, r, link)
}

// movie serves GET /movie/<username>/<password>/<stream_id>.<ext>: the
// film's file as it is, through the hub, which passes on a player's range
// requests so that it can seek (D64, D75), or, where its setting transcodes
// it always, ffmpeg's transcode, in which the player seeks by bytes as in
// a file (D151). The extension is not checked: a player takes the one its
// list gave.
func (o *Output) movie(w http.ResponseWriter, r *http.Request) {
	o.item(w, r, store.KindMovie)
}

// episode serves GET /series/<username>/<password>/<id>.<ext>: an episode,
// as movie serves a film.
func (o *Output) episode(w http.ResponseWriter, r *http.Request) {
	o.item(w, r, store.KindEpisode)
}

func (o *Output) item(w http.ResponseWriter, r *http.Request, kind string) {
	acct, ok := o.streamLogin(w, r, r.PathValue("user"), r.PathValue("pass"))
	if !ok {
		return
	}
	id, _, ok := streamFile(r.PathValue("file"))
	if !ok || o.library == nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	var item store.PlayableItem
	var err error
	if kind == store.KindMovie {
		item, err = o.store.PlayableMovie(ctx, id)
	} else {
		item, err = o.store.PlayableEpisode(ctx, id)
	}
	switch {
	case errors.Is(err, store.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		o.failed(w, "look up a title to play", err)
		return
	case item.Locked && acct.PINHash != "", !acct.Sources.Uses(item.SourceID):
		http.NotFound(w, r)
		return
	}
	player := output.Player(o.hub, acct.ID, output.Xtream, r)
	o.hub.ServePlayerItem(w, r, player, item)
}

// archive serves GET /timeshift/<username>/<password>/<stream_id>.ts?start=S&end=E,
// the address that get.php's playlist gives for a channel's past
// programmes, as the token playlist's archive address (D103).
func (o *Output) archive(w http.ResponseWriter, r *http.Request) {
	acct, ok := o.streamLogin(w, r, r.PathValue("user"), r.PathValue("pass"))
	if !ok {
		return
	}
	id, ext, ok := streamFile(r.PathValue("file"))
	if !ok || ext != "ts" {
		http.NotFound(w, r)
		return
	}
	q := r.URL.Query()
	o.playArchive(w, r, acct, id, func(days int, now time.Time) (time.Time, time.Time, bool) {
		return m3u.ArchiveSpan(q.Get("start"), q.Get("end"), days, now)
	})
}

// timeshift serves GET /timeshift/<username>/<password>/<minutes>/<YYYY-MM-DD:HH-MM>/<stream_id>.ts:
// a channel's past programme from its archive, from the start, in UTC, for
// the minutes given (notes, section 6).
func (o *Output) timeshift(w http.ResponseWriter, r *http.Request) {
	acct, ok := o.streamLogin(w, r, r.PathValue("user"), r.PathValue("pass"))
	if !ok {
		return
	}
	id, ext, ok := streamFile(r.PathValue("file"))
	if !ok || ext != "ts" && ext != "" {
		http.NotFound(w, r)
		return
	}
	minutes, err := strconv.Atoi(r.PathValue("minutes"))
	start, startErr := time.ParseInLocation(shiftStart, r.PathValue("start"), time.UTC)
	if err != nil || startErr != nil || minutes <= 0 || time.Duration(minutes)*time.Minute > longestShift {
		http.Error(w, "the start must be YYYY-MM-DD:HH-MM in UTC and the minutes from 1 to 720", http.StatusBadRequest)
		return
	}
	o.playArchive(w, r, acct, id, func(days int, now time.Time) (time.Time, time.Time, bool) {
		return m3u.ArchiveSpan(strconv.FormatInt(start.Unix(), 10),
			strconv.FormatInt(start.Add(time.Duration(minutes)*time.Minute).Unix(), 10), days, now)
	})
}

// playArchive plays a past programme of a channel from its source's
// archive (D81), from the span that span gives, through the hub, as the
// account's player.
func (o *Output) playArchive(w http.ResponseWriter, r *http.Request, acct store.Account, id int64,
	span func(days int, now time.Time) (time.Time, time.Time, bool)) {
	entry, err := o.store.PlayableArchive(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, store.ErrNoArchive):
		http.NotFound(w, r)
		return
	case err != nil:
		o.failed(w, "look up a channel's archive", err)
		return
	case entry.LineupID != acct.LineupID, entry.Locked && acct.PINHash != "", !acct.Sources.Uses(entry.SourceID):
		// A past programme comes from the channel's own source (D114).
		http.NotFound(w, r)
		return
	}
	start, stop, ok := span(entry.Days, o.now())
	if !ok {
		http.Error(w, "the start must be a past time within the channel's archive", http.StatusBadRequest)
		return
	}
	player := output.Player(o.hub, acct.ID, output.Xtream, r)
	link, _ := o.hub.ArchiveLink(player, entry.PlayableEntry, start, stop)
	o.hub.ServeLink(w, r, link)
}
