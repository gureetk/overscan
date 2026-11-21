// SPDX-License-Identifier: AGPL-3.0-or-later

package clientaddr

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	good := []string{"192.168.1.2", " 172.16.0.0/12 ", "10.1.0.0/16", "127.0.0.1", "::1", "fd00::/8",
		"fe80::1%eth0", "169.254.0.0/16", "100.64.0.0/10", "::ffff:192.168.1.3"}
	p, err := Parse(good)
	if err != nil {
		t.Fatalf("Parse(%q): %v", good, err)
	}
	if p.Len() != len(good) {
		t.Errorf("Len() = %d, want %d", p.Len(), len(good))
	}

	for _, bad := range []string{"", "proxy.example", "192.168.1.300", "192.168.1.0/33", "0.0.0.0/0", "::/0",
		"192.0.2.10", "198.51.100.0/24", "2001:db8::1", "10.0.0.0/7", "172.16.0.0/11", "100.64.0.0/9"} {
		if _, err := Parse([]string{"192.168.1.2", bad}); err == nil {
			t.Errorf("Parse(%q) accepted it", bad)
		}
	}
	if p, err := Parse(nil); err != nil || p.Len() != 0 {
		t.Errorf("Parse(nil) = %d proxies, %v", p.Len(), err)
	}
}

func TestOwnNetwork(t *testing.T) {
	for _, s := range []string{"192.168.1.20", "10.1.2.3", "172.31.255.255", "127.0.0.1", "::1", "fd12:3456::1",
		"fe80::1%eth0", "169.254.10.1", "100.64.0.1", "100.127.255.254", "::ffff:192.168.1.20"} {
		if !OwnNetwork(netip.MustParseAddr(s)) {
			t.Errorf("OwnNetwork(%s) = false", s)
		}
	}
	for _, s := range []string{"192.0.2.10", "198.51.100.7", "2001:db8::1", "100.128.0.1", "172.32.0.1", "8.8.8.8"} {
		if OwnNetwork(netip.MustParseAddr(s)) {
			t.Errorf("OwnNetwork(%s) = true", s)
		}
	}
}

// request returns a request from remote with the given headers, each
// "Name: value".
func request(remote string, headers ...string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	for _, h := range headers {
		name, value, _ := strings.Cut(h, ": ")
		r.Header.Add(name, value)
	}
	return r
}

func TestResolve(t *testing.T) {
	p, err := Parse([]string{"192.168.1.2", "172.16.0.0/24", "fd00:1::/64"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		r       *http.Request
		want    string // String()
		proxy   string // "" when not through a proxy
		local   bool
		https   bool
		trustNo bool // the same with no proxy trusted gives the peer
	}{
		{"straight from a player at home", request("192.168.1.20:5000"), "192.168.1.20", "", true, false, false},
		{"straight from outside", request("198.51.100.7:5000"), "198.51.100.7", "", false, false, false},
		{"from outside, with headers that only a proxy may set",
			request("198.51.100.7:5000", "X-Forwarded-For: 192.168.1.20", "X-Forwarded-Proto: https"),
			"198.51.100.7", "", false, false, false},
		{"from a player at home that says it is someone else",
			request("192.168.1.20:5000", "X-Forwarded-For: 192.168.1.30"), "192.168.1.20", "", true, false, false},
		{"through the proxy, for a player at home",
			request("192.168.1.2:40000", "X-Forwarded-For: 192.168.1.20"), "192.168.1.20", "192.168.1.2", true, false, true},
		{"through the proxy, for someone outside, over HTTPS",
			request("192.168.1.2:40000", "X-Forwarded-For: 198.51.100.7", "X-Forwarded-Proto: https"),
			"198.51.100.7", "192.168.1.2", false, true, true},
		{"what the client put in front of the proxy's entry is not believed",
			request("192.168.1.2:40000", "X-Forwarded-For: 192.168.1.20, 198.51.100.7"),
			"198.51.100.7", "192.168.1.2", false, false, true},
		{"through two trusted proxies",
			request("192.168.1.2:40000", "X-Forwarded-For: 203.0.113.9", "X-Forwarded-For: 172.16.0.5"),
			"203.0.113.9", "192.168.1.2", false, false, true},
		{"every entry a trusted proxy: the leftmost",
			request("192.168.1.2:40000", "X-Forwarded-For: 172.16.0.5, 172.16.0.6"),
			"172.16.0.5", "192.168.1.2", true, false, true},
		{"without X-Forwarded-For", request("192.168.1.2:40000"), "unknown", "192.168.1.2", false, false, true},
		{"with an empty one", request("192.168.1.2:40000", "X-Forwarded-For: "), "unknown", "192.168.1.2", false, false, true},
		{"with an entry that is not an address",
			request("192.168.1.2:40000", "X-Forwarded-For: 192.168.1.20, unknown"), "unknown", "192.168.1.2", false, false, true},
		{"with ports and brackets",
			request("[fd00:1::2]:40000", "X-Forwarded-For: [2001:db8::7]:5000"), "2001:db8::7", "fd00:1::2", false, false, true},
		{"IPv6 in brackets without a port",
			request("[fd00:1::2]:40000", "X-Forwarded-For: [fd00:2::7]"), "fd00:2::7", "fd00:1::2", true, false, true},
		{"IPv4 with a port", request("192.168.1.2:40000", "X-Forwarded-For: 192.168.1.20:5000"),
			"192.168.1.20", "192.168.1.2", true, false, true},
		{"an IPv4 address written as IPv6", request("[::ffff:192.168.1.2]:40000", "X-Forwarded-For: ::ffff:192.168.1.20"),
			"192.168.1.20", "192.168.1.2", true, false, true},
		{"the proxy says plain HTTP", request("192.168.1.2:40000", "X-Forwarded-For: 192.168.1.20", "X-Forwarded-Proto: http"),
			"192.168.1.20", "192.168.1.2", true, false, true},
		{"the first of several schemes", request("192.168.1.2:40000", "X-Forwarded-For: 192.168.1.20",
			"X-Forwarded-Proto: HTTPS , http"), "192.168.1.20", "192.168.1.2", true, true, true},
		{"a remote address that is not one", request("pipe"), "pipe", "", false, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c := p.Resolve(tt.r)
			if got := c.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
			proxy := ""
			if c.Proxy.IsValid() {
				proxy = c.Proxy.String()
			}
			if proxy != tt.proxy {
				t.Errorf("Proxy = %q, want %q", proxy, tt.proxy)
			}
			if c.Local() != tt.local {
				t.Errorf("Local() = %v, want %v", c.Local(), tt.local)
			}
			if c.HTTPS != tt.https {
				t.Errorf("HTTPS = %v, want %v", c.HTTPS, tt.https)
			}
			if tt.trustNo {
				none := Proxies{}.Resolve(tt.r)
				if none.Proxy.IsValid() || none.Addr != netip.MustParseAddr(proxy).Unmap() || none.HTTPS {
					t.Errorf("with no proxy trusted: %+v, want the proxy's own address", none)
				}
			}
		})
	}
}

