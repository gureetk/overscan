// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// filmNames are the names of a fictional panel's films, the latest added
// first.
var filmNames = []string{"The Long Field", "Quiet Harbour", "Copper Signal", "Paper Orchard", "Velvet Tide",
	"Hollow Summit", "Winter Garden", "Golden Bridge", "Hidden Window", "Painted Station", "Last River",
	"Open Letter", "Silver Island", "Northern Road", "Distant Shore", "Broken Meadow"}

// libraryPanel is a fictional Xtream panel with films and series, which
// serves their files and counts the calls made of its API by action.
type libraryPanel struct {
	*httptest.Server
	mu    sync.Mutex
	calls map[string]int
	hold  chan struct{} // if set, files are sent once it is closed
}

const panelPassword = "pw-films"

// file is what the fictional panel serves for a film or an episode.
func file(id string) []byte {
	return bytes.Repeat([]byte("FILE "+id+" "), 400)
}

func newLibraryPanel(t *testing.T) *libraryPanel {
	t.Helper()
	var films []string
	for i, name := range filmNames {
		extra := ""
		if i == 0 {
			extra = `,"stream_icon":"http://img.example/5001.jpg","rating":"7.4","year":"2024"`
		}
		films = append(films, fmt.Sprintf(`{"stream_id":%d,"name":%q,"category_id":"%d","container_extension":"mkv","added":"%d"%s}`,
			5001+i, name, 21+i%2, time.Date(2026, 12, 20-i, 0, 0, 0, 0, time.UTC).Unix(), extra))
	}
	replies := map[string]string{
		"get_vod_categories":    `[{"category_id":"21","category_name":"Drama"},{"category_id":"22","category_name":"Comedy"}]`,
		"get_vod_streams":       "[" + strings.Join(films, ",") + "]",
		"get_series_categories": `[{"category_id":"31","category_name":"Documentary"}]`,
		"get_series": `[{"series_id":7001,"name":"Coastline","category_id":"31","plot":"Waves.","last_modified":"1796000000",
			"cover":"http://img.example/7001.jpg","episode_run_time":"40"},
			{"series_id":7002,"name":"Harbour Lights","category_id":"31","last_modified":"1795000000"}]`,
		"get_vod_info": `{"info":{"plot":"A long field."},"movie_data":{"stream_id":5001,"container_extension":"mkv"}}`,
	}
	seriesInfo := map[string]string{
		"7001": `{"info":{"name":"Coastline","plot":"Waves, closer."},
			"seasons":[{"season_number":1,"name":"The First Season"}],
			"episodes":{"1":[{"id":"9001","episode_num":1,"title":"Rocks","container_extension":"mkv"},
				{"id":"9002","episode_num":2,"title":"Sand","container_extension":"mkv"}],
			"2":[{"id":"9003","episode_num":1,"title":"Tides","container_extension":"mp4"}]}}`,
		// Two episodes with the same number.
		"7002": `{"info":{"name":"Harbour Lights"},
			"episodes":{"1":[{"id":"9101","episode_num":3,"title":"One","container_extension":"mkv"},
				{"id":"9102","episode_num":3,"title":"Two","container_extension":"mkv"}]}}`,
	}
	p := &libraryPanel{calls: map[string]int{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/movie/viewer/"+panelPassword+"/"); ok {
			p.serveFile(w, r, "film "+strings.TrimSuffix(rest, ".mkv"))
			return
		}
		if rest, ok := strings.CutPrefix(r.URL.Path, "/series/viewer/"+panelPassword+"/"); ok {
			p.serveFile(w, r, "episode "+rest[:strings.IndexByte(rest+".", '.')])
			return
		}
		q := r.URL.Query()
		action := q.Get("action")
		p.mu.Lock()
		p.calls[action]++
		p.mu.Unlock()
		reply, ok := replies[action]
		if action == "get_series_info" {
			reply, ok = seriesInfo[q.Get("series_id")]
		}
		if q.Get("username") != "viewer" || q.Get("password") != panelPassword || !ok {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, reply)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *libraryPanel) serveFile(w http.ResponseWriter, r *http.Request, id string) {
	p.mu.Lock()
	hold := p.hold
	p.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "video/x-matroska")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(file(id)))
}

// called returns how many times the panel's API was called with an action.
func (p *libraryPanel) called(action string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[action]
}

