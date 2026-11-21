// SPDX-License-Identifier: AGPL-3.0-or-later

package artwork

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/adapters/plex"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

func TestHandler(t *testing.T) {
	p := newProvider(t)
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
	channels := []store.CatalogChannel{
		{IdentityKey: "a", Name: "With a logo", Logo: p.URL + "/logo.png", StreamURL: "http://streams.example/a"},
		{IdentityKey: "b", Name: "Without", StreamURL: "http://streams.example/b"},
		{IdentityKey: "c", Name: "Hidden", Logo: p.URL + "/logo.jpg", StreamURL: "http://streams.example/c"},
		{IdentityKey: "d", Name: "Broken", Logo: p.URL + "/logo.svg", StreamURL: "http://streams.example/d"},
	}
	if _, err := st.ApplyCatalog(ctx, src, channels, time.Now()); err != nil {
		t.Fatal(err)
	}
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]string{}
	ids := map[string]int64{}
	for _, c := range lineup {
		paths[c.Name], ids[c.Name] = LogoPath(c.EntryID, c.Logo), c.EntryID
	}
	if err := st.SetEntriesVisibility(ctx, store.DefaultLineupID, []int64{ids["Hidden"]}, store.Hidden); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(Handler(st, New(t.TempDir(), quiet(), "test"), quiet()))
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + paths["With a logo"])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	h := resp.Header
	if resp.StatusCode != http.StatusOK || h.Get("Content-Type") != "image/png" || h.Get("X-Content-Type-Options") != "nosniff" ||
		h.Get("Cache-Control") != "public, max-age=31536000, immutable" || h.Get("Content-Security-Policy") == "" {
		t.Errorf("logo: %d %v", resp.StatusCode, h)
	}
	if p.agent != "Box/2.0" {
		t.Errorf("fetched as %q, not as the source's user agent", p.agent)
	}

	hidden := fmt.Sprintf("/artwork/logo/%d/%s", ids["Hidden"], Key(p.URL+"/logo.jpg"))
	broken := fmt.Sprintf("/artwork/logo/%d/%s", ids["Broken"], Key(p.URL+"/logo.svg"))
	for _, path := range []string{
		fmt.Sprintf("/artwork/logo/%d/%s", ids["With a logo"], Key("http://elsewhere.example/old.png")), // a logo since replaced
		fmt.Sprintf("/artwork/logo/%d/%s", ids["Without"], Key("")),
		"/artwork/logo/999999/" + Key(p.URL+"/logo.png"),
		"/artwork/logo/x/y",
		"/artwork/other",
		hidden, broken,
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	if p.count("/logo.jpg") != 0 {
		t.Error("fetched the logo of a hidden channel")
	}
}

func TestTitlePictures(t *testing.T) {
	p := newProvider(t)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.UseKey(secrets.NewRandomBox())
	src, err := st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "pw", UserAgent: "Box/2.0"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	if _, err := st.ApplyLibrary(ctx, src, store.KindSeries, nil, []store.LibraryTitle{{Key: "7001", Name: "Coastline",
		Poster: p.URL + "/logo.png", Summary: store.TitleSummary{Backdrop: p.URL + "/logo.jpg"}}}, at); err != nil {
		t.Fatal(err)
	}
	titles, err := st.SearchTitles(ctx, "coast", 1, store.EverySource)
	if err != nil || len(titles) != 1 {
		t.Fatalf("titles %+v, %v", titles, err)
	}
	id := titles[0].ID
	if err := st.SetSeriesDetails(ctx, id, store.TitleDetails{Poster: p.URL + "/logo.webp"}, []store.LibraryEpisode{
		{Key: "9001", Season: 1, Number: 1, Picture: p.URL + "/logo.jpg"}}, at); err != nil {
		t.Fatal(err)
	}
	eps, err := st.Episodes(ctx, id)
	if err != nil || len(eps) != 1 {
		t.Fatalf("episodes %+v, %v", eps, err)
	}
	cache := New(t.TempDir(), quiet(), "test")
	srv := httptest.NewServer(Handler(st, cache, quiet()))
	t.Cleanup(srv.Close)

	for _, tc := range []struct {
		path, contentType string
		w, h              int
	}{
		{TitlePath(PosterKind, id, p.URL+"/logo.png"), "image/jpeg", 240, 96}, // 1000×400, fitted to 240 wide
		{TitlePath(PosterKind, id, p.URL+"/logo.webp"), "image/webp", 0, 0},   // the details' poster, as it came
		{TitlePath(ThumbKind, id, p.URL+"/logo.png"), "image/jpeg", 96, 38},   // a poster, small
		{TitlePath(BackdropKind, id, p.URL+"/logo.jpg"), "image/jpeg", 64, 64},
		{TitlePath(StillKind, eps[0].ID, p.URL+"/logo.jpg"), "image/jpeg", 64, 64},
	} {
		resp, err := http.Get(srv.URL + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != tc.contentType {
			t.Errorf("%s: %d %v", tc.path, resp.StatusCode, resp.Header)
			continue
		}
		if tc.w > 0 {
			cfg, err := jpeg.DecodeConfig(bytes.NewReader(data))
			if err != nil || cfg.Width != tc.w || cfg.Height != tc.h {
				t.Errorf("%s: %d×%d, %v", tc.path, cfg.Width, cfg.Height, err)
			}
		}
	}
	if p.agent != "Box/2.0" {
		t.Errorf("fetched as %q", p.agent)
	}
	for _, path := range []string{
		TitlePath(PosterKind, id, p.URL+"/logo.jpg"),       // the backdrop's address as a poster
		TitlePath(BackdropKind, id, p.URL+"/logo.png"),     // and the poster's as a backdrop
		TitlePath(StillKind, eps[0].ID, p.URL+"/logo.png"), // another picture's address
		TitlePath(PosterKind, 999999, p.URL+"/logo.png"),   // no such title
	} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %d", path, resp.StatusCode)
		}
	}
	if TitlePath(PosterKind, id, "") != "" {
		t.Error("a path for no picture")
	}
	// Each kind keeps its own file: the same address as a backdrop and as a
	// still is fetched once each.
	if n := p.count("/logo.jpg"); n != 2 {
		t.Errorf("fetched %d times", n)
	}
}

