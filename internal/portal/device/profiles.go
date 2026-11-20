// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"encoding/json"
	"errors"
	"net/http"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/theme"
)

// Profiles (D94): who watches on the box. The box plays as one of its
// account's profiles, whose favourites and progress it shows, and switches
// to another, with that profile's PIN if it has one.

// profileView is a profile as the box sees it.
type profileView struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	PIN  bool   `json:"pin,omitempty"` // switching to it asks for its PIN
}

// profilesResponse is the account's profiles, and the one the box plays
// as, with the stylesheet of that profile's theme, beside the page, if it
// has one other than Overscan's own (D97).
type profilesResponse struct {
	Profile  int64         `json:"profile"`
	Profiles []profileView `json:"profiles"`
	Theme    string        `json:"theme,omitempty"`
}

// profilesOf returns what GET profiles answers for d.
func (a *API) profilesOf(r *http.Request, d store.Device) (profilesResponse, error) {
	list, err := a.store.Profiles(r.Context(), d.AccountID)
	if err != nil {
		return profilesResponse{}, err
	}
	resp := profilesResponse{Profile: d.ProfileID, Profiles: make([]profileView, len(list))}
	for i, p := range list {
		resp.Profiles[i] = profileView{ID: p.ID, Name: p.Name, PIN: p.PINHash != ""}
	}
	th, ok, err := a.store.ProfileTheme(r.Context(), d.ProfileID)
	if err != nil {
		return profilesResponse{}, err
	}
	if ok {
		resp.Theme = theme.Path(th.ID, th.Data)
	}
	return resp, nil
}

// profiles answers GET profiles: the account's profiles, the first first,
// and the one the box plays as.
func (a *API) profiles(w http.ResponseWriter, r *http.Request, d store.Device) {
	resp, err := a.profilesOf(r, d)
	if err != nil {
		a.internalError(w, "list profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// switchRequest is the body of PUT profile.
type switchRequest struct {
	ID  int64  `json:"id"`
	PIN string `json:"pin"`
}

// switchProfile answers PUT profile, which makes the box play as another
// of its account's profiles, with that profile's PIN if it has one; wrong
// PINs count as the parental PIN's do. It answers as GET profiles does.
func (a *API) switchProfile(w http.ResponseWriter, r *http.Request, d store.Device) {
	var req switchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ID <= 0 {
		writeError(w, http.StatusBadRequest, errBadRequest)
		return
	}
	ctx := r.Context()
	p, err := a.store.Profile(ctx, req.ID)
	if errors.Is(err, store.ErrNotFound) || err == nil && p.AccountID != d.AccountID {
		writeError(w, http.StatusNotFound, errNotFound)
		return
	}
	if err != nil {
		a.internalError(w, "look up a profile", err)
		return
	}
	if p.ID != d.ProfileID && p.PINHash != "" && !a.pinMatches(w, r, d, p.PINHash, req.PIN, "wrong profile PIN") {
		return
	}
	if p.ID != d.ProfileID {
		if err := a.store.SetDeviceProfile(ctx, d.ID, p.ID); err != nil {
			a.internalError(w, "switch profile", err)
			return
		}
		a.log.Info("a box switched profile", "mac", d.MAC, "label", d.Label, "profile", p.Name)
		d.ProfileID = p.ID
	}
	resp, err := a.profilesOf(r, d)
	if err != nil {
		a.internalError(w, "list profiles", err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
