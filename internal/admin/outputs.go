// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"net/http"

	"overscan.gureet.ca/internal/store"
)

// The outputs that players use (D103): each account's playlist and guide,
// and the Xtream-compatible API, behind a switch of the whole server that
// starts off, and each account's own switches. They answer players on the
// server's own networks only, unless the administrator lets them answer
// anywhere. The Accounts page sets both.

// setOutputsOn turns the outputs on or off.
func (a *Admin) setOutputsOn(ctx context.Context, on bool) error {
	if err := a.store.SetOutputs(ctx, on); err != nil {
		return err
	}
	a.log.Info("the outputs set in the admin", "outputs", on)
	return nil
}

// setOutputsAnywhere lets the outputs answer players from any address, or
// from the server's own networks only.
func (a *Admin) setOutputsAnywhere(ctx context.Context, on bool) error {
	if err := a.store.SetOutputsAnywhere(ctx, on); err != nil {
		return err
	}
	a.log.Info("where the outputs answer set in the admin", "anywhere", on)
	return nil
}

// outputsForm sets the outputs' switches from the Accounts page.
func (a *Admin) outputsForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	ctx := r.Context()
	var err error
	var notice string
	switch r.PathValue("which") {
	case "on":
		on := r.PostFormValue("on") != ""
		if err = a.setOutputsOn(ctx, on); err == nil {
			notice = "The outputs are off: players get nothing from them."
			if on {
				notice = "The outputs are on: the players of each account whose playlist is on can read it."
			}
		}
	case "anywhere":
		on := r.PostFormValue("on") != ""
		if err = a.setOutputsAnywhere(ctx, on); err == nil {
			notice = "The outputs answer players on this server's own networks only."
			if on {
				notice = "The outputs answer players from any address. Keep the server behind a VPN or a proxy with TLS."
			}
		}
	default:
		a.notFound(w, r)
		return
	}
	a.accountsDone(w, r, s, err, notice, accountsData{})
}