// withLibrary gives the harness an Xtream source of films and series on
// the panel, with a connection limit of 1, and a library that reads them.
// It returns a server that serves the hub's links.
func (h *harness) withLibrary(p *libraryPanel) *httptest.Server {
	h.t.Helper()
	ctx := context.Background()
	src, err := h.store.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeXtream, URL: p.URL,
		Username: "viewer", Password: panelPassword, MaxConnections: 1})
	if err != nil {
		h.t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	lib := library.New(h.store, log, "test")
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lib.Run(runCtx)
		close(done)
	}()
	h.t.Cleanup(func() {
		cancel()
		<-done
	})
	h.api.UseLibrary(lib)
	h.hub.SetSources(map[int64]hub.Source{src: {Name: "films", MaxConnections: 1}})
	front := httptest.NewServer(h.hub)
	h.t.Cleanup(front.Close)
	return front
}

// categories returns the IDs of a module's categories by their titles.
func (h *harness) categories(module, mac, token string) (map[string]string, []string) {
	h.t.Helper()
	var got []genre
	h.js(h.call("type="+module+"&action=get_categories", mac, token), &got)
	ids := map[string]string{}
	var titles []string
	for _, g := range got {
		if g.Censored != "0" {
			h.t.Errorf("category %+v", g)
		}
		ids[g.Title] = g.ID
		titles = append(titles, g.Title)
	}
	return ids, titles
}

func (h *harness) titles(query, mac, token string) titlePage[vodItem] {
	h.t.Helper()
	var got titlePage[vodItem]
	h.js(h.call(query, mac, token), &got)
	return got
}

func itemNames(items []vodItem) string {
	var out []string
	for _, it := range items {
		out = append(out, it.Name)
	}
	return strings.Join(out, ", ")
}

type linkAnswer struct {
	Cmd   string `json:"cmd"`
	Error string `json:"error"`
}

func (h *harness) link(module, cmd, series, mac, token string) linkAnswer {
	h.t.Helper()
	var got linkAnswer
	q := "type=" + module + "&action=create_link&cmd=" + url.QueryEscape(cmd)
	if series != "" {
		q += "&series=" + series
	}
	h.js(h.call(q, mac, token), &got)
	return got
}

