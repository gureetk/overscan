// SPDX-License-Identifier: AGPL-3.0-or-later

package xtream

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// The login of the household's account in these tests, which is fictional.
const (
	username = "alex"
	password = "correct-horse-battery"
)

type harness struct {
	t     *testing.T
	store *store.Store
	hub   *hub.Hub
	out   *Output
	front *httptest.Server
	panel *panel
	now   time.Time
	logs  syncBuffer
	ids   map[string]int64 // entry IDs by channel name
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// stream is a fictional source's stream: MPEG-TS packets until the
// request ends.
func stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/mp2t")
	rc := http.NewResponseController(w)
	for {
		if _, err := w.Write(bytes.Repeat([]byte{0x47, 0x1f, 0xff, 0x10}, 47)); err != nil {
			return
		}
		_ = rc.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// newHarness puts fictional channels in the household's lineup, with a
// guide, and an Xtream source of films and series on a fictional panel;
// turns the outputs on, and gives the household's account a login that
// may use the Xtream API.
func newHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, now: time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC), ids: map[string]int64{}}
	log := slog.New(slog.NewTextHandler(&h.logs, nil))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	h.store = st

	up := httptest.NewServer(http.HandlerFunc(stream))
	t.Cleanup(up.Close)
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	channels := []store.CatalogChannel{
		{IdentityKey: "a", Name: "Alpha", Category: "News", XMLTVID: "alpha.example", Logo: "http://logos.example/a.png", StreamURL: up.URL + "/a.ts"},
		{IdentityKey: "b", Name: "Bravo", Category: "Sport", StreamURL: up.URL + "/b.ts",
			ArchiveDays: 2, Catchup: catchup.Playlist{Mode: catchup.ModeDefault, Source: up.URL + "/archive?from={utc}"}},
		{IdentityKey: "r", Name: "Radio Three", Category: "Radio", StreamURL: up.URL + "/r.ts", Radio: true},
		{IdentityKey: "x", Name: "Adults", Category: "Late", StreamURL: up.URL + "/x.ts", Adult: true},
		{IdentityKey: "p", Name: "Plain", StreamURL: up.URL + "/p.ts"},
	}
	if _, err := st.ApplyCatalog(ctx, srcID, channels, h.now); err != nil {
		t.Fatal(err)
	}
	lineup, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	channelIDs := map[string]int64{}
	for _, c := range lineup {
		h.ids[c.Name] = c.EntryID
		channelIDs[c.Name] = c.ChannelID
	}
	gw, err := st.BeginGuide(ctx, srcID)
	if err != nil {
		t.Fatal(err)
	}
	start := h.now.Truncate(time.Hour).Add(-30 * time.Hour)
	for i := 0; i < 40; i++ {
		at := start.Add(time.Duration(i) * time.Hour)
		for key, name := range map[string]string{"a": "Alpha", "b": "Bravo"} {
			if err := gw.Add(ctx, store.GuideProgramme{ChannelID: channelIDs[name], Start: at, Stop: at.Add(time.Hour),
				Title: fmt.Sprintf("%s & programme %d", key, i), Description: "A made-up programme."}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := gw.Commit(ctx, h.now, "test"); err != nil {
		t.Fatal(err)
	}

	h.panel = newPanel(t)
	films, err := st.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeXtream, URL: h.panel.URL,
		Username: "viewer", Password: panelPassword})
	if err != nil {
		t.Fatal(err)
	}
	lib := library.New(st, slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lib.Run(runCtx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	h.hub = hub.New(st, log, "test", map[int64]hub.Source{srcID: {Name: "main"}, films: {Name: "films"}})
	h.out = New(st, h.hub, admin.NewLogins(st), log)
	h.out.UseLibrary(lib)
	h.out.now = func() time.Time { return h.now }
	mux := routes.NewMux()
	h.out.Register(mux)
	mux.Handle("GET /live/", h.hub)
	mux.Handle("GET /vod/", h.hub)
	mux.Handle("GET /archive/", h.hub)
	h.front = httptest.NewServer(mux)
	t.Cleanup(h.front.Close)

	if err := st.SetOutputs(ctx, true); err != nil {
		t.Fatal(err)
	}
	hash, err := admin.HashPassword(ctx, password, admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountLogin(ctx, store.HouseholdAccount, username, hash); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{Xtream: true}); err != nil {
		t.Fatal(err)
	}
	return h
}

// get asks for a path, with a player's user agent.
func (h *harness) get(path string) (*http.Response, string) {
	h.t.Helper()
	req, err := http.NewRequest("GET", h.front.URL+path, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	req.Header.Set("User-Agent", "TiviMate/5.1 (fictional)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

// api calls player_api.php with the login and the given parameters, and
// decodes its answer into v.
func (h *harness) api(v any, params ...string) {
	h.t.Helper()
	q := url.Values{"username": {username}, "password": {password}}
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	resp, body := h.get("/player_api.php?" + q.Encode())
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json" {
		h.t.Fatalf("%v: %s %s %q", params, resp.Status, resp.Header.Get("Content-Type"), body)
	}
	if err := json.Unmarshal([]byte(body), v); err != nil {
		h.t.Fatalf("%v: %v in %q", params, err, body)
	}
}

// path is a stream's path for the login.
func path(kind string, rest ...string) string {
	return "/" + kind + "/" + username + "/" + password + "/" + strings.Join(rest, "/")
}

// panelPassword is the fictional panel's password for the films source.
const panelPassword = "pw-films"

// panel is a fictional Xtream panel with films and series, which serves
// their files.
type panel struct {
	*httptest.Server
}

func newPanel(t *testing.T) *panel {
	t.Helper()
	replies := map[string]string{
		"get_vod_categories": `[{"category_id":"21","category_name":"Drama"},{"category_id":"22","category_name":"Comedy"}]`,
		"get_vod_streams": fmt.Sprintf(`[
			{"stream_id":5001,"name":"The Long Field","category_id":"21","container_extension":"mkv","added":"%d","rating":"7.4","year":"2024","stream_icon":"http://img.example/5001.jpg"},
			{"stream_id":5002,"name":"Quiet Harbour","category_id":"22","container_extension":"mp4","added":"%d"},
			{"stream_id":5003,"name":"Copper Signal","category_id":"21","added":"%d"}]`,
			time.Date(2026, 12, 3, 0, 0, 0, 0, time.UTC).Unix(), time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC).Unix(),
			time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC).Unix()),
		"get_series_categories": `[{"category_id":"31","category_name":"Documentary"}]`,
		"get_series": `[{"series_id":7001,"name":"Coastline","category_id":"31","plot":"Waves.","last_modified":"1796000000",
			"cover":"http://img.example/7001.jpg","episode_run_time":"40"},
			{"series_id":7002,"name":"Harbour Lights","category_id":"31","last_modified":"1795000000"}]`,
		"get_vod_info": `{"info":{"plot":"A long field.","duration_secs":5400,"genre":"Drama"},"movie_data":{"stream_id":5001,"container_extension":"mkv"}}`,
	}
	seriesInfo := map[string]string{
		"7001": `{"info":{"name":"Coastline","plot":"Waves, closer."},
			"seasons":[{"season_number":1,"name":"The First Season"}],
			"episodes":{"1":[{"id":"9001","episode_num":1,"title":"Rocks","container_extension":"mkv","info":{"duration_secs":2400}},
				{"id":"9002","episode_num":2,"title":"Sand","container_extension":"mkv"}],
			"2":[{"id":"9003","episode_num":1,"title":"Tides","container_extension":"mp4"}]}}`,
		// Two episodes with the same number.
		"7002": `{"info":{"name":"Harbour Lights"},
			"episodes":{"1":[{"id":"9101","episode_num":3,"title":"One","container_extension":"mkv"},
				{"id":"9102","episode_num":3,"title":"Two","container_extension":"mkv"}]}}`,
	}
	p := &panel{}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, kind := range []string{"movie", "series"} {
			if rest, ok := strings.CutPrefix(r.URL.Path, "/"+kind+"/viewer/"+panelPassword+"/"); ok {
				id, _, _ := strings.Cut(rest, ".")
				w.Header().Set("Content-Type", "video/x-matroska")
				http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(file(kind+" "+id)))
				return
			}
		}
		q := r.URL.Query()
		reply, ok := replies[q.Get("action")]
		if q.Get("action") == "get_series_info" {
			reply, ok = seriesInfo[q.Get("series_id")]
		}
		if q.Get("username") != "viewer" || q.Get("password") != panelPassword || !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(p.Close)
	return p
}

// file is what the fictional panel serves for a film or an episode.
func file(id string) []byte { return bytes.Repeat([]byte("FILE "+id+" "), 400) }

type userInfo struct {
	Auth    int      `json:"auth"`
	User    string   `json:"username"`
	Status  string   `json:"status"`
	Expires *string  `json:"exp_date"`
	Max     string   `json:"max_connections"`
	Active  string   `json:"active_cons"`
	Formats []string `json:"allowed_output_formats"`
	Created string   `json:"created_at"`
	Trial   string   `json:"is_trial"`
}

type serverInfo struct {
	URL       string `json:"url"`
	Port      string `json:"port"`
	Protocol  string `json:"server_protocol"`
	Timezone  string `json:"timezone"`
	Timestamp int64  `json:"timestamp_now"`
	TimeNow   string `json:"time_now"`
}

func TestAccount(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var got struct {
		User   userInfo   `json:"user_info"`
		Server serverInfo `json:"server_info"`
	}
	h.api(&got)
	u, s := got.User, got.Server
	if u.Auth != 1 || u.User != username || u.Status != "Active" || u.Expires != nil || u.Max != "100" ||
		u.Active != "0" || len(u.Formats) != 1 || u.Formats[0] != "ts" || u.Trial != "0" {
		t.Errorf("user_info: %+v", u)
	}
	host, port, _ := strings.Cut(strings.TrimPrefix(h.front.URL, "http://"), ":")
	if s.URL != host || s.Port != port || s.Protocol != "http" || s.Timezone != "UTC" || s.Timestamp != h.now.Unix() ||
		s.TimeNow != "2026-12-04 10:30:00" {
		t.Errorf("server_info: %+v", s)
	}

	// A limit and an end date.
	if err := h.store.SetAccountStreams(ctx, store.HouseholdAccount, 2); err != nil {
		t.Fatal(err)
	}
	ends := h.now.Add(48 * time.Hour)
	if err := h.store.SetAccountExpiry(ctx, store.HouseholdAccount, ends); err != nil {
		t.Fatal(err)
	}
	h.api(&got)
	if got.User.Max != "2" || got.User.Expires == nil || *got.User.Expires != strconv.FormatInt(ends.Unix(), 10) {
		t.Errorf("with a limit and an end date: %+v", got.User)
	}

	// POST, as some players send it.
	resp, err := http.PostForm(h.front.URL+"/player_api.php", url.Values{"username": {username}, "password": {password}})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"auth":1`) {
		t.Errorf("POST: %s %s", resp.Status, b)
	}
	// A form far larger than a login is refused before it is read.
	resp, err = http.PostForm(h.front.URL+"/player_api.php", url.Values{"username": {username}, "password": {password},
		"padding": {strings.Repeat("x", 2*maxFormBytes)}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("POST of a large form: %s", resp.Status)
	}

	// An account that has ended reads its state, and nothing else.
	if err := h.store.SetAccountExpiry(ctx, store.HouseholdAccount, h.now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	h.api(&got)
	if got.User.Auth != 1 || got.User.Status != "Expired" {
		t.Errorf("an ended account: %+v", got.User)
	}
	var list []any
	h.api(&list, "action", "get_live_streams")
	if len(list) != 0 {
		t.Errorf("an ended account's channels: %v", list)
	}
}

// A login that does not open the API answers as panels do: auth 0.
func TestWrongLogins(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	failed := func(what, user, pass string) {
		t.Helper()
		_, body := h.get("/player_api.php?" + url.Values{"username": {user}, "password": {pass}}.Encode())
		if body != `{"user_info":{"auth":0}}`+"\n" {
			t.Errorf("%s: %q", what, body)
		}
	}
	failed("a wrong password", username, "wrong-horse-battery")
	failed("no password", username, "")
	failed("an unknown user", "sam", password)
	failed("a long username", strings.Repeat("a", 300), password)
	if err := h.store.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{M3U: true}); err != nil {
		t.Fatal(err)
	}
	failed("an account without the Xtream API", username, password)
	if resp, body := h.get(path("live", fmt.Sprint(h.ids["Alpha"])+".ts")); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(body, "may not use the Xtream API") {
		t.Errorf("a stream of an account without the Xtream API: %s %q", resp.Status, body)
	}
	if err := h.store.SetAccountOutputs(ctx, store.HouseholdAccount, store.Outputs{Xtream: true}); err != nil {
		t.Fatal(err)
	}
	if resp, body := h.get("/live/" + username + "/wrong-horse-battery/1.ts"); resp.StatusCode != http.StatusForbidden ||
		!strings.Contains(body, "Wrong username or password") {
		t.Errorf("a stream with a wrong password: %s %q", resp.Status, body)
	}
	// With the outputs off, nothing is there.
	if err := h.store.SetOutputs(ctx, false); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/player_api.php?username=alex&password=" + password, path("live", "1.ts"),
		"/get.php?username=alex&password=" + password, "/xmltv.php?username=alex&password=" + password} {
		if resp, _ := h.get(p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s with the outputs off: %s", p, resp.Status)
		}
	}
	if strings.Contains(h.logs.String(), password) {
		t.Errorf("the log holds the password:\n%s", h.logs.String())
	}
}

// Players from outside the server's own networks are refused, unless the
// outputs answer anywhere.
func TestOwnNetworks(t *testing.T) {
	h := newHarness(t)
	mux := routes.NewMux()
	h.out.Register(mux)
	ask := func(from string) int {
		r := httptest.NewRequest("GET", "/player_api.php?username=alex&password="+password, nil)
		r.RemoteAddr = from
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, r)
		return rec.Code
	}
	if got := ask("203.0.113.7:5000"); got != http.StatusForbidden {
		t.Errorf("from outside: %d", got)
	}
	if got := ask("192.168.1.20:5000"); got != http.StatusOK {
		t.Errorf("from a private address: %d", got)
	}
	if err := h.store.SetOutputsAnywhere(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if got := ask("203.0.113.7:5000"); got != http.StatusOK {
		t.Errorf("anywhere, from outside: %d", got)
	}
}

type liveStreamJSON struct {
	Num         int     `json:"num"`
	Name        string  `json:"name"`
	StreamType  string  `json:"stream_type"`
	StreamID    int64   `json:"stream_id"`
	StreamIcon  string  `json:"stream_icon"`
	EPGID       string  `json:"epg_channel_id"`
	CategoryID  *string `json:"category_id"`
	CategoryIDs []int64 `json:"category_ids"`
	Archive     int     `json:"tv_archive"`
	Days        int     `json:"tv_archive_duration"`
	Direct      string  `json:"direct_source"`
}

func TestLiveLists(t *testing.T) {
	h := newHarness(t)
	var cats []category
	h.api(&cats, "action", "get_live_categories")
	var names []string
	ids := map[string]string{}
	for _, c := range cats {
		names = append(names, c.Name)
		ids[c.Name] = c.ID
		if c.ParentID != 0 || c.ID == "" {
			t.Errorf("category %+v", c)
		}
	}
	if strings.Join(names, ",") != "News,Sport,Radio,Late" {
		t.Errorf("categories: %v", names)
	}

	var streams []liveStreamJSON
	h.api(&streams, "action", "get_live_streams")
	byName := map[string]liveStreamJSON{}
	for _, s := range streams {
		byName[s.Name] = s
	}
	if len(streams) != 5 {
		t.Fatalf("streams: %+v", streams)
	}
	a := byName["Alpha"]
	if a.Num != 1 || a.StreamID != h.ids["Alpha"] || a.StreamType != "live" || a.EPGID != fmt.Sprint(h.ids["Alpha"]) ||
		!strings.HasPrefix(a.StreamIcon, h.front.URL+"/artwork/logo/") || a.CategoryID == nil || *a.CategoryID != ids["News"] ||
		len(a.CategoryIDs) != 1 || fmt.Sprint(a.CategoryIDs[0]) != ids["News"] || a.Archive != 0 || a.Direct != "" {
		t.Errorf("Alpha: %+v", a)
	}
	if b := byName["Bravo"]; b.Archive != 1 || b.Days != 2 {
		t.Errorf("Bravo: %+v", b)
	}
	if r := byName["Radio Three"]; r.StreamType != "radio_streams" {
		t.Errorf("the radio station: %+v", r)
	}
	if p := byName["Plain"]; p.CategoryID != nil || len(p.CategoryIDs) != 0 {
		t.Errorf("a channel without a group: %+v", p)
	}
	h.api(&streams, "action", "get_live_streams", "category_id", ids["Sport"])
	if len(streams) != 1 || streams[0].Name != "Bravo" {
		t.Errorf("Sport's streams: %+v", streams)
	}

	// The adult channel is locked by default, which does nothing without a
	// PIN. With a PIN, it is left out, and so is its group.
	if err := h.store.SetAccountPIN(context.Background(), store.HouseholdAccount, "hash"); err != nil {
		t.Fatal(err)
	}
	h.api(&streams, "action", "get_live_streams")
	h.api(&cats, "action", "get_live_categories")
	if len(streams) != 4 || len(cats) != 3 {
		t.Errorf("with a PIN: %d streams, %d categories", len(streams), len(cats))
	}
	if resp, _ := h.get(path("live", fmt.Sprint(h.ids["Adults"])+".ts")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("a locked channel with a PIN: %s", resp.Status)
	}
}

type listingJSON struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Start      string `json:"start"`
	End        string `json:"end"`
	ChannelID  string `json:"channel_id"`
	StartStamp string `json:"start_timestamp"`
	StopStamp  string `json:"stop_timestamp"`
	NowPlaying *int   `json:"now_playing"`
	HasArchive *int   `json:"has_archive"`
}

func (l listingJSON) title(t *testing.T) string {
	b, err := base64.StdEncoding.DecodeString(l.Title)
	if err != nil {
		t.Errorf("title %q: %v", l.Title, err)
	}
	return string(b)
}

func TestGuideCalls(t *testing.T) {
	h := newHarness(t)
	a, b := fmt.Sprint(h.ids["Alpha"]), fmt.Sprint(h.ids["Bravo"])
	var got struct {
		Listings []listingJSON `json:"epg_listings"`
	}
	h.api(&got, "action", "get_short_epg", "stream_id", a)
	if len(got.Listings) != 4 {
		t.Fatalf("short guide: %+v", got.Listings)
	}
	onNow := h.now.Truncate(time.Hour)
	first := got.Listings[0]
	if first.title(t) != "a & programme 30" || first.Start != onNow.Format(xtreamTime) ||
		first.StartStamp != fmt.Sprint(onNow.Unix()) || first.ChannelID != a || first.NowPlaying != nil {
		t.Errorf("the programme on now: %+v", first)
	}
	if want := fmt.Sprint(h.ids["Alpha"]<<minuteBits | onNow.Unix()/60); first.ID != want {
		t.Errorf("its ID: %s, want %s", first.ID, want)
	}
	h.api(&got, "action", "get_short_epg", "stream_id", a, "limit", "2")
	if len(got.Listings) != 2 {
		t.Errorf("limit 2: %d", len(got.Listings))
	}
	for _, id := range []string{"", "x", "999999", fmt.Sprint(h.ids["Plain"])} {
		h.api(&got, "action", "get_short_epg", "stream_id", id)
		if len(got.Listings) != 0 {
			t.Errorf("stream %q: %+v", id, got.Listings)
		}
	}

	// The day's table: from as far back as Bravo's archive, which keeps
	// more than the guide holds, and a day back for Alpha.
	h.api(&got, "action", "get_simple_data_table", "stream_id", b)
	playing, archived := 0, 0
	for _, l := range got.Listings {
		if l.NowPlaying == nil || l.HasArchive == nil {
			t.Fatalf("a listing without its marks: %+v", l)
		}
		playing += *l.NowPlaying
		archived += *l.HasArchive
	}
	if len(got.Listings) != 40 || playing != 1 || archived != 30 {
		t.Errorf("Bravo's table: %d listings, %d on now, %d archived", len(got.Listings), playing, archived)
	}
	h.api(&got, "action", "get_simple_data_table", "stream_id", a)
	archived = 0
	for _, l := range got.Listings {
		archived += *l.HasArchive
	}
	if len(got.Listings) != 34 || archived != 0 {
		t.Errorf("Alpha's table: %d listings, %d archived", len(got.Listings), archived)
	}
}

func TestLiveStreams(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.store.SetAccountStreams(ctx, store.HouseholdAccount, 1); err != nil {
		t.Fatal(err)
	}
	a, b := fmt.Sprint(h.ids["Alpha"]), fmt.Sprint(h.ids["Bravo"])
	req, _ := http.NewRequest("GET", h.front.URL+path("live", a+".ts"), nil)
	req.Header.Set("User-Agent", "TiviMate/5.1 (fictional)")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 188*2)
	if _, err := io.ReadFull(resp.Body, buf); err != nil || resp.Header.Get("Content-Type") != "video/mp2t" || buf[0] != 0x47 {
		t.Fatalf("live: %s %s %v", resp.Status, resp.Header.Get("Content-Type"), err)
	}
	streams := h.hub.Streams()
	if len(streams) != 1 || streams[0].AccountID != store.HouseholdAccount || !hub.IsPlayer(streams[0].DeviceID) ||
		!strings.HasPrefix(streams[0].Player, "TiviMate/5.1 at 127.0.0.1") {
		t.Errorf("streams: %+v", streams)
	}
	var info struct {
		User userInfo `json:"user_info"`
	}
	h.api(&info)
	if info.User.Active != "1" || info.User.Max != "1" {
		t.Errorf("while it plays: %+v", info.User)
	}

	// Another player of the account is refused at its limit of one.
	other, _ := http.NewRequest("GET", h.front.URL+path("live", b+".ts"), nil)
	other.Header.Set("User-Agent", "VLC/3.0 (fictional)")
	resp2, err := http.DefaultClient.Do(other)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(text), "account") {
		t.Errorf("the second player: %s %q", resp2.Status, text)
	}
	resp.Body.Close()
	if err := h.store.SetAccountStreams(ctx, store.HouseholdAccount, 0); err != nil {
		t.Fatal(err)
	}

	for p, want := range map[string]int{
		path("live", a+".m3u8"):      http.StatusNotFound, // its source sends MPEG-TS
		path("live", a+".mkv"):       http.StatusNotFound,
		path("live", "0"+a+".ts"):    http.StatusNotFound,
		path("live", "999999.ts"):    http.StatusNotFound,
		path("live", "x.ts"):         http.StatusNotFound,
		"/live/" + username + "/r/0": http.StatusForbidden, // a login, not one of the hub's tokens
	} {
		if resp, body := h.get(p); resp.StatusCode != want {
			t.Errorf("%s: %s %q, want %d", p, resp.Status, body, want)
		}
	}
	// No extension plays as .ts.
	resp3, err := http.Get(h.front.URL + path("live", a))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(resp3.Body, buf); err != nil || resp3.StatusCode != http.StatusOK {
		t.Errorf("no extension: %s %v", resp3.Status, err)
	}
	resp3.Body.Close()

	// A path of the hub's, which shares /live/ with the output, goes to
	// the hub: here, a session that has ended.
	link := h.hub.Link(hub.PlayerBase+999, store.PlayableEntry{EntryID: h.ids["Alpha"]})
	token := strings.TrimSuffix(strings.TrimPrefix(link, "/live/"), ".ts")
	if resp, body := h.get("/live/" + token + "/r/0.ts"); resp.StatusCode != http.StatusNotFound ||
		!strings.Contains(body, "this stream has ended") {
		t.Errorf("the hub's resource: %s %q", resp.Status, body)
	}
	if strings.Contains(h.logs.String(), password) {
		t.Errorf("the log holds the password:\n%s", h.logs.String())
	}
}

func TestTimeshift(t *testing.T) {
	h := newHarness(t)
	a, b := fmt.Sprint(h.ids["Alpha"]), fmt.Sprint(h.ids["Bravo"])
	start := h.now.Truncate(time.Hour).Add(-5 * time.Hour)
	at := start.Format(shiftStart)
	for _, tc := range []struct {
		p    string
		want int
	}{
		{path("timeshift", "60", at, b+".ts"), http.StatusOK},
		{path("timeshift", "60", at, b), http.StatusOK},
		{path("timeshift", "60", h.now.Add(time.Hour).Format(shiftStart), b+".ts"), http.StatusBadRequest},
		{path("timeshift", "60", h.now.Add(-72*time.Hour).Format(shiftStart), b+".ts"), http.StatusBadRequest},
		{path("timeshift", "0", at, b+".ts"), http.StatusBadRequest},
		{path("timeshift", "721", at, b+".ts"), http.StatusBadRequest},
		{path("timeshift", "60", "yesterday", b+".ts"), http.StatusBadRequest},
		{path("timeshift", "60", at, a+".ts"), http.StatusNotFound}, // no archive
		{path("timeshift", fmt.Sprintf("%s.ts?start=%d&end=%d", b, start.Unix(), start.Add(time.Hour).Unix())), http.StatusOK},
		{path("timeshift", fmt.Sprintf("%s.ts?start=%d", a, start.Unix())), http.StatusNotFound},
	} {
		p, want := tc.p, tc.want
		resp, err := http.Get(h.front.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			text, _ := io.ReadAll(resp.Body)
			t.Errorf("%s: %s %q, want %d", p, resp.Status, text, want)
		} else if want == http.StatusOK {
			buf := make([]byte, 188)
			if _, err := io.ReadFull(resp.Body, buf); err != nil || buf[0] != 0x47 {
				t.Errorf("%s: %v %x", p, err, buf[:4])
			}
		}
		resp.Body.Close()
	}
}

func TestPlaylistAndGuide(t *testing.T) {
	h := newHarness(t)
	login := url.Values{"username": {username}, "password": {password}}.Encode()
	resp, body := h.get("/get.php?" + login + "&type=m3u_plus&output=ts")
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "audio/x-mpegurl") {
		t.Fatalf("get.php: %s %s", resp.Status, resp.Header.Get("Content-Type"))
	}
	base := h.front.URL
	a, b := h.ids["Alpha"], h.ids["Bravo"]
	for _, want := range []string{
		fmt.Sprintf(`#EXTM3U url-tvg="%s/xmltv.php?%s"`, base, login),
		fmt.Sprintf(`#EXTINF:-1 tvg-id="%d" tvg-name="Alpha" tvg-chno="1" tvg-logo="%s/artwork/logo/%d/`, a, base, a),
		fmt.Sprintf("%s/live/%s/%s/%d.ts\n", base, username, password, a),
		fmt.Sprintf(`catchup="default" catchup-source="%s/timeshift/%s/%s/%d.ts?start={utc}&end={utcend}" catchup-days="2",Bravo`,
			base, username, password, b),
	} {
		if !strings.Contains(body, want) {
			t.Errorf("get.php lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "logos.example") || strings.Contains(body, "/a.ts\n") {
		t.Errorf("get.php has a source's address:\n%s", body)
	}

	resp, body = h.get("/xmltv.php?" + login)
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/xml") {
		t.Fatalf("xmltv.php: %s %s", resp.Status, resp.Header.Get("Content-Type"))
	}
	var tv struct {
		Channels []struct {
			ID string `xml:"id,attr"`
		} `xml:"channel"`
		Programmes []struct {
			Channel string `xml:"channel,attr"`
		} `xml:"programme"`
	}
	if err := xml.Unmarshal([]byte(body), &tv); err != nil {
		t.Fatalf("xmltv.php is not XML: %v", err)
	}
	if len(tv.Channels) != 5 || tv.Channels[0].ID != fmt.Sprint(a) || len(tv.Programmes) != 74 {
		t.Errorf("xmltv.php: %d channels, %d programmes", len(tv.Channels), len(tv.Programmes))
	}
	if resp, _ := h.get("/get.php?username=alex&password=wrong-horse-battery"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("get.php with a wrong password: %s", resp.Status)
	}
	if !strings.Contains(h.logs.String(), "output=xtream") || strings.Contains(h.logs.String(), password) {
		t.Errorf("the log:\n%s", h.logs.String())
	}
}

type filmJSON struct {
	Num        int     `json:"num"`
	Name       string  `json:"name"`
	StreamID   int64   `json:"stream_id"`
	StreamType string  `json:"stream_type"`
	Icon       string  `json:"stream_icon"`
	Rating     string  `json:"rating"`
	Rating5    float64 `json:"rating_5based"`
	Year       string  `json:"year"`
	Added      string  `json:"added"`
	CategoryID *string `json:"category_id"`
	Container  string  `json:"container_extension"`
}

func TestFilms(t *testing.T) {
	h := newHarness(t)
	var cats []category
	h.api(&cats, "action", "get_vod_categories")
	if len(cats) != 2 || cats[0].Name != "Drama" || cats[1].Name != "Comedy" {
		t.Fatalf("film categories: %+v", cats)
	}
	var films []filmJSON
	h.api(&films, "action", "get_vod_streams")
	if len(films) != 3 {
		t.Fatalf("films: %+v", films)
	}
	f := films[0]
	if f.Num != 1 || f.Name != "The Long Field" || f.StreamType != "movie" || f.StreamID == 5001 || f.Rating != "7.4" ||
		f.Rating5 != 3.7 || f.Year != "2024" || f.CategoryID == nil || *f.CategoryID != cats[0].ID || f.Container != "mkv" ||
		!strings.HasPrefix(f.Icon, h.front.URL+"/artwork/poster/") {
		t.Errorf("the first film: %+v", f)
	}
	if films[1].Name != "Quiet Harbour" || films[2].Container != "mp4" || films[2].Num != 3 {
		t.Errorf("films: %+v", films)
	}
	h.api(&films, "action", "get_vod_streams", "category_id", cats[1].ID)
	if len(films) != 1 || films[0].Name != "Quiet Harbour" {
		t.Errorf("Comedy's films: %+v", films)
	}
	h.api(&films, "action", "get_vod_streams", "category_id", "999999")
	if len(films) != 0 {
		t.Errorf("an unknown category's films: %+v", films)
	}

	// A film's details, read from its source when a player opens it.
	id := fmt.Sprint(h.panel.filmID(t, h, "The Long Field"))
	var info struct {
		Info struct {
			Plot     string `json:"plot"`
			Genre    string `json:"genre"`
			Secs     int    `json:"duration_secs"`
			Duration string `json:"duration"`
			Rating   string `json:"rating"`
		} `json:"info"`
		Data struct {
			StreamID   int64  `json:"stream_id"`
			Container  string `json:"container_extension"`
			CategoryID string `json:"category_id"`
		} `json:"movie_data"`
	}
	h.api(&info, "action", "get_vod_info", "vod_id", id)
	if info.Info.Plot != "A long field." || info.Info.Genre != "Drama" || info.Info.Secs != 5400 ||
		info.Info.Duration != "01:30:00" || fmt.Sprint(info.Data.StreamID) != id || info.Data.Container != "mkv" ||
		info.Data.CategoryID != cats[0].ID {
		t.Errorf("get_vod_info: %+v", info)
	}
	var none map[string]any
	h.api(&none, "action", "get_vod_info", "vod_id", "999999")
	if fmt.Sprint(none["info"]) != "[]" {
		t.Errorf("an unknown film: %v", none)
	}

	// It plays the file as it is, from the part a player asks for.
	req, _ := http.NewRequest("GET", h.front.URL+path("movie", id+".mkv"), nil)
	req.Header.Set("Range", "bytes=0-14")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(b) != "FILE movie 5001" {
		t.Errorf("the film: %s %q", resp.Status, b)
	}
	if resp, _ := h.get(path("movie", "999999.mkv")); resp.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown film: %s", resp.Status)
	}
}

