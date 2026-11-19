// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// libraryPanel is a fictional Xtream panel with films and series, and their
// files.
type libraryPanel struct {
	mu      sync.Mutex
	replies map[string]string
	hold    chan struct{}     // if set, API calls wait for it
	files   map[string][]byte // files it serves, with ranges, by path; others answer FILE and the path
}

const panelPassword = "pw/films"

func newLibraryPanel(t *testing.T) (*libraryPanel, *httptest.Server) {
	t.Helper()
	p := &libraryPanel{replies: map[string]string{
		"get_vod_categories": `[{"category_id":"21","category_name":"Drama"},{"category_id":"22","category_name":"Comedy"}]`,
		"get_vod_streams": `[
			{"stream_id":5001,"name":"The Long Field","category_id":"21","container_extension":"mkv","added":"1767225600",
			 "stream_icon":"http://img.example/5001.jpg","rating":"7.4","year":"2024"},
			{"stream_id":5002,"name":"Quiet Harbour","category_id":"22","added":"1767312000"},
			{"stream_id":5003,"name":"Another Field","category_id":"21","added":"1767139200"}]`,
		"get_series_categories": `[{"category_id":"31","category_name":"Documentary"}]`,
		"get_series":            `[{"series_id":7001,"name":"Coastline","category_id":"31","plot":"Waves.","last_modified":"1767225600"}]`,
		"get_vod_info": `{"info":{"plot":"A long field.","genre":"Drama","duration_secs":6000,"cover_big":"http://img.example/5001-big.jpg",
			"backdrop_path":["http://img.example/5001-wide.jpg"],"country":"Nowhere"},
			"movie_data":{"stream_id":5001,"container_extension":"mkv"}}`,
		"get_series_info": `{"info":{"name":"Coastline","plot":"Waves, closer."},
			"seasons":[{"season_number":1,"name":"The First Season"}],
			"episodes":{"1":[{"id":"9001","episode_num":1,"title":"Rocks","container_extension":"mkv",
				"info":{"duration_secs":2400,"movie_image":"http://img.example/9001.jpg"}},
				{"id":"9002","episode_num":2,"title":"Sand","container_extension":"mkv","info":{"duration_secs":2400}}],
			"2":[{"id":"9003","episode_num":1,"title":"Tides","container_extension":"mp4"}]}}`,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/movie/viewer/") || strings.HasPrefix(r.URL.Path, "/series/viewer/") {
			p.mu.Lock()
			file, ok := p.files[r.URL.EscapedPath()]
			p.mu.Unlock()
			w.Header().Set("Content-Type", "video/x-matroska")
			if ok {
				http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(file))
				return
			}
			io.WriteString(w, "FILE "+r.URL.EscapedPath())
			return
		}
		q := r.URL.Query()
		p.mu.Lock()
		reply, ok := p.replies[q.Get("action")]
		hold := p.hold
		p.mu.Unlock()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if q.Get("username") != "viewer" || q.Get("password") != panelPassword || !ok {
			http.NotFound(w, r)
			return
		}
		if reply == "503" {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return p, srv
}

// withLibrary gives the harness an Xtream source with films and series, a
// library that reads them, and the hub's links for them. It returns a
// token of an approved box.
func (h *harness) withLibrary(srv *httptest.Server) string {
	h.t.Helper()
	ctx := context.Background()
	src, err := h.store.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeXtream, URL: srv.URL,
		Username: "viewer", Password: panelPassword})
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
	hb := hub.New(h.store, log, "test", map[int64]hub.Source{src: {Name: "films"}})
	h.api.hub = hb
	mux := http.NewServeMux()
	mux.Handle(Prefix, h.api)
	mux.Handle("GET /vod/", hb)
	h.front.Config.Handler = mux
	h.approve(macA)
	return h.session(macA).Token
}

// shelfID returns the ID of the shelf with the given name.
func shelfID(t *testing.T, r libraryResponse, name string) string {
	t.Helper()
	for _, s := range r.Shelves {
		if s.Name == name {
			return s.ID
		}
	}
	t.Fatalf("no shelf %q in %+v", name, r.Shelves)
	return ""
}

func titleNames(rows []titleRow) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return strings.Join(out, ", ")
}