// fetch fetches the part of a link's file from byte 100 to 199 through
// the hub at front, as a player that seeks does.
func fetch(t *testing.T, front *httptest.Server, link string) (int, string) {
	t.Helper()
	u, err := url.Parse(link)
	if err != nil || u.Host != "example.com" || !strings.HasPrefix(u.Path, "/vod/") {
		t.Fatalf("link %q, %v", link, err)
	}
	req, _ := http.NewRequest(http.MethodGet, front.URL+u.Path, nil)
	req.Header.Set("Range", "bytes=100-199")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestFilmsAndSeriesWithoutLibrary(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.handshake(macA)
	for _, module := range []string{"vod", "series"} {
		if _, titles := h.categories(module, macA, token); len(titles) != 0 {
			t.Errorf("%s categories %q", module, titles)
		}
		if got := h.titles("type="+module+"&action=get_ordered_list&category=*&p=1", macA, token); got.TotalItems != 0 ||
			got.MaxPageItems != pageSize || len(got.Data) != 0 {
			t.Errorf("%s list %+v", module, got)
		}
		if got := h.link(module, "/media/1.mpg", "", macA, token); got.Cmd != "" || got.Error != "nothing_to_play" {
			t.Errorf("%s link %+v", module, got)
		}
	}
}

func TestFilmLists(t *testing.T) {
	h := newHarness(t)
	panel := newLibraryPanel(t)
	h.withLibrary(panel)
	h.approve(macA)
	token := h.handshake(macA)

	ids, titles := h.categories("vod", macA, token)
	if !reflect.DeepEqual(titles, []string{"All", "Drama", "Comedy"}) || ids["All"] != "*" {
		t.Fatalf("categories %q %v", titles, ids)
	}
	// The whole list was read in one request, and no film's details.
	if panel.called("get_vod_streams") != 1 || panel.called("get_vod_info") != 0 {
		t.Errorf("calls %v", panel.calls)
	}

	all := h.titles("type=vod&action=get_ordered_list&category=*&genre=*&sortby=added&fav=0&p=1", macA, token)
	if all.TotalItems != 16 || all.MaxPageItems != 14 || len(all.Data) != 14 ||
		!strings.HasPrefix(itemNames(all.Data), "The Long Field, Quiet Harbour, Copper Signal") {
		t.Fatalf("first page: %d %d %q", all.TotalItems, all.MaxPageItems, itemNames(all.Data))
	}
	first := all.Data[0]
	id, _ := strconv.ParseInt(first.ID, 10, 64)
	if first.Cmd != "/media/"+first.ID+".mpg" || first.CategoryID != ids["Drama"] || first.Year != "2024" ||
		first.RatingIMDB != "7.4" || first.Added != "2026-12-20 00:00:00" || first.Censored != "0" || first.Time != "" ||
		!strings.HasPrefix(first.ScreenshotURI, "http://example.com/artwork/poster/"+first.ID+"/") {
		t.Errorf("first %+v", first)
	}
	if second := all.Data[1]; second.CategoryID != ids["Comedy"] || second.Year != "" || second.RatingIMDB != "" ||
		second.ScreenshotURI != "" {
		t.Errorf("second %+v", second)
	}
	page2 := h.titles("type=vod&action=get_ordered_list&category=*&p=2", macA, token)
	if page2.TotalItems != 16 || itemNames(page2.Data) != "Distant Shore, Broken Meadow" {
		t.Errorf("second page: %d %q", page2.TotalItems, itemNames(page2.Data))
	}
	if past := h.titles("type=vod&action=get_ordered_list&category=*&p=3", macA, token); past.TotalItems != 16 || len(past.Data) != 0 {
		t.Errorf("third page %+v", past)
	}

	byName := h.titles("type=vod&action=get_ordered_list&category="+ids["Comedy"]+"&sortby=name", macA, token)
	if byName.TotalItems != 8 || !strings.HasPrefix(itemNames(byName.Data), "Broken Meadow, Golden Bridge, Hollow Summit") {
		t.Errorf("comedy by name: %d %q", byName.TotalItems, itemNames(byName.Data))
	}
	found := h.titles("type=vod&action=get_ordered_list&category=*&search=FIELD", macA, token)
	if found.TotalItems != 1 || itemNames(found.Data) != "The Long Field" {
		t.Errorf("search: %+v", found)
	}
	for _, q := range []string{"category=*&fav=1", "category=999999", "category=x", "movie_id=" + first.ID} {
		if got := h.titles("type=vod&action=get_ordered_list&"+q, macA, token); got.TotalItems != 0 || len(got.Data) != 0 {
			t.Errorf("%s: %+v", q, got)
		}
	}
	// A series' category holds no films.
	sids, _ := h.categories("series", macA, token)
	if got := h.titles("type=vod&action=get_ordered_list&category="+sids["Documentary"], macA, token); got.TotalItems != 0 {
		t.Errorf("films in a series' category: %+v", got)
	}

	// Once a device has opened a film, its details come with it.
	if err := h.store.SetMovieDetails(context.Background(), id, store.TitleDetails{
		TitleSummary: store.TitleSummary{Plot: "A long field.", Cast: "A. Actor", Director: "D. Director", Genre: "Drama"},
		Duration:     6010, AgeRating: "12", Year: 2023, Rating: 81}, "", h.now); err != nil {
		t.Fatal(err)
	}
	got := h.titles("type=vod&action=get_ordered_list&category=*", macA, token).Data[0]
	if got.Description != "A long field." || got.Actors != "A. Actor" || got.Director != "D. Director" ||
		got.GenresStr != "Drama" || got.Time != "100" || got.Age != "12" || got.Year != "2023" || got.RatingIMDB != "8.1" {
		t.Errorf("with details %+v", got)
	}
}

func TestSeriesSeasons(t *testing.T) {
	h := newHarness(t)
	panel := newLibraryPanel(t)
	h.withLibrary(panel)
	h.approve(macA)
	token := h.handshake(macA)

	ids, titles := h.categories("series", macA, token)
	if !reflect.DeepEqual(titles, []string{"All", "Documentary"}) {
		t.Fatalf("categories %q", titles)
	}
	list := h.titles("type=series&action=get_ordered_list&category="+ids["Documentary"]+"&p=1", macA, token)
	if list.TotalItems != 2 || itemNames(list.Data) != "Coastline, Harbour Lights" {
		t.Fatalf("series %+v", list)
	}
	coastline := list.Data[0]
	if coastline.Description != "Waves." || coastline.Time != "40" || coastline.Cmd != "/media/"+coastline.ID+".mpg" ||
		coastline.CategoryID != ids["Documentary"] || coastline.ScreenshotURI == "" {
		t.Errorf("coastline %+v", coastline)
	}
	if panel.called("get_series") != 1 || panel.called("get_series_info") != 0 {
		t.Errorf("listing read a series' details: %v", panel.calls)
	}

	var seasons titlePage[seasonItem]
	h.js(h.call("type=series&action=get_ordered_list&movie_id="+coastline.ID+"&p=1", macA, token), &seasons)
	if panel.called("get_series_info") != 1 || seasons.TotalItems != 2 || len(seasons.Data) != 2 {
		t.Fatalf("seasons %+v, calls %v", seasons, panel.calls)
	}
	s1, s2 := seasons.Data[0], seasons.Data[1]
	id, _ := strconv.ParseInt(coastline.ID, 10, 64)
	if s1.ID != strconv.FormatInt(id*1000+1, 10) || s1.Name != "The First Season" || s1.Cmd != "/media/"+coastline.ID+"_1.mpg" ||
		!reflect.DeepEqual(s1.Series, []int{1, 2}) || s1.Description != "Waves, closer." || s1.ScreenshotURI != coastline.ScreenshotURI {
		t.Errorf("season 1 %+v", s1)
	}
	if s2.Name != "Season 2" || s2.Cmd != "/media/"+coastline.ID+"_2.mpg" || !reflect.DeepEqual(s2.Series, []int{1}) {
		t.Errorf("season 2 %+v", s2)
	}
	// Opened again, the series is not read again.
	h.js(h.call("type=series&action=get_ordered_list&movie_id="+coastline.ID, macA, token), &seasons)
	if panel.called("get_series_info") != 1 || len(seasons.Data) != 2 {
		t.Errorf("again: %+v, calls %v", seasons, panel.calls)
	}

	// A season whose episodes share a number goes by their places.
	h.js(h.call("type=series&action=get_ordered_list&movie_id="+list.Data[1].ID, macA, token), &seasons)
	if len(seasons.Data) != 1 || !reflect.DeepEqual(seasons.Data[0].Series, []int{1, 2}) || seasons.Data[0].Name != "Season 1" {
		t.Errorf("harbour lights %+v", seasons)
	}
	for _, q := range []string{"movie_id=999999", "movie_id=x", "movie_id=" + coastline.ID + "&p=2"} {
		h.js(h.call("type=series&action=get_ordered_list&"+q, macA, token), &seasons)
		if len(seasons.Data) != 0 {
			t.Errorf("%s: %+v", q, seasons)
		}
	}
	// A film is no series, and asking for its seasons reads nothing of it.
	films := h.titles("type=vod&action=get_ordered_list&category=*", macA, token)
	h.js(h.call("type=series&action=get_ordered_list&movie_id="+films.Data[0].ID, macA, token), &seasons)
	if seasons.TotalItems != 0 || len(seasons.Data) != 0 || panel.called("get_vod_info") != 0 {
		t.Errorf("a film's seasons %+v, calls %v", seasons, panel.calls)
	}
}

func TestTitleLinks(t *testing.T) {
	h := newHarness(t)
	panel := newLibraryPanel(t)
	front := h.withLibrary(panel)
	h.approve(macA)
	h.approve(macB)
	token := h.handshake(macA)

	films := h.titles("type=vod&action=get_ordered_list&category=*", macA, token)
	film := films.Data[0]
	got := h.link("vod", film.Cmd, "", macA, token)
	if got.Error != "" || !strings.HasPrefix(got.Cmd, "http://example.com/vod/") || !strings.HasSuffix(got.Cmd, ".mkv") ||
		strings.Contains(got.Cmd, " ") {
		t.Fatalf("film link %+v", got)
	}
	// The file plays as it is, from the part the player asks for.
	if status, body := fetch(t, front, got.Cmd); status != http.StatusPartialContent || body != string(file("film 5001")[100:200]) {
		t.Errorf("film: %d %q", status, body)
	}
	// The cmd's last word counts, and a series parameter of 0 changes
	// nothing for a film.
	if got := h.link("vod", "ffmpeg http://example.com"+film.Cmd, "0", macA, token); got.Error != "" {
		t.Errorf("film with a prefix %+v", got)
	}

	series := h.titles("type=series&action=get_ordered_list&category=*", macA, token)
	coastline := series.Data[0]
	var seasons titlePage[seasonItem]
	h.js(h.call("type=series&action=get_ordered_list&movie_id="+coastline.ID, macA, token), &seasons)
	for _, c := range []struct {
		module, cmd, series, file string
	}{
		{"series", seasons.Data[0].Cmd, "2", "episode 9002"},
		{"vod", seasons.Data[0].Cmd, "1", "episode 9001"},
		{"series", seasons.Data[1].Cmd, "1", "episode 9003"},
		{"series", seasons.Data[0].Cmd, "", "episode 9001"}, // a season's first
	} {
		got := h.link(c.module, c.cmd, c.series, macA, token)
		if got.Error != "" {
			t.Errorf("%+v: %+v", c, got)
			continue
		}
		if status, body := fetch(t, front, got.Cmd); status != http.StatusPartialContent || body != string(file(c.file)[100:200]) {
			t.Errorf("%+v: %d %q", c, status, body)
		}
	}

	for _, c := range []struct{ cmd, series string }{
		{"", ""},
		{"garbage", ""},
		{"/media/0.mpg", ""},
		{"/media/999999.mpg", ""},
		{"/media/+" + film.ID + ".mpg", ""},
		{coastline.Cmd, "1"},                          // a series, not a season
		{"/media/" + film.ID + "_1.mpg", "1"},         // a film as a series
		{"/media/" + coastline.ID + "_9.mpg", "1"},    // no such season
		{"/media/" + coastline.ID + "_1.mpg", "7"},    // no such episode
		{"/media/" + coastline.ID + "_1000.mpg", "1"}, // not a season's number
		{"/media/file_" + film.ID + ".mpg", ""},       // no files apart
		{"ffrt http://example.com/ch/" + film.ID, ""}, // a channel's form
		{"/media/" + film.ID + ".mp4", ""},            // not the form of a cmd
		{"http://example.com/vod/" + film.ID + ".mkv", ""},
	} {
		if got := h.link("vod", c.cmd, c.series, macA, token); got.Cmd != "" || got.Error != "nothing_to_play" {
			t.Errorf("%q series %q: %+v", c.cmd, c.series, got)
		}
	}

	// While another box plays from the source, which allows one
	// connection, there is no room for this one.
	panel.mu.Lock()
	panel.hold = make(chan struct{})
	panel.mu.Unlock()
	other := h.link("vod", films.Data[1].Cmd, "", macB, h.handshake(macB))
	u, _ := url.Parse(other.Cmd)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if resp, err := http.Get(front.URL + u.Path); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
	}()
	playing := func() bool {
		for _, s := range h.hub.Streams() {
			if s.Playing {
				return true
			}
		}
		return false
	}
	for deadline := time.Now().Add(5 * time.Second); !playing() && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	if got := h.link("vod", film.Cmd, "", macA, token); got.Cmd != "" || got.Error != "limit" {
		t.Errorf("at the limit: %+v", got)
	}
	close(panel.hold)
	<-done
}

