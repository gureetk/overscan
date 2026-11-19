// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/store"
)

// The guide (D51), as the protocol notes describe it (section 5). Times go
// out as Unix seconds; a device's time zone, from its timezone cookie,
// decides only which day a date names.

// Limits of the guide's answers.
const (
	epgPageSize      = 10 // programmes on a page of a day's table
	shortEPGSize     = 10 // programmes that get_short_epg gives without a size
	maxShortEPGSize  = 50
	shortEPGAhead    = 48 * time.Hour // how far ahead get_short_epg looks
	defaultEPGPeriod = 3              // hours that get_epg_info covers without a period
	maxEPGPeriod     = 24
)

// epgProgramme is a programme in the shape the protocol notes describe:
// numbers and times as strings, times in Unix seconds. mark_archive is "1"
// for a programme that plays from its channel's archive (D81).
type epgProgramme struct {
	ID             string `json:"id"`
	ChID           string `json:"ch_id"`
	Name           string `json:"name"`
	Descr          string `json:"descr"`
	Category       string `json:"category"`
	StartTimestamp string `json:"start_timestamp"`
	StopTimestamp  string `json:"stop_timestamp"`
	MarkArchive    string `json:"mark_archive"`
}

// epgItem returns a programme of a lineup entry's channel in its Stalker
// shape, marked as one that plays from the channel's archive if it has
// ended within the days that the archive reaches back.
func epgItem(entryID int64, p store.Programme, days int, now time.Time) epgProgramme {
	mark := "0"
	if store.InArchive(p, days, now) {
		mark = "1"
	}
	return epgProgramme{
		ID:             programmeID(entryID, p.Start),
		ChID:           strconv.FormatInt(entryID, 10),
		Name:           p.Title,
		Descr:          p.Description,
		Category:       p.Category,
		StartTimestamp: strconv.FormatInt(p.Start.Unix(), 10),
		StopTimestamp:  strconv.FormatInt(p.Stop.Unix(), 10),
		MarkArchive:    mark,
	}
}

// minuteBits is how many low bits of a programme's ID hold its start, in
// whole minutes since 1970: enough until 2097.
const minuteBits = 26

// programmeID returns the ID that clients know a programme by: its
// channel's ID and its start, in whole minutes, as one number, so that it
// stays the same when the guide is read again (D81).
func programmeID(entryID int64, start time.Time) string {
	return strconv.FormatInt(entryID<<minuteBits|(start.Unix()/60)&(1<<minuteBits-1), 10)
}

// programmeOf returns the channel and the minute of the start that a
// programme's ID names.
func programmeOf(id string) (entryID int64, minute time.Time, ok bool) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, time.Time{}, false
	}
	entryID = n >> minuteBits
	return entryID, time.Unix((n&(1<<minuteBits-1))*60, 0).UTC(), entryID > 0
}

// deviceZone returns the time zone that a device's timezone cookie names,
// and the server's own if it names none that is known.
func (a *API) deviceZone(r *http.Request) *time.Location {
	if c, err := r.Cookie("timezone"); err == nil {
		if name, err := url.QueryUnescape(c.Value); err == nil && name != "" && len(name) < 64 {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc
			}
		}
	}
	return a.local
}

// lineupID returns the lineup of d's account.
func (a *API) lineupID(r *http.Request, d store.Device) (int64, error) {
	acct, err := a.store.Account(r.Context(), d.AccountID)
	return acct.LineupID, err
}

// entryProgrammes returns the programmes of the channel that the ch_id
// parameter names, in d's lineup, between from and to. A channel that d
// does not see has none.
func (a *API) entryProgrammes(r *http.Request, d store.Device, from, to time.Time) (int64, []store.Programme, error) {
	lineupID, err := a.lineupID(r, d)
	if err != nil {
		return 0, nil, err
	}
	entryID, err := strconv.ParseInt(r.Form.Get("ch_id"), 10, 64)
	if err != nil || entryID <= 0 {
		return 0, nil, nil
	}
	ps, err := a.store.EntryProgrammes(r.Context(), lineupID, entryID, from, to)
	if errors.Is(err, store.ErrNotFound) {
		return entryID, nil, nil
	}
	return entryID, ps, err
}

