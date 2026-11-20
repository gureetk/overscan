// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/store"
)

// Profiles (D94): who watches on the household's devices. Each keeps its
// own favourites and how far it got into films and episodes. The Devices
// page lists the household's profiles, adds, renames and removes them,
// sets or removes the PIN that a box asks for before it switches to one,
// and chooses the profile a device plays as, which is how a Stalker client
// gets one. These wrap the store's functions, as the device functions do.

// profileStoreError turns the store's errors about a profile into mistakes
// that the request can put right.
func profileStoreError(err error) error {
	switch {
	case errors.Is(err, store.ErrProfileName):
		return badDevice(http.StatusBadRequest, "bad_name",
			"A profile's name has 1 to %d characters, none of them control characters, and is not another profile's.", store.MaxProfileName)
	case errors.Is(err, store.ErrTooManyProfiles):
		return badDevice(http.StatusConflict, "too_many_profiles", "A household can have at most %d profiles.", store.MaxProfiles)
	case errors.Is(err, store.ErrLastProfile):
		return badDevice(http.StatusConflict, "last_profile", "The household's only profile cannot be removed.")
	case errors.Is(err, store.ErrNotFound):
		return badDevice(http.StatusNotFound, "no_such_profile", "There is no such profile.")
	}
	return err
}

// householdProfile returns one of the household's profiles.
func (a *Admin) householdProfile(ctx context.Context, id int64) (store.Profile, error) {
	p, err := a.store.Profile(ctx, id)
	if errors.Is(err, store.ErrNotFound) || err == nil && p.AccountID != householdAccount {
		return store.Profile{}, badDevice(http.StatusNotFound, "no_such_profile", "There is no profile %d.", id)
	}
	return p, err
}

// addProfile adds a profile to the household.
func (a *Admin) addProfile(ctx context.Context, name string) (store.Profile, error) {
	p, err := a.store.AddProfile(ctx, householdAccount, name)
	if err != nil {
		return store.Profile{}, profileStoreError(err)
	}
	a.log.Info("profile added in the admin", "profile", p.Name)
	return p, nil
}

// renameProfile gives one of the household's profiles another name.
func (a *Admin) renameProfile(ctx context.Context, id int64, name string) (store.Profile, error) {
	old, err := a.householdProfile(ctx, id)
	if err != nil {
		return store.Profile{}, err
	}
	if err := a.store.RenameProfile(ctx, id, name); err != nil {
		return store.Profile{}, profileStoreError(err)
	}
	p, err := a.store.Profile(ctx, id)
	if err != nil {
		return store.Profile{}, err
	}
	if p.Name != old.Name {
		a.log.Info("profile renamed in the admin", "profile", p.Name, "was", old.Name)
	}
	return p, nil
}

// setProfilePIN sets the PIN that a box asks for before it switches to one
// of the household's profiles, or with "" takes it away.
func (a *Admin) setProfilePIN(ctx context.Context, id int64, pin string) (store.Profile, error) {
	p, err := a.householdProfile(ctx, id)
	if err != nil {
		return store.Profile{}, err
	}
	hash := ""
	if pin != "" {
		if err := checkPIN(pin); err != nil {
			var bad *editError
			if errors.As(err, &bad) {
				return store.Profile{}, badDevice(bad.status, bad.code, "%s", bad.text)
			}
			return store.Profile{}, err
		}
		if hash, err = HashPassword(ctx, pin, a.params); err != nil {
			return store.Profile{}, err
		}
	}
	if err := a.store.SetProfilePIN(ctx, id, hash); err != nil {
		return store.Profile{}, profileStoreError(err)
	}
	if pin == "" {
		a.log.Info("a profile's PIN removed in the admin", "profile", p.Name)
	} else {
		a.log.Info("a profile's PIN set in the admin", "profile", p.Name)
	}
	p.PINHash = hash
	return p, nil
}

// removeProfile removes one of the household's profiles, with its
// favourites and progress; its devices play as the first profile.
func (a *Admin) removeProfile(ctx context.Context, id int64) (store.Profile, error) {
	p, err := a.householdProfile(ctx, id)
	if err != nil {
		return store.Profile{}, err
	}
	if err := a.store.RemoveProfile(ctx, id); err != nil {
		return store.Profile{}, profileStoreError(err)
	}
	a.log.Info("profile removed in the admin", "profile", p.Name, "devices", p.Devices)
	return p, nil
}

// setDeviceProfile makes an approved device of the household play as one of
// its profiles.
func (a *Admin) setDeviceProfile(ctx context.Context, mac string, id int64) (store.Device, store.Profile, error) {
	d, err := a.store.Device(ctx, mac)
	if err != nil {
		return store.Device{}, store.Profile{}, deviceStoreError(err, mac)
	}
	if d.State != store.DeviceApproved || d.AccountID != householdAccount {
		return store.Device{}, store.Profile{}, badDevice(http.StatusConflict, "not_approved",
			"%s is not approved for the household, so it plays as no profile.", deviceName(d))
	}
	p, err := a.householdProfile(ctx, id)
	if err != nil {
		return store.Device{}, store.Profile{}, err
	}
	if err := a.store.SetDeviceProfile(ctx, d.ID, p.ID); err != nil {
		return store.Device{}, store.Profile{}, profileStoreError(err)
	}
	if d.ProfileID != p.ID {
		a.log.Info("a device's profile chosen in the admin", "mac", d.MAC, "label", d.Label, "profile", p.Name)
	}
	d.ProfileID = p.ID
	return d, p, nil
}