// filmID returns the ID that Overscan gives a film of the panel.
func (p *panel) filmID(t *testing.T, h *harness, name string) int64 {
	t.Helper()
	var films []filmJSON
	h.api(&films, "action", "get_vod_streams")
	for _, f := range films {
		if f.Name == name {
			return f.StreamID
		}
	}
	t.Fatalf("no film %q", name)
	return 0
}

func TestSeries(t *testing.T) {
	h := newHarness(t)
	var cats []category
	h.api(&cats, "action", "get_series_categories")
	if len(cats) != 1 || cats[0].Name != "Documentary" {
		t.Fatalf("series categories: %+v", cats)
	}
	var list []struct {
		Num      int     `json:"num"`
		Name     string  `json:"name"`
		SeriesID int64   `json:"series_id"`
		Cover    string  `json:"cover"`
		Plot     string  `json:"plot"`
		RunTime  string  `json:"episode_run_time"`
		Category *string `json:"category_id"`
		Backdrop []any   `json:"backdrop_path"`
	}
	h.api(&list, "action", "get_series")
	if len(list) != 2 || list[0].Name != "Coastline" || list[0].Plot != "Waves." || list[0].RunTime != "40" ||
		list[0].Category == nil || *list[0].Category != cats[0].ID || list[0].Backdrop == nil ||
		!strings.HasPrefix(list[0].Cover, h.front.URL+"/artwork/poster/") {
		t.Fatalf("series: %+v", list)
	}
	type episodeJSON struct {
		ID        string `json:"id"`
		Num       int    `json:"episode_num"`
		Title     string `json:"title"`
		Season    int    `json:"season"`
		Container string `json:"container_extension"`
		Info      struct {
			Secs int `json:"duration_secs"`
		} `json:"info"`
	}
	var info struct {
		Seasons []struct {
			Number int    `json:"season_number"`
			Name   string `json:"name"`
			Count  int    `json:"episode_count"`
		} `json:"seasons"`
		Info struct {
			Name string `json:"name"`
			Plot string `json:"plot"`
		} `json:"info"`
		Episodes map[string][]episodeJSON `json:"episodes"`
	}
	h.api(&info, "action", "get_series_info", "series_id", fmt.Sprint(list[0].SeriesID))
	if len(info.Seasons) != 2 || info.Seasons[0].Name != "The First Season" || info.Seasons[1].Name != "Season 2" ||
		info.Seasons[0].Count != 2 || info.Info.Name != "Coastline" || info.Info.Plot != "Waves, closer." {
		t.Errorf("get_series_info: %+v", info)
	}
	one := info.Episodes["1"]
	if len(one) != 2 || one[0].Title != "Rocks" || one[0].Num != 1 || one[1].Num != 2 || one[0].Season != 1 ||
		one[0].Container != "mkv" || one[0].ID == "9001" || one[0].Info.Secs != 2400 || len(info.Episodes["2"]) != 1 {
		t.Errorf("season 1's episodes: %+v", info.Episodes)
	}
	resp, err := http.Get(h.front.URL + path("series", one[1].ID+".mkv"))
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 16)
	_, _ = io.ReadFull(resp.Body, b)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(b) != "FILE series 9002" {
		t.Errorf("an episode: %s %q", resp.Status, b)
	}

	// Two episodes with the same number go by their places.
	h.api(&info, "action", "get_series_info", "series_id", fmt.Sprint(list[1].SeriesID))
	if eps := info.Episodes["1"]; len(eps) != 2 || eps[0].Num != 1 || eps[1].Num != 2 {
		t.Errorf("Harbour Lights: %+v", info.Episodes)
	}
	var none map[string]any
	h.api(&none, "action", "get_series_info", "series_id", fmt.Sprint(h.panel.filmID(t, h, "Quiet Harbour")))
	if fmt.Sprint(none["episodes"]) != "[]" {
		t.Errorf("a film as a series: %v", none)
	}
}