func TestLibraryShelves(t *testing.T) {
	h := newHarness(t)
	_, srv := newLibraryPanel(t)
	token := h.withLibrary(srv)

	var movies libraryResponse
	if code := h.call("GET", Prefix+"movies", token, "", &movies); code != http.StatusOK || movies.State != libraryReady {
		t.Fatalf("movies: %d %+v", code, movies)
	}
	if len(movies.Shelves) != 3 || movies.Shelves[0].ID != shelfRecent || movies.Shelves[0].Count != 3 ||
		movies.Shelves[1].Name != "Drama" || movies.Shelves[1].Count != 2 || movies.Shelves[2].Name != "Comedy" {
		t.Errorf("shelves %+v", movies.Shelves)
	}
	var recent shelfResponse
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	if recent.Name != "Recently Added" || recent.Total != 3 || titleNames(recent.Titles) != "Quiet Harbour, The Long Field, Another Field" {
		t.Errorf("recent %+v", recent)
	}
	if r := recent.Titles[1]; r.Year != 2024 || r.Rating != 74 || !strings.HasPrefix(r.Poster, "/artwork/poster/") {
		t.Errorf("row %+v", r)
	}
	drama := shelfID(t, movies, "Drama")
	var shelf shelfResponse
	h.call("GET", Prefix+"movies/shelves/"+drama+"?order=name", token, "", &shelf)
	if shelf.Name != "Drama" || titleNames(shelf.Titles) != "Another Field, The Long Field" {
		t.Errorf("drama by name %+v", shelf)
	}
	h.call("GET", Prefix+"movies/shelves/"+drama+"?from=1&count=1", token, "", &shelf)
	if shelf.Total != 2 || shelf.From != 1 || titleNames(shelf.Titles) != "Another Field" {
		t.Errorf("drama's second %+v", shelf)
	}
	for path, want := range map[string]int{
		"movies/shelves/" + drama + "?order=size":       http.StatusBadRequest,
		"movies/shelves/" + drama + "?count=0":          http.StatusBadRequest,
		"movies/shelves/" + drama + "?from=-1":          http.StatusBadRequest,
		"movies/shelves/999999":                         http.StatusNotFound,
		"movies/shelves/x":                              http.StatusNotFound,
		"series/shelves/" + drama:                       http.StatusNotFound, // a film shelf as a series one
		"movies/shelves/" + drama + "?count=1&from=400": http.StatusOK,
	} {
		if code := h.call("GET", Prefix+path, token, "", nil); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}

	var series libraryResponse
	h.call("GET", Prefix+"series", token, "", &series)
	if series.State != libraryReady || len(series.Shelves) != 2 || series.Shelves[1].Name != "Documentary" {
		t.Errorf("series %+v", series)
	}

	var found searchResponse
	h.call("GET", Prefix+"search?q=field", token, "", &found)
	if titleNames(found.Movies) != "Another Field, The Long Field" || len(found.Series) != 0 {
		t.Errorf("search %+v", found)
	}
	h.call("GET", Prefix+"search?q=COAST", token, "", &found)
	if titleNames(found.Series) != "Coastline" {
		t.Errorf("search %+v", found)
	}
}

func TestLibraryStates(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	var r libraryResponse
	if h.call("GET", Prefix+"movies", token, "", &r); r.State != libraryNone || r.Shelves == nil {
		t.Errorf("without a library: %+v", r)
	}

	p, srv := newLibraryPanel(t)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.replies["get_series"] = "503"
	p.mu.Unlock()
	token = h.withLibrary(srv)
	h.api.libraryWait = 50 * time.Millisecond
	for range 2 {
		if r = (libraryResponse{}); h.call("GET", Prefix+"movies", token, "", &r) != http.StatusOK || r.State != libraryLoading {
			t.Errorf("while reading: %+v", r)
		}
	}
	close(hold)
	h.api.libraryWait = 5 * time.Second
	if r = (libraryResponse{}); h.call("GET", Prefix+"movies", token, "", &r) != http.StatusOK || r.State != libraryReady {
		t.Errorf("after reading: %+v", r)
	}
	if h.call("GET", Prefix+"series", token, "", &r); r.State != libraryFailed || !strings.Contains(r.Error, "503") {
		t.Errorf("a failed read: %+v", r)
	}
}

