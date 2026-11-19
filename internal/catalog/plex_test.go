// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

const plexToken = "fictional-" + secret

// newPlex returns a fictional Plex server with a library of films, one of
// shows and one of music.
func newPlex(t *testing.T) (*sync.Mutex, *[]map[string]any, *httptest.Server) {
	t.Helper()
	var mu sync.Mutex
	libraries := []map[string]any{
		{"key": "1", "title": "Films", "type": "movie"},
		{"key": "2", "title": "Shows", "type": "show"},
		{"key": "3", "title": "Music", "type": "artist"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("X-Plex-Token") != plexToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var answer map[string]any
		switch r.URL.Path {
		case "/":
			answer = map[string]any{"friendlyName": "Lounge", "version": "1.42.2.10156", "machineIdentifier": "m1"}
		case "/library/sections/all":
			answer = map[string]any{"Directory": libraries}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"MediaContainer": answer})
	}))
	t.Cleanup(srv.Close)
	return &mu, &libraries, srv
}

func plexSettings(srv *httptest.Server) source.Settings {
	return source.Settings{Name: "lounge", Type: source.TypePlex, URL: srv.URL, Password: plexToken,
		Refresh: time.Hour, Numbers: source.NumbersPlaylist}
}

func TestPlexTest(t *testing.T) {
	_, _, srv := newPlex(t)
	_, r, _ := newStore(t)
	ctx := context.Background()
	got, err := r.Test(ctx, plexSettings(srv))
	want := Tested{Server: &Server{Name: "Lounge", Version: "1.42.2.10156"}, Libraries: []string{"Films", "Shows"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("test %+v, %v", got, err)
	}
	wrong := plexSettings(srv)
	wrong.Password = "wrong"
	if _, err := r.Test(ctx, wrong); err == nil || !strings.Contains(err.Error(), "refused the token") {
		t.Errorf("a wrong token: %v", err)
	}
}

func TestPlexRefresh(t *testing.T) {
	mu, libraries, srv := newPlex(t)
	st, r, logs := newStore(t)
	ctx := context.Background()
	settings := plexSettings(srv)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	src, err := st.Source(ctx, id)
	if err != nil || src.RefreshedAt.IsZero() || src.RefreshError != "" || src.Channels != 0 {
		t.Errorf("after a check %+v, %v", src, err)
	}
	if !strings.Contains(logs.String(), "source checked") || !strings.Contains(logs.String(), "libraries=2") ||
		strings.Contains(logs.String(), secret) {
		t.Errorf("logs:\n%s", logs)
	}
	mu.Lock()
	*libraries = (*libraries)[2:]
	mu.Unlock()
	if _, err := r.Refresh(ctx, settings, id); err == nil || !strings.Contains(err.Error(), "no libraries of films or shows") {
		t.Errorf("without libraries: %v", err)
	}
}
