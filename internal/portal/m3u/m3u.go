// SPDX-License-Identifier: AGPL-3.0-or-later

// Package m3u serves each account's playlist and guide to players (D103):
// an M3U playlist of the channels that the account sees, at addresses that
// play through the hub as the account, and an XMLTV guide of those
// channels. An account's playlist token names it in every address; the
// outputs, and the account's own switch, start off. What players read is
// in docs/protocol/m3u-output-notes.md.
package m3u

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/portal/output"
	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/store"
)

// How far the guide reaches: back a day, or a channel's archive's days,
// and ahead a week.
const (
	guideBack  = 24 * time.Hour
	guideAhead = 7 * 24 * time.Hour
	// longestArchive caps a past programme that a player asks for.
	longestArchive = 12 * time.Hour
)

// Output serves the playlists and guides.
type Output struct {
	store  *store.Store
	hub    *hub.Hub
	logins *admin.Logins
	log    *slog.Logger
	now    func() time.Time
}

// New returns the output, which finds accounts by their tokens with logins.
func New(st *store.Store, h *hub.Hub, logins *admin.Logins, log *slog.Logger) *Output {
	return &Output{store: st, hub: h, logins: logins, log: log, now: time.Now}
}

// Register adds the output's paths to mux.
func (o *Output) Register(mux *routes.Mux) {
	mux.HandleFunc("GET /playlist/{file}", o.playlist)
	mux.HandleFunc("GET /epg/{file}", o.guide)
	mux.HandleFunc("GET /playlist/{token}/{file}", o.live)
	mux.HandleFunc("GET /playlist/{token}/archive/{file}", o.archive)
}

// account returns the account whose token a request names, if the outputs
// answer it and the account may use its playlist; otherwise it answers
// the request and returns false. A token that no account has is answered
// as if nothing were there.
func (o *Output) account(w http.ResponseWriter, r *http.Request, token string) (store.Account, bool) {
	ctx := r.Context()
	refusal, err := output.Gate(ctx, o.store, r)
	if err != nil {
		o.failed(w, "read the outputs' settings", err)
		return store.Account{}, false
	}
	if refusal != nil {
		refusal.Refuse(w)
		return store.Account{}, false
	}
	acct, err := o.logins.Token(ctx, token)
	switch {
	case errors.Is(err, admin.ErrLogin):
		(&output.Refusal{Status: http.StatusNotFound, Text: "404 page not found"}).Refuse(w)
		return store.Account{}, false
	case err != nil:
		o.failed(w, "look up a playlist token", err)
		return store.Account{}, false
	case !acct.Outputs.M3U:
		(&output.Refusal{Status: http.StatusForbidden, Text: "This account may not use playlists."}).Refuse(w)
		return store.Account{}, false
	case acct.Ended(o.now()):
		(&output.Refusal{Status: http.StatusForbidden, Text: "This account has ended."}).Refuse(w)
		return store.Account{}, false
	}
	return acct, true
}

