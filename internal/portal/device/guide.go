// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"errors"
	"maps"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/store"
)

// The guide (D51) for the box's page: what is on every channel over a few
// hours, for now and next and the guide's grid, and one channel's
// programmes in full, with placeholders where the setting asks for them
// (D142).

// Windows of the guide's calls.
const (
	guideSpan        = 3 * time.Hour  // what GET guide covers without a to
	maxGuideSpan     = 6 * time.Hour  // the most it covers
	channelGuideSpan = 24 * time.Hour // what GET channels/{id}/guide covers without a to
	maxChannelSpan   = 48 * time.Hour

	// maxGuideIDs caps the channels that one GET guide names.
	maxGuideIDs = 100
)

// programme is a programme of a channel. Times are Unix seconds. GET guide
// leaves out the subtitle, description and category, to keep its answer
// small. Placeholder is true of a placeholder programme (D142), which
// cannot be recorded or played from an archive.
type programme struct {
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	SubTitle    string `json:"subtitle,omitempty"`
	Description string `json:"description,omitempty"`
	Category    string `json:"category,omitempty"`
	Start       int64  `json:"start"`
	Stop        int64  `json:"stop"`
	Placeholder bool   `json:"placeholder,omitempty"`
}

func shortProgramme(p store.Programme) programme {
	return programme{ID: p.ID, Title: p.Title, Start: p.Start.Unix(), Stop: p.Stop.Unix(), Placeholder: p.Placeholder}
}

func fullProgramme(p store.Programme) programme {
	out := shortProgramme(p)
	out.SubTitle, out.Description, out.Category = p.SubTitle, p.Description, p.Category
	return out
}

// window reads the from and to parameters, in Unix seconds: from defaults to
// now, to to from plus span, and to must come after from, by at most limit.
func (a *API) window(r *http.Request, span, limit time.Duration) (time.Time, time.Time, bool) {
	from, to := a.now().Truncate(time.Second), time.Time{}
	q := r.URL.Query()
	if v := q.Get("from"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > 1<<40 {
			return from, to, false
		}
		from = time.Unix(n, 0)
	}
	to = from.Add(span)
	if v := q.Get("to"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 || n > 1<<40 {
			return from, to, false
		}
		to = time.Unix(n, 0)
	}
	return from, to, to.After(from) && to.Sub(from) <= limit
}

// guideResponse holds, by channel ID, each channel's programmes in the
// window. Channels without any are left out.
type guideResponse struct {
	From     int64                  `json:"from"`
	To       int64                  `json:"to"`
	Channels map[string][]programme `json:"channels"`
}

// guideIDs reads GET guide's ids parameter: lineup entry IDs separated by
// commas, at most maxGuideIDs. None means every channel.
func guideIDs(r *http.Request) ([]int64, bool) {
	v := r.URL.Query().Get("ids")
	if v == "" {
		return nil, true
	}
	parts := strings.Split(v, ",")
	if len(parts) > maxGuideIDs {
		return nil, false
	}
	ids := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil || n <= 0 {
			return nil, false
		}
		ids = append(ids, n)
	}
	return ids, true
}

// guide answers GET guide: what is on every channel of the box's lineup, or
// on the channels that ids names, between from and to.
func (a *API) guide(w http.ResponseWriter, r *http.Request, d store.Device) {
	from, to, ok := a.window(r, guideSpan, maxGuideSpan)
	ids, idsOK := guideIDs(r)
	if !ok || !idsOK {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	byEntry, err := a.store.LineupListings(r.Context(), acct.LineupID, from, to, a.now(), ids...)
	if err != nil {
		a.internalError(w, "read the guide", err)
		return
	}
	if acct.Sources.Only {
		// Only the channels of the sources that the account uses (D114).
		seen, err := a.store.AccountEntries(r.Context(), acct)
		if err != nil {
			a.internalError(w, "read the guide", err)
			return
		}
		maps.DeleteFunc(byEntry, func(id int64, _ []store.Programme) bool { return !seen[id] })
	}
	resp := guideResponse{From: from.Unix(), To: to.Unix(), Channels: make(map[string][]programme, len(byEntry))}
	for entryID, ps := range byEntry {
		items := make([]programme, len(ps))
		for i, p := range ps {
			items[i] = shortProgramme(p)
		}
		resp.Channels[strconv.FormatInt(entryID, 10)] = items
	}
	writeJSON(w, http.StatusOK, resp)
}

// channelGuideResponse holds a channel's programmes in the window, in full.
type channelGuideResponse struct {
	From       int64       `json:"from"`
	To         int64       `json:"to"`
	Programmes []programme `json:"programmes"`
}

// channelGuide answers GET channels/{id}/guide: one channel's programmes
// between from and to, with their descriptions.
func (a *API) channelGuide(w http.ResponseWriter, r *http.Request, d store.Device) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	from, to, ok := a.window(r, channelGuideSpan, maxChannelSpan)
	if !ok {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if acct.Sources.Only {
		if err := a.store.AccountSees(r.Context(), acct, id); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, errNotFound)
			} else {
				a.internalError(w, "read the guide", err)
			}
			return
		}
	}
	ps, err := a.store.EntryListings(r.Context(), acct.LineupID, id, from, to, a.now())
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "read the guide", err)
		return
	}
	resp := channelGuideResponse{From: from.Unix(), To: to.Unix(), Programmes: make([]programme, len(ps))}
	for i, p := range ps {
		resp.Programmes[i] = fullProgramme(p)
	}
	writeJSON(w, http.StatusOK, resp)
}
