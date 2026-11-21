// SPDX-License-Identifier: AGPL-3.0-or-later

// Package netguard keeps the requests that Overscan makes for a source off
// the server's own networks, when the source says so (D121): such a source
// may reach public addresses only. The check is made on the address that a
// connection goes to, once its name has been looked up, so a name that
// leads to an address on the home network is refused too, and so is a
// redirect to one.
package netguard

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"overscan.gureet.ca/internal/clientaddr"
)

// notPublic are ranges that are not on the server's own networks, but are
// not public either: reserved, or for special uses.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),      // this network
	netip.MustParsePrefix("192.0.0.0/24"),   // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"),  // benchmarking
	netip.MustParsePrefix("240.0.0.0/4"),    // reserved, and the broadcast address
	netip.MustParsePrefix("64:ff9b:1::/48"), // local-use NAT64
	netip.MustParsePrefix("100::/64"),       // discard-only
	netip.MustParsePrefix("2001:2::/48"),    // benchmarking
}

// The ranges that carry an IPv4 address inside an IPv6 one: the IPv4
// address must be public too.
var (
	nat64     = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

// Public reports whether an address is a public one: a unicast address on
// none of the server's own networks (clientaddr.OwnNetwork), nor in a
// range that is reserved or kept for special uses.
func Public(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	if !addr.IsValid() || !addr.IsGlobalUnicast() || clientaddr.OwnNetwork(addr) {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(addr) {
			return false
		}
	}
	switch b := addr.As16(); {
	case nat64.Contains(addr):
		return Public(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
	case sixToFour.Contains(addr):
		return Public(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
	}
	return true
}

// NotPublicError is a connection that was refused because its address is
// not a public one.
type NotPublicError struct {
	Addr netip.Addr
}

func (e *NotPublicError) Error() string {
	return fmt.Sprintf("the source may reach public addresses only, and %s is not one", e.Addr)
}

// ErrNotPublic is what errors.Is finds in a *NotPublicError.
var ErrNotPublic = errors.New("the source may reach public addresses only")

func (e *NotPublicError) Is(target error) bool { return target == ErrNotPublic }

// control refuses a connection to an address that is not public. It runs
// for every address that a connection is tried at, after the name is
// looked up.
func control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return fmt.Errorf("the source may reach public addresses only, and %q is not an address", host)
	}
	if !Public(addr) {
		return &NotPublicError{Addr: addr.Unmap().WithZone("")}
	}
	return nil
}

// guarded holds, for each transport given to Client, its copy whose
// connections may reach public addresses only.
var guarded sync.Map // *http.Transport → *http.Transport

// Client returns c, or, if publicOnly, a client like it whose connections
// may reach public addresses only. Its transport is a copy of c's, kept
// for all such clients of c, which share its connections. A nil c is
// http.DefaultClient. A transport that is not an *http.Transport cannot be
// checked, so such a client refuses every request.
func Client(c *http.Client, publicOnly bool) *http.Client {
	if !publicOnly {
		return c
	}
	if c == nil {
		c = http.DefaultClient
	}
	g := *c
	g.Transport = guard(c.Transport)
	return &g
}

func guard(rt http.RoundTripper) http.RoundTripper {
	if rt == nil {
		rt = http.DefaultTransport
	}
	t, ok := rt.(*http.Transport)
	if !ok {
		return refuse{}
	}
	if g, ok := guarded.Load(t); ok {
		return g.(*http.Transport)
	}
	c := t.Clone()
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second, Control: control}
	c.DialContext = dialer.DialContext
	// Cleared, so that every connection goes through the check.
	c.Dial, c.DialTLS, c.DialTLSContext = nil, nil, nil
	kept, _ := guarded.LoadOrStore(t, c)
	return kept.(*http.Transport)
}

// refuse is the transport of a client whose connections cannot be checked.
type refuse struct{}

func (refuse) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Body != nil {
		r.Body.Close()
	}
	return nil, errors.New("the source may reach public addresses only, which this request cannot check")
}
