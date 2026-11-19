// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"cmp"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/lineup"
	"overscan.gureet.ca/internal/store"
)

// pageSize is how many items a page of a Stalker list holds.
const pageSize = 14

// genre is an itv genre: a group of channels in the lineup.
type genre struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Censored string `json:"censored"`
}

// itvChannel is a channel in the shape the protocol notes describe (section
// 4). Numbers are strings. A logo is an address on Overscan, which serves it
// from its own copy (D54). The archive flags are on for a channel whose past
// programmes play from its source's archive (D81), with how far back it
// reaches in hours.
type itvChannel struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Number            string `json:"number"`
	Cmd               string `json:"cmd"`
	Logo              string `json:"logo"`
	TVGenreID         string `json:"tv_genre_id"`
	XMLTVID           string `json:"xmltv_id"`
	Censored          string `json:"censored"`
	TVArchive         string `json:"tv_archive"`
	EnableTVArchive   string `json:"enable_tv_archive"`
	Archive           string `json:"archive"`
	TVArchiveDuration string `json:"tv_archive_duration"`
}

// page is a page of a Stalker list.
type page struct {
	TotalItems   int          `json:"total_items"`
	MaxPageItems int          `json:"max_page_items"`
	Data         []itvChannel `json:"data"`
}

// lineup returns the channels d's account sees, and their genres in the order
// of their first channel (see lineup.Groups). Channels that need the
// account's parental PIN are left out, since Stalker clients have no way to
// ask for it (D55).
func (a *API) lineup(r *http.Request, d store.Device) ([]store.LineupChannel, []genre, error) {
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		return nil, nil, err
	}
	channels, err := a.store.LineupChannels(r.Context(), acct.LineupID)
	if err != nil {
		return nil, nil, err
	}
	if acct.PINHash != "" {
		channels = slices.DeleteFunc(channels, func(c store.LineupChannel) bool { return c.Locked })
	}
	groups := lineup.Groups(channels)
	genres := make([]genre, len(groups))
	for i, g := range groups {
		genres[i] = genre{ID: strconv.FormatInt(g.ID, 10), Title: g.Name, Censored: "0"}
	}
	return channels, genres, nil
}

// genreID is the ID of a channel's genre, "" if it has none.
func genreID(c store.LineupChannel) string {
	if c.GroupID == 0 {
		return ""
	}
	return strconv.FormatInt(c.GroupID, 10)
}

// getGenres answers itv/get_genres: "All" first, then the lineup's genres.
func (a *API) getGenres(w http.ResponseWriter, r *http.Request, d store.Device) {
	_, genres, err := a.lineup(r, d)
	if err != nil {
		a.internalError(w, "list genres", err)
		return
	}
	writeJS(w, append([]genre{{ID: "*", Title: "All", Censored: "0"}}, genres...))
}

// getAllChannels answers itv/get_all_channels with every channel, unpaged.
func (a *API) getAllChannels(w http.ResponseWriter, r *http.Request, d store.Device) {
	channels, _, err := a.lineup(r, d)
	if err != nil {
		a.internalError(w, "list channels", err)
		return
	}
	data := make([]itvChannel, len(channels))
	for i, c := range channels {
		data[i] = a.itvChannel(r, c)
	}
	writeJS(w, struct {
		Data []itvChannel `json:"data"`
	}{data})
}