func TestResolveHTTPSOnTheConnection(t *testing.T) {
	r := request("198.51.100.7:5000")
	r.TLS = &tls.ConnectionState{}
	if !(Proxies{}).Resolve(r).HTTPS {
		t.Error("HTTPS = false on a TLS connection")
	}
}

func TestLongChains(t *testing.T) {
	p, err := Parse([]string{"172.16.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	hops := func(n int) string {
		var s []string
		for range n {
			s = append(s, "172.16.0.9")
		}
		return strings.Join(s, ", ")
	}
	r := request("172.16.0.1:40000", "X-Forwarded-For: 198.51.100.7, "+hops(maxHops-1))
	if got := p.Resolve(r).String(); got != "198.51.100.7" {
		t.Errorf("through %d proxies: %q", maxHops-1, got)
	}
	r = request("172.16.0.1:40000", "X-Forwarded-For: 198.51.100.7, "+hops(maxHops))
	if got := p.Resolve(r).String(); got != "unknown" {
		t.Errorf("through %d proxies: %q, want unknown", maxHops, got)
	}
	// A header as long as the server lets through is read in time.
	r = request("172.16.0.1:40000", "X-Forwarded-For: "+strings.Repeat("198.51.100.7, ", 4000)+"192.168.1.20")
	if got := p.Resolve(r).String(); got != "192.168.1.20" {
		t.Errorf("a long header: %q", got)
	}
}

func TestMiddleware(t *testing.T) {
	p, err := Parse([]string{"192.168.1.2"})
	if err != nil {
		t.Fatal(err)
	}
	var got Client
	h := p.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = Of(r) }))
	h.ServeHTTP(httptest.NewRecorder(), request("192.168.1.2:40000", "X-Forwarded-For: 198.51.100.7", "X-Forwarded-Proto: https"))
	if got.String() != "198.51.100.7" || !got.HTTPS || got.Local() {
		t.Errorf("through the middleware: %+v", got)
	}
	// Without the middleware, the connection's own address.
	if c := Of(request("192.168.1.2:40000", "X-Forwarded-For: 198.51.100.7")); c.String() != "192.168.1.2" || c.Proxy.IsValid() {
		t.Errorf("without the middleware: %+v", c)
	}
}

func FuzzResolve(f *testing.F) {
	f.Add("192.168.1.20, 198.51.100.7", "https")
	f.Add("[fd00:1::2]:5000,unknown", "http,https")
	f.Add("", "")
	p, err := Parse([]string{"192.168.1.0/24", "fd00:1::/64"})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, xff, proto string) {
		r := request("192.168.1.2:40000")
		r.Header["X-Forwarded-For"] = []string{xff}
		r.Header["X-Forwarded-Proto"] = []string{proto}
		c := p.Resolve(r)
		if c.Proxy != netip.MustParseAddr("192.168.1.2") {
			t.Fatalf("Proxy = %v", c.Proxy)
		}
		if c.Addr.IsValid() {
			if c.Addr.Zone() != "" || c.Addr.Is4In6() {
				t.Fatalf("Addr = %v, with a zone or mapped", c.Addr)
			}
		} else if c.String() != "unknown" {
			t.Fatalf("String() = %q for no address", c.String())
		}
		if c.Local() && !OwnNetwork(c.Addr) {
			t.Fatalf("Local() for %v", c.Addr)
		}
	})
}
