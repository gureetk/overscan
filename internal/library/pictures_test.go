// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// TestTMDBQueries checks the names and years that providers' titles are
// looked up on TMDB by (D143).
func TestTMDBQueries(t *testing.T) {
	for _, tc := range []struct {
		name  string
		year  int
		names []string
		want  int
	}{
		{"EN - Calm Water (2021)", 0, []string{"Calm Water", "EN - Calm Water"}, 2021},
		{"Calm Water (2020)", 2021, []string{"Calm Water"}, 2021},
		{"[UK] Calm Water [4K]", 0, []string{"Calm Water"}, 0},
		{"UK| Calm Water", 0, []string{"Calm Water", "UK| Calm Water"}, 0},
		{"|FR| Calm Water", 0, []string{"Calm Water", "|FR| Calm Water"}, 0},
		{"IT: Chapter Two", 0, []string{"Chapter Two", "IT: Chapter Two"}, 0},
		{"Calm Water - 2021", 0, []string{"Calm Water"}, 2021},
		{"Calm Water FHD 1080p", 0, []string{"Calm Water"}, 0},
		{"Calm Water (Multi-Sub)", 0, []string{"Calm Water"}, 0},
		{"Calm Water - HD", 0, []string{"Calm Water"}, 0},
		{"Harbour Lights 2049", 0, []string{"Harbour Lights 2049"}, 0},
		{"2012", 0, []string{"2012"}, 0},
		{"Calm Water", 0, []string{"Calm Water"}, 0},
	} {
		names, year := tmdbQueries(tc.name, tc.year)
		if !reflect.DeepEqual(names, tc.names) || year != tc.want {
			t.Errorf("%q, %d: %q, %d; want %q, %d", tc.name, tc.year, names, year, tc.names, tc.want)
		}
	}
}

// TestTMDBPictures gives an Xtream server's films TMDB's pictures where
// theirs want replacing, once the setting is on: one without a poster, by
// its name and year; one whose details give a TMDB ID, by it; and none for
// a film whose poster loads (D143).
func TestTMDBPictures(t *testing.T) {
	p, panelSrv := newPanel(t)
	p.set("get_vod_streams", `[{"stream_id":5001,"name":"EN - Calm Water (2021)","category_id":"21","added":"1767225600"},
		{"stream_id":5002,"name":"Quiet Harbour","category_id":"22","stream_icon":"http://img.example/quiet.jpg","added":"1767225601"},
		{"stream_id":5003,"name":"Odd Name","category_id":"22","added":"1767225602"}]`)
	p.set("get_vod_info", `{"info":{"plot":"Odd.","tmdb_id":"11"},"movie_data":{"stream_id":5003}}`)
	l, st, src, logs := newLibrary(t, panelSrv)
	f, srv := newFakeTMDB(t)
	withTMDBKey(t, l, st, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// Off at first: TMDB is not asked.
	l.FillPictures(ctx)
	time.Sleep(50 * time.Millisecond)
	if f.count("/3/search/movie") != 0 || rowNamed(t, st, store.KindMovie, "EN - Calm Water (2021)").Poster != "" {
		t.Fatalf("off: asked %v", f.asked)
	}
	// The details of Odd Name give its TMDB ID.
	if _, err := l.Open(ctx, rowNamed(t, st, store.KindMovie, "Odd Name").ID); err != nil {
		t.Fatal(err)
	}

	if err := st.SetTMDBPictures(ctx, true); err != nil {
		t.Fatal(err)
	}
	l.FillPictures(ctx)
	waitFor(t, func() bool {
		return rowNamed(t, st, store.KindMovie, "EN - Calm Water (2021)").Poster == "https://image.tmdb.org/t/p/w342/calmposter.jpg" &&
			rowNamed(t, st, store.KindMovie, "Odd Name").Poster == "https://image.tmdb.org/t/p/w342/calmposter.jpg"
	})
	if f.count("/3/search/movie") != 1 || f.count("/3/movie/11") != 2 {
		t.Errorf("asked %v", f.asked)
	}
	if row := rowNamed(t, st, store.KindMovie, "Quiet Harbour"); row.Poster != "http://img.example/quiet.jpg" {
		t.Errorf("a film whose poster loads: %+v", row)
	}
	o, err := l.Open(ctx, rowNamed(t, st, store.KindMovie, "EN - Calm Water (2021)").ID)
	if err != nil || o.Summary.Backdrop != "https://image.tmdb.org/t/p/w1280/calmback.jpg" {
		t.Errorf("Calm Water's wide picture: %+v, %v", o.Summary, err)
	}
	waitFor(t, func() bool { return strings.Contains(logs.String(), "gave a source's films TMDB's pictures") })

	// Quiet Harbour's poster does not load; TMDB does not know it, and is
	// not asked again within a week.
	if err := st.PictureFailed(ctx, src, "http://img.example/quiet.jpg", time.Now()); err != nil {
		t.Fatal(err)
	}
	l.FillPictures(ctx)
	waitFor(t, func() bool { return f.count("/3/search/movie") == 2 })
	waitFor(t, func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return len(l.pictureRuns) == 0
	})
	l.FillPictures(ctx)
	time.Sleep(50 * time.Millisecond)
	if f.count("/3/search/movie") != 2 {
		t.Errorf("asked again %v", f.asked)
	}
	if row := rowNamed(t, st, store.KindMovie, "Quiet Harbour"); row.Poster != "http://img.example/quiet.jpg" {
		t.Errorf("a film TMDB does not know: %+v", row)
	}

	// Off again: their own pictures come back.
	if err := st.SetTMDBPictures(ctx, false); err != nil {
		t.Fatal(err)
	}
	if row := rowNamed(t, st, store.KindMovie, "EN - Calm Water (2021)"); row.Poster != "" {
		t.Errorf("off again: %+v", row)
	}
}

