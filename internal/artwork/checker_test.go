// SPDX-License-Identifier: AGPL-3.0-or-later

package artwork

import (
	"context"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// TestChecker checks that the logos that channels choose between are
// checked in the background, that what is settled is kept, and that a
// channel whose own logo does not load takes its guide's (D141).
func TestChecker(t *testing.T) {
	png := encodePNG(t, solid(40, 20, color.NRGBA{200, 40, 40, 255}))
	var mu sync.Mutex
	down := true
	hits := map[string]int{}
	agents := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		agents[r.URL.Path] = r.UserAgent()
		isDown := down
		mu.Unlock()
		switch r.URL.Path {
		case "/good.png", "/icon-a.png", "/icon-c.png":
			w.Write(png)
		case "/down.png":
			if isDown {
				http.Error(w, "busy", http.StatusServiceUnavailable)
				return
			}
			w.Write(png)
		case "/page.png":
			io.WriteString(w, "<html>not an image</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.UseKey(secrets.NewRandomBox())
	src, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", UserAgent: "Box/2.0"})
	if err != nil {
		t.Fatal(err)
	}
	chs := []store.CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", Logo: srv.URL + "/good.png", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Bravo", Logo: srv.URL + "/gone.png", StreamURL: "http://streams.example/b"},
		{IdentityKey: "c", Name: "Charlie", Logo: srv.URL + "/page.png", StreamURL: "http://streams.example/c"},
		{IdentityKey: "d", Name: "Delta", Logo: srv.URL + "/down.png", StreamURL: "http://streams.example/d"},
		{IdentityKey: "e", Name: "Echo", Logo: srv.URL + "/gone.png", StreamURL: "http://streams.example/e"},
	}
	if _, err := st.ApplyCatalog(ctx, src, chs, time.Now()); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	gcs, err := st.GuideChannels(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range gcs {
		ids[c.Name] = c.ID
	}
	// The source's guide gives every channel but Echo an icon, with
	// Delta's its own logo again.
	w, err := st.BeginGuide(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	for name, icon := range map[string]string{"Alpha": "/icon-a.png", "Bravo": "/icon-a.png", "Charlie": "/icon-c.png", "Delta": "/down.png"} {
		w.SetIcon(ids[name], srv.URL+icon)
	}
	if err := w.Commit(ctx, time.Now(), "own"); err != nil {
		t.Fatal(err)
	}
	logos := func() map[string]string {
		t.Helper()
		cs, err := st.LineupChannels(ctx, store.DefaultLineupID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for _, c := range cs {
			if c.Logo != "" {
				out[c.Name] = c.Logo[len(srv.URL):]
			} else {
				out[c.Name] = ""
			}
		}
		return out
	}

	cache := New(t.TempDir(), quiet(), "test")
	k := NewChecker(st, cache, quiet())
	k.pause = 0
	// Without the guide's logos in use, nothing is checked.
	if n := k.Round(ctx); n != 0 {
		t.Errorf("%d checked without a guide's logos", n)
	}
	if err := st.SetSourceGuideLogos(ctx, src, store.GuideLogosFill); err != nil {
		t.Fatal(err)
	}
	if n := k.Round(ctx); n != 6 {
		t.Errorf("%d checked, want the 6 logos of the four channels with icons", n)
	}
	// Echo has the logo that Bravo's check found gone, and no icon: none.
	want := map[string]string{"Alpha": "/good.png", "Bravo": "/icon-a.png", "Charlie": "/icon-c.png", "Delta": "/down.png", "Echo": ""}
	if got := logos(); !reflect.DeepEqual(got, want) {
		t.Errorf("logos %v, want %v", got, want)
	}
	// Two settled failures; the one that did not answer is not known.
	if checked, failing, err := st.LogoCounts(ctx); err != nil || checked != 5 || failing != 2 {
		t.Errorf("checks %d, %d, %v", checked, failing, err)
	}
	mu.Lock()
	if agents["/good.png"] != "Box/2.0" || hits["/icon-a.png"] != 1 {
		t.Errorf("fetched as %q, the shared icon %d times", agents["/good.png"], hits["/icon-a.png"])
	}
	mu.Unlock()
	// A kept logo is served from the cache.
	if _, _, ok := cache.kept(LogoKind, Key(srv.URL+"/good.png")); !ok {
		t.Error("the logo that loads is not kept")
	}

	// The next round leaves out what was settled, and what did not answer
	// for an hour.
	if n := k.Round(ctx); n != 0 {
		t.Errorf("%d checked again at once", n)
	}
	mu.Lock()
	down = false
	mu.Unlock()
	later := time.Now().Add(2 * time.Hour)
	k.now = func() time.Time { return later }
	cache.now = k.now
	if n := k.Round(ctx); n != 1 {
		t.Errorf("%d checked two hours later, want the one that did not answer", n)
	}
	if checked, failing, _ := st.LogoCounts(ctx); checked != 6 || failing != 2 {
		t.Errorf("checks %d, %d two hours later", checked, failing)
	}

	// A day later, the failures are checked again.
	dayLater := time.Now().Add(25 * time.Hour)
	k.now = func() time.Time { return dayLater }
	cache.now = k.now
	if n := k.Round(ctx); n != 2 {
		t.Errorf("%d checked a day later, want the 2 that did not load", n)
	}
}

func TestSettled(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{ErrUnusable, true}, {&StatusError{404}, true}, {&StatusError{403}, true}, {&StatusError{429}, false},
		{&StatusError{408}, false}, {&StatusError{503}, false}, {io.ErrUnexpectedEOF, false}, {context.DeadlineExceeded, false},
	} {
		if got := Settled(c.err); got != c.want {
			t.Errorf("%v: %v", c.err, got)
		}
	}
}
