// SPDX-License-Identifier: AGPL-3.0-or-later

package netguard

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

func TestPublic(t *testing.T) {
	for _, s := range []string{"8.8.8.8", "1.1.1.1", "192.0.2.10", "203.0.113.7", "2001:db8::1", "2606:4700::1111",
		"::ffff:8.8.8.8", "64:ff9b::808:808", "2002:808:808::1", "100.128.0.1", "172.32.0.1"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("Public(%s) = false", s)
		}
	}
	for _, s := range []string{"10.1.2.3", "172.16.0.9", "192.168.1.20", "127.0.0.1", "127.1.2.3", "::1", "fd12:3456::7",
		"fe80::1%eth0", "169.254.169.254", "100.64.0.1", "0.0.0.0", "0.1.2.3", "::", "224.0.0.1", "ff02::1",
		"255.255.255.255", "240.0.0.1", "192.0.0.8", "198.18.0.1", "::ffff:192.168.1.20", "::ffff:127.0.0.1",
		"64:ff9b::c0a8:114", "64:ff9b::7f00:1", "2002:c0a8:114::1", "2002:7f00:1::1", "64:ff9b:1::1", "100::1"} {
		if Public(netip.MustParseAddr(s)) {
			t.Errorf("Public(%s) = true", s)
		}
	}
}

func TestClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/away" {
			http.Redirect(w, r, "/here", http.StatusFound)
			return
		}
		io.WriteString(w, "here")
	}))
	defer srv.Close()
	base := srv.Client()

	if got := Client(base, false); got != base {
		t.Error("Client(c, false) is not c")
	}
	if resp, err := base.Get(srv.URL); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}

	guarded := Client(base, true)
	for _, path := range []string{"/", "/away"} {
		resp, err := guarded.Get(srv.URL + path)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s: a public-only client reached %s", path, srv.URL)
		}
		if !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: %v, want ErrNotPublic", path, err)
		}
		if !strings.Contains(err.Error(), "127.0.0.1") || strings.Contains(err.Error(), path+"?") {
			t.Errorf("%s: %v", path, err)
		}
	}
	// A name that leads to the loopback address is refused as well.
	if resp, err := guarded.Get(strings.Replace(srv.URL, "127.0.0.1", "localhost", 1)); err == nil {
		resp.Body.Close()
		t.Error("a public-only client reached localhost")
	} else if !errors.Is(err, ErrNotPublic) {
		t.Errorf("localhost: %v", err)
	}

	// Every public-only client of a transport shares one copy of it.
	if Client(base, true).Transport != guarded.Transport {
		t.Error("a second public-only client has a transport of its own")
	}
	if Client(nil, true).Transport == nil {
		t.Error("the default client's public-only copy has no transport")
	}

	// A transport that cannot be checked refuses everything.
	odd := Client(&http.Client{Transport: roundTripper(func(*http.Request) (*http.Response, error) {
		t.Error("the odd transport was used")
		return nil, errors.New("used")
	})}, true)
	if resp, err := odd.Get("http://public.example/"); err == nil {
		resp.Body.Close()
		t.Error("a transport that cannot be checked let a request through")
	}
}

// Each address that a connection is tried at is checked, whatever led to
// it: a name, or a redirect, which an http.Client follows with the same
// transport.
func TestControl(t *testing.T) {
	for address, ok := range map[string]bool{
		"203.0.113.7:443": true, "[2001:db8::1]:80": true,
		"192.168.1.20:80": false, "[::1]:80": false, "169.254.169.254:80": false, "[fe80::1%eth0]:80": false,
		"not-an-address:80": false,
	} {
		err := control("tcp", address, nil)
		if (err == nil) != ok {
			t.Errorf("control(%s) = %v", address, err)
		}
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