// What the output does not know goes to the capture log, without the
// login or anything else that can carry a secret.
func TestUnknownAction(t *testing.T) {
	h := newHarness(t)
	var got []any
	h.api(&got, "action", "get_something_new", "stream_id", "7", "device_token", "tok-1234", "mac", "00:1A:79:00:00:01")
	if len(got) != 0 {
		t.Errorf("an unknown action: %v", got)
	}
	captures, err := h.store.Captures(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(captures) != 1 || captures[0].Module != "xtream" || captures[0].Action != "get_something_new" ||
		captures[0].Params != "action,device_token,mac,password,stream_id,username" {
		t.Fatalf("captures: %+v", captures)
	}
	example, err := url.ParseQuery(captures[0].Example)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"stream_id": "7", "action": "get_something_new", "username": "REDACTED",
		"password": "REDACTED", "device_token": "REDACTED", "mac": "REDACTED"} {
		if got := example.Get(name); got != want {
			t.Errorf("the example's %s is %q, want %q", name, got, want)
		}
	}
}

func FuzzStreamFile(f *testing.F) {
	for _, s := range []string{"1.ts", "123.m3u8", "5", "1.", "01.ts", ".ts", "x.ts", "-1.ts", "1.ts.ts", "99999999999999999999.ts", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, file string) {
		id, ext, ok := streamFile(file)
		if !ok {
			return
		}
		want := strconv.FormatInt(id, 10)
		if ext != "" {
			want += "." + ext
		}
		if id <= 0 || want != file || strings.Contains(ext, ".") {
			t.Errorf("streamFile(%q) = %d, %q", file, id, ext)
		}
	})
}