// The page's forms.

// profileAction serves the profile forms of the devices page: adding one,
// and the path's last part for one of them, rename, pin, remove-pin or
// remove.
func (a *Admin) profileAction(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	action := r.PathValue("action")
	var id int64
	if action != "" {
		n, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			a.notFound(w, r)
			return
		}
		id = n
	}
	var notice string
	var err error
	var p store.Profile
	switch action {
	case "":
		if p, err = a.addProfile(ctx, r.PostFormValue("name")); err == nil {
			notice = "Added the profile " + p.Name + ". Boxes can switch to it now."
		}
	case "rename":
		if p, err = a.renameProfile(ctx, id, r.PostFormValue("name")); err == nil {
			notice = "Renamed the profile " + p.Name + "."
		}
	case "pin":
		pin := r.PostFormValue("pin")
		if pin == "" {
			err = badDevice(http.StatusBadRequest, "bad_pin", "Type the PIN.")
		} else if p, err = a.setProfilePIN(ctx, id, pin); err == nil {
			notice = "Set a PIN for " + p.Name + ". Boxes ask for it before they switch to " + p.Name + "."
		}
	case "remove-pin":
		if p, err = a.setProfilePIN(ctx, id, ""); err == nil {
			notice = "Removed the PIN of " + p.Name + "."
		}
	case "remove":
		if p, err = a.removeProfile(ctx, id); err == nil {
			notice = "Removed the profile " + p.Name + ", with its favourites and where it stopped."
		}
	default:
		a.notFound(w, r)
		return
	}
	a.devicesDone(w, r, s, err, notice, devicesData{ProfileName: truncate(strings.TrimSpace(r.PostFormValue("name")), 400)})
}

// The API.

// apiProfile is a profile as the API describes it.
type apiProfile struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	First   bool   `json:"first"`
	PIN     bool   `json:"pin"`
	Devices int    `json:"devices"`
}

func profileBody(p store.Profile) apiProfile {
	return apiProfile{ID: p.ID, Name: p.Name, First: p.First, PIN: p.PINHash != "", Devices: p.Devices}
}

// profileAnswer answers with one of the household's profiles, as it is now,
// or with what went wrong.
func (a *Admin) profileAnswer(w http.ResponseWriter, r *http.Request, id int64, err error) {
	if a.profileFailed(w, err) {
		return
	}
	p, err := a.store.Profile(r.Context(), id)
	if a.profileFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, profileBody(p))
}

// profileFailed answers for err, if there is one.
func (a *Admin) profileFailed(w http.ResponseWriter, err error) bool {
	var bad *deviceError
	var badPIN *editError
	switch {
	case err == nil:
		return false
	case errors.As(err, &bad):
		apiError(w, bad.status, bad.code, bad.text)
	case errors.As(err, &badPIN):
		apiError(w, badPIN.status, badPIN.code, badPIN.text)
	default:
		a.apiInternal(w, "cannot change a profile", err)
	}
	return true
}

func (a *Admin) apiProfiles(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	list, err := a.store.Profiles(r.Context(), householdAccount)
	if err != nil {
		a.apiInternal(w, "cannot list the profiles", err)
		return
	}
	out := make([]apiProfile, len(list))
	for i, p := range list {
		out[i] = profileBody(p)
	}
	writeJSON(w, http.StatusOK, struct {
		Profiles []apiProfile `json:"profiles"`
	}{out})
}

func (a *Admin) apiAddProfile(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	p, err := a.addProfile(r.Context(), body.Name)
	if a.profileFailed(w, err) {
		return
	}
	writeJSON(w, http.StatusCreated, profileBody(p))
}

func (a *Admin) apiChangeProfile(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "no_such_profile", "There is no such profile.")
		return
	}
	var body struct {
		Name *string `json:"name"`
		PIN  *string `json:"pin"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Name == nil && body.PIN == nil {
		apiError(w, http.StatusBadRequest, "bad_json", "Say what to change: name, pin or both.")
		return
	}
	ctx := r.Context()
	if _, err := a.householdProfile(ctx, id); a.profileFailed(w, err) {
		return
	}
	if body.PIN != nil && *body.PIN != "" {
		if err := checkPIN(*body.PIN); err != nil {
			a.profileAnswer(w, r, id, err)
			return
		}
	}
	if body.Name != nil {
		if _, err := a.renameProfile(ctx, id, *body.Name); err != nil {
			a.profileAnswer(w, r, id, err)
			return
		}
	}
	if body.PIN != nil {
		_, err = a.setProfilePIN(ctx, id, *body.PIN)
	}
	a.profileAnswer(w, r, id, err)
}

func (a *Admin) apiRemoveProfile(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	id, err := pathID(r)
	if err != nil {
		apiError(w, http.StatusNotFound, "no_such_profile", "There is no such profile.")
		return
	}
	if _, err := a.removeProfile(r.Context(), id); a.profileFailed(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
