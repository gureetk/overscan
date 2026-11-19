// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"net/http"

	"overscan.gureet.ca/internal/store"
)

// The parental PIN (D55): four to eight digits, kept as an argon2id hash on
// the household account. Overscan's page asks for it before it plays a
// locked channel.

const (
	minPINDigits = 4
	maxPINDigits = 8
)

// checkPIN returns why pin cannot be a PIN, if it cannot.
func checkPIN(pin string) error {
	if len(pin) < minPINDigits || len(pin) > maxPINDigits {
		return badEdit(http.StatusBadRequest, "bad_pin", "A PIN has %d to %d digits.", minPINDigits, maxPINDigits)
	}
	for _, r := range pin {
		if r < '0' || r > '9' {
			return badEdit(http.StatusBadRequest, "bad_pin", "A PIN has only digits, which a remote control can type.")
		}
	}
	return nil
}

// pinSet reports whether the household account has a PIN.
func (a *Admin) pinSet(ctx context.Context) (bool, error) {
	acct, err := a.store.Account(ctx, householdAccount)
	return acct.PINHash != "", err
}

// setPIN sets the household account's PIN, or with "" takes it away.
func (a *Admin) setPIN(ctx context.Context, pin string) error {
	hash := ""
	if pin != "" {
		if err := checkPIN(pin); err != nil {
			return err
		}
		var err error
		if hash, err = HashPassword(ctx, pin, a.params); err != nil {
			return err
		}
	}
	if err := a.store.SetAccountPIN(ctx, householdAccount, hash); err != nil {
		return err
	}
	if pin == "" {
		a.log.Info("parental PIN removed in the admin")
	} else {
		a.log.Info("parental PIN set in the admin")
	}
	return nil
}

type apiPINState struct {
	Set bool `json:"set"`
}

func (a *Admin) apiPIN(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	set, err := a.pinSet(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, apiPINState{set})
}

func (a *Admin) apiSetPIN(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	var body struct {
		PIN string `json:"pin"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.PIN == "" {
		apiError(w, http.StatusBadRequest, "bad_pin", "Give the PIN; to remove it, use DELETE.")
		return
	}
	if a.editAnswer(w, a.setPIN(r.Context(), body.PIN)) {
		return
	}
	writeJSON(w, http.StatusOK, apiPINState{true})
}

func (a *Admin) apiRemovePIN(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if a.editAnswer(w, a.setPIN(r.Context(), "")) {
		return
	}
	writeJSON(w, http.StatusOK, apiPINState{false})
}

// pinForm sets or removes the PIN from the Settings page.
func (a *Admin) pinForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	if r.PostFormValue("remove") != "" {
		a.settingsDone(w, r, s, a.setPIN(r.Context(), ""), "Removed the PIN. Locked channels play without one.")
		return
	}
	pin := r.PostFormValue("pin")
	if pin == "" {
		a.settingsDone(w, r, s, badEdit(http.StatusBadRequest, "bad_pin", "Type the new PIN."), "")
		return
	}
	a.settingsDone(w, r, s, a.setPIN(r.Context(), pin), "Set the PIN. Boxes ask for it before they play a locked channel.")
}