// getOrderedList answers itv/get_ordered_list: one page of the channels in a
// genre ("*" for all), by number or by name. fav=1 keeps the account's
// favourites (D53).
func (a *API) getOrderedList(w http.ResponseWriter, r *http.Request, d store.Device) {
	channels, _, err := a.lineup(r, d)
	if err != nil {
		a.internalError(w, "list channels", err)
		return
	}
	if r.Form.Get("fav") == "1" {
		favourites, err := a.store.Favourites(r.Context(), d.AccountID)
		if err != nil {
			a.internalError(w, "list favourites", err)
			return
		}
		channels = slices.DeleteFunc(channels, func(c store.LineupChannel) bool {
			return !slices.Contains(favourites, c.EntryID)
		})
	}
	if g := r.Form.Get("genre"); g != "" && g != "*" {
		channels = slices.DeleteFunc(channels, func(c store.LineupChannel) bool {
			return genreID(c) != g
		})
	}
	if r.Form.Get("sortby") == "name" {
		slices.SortStableFunc(channels, func(x, y store.LineupChannel) int {
			return cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name))
		})
	}

	p, err := strconv.Atoi(r.Form.Get("p"))
	if err != nil || p < 1 {
		p = 1
	}
	start := min((p-1)*pageSize, len(channels))
	end := min(start+pageSize, len(channels))
	out := page{TotalItems: len(channels), MaxPageItems: pageSize, Data: make([]itvChannel, 0, end-start)}
	for _, c := range channels[start:end] {
		out.Data = append(out.Data, a.itvChannel(r, c))
	}
	writeJS(w, out)
}

// itvChannel turns a lineup channel into its Stalker shape. Its ID is the
// lineup entry's, which stays the same across refreshes. Its cmd names the
// channel for create_link, in the form clients expect, and is no stream
// address.
func (a *API) itvChannel(r *http.Request, c store.LineupChannel) itvChannel {
	id := strconv.FormatInt(c.EntryID, 10)
	archive := "0"
	if c.ArchiveDays > 0 {
		archive = "1"
	}
	return itvChannel{
		ID:                id,
		Name:              c.Name,
		Number:            strconv.Itoa(c.Number),
		Cmd:               "ffrt http://" + r.Host + "/ch/" + id,
		TVGenreID:         genreID(c),
		Logo:              logoURL(r, c),
		XMLTVID:           c.XMLTVID,
		Censored:          "0",
		TVArchive:         archive,
		EnableTVArchive:   archive,
		Archive:           archive,
		TVArchiveDuration: strconv.Itoa(c.ArchiveDays * 24),
	}
}

// logoURL returns the address on Overscan of a channel's logo, "" if it has
// none.
func logoURL(r *http.Request, c store.LineupChannel) string {
	path := artwork.LogoPath(c.EntryID, c.Logo)
	if path == "" {
		return ""
	}
	return "http://" + r.Host + path
}

// createLink answers itv/create_link with a link that plays the channel cmd
// names, through the hub, or with the error a client shows instead:
// nothing_to_play for a channel the device cannot play, limit when the
// channel's source has no free connection (protocol notes, section 4).
func (a *API) createLink(w http.ResponseWriter, r *http.Request, d store.Device) {
	reply := func(cmd, errText string) {
		writeJS(w, struct {
			Cmd   string `json:"cmd"`
			Error string `json:"error"`
		}{cmd, errText})
	}
	id, ok := entryFromCmd(r.Form.Get("cmd"))
	if !ok {
		reply("", "nothing_to_play")
		return
	}
	entry, err := a.store.PlayableEntry(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		reply("", "nothing_to_play")
		return
	}
	if err != nil {
		a.internalError(w, "look up channel", err)
		return
	}
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if entry.LineupID != acct.LineupID || (entry.Locked && acct.PINHash != "") {
		reply("", "nothing_to_play")
		return
	}
	if !a.hub.AvailableChannel(r.Context(), entry, d.ID) {
		reply("", "limit")
		return
	}
	link := a.hub.Link(d.ID, entry)
	if err := a.store.RecordWatch(r.Context(), d.ID, entry.EntryID, a.now()); err != nil {
		a.log.Warn("cannot record what a device watched", "mac", d.MAC, "error", err)
	}
	reply("ffmpeg http://"+r.Host+link, "")
}

// entryFromCmd returns the lineup entry that a channel's cmd names: the last
// word of the cmd is an address whose path is /ch/<id>.
func entryFromCmd(cmd string) (int64, bool) {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return 0, false
	}
	u, err := url.Parse(fields[len(fields)-1])
	if err != nil {
		return 0, false
	}
	rest, ok := strings.CutPrefix(u.Path, "/ch/")
	if !ok {
		return 0, false
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	return id, err == nil && id > 0
}