func TestMediaFromCmd(t *testing.T) {
	for cmd, want := range map[string][2]int64{
		"/media/42.mpg":                         {42, -1},
		"/media/42_3.mpg":                       {42, 3},
		"/media/42_0.mpg":                       {42, 0},
		"auto /media/42.mpg":                    {42, -1},
		"ffmpeg http://host:8080/media/7_1.mpg": {7, 1},
		"/media/0.mpg":                          {0, 0},
		"/media/-1.mpg":                         {0, 0},
		"/media/42_-1.mpg":                      {0, 0},
		"/media/42_.mpg":                        {0, 0},
		"/media/42_1_2.mpg":                     {0, 0},
		"/media/42":                             {0, 0},
		"/media/a.mpg":                          {0, 0},
		"/ch/42":                                {0, 0},
		"/media/99999999999999999999.mpg":       {0, 0},
	} {
		id, season, ok := mediaFromCmd(cmd)
		if (want[0] == 0 && ok) || (want[0] != 0 && (!ok || id != want[0] || int64(season) != want[1])) {
			t.Errorf("mediaFromCmd(%q) = %d, %d, %v; want %v", cmd, id, season, ok, want)
		}
	}
}

func TestNumberedSeasons(t *testing.T) {
	eps := []store.Episode{{ID: 1, Season: 0, Number: 1}, {ID: 2, Season: 1, Number: 2}, {ID: 3, Season: 1, Number: 5},
		{ID: 4, Season: 2, Number: 0}, {ID: 5, Season: 2, Number: 1}, {ID: 6, Season: 3, Number: 1}, {ID: 7, Season: 3, Number: 1}}
	got := numberedSeasons(eps)
	want := [][]int{{1}, {2, 5}, {1, 2}, {1, 2}}
	if len(got) != len(want) {
		t.Fatalf("seasons %+v", got)
	}
	for i, s := range got {
		if !reflect.DeepEqual(s.Numbers, want[i]) || len(s.Episodes) != len(want[i]) {
			t.Errorf("season %d: %+v, want %v", s.Number, s, want[i])
		}
	}
	if numberedSeasons(nil) != nil {
		t.Error("no episodes, but seasons")
	}
	if seasonName(0, "") != "Specials" || seasonName(4, "") != "Season 4" || seasonName(4, "Four") != "Four" {
		t.Error("season names")
	}
	if rating10(74) != "7.4" || rating10(100) != "10.0" || rating10(5) != "0.5" || rating10(0) != "" || number(0) != "" {
		t.Error("numbers")
	}
}

