// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Catch-up (section 5). A portal's channel with an archive plays its past
// programmes from it. A box finds a past programme in the channel's guide
// of the day it began, where the programmes that play are marked
// (mark_archive), and asks for its link with tv_archive/create_link.

const (
	// archiveMatch is how far the start of a programme in the portal's
	// day guide may be from the start asked for, as the reference client
	// matches them (section 5).
	archiveMatch = 5 * time.Minute
	// maxDayPages caps the pages of a day's guide read for one programme.
	maxDayPages = 20
	// maxProgrammeID caps a programme's ID, which goes into a cmd.
	maxProgrammeID = 64
)

// ErrNotArchived is returned when the portal's guide of the day has no
// programme in its archive that starts near the time asked for.
var ErrNotArchived = errors.New("the portal's archive has no programme that starts then")

// dayJSON is a page of a channel's day guide (section 5), in the form of
// a list's page (section 1).
type dayJSON struct {
	TotalItems   number              `json:"total_items"`
	MaxPageItems number              `json:"max_page_items"`
	Data         list[programmeJSON] `json:"data"`
}

// ArchiveLink asks the portal for the address that plays a channel's past
// programme, which began at start, from the portal's archive. It reads the
// channel's programmes of the day that the programme began on, in the zone
// that the box's cookie names, with epg/get_simple_data_table a page at a
// time, and asks for the link of the programme in the archive whose start
// is nearest, within five minutes, with tv_archive/create_link (section
// 5). Where that day has none, it reads the day before, or the day after
// for a programme that began after midday, since a portal may read dates
// in a zone of its own. A *LinkError says why the portal gave no link.
func (c *Client) ArchiveLink(ctx context.Context, channelID string, start time.Time) (string, error) {
	zone, err := time.LoadLocation(c.config.TimeZone)
	if err != nil {
		zone = time.UTC
	}
	day := start.In(zone)
	other := day.AddDate(0, 0, -1)
	if day.Hour() >= 12 {
		other = day.AddDate(0, 0, 1)
	}
	id := ""
	for _, d := range []time.Time{day, other} {
		if id, err = c.archived(ctx, channelID, d.Format(time.DateOnly), start); err != nil || id != "" {
			break
		}
	}
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", ErrNotArchived
	}
	var answer linkJSON
	err = c.call(ctx, "tv_archive", "create_link", url.Values{"cmd": {"auto /media/" + id + ".mpg"}, "series": {""},
		"forced_storage": {"undefined"}, "disable_ad": {"0"}, "download": {"0"}}, &answer)
	if err != nil {
		return "", err
	}
	return c.link(answer)
}

// archived returns the ID of the programme in the archive, in a channel's
// guide of the day date, whose start is nearest start, within
// archiveMatch; "" if there is none. It reads the day's pages in turn until
// one reaches past that, or the day ends.
func (c *Client) archived(ctx context.Context, channelID, date string, start time.Time) (string, error) {
	best, bestOff := "", archiveMatch+time.Second
	read, firstOfLast := 0, ""
	for p := 1; p <= maxDayPages; p++ {
		var page dayJSON
		err := c.call(ctx, "epg", "get_simple_data_table", url.Values{"ch_id": {channelID}, "date": {date},
			"p": {strconv.Itoa(p)}}, &page)
		if err != nil {
			return "", err
		}
		if len(page.Data) == 0 {
			break // the day has ended
		}
		first := string(page.Data[0].ID) + "@" + strconv.FormatInt(int64(page.Data[0].Start), 10)
		if first == firstOfLast {
			break // the portal gives the same page whatever p asks
		}
		firstOfLast = first
		var last time.Time
		for _, prog := range page.Data {
			if prog.Start <= 0 {
				continue
			}
			at := time.Unix(int64(prog.Start), 0)
			if at.After(last) {
				last = at
			}
			id := strings.TrimSpace(string(prog.ID))
			if !bool(prog.MarkArchive) || !validProgrammeID(id) {
				continue
			}
			if off := at.Sub(start).Abs(); off <= archiveMatch && off < bestOff {
				best, bestOff = id, off
			}
		}
		read += len(page.Data)
		if bestOff == 0 || last.After(start.Add(archiveMatch)) || page.TotalItems > 0 && int64(read) >= int64(page.TotalItems) ||
			page.MaxPageItems > 0 && int64(len(page.Data)) < int64(page.MaxPageItems) {
			break
		}
	}
	return best, nil
}

// validProgrammeID reports whether a programme's ID can go into a cmd:
// letters, digits and a few marks, and not too long.
func validProgrammeID(id string) bool {
	if id == "" || len(id) > maxProgrammeID {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' || r == '.' || r == ':') {
			return false
		}
	}
	return true
}