func TestOpenAndPlayFilm(t *testing.T) {
	h := newHarness(t)
	_, srv := newLibraryPanel(t)
	token := h.withLibrary(srv)
	var movies libraryResponse
	h.call("GET", Prefix+"movies", token, "", &movies)
	var recent shelfResponse
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	film := recent.Titles[1] // The Long Field
	path := fmt.Sprintf("%smovies/%d", Prefix, film.ID)

	var t1 titleResponse
	if code := h.call("GET", path, token, "", &t1); code != http.StatusOK {
		t.Fatalf("open: %d", code)
	}
	if t1.Kind != store.KindMovie || t1.Name != "The Long Field" || !t1.Details || t1.Plot != "A long field." ||
		t1.Genre != "Drama" || t1.Country != "Nowhere" || t1.Runtime != 100 || t1.Shelf != "Drama" ||
		!strings.HasPrefix(t1.Poster, fmt.Sprintf("/artwork/poster/%d/", film.ID)) || t1.Poster == film.Poster ||
		!strings.HasPrefix(t1.Backdrop, "/artwork/backdrop/") || t1.Progress != nil {
		t.Errorf("film %+v", t1)
	}
	if code := h.call("GET", fmt.Sprintf("%sseries/%d", Prefix, film.ID), token, "", nil); code != http.StatusNotFound {
		t.Errorf("a film as a series: %d", code)
	}

	var link itemPlayResponse
	if code := h.call("POST", path+"/play", token, "", &link); code != http.StatusOK {
		t.Fatalf("play: %d", code)
	}
	if link.Solution != "ffmpeg" || !strings.Contains(link.URL, "/vod/") || !strings.HasSuffix(link.URL, ".mkv") ||
		strings.Contains(link.URL, panelPassword) || link.Seek != "player" || link.Start != "" || link.Duration != 0 ||
		link.Audio != nil {
		t.Errorf("link %+v", link)
	}
	resp, err := http.Get(link.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(got) != "FILE /movie/viewer/pw%2Ffilms/5001.mkv" {
		t.Errorf("played %q", got)
	}

	// Part way through: it is on the continue shelf, and its row shows it.
	keep := func(body string) progress {
		t.Helper()
		var kept progress
		if code := h.call("PUT", path+"/progress", token, body, &kept); code != http.StatusOK {
			t.Errorf("progress %s: %d", body, code)
		}
		return kept
	}
	if kept := keep(`{"position":600,"duration":6000}`); kept.Position != 600 || kept.Finished {
		t.Errorf("progress %+v", kept)
	}
	h.call("GET", Prefix+"movies", token, "", &movies)
	if movies.Shelves[0].ID != shelfContinue || movies.Shelves[0].Count != 1 {
		t.Errorf("shelves %+v", movies.Shelves)
	}
	var cont shelfResponse
	h.call("GET", Prefix+"movies/shelves/continue", token, "", &cont)
	if len(cont.Titles) != 1 || cont.Titles[0].Progress != 10 || cont.Titles[0].Name != "The Long Field" {
		t.Errorf("continue %+v", cont)
	}
	h.call("GET", path, token, "", &t1)
	if t1.Progress == nil || t1.Progress.Position != 600 || t1.Progress.Duration != 6000 {
		t.Errorf("opened again %+v", t1.Progress)
	}

	// Near the end, it is finished.
	if kept := keep(`{"position":5800,"duration":6000}`); !kept.Finished || kept.Position != 0 {
		t.Errorf("near the end %+v", kept)
	}
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	if !recent.Titles[1].Watched || recent.Titles[1].Progress != 0 {
		t.Errorf("row %+v", recent.Titles[1])
	}
	h.call("GET", Prefix+"movies", token, "", &movies)
	if movies.Shelves[0].ID == shelfContinue {
		t.Errorf("shelves %+v", movies.Shelves)
	}
	// At the very start, it is not started.
	if kept := keep(`{"position":12,"duration":6000}`); kept.Position != 0 || kept.Finished {
		t.Errorf("at the start %+v", kept)
	}
	for body, want := range map[string]int{`{"position":-1}`: 400, `{"position":"x"}`: 400, `{"duration":99999999}`: 400} {
		if code := h.call("PUT", path+"/progress", token, body, nil); code != want {
			t.Errorf("%s: %d", body, code)
		}
	}
	for _, p := range []string{"movies/999999/play", "episodes/999999/play"} {
		if code, e := h.errorCode("POST", Prefix+p, token); code != http.StatusNotFound || e != errNotFound {
			t.Errorf("%s: %d %s", p, code, e)
		}
	}
	if code := h.call("PUT", fmt.Sprintf("%sepisodes/%d/progress", Prefix, film.ID), token, `{"position":100}`, nil); code != http.StatusNotFound {
		t.Errorf("a film as an episode: %d", code)
	}
}

func TestSeriesAndWhatPlaysNext(t *testing.T) {
	h := newHarness(t)
	_, srv := newLibraryPanel(t)
	token := h.withLibrary(srv)
	var series libraryResponse
	h.call("GET", Prefix+"series", token, "", &series)
	var shelf shelfResponse
	h.call("GET", Prefix+"series/shelves/"+shelfID(t, series, "Documentary"), token, "", &shelf)
	path := fmt.Sprintf("%sseries/%d", Prefix, shelf.Titles[0].ID)

	var s titleResponse
	if code := h.call("GET", path, token, "", &s); code != http.StatusOK {
		t.Fatalf("open: %d", code)
	}
	if s.Kind != store.KindSeries || s.Plot != "Waves, closer." || len(s.Seasons) != 2 || s.Seasons[0].Name != "The First Season" ||
		s.Seasons[1].Name != "Season 2" || len(s.Seasons[0].Episodes) != 2 || s.Seasons[0].Episodes[0].Name != "Rocks" ||
		s.Seasons[0].Episodes[0].Duration != 2400 || !strings.HasPrefix(s.Seasons[0].Episodes[0].Picture, "/artwork/still/") {
		t.Fatalf("series %+v", s)
	}
	rocks, sand, tides := s.Seasons[0].Episodes[0].ID, s.Seasons[0].Episodes[1].ID, s.Seasons[1].Episodes[0].ID
	if s.Next == nil || s.Next.Episode != rocks || s.Next.Position != 0 {
		t.Errorf("next %+v", s.Next)
	}

	var link playResponse
	if h.call("POST", fmt.Sprintf("%sepisodes/%d/play", Prefix, tides), token, "", &link); !strings.HasSuffix(link.URL, ".mp4") {
		t.Errorf("link %+v", link)
	}
	step := func(episode int64, body string, wantNext int64, wantAt int) {
		t.Helper()
		h.now = h.now.Add(time.Minute)
		if code := h.call("PUT", fmt.Sprintf("%sepisodes/%d/progress", Prefix, episode), token, body, nil); code != http.StatusOK {
			t.Fatalf("progress: %d", code)
		}
		h.call("GET", path, token, "", &s)
		if s.Next == nil || s.Next.Episode != wantNext || s.Next.Position != wantAt {
			t.Errorf("after %s: next %+v", body, s.Next)
		}
	}
	step(rocks, `{"finished":true}`, sand, 0)
	var cont shelfResponse
	h.call("GET", Prefix+"series/shelves/continue", token, "", &cont)
	if len(cont.Titles) != 1 || cont.Titles[0].Episode != "S1 E2" || cont.Titles[0].Progress != 0 {
		t.Errorf("continue after Rocks %+v", cont) // the episode that plays next
	}
	step(sand, `{"position":10,"duration":2400}`, sand, 0) // barely started: Sand again, from the start
	step(sand, `{"position":300,"duration":2400}`, sand, 300)
	if p := s.Seasons[0].Episodes[1].Progress; p == nil || p.Position != 300 {
		t.Errorf("Sand's progress %+v", p)
	}
	cont = shelfResponse{}
	h.call("GET", Prefix+"series/shelves/continue", token, "", &cont)
	if len(cont.Titles) != 1 || cont.Titles[0].Episode != "S1 E2" || cont.Titles[0].Progress != 12 {
		t.Errorf("continue %+v", cont)
	}
	step(sand, `{"position":2350,"duration":2400}`, tides, 0)
	step(tides, `{"finished":true}`, rocks, 0) // all of it watched: from the start again
}

// TestPlayConverted plays a film whose sound the box cannot play: the hub
// reads what its file holds, and the page gets a link to the stream that
// ffmpeg makes of it, which it seeks in by asking for it from a time.
func TestPlayConverted(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg in PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe in PATH")
	}
	path := filepath.Join(t.TempDir(), "film.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=6",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=6",
		"-c:v", "mpeg2video", "-g", "25", "-c:a", "flac", "-ac", "2", "-metadata:s:a:0", "language=eng", path).CombinedOutput()
	if err != nil {
		t.Fatalf("making a film: %v: %s", err, out)
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)
	p, srv := newLibraryPanel(t)
	p.files = map[string][]byte{"/movie/viewer/pw%2Ffilms/5001.mkv": file}
	token := h.withLibrary(srv)
	h.api.hub.UseFFmpeg(ffmpeg, hub.RepairSet)
	h.api.hub.UseFFprobe(ffprobe)
	var movies libraryResponse
	h.call("GET", Prefix+"movies", token, "", &movies)
	var recent shelfResponse
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	if len(recent.Titles) < 2 || recent.Titles[1].Name != "The Long Field" {
		t.Fatalf("recent %+v", recent)
	}

	// A page that cannot seek by asking for a stream from a time gets the
	// file as it is.
	var link itemPlayResponse
	play := fmt.Sprintf("%smovies/%d/play", Prefix, recent.Titles[1].ID)
	if code := h.call("POST", play, token, "", &link); code != http.StatusOK || link.Seek != "player" ||
		!strings.HasSuffix(link.URL, ".mkv") {
		t.Fatalf("play without restart: %d, %+v", code, link)
	}
	if code := h.call("POST", play, token, `{"restart": "yes"}`, nil); code != http.StatusBadRequest {
		t.Errorf("play with a body that makes no sense: %d", code)
	}
	link = itemPlayResponse{}
	if code := h.call("POST", play, token, `{"restart": true}`, &link); code != http.StatusOK {
		t.Fatalf("play: %d", code)
	}
	if link.Seek != "restart" || !strings.HasSuffix(link.URL, ".ts") || link.Start != strings.TrimSuffix(link.URL, ".ts")+"/start" ||
		link.Duration != 6 || len(link.Audio) != 1 || link.Audio[0] != (audioTrack{Language: "eng", Codec: "flac", Channels: 2, Converted: true}) {
		t.Fatalf("link %+v", link)
	}
	resp, err := http.Get(link.Start + "?t=3.5")
	if err != nil {
		t.Fatal(err)
	}
	var start struct{ T float64 }
	err = json.NewDecoder(resp.Body).Decode(&start)
	resp.Body.Close()
	if err != nil || start.T != 3 {
		t.Errorf("from 3.5 s, the stream starts at %v, %v; want the picture at 3 s", start.T, err)
	}
	resp, err = http.Get(link.URL + "?t=3")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "video/mp2t" || len(got) < 188*10 || got[0] != 0x47 {
		t.Errorf("stream: status %s, %d bytes", resp.Status, len(got))
	}

	// A box whose playback check found that it plays FLAC gets the file
	// as it is (D70).
	if code := h.call("PUT", Prefix+"check", token, results("", map[string]string{"base": "yes", "flac": "yes"}), nil); code != http.StatusOK {
		t.Fatalf("PUT check: %d", code)
	}
	link = itemPlayResponse{}
	if code := h.call("POST", play, token, `{"restart": true}`, &link); code != http.StatusOK || link.Seek != "player" ||
		!strings.HasSuffix(link.URL, ".mkv") || link.Duration != 6 {
		t.Errorf("play after the check: %d, %+v", code, link)
	}
}

