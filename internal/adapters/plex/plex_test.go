// SPDX-License-Identifier: AGPL-3.0-or-later

package plex

import (
	"context"
	"encoding/json"
	"errors"
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

const testToken = "fictional-token-0123"

// film returns a film as a fictional server lists it.
func film(key, name string, extra map[string]any) map[string]any {
	m := map[string]any{"ratingKey": key, "key": "/library/metadata/" + key, "type": "movie", "title": name,
		"thumb": "/library/metadata/" + key + "/thumb/1700000000", "art": "/library/metadata/" + key + "/art/1700000000",
		"Media": []map[string]any{{"container": "mkv", "Part": []map[string]any{
			{"id": 900, "key": "/library/parts/9" + key + "/1700000000/file.mkv", "container": "mkv"}}}}}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

// newServer returns a fictional Plex server with three films in a library
// that pages its lists two titles at a time, a show with three episodes (one
// without a file), and libraries of films, shows and music. It records the
// paths it was asked for.
func newServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	films := []map[string]any{
		film("101", "Another Field", map[string]any{"year": 2024, "audienceRating": 7.38, "rating": 5.0,
			"contentRating": "PG-13", "summary": "A field, again.", "duration": 5_400_000, "addedAt": 1796380200,
			"originallyAvailableAt": "2024-05-01",
			"Genre":                 []map[string]any{{"tag": "Drama"}, {"tag": "Mystery"}}}),
		film("102", "Bright Harbour", map[string]any{"year": "2019", "rating": "6.1", "thumb": "", "art": nil,
			"Media": []map[string]any{{"container": "mp4", "Part": []map[string]any{
				{"id": 901, "key": "/library/parts/901/1700000001/file.mp4"}}}}}),
		{"ratingKey": "", "type": "movie", "title": "No key"},
		{"ratingKey": "103", "type": "show", "title": "A show among the films"},
		film("104", "Cold Orchard", map[string]any{"Media": []map[string]any{{"container": "avi", "Part": []map[string]any{
			{"id": 902, "key": "http://elsewhere.example/file.avi"}}}}}),
	}
	details := film("101", "Another Field", map[string]any{"year": 2024, "audienceRating": 7.38,
		"Role":     []map[string]any{{"tag": "A. Actor"}, {"tag": "B. Actor"}},
		"Director": []map[string]any{{"tag": "D. Director"}},
		"Country":  []map[string]any{{"tag": "Wales"}},
		"Media": []map[string]any{{"container": "mkv", "Part": []map[string]any{{"id": 900,
			"key": "/library/parts/9101/1700000000/file.mkv", "container": "mkv", "Stream": []map[string]any{
				{"id": 1, "streamType": 1, "index": 0, "codec": "h264"},
				{"id": 2, "streamType": 2, "index": 1, "codec": "dca", "languageCode": "eng"},
				{"id": 3, "streamType": 3, "index": 2, "codec": "srt", "languageCode": "eng", "default": true},
				{"id": 4, "streamType": 3, "index": 3, "codec": "ass", "languageCode": "fre", "title": "Signs", "forced": "1"},
				{"id": 5, "streamType": 3, "index": 4, "codec": "pgs", "languageCode": "ger"},
				{"id": 6, "streamType": 3, "codec": "srt", "languageCode": "und", "hearingImpaired": true,
					"key": "/library/streams/6"},
				{"id": 7, "streamType": 3, "codec": "vobsub", "languageCode": "spa", "key": "/library/streams/7"},
			}}}}}})
	episodes := []map[string]any{
		{"ratingKey": "301", "type": "episode", "title": "Arrival", "index": 1, "parentIndex": 1, "parentTitle": "Season 1",
			"thumb": "/library/metadata/301/thumb/1700000000", "duration": 1_800_000,
			"Media": []map[string]any{{"Part": []map[string]any{{"key": "/library/parts/3011/1700000000/file.mkv", "container": "mkv"}}}}},
		{"ratingKey": "302", "type": "episode", "title": "Departure", "index": 2, "parentIndex": 1, "parentTitle": "Season 1"},
		{"ratingKey": "303", "type": "episode", "title": "Return", "index": 1, "parentIndex": 2, "parentTitle": "Specials Too",
			"Media": []map[string]any{{"container": "mp4", "Part": []map[string]any{{"key": "/library/parts/3031/1700000002/file.mp4"}}}}},
	}
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.Header.Get("X-Plex-Token") != testToken {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		send := func(v map[string]any) {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"MediaContainer": v})
		}
		page := func(all []map[string]any) {
			start, _ := strconv.Atoi(r.URL.Query().Get("X-Plex-Container-Start"))
			end := min(start+2, len(all))
			if start > len(all) {
				start = len(all)
			}
			send(map[string]any{"size": end - start, "totalSize": len(all), "offset": start, "Metadata": all[start:end]})
		}
		switch r.URL.Path {
		case "/":
			send(map[string]any{"friendlyName": "Fictional\nDen", "version": "1.42.2.10156-f737b826c",
				"machineIdentifier": "abc123"})
		case "/library/sections/all":
			send(map[string]any{"Directory": []map[string]any{
				{"key": "1", "title": "Films", "type": "movie"},
				{"key": 2, "title": "Shows", "type": "show"},
				{"key": "3", "title": "Music", "type": "artist"},
				{"key": "../4", "title": "Odd", "type": "movie"},
			}})
		case "/library/sections/1/all":
			if r.URL.Query().Get("type") != "1" {
				http.Error(w, "type", http.StatusBadRequest)
				return
			}
			page(films)
		case "/library/sections/2/all":
			page([]map[string]any{{"ratingKey": "300", "type": "show", "title": "Coastline", "addedAt": "1796380200"}})
		case "/library/metadata/101":
			send(map[string]any{"size": 1, "Metadata": []map[string]any{details}})
		case "/library/metadata/300/allLeaves":
			send(map[string]any{"size": len(episodes), "Metadata": episodes})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &asked
}

func newClient(srv *httptest.Server, token string) *Client {
	return New(source.Settings{Name: "main", Type: "plex", URL: srv.URL + "/", Password: token}, srv.Client(), "Overscan/0.2.0")
}

func TestInfo(t *testing.T) {
	srv, _ := newServer(t)
	info, err := newClient(srv, testToken).Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := (Info{Name: "Fictional Den", Version: "1.42.2.10156-f737b826c", ID: "abc123"}); info != want {
		t.Errorf("info %+v, want %+v", info, want)
	}
	if _, err := newClient(srv, "wrong").Info(context.Background()); !errors.Is(err, ErrToken) {
		t.Errorf("a wrong token: %v", err)
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Write([]byte("<html>a router</html>"))
			return
		}
		w.Write([]byte(`{"MediaContainer":{"size":0}}`))
	}))
	defer page.Close()
	if _, err := newClient(page, testToken).Info(context.Background()); !errors.Is(err, ErrNotJSON) {
		t.Errorf("a web page: %v", err)
	}
}

func TestHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Write([]byte(`{"MediaContainer":{"version":"1.0","machineIdentifier":"x"}}`))
	}))
	defer srv.Close()
	if _, err := newClient(srv, testToken).Info(context.Background()); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"X-Plex-Token": testToken, "Accept": "application/json",
		"X-Plex-Client-Identifier": "overscan", "X-Plex-Product": "Overscan", "X-Plex-Version": "0.2.0",
		"User-Agent": "Overscan/0.2.0"} {
		if got.Get(k) != v {
			t.Errorf("%s: %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestLibraries(t *testing.T) {
	srv, _ := newServer(t)
	libs, err := newClient(srv, testToken).Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []Library{{Key: "1", Name: "Films", Type: TypeMovie}, {Key: "2", Name: "Shows", Type: TypeShow}}
	if !reflect.DeepEqual(libs, want) {
		t.Errorf("libraries %+v, want %+v", libs, want)
	}

	// A server without the documented list has the older one.
	var asked []string
	old := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = append(asked, r.URL.Path)
		if r.URL.Path != "/library/sections" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"MediaContainer":{"Directory":[{"key":"7","title":"","type":"show"}]}}`))
	}))
	defer old.Close()
	libs, err = newClient(old, testToken).Libraries(context.Background())
	if err != nil || len(libs) != 1 || libs[0] != (Library{Key: "7", Name: "Library 7", Type: TypeShow}) {
		t.Errorf("older list: %+v, %v", libs, err)
	}
	if want := []string{"/library/sections/all", "/library/sections"}; !reflect.DeepEqual(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
}

func TestItems(t *testing.T) {
	srv, asked := newServer(t)
	var got []Item
	skipped, err := newClient(srv, testToken).Items(context.Background(), "1", TypeMovie, func(it Item) error {
		got = append(got, it)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if skipped != 2 || len(got) != 3 {
		t.Fatalf("%d titles, %d skipped: %+v", len(got), skipped, got)
	}
	if n := len(*asked); n != 3 {
		t.Errorf("%d requests for five entries two at a time", n)
	}
	a := got[0]
	want := Item{Key: "101", Name: "Another Field", Type: TypeMovie, Year: 2024, Rating: 74, AgeRating: "PG-13",
		Plot: "A field, again.", Genres: []string{"Drama", "Mystery"}, Duration: 90 * time.Minute,
		Added: time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC), Premiere: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC),
		Poster: "/library/metadata/101/thumb/1700000000", Backdrop: "/library/metadata/101/art/1700000000",
		File: File{Key: "/library/parts/9101/1700000000/file.mkv", Container: "mkv"}}
	if !reflect.DeepEqual(a, want) {
		t.Errorf("first film\n %+v\nwant\n %+v", a, want)
	}
	b := got[1]
	if b.Year != 2019 || b.Rating != 61 || b.Poster != "" || b.Backdrop != "" ||
		b.File != (File{Key: "/library/parts/901/1700000001/file.mp4", Container: "mp4"}) {
		t.Errorf("second film %+v", b)
	}
	if c := got[2]; c.File != (File{}) {
		t.Errorf("a part elsewhere was kept: %+v", c.File)
	}

	var shows []Item
	if _, err := newClient(srv, testToken).Items(context.Background(), "2", TypeShow, func(it Item) error {
		shows = append(shows, it)
		return nil
	}); err != nil || len(shows) != 1 || shows[0].Added != time.Unix(1796380200, 0).UTC() || shows[0].File != (File{}) {
		t.Errorf("shows %+v, %v", shows, err)
	}

	stop := errors.New("stop")
	if _, err := newClient(srv, testToken).Items(context.Background(), "1", TypeMovie, func(Item) error { return stop }); err != stop {
		t.Errorf("each's error: %v", err)
	}
	if _, err := newClient(srv, testToken).Items(context.Background(), "1/../2", TypeMovie, nil); err == nil {
		t.Error("a key with a path in it was asked for")
	}
}

func TestItemAndSubtitles(t *testing.T) {
	srv, _ := newServer(t)
	c := newClient(srv, testToken)
	it, err := c.Item(context.Background(), "101")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(it.Cast, []string{"A. Actor", "B. Actor"}) || !reflect.DeepEqual(it.Directors, []string{"D. Director"}) ||
		!reflect.DeepEqual(it.Countries, []string{"Wales"}) {
		t.Errorf("details %+v", it)
	}
	subs, err := c.Subtitles(context.Background(), "101")
	if err != nil {
		t.Fatal(err)
	}
	want := []Subtitle{
		{ID: 3, Index: 2, Codec: "srt", Language: "eng", Default: true},
		{ID: 4, Index: 3, Codec: "ass", Language: "fre", Title: "Signs", Forced: true},
		{ID: 6, Index: -1, Codec: "srt", Hearing: true, External: true},
	}
	if !reflect.DeepEqual(subs, want) {
		t.Errorf("subtitles\n %+v\nwant\n %+v", subs, want)
	}
	if _, err := c.Item(context.Background(), "999"); !errors.Is(err, ErrNoTitle) {
		t.Errorf("an unknown title: %v", err)
	}
	if _, err := c.Item(context.Background(), "../1"); !errors.Is(err, ErrNoTitle) {
		t.Errorf("a key with a path in it: %v", err)
	}
}

func TestEpisodes(t *testing.T) {
	srv, _ := newServer(t)
	eps, err := newClient(srv, testToken).Episodes(context.Background(), "300")
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 2 {
		t.Fatalf("%d episodes: %+v", len(eps), eps)
	}
	if e := eps[0]; e.Key != "301" || e.Season != 1 || e.Number != 1 || e.SeasonName != "Season 1" ||
		e.Duration != 30*time.Minute || e.Poster != "/library/metadata/301/thumb/1700000000" ||
		e.File != (File{Key: "/library/parts/3011/1700000000/file.mkv", Container: "mkv"}) {
		t.Errorf("first episode %+v", e)
	}
	if e := eps[1]; e.Key != "303" || e.Season != 2 || e.SeasonName != "Specials Too" || e.File.Container != "mp4" {
		t.Errorf("last episode %+v", e)
	}
	if _, err := newClient(srv, testToken).Episodes(context.Background(), "999"); !errors.Is(err, ErrNoTitle) {
		t.Errorf("an unknown show: %v", err)
	}
}

func TestAddresses(t *testing.T) {
	c := New(source.Settings{URL: "http://192.0.2.10:32400/", Password: "t&k"}, http.DefaultClient, "Overscan/0.2.0")
	got := c.PictureURL("/library/metadata/101/thumb/1700000000", 480, 720)
	u, err := url.Parse(got)
	if err != nil || u.Host != "192.0.2.10:32400" || u.Path != "/photo/:/transcode" {
		t.Fatalf("picture address %q", got)
	}
	q := u.Query()
	if q.Get("url") != "/library/metadata/101/thumb/1700000000" || q.Get("width") != "480" || q.Get("height") != "720" ||
		q.Get("X-Plex-Token") != "t&k" || q.Get("minSize") != "1" {
		t.Errorf("picture query %v", q)
	}
	for _, bad := range []string{"", "library/1", "//elsewhere.example/a.jpg", "/a?b", "/a/../../etc", "http://x/a.jpg"} {
		if got := c.PictureURL(bad, 1, 1); got != "" {
			t.Errorf("picture %q: %q", bad, got)
		}
	}
	address, format := c.SubtitleURL(Subtitle{ID: 6, Codec: "srt", External: true})
	if format != "srt" || address != "http://192.0.2.10:32400/library/streams/6.srt?X-Plex-Token=t%26k&encoding=utf-8&format=srt" {
		t.Errorf("subtitle %q, %q", address, format)
	}
	if address, format := c.SubtitleURL(Subtitle{ID: 8, Codec: "ass", External: true}); !strings.Contains(address, "/8.ass?") || format != "ass" {
		t.Errorf("SubStation subtitle %q, %q", address, format)
	}
	if address, _ := c.SubtitleURL(Subtitle{ID: 3, Index: 2, Codec: "srt"}); address != "" {
		t.Errorf("a track in the file has an address: %q", address)
	}
}

func TestPictureAddress(t *testing.T) {
	kept := PictureAddress("/library/metadata/101/thumb/1700000000", 480, 720)
	if kept != "plex:480x720/library/metadata/101/thumb/1700000000" || !IsPicture(kept) {
		t.Fatalf("kept %q", kept)
	}
	c := New(source.Settings{URL: "http://192.0.2.10:32400", Password: "tok"}, nil, "")
	if got, want := c.PictureFor(kept), c.PictureURL("/library/metadata/101/thumb/1700000000", 480, 720); got != want || got == "" {
		t.Errorf("fetch %q, want %q", got, want)
	}
	for _, bad := range []string{"", "http://192.0.2.10/a.jpg", "plex:480x720", "plex:0x720/a", "plex:480x/a",
		"plex:480x720/a?b", "plex:480x720//elsewhere.example/a", "plex:+480x720/a", "plex:480x720/a/../../b"} {
		if got := c.PictureFor(bad); got != "" {
			t.Errorf("fetch %q: %q", bad, got)
		}
	}
	if PictureAddress("relative", 1, 1) != "" || PictureAddress("/a", 0, 1) != "" || PictureAddress("/a", 1, 5000) != "" {
		t.Error("bad pictures were kept")
	}
}

func TestExtension(t *testing.T) {
	for in, want := range map[string]string{"mkv": "mkv", "MP4": "mp4", "mpegts": "ts", "matroska": "mkv", "": "",
		"mov,mp4": "", "a/b": ""} {
		if got := Extension(in); got != want {
			t.Errorf("Extension(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDecodeLoose(t *testing.T) {
	raw := `{"ratingKey":5,"type":"Movie","title":" Five ","year":"1999","duration":"60000","audienceRating":"11",
		"rating":8,"addedAt":"x","Media":[{"Part":[{"key":"/library/parts/5/1/file.mkv","container":"MKV"}]}]}`
	it, ok := decodeItem(json.RawMessage(raw))
	if !ok || it.Key != "5" || it.Type != TypeMovie || it.Name != "Five" || it.Year != 1999 || it.Duration != time.Minute ||
		it.Rating != 80 || !it.Added.IsZero() || it.File.Container != "mkv" {
		t.Errorf("decoded %+v, %v", it, ok)
	}
	if _, ok := decodeItem(json.RawMessage(`{"ratingKey":"1a","title":"x"}`)); ok {
		t.Error("a key that is not digits was taken")
	}
	if _, ok := decodeItem(json.RawMessage(`[1,2]`)); ok {
		t.Error("an array was taken")
	}
}

func FuzzDecodeItem(f *testing.F) {
	for _, s := range []string{
		`{"ratingKey":"1","type":"movie","title":"A","year":2024,"audienceRating":7.4,"duration":5400000,
		  "Role":[{"tag":"P"}],"Genre":[{"tag":"G"}],"thumb":"/library/metadata/1/thumb/2",
		  "Media":[{"Part":[{"key":"/library/parts/1/2/file.mkv","Stream":[{"streamType":3,"codec":"srt","index":2}]}]}]}`,
		`{"ratingKey":"2","type":"episode","title":"E","index":-1,"parentIndex":99999,"addedAt":"1e300"}`,
		`{"ratingKey":"3","title":"Y","rating":1e308,"duration":9223372036854775807,"year":"NaN"}`,
		`[]`, `null`, `{"ratingKey":1}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		it, ok := decodeItem(data)
		if !ok {
			return
		}
		if !validKey(it.Key) || it.Name == "" || it.Rating < 0 || it.Rating > 100 || it.Duration < 0 || it.Season < 0 ||
			it.Number < 0 || len(it.Cast) > maxPeople || it.Poster != "" && !validPath(it.Poster) ||
			it.File.Key != "" && !strings.HasPrefix(it.File.Key, "/library/parts/") || len(it.subtitles) > 64 {
			t.Errorf("item %+v from %q", it, data)
		}
	})
}
