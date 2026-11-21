// SPDX-License-Identifier: AGPL-3.0-or-later

package netguard_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"overscan.gureet.ca/internal/adapters/jellyfin"
	"overscan.gureet.ca/internal/adapters/plex"
	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/xtream"
	"overscan.gureet.ca/internal/netguard"
	"overscan.gureet.ca/internal/source"
)

// Every adapter keeps a public-only source's requests off the server's own
// networks (D121), whatever client it is given: a server on the loopback
// address hears nothing from it.
func TestAdaptersKeepToPublicAddresses(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "not here", http.StatusNotFound)
	}))
	defer srv.Close()
	ctx := context.Background()

	for _, client := range []*http.Client{srv.Client(), nil} {
		settings := func(typ string) source.Settings {
			return source.Settings{Name: "main", Type: typ, URL: srv.URL, Username: "alex", Password: "correct-horse",
				PublicOnly: true}
		}
		checks := map[string]func() error{
			"xtream": func() error {
				_, err := xtream.New(settings(source.TypeXtream), client, "").Account(ctx)
				return err
			},
			"jellyfin": func() error {
				_, err := jellyfin.New(settings(source.TypeJellyfin), client, "").Info(ctx)
				return err
			},
			"plex": func() error {
				_, err := plex.New(settings(source.TypePlex), client, "").Info(ctx)
				return err
			},
			"stalker": func() error {
				s := settings(source.TypeStalker)
				s.URL = srv.URL + "/c/"
				s.Device = source.Device{MAC: "00:1A:79:00:00:01"}
				c, err := stalker.New(stalker.FromSettings(s), client)
				if err != nil {
					return err
				}
				_, err = c.SignIn(ctx)
				return err
			},
		}
		for name, check := range checks {
			if err := check(); !errors.Is(err, netguard.ErrNotPublic) {
				t.Errorf("%s, client %v: %v, want ErrNotPublic", name, client != nil, err)
			}
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server on the loopback address heard %d requests", n)
	}

	// Without the setting, the same adapter reaches it.
	if _, err := xtream.New(source.Settings{Type: source.TypeXtream, URL: srv.URL, Username: "alex", Password: "p"},
		srv.Client(), "").Account(ctx); errors.Is(err, netguard.ErrNotPublic) || hits.Load() == 0 {
		t.Errorf("without the setting: %v, %d requests", err, hits.Load())
	}
}
