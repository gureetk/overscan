// SPDX-License-Identifier: AGPL-3.0-or-later

// Package clientaddr tells which address a request came from, and whether
// it was made over HTTPS, through the reverse proxies that the
// configuration trusts (D119). With no proxy trusted, a request comes from
// the address that connected, and the headers that proxies set are
// ignored, as they always were.
package clientaddr

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ownNetworks are the ranges that OwnNetwork accepts, for checking that a
// trusted range lies wholly within them.
var ownNetworks = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),     // RFC 1918
	netip.MustParsePrefix("172.16.0.0/12"),  // RFC 1918
	netip.MustParsePrefix("192.168.0.0/16"), // RFC 1918
	netip.MustParsePrefix("fc00::/7"),       // RFC 4193
	netip.MustParsePrefix("127.0.0.0/8"),    // loopback
	netip.MustParsePrefix("::1/128"),        // loopback
	netip.MustParsePrefix("169.254.0.0/16"), // link-local
	netip.MustParsePrefix("fe80::/10"),      // link-local
	cgnat,
}

// cgnat is the shared address space of RFC 6598, which carriers' NAT and
// some VPNs, such as Tailscale, use inside a network of their own.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// OwnNetwork reports whether an address is on one of this server's own
// networks, as far as an address can say: private (RFC 1918 and RFC
// 4193), loopback, link-local, or the shared space of RFC 6598.
func OwnNetwork(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() || cgnat.Contains(addr.WithZone(""))
}

// maxHops is the most entries of X-Forwarded-For that are read, from the
// right; a chain of trusted proxies longer than that names no client.
const maxHops = 16

// Proxies are the reverse proxies that are taken at their word about whom
// they forward a request for: addresses and ranges on the server's own
// networks. The zero value trusts none.
type Proxies struct {
	prefixes []netip.Prefix
}

// Parse returns the proxies that entries name, each an address such as
// 192.0.2.10 or a range such as 192.0.2.0/24. Each must lie on the server's
// own networks: a proxy anywhere else would let whoever is there say where
// a request came from.
func Parse(entries []string) (Proxies, error) {
	var p Proxies
	for _, e := range entries {
		e = strings.TrimSpace(e)
		var prefix netip.Prefix
		if strings.Contains(e, "/") {
			pr, err := netip.ParsePrefix(e)
			if err != nil {
				return Proxies{}, fmt.Errorf("%q is neither an address nor a range of addresses such as 192.168.1.0/24", e)
			}
			prefix = pr.Masked()
		} else {
			addr, err := netip.ParseAddr(e)
			if err != nil {
				return Proxies{}, fmt.Errorf("%q is neither an address nor a range of addresses such as 192.168.1.0/24", e)
			}
			addr = addr.Unmap().WithZone("")
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if !ownRange(prefix) {
			return Proxies{}, fmt.Errorf("%s is not on the server's own networks: a trusted proxy has a private, loopback, link-local or shared (100.64.0.0/10) address", e)
		}
		p.prefixes = append(p.prefixes, prefix)
	}
	return p, nil
}

// ownRange reports whether every address of a range is on the server's own
// networks.
func ownRange(p netip.Prefix) bool {
	for _, own := range ownNetworks {
		if own.Bits() <= p.Bits() && own.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// Len returns how many addresses and ranges are trusted.
func (p Proxies) Len() int { return len(p.prefixes) }

// trusts reports whether an address is a trusted proxy's.
func (p Proxies) trusts(addr netip.Addr) bool {
	addr = addr.Unmap().WithZone("")
	for _, pr := range p.prefixes {
		if pr.Contains(addr) {
			return true
		}
	}
	return false
}

// Client is where a request came from.
type Client struct {
	// Addr is the client's address. It is not valid when a trusted proxy
	// forwarded the request without saying for whom, or said it in a way
	// that cannot be read, or when the connection's own address is not one.
	Addr netip.Addr
	// Proxy is the address of the trusted proxy that the request came
	// through; not valid when it came straight from the client.
	Proxy netip.Addr
	// HTTPS reports whether the client made the request over HTTPS: on
	// the connection itself or, through a trusted proxy, as it says.
	HTTPS bool
	// raw is the connection's remote address when it is not an address,
	// as in some tests.
	raw string
}

// String returns the client's address for the log and for counting
// failures by address: "unknown" when a trusted proxy did not say.
func (c Client) String() string {
	switch {
	case c.Addr.IsValid():
		return c.Addr.String()
	case c.raw != "":
		return c.raw
	}
	return "unknown"
}

// Local reports whether the client is on the server's own networks. One
// that a trusted proxy forwarded without saying for whom is not.
func (c Client) Local() bool {
	return c.Addr.IsValid() && OwnNetwork(c.Addr)
}

// Resolve returns where a request came from. From a trusted proxy, the
// client is the one its X-Forwarded-For header names and HTTPS is what its
// X-Forwarded-Proto says; from anywhere else, both headers are ignored.
func (p Proxies) Resolve(r *http.Request) Client {
	host := r.RemoteAddr
	if h, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = h
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return Client{HTTPS: r.TLS != nil, raw: host}
	}
	peer = peer.Unmap()
	c := Client{Addr: peer, HTTPS: r.TLS != nil}
	if !p.trusts(peer) {
		return c
	}
	c.Proxy = peer
	c.Addr = p.forwardedFor(r.Header.Values("X-Forwarded-For"))
	if proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ","); strings.TrimSpace(proto) != "" {
		c.HTTPS = strings.EqualFold(strings.TrimSpace(proto), "https")
	}
	return c
}

// forwardedFor returns the client that X-Forwarded-For names. Each proxy
// adds the address it was reached from at the right, so the header is read
// from the right, past the trusted proxies: the first address that is not
// one is the client's, and if every address is a trusted proxy's, the
// leftmost is. An entry that cannot be read ends the search with no
// client, since whatever lies to its left cannot be checked; so does a
// missing or empty header, or a chain of more than maxHops proxies.
func (p Proxies) forwardedFor(values []string) netip.Addr {
	var entries []string
	for _, v := range values {
		entries = append(entries, strings.Split(v, ",")...)
	}
	var addr netip.Addr
	for i, hops := len(entries)-1, 0; i >= 0; i, hops = i-1, hops+1 {
		if hops == maxHops {
			return netip.Addr{}
		}
		a, ok := parseEntry(entries[i])
		if !ok {
			return netip.Addr{}
		}
		if !p.trusts(a) {
			return a
		}
		addr = a
	}
	return addr
}

// parseEntry reads one address of X-Forwarded-For, which some proxies
// write with a port or, for IPv6, in brackets.
func parseEntry(s string) (netip.Addr, bool) {
	s = strings.TrimSpace(s)
	if a, err := netip.ParseAddr(s); err == nil {
		return a.Unmap().WithZone(""), true
	}
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	if len(s) > 2 && s[0] == '[' && s[len(s)-1] == ']' {
		if a, err := netip.ParseAddr(s[1 : len(s)-1]); err == nil {
			return a.Unmap().WithZone(""), true
		}
	}
	return netip.Addr{}, false
}

// ctxKey keeps a request's Client in its context.
type ctxKey struct{}

// Middleware returns h with each request's client found once, for Of.
func (p Proxies) Middleware(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p.Resolve(r))))
	})
}

// Of returns where a request came from, as Middleware found. A request
// that did not pass through it, as in a test that calls a handler itself,
// comes from the address that connected.
func Of(r *http.Request) Client {
	if c, ok := r.Context().Value(ctxKey{}).(Client); ok {
		return c
	}
	return Proxies{}.Resolve(r)
}
