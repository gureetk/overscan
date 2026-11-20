// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"overscan.gureet.ca/internal/store"
)

// The viewer's own lists and settings (D53): the favourites of the profile
// the box plays as (D94), and the page's settings on the device.

// favourite answers PUT and DELETE channels/{id}/favourite, which mark a
// channel of the box's lineup as one of its profile's favourites, or
// unmark it, and say which it now is.
func (a *API) favourite(on bool) func(http.ResponseWriter, *http.Request, store.Device) {
	return func(w http.ResponseWriter, r *http.Request, d store.Device) {
		ctx := r.Context()
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil || id <= 0 {
			writeError(w, http.StatusNotFound, errNotFound)
			return
		}
		acct, err := a.store.Account(ctx, d.AccountID)
		if err != nil {
			a.internalError(w, "look up account", err)
			return
		}
		if on {
			// Only a channel the box sees can become a favourite; one that
			// has gone can still be unmarked.
			if err := a.store.EntryShown(ctx, acct.LineupID, id); errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, errNotFound)
				return
			} else if err != nil {
				a.internalError(w, "look up channel", err)
				return
			}
		}
		if err := a.store.SetFavourite(ctx, d.ProfileID, id, on, a.now()); err != nil {
			a.internalError(w, "change a favourite", err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			ID        int64 `json:"id"`
			Favourite bool  `json:"favourite"`
		}{id, on})
	}
}

// settings is how Overscan's page looks and acts on a device (D52, D56,
// D62, D99).
type settings struct {
	Home    string `json:"home"`    // simple, or picture for a picture behind the home screen
	Picture string `json:"picture"` // which of the page's pictures, when home is picture
	Accent  string `json:"accent"`  // teal, white, blue or orange
	Clock   string `json:"clock"`   // 24 or 12
	Numbers string `json:"numbers"` // lineup, favourites (they count from 1) or groups (each counts from 1)
	Groups  string `json:"groups"`  // preview, Live TV shows a group's channels as the focus rests on it, or ok, only on OK or Right
	// Record is how a recording that the box starts on a channel ends:
	// household, as the household's setting says, or the box's own choice,
	// programme or stopped (D99).
	Record string `json:"record"`
}

// recordHousehold is the box's choice to end the recordings it starts on a
// channel as the household's setting says (D99).
const recordHousehold = "household"

// defaultSettings are a device's settings until it changes them.
var defaultSettings = settings{Home: "simple", Picture: "dusk", Accent: "teal", Clock: "24", Numbers: "lineup", Groups: "preview",
	Record: recordHousehold}

// pictureName is the form of a picture's name; the page knows which exist,
// and shows its first for one it does not have.
var pictureName = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

// valid reports whether s holds only values the page understands.
func (s settings) valid() bool {
	return (s.Home == "simple" || s.Home == "picture") &&
		pictureName.MatchString(s.Picture) &&
		(s.Accent == "teal" || s.Accent == "white" || s.Accent == "blue" || s.Accent == "orange") &&
		(s.Clock == "24" || s.Clock == "12") &&
		(s.Numbers == "lineup" || s.Numbers == "favourites" || s.Numbers == "groups") &&
		(s.Groups == "preview" || s.Groups == "ok") &&
		(s.Record == recordHousehold || s.Record == store.RecordUntilProgramme || s.Record == store.RecordUntilStopped)
}

// deviceSettings returns d's settings, with the defaults for any it has not
// set or that the page no longer understands.
func (a *API) deviceSettings(r *http.Request, d store.Device) (settings, error) {
	stored, err := a.store.DeviceSettings(r.Context(), d.ID)
	if err != nil || stored == "" {
		return defaultSettings, err
	}
	s := defaultSettings
	if err := json.Unmarshal([]byte(stored), &s); err != nil || !s.valid() {
		return defaultSettings, nil
	}
	return s, nil
}

// getSettings answers GET settings with the page's settings on the device.
func (a *API) getSettings(w http.ResponseWriter, r *http.Request, d store.Device) {
	s, err := a.deviceSettings(r, d)
	if err != nil {
		a.internalError(w, "read settings", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// patchSettings answers PATCH settings: it changes the settings the body
// gives and keeps the others, and answers with them all.
func (a *API) patchSettings(w http.ResponseWriter, r *http.Request, d store.Device) {
	s, err := a.deviceSettings(r, d)
	if err != nil {
		a.internalError(w, "read settings", err)
		return
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil || !s.valid() {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	b, _ := json.Marshal(s) // a struct of strings always marshals
	if err := a.store.SetDeviceSettings(r.Context(), d.ID, string(b)); err != nil {
		a.internalError(w, "save settings", err)
		return
	}
	writeJSON(w, http.StatusOK, s)
}

// Limits of a search.
const (
	maxQueryLen         = 64
	maxSearchChannels   = 50
	maxSearchProgrammes = 30
	maxSearchTitles     = 30             // of films, and of series
	searchAhead         = 24 * time.Hour // how far ahead programmes are searched
)

// searchProgramme is a programme that a search found, on a channel of the
// box's lineup.
type searchProgramme struct {
	Channel int64 `json:"channel"`
	programme
}

// searchResponse is what a search found: channels by number, and programmes
// on now or later, the earliest first.
type searchResponse struct {
	Channels   []int64           `json:"channels"`
	Programmes []searchProgramme `json:"programmes"`
	Movies     []titleRow        `json:"movies"`
	Series     []titleRow        `json:"series"`
}

// search answers GET search?q=: the channels of the box's lineup whose name
// holds q, or whose number is q, the programmes whose title holds q that are
// on now or in the next 24 hours, and the films and series whose name holds
// q. Case is ignored.
func (a *API) search(w http.ResponseWriter, r *http.Request, d store.Device) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" || utf8.RuneCountInString(q) > maxQueryLen {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	ctx := r.Context()
	acct, err := a.store.Account(ctx, d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	entries, err := a.store.LineupChannels(ctx, acct.LineupID)
	if err != nil {
		a.internalError(w, "list channels", err)
		return
	}
	resp := searchResponse{Channels: []int64{}, Programmes: []searchProgramme{}}
	lower := strings.ToLower(q)
	for _, c := range entries {
		if len(resp.Channels) == maxSearchChannels {
			break
		}
		if strings.Contains(strings.ToLower(c.Name), lower) || strconv.Itoa(c.Number) == q {
			resp.Channels = append(resp.Channels, c.EntryID)
		}
	}
	now := a.now()
	found, err := a.store.SearchProgrammes(ctx, acct.LineupID, q, now, now.Add(searchAhead), maxSearchProgrammes)
	if err != nil {
		a.internalError(w, "search the guide", err)
		return
	}
	for _, p := range found {
		resp.Programmes = append(resp.Programmes, searchProgramme{Channel: p.EntryID, programme: shortProgramme(p.Programme)})
	}
	if resp.Movies, resp.Series, err = a.searchTitles(ctx, d, q); err != nil {
		a.internalError(w, "search the films and series", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
