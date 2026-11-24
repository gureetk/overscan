// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"context"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/lineup"
	"overscan.gureet.ca/internal/portal/m3u"
	"overscan.gureet.ca/internal/portal/output"
	"overscan.gureet.ca/internal/store"
)

// How far the guide reaches, as the playlist's does (D103).
const (
	guideBack  = 24 * time.Hour
	guideAhead = 7 * 24 * time.Hour
	// shortGuide is how many programmes get_short_epg gives without a
	// limit (notes, section 5), and maxShortGuide the most it gives.
	shortGuide    = 4
	maxShortGuide = 100
)

// xtreamTime is how the API writes a time without a zone, in UTC, the
// time zone that server_info names.
const xtreamTime = "2006-01-02 15:04:05"

// account answers player_api.php without an action: the account and the
// server (notes, section 2).
func (o *Output) account(w http.ResponseWriter, r *http.Request, acct store.Account) {
	now := o.now()
	status := "Active"
	if acct.Ended(now) {
		status = "Expired"
	}
	var expires any
	if !acct.ExpiresAt.IsZero() {
		expires = strconv.FormatInt(acct.ExpiresAt.Unix(), 10)
	}
	limit := acct.MaxStreams
	if limit == 0 {
		limit = store.MaxAccountStreams
	}
	base := output.Base(r)
	scheme, host := "http", r.Host
	if len(base) > 8 && base[:8] == "https://" {
		scheme = "https"
	}
	port := "80"
	if scheme == "https" {
		port = "443"
	}
	if h, p, err := net.SplitHostPort(r.Host); err == nil {
		host, port = h, p
	}
	httpsPort := ""
	if scheme == "https" {
		httpsPort = port
	}
	writeJSON(w, map[string]any{
		"user_info": map[string]any{
			"username":               acct.Username,
			"password":               r.Form.Get("password"),
			"message":                "",
			"auth":                   1,
			"status":                 status,
			"exp_date":               expires,
			"is_trial":               "0",
			"active_cons":            strconv.Itoa(o.hub.AccountStreams(acct.ID)),
			"created_at":             strconv.FormatInt(acct.CreatedAt.Unix(), 10),
			"max_connections":        strconv.Itoa(limit),
			"allowed_output_formats": []string{"ts"},
		},
		"server_info": map[string]any{
			"url":             host,
			"port":            port,
			"https_port":      httpsPort,
			"server_protocol": scheme,
			"rtmp_port":       "",
			"timezone":        "UTC",
			"timestamp_now":   now.Unix(),
			"time_now":        now.UTC().Format(xtreamTime),
		},
	})
}

// category is a category as the API lists them.
type category struct {
	ID       string `json:"category_id"`
	Name     string `json:"category_name"`
	ParentID int    `json:"parent_id"`
}

// liveCategories answers get_live_categories: the groups of the account's
// channels, in the order of their first channel (notes, section 3).
func (o *Output) liveCategories(w http.ResponseWriter, r *http.Request, acct store.Account) {
	list, err := output.Channels(r.Context(), o.store, acct)
	if err != nil {
		o.failed(w, "list an account's channels", err)
		return
	}
	out := []category{}
	for _, g := range lineup.Groups(list) {
		out = append(out, category{ID: strconv.FormatInt(g.ID, 10), Name: g.Name})
	}
	writeJSON(w, out)
}

// liveStream is a channel as get_live_streams lists it.
type liveStream struct {
	Num               int     `json:"num"`
	Name              string  `json:"name"`
	StreamType        string  `json:"stream_type"`
	StreamID          int64   `json:"stream_id"`
	StreamIcon        string  `json:"stream_icon"`
	EPGChannelID      string  `json:"epg_channel_id"`
	Added             string  `json:"added"`
	CategoryID        *string `json:"category_id"`
	CategoryIDs       []int64 `json:"category_ids"`
	CustomSID         string  `json:"custom_sid"`
	TVArchive         int     `json:"tv_archive"`
	DirectSource      string  `json:"direct_source"`
	TVArchiveDuration int     `json:"tv_archive_duration"`
	Thumbnail         string  `json:"thumbnail"`
}

// liveStreams answers get_live_streams, of every channel or of one
// category's: each with the lineup entry's ID as its stream_id and its
// guide ID, and its number as num (notes, section 3).
func (o *Output) liveStreams(w http.ResponseWriter, r *http.Request, acct store.Account) {
	list, err := output.Channels(r.Context(), o.store, acct)
	if err != nil {
		o.failed(w, "list an account's channels", err)
		return
	}
	only := r.Form.Get("category_id")
	base := output.Base(r)
	out := []liveStream{}
	for _, c := range list {
		group := strconv.FormatInt(c.GroupID, 10)
		if only != "" && only != group {
			continue
		}
		s := liveStream{Num: c.Number, Name: c.Name, StreamType: "live", StreamID: c.EntryID,
			EPGChannelID: m3u.GuideID(c.EntryID), Added: "0", CategoryIDs: []int64{}}
		if c.Radio {
			s.StreamType = "radio_streams"
		}
		if logo := artwork.LogoPath(c.EntryID, c.Logo); logo != "" {
			s.StreamIcon = base + logo
		}
		if c.GroupID != 0 {
			s.CategoryID = &group
			s.CategoryIDs = []int64{c.GroupID}
		}
		if c.ArchiveDays > 0 {
			s.TVArchive, s.TVArchiveDuration = 1, c.ArchiveDays
		}
		out = append(out, s)
	}
	writeJSON(w, out)
}