// failed answers a request that the server could not, and logs why,
// without the request's address, which holds a token.
func (o *Output) failed(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	o.log.Error("an output failed", "while", what, "error", err)
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// attr makes s safe inside a double-quoted attribute of a playlist.
func attr(s string) string {
	return strings.NewReplacer(`"`, "'", "\r", " ", "\n", " ").Replace(s)
}

// title makes s safe as the name after an #EXTINF line's comma.
func title(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// GuideID is the ID of a lineup entry's channel in an output's guide, and
// its tvg-id in the playlist: the entry's own ID, which outlives refreshes
// and is unique on the server, whatever the sources call their channels.
func GuideID(entryID int64) string { return strconv.FormatInt(entryID, 10) }

// playlist serves GET /playlist/<token>.m3u: the account's channels, in
// the lineup's order.
func (o *Output) playlist(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	token, ok := strings.CutSuffix(file, ".m3u")
	if !ok {
		token, ok = strings.CutSuffix(file, ".m3u8")
	}
	if !ok || token == "" {
		http.NotFound(w, r)
		return
	}
	acct, ok := o.account(w, r, token)
	if !ok {
		return
	}
	ctx := r.Context()
	list, err := output.Channels(ctx, o.store, acct)
	if err != nil {
		o.failed(w, "list an account's channels", err)
		return
	}
	base := output.Base(r) + "/playlist/" + token
	o.log.Info("a player read its playlist", "account", acct.Name, "player", output.ClientAddress(r), "channels", len(list))
	links := Links{
		Stream:  func(entryID int64) string { return base + "/" + strconv.FormatInt(entryID, 10) + ".ts" },
		Archive: func(entryID int64) string { return base + "/archive/" + strconv.FormatInt(entryID, 10) + ".ts" },
	}
	if acct.Outputs.HLS && output.WantsHLS(r) {
		// A player that asks for HLS gets its addresses (D153).
		links.HLS = func(entryID int64) string { return base + "/" + strconv.FormatInt(entryID, 10) + ".m3u8" }
	}
	WritePlaylist(w, list, output.Base(r), output.Base(r)+"/epg/"+token+".xml.gz", links)
}

// Links makes a playlist's addresses: a channel's stream, and the address
// of its past programmes, to which the playlist adds start={utc} and
// end={utcend}. HLS, if set, makes the address of a channel's HLS, which
// a channel other than a radio station gets in place of its stream's
// (D153).
type Links struct {
	Stream  func(entryID int64) string
	Archive func(entryID int64) string
	HLS     func(entryID int64) string
}

// WritePlaylist answers with an M3U playlist of channels, in their order,
// that names guide as its guide; logos are addresses under base.
func WritePlaylist(w http.ResponseWriter, list []store.LineupChannel, base, guide string, links Links) {
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U url-tvg=\"%s\" x-tvg-url=\"%s\"\n", attr(guide), attr(guide))
	for _, c := range list {
		fmt.Fprintf(&b, "#EXTINF:-1 tvg-id=\"%s\" tvg-name=\"%s\" tvg-chno=\"%d\"", GuideID(c.EntryID), attr(c.Name), c.Number)
		if logo := artwork.LogoPath(c.EntryID, c.Logo); logo != "" {
			fmt.Fprintf(&b, " tvg-logo=\"%s\"", base+logo)
		}
		if c.Group != "" {
			fmt.Fprintf(&b, " group-title=\"%s\"", attr(c.Group))
		}
		if c.Radio {
			b.WriteString(` radio="true"`)
		}
		if c.ArchiveDays > 0 {
			fmt.Fprintf(&b, ` catchup="default" catchup-source="%s?start={utc}&end={utcend}" catchup-days="%d"`,
				attr(links.Archive(c.EntryID)), c.ArchiveDays)
		}
		link := links.Stream(c.EntryID)
		if links.HLS != nil && !c.Radio {
			link = links.HLS(c.EntryID)
		}
		fmt.Fprintf(&b, ",%s\n%s\n", title(c.Name), link)
	}
	w.Header().Set("Content-Type", "audio/x-mpegurl; charset=utf-8")
	w.Header().Set("Content-Disposition", `inline; filename="overscan.m3u"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

// guide serves GET /epg/<token>.xml.gz, and the same without gzip at
// <token>.xml: the account's channels and their programmes, times in UTC.
func (o *Output) guide(w http.ResponseWriter, r *http.Request) {
	file := r.PathValue("file")
	token, zipped := strings.CutSuffix(file, ".xml.gz")
	if !zipped {
		var ok bool
		if token, ok = strings.CutSuffix(file, ".xml"); !ok {
			http.NotFound(w, r)
			return
		}
	}
	if token == "" {
		http.NotFound(w, r)
		return
	}
	acct, ok := o.account(w, r, token)
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
	if zipped {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", `inline; filename="overscan.xml.gz"`)
	} else {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	}
	var out io.Writer = w
	var zw *gzip.Writer
	if zipped {
		zw = gzip.NewWriter(w)
		out = zw
	}
	bw := bufio.NewWriterSize(out, 64<<10)
	n, err := WriteGuide(ctx, bw, o.store, acct.LineupID, list, output.Base(r), o.now())
	if err == nil {
		err = bw.Flush()
	}
	if err == nil && zw != nil {
		err = zw.Close()
	}
	if err != nil {
		// The answer has begun: all that can be done is to stop it.
		if !errors.Is(err, context.Canceled) {
			o.log.Warn("an output's guide stopped part way", "account", acct.Name, "error", err)
		}
		return
	}
	o.log.Info("a player read its guide", "account", acct.Name, "player", output.ClientAddress(r), "channels", len(list),
		"programmes", n)
}

// WriteGuide writes an XMLTV guide of the given channels of a lineup, and
// their programmes from a day before now, or a channel's archive's days,
// to a week ahead, with placeholders where the setting asks for them
// (D142), and returns how many programmes it wrote. Every time is in UTC.
// Logos are addresses under base. It gives the memory that it used back to
// the system once it is done.
func WriteGuide(ctx context.Context, w io.Writer, st *store.Store, lineupID int64, channels []store.LineupChannel, base string, now time.Time) (int, error) {
	// A guide of a large lineup, with a week of programmes, runs to
	// hundreds of megabytes, built a programme at a time from the
	// database: garbage that Go would hand back to the system only over
	// minutes. A player reads it every few hours, so collecting it at once
	// costs little and keeps the idle server small (the plan's section 15),
	// as a source's read does.
	defer debug.FreeOSMemory()
	from := now.Add(-guideBack)
	shown := make(map[int64]time.Time, len(channels))
	if _, err := io.WriteString(w, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE tv SYSTEM \"xmltv.dtd\">\n"+
		"<tv generator-info-name=\"Overscan\">\n"); err != nil {
		return 0, err
	}
	for _, c := range channels {
		back := now.Add(-guideBack)
		if c.ArchiveDays > 0 {
			back = now.Add(-time.Duration(c.ArchiveDays) * 24 * time.Hour)
		}
		if back.Before(from) {
			from = back
		}
		shown[c.EntryID] = back
		if _, err := fmt.Fprintf(w, "<channel id=\"%s\"><display-name>%s</display-name>", GuideID(c.EntryID), text(c.Name)); err != nil {
			return 0, err
		}
		if logo := artwork.LogoPath(c.EntryID, c.Logo); logo != "" {
			if _, err := fmt.Fprintf(w, "<icon src=\"%s\"/>", text(base+logo)); err != nil {
				return 0, err
			}
		}
		if _, err := io.WriteString(w, "</channel>\n"); err != nil {
			return 0, err
		}
	}
	n := 0
	err := st.EachLineupListing(ctx, lineupID, from, now.Add(guideAhead), now, func(entryID int64, p store.Programme) error {
		back, ok := shown[entryID]
		if !ok || !p.Stop.After(back) {
			return nil
		}
		n++
		if _, err := fmt.Fprintf(w, "<programme start=\"%s\" stop=\"%s\" channel=\"%s\"><title>%s</title>",
			XMLTVTime(p.Start), XMLTVTime(p.Stop), GuideID(entryID), text(p.Title)); err != nil {
			return err
		}
		for _, part := range []struct{ tag, value string }{{"sub-title", p.SubTitle}, {"desc", p.Description}, {"category", p.Category}} {
			if part.value != "" {
				if _, err := fmt.Fprintf(w, "<%s>%s</%s>", part.tag, text(part.value), part.tag); err != nil {
					return err
				}
			}
		}
		_, err := io.WriteString(w, "</programme>\n")
		return err
	})
	if err != nil {
		return n, err
	}
	_, err = io.WriteString(w, "</tv>\n")
	return n, err
}

// XMLTVTime writes a time as XMLTV does, in UTC.
func XMLTVTime(t time.Time) string { return t.UTC().Format("20060102150405") + " +0000" }

// text escapes s for XML, leaving out what XML cannot hold.
func text(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' || r >= 0x20 && r != 0xFFFE && r != 0xFFFF {
			return r
		}
		return -1
	}, s)))
	return b.String()
}

// entryOf returns the lineup entry that a stream's file names, such as
// 123.ts, if its account's players may play it.
func (o *Output) entryOf(w http.ResponseWriter, r *http.Request, acct store.Account) (store.PlayableEntry, bool) {
	id, ok := entryFile(r.PathValue("file"))
	if !ok && acct.Outputs.HLS {
		id, ok = entryFile(strings.TrimSuffix(r.PathValue("file"), ".m3u8") + ".ts")
	}
	if !ok {
		http.NotFound(w, r)
		return store.PlayableEntry{}, false
	}
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
	// Only its streams from the sources that the account uses (D114).
	entry, ok = acct.Sources.For(entry)
	if !ok {
		http.NotFound(w, r)
	}
	return entry, ok
}

// entryFile reads a stream's file name, such as 123.ts.
func entryFile(file string) (int64, bool) {
	digits, ok := strings.CutSuffix(file, ".ts")
	if !ok || digits == "" || len(digits) > 18 {
		return 0, false
	}
	id, err := strconv.ParseInt(digits, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != digits {
		return 0, false
	}
	return id, true
}

// live serves GET /playlist/<token>/<entry>.ts: the channel, through the
// hub, as the account's player. While the account's HLS for players is on,
// <entry>.m3u8 plays it as HLS that the hub cuts from the channel's one
// stream (D153), or, for a channel that the hub cannot cut, as at its .ts
// address.
func (o *Output) live(w http.ResponseWriter, r *http.Request) {
	acct, ok := o.account(w, r, r.PathValue("token"))
	if !ok {
		return
	}
	entry, ok := o.entryOf(w, r, acct)
	if !ok {
		return
	}
	player := output.Player(o.hub, acct.ID, output.M3U, r)
	if strings.HasSuffix(r.PathValue("file"), ".m3u8") {
		if link := o.hub.CutLink(player, entry); link != "" {
			o.hub.ServeLink(w, r, link)
			return
		}
	}
	o.hub.ServeLink(w, r, output.Continuous(o.hub.Link(player, entry)))
}

// archive serves GET /playlist/<token>/archive/<entry>.ts?start=S&end=E:
// a past programme of the channel from its source's archive, from S to E
// in Unix seconds, through the hub, as the account's player (D81).
func (o *Output) archive(w http.ResponseWriter, r *http.Request) {
	acct, ok := o.account(w, r, r.PathValue("token"))
	if !ok {
		return
	}
	id, ok := entryFile(r.PathValue("file"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	entry, err := o.store.PlayableArchive(ctx, id)
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
	start, stop, ok := ArchiveSpan(r.URL.Query().Get("start"), r.URL.Query().Get("end"), entry.Days, o.now())
	if !ok {
		http.Error(w, "start and end must name a past time within the channel's archive", http.StatusBadRequest)
		return
	}
	player := output.Player(o.hub, acct.ID, output.M3U, r)
	link, _ := o.hub.ArchiveLink(player, entry.PlayableEntry, start, stop)
	o.hub.ServeLink(w, r, link)
}

// ArchiveSpan reads the start and end of a past programme that a player
// asks for, in Unix seconds: the start within the archive's days, before
// now, and the end after it, at most 12 hours on. An end that is missing
// or past those is cut to them.
func ArchiveSpan(startText, endText string, days int, now time.Time) (start, stop time.Time, ok bool) {
	s, err := strconv.ParseInt(startText, 10, 64)
	if err != nil || days <= 0 {
		return time.Time{}, time.Time{}, false
	}
	start = time.Unix(s, 0).UTC()
	if !start.Before(now) || !start.After(now.Add(-time.Duration(days)*24*time.Hour)) {
		return time.Time{}, time.Time{}, false
	}
	stop = start.Add(longestArchive)
	if e, err := strconv.ParseInt(endText, 10, 64); err == nil && e > s && e-s <= int64(longestArchive/time.Second) {
		stop = time.Unix(e, 0).UTC()
	}
	return start, stop, true
}
