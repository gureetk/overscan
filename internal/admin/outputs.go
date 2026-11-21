// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"net/http"
	"net/netip"
	"slices"

	"overscan.gureet.ca/internal/clientaddr"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
)

// The outputs that players use (D103, D104): each account's playlist and
// guide, and the Xtream-compatible API, behind a switch of the whole server
// that starts off, and each account's own switches. They answer players on
// the server's own networks only, unless the administrator lets them answer
// anywhere. The Accounts page sets both. What a switch, a token or a login
// let a player open stops when it no longer would.

// setOutputsOn turns the outputs on or off.
func (a *Admin) setOutputsOn(ctx context.Context, on bool) error {
	if err := a.store.SetOutputs(ctx, on); err != nil {
		return err
	}
	a.log.Info("the outputs set in the admin", "outputs", on)
	if !on {
		a.stopPlayers(func(hub.Player) bool { return true }, "the outputs were turned off")
	}
	return nil
}

// setOutputsAnywhere lets the outputs answer players from any address, or
// from the server's own networks only.
func (a *Admin) setOutputsAnywhere(ctx context.Context, on bool) error {
	if err := a.store.SetOutputsAnywhere(ctx, on); err != nil {
		return err
	}
	a.log.Info("where the outputs answer set in the admin", "anywhere", on)
	if !on {
		a.stopPlayers(func(p hub.Player) bool {
			addr, err := netip.ParseAddr(p.Address)
			return err != nil || !clientaddr.OwnNetwork(addr)
		}, "the outputs answer players on the server's own networks only")
	}
	return nil
}

// stopPlayers ends the streams of the players that match, reason saying
// why: the hub logs each.
func (a *Admin) stopPlayers(match func(hub.Player) bool, reason string) {
	if a.streams != nil {
		a.streams.StopPlayers(match, reason)
	}
}

// playersOf matches the players of an account that came through the given
// outputs, or through any of them if none is given.
func playersOf(accountID int64, outputs ...string) func(hub.Player) bool {
	return func(p hub.Player) bool {
		return p.AccountID == accountID && (len(outputs) == 0 || slices.Contains(outputs, p.Output))
	}
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
				notice = "The outputs are on: the players of each account whose playlist or Xtream login is on can use them."
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