// listing is a programme as get_short_epg and get_simple_data_table give
// it (notes, section 5).
type listing struct {
	ID             string `json:"id"`
	EPGID          string `json:"epg_id"`
	Title          string `json:"title"`
	Lang           string `json:"lang"`
	Start          string `json:"start"`
	End            string `json:"end"`
	Description    string `json:"description"`
	ChannelID      string `json:"channel_id"`
	StartTimestamp string `json:"start_timestamp"`
	StopTimestamp  string `json:"stop_timestamp"`
	NowPlaying     *int   `json:"now_playing,omitempty"`
	HasArchive     *int   `json:"has_archive,omitempty"`
}

// minuteBits is how many low bits of a programme's ID hold its start, in
// minutes since 1970: an ID is the channel's entry ID and the minute it
// starts, as the Stalker API makes them, so that it stays the same when the
// guide is read again.
const minuteBits = 26

// listingOf turns a programme into a listing, its title and description
// in Base64.
func listingOf(entryID int64, p store.Programme) listing {
	b64 := base64.StdEncoding.EncodeToString
	id := entryID<<minuteBits | (p.Start.Unix()/60)&(1<<minuteBits-1)
	return listing{ID: strconv.FormatInt(id, 10), EPGID: strconv.FormatInt(entryID, 10), Title: b64([]byte(p.Title)),
		Start: p.Start.UTC().Format(xtreamTime), End: p.Stop.UTC().Format(xtreamTime), Description: b64([]byte(p.Description)),
		ChannelID: m3u.GuideID(entryID), StartTimestamp: strconv.FormatInt(p.Start.Unix(), 10),
		StopTimestamp: strconv.FormatInt(p.Stop.Unix(), 10)}
}

// channelOf returns the account's channel that a request's stream_id
// names, if its players see it.
func (o *Output) channelOf(ctx context.Context, acct store.Account, id string) (store.LineupChannel, bool, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return store.LineupChannel{}, false, nil
	}
	list, err := output.Channels(ctx, o.store, acct)
	if err != nil {
		return store.LineupChannel{}, false, err
	}
	for _, c := range list {
		if c.EntryID == n {
			return c, true, nil
		}
	}
	return store.LineupChannel{}, false, nil
}

// shortGuide answers get_short_epg: the programme on now on a channel and
// the ones after it, limit of them, four if not given.
func (o *Output) shortGuide(w http.ResponseWriter, r *http.Request, acct store.Account) {
	ctx := r.Context()
	empty := map[string]any{"epg_listings": []listing{}}
	c, ok, err := o.channelOf(ctx, acct, r.Form.Get("stream_id"))
	if err != nil {
		o.failed(w, "look up a channel", err)
		return
	}
	if !ok {
		writeJSON(w, empty)
		return
	}
	limit := shortGuide
	if n, err := strconv.Atoi(r.Form.Get("limit")); err == nil && n > 0 {
		limit = min(n, maxShortGuide)
	}
	now := o.now()
	ps, err := o.store.EntryListings(ctx, acct.LineupID, c.EntryID, now, now.Add(guideAhead), now)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, empty)
		return
	}
	if err != nil {
		o.failed(w, "read a channel's guide", err)
		return
	}
	out := []listing{}
	for _, p := range ps {
		if len(out) == limit {
			break
		}
		out = append(out, listingOf(c.EntryID, p))
	}
	writeJSON(w, map[string]any{"epg_listings": out})
}

// dayTable answers get_simple_data_table: a channel's whole guide, from a
// day back, or as far back as its archive, to a week ahead, each programme
// marked if it is on now and if its archive keeps it.
func (o *Output) dayTable(w http.ResponseWriter, r *http.Request, acct store.Account) {
	ctx := r.Context()
	empty := map[string]any{"epg_listings": []listing{}}
	c, ok, err := o.channelOf(ctx, acct, r.Form.Get("stream_id"))
	if err != nil {
		o.failed(w, "look up a channel", err)
		return
	}
	if !ok {
		writeJSON(w, empty)
		return
	}
	now := o.now()
	from := now.Add(-guideBack)
	if c.ArchiveDays > 0 {
		from = now.Add(-time.Duration(c.ArchiveDays) * 24 * time.Hour)
	}
	ps, err := o.store.EntryListings(ctx, acct.LineupID, c.EntryID, from, now.Add(guideAhead), now)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, empty)
		return
	}
	if err != nil {
		o.failed(w, "read a channel's guide", err)
		return
	}
	out := []listing{}
	for _, p := range ps {
		l := listingOf(c.EntryID, p)
		playing, archived := 0, 0
		if !now.Before(p.Start) && now.Before(p.Stop) {
			playing = 1
		}
		if store.InArchive(p, c.ArchiveDays, now) {
			archived = 1
		}
		l.NowPlaying, l.HasArchive = &playing, &archived
		out = append(out, l)
	}
	writeJSON(w, map[string]any{"epg_listings": out})
}