func TestFolderPictures(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.UseKey(secrets.NewRandomBox())
	dir := filepath.Join(t.TempDir(), "Films")
	var poster bytes.Buffer
	if err := jpeg.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 600, 900)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "Calm Water (2021)"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"Calm Water (2021)/Calm Water (2021).mkv": []byte("video"),
		"Calm Water (2021)/poster.jpg": poster.Bytes()} {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f, err := folder.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	films, _, err := f.Films(ctx)
	f.Close()
	if err != nil || len(films) != 1 || films[0].Poster == "" {
		t.Fatalf("films %+v, %v", films, err)
	}
	address := films[0].Poster
	src, err := st.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeFolder, URL: dir})
	if err != nil {
		t.Fatal(err)
	}
	// A provider's list that names a picture in the folder gets nothing.
	other, err := st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	for id, title := range map[int64]store.LibraryTitle{
		src:   {Key: films[0].Key, Name: "Calm Water", Poster: address},
		other: {Key: "1", Name: "Borrowed", Poster: address},
	} {
		if _, err := st.ApplyLibrary(ctx, id, store.KindMovie, nil, []store.LibraryTitle{title}, at); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(Handler(st, New(t.TempDir(), quiet(), "test"), quiet()))
	t.Cleanup(srv.Close)
	for name, want := range map[string]int{"calm": http.StatusOK, "borrowed": http.StatusNotFound} {
		titles, err := st.SearchTitles(ctx, name, 1, store.EverySource)
		if err != nil || len(titles) != 1 {
			t.Fatalf("titles %+v, %v", titles, err)
		}
		resp, err := http.Get(srv.URL + TitlePath(PosterKind, titles[0].ID, address))
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d", name, resp.StatusCode)
			continue
		}
		if want == http.StatusOK {
			if cfg, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil || cfg.Width != 240 || cfg.Height != 360 {
				t.Errorf("%s: %d×%d, %v", name, cfg.Width, cfg.Height, err)
			}
		}
	}
}

func TestPlexPictures(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	st.UseKey(secrets.NewRandomBox())
	var poster bytes.Buffer
	if err := jpeg.Encode(&poster, image.NewRGBA(image.Rect(0, 0, 480, 720)), nil); err != nil {
		t.Fatal(err)
	}
	var asked []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+" "+r.URL.Query().Get("url")+" "+r.URL.Query().Get("width"))
		if r.URL.Path != "/photo/:/transcode" || r.URL.Query().Get("X-Plex-Token") != "tok" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(poster.Bytes())
	}))
	t.Cleanup(server.Close)
	address := plex.PictureAddress("/library/metadata/101/thumb/1700000000", 480, 720)
	src, err := st.AddSource(ctx, source.Settings{Name: "lounge", Type: source.TypePlex, URL: server.URL, Password: "tok"})
	if err != nil {
		t.Fatal(err)
	}
	// A provider's list that names a Plex picture gets nothing.
	other, err := st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now()
	for id, title := range map[int64]store.LibraryTitle{
		src:   {Key: "101", Name: "Calm Water", Poster: address, File: "/library/parts/1/2/file.mkv"},
		other: {Key: "1", Name: "Borrowed", Poster: address},
	} {
		if _, err := st.ApplyLibrary(ctx, id, store.KindMovie, nil, []store.LibraryTitle{title}, at); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(Handler(st, New(t.TempDir(), quiet(), "test"), quiet()))
	t.Cleanup(srv.Close)
	for name, want := range map[string]int{"calm": http.StatusOK, "borrowed": http.StatusNotFound} {
		titles, err := st.SearchTitles(ctx, name, 1, store.EverySource)
		if err != nil || len(titles) != 1 {
			t.Fatalf("titles %+v, %v", titles, err)
		}
		resp, err := http.Get(srv.URL + TitlePath(PosterKind, titles[0].ID, address))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("%s: %d", name, resp.StatusCode)
		}
	}
	if len(asked) != 1 || asked[0] != "/photo/:/transcode /library/metadata/101/thumb/1700000000 480" {
		t.Errorf("asked the server %q", asked)
	}
}
