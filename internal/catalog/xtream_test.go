// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/xtream"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// A fictional Xtream panel, which also publishes its channels as an M3U
// playlist the way such panels do.
type fakePanel struct {
	mu       sync.Mutex
	status   string // the account's status
	limit    int    // the account's max_connections
	formats  string // allowed_output_formats, as JSON
	streams  string // get_live_streams, as JSON
	playlist string // get.php, with HOST for the server's address
}

const panelPassword = "pw/" + secret

const panelStreams = `[
	{"num":1,"name":"Example News","stream_type":"live","stream_id":101,"stream_icon":"http://logos.example/news.png",
	 "epg_channel_id":"news.example","category_id":"1","tv_archive":1,"tv_archive_duration":"3"},
	{"num":2,"name":"Example Sports","stream_type":"live","stream_id":"102","epg_channel_id":null,"category_id":"2"},
	{"num":3,"name":"Example Sports","stream_type":"live","stream_id":103,"epg_channel_id":"","category_id":"2"},
	{"num":4,"name":"Lost","stream_type":"live","stream_id":104,"category_id":null},
	{"num":5,"name":"Example News","stream_type":"live","stream_id":101,"category_id":"1"}
]`

const panelPlaylist = `#EXTM3U
#EXTINF:-1 tvg-id="news.example" tvg-name="Example News" group-title="News",Example News
HOST/live/viewer/PASS/101.ts
#EXTINF:-1 tvg-id="" tvg-name="Example Sports" group-title="Sports",Example Sports
HOST/live/viewer/PASS/102.ts
#EXTINF:-1 tvg-id="" tvg-name="Example Sports" group-title="Sports",Example Sports
HOST/live/viewer/PASS/103.ts
#EXTINF:-1 tvg-id="" tvg-name="Lost" group-title="",Lost
HOST/live/viewer/PASS/104.ts
`

func newPanel(t *testing.T) (*fakePanel, *httptest.Server) {
	t.Helper()
	p := &fakePanel{status: "Active", limit: 2, formats: `["m3u8","ts"]`, streams: panelStreams, playlist: panelPlaylist}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		q := r.URL.Query()
		if q.Get("username") != "viewer" || q.Get("password") != panelPassword {
			io.WriteString(w, `{"user_info":{"auth":0}}`)
			return
		}
		switch r.URL.Path + "?" + q.Get("action") {
		case "/player_api.php?":
			fmt.Fprintf(w, `{"user_info":{"auth":1,"status":%q,"exp_date":"1798761600","max_connections":"%d",
				"active_cons":"0","allowed_output_formats":%s},"server_info":{"timezone":"Europe/London"}}`,
				p.status, p.limit, p.formats)
		case "/player_api.php?get_live_categories":
			io.WriteString(w, `[{"category_id":"1","category_name":"News","parent_id":0},
				{"category_id":"2","category_name":"Sports","parent_id":0}]`)
		case "/player_api.php?get_live_streams":
			io.WriteString(w, p.streams)
		case "/xmltv.php?":
			start := time.Now().UTC().Truncate(time.Hour)
			fmt.Fprintf(w, `<?xml version="1.0"?><tv><channel id="news.example"><display-name>Example News</display-name></channel>
				<programme start="%s +0000" stop="%s +0000" channel="news.example"><title>News Hour</title></programme></tv>`,
				start.Format("20060102150405"), start.Add(time.Hour).Format("20060102150405"))
		case "/get.php?":
			host := "http://" + r.Host
			io.WriteString(w, strings.NewReplacer("HOST", host, "PASS", "pw%2F"+secret).Replace(p.playlist))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return p, srv
}

func (p *fakePanel) set(fn func(p *fakePanel)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p)
}

func xtreamSettings(srv *httptest.Server) source.Settings {
	return source.Settings{Name: "main", Type: source.TypeXtream, URL: srv.URL, Username: "viewer",
		Password: panelPassword, Refresh: time.Hour, MaxConnections: 0, Numbers: source.NumbersPlaylist}
}

// newStore opens a store with a key, and a refresher that logs to logs.
func newStore(t *testing.T) (*store.Store, *Refresher, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	return st, NewRefresher(st, slog.New(slog.NewTextHandler(logs, nil)), "test"), logs
}