// TestGuidePlaceholders checks that Xtream players see placeholder
// programmes where the setting asks for them, never marked as playing
// from an archive (D142).
func TestGuidePlaceholders(t *testing.T) {
	h := newHarness(t)
	if err := h.store.SetPlaceholders(context.Background(), 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Listings []listingJSON `json:"epg_listings"`
	}
	h.api(&got, "action", "get_short_epg", "stream_id", fmt.Sprint(h.ids["Plain"]))
	if len(got.Listings) != 4 {
		t.Fatalf("Plain's short guide: %+v", got.Listings)
	}
	for _, l := range got.Listings {
		if l.title(t) != "Plain" {
			t.Errorf("%+v", l)
		}
	}
	h.api(&got, "action", "get_short_epg", "stream_id", fmt.Sprint(h.ids["Alpha"]))
	if got.Listings[0].title(t) != "a & programme 30" {
		t.Errorf("Alpha's short guide: %+v", got.Listings)
	}
	// Bravo's day keeps its archive's marks, and its placeholders have
	// none.
	h.api(&got, "action", "get_simple_data_table", "stream_id", fmt.Sprint(h.ids["Bravo"]))
	archived, placeholders := 0, 0
	for _, l := range got.Listings {
		archived += *l.HasArchive
		if l.title(t) == "Bravo" {
			placeholders++
			if *l.HasArchive != 0 {
				t.Errorf("an archived placeholder: %+v", l)
			}
		}
	}
	if archived != 30 || placeholders < 19 || placeholders > 20 {
		t.Errorf("Bravo's table: %d listings, %d archived, %d placeholders", len(got.Listings), archived, placeholders)
	}
}