// TestPlayTranscoded plays a film whose video the box cannot play, which
// the server's ffmpeg transcodes where the administrator lets it (D73).
// While one box's transcode runs, at one at a time, another box is told
// that the server is busy.
func TestPlayTranscoded(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg in PATH")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("no ffprobe in PATH")
	}
	// H.264 at 10 bits, which the MAG322 cannot play; of noise, so that
	// its stream is too big to wait unread in the connection, and its
	// transcode goes on while the box holds it.
	path := filepath.Join(t.TempDir(), "film.mkv")
	out, err := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-nostdin",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=25:duration=20,noise=alls=80:allf=t",
		"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000:duration=20",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p10le", "-g", "50", "-c:a", "aac", "-ac", "2", path).CombinedOutput()
	if err != nil {
		t.Fatalf("making a film: %v: %s", err, out)
	}
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	h := newHarness(t)
	p, srv := newLibraryPanel(t)
	p.files = map[string][]byte{"/movie/viewer/pw%2Ffilms/5001.mkv": file}
	token := h.withLibrary(srv)
	h.api.hub.UseFFmpeg(ffmpeg, hub.RepairSet)
	h.api.hub.UseFFprobe(ffprobe)
	h.api.hub.UseEncoder(hub.Encoder{H264: true, ToneMap: true})
	h.approve(macB)
	tokenB := h.session(macB).Token
	var movies libraryResponse
	h.call("GET", Prefix+"movies", token, "", &movies)
	var recent shelfResponse
	h.call("GET", Prefix+"movies/shelves/recent", token, "", &recent)
	if len(recent.Titles) < 2 || recent.Titles[1].Name != "The Long Field" {
		t.Fatalf("recent %+v", recent)
	}
	play := fmt.Sprintf("%smovies/%d/play", Prefix, recent.Titles[1].ID)

	// Until the administrator lets it, the file goes as it is.
	var link itemPlayResponse
	if code := h.call("POST", play, token, `{"restart": true}`, &link); code != http.StatusOK || link.Seek != "player" ||
		!strings.HasSuffix(link.URL, ".mkv") {
		t.Fatalf("play with transcoding off: %d, %+v", code, link)
	}
	if err := h.store.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	link = itemPlayResponse{}
	if code := h.call("POST", play, token, `{"restart": true}`, &link); code != http.StatusOK {
		t.Fatalf("play: %d", code)
	}
	// Every sound track comes in the stream, as for one that ffmpeg
	// converts, so the page switches among them as it does there.
	if link.Seek != "restart" || link.AudioSwitch != "" || !strings.HasSuffix(link.URL, ".ts") || link.Duration != 20 ||
		len(link.Audio) != 1 || link.Audio[0] != (audioTrack{Codec: "aac", Channels: 2, Converted: true}) {
		t.Fatalf("link %+v", link)
	}
	// It starts at the very time asked.
	resp, err := http.Get(link.Start + "?t=2.5")
	if err != nil {
		t.Fatal(err)
	}
	var start struct{ T float64 }
	err = json.NewDecoder(resp.Body).Decode(&start)
	resp.Body.Close()
	if err != nil || start.T != 2.5 {
		t.Errorf("from 2.5 s, the stream starts at %v, %v", start.T, err)
	}
	stream, err := http.Get(link.URL + "?t=2.5")
	if err != nil {
		t.Fatal(err)
	}
	first := make([]byte, 188)
	if _, err := io.ReadFull(stream.Body, first); err != nil || stream.StatusCode != http.StatusOK || first[0] != 0x47 {
		t.Fatalf("stream: status %s, %v", stream.Status, err)
	}

	// While it plays, the other box cannot start one; the box itself can.
	var e struct{ Error string }
	if code := h.call("POST", play, tokenB, `{"restart": true}`, &e); code != http.StatusServiceUnavailable || e.Error != errBusy {
		t.Errorf("second box: status %d, error %q", code, e.Error)
	}
	if code := h.call("POST", play, token, `{"restart": true}`, &link); code != http.StatusOK || link.Seek != "restart" {
		t.Errorf("the same box again: status %d, %+v", code, link)
	}
	stream.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		code := h.call("POST", play, tokenB, `{"restart": true}`, &link)
		if code == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("second box once the first stopped: status %d", code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestItemResponseOfATranscode(t *testing.T) {
	pb := hub.Playback{Path: "/vod/tok.ts", Converted: true, Transcoded: true, StartPath: "/vod/tok/start", Duration: 5400.4,
		Tracks: []media.Track{{Index: 2, Codec: "dts", Channels: 6, Language: "eng", Convert: true}, {Index: 1, Codec: "ac3", Channels: 6}}}
	got := itemResponse("192.0.2.10:8080", pb)
	want := itemPlayResponse{URL: "http://192.0.2.10:8080/vod/tok.ts", Solution: "ffmpeg", Seek: "restart",
		Start: "http://192.0.2.10:8080/vod/tok/start", Duration: 5400, AudioSwitch: "restart",
		Audio: []audioTrack{{Language: "eng", Codec: "dts", Channels: 6, Converted: true}, {Codec: "ac3", Channels: 6}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("itemResponse = %+v\nwant %+v", got, want)
	}
	pb.Transcoded = false
	if got := itemResponse("192.0.2.10:8080", pb); got.AudioSwitch != "" || got.Seek != "restart" {
		t.Errorf("a stream through ffmpeg: %+v", got)
	}
	b, _ := json.Marshal(itemResponse("h", hub.Playback{Path: "/vod/tok.mkv"}))
	if strings.Contains(string(b), "audio_switch") {
		t.Errorf("a file as it is: %s", b)
	}
}
