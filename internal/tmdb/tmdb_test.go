// SPDX-License-Identifier: AGPL-3.0-or-later

package tmdb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A made-up key and token: neither is a real one.
const (
	testKey   = "0123456789abcdef0123456789abcdef"
	testToken = "eyJhbGciOiJIUzI1NiJ9.eyJhdWQiOiJleGFtcGxlIn0.c2lnbmF0dXJl"
)

func TestAuthAndErrors(t *testing.T) {
	var got struct{ key, auth, lang, query string }
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.key, got.auth, got.lang = r.URL.Query().Get("api_key"), r.Header.Get("Authorization"), r.URL.Query().Get("language")
		got.query = r.URL.Query().Get("query")
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"results":[{"id":7,"title":"Quiet Harbour","release_date":"2019-04-02"}]}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	// An API key goes in the query; a read access token in a header.
	c := New(testKey, "en-GB", "Overscan/test").WithBase(srv.URL)
	if rs, err := c.SearchMovie(ctx, "Quiet Harbour", 2019); err != nil || len(rs) != 1 || rs[0].ID != 7 {
		t.Fatalf("search: %+v, %v", rs, err)
	}
	if got.key != testKey || got.auth != "" || got.lang != "en-GB" || got.query != "Quiet Harbour" {
		t.Errorf("an API key: %+v", got)
	}
	c = New(testToken, "", "").WithBase(srv.URL)
	if err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	if got.key != "" || got.auth != "Bearer "+testToken || got.lang != "" {
		t.Errorf("a token: %+v", got)
	}

	for code, want := range map[int]error{http.StatusUnauthorized: ErrKey, http.StatusNotFound: ErrNotFound,
		http.StatusTooManyRequests: ErrBusy} {
		status = code
		if _, err := New(testKey, "", "").WithBase(srv.URL).Movie(ctx, 7); !errors.Is(err, want) {
			t.Errorf("status %d: %v", code, err)
		}
	}
	status = http.StatusBadGateway
	if _, err := New(testKey, "", "").WithBase(srv.URL).Show(ctx, 7); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("status 502: %v", err)
	}

	// An error never holds the key, even when the address does.
	srv.Close()
	_, err := New(testKey, "", "").WithBase(srv.URL).Season(ctx, 7, 1)
	if err == nil || strings.Contains(err.Error(), testKey) || !strings.Contains(err.Error(), "cannot reach TMDB") {
		t.Errorf("unreachable: %v", err)
	}
}

func TestPick(t *testing.T) {
	rs := []Result{
		{ID: 1, Title: "Quiet Harbour", ReleaseDate: "1988-06-01"},
		{ID: 2, Title: "Quiet Harbour", ReleaseDate: "2019-04-02"},
		{ID: 3, Title: "Le Port", OriginalTitle: "Quiet Harbour: The Return", ReleaseDate: "2021-01-01"},
		{ID: 4, Name: "Paper & Lanterns", FirstAirDate: "2020-09-09"},
	}
	for _, c := range []struct {
		name string
		year int
		want int64
	}{
		{"Quiet Harbour", 2019, 2},
		{"quiet harbour", 2020, 2},
		{"Quiet Harbour", 0, 1},
		{"Quiet Harbour - The Return", 2021, 3},
		{"Paper and Lanterns", 2020, 4},
		{"Quiet Harbour", 2005, 0},
		{"Another Film", 2019, 0},
	} {
		got, ok := Pick(rs, c.name, c.year)
		if got != c.want || ok != (c.want != 0) {
			t.Errorf("%q %d: %d, %v", c.name, c.year, got, ok)
		}
	}
	// A search that found one title of the year picks it, whatever its name.
	if id, ok := Pick([]Result{{ID: 9, Title: "Harbour Lights", ReleaseDate: "2019-01-01"}}, "Harbor Lights", 2019); !ok || id != 9 {
		t.Errorf("a single result: %d, %v", id, ok)
	}
	if _, ok := Pick([]Result{{ID: 9, Title: "Harbour Lights", ReleaseDate: "2019-01-01"}}, "Harbor Lights", 0); ok {
		t.Error("a single result without a year to check")
	}
}

func TestImageAndLanguage(t *testing.T) {
	if got := Image("/a1B2c3.jpg", PosterSize); got != "https://image.tmdb.org/t/p/w342/a1B2c3.jpg" {
		t.Errorf("image %q", got)
	}
	for _, bad := range []string{"", "a.jpg", "/../x.jpg", "/x.jpg?y", "//host/x.jpg", "/x.gif"} {
		if got := Image(bad, PosterSize); got != "" {
			t.Errorf("%q: %q", bad, got)
		}
	}
	for s, want := range map[string]bool{"en": true, "en-GB": true, "pt-BR": true, "": false, "EN": false, "en-gb": false, "en_GB": false} {
		if ValidLanguage(s) != want {
			t.Errorf("language %q", s)
		}
	}
	if Year("2019-04-02") != 2019 || Year("") != 0 || Year("19") != 0 {
		t.Error("year")
	}
}
