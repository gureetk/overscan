// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"net/http"
	"strconv"

	"overscan.gureet.ca/internal/store"
)

// stalkerDate is the date form Ministra-style portals use for end dates. The
// protocol notes list it among the forms clients accept.
const stalkerDate = "2006-01-02 15:04:05"

// handshake gives d a new token, which replaces any earlier one (protocol
// notes, section 3). A token the client proposes is ignored.
func (a *API) handshake(w http.ResponseWriter, r *http.Request, d store.Device) {
	token := newToken()
	if err := a.store.SetStalkerToken(r.Context(), d.ID, hashToken(token), a.now()); err != nil {
		a.internalError(w, "store token", err)
		return
	}
	writeJS(w, struct {
		Token string `json:"token"`
	}{token})
}

// profile is the part of a Ministra-style profile that the protocol notes
// describe. Numbers are strings, as such portals send them.
type profile struct {
	ID                string `json:"id"`
	Blocked           string `json:"blocked"`
	WatchdogTimeout   string `json:"watchdog_timeout"`
	ExpireBillingDate string `json:"expire_billing_date,omitempty"`
}

// getProfile answers stb/get_profile. The hardware identity parameters the
// client sends (sn, device_id, signature and so on) are not checked yet. A
// device whose account has ended reads that it is blocked, with its end
// date (D95).
func (a *API) getProfile(w http.ResponseWriter, r *http.Request, d store.Device) {
	p := profile{
		ID:              strconv.FormatInt(d.ID, 10),
		Blocked:         "0",
		WatchdogTimeout: strconv.Itoa(watchdogTimeout),
	}
	if d.State == store.DeviceBlocked {
		p.Blocked = "1"
	}
	if d.AccountID != 0 {
		acct, err := a.store.Account(r.Context(), d.AccountID)
		if err != nil {
			a.internalError(w, "look up account", err)
			return
		}
		if !acct.ExpiresAt.IsZero() {
			p.ExpireBillingDate = acct.ExpiresAt.UTC().Format(stalkerDate)
		}
		if acct.Ended(a.now()) {
			p.Blocked = "1" // as a blocked device is refused (D95)
		}
	}
	writeJS(w, p)
}

// doAuth answers stb/do_auth. Accounts have no logins yet (they arrive with
// the hub stage), so there is nothing to check the login and password
// against, and the device's approval stands.
func (a *API) doAuth(w http.ResponseWriter, _ *http.Request, _ store.Device) {
	writeJS(w, true)
}

// getMainInfo answers account_info/get_main_info with the account's end date,
// which clients read from end_date. An account without an expiry has none.
func (a *API) getMainInfo(w http.ResponseWriter, r *http.Request, d store.Device) {
	var info struct {
		EndDate string `json:"end_date,omitempty"`
	}
	acct, err := a.store.Account(r.Context(), d.AccountID)
	if err != nil {
		a.internalError(w, "look up account", err)
		return
	}
	if !acct.ExpiresAt.IsZero() {
		info.EndDate = acct.ExpiresAt.UTC().Format(stalkerDate)
	}
	writeJS(w, info)
}

// getEvents answers watchdog/get_events, the keep-alive. Messages to a device
// will come this way; there are none yet.
func (a *API) getEvents(w http.ResponseWriter, _ *http.Request, _ store.Device) {
	writeJS(w, map[string]any{"data": map[string]int{"msgs": 0}})
}