func TestXtreamRefresh(t *testing.T) {
	_, srv := newPanel(t)
	st, r, logs := newStore(t)
	ctx := context.Background()
	settings := xtreamSettings(srv)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := r.Refresh(ctx, settings, id)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Added != 4 {
		t.Errorf("changes %+v", ch)
	}
	cs, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range cs {
		got = append(got, fmt.Sprintf("%d %s [%s] %s %d", c.Number, c.Name, c.Group, c.XMLTVID, c.ArchiveDays))
	}
	want := []string{"1 Example News [News] news.example 3", "2 Example Sports [Sports]  0",
		"3 Example Sports [Sports]  0", "4 Lost []  0"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("channels\n%q\nwant\n%q", got, want)
	}
	if !strings.Contains(logs.String(), "skipped=1") {
		t.Errorf("the repeated stream is not counted as skipped: %s", logs)
	}

	// The stream's address is made at play time, with the login in it.
	p, err := st.PlayableEntry(ctx, cs[1].EntryID)
	if err != nil {
		t.Fatal(err)
	}
	if want := srv.URL + "/live/viewer/pw%2F" + secret + "/102.ts"; p.StreamURL != want {
		t.Errorf("stream %q, want %q", p.StreamURL, want)
	}

	src, err := st.Source(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	want2 := store.SourceAccount{Status: "Active", ExpiresAt: time.Unix(1798761600, 0).UTC(), MaxConnections: 2,
		TimeZone: "Europe/London"}
	if !src.Account.ExpiresAt.Equal(want2.ExpiresAt) || src.Account.Status != want2.Status ||
		src.Account.MaxConnections != 2 || src.Account.TimeZone != want2.TimeZone {
		t.Errorf("account %+v", src.Account)
	}
	if src.StreamLimit() != 2 || src.Host != strings.TrimPrefix(srv.URL, "http://") {
		t.Errorf("limit %d, host %q", src.StreamLimit(), src.Host)
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("the log shows the password: %s", logs)
	}
}

func TestXtreamFormat(t *testing.T) {
	panel, srv := newPanel(t)
	panel.set(func(p *fakePanel) { p.formats = `["m3u8"]` })
	st, r, _ := newStore(t)
	ctx := context.Background()
	settings := xtreamSettings(srv)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	cs, _ := st.LineupChannels(ctx, store.DefaultLineupID)
	p, err := st.PlayableEntry(ctx, cs[0].EntryID)
	if err != nil || !strings.HasSuffix(p.StreamURL, "/live/viewer/pw%2F"+secret+"/101.m3u8") {
		t.Errorf("stream %q, %v", p.StreamURL, err)
	}
}

func TestXtreamAccountCannotPlay(t *testing.T) {
	panel, srv := newPanel(t)
	st, r, logs := newStore(t)
	ctx := context.Background()
	settings := xtreamSettings(srv)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	panel.set(func(p *fakePanel) { p.status = "Expired" })
	_, err = r.Refresh(ctx, settings, id)
	var aerr *xtream.AccountError
	if !errors.As(err, &aerr) {
		t.Fatalf("refresh: %v", err)
	}
	src, _ := st.Source(ctx, id)
	if src.Account.Status != "Expired" || src.RefreshError != "the server says the account is Expired" || src.Channels != 4 {
		t.Errorf("source %+v", src)
	}
	tested, err := r.Test(ctx, settings)
	if err == nil || tested.Account == nil || tested.Account.Status != "Expired" {
		t.Errorf("test %+v, %v", tested, err)
	}

	wrong := settings
	wrong.Password = "nope"
	if _, err := r.Test(ctx, wrong); !errors.Is(err, xtream.ErrLogin) {
		t.Errorf("wrong password: %v", err)
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("the log shows the password: %s", logs)
	}
}

func TestXtreamLimitReachesTheHub(t *testing.T) {
	panel, srv := newPanel(t)
	st, r, _ := newStore(t)
	ctx := context.Background()
	settings := xtreamSettings(srv)
	settings.MaxConnections = 3
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var limits []int
	r.OnChange(func(srcs []store.Source) {
		mu.Lock()
		defer mu.Unlock()
		limits = append(limits, srcs[0].StreamLimit())
	})
	for _, limit := range []int{2, 2, 1, 5} {
		panel.set(func(p *fakePanel) { p.limit = limit })
		if _, err := r.Refresh(ctx, settings, id); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []int{2, 1, 3}; !reflect.DeepEqual(limits, want) {
		t.Errorf("limits %v, want %v", limits, want)
	}
}

// TestPlaylistBecomesXtream switches a playlist source to the API of the
// server that publishes it, and back: its channels keep their IDs, numbers
// and edits, found by their stream IDs even where the server renamed one.
func TestPlaylistBecomesXtream(t *testing.T) {
	panel, srv := newPanel(t)
	st, r, _ := newStore(t)
	ctx := context.Background()
	m3uSettings := source.Settings{Name: "main", Type: source.TypeM3U,
		URL:     srv.URL + "/get.php?username=viewer&password=pw%2F" + secret + "&type=m3u_plus&output=ts",
		Refresh: time.Hour, MaxConnections: 1, Numbers: source.NumbersPlaylist}
	id, err := st.AddSource(ctx, m3uSettings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, m3uSettings, id); err != nil {
		t.Fatal(err)
	}
	before, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 4 {
		t.Fatalf("channels %+v", before)
	}
	// Edits: a new name, and a number set by hand.
	if err := st.RenameEntry(ctx, store.DefaultLineupID, before[2].EntryID, "Sports Two"); err != nil {
		t.Fatal(err)
	}
	if err := st.PinEntryNumber(ctx, store.DefaultLineupID, before[0].EntryID, 50); err != nil {
		t.Fatal(err)
	}

	panel.set(func(p *fakePanel) {
		p.streams = strings.Replace(p.streams, `"name":"Example Sports","stream_type":"live","stream_id":"102"`,
			`"name":"Sports Extra","stream_type":"live","stream_id":"102"`, 1)
	})
	x := xtreamSettings(srv)
	if err := st.UpdateSource(ctx, id, x); err != nil {
		t.Fatal(err)
	}
	saved, err := st.SourceSettings(ctx, id)
	if err != nil || saved != x {
		t.Fatalf("settings %+v, %v", saved, err)
	}
	ch, err := r.Refresh(ctx, saved, id)
	if err != nil {
		t.Fatal(err)
	}
	if ch.Added != 0 || ch.Updated != 4 || ch.Missing != 0 {
		t.Errorf("changes %+v", ch)
	}
	after, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	summary := func(cs []store.LineupChannel) []string {
		var out []string
		for _, c := range cs {
			out = append(out, fmt.Sprintf("%d %d %d %s", c.EntryID, c.ChannelID, c.Number, c.Name))
		}
		return out
	}
	want := []string{
		fmt.Sprintf("%d %d 2 Sports Extra", before[1].EntryID, before[1].ChannelID),
		fmt.Sprintf("%d %d 3 Sports Two", before[2].EntryID, before[2].ChannelID),
		fmt.Sprintf("%d %d 4 Lost", before[3].EntryID, before[3].ChannelID),
		fmt.Sprintf("%d %d 50 Example News", before[0].EntryID, before[0].ChannelID),
	}
	if got := summary(after); !reflect.DeepEqual(got, want) {
		t.Errorf("after the switch\n%q\nwant\n%q", got, want)
	}
	p, err := st.PlayableEntry(ctx, before[2].EntryID)
	if err != nil || p.StreamURL != srv.URL+"/live/viewer/pw%2F"+secret+"/103.ts" {
		t.Errorf("stream %q, %v", p.StreamURL, err)
	}

	// And back to the playlist.
	if err := st.UpdateSource(ctx, id, m3uSettings); err != nil {
		t.Fatal(err)
	}
	if ch, err := r.Refresh(ctx, m3uSettings, id); err != nil || ch.Added != 0 || ch.Missing != 0 {
		t.Fatalf("back: %+v, %v", ch, err)
	}
	back, _ := st.LineupChannels(ctx, store.DefaultLineupID)
	want[0] = fmt.Sprintf("%d %d 2 Example Sports", before[1].EntryID, before[1].ChannelID)
	if got := summary(back); !reflect.DeepEqual(got, want) {
		t.Errorf("back on the playlist\n%q\nwant\n%q", got, want)
	}
}

func TestXtreamStreamID(t *testing.T) {
	for address, want := range map[string]string{
		"http://p.example:8080/live/viewer/pw%2Fx/101.ts":   "101",
		"http://p.example:8080/live/viewer/pw%2Fx/101.m3u8": "101",
		"http://p.example:8080/viewer/pw%2Fx/101":           "101",
		"http://p.example/panel/live/viewer/pw%2Fx/7.ts":    "7",
		"http://p.example:8080/live/other/pw%2Fx/101.ts":    "",
		"http://p.example:8080/movie/viewer/pw%2Fx/101.mkv": "",
		"http://p.example:8080/live/viewer/pw%2Fx/abc.ts":   "",
		"http://p.example:8080/x/viewer/pw%2Fx/101.ts":      "",
		"http://p.example:8080/101.ts":                      "",
	} {
		got, ok := xtreamStreamID(address, "viewer", "pw/x")
		if got != want || ok != (want != "") {
			t.Errorf("%s: %q, %v; want %q", address, got, ok, want)
		}
	}
	if got, ok := xtreamStreamID("http://p.example/live/anyone/any/42.ts", "", ""); !ok || got != "42" {
		t.Errorf("without a login: %q, %v", got, ok)
	}
}

func TestChangeXtreamLogin(t *testing.T) {
	_, srv := newPanel(t)
	st, _, _ := newStore(t)
	ctx := context.Background()
	x := xtreamSettings(srv)
	id, err := st.AddSource(ctx, x)
	if err != nil {
		t.Fatal(err)
	}
	// Only the password: the rest of the login stays.
	change := x
	change.URL, change.Username, change.Password = "", "", "new-password"
	if err := st.UpdateSource(ctx, id, change); err != nil {
		t.Fatal(err)
	}
	saved, _ := st.SourceSettings(ctx, id)
	if saved.URL != x.URL || saved.Username != "viewer" || saved.Password != "new-password" {
		t.Errorf("settings %+v", saved)
	}
	// Nothing: all of it stays.
	change.Password = ""
	change.Refresh = 2 * time.Hour
	if err := st.UpdateSource(ctx, id, change); err != nil {
		t.Fatal(err)
	}
	if saved, _ = st.SourceSettings(ctx, id); saved.Password != "new-password" || saved.Refresh != 2*time.Hour {
		t.Errorf("settings %+v", saved)
	}
	// A playlist source that becomes an Xtream one needs the whole login,
	// and the other way round, the playlist's address.
	m3u := source.Settings{Name: "list", URL: "/srv/list.m3u"}
	listID, err := st.AddSource(ctx, m3u)
	if err != nil {
		t.Fatal(err)
	}
	half := xtreamSettings(srv)
	half.Name, half.URL = "list", ""
	var serr *source.Error
	if err := st.UpdateSource(ctx, listID, half); !errors.As(err, &serr) || serr.Field != "url" {
		t.Errorf("half a login: %v", err)
	}
	back := source.Settings{Name: "main", Type: source.TypeM3U}
	if err := st.UpdateSource(ctx, id, back); !errors.As(err, &serr) || serr.Field != "url" {
		t.Errorf("no address: %v", err)
	}
	if err := st.UpdateSource(ctx, id, source.Settings{Name: "main", Type: source.TypeM3U, URL: "/srv/main.m3u"}); err != nil {
		t.Fatal(err)
	}
	src, _ := st.Source(ctx, id)
	if saved, _ = st.SourceSettings(ctx, id); saved.Username != "" || saved.Password != "" || src.Account != (store.SourceAccount{}) {
		t.Errorf("a playlist kept a login: %+v, %+v", saved, src.Account)
	}
}

// TestGuideAfterRefresh runs the refresher, which reads the source's guide
// after its channels.
func TestGuideAfterRefresh(t *testing.T) {
	_, srv := newPanel(t)
	st, r, logs := newStore(t)
	ctx := context.Background()
	id, err := st.AddSource(ctx, xtreamSettings(srv))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		r.Run(runCtx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	waitFor(t, "the guide", func() bool {
		src, err := st.Source(ctx, id)
		return err == nil && !src.Guide.ReadAt.IsZero()
	})
	src, _ := st.Source(ctx, id)
	if src.Guide.Channels != 1 || src.Guide.Programmes != 1 || src.Guide.Error != "" {
		t.Fatalf("guide %+v; log: %s", src.Guide, logs)
	}
	cs, _ := st.LineupChannels(ctx, store.DefaultLineupID)
	ps, err := st.Programmes(ctx, cs[0].ChannelID, time.Now(), time.Now().Add(time.Minute))
	if err != nil || len(ps) != 1 || ps[0].Title != "News Hour" {
		t.Errorf("programmes %+v, %v", ps, err)
	}
	if strings.Contains(logs.String(), secret) {
		t.Errorf("the log shows the password: %s", logs)
	}
}