func FuzzMediaFromCmd(f *testing.F) {
	for _, cmd := range []string{"/media/42.mpg", "/media/42_3.mpg", "ffmpeg http://host/media/7_1.mpg", "auto /media/1.mpg",
		"/media/file_1.mpg", "http://[::1]/media/1.mpg", "/media/%31.mpg"} {
		f.Add(cmd)
	}
	f.Fuzz(func(t *testing.T, cmd string) {
		id, season, ok := mediaFromCmd(cmd)
		if !ok {
			return
		}
		if id <= 0 || season < -1 || season >= seasonsPerSeries {
			t.Fatalf("mediaFromCmd(%q) = %d, %d", cmd, id, season)
		}
		// What it names, its own cmd names too.
		again := titleCmd(id)
		if season >= 0 {
			again = seasonCmd(id, season)
		}
		if id2, season2, ok := mediaFromCmd(again); !ok || id2 != id || season2 != season {
			t.Fatalf("%q names %d, %d; %q names %d, %d", cmd, id, season, again, id2, season2)
		}
	})
}

// Once the account has a PIN, Stalker clients do not see locked shelves and
// titles, which they could not ask for it for, nor get their links (D85).
func TestLockedTitlesLeftOut(t *testing.T) {
	h := newHarness(t)
	panel := newLibraryPanel(t)
	h.withLibrary(panel)
	h.approve(macA)
	token := h.handshake(macA)
	ctx := context.Background()
	ids, _ := h.categories("vod", macA, token)
	all := h.titles("type=vod&action=get_ordered_list&category=*&sortby=name&p=1", macA, token)
	byName := map[string]vodItem{}
	for p := 1; p <= 2; p++ {
		page := h.titles("type=vod&action=get_ordered_list&category=*&sortby=name&p="+strconv.Itoa(p), macA, token)
		for _, it := range page.Data {
			byName[it.Name] = it
		}
	}
	long := byName["The Long Field"]
	id, _ := strconv.ParseInt(long.ID, 10, 64)
	if _, err := h.store.SetTitlesLocked(ctx, []int64{id}, true); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetShelfLocked(ctx, store.KindMovie, "name:Comedy", true); err != nil {
		t.Fatal(err)
	}
	// Without a PIN, locks do nothing.
	if got := h.titles("type=vod&action=get_ordered_list&category=*&sortby=name&p=1", macA, token); got.TotalItems != all.TotalItems {
		t.Errorf("without a PIN: %d of %d", got.TotalItems, all.TotalItems)
	}

	if err := h.store.SetAccountPIN(ctx, 1, "$argon2id$made-up"); err != nil {
		t.Fatal(err)
	}
	if _, titles := h.categories("vod", macA, token); !reflect.DeepEqual(titles, []string{"All", "Drama"}) {
		t.Errorf("categories %q", titles)
	}
	got := h.titles("type=vod&action=get_ordered_list&category=*&sortby=name&p=1", macA, token)
	for _, it := range got.Data {
		if it.Name == "The Long Field" || it.CategoryID == ids["Comedy"] {
			t.Errorf("a locked title listed: %+v", it)
		}
	}
	if got.TotalItems >= all.TotalItems-1 {
		t.Errorf("%d of %d listed", got.TotalItems, all.TotalItems)
	}
	if drama := h.titles("type=vod&action=get_ordered_list&category="+ids["Drama"]+"&p=1", macA, token); strings.Contains(itemNames(drama.Data), "The Long Field") {
		t.Errorf("Drama lists a locked title: %q", itemNames(drama.Data))
	}
	if l := h.link("vod", long.Cmd, "", macA, token); l.Cmd != "" || l.Error != "nothing_to_play" {
		t.Errorf("a locked film's link: %+v", l)
	}
}
