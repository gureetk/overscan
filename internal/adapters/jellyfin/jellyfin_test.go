// SPDX-License-Identifier: AGPL-3.0-or-later

package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

const testKey = "0123456789abcdef0123456789abcdef"

// film returns a film as a fictional server lists it.
func film(id, name string, extra map[string]any) map[string]any {
	m := map[string]any{"Id": id, "Name": name, "Type": "Movie", "Container": "mkv",
		"ImageTags": map[string]string{"Primary": "p" + id}, "BackdropImageTags": []string{"b" + id}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// newServer returns a fictional Jellyfin server with two films, a series
// with three episodes (one of them missing) and four libraries, which pages
// its lists two titles at a time. It records the paths it was asked for.
func newServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	films := []map[string]any{
		film("a1", "Another Field", map[string]any{"ProductionYear": 2024, "CommunityRating": 7.38,
			"OfficialRating": "PG-13", "Overview": "A field, again.", "Genres": []string{"Drama", "Drama", "Mystery"},
			"People": []map[string]any{{"Name": "A. Actor", "Type": "Actor"}, {"Name": "D. Director", "Type": "Director"},
				{"Name": "W. Writer", "Type": "Writer"}, {"Name": "B. Actor", "Type": "Actor"}},
			"RunTimeTicks": int64(5400) * 10_000_000, "DateCreated": "2026-12-04T10:30:00.0000000Z",
			"PremiereDate": "2024-05-01T00:00:00.0000000Z", "ProductionLocations": []string{"Wales", "", "Wales", "Peru"},
			"MediaSources": []map[string]any{{"Id": "ms1", "MediaStreams": []map[string]any{
				{"Type": "Video", "Index": 0, "Codec": "h264"},
				{"Type": "Audio", "Index": 1, "Codec": "dts", "Language": "eng"},
				{"Type": "Subtitle", "Index": 2, "Codec": "subrip", "Language": "eng", "IsDefault": true, "IsTextSubtitleStream": true},
				{"Type": "Subtitle", "Index": 3, "Codec": "ass", "Language": "fre", "Title": "Signs", "IsForced": true},
				{"Type": "Subtitle", "Index": 4, "Codec": "PGSSUB", "Language": "ger", "IsTextSubtitleStream": false},
				{"Type": "Subtitle", "Index": 5, "Codec": "srt", "Language": "und", "IsExternal": true, "IsHearingImpaired": true,
					"IsTextSubtitleStream": true},
				{"Type": "Subtitle", "Index": 6, "Codec": "dvdsub", "Language": "spa"},
			}}}}),
		film("a2", "Bright Harbour", map[string]any{"Container": "mov,mp4,m4a,3gp,3g2,mj2", "ImageTags": nil,
			"BackdropImageTags": []string{}, "DateCreated": "2026-12-03T09:00:00.123Z"}),
		{"Id": "", "Name": "No ID", "Type": "Movie"},
		{"Id": "a3", "Name": "Wrong Shape", "Type": "Movie", "ProductionYear": "soon"},
		film("a4", "Calm Water", nil),
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path+"?"+r.URL.RawQuery)
		if !strings.Contains(r.Header.Get("Authorization"), `Token="`+testKey+`"`) ||
			!strings.HasPrefix(r.Header.Get("Authorization"), "MediaBrowser ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		q := r.URL.Query()
		var answer any
		switch r.URL.Path {
		case "/System/Info":
			answer = map[string]any{"ServerName": "Den\nServer", "Version": "10.11.2", "Id": "s1"}
		case "/Users":
			answer = []map[string]any{{"Name": "Guest", "Id": "u2"}, {"Name": "Anna", "Id": "u1"}}
		case "/Library/MediaFolders":
			answer = map[string]any{"Items": []map[string]any{
				{"Id": "lib1", "Name": "Films", "CollectionType": "movies"},
				{"Id": "lib2", "Name": "Shows", "CollectionType": "tvshows"},
				{"Id": "lib3", "Name": "Music", "CollectionType": "music"},
				{"Id": "lib4", "Name": "Everything"},
				{"Id": "lib5", "Name": "Playlists", "CollectionType": "playlists"},
				{"Id": "lib6", "Name": "Home", "CollectionType": "homevideos"},
				{"Id": "lib7", "Name": "Folders", "CollectionType": "folders"},
			}}
		case "/UserViews":
			if q.Get("userId") != "u1" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			answer = map[string]any{"Items": []map[string]any{{"Id": "lib1", "Name": "Films", "CollectionType": "movies"}}}
		case "/Items":
			if ids := q.Get("Ids"); ids != "" {
				var found []map[string]any
				for _, f := range films {
					if f["Id"] == ids {
						found = append(found, f)
					}
				}
				answer = map[string]any{"Items": found, "TotalRecordCount": len(found)}
				break
			}
			if q.Get("ParentId") == "lib6" && q.Get("IncludeItemTypes") == "Video" {
				answer = map[string]any{"TotalRecordCount": 2, "Items": []map[string]any{
					{"Id": "v1", "Name": "Birthday", "Type": "Video"},
					{"Id": "v2", "Name": "Birthday: the making of", "Type": "Video", "ExtraType": "BehindTheScenes"},
				}}
				break
			}
			if q.Get("ParentId") != "lib1" || q.Get("IncludeItemTypes") != "Movie" || q.Get("Recursive") != "true" {
				answer = map[string]any{"Items": []any{}, "TotalRecordCount": 0}
				break
			}
			start, _ := strconv.Atoi(q.Get("StartIndex"))
			end := min(start+2, len(films)) // two a page, whatever the client asks
			answer = map[string]any{"Items": films[start:end], "TotalRecordCount": len(films), "StartIndex": start}
		case "/Shows/s9/Episodes":
			if q.Get("IsMissing") != "false" {
				t.Errorf("episodes asked for with IsMissing=%q", q.Get("IsMissing"))
			}
			answer = map[string]any{"Items": []map[string]any{
				{"Id": "e1", "Name": "Rocks", "Type": "Episode", "IndexNumber": 1, "ParentIndexNumber": 1,
					"SeasonName": "Season 1", "Container": "mkv", "RunTimeTicks": int64(2400) * 10_000_000,
					"Overview": "Rocks, mostly.", "ImageTags": map[string]string{"Primary": "t1"}},
				{"Id": "e2", "Name": "Sand", "Type": "Episode", "IndexNumber": 2, "ParentIndexNumber": 1, "LocationType": "Virtual"},
				{"Id": "e3", "Name": "Tides", "Type": "Episode", "IndexNumber": 1, "ParentIndexNumber": 2, "SeasonName": "The Second"},
			}}
		case "/Shows/s0/Episodes":
			w.WriteHeader(http.StatusNotFound)
			return
		case "/Page":
			_, _ = io.WriteString(w, "<html>not here</html>")
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(answer)
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func newClient(srv *httptest.Server, user string) *Client {
	return New(source.Settings{Name: "home-server", Type: "jellyfin", URL: srv.URL + "/", Username: user, Password: testKey},
		srv.Client(), "Overscan/0.2.0")
}

func TestInfo(t *testing.T) {
	srv, _ := newServer(t)
	info, err := newClient(srv, "").Info(context.Background())
	if err != nil || info != (Info{Name: "Den Server", Version: "10.11.2", ID: "s1"}) {
		t.Errorf("info %+v, %v", info, err)
	}
	c := newClient(srv, "")
	c.key = "wrong"
	if _, err := c.Info(context.Background()); !errors.Is(err, ErrKey) {
		t.Errorf("a wrong key: %v", err)
	}
	if err := newClient(srv, "").get(context.Background(), "/Page", nil, &struct{}{}); !errors.Is(err, ErrNotJSON) {
		t.Errorf("a web page: %v", err)
	}
	err = newClient(srv, "").get(context.Background(), "/Nothing", nil, &struct{}{})
	if err == nil || strings.Contains(err.Error(), srv.URL) || !strings.Contains(err.Error(), "404") {
		t.Errorf("a missing page: %v", err)
	}
}

func TestAuthorization(t *testing.T) {
	c := New(source.Settings{URL: "http://jellyfin.example", Password: `a"b,c`}, http.DefaultClient, "Overscan/1.2.3")
	want := `MediaBrowser Client="Overscan", Device="Overscan", DeviceId="overscan", Version="1.2.3", Token="a%22b%2Cc"`
	if got := c.authorization(); got != want {
		t.Errorf("authorization %s", got)
	}
}

func TestLibraries(t *testing.T) {
	srv, asked := newServer(t)
	libs, err := newClient(srv, "").Libraries(context.Background())
	want := []Library{{"lib1", "Films", "movies"}, {"lib2", "Shows", "tvshows"}, {"lib4", "Everything", ""},
		{"lib6", "Home", "homevideos"}}
	if err != nil || !reflect.DeepEqual(libs, want) {
		t.Errorf("libraries %+v, %v", libs, err)
	}
	for i, w := range []struct {
		films  string
		series bool
	}{{TypeMovie, false}, {"", true}, {TypeMovie, true}, {TypeVideo, false}} {
		if i < len(libs) && (libs[i].FilmType() != w.films || libs[i].HasSeries() != w.series) {
			t.Errorf("library %s: films %q, series %v", libs[i].Name, libs[i].FilmType(), libs[i].HasSeries())
		}
	}
	libs, err = newClient(srv, "anna").Libraries(context.Background())
	if err != nil || len(libs) != 1 || libs[0].ID != "lib1" {
		t.Errorf("Anna's libraries %+v, %v", libs, err)
	}
	if !strings.Contains(strings.Join(*asked, " "), "/UserViews?userId=u1") {
		t.Errorf("asked %v", *asked)
	}
	var uerr *UserError
	if _, err := newClient(srv, "nobody").Libraries(context.Background()); !errors.As(err, &uerr) || uerr.Name != "nobody" {
		t.Errorf("no such user: %v", err)
	}
}

func TestItems(t *testing.T) {
	srv, asked := newServer(t)
	var got []Item
	skipped, err := newClient(srv, "").Items(context.Background(), "lib1", TypeMovie, func(it Item) error {
		got = append(got, it)
		return nil
	})
	if err != nil || skipped != 2 || len(got) != 3 {
		t.Fatalf("items %+v, skipped %d, %v", got, skipped, err)
	}
	want := Item{ID: "a1", Name: "Another Field", Type: "Movie", Year: 2024, Rating: 74, AgeRating: "PG-13",
		Plot: "A field, again.", Genres: []string{"Drama", "Mystery"}, Cast: []string{"A. Actor", "B. Actor"},
		Directors: []string{"D. Director"}, Duration: 90 * time.Minute, Added: time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC),
		Premiere: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), Countries: []string{"Wales", "Peru"}, Container: "mkv",
		Poster: "pa1", Backdrop: "ba1"}
	if !reflect.DeepEqual(got[0], want) {
		t.Errorf("first film\n%+v\nwant\n%+v", got[0], want)
	}
	if b := got[1]; b.Container != "mp4" || b.Poster != "" || b.Backdrop != "" ||
		!b.Added.Equal(time.Date(2026, 12, 3, 9, 0, 0, 123e6, time.UTC)) {
		t.Errorf("second film %+v", b)
	}
	pages := 0
	for _, a := range *asked {
		if strings.HasPrefix(a, "/Items?") {
			pages++
			q, _ := url.ParseQuery(strings.SplitN(a, "?", 2)[1])
			if q.Get("Fields") != "Overview,Genres,People,DateCreated,DateLastMediaAdded,ProductionLocations" ||
				q.Get("Limit") != "500" || q.Get("SortBy") != "SortName" {
				t.Errorf("asked %s", a)
			}
		}
	}
	if pages != 3 {
		t.Errorf("%d pages", pages)
	}

	stop := errors.New("enough")
	if _, err := newClient(srv, "").Items(context.Background(), "lib1", TypeMovie, func(Item) error { return stop }); err != stop {
		t.Errorf("stopping: %v", err)
	}
	n := 0
	if _, err := newClient(srv, "").Items(context.Background(), "lib2", TypeSeries, func(Item) error { n++; return nil }); err != nil || n != 0 {
		t.Errorf("an empty library: %d, %v", n, err)
	}
	var names []string
	skipped, err = newClient(srv, "").Items(context.Background(), "lib6", TypeVideo, func(it Item) error {
		names = append(names, it.Name)
		return nil
	})
	if err != nil || skipped != 0 || !reflect.DeepEqual(names, []string{"Birthday"}) {
		t.Errorf("home videos %v, skipped %d, %v", names, skipped, err)
	}
}

func TestItem(t *testing.T) {
	srv, _ := newServer(t)
	it, err := newClient(srv, "").Item(context.Background(), "a1")
	if err != nil || it.Name != "Another Field" {
		t.Errorf("item %+v, %v", it, err)
	}
	if _, err := newClient(srv, "").Item(context.Background(), "zz"); !errors.Is(err, ErrNoTitle) {
		t.Errorf("no such item: %v", err)
	}
}

func TestEpisodes(t *testing.T) {
	srv, _ := newServer(t)
	eps, err := newClient(srv, "").Episodes(context.Background(), "s9")
	if err != nil || len(eps) != 2 {
		t.Fatalf("episodes %+v, %v", eps, err)
	}
	if e := eps[0]; e.ID != "e1" || e.Season != 1 || e.Number != 1 || e.Duration != 40*time.Minute ||
		e.Poster != "t1" || e.Plot != "Rocks, mostly." || e.SeasonName != "Season 1" || e.Container != "mkv" {
		t.Errorf("first episode %+v", e)
	}
	if e := eps[1]; e.ID != "e3" || e.Season != 2 || e.SeasonName != "The Second" {
		t.Errorf("second episode %+v", e)
	}
	if _, err := newClient(srv, "").Episodes(context.Background(), "s0"); !errors.Is(err, ErrNoTitle) {
		t.Errorf("no such series: %v", err)
	}
}

func TestAddresses(t *testing.T) {
	c := New(source.Settings{URL: "http://jellyfin.example:8096/media/"}, http.DefaultClient, "x")
	if got := c.ImageURL("a1", "Primary", "t/1", 600); got != "http://jellyfin.example:8096/media/Items/a1/Images/Primary?maxWidth=600&tag=t%2F1" {
		t.Errorf("poster %s", got)
	}
	if got := c.ImageURL("a1", "Backdrop", "b1", 0); got != "http://jellyfin.example:8096/media/Items/a1/Images/Backdrop/0?tag=b1" {
		t.Errorf("backdrop %s", got)
	}
	if got := c.ImageURL("a1", "Primary", "", 600); got != "" {
		t.Errorf("no tag: %s", got)
	}
}

func TestExtension(t *testing.T) {
	for in, want := range map[string]string{
		"mkv": "mkv", "MKV ": "mkv", "matroska,webm": "mkv", "mov,mp4,m4a,3gp,3g2,mj2": "mp4", "mpegts": "ts",
		"avi": "avi", "webm": "webm", "": "", "a b": "", "../x": "", "averyverylongname": "",
	} {
		if got := Extension(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestParseTime(t *testing.T) {
	for in, want := range map[string]time.Time{
		"2026-12-04T10:30:00.0000000Z":      time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC),
		"2026-12-04T10:30:00.0000000+01:00": time.Date(2026, 12, 4, 9, 30, 0, 0, time.UTC),
		"2026-12-04T10:30:00.1234567":       time.Date(2026, 12, 4, 10, 30, 0, 123456700, time.UTC),
		"0001-01-01T00:00:00.0000000Z":      {},
		"soon":                              {},
	} {
		if got := parseTime(in); !got.Equal(want) {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}

func FuzzDecodeItem(f *testing.F) {
	for _, s := range []string{
		`{"Id":"a1","Name":"A","Type":"Movie","ProductionYear":2024,"CommunityRating":7.4,"RunTimeTicks":54000000000,
		  "People":[{"Name":"P","Type":"Actor"}],"Genres":["G"],"ImageTags":{"Primary":"t"},"BackdropImageTags":["b"]}`,
		`{"Id":"e1","Name":"E","Type":"Episode","IndexNumber":-1,"ParentIndexNumber":99999,"LocationType":"Virtual"}`,
		`{"Id":"x","Name":"Y","CommunityRating":1e308,"RunTimeTicks":9223372036854775807}`,
		`[]`, `null`, `{"Id":1}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		it, ok := decodeItem(data)
		if !ok {
			return
		}
		if it.ID == "" || it.Name == "" || it.Rating < 0 || it.Rating > 100 || it.Duration < 0 || it.Season < 0 ||
			it.Number < 0 || len(it.Cast) > maxPeople {
			t.Errorf("item %+v from %q", it, data)
		}
		_ = fmt.Sprint(it)
	})
}

func TestDecodeSeries(t *testing.T) {
	it, ok := decodeItem(json.RawMessage(`{"Id":"s9","Name":"Coastline","Type":"Series","Container":null,
		"DateCreated":"2025-01-01T00:00:00.0000000Z","DateLastMediaAdded":"2026-12-01T08:00:00.0000000Z"}`))
	if !ok || it.Container != "" || !it.Added.Equal(time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) ||
		!it.Updated.Equal(time.Date(2026, 12, 1, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("series %+v", it)
	}
}

func TestSubtitles(t *testing.T) {
	srv, _ := newServer(t)
	c := newClient(srv, "")
	ctx := context.Background()
	source, subs, err := c.Subtitles(ctx, "a1")
	if err != nil {
		t.Fatal(err)
	}
	want := []Subtitle{
		{Index: 2, Codec: "subrip", Language: "eng", Default: true},
		{Index: 3, Codec: "ass", Language: "fre", Title: "Signs", Forced: true},
		{Index: 5, Codec: "srt", Hearing: true, External: true},
	}
	if source != "ms1" || !reflect.DeepEqual(subs, want) {
		t.Errorf("Subtitles = %q, %+v", source, subs)
	}
	address, format := c.SubtitleURL("a1", source, subs[1])
	if address != srv.URL+"/Videos/a1/ms1/Subtitles/3/Stream.ass?ApiKey="+testKey || format != "ass" {
		t.Errorf("SubtitleURL = %q, %q", address, format)
	}
	if _, format := c.SubtitleURL("a1", source, subs[0]); format != "srt" {
		t.Errorf("SubRip's format %q", format)
	}
	if source, subs, err := c.Subtitles(ctx, "a2"); source != "" || subs != nil || err != nil {
		t.Errorf("a film without sources: %q, %+v, %v", source, subs, err)
	}
	if _, _, err := c.Subtitles(ctx, "zz"); !errors.Is(err, ErrNoTitle) {
		t.Errorf("an unknown film: %v", err)
	}
}
