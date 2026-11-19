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

const jellyfinKey = "0123456789abcdef" + secret

// fakeJellyfin is a fictional Jellyfin server with a library of films, one
// of shows and one of music, and one user who sees only the films.
type fakeJellyfin struct {
	mu        sync.Mutex
	libraries []map[string]any
}

func newJellyfin(t *testing.T) (*fakeJellyfin, *httptest.Server) {
	t.Helper()
	f := &fakeJellyfin{libraries: []map[string]any{
		{"Id": "lib1", "Name": "Films", "CollectionType": "movies"},
		{"Id": "lib2", "Name": "Shows", "CollectionType": "tvshows"},
		{"Id": "lib3", "Name": "Music", "CollectionType": "music"},
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if !strings.Contains(r.Header.Get("Authorization"), `Token="`+jellyfinKey+`"`) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var answer any
		switch r.URL.Path {
		case "/System/Info":
			answer = map[string]any{"ServerName": "Den", "Version": "10.11.2", "Id": "s1"}
		case "/Users":
			answer = []map[string]any{{"Name": "Anna", "Id": "u1"}}
		case "/Library/MediaFolders":
			answer = map[string]any{"Items": f.libraries}
		case "/UserViews":
			answer = map[string]any{"Items": f.libraries[:1]}
		default:
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return f, srv
}

func jellyfinSettings(srv *httptest.Server) source.Settings {
	return source.Settings{Name: "den", Type: source.TypeJellyfin, URL: srv.URL, Password: jellyfinKey,
		Refresh: time.Hour, Numbers: source.NumbersPlaylist}
}

func TestJellyfinTest(t *testing.T) {
	_, srv := newJellyfin(t)
	_, r, _ := newStore(t)
	ctx := context.Background()
	got, err := r.Test(ctx, jellyfinSettings(srv))
	want := Tested{Server: &Server{Name: "Den", Version: "10.11.2"}, Libraries: []string{"Films", "Shows"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("test %+v, %v", got, err)
	}
	anna := jellyfinSettings(srv)
	anna.Username = "anna"
	if got, err := r.Test(ctx, anna); err != nil || !reflect.DeepEqual(got.Libraries, []string{"Films"}) {
		t.Errorf("Anna's %+v, %v", got, err)
	}
	nobody := jellyfinSettings(srv)
	nobody.Username = "Bert"
	if _, err := r.Test(ctx, nobody); err == nil || !strings.Contains(err.Error(), `no user named "Bert"`) {
		t.Errorf("no such user: %v", err)
	}
	wrong := jellyfinSettings(srv)
	wrong.Password = "wrong"
	if _, err := r.Test(ctx, wrong); err == nil || !strings.Contains(err.Error(), "refused the API key") {
		t.Errorf("a wrong key: %v", err)
	}
}

func TestJellyfinRefresh(t *testing.T) {
	f, srv := newJellyfin(t)
	st, r, logs := newStore(t)
	ctx := context.Background()
	settings := jellyfinSettings(srv)
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
	if !strings.Contains(logs.String(), "source checked") || strings.Contains(logs.String(), secret) {
		t.Errorf("logs:\n%s", logs)
	}

	// A server whose libraries hold no films or shows is no use to boxes.
	f.mu.Lock()
	f.libraries = f.libraries[2:]
	f.mu.Unlock()
	if _, err := r.Refresh(ctx, settings, id); err == nil || !strings.Contains(err.Error(), "no libraries of films or shows") {
		t.Errorf("without libraries: %v", err)
	}
	if src, _ := st.Source(ctx, id); !strings.Contains(src.RefreshError, "no libraries") {
		t.Errorf("recorded %q", src.RefreshError)
	}
}

func TestPlaylistBecomesJellyfin(t *testing.T) {
	_, srv := newJellyfin(t)
	h := newHarness(t)
	ctx := context.Background()
	h.body.Store(`#EXTM3U url-tvg="http://guide.example/epg.xml"` + "\n" + strings.TrimPrefix(playlist, "#EXTM3U\n"))
	if _, err := h.r.Refresh(ctx, h.source(), h.srcID); err != nil {
		t.Fatal(err)
	}
	if len(h.channelNames()) != 2 {
		t.Fatalf("channels %v", h.channelNames())
	}
	if listed, _ := h.store.ListedGuide(ctx, h.srcID); listed == "" {
		t.Fatal("the playlist's guide was not kept")
	}

	settings := jellyfinSettings(srv)
	settings.Name = "main"
	if err := h.store.UpdateSource(ctx, h.srcID, settings); err != nil {
		t.Fatal(err)
	}
	h.r.now = func() time.Time { return time.Now().Add(time.Minute) }
	ch, err := h.r.Refresh(ctx, settings, h.srcID)
	if err != nil || ch.Missing != 2 {
		t.Fatalf("changes %+v, %v", ch, err)
	}
	if names := h.channelNames(); len(names) != 0 {
		t.Errorf("a Jellyfin server's channels: %v", names)
	}
	if listed, _ := h.store.ListedGuide(ctx, h.srcID); listed != "" {
		t.Errorf("the playlist's guide stayed: %q", listed)
	}
	src, err := h.store.Source(ctx, h.srcID)
	if err != nil || src.Type != source.TypeJellyfin || src.RefreshError != "" {
		t.Errorf("source %+v, %v", src, err)
	}
}
