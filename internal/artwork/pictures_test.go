// SPDX-License-Identifier: AGPL-3.0-or-later

package artwork

import (
	"context"
	"image/color"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// TestProviderPictures checks that a provider's poster that does not load
// for a device is recorded, that a check a day later finds it again, and
// that once it loads its titles show it in place of TMDB's (D143).
func TestProviderPictures(t *testing.T) {
	png := encodePNG(t, solid(40, 60, color.NRGBA{40, 120, 200, 255}))
	var mu sync.Mutex
	back := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		isBack := back
		mu.Unlock()
		switch r.URL.Path {
		case "/good.png":
			w.Write(png)
		case "/flaky.png":
			if !isBack {
				http.NotFound(w, r)
				return
			}
			w.Write(png)
		case "/busy.png":
			http.Error(w, "busy", http.StatusServiceUnavailable)
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
	src, err := st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if _, err := st.ApplyLibrary(ctx, src, store.KindMovie, nil, []store.LibraryTitle{
		{Key: "1", Name: "Alpha", Poster: srv.URL + "/flaky.png"},
		{Key: "2", Name: "Bravo", Poster: srv.URL + "/good.png"},
		{Key: "3", Name: "Charlie", Poster: srv.URL + "/page.png"},
		{Key: "4", Name: "Delta", Poster: srv.URL + "/busy.png"},
	}, at); err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	rows, err := st.SearchTitles(ctx, "a", 10, store.EverySource)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		ids[r.Name] = r.ID
	}
	cache := New(t.TempDir(), quiet(), "test")
	h := httptest.NewServer(Handler(st, cache, quiet()))
	t.Cleanup(h.Close)
	get := func(kind Kind, name, path string) int {
		t.Helper()
		resp, err := http.Get(h.URL + TitlePath(kind, ids[name], srv.URL+path))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for _, tc := range []struct {
		kind       Kind
		name, path string
		want       int
	}{
		{PosterKind, "Alpha", "/flaky.png", http.StatusNotFound},
		{PosterKind, "Bravo", "/good.png", http.StatusOK},
		{ThumbKind, "Charlie", "/page.png", http.StatusNotFound},
		{PosterKind, "Delta", "/busy.png", http.StatusNotFound},
	} {
		if got := get(tc.kind, tc.name, tc.path); got != tc.want {
			t.Errorf("%s: %d", tc.name, got)
		}
	}
	// Alpha's and Charlie's are recorded; Delta's server was busy, which
	// settles nothing.
	if _, failing, err := st.PictureCounts(ctx); failing != 2 || err != nil {
		t.Fatalf("recorded %d, %v", failing, err)
	}
	if err := st.SetPictureFill(ctx, ids["Alpha"], "https://image.tmdb.org/t/p/w342/alpha.jpg", ""); err != nil {
		t.Fatal(err)
	}

	// A day later, Alpha's loads again, and Charlie's still is no image.
	mu.Lock()
	back = true
	mu.Unlock()
	k := NewChecker(st, cache, quiet())
	k.pause = 0
	k.now = func() time.Time { return at.Add(RecheckAfter + time.Hour) }
	cache.now = k.now // the cache retries what failed after an hour
	if n := k.Round(ctx); n != 2 {
		t.Errorf("checked %d", n)
	}
	if _, failing, _ := st.PictureCounts(ctx); failing != 1 {
		t.Errorf("still recorded: %d", failing)
	}
	ti, err := st.Title(ctx, ids["Alpha"])
	if err != nil || ti.Poster != srv.URL+"/flaky.png" {
		t.Errorf("Alpha once its poster loads: %+v, %v", ti.TitleRow, err)
	}
	// Charlie's was checked just now: nothing is due.
	if n := k.Round(ctx); n != 0 {
		t.Errorf("checked again: %d", n)
	}
}

func TestProviderPicture(t *testing.T) {
	for _, tc := range []struct {
		typ, address string
		want         bool
	}{
		{source.TypeXtream, "http://img.example/a.jpg", true},
		{source.TypeStalker, "http://img.example/a.jpg", true},
		{source.TypeXtream, "https://image.tmdb.org/t/p/w342/a.jpg", false},
		{source.TypeFolder, "poster.jpg", false},
		{source.TypeJellyfin, "jellyfin:1/Primary", false},
		{"", "http://img.example/a.jpg", false},
	} {
		if got := ProviderPicture(tc.typ, tc.address); got != tc.want {
			t.Errorf("%s %s: %v", tc.typ, tc.address, got)
		}
	}
}