// TestPicturesDoneWhen walks the line of M9's Done when that is about
// TMDB (the plan, section 14): titles whose pictures are missing, broken
// or placeholders show TMDB's, and a title whose own picture loads keeps
// it. The fake TMDB knows every title of a fictional Xtream server by its
// name and year.
func TestPicturesDoneWhen(t *testing.T) {
	films := []string{`{"stream_id":6001,"name":"Film 1 (2020)","category_id":"21","added":"1767225600"}`, // missing
		`{"stream_id":6002,"name":"Film 2 (2020)","category_id":"21","stream_icon":"http://img.example/two.jpg","added":"1767225600"}`} // broken
	for n := 3; n <= 12; n++ { // ten that share a placeholder
		films = append(films, fmt.Sprintf(`{"stream_id":%d,"name":"Film %d (2020)","category_id":"22","stream_icon":"http://img.example/none.jpg","added":"1767225600"}`, 6000+n, n))
	}
	films = append(films, `{"stream_id":6013,"name":"Film 13 (2020)","category_id":"22","stream_icon":"http://img.example/thirteen.jpg","added":"1767225600"}`)
	p, panelSrv := newPanel(t)
	p.set("get_vod_streams", "["+strings.Join(films, ",")+"]")
	p.set("get_series", `[{"series_id":7001,"name":"Show 1 (2019)","category_id":"31","last_modified":"1767225600"},
		{"series_id":7002,"name":"Show 2 (2019)","category_id":"31","cover":"http://img.example/show2.jpg","last_modified":"1767225600"}]`)
	l, st, src, _ := newLibrary(t, panelSrv)
	tmdb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/json")
		var n int
		switch {
		case r.URL.Path == "/3/search/movie" && q.Get("year") == "2020":
			if _, err := fmt.Sscanf(q.Get("query"), "Film %d", &n); err == nil {
				fmt.Fprintf(w, `{"results":[{"id":%d,"title":"Film %d","release_date":"2020-01-01"}]}`, 100+n, n)
				return
			}
			fmt.Fprint(w, `{"results":[]}`)
		case strings.HasPrefix(r.URL.Path, "/3/movie/"):
			fmt.Sscanf(r.URL.Path, "/3/movie/%d", &n)
			fmt.Fprintf(w, `{"id":%d,"title":"Film %d","release_date":"2020-01-01","poster_path":"/f%d.jpg","backdrop_path":"/b%d.jpg"}`,
				n, n-100, n-100, n-100)
		case r.URL.Path == "/3/search/tv" && q.Get("first_air_date_year") == "2019":
			if _, err := fmt.Sscanf(q.Get("query"), "Show %d", &n); err == nil {
				fmt.Fprintf(w, `{"results":[{"id":%d,"name":"Show %d","first_air_date":"2019-01-01"}]}`, 300+n, n)
				return
			}
			fmt.Fprint(w, `{"results":[]}`)
		case strings.HasPrefix(r.URL.Path, "/3/tv/"):
			fmt.Sscanf(r.URL.Path, "/3/tv/%d", &n)
			fmt.Fprintf(w, `{"id":%d,"name":"Show %d","first_air_date":"2019-01-01","poster_path":"/s%d.jpg"}`, n, n-300, n-300)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(tmdb.Close)
	withTMDBKey(t, l, st, tmdb)
	ctx := context.Background()
	for _, kind := range []string{store.KindMovie, store.KindSeries} {
		if _, err := l.Want(ctx, kind, 5*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	// Film 2's poster did not load for a device.
	if err := st.PictureFailed(ctx, src, "http://img.example/two.jpg", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTMDBPictures(ctx, true); err != nil {
		t.Fatal(err)
	}
	l.FillPictures(ctx)
	want := map[string]string{"Film 13 (2020)": "http://img.example/thirteen.jpg", "Show 1 (2019)": "https://image.tmdb.org/t/p/w342/s1.jpg",
		"Show 2 (2019)": "http://img.example/show2.jpg"}
	for n := 1; n <= 12; n++ {
		want[fmt.Sprintf("Film %d (2020)", n)] = fmt.Sprintf("https://image.tmdb.org/t/p/w342/f%d.jpg", n)
	}
	posters := func() map[string]string {
		out := map[string]string{}
		for _, kind := range []string{store.KindMovie, store.KindSeries} {
			rows, err := st.RecentTitles(ctx, kind, 100, store.EverySource)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range rows {
				out[r.Name] = r.Poster
			}
		}
		return out
	}
	for range 500 {
		if reflect.DeepEqual(posters(), want) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := posters(); !reflect.DeepEqual(got, want) {
		t.Errorf("posters:\n%v\nwant\n%v", got, want)
	}
}
