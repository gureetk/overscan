// SPDX-License-Identifier: AGPL-3.0-or-later

// Package output holds what the outputs that players use share (D103):
// whether they answer at all and to whom, how they name the server in the
// addresses they give, and how a player's stream reaches the hub.
package output

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
)

// cgnat is the shared address space of RFC 6598, which carrier-grade NAT
// and some VPNs, such as Tailscale, use inside a network of their own.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// OwnNetwork reports whether an address is on one of this server's own
// networks, as far as an address can say: private (RFC 1918 and RFC
// 4193), loopback, link-local, or the shared space of RFC 6598.
func OwnNetwork(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr)
}

// ClientAddress returns the address that a request came from, without its
// port.
func ClientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Refusal is why an output does not answer: a status and a short text,
// which never says more than the player needs.
type Refusal struct {
	Status int
	Text   string
}

// Gate says whether the outputs answer a request at all: while they are
// off, nothing is there (404); a request from outside the server's own
// networks is refused unless the outputs answer anywhere (403). It returns
// nil if they answer.
func Gate(ctx context.Context, st *store.Store, r *http.Request) (*Refusal, error) {
	on, err := st.Outputs(ctx)
	if err != nil {
		return nil, err
	}
	if !on {
		return &Refusal{http.StatusNotFound, "404 page not found"}, nil
	}
	anywhere, err := st.OutputsAnywhere(ctx)
	if err != nil {
		return nil, err
	}
	if !anywhere {
		addr, err := netip.ParseAddr(ClientAddress(r))
		if err != nil || !OwnNetwork(addr) {
			return &Refusal{http.StatusForbidden, "Overscan's outputs answer players on its own networks only."}, nil
		}
	}
	return nil, nil
}

// Refuse answers a refused request.
func (f *Refusal) Refuse(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, f.Text, f.Status)
}

// Base returns the scheme and host that a player reached the server at,
// such as http://192.0.2.10:8080, for the addresses an output gives it. A
// proxy in front that says it was asked over HTTPS makes them https: the
// header can only change what the player that sent it is given.
func Base(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// The outputs, as the hub knows the players that come through them.
const (
	M3U    = "m3u"    // the playlist and guide with a token (D103)
	Xtream = "xtream" // the Xtream-compatible API, with a login (D104)
)

// Player returns the hub's device ID of the player that made a request
// through an output, as a player of an account (D103).
func Player(h *hub.Hub, accountID int64, output string, r *http.Request) int64 {
	return h.Player(accountID, output, ClientAddress(r), r.UserAgent())
}

// Channels returns the channels that an account's players see: those its
// lineup shows, of the sources it uses (D114), without the locked ones once
// it has a parental PIN, since a player cannot ask for one (D55).
func Channels(ctx context.Context, st *store.Store, acct store.Account) ([]store.LineupChannel, error) {
	all, err := st.AccountChannels(ctx, acct)
	if err != nil {
		return nil, err
	}
	out := all[:0]
	for _, c := range all {
		if !(c.Locked && acct.PINHash != "") {
			out = append(out, c)
		}
	}
	return out, nil
}

// Continuous returns a channel's link as a continuous MPEG-TS stream: the
// hub relays an HLS source as one (D42), so that every player gets the
// same kind of stream from the same address.
func Continuous(link string) string {
	if t, ok := strings.CutSuffix(link, ".m3u8"); ok {
		return t + ".ts"
	}
	return link
}