// getShortEPG answers itv/get_short_epg: the programme on now on the
// channel ch_id, and the ones after it, size of them in all.
func (a *API) getShortEPG(w http.ResponseWriter, r *http.Request, d store.Device) {
	size, err := strconv.Atoi(r.Form.Get("size"))
	if err != nil || size <= 0 {
		size = shortEPGSize
	}
	size = min(size, maxShortEPGSize)
	now := a.now()
	entryID, ps, err := a.entryProgrammes(r, d, now, now.Add(shortEPGAhead))
	if err != nil {
		a.internalError(w, "read the guide", err)
		return
	}
	out := make([]epgProgramme, 0, min(size, len(ps)))
	for _, p := range ps[:min(size, len(ps))] {
		out = append(out, epgItem(entryID, p, 0, now)) // none has ended
	}
	writeJS(w, out)
}

// getEPGInfo answers itv/get_epg_info: the programmes of every channel in
// the device's lineup over the next period hours, by channel ID. Channels
// without any are left out.
func (a *API) getEPGInfo(w http.ResponseWriter, r *http.Request, d store.Device) {
	hours, err := strconv.Atoi(r.Form.Get("period"))
	if err != nil || hours <= 0 {
		hours = defaultEPGPeriod
	}
	hours = min(hours, maxEPGPeriod)
	lineupID, err := a.lineupID(r, d)
	if err != nil {
		a.internalError(w, "read the guide", err)
		return
	}
	now := a.now()
	byEntry, err := a.store.LineupProgrammes(r.Context(), lineupID, now, now.Add(time.Duration(hours)*time.Hour))
	if err != nil {
		a.internalError(w, "read the guide", err)
		return
	}
	data := make(map[string][]epgProgramme, len(byEntry))
	for entryID, ps := range byEntry {
		items := make([]epgProgramme, len(ps))
		for i, p := range ps {
			items[i] = epgItem(entryID, p, 0, now) // none has ended
		}
		data[strconv.FormatInt(entryID, 10)] = items
	}
	writeJS(w, struct {
		Data map[string][]epgProgramme `json:"data"`
	}{data})
}

// epgPage is a page of a day's programmes on a channel. SelectedItem is the
// place on the page, from 1, of the programme on now, or 0 if none of the
// page's is; CurPage is the page's number, from 1.
type epgPage struct {
	TotalItems   int            `json:"total_items"`
	MaxPageItems int            `json:"max_page_items"`
	SelectedItem int            `json:"selected_item"`
	CurPage      int            `json:"cur_page"`
	Data         []epgProgramme `json:"data"`
}

// getSimpleDataTable answers epg/get_simple_data_table: the programmes that
// start on the date named, in the device's time zone, on the channel ch_id,
// a page at a time. Without a date it is today; without a page (p) it is
// the one with the programme on now, if the day has it, and the first
// otherwise.
func (a *API) getSimpleDataTable(w http.ResponseWriter, r *http.Request, d store.Device) {
	loc := a.deviceZone(r)
	now := a.now().In(loc)
	day, err := time.ParseInLocation("2006-01-02", r.Form.Get("date"), loc)
	if err != nil {
		day = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	}
	next := day.AddDate(0, 0, 1)
	entryID, ps, err := a.entryProgrammes(r, d, day, next)
	if err != nil {
		a.internalError(w, "read the guide", err)
		return
	}
	days := 0
	if len(ps) > 0 {
		lineupID, err := a.lineupID(r, d)
		if err == nil {
			days, err = a.store.EntryArchiveDays(r.Context(), lineupID, entryID)
		}
		if err != nil {
			a.internalError(w, "read the guide", err)
			return
		}
	}
	// The day's own programmes: those that start on it.
	var own []store.Programme
	current := -1
	for _, p := range ps {
		if p.Start.Before(day) {
			continue
		}
		if !p.Start.After(now) && p.Stop.After(now) {
			current = len(own)
		}
		own = append(own, p)
	}
	pages := max(1, (len(own)+epgPageSize-1)/epgPageSize)
	pg, err := strconv.Atoi(r.Form.Get("p"))
	if err != nil || pg <= 0 {
		pg = 1
		if current >= 0 {
			pg = current/epgPageSize + 1
		}
	}
	pg = min(pg, pages)
	start := min((pg-1)*epgPageSize, len(own))
	end := min(start+epgPageSize, len(own))
	out := epgPage{TotalItems: len(own), MaxPageItems: epgPageSize, CurPage: pg, Data: make([]epgProgramme, 0, end-start)}
	for i, p := range own[start:end] {
		if start+i == current {
			out.SelectedItem = i + 1
		}
		out.Data = append(out.Data, epgItem(entryID, p, days, a.now()))
	}
	writeJS(w, out)
}
