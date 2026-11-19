// SPDX-License-Identifier: AGPL-3.0-or-later

// Package stalkertest is a fake Stalker portal for tests, written from the
// protocol notes (docs/protocol/stalker-protocol-notes.md): it answers as
// the notes say a portal does, with fictional channels, a guide, films and
// series in each of the notes' three shapes, and it serves their streams.
// All names, addresses and MAC addresses are fictional.
package stalkertest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// MAC is the fictional box that the portal knows.
const MAC = "00:1A:79:00:00:51"

// How a portal without a series module lists its series in vod (section 6).
const (
	Inline    = "inline"     // series:[1,2,…] on the item
	OnRequest = "on_request" // is_series, with seasons and episodes on request
	Module    = "module"     // a series module of its own
)

// Portal is a fake Stalker portal. Set its fields before the first request.
type Portal struct {
	*httptest.Server

	// APIPath is where the API answers: /stalker_portal/server/load.php,
	// unless set.
	APIPath string
	// Login and Password, if set, are what stb/do_auth must give before
	// any call but the handshake and the profile.
	Login, Password string
	// DeviceID, if set, is what get_profile must send as device_id.
	DeviceID string
	// Series is the shape of its series: Module (the default), Inline or
	// OnRequest.
	Series string
	// NoAllChannels makes itv/get_all_channels answer 404.
	NoAllChannels bool
	// RefusedBody makes a call without a valid token answer HTTP 200 with
	// "Authorization failed." instead of 401.
	RefusedBody bool
	// RelativeLinks makes create_link answer with paths on the portal.
	RelativeLinks bool
	// LinkError makes create_link answer with this error, such as limit.
	LinkError string
	// Blocked makes the profile say that the box is blocked.
	Blocked bool
	// WatchdogTimeout is the profile's watchdog_timeout in seconds.
	WatchdogTimeout int
	// GuideWait, if set, holds itv/get_epg_info's answer until it is
	// closed.
	GuideWait chan struct{}
	// OneUseLinks makes a channel's link play once: a second request for
	// it is refused, as a link whose token has lapsed.
	OneUseLinks bool
	// MoreFilms adds that many films to Drama after the twenty, for a list
	// of more than one page; their IDs are from 3001.
	MoreFilms int
	// OneUseFileLinks makes a film's or an episode's link answer one
	// request: a second is refused, as a link whose token has lapsed.
	OneUseFileLinks bool
	// Now is the portal's clock, for the guide: time.Now unless set.
	Now func() time.Time

	mu      sync.Mutex
	token   string
	authed  bool
	calls   map[string]int
	links   map[string]string // play tokens, to what they play
	streams int               // streams playing now
	most    int               // the most that played at once
	agents  map[string]bool   // the user agents seen
}

// New starts a fake portal, which the test stops when it ends.
func New(t testing.TB) *Portal {
	p := &Portal{calls: map[string]int{}, links: map[string]string{}, agents: map[string]bool{}}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

// Calls returns how many times the API was called with module/action.
func (p *Portal) Calls(what string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[what]
}

// Streams returns the streams playing now, and the most that played at
// once.
func (p *Portal) Streams() (now, most int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.streams, p.most
}

// ForgetToken makes the portal forget the session's token, as a portal
// does when a token lapses.
func (p *Portal) ForgetToken() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.token = ""
}

// UserAgents returns the user agents that called the API.
func (p *Portal) UserAgents() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for ua := range p.agents {
		out = append(out, ua)
	}
	return out
}

func (p *Portal) apiPath() string {
	if p.APIPath != "" {
		return p.APIPath
	}
	return "/stalker_portal/server/load.php"
}

func (p *Portal) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Portal) serve(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == p.apiPath():
		p.api(w, r)
	case strings.HasPrefix(r.URL.Path, "/play/"):
		p.play(w, r)
	case strings.HasSuffix(r.URL.Path, "/c/") || strings.HasSuffix(r.URL.Path, "/c"):
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html><body>A portal's page</body></html>")
	default:
		http.NotFound(w, r)
	}
}

// js answers with {"js": v}.
func js(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"js": v})
}

func (p *Portal) refuse(w http.ResponseWriter) {
	if p.RefusedBody {
		io.WriteString(w, "Authorization failed.")
		return
	}
	http.Error(w, "Authorization failed.", http.StatusUnauthorized)
}

func newToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return strings.ToUpper(hex.EncodeToString(b))
}

func (p *Portal) api(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	module, action := q.Get("type"), q.Get("action")
	p.mu.Lock()
	p.calls[module+"/"+action]++
	p.agents[r.UserAgent()] = true
	p.mu.Unlock()

	mac := ""
	if c, err := r.Cookie("mac"); err == nil {
		mac, _ = url.QueryUnescape(c.Value)
	}
	if !strings.EqualFold(mac, MAC) {
		p.refuse(w)
		return
	}
	if module == "stb" && action == "handshake" {
		token := newToken()
		p.mu.Lock()
		p.token, p.authed = token, false
		p.mu.Unlock()
		js(w, map[string]string{"token": token})
		return
	}
	p.mu.Lock()
	valid := p.token != "" && r.Header.Get("Authorization") == "Bearer "+p.token
	authed := p.authed || p.Login == ""
	p.mu.Unlock()
	if !valid {
		p.refuse(w)
		return
	}
	switch module + "/" + action {
	case "stb/get_profile":
		if p.DeviceID != "" && q.Get("device_id") != p.DeviceID {
			p.refuse(w)
			return
		}
		blocked := "0"
		if p.Blocked {
			blocked = "1"
		}
		js(w, map[string]string{"id": "7", "blocked": blocked, "watchdog_timeout": strconv.Itoa(p.WatchdogTimeout),
			"expire_billing_date": "2027-03-12 00:00:00"})
		return
	case "stb/do_auth":
		ok := q.Get("login") == p.Login && q.Get("password") == p.Password
		p.mu.Lock()
		p.authed = ok
		p.mu.Unlock()
		js(w, ok)
		return
	}
	if !authed {
		p.refuse(w)
		return
	}
	switch module + "/" + action {
	case "account_info/get_main_info":
		js(w, map[string]string{"end_date": "1804809600"})
	case "watchdog/get_events":
		js(w, map[string]any{"data": map[string]any{"msgs": 0}})
	case "itv/get_genres":
		js(w, []map[string]string{{"id": "*", "title": "All"}, {"id": "10", "title": "News"}, {"id": "20", "title": "Sports"}})
	case "itv/get_all_channels":
		if p.NoAllChannels {
			http.NotFound(w, r)
			return
		}
		js(w, map[string]any{"data": p.channels()})
	case "itv/get_ordered_list":
		all := p.channels()
		page, _ := strconv.Atoi(q.Get("p"))
		js(w, pageOf(all, max(page, 1)))
	case "itv/create_link":
		p.createLink(w, r, "live", strings.TrimPrefix(lastWord(q.Get("cmd")), "http://localhost/ch/"), "")
	case "itv/get_epg_info":
		if p.GuideWait != nil {
			select {
			case <-p.GuideWait:
			case <-r.Context().Done():
				return
			}
		}
		p.guide(w, q.Get("period"))
	case "vod/get_categories":
		js(w, []map[string]string{{"id": "*", "title": "All"}, {"id": "1", "title": "Drama"}, {"id": "2", "title": "Comedy"}})
	case "series/get_categories":
		if p.Series != "" && p.Series != Module {
			http.NotFound(w, r)
			return
		}
		js(w, []map[string]string{{"id": "*", "title": "All"}, {"id": "5", "title": "Documentary"}})
	case "vod/get_ordered_list", "series/get_ordered_list":
		if module == "series" && p.Series != "" && p.Series != Module {
			http.NotFound(w, r)
			return
		}
		p.orderedList(w, r, module)
	case "vod/create_link", "series/create_link":
		p.vodLink(w, r)
	default:
		http.NotFound(w, r)
	}
}

func lastWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// pageOf returns page n, from 1, of items, 14 to a page.
func pageOf[T any](items []T, n int) map[string]any {
	start := min((n-1)*14, len(items))
	end := min(start+14, len(items))
	return map[string]any{"total_items": len(items), "max_page_items": 14, "data": items[start:end]}
}

// channels are the portal's three channels in two genres (section 10).
// Numbers come as numbers and strings, as portals send them.
func (p *Portal) channels() []map[string]any {
	return []map[string]any{
		{"id": "101", "name": "Example News", "number": "1", "cmd": "ffrt http://localhost/ch/101_", "logo": "",
			"tv_genre_id": "10", "xmltv_id": "news.example", "censored": "0", "tv_archive": "0", "tv_archive_duration": "0"},
		{"id": 102, "name": "Example Sports", "number": 2, "cmd": "ffrt http://localhost/ch/102_",
			"logo": "http://img.example/102.png", "tv_genre_id": "20", "xmltv_id": "", "censored": 0,
			"tv_archive": 1, "tv_archive_duration": 24},
		{"id": "103", "name": "Example Weather", "number": "3", "cmd": "ffmpeg http://localhost/ch/103_",
			"logo": "/stalker_portal/misc/logos/103.png", "tv_genre_id": "10", "censored": "0"},
	}
}

// guide answers itv/get_epg_info: hour-long programmes on Example News and
// two-hour ones on Example Sports over the period, from the hour before
// now; none on Example Weather. The first programme of each is in the
// archive.
func (p *Portal) guide(w http.ResponseWriter, period string) {
	hours, _ := strconv.Atoi(period)
	if hours <= 0 {
		hours = 3
	}
	start := p.now().UTC().Truncate(time.Hour).Add(-time.Hour)
	data := map[string][]map[string]any{}
	for _, ch := range []struct {
		id, title string
		length    time.Duration
	}{{"101", "News", time.Hour}, {"102", "Sports", 2 * time.Hour}} {
		for i, at := 0, start; at.Before(start.Add(time.Duration(hours+1) * time.Hour)); i, at = i+1, at.Add(ch.length) {
			archive := "0"
			if i == 0 {
				archive = "1"
			}
			data[ch.id] = append(data[ch.id], map[string]any{"id": ch.id + strconv.Itoa(i), "ch_id": ch.id,
				"name": fmt.Sprintf("%s %d", ch.title, i), "descr": "A fictional programme.", "category": ch.title,
				"start_timestamp": strconv.FormatInt(at.Unix(), 10), "stop_timestamp": strconv.FormatInt(at.Add(ch.length).Unix(), 10),
				"mark_archive": archive})
		}
	}
	data["103"] = nil
	js(w, map[string]any{"data": data})
}

// Film is one of the portal's films.
type Film struct {
	ID, Name, Category string
}

// Films are the portal's twenty films, in two categories, the latest added
// first. Film 1003's file is asked for apart (has_files).
func Films() []Film {
	var out []Film
	for i := 1; i <= 20; i++ {
		out = append(out, Film{ID: strconv.Itoa(1000 + i), Name: fmt.Sprintf("Example Film %02d", i),
			Category: strconv.Itoa(1 + i%2)})
	}
	return out
}

func (p *Portal) filmItem(f Film, i int) map[string]any {
	item := map[string]any{"id": f.ID, "name": f.Name, "cmd": "/media/" + f.ID + ".mpg", "category_id": f.Category,
		"screenshot_uri": "/stalker_portal/screenshots/" + f.ID + ".jpg", "description": "A fictional film.",
		"actors": "A. Actor", "director": "D. Director", "genres_str": "Drama", "year": strconv.Itoa(2000 + i),
		"time": "95", "rating_imdb": "7.4", "age": "12+", "censored": "0",
		"added": time.Date(2026, 12, 20-i, 0, 0, 0, 0, time.UTC).Format("2006-01-02 15:04:05")}
	if f.ID == "1003" {
		item["has_files"] = 1
		item["time"] = "01:41:20"
	}
	return item
}

// The series: Example Coast, with two seasons of three and two episodes,
// and Example Harbour, with one season of two.
var seriesSeasons = map[string][]int{"2001": {3, 2}, "2002": {2}}

func seriesItem(id, name string, shape string) map[string]any {
	item := map[string]any{"id": id, "name": name, "cmd": "/media/" + id + ".mpg", "category_id": "5",
		"screenshot_uri": "http://img.example/" + id + ".jpg", "description": "A fictional series.", "year": "2020",
		"added": "1795000000", "censored": "0"}
	switch shape {
	case Inline:
		item["category_id"] = "1"
		n := 0
		for _, eps := range seriesSeasons[id] {
			n += eps
		}
		var series []int
		for i := 1; i <= n; i++ {
			series = append(series, i)
		}
		item["series"] = series
	case OnRequest:
		item["category_id"] = "1"
		item["is_series"] = "1"
	}
	return item
}

func (p *Portal) orderedList(w http.ResponseWriter, r *http.Request, module string) {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("p"))
	page = max(page, 1)
	shape := p.Series
	if shape == "" {
		shape = Module
	}
	if id := q.Get("movie_id"); id != "" {
		switch {
		case id == "1003" && module == "vod":
			js(w, pageOf([]map[string]any{{"id": "9003", "name": "Example Film 03.mkv"}}, page))
		case seriesSeasons[id] != nil && module == "series" && shape == Module:
			var seasons []map[string]any
			for s, eps := range seriesSeasons[id] {
				var numbers []string
				for e := 1; e <= eps; e++ {
					numbers = append(numbers, strconv.Itoa(e))
				}
				seasons = append(seasons, map[string]any{"id": id + strconv.Itoa(s+1), "name": "Season " + strconv.Itoa(s+1),
					"cmd": "/media/" + id + "_" + strconv.Itoa(s+1) + ".mpg", "series": numbers})
			}
			js(w, pageOf(seasons, page))
		case seriesSeasons[id] != nil && shape == OnRequest && q.Get("season_id") == "0":
			var seasons []map[string]any
			for s := range seriesSeasons[id] {
				seasons = append(seasons, map[string]any{"id": id + "-s" + strconv.Itoa(s+1), "is_season": true,
					"season_number": strconv.Itoa(s + 1), "name": fmt.Sprintf("Season %d", s+1)})
			}
			js(w, pageOf(seasons, page))
		case seriesSeasons[id] != nil && shape == OnRequest:
			season, _ := strconv.Atoi(strings.TrimPrefix(q.Get("season_id"), id+"-s"))
			if season < 1 || season > len(seriesSeasons[id]) {
				js(w, pageOf([]map[string]any{}, page))
				return
			}
			var eps []map[string]any
			for e := 1; e <= seriesSeasons[id][season-1]; e++ {
				eid := fmt.Sprintf("%s%d%02d", id, season, e)
				eps = append(eps, map[string]any{"id": eid, "is_episode": 1, "series_number": e,
					"name": fmt.Sprintf("Episode %d", e), "time": "42", "screenshot_uri": "/shots/" + eid + ".jpg",
					"description": "A fictional episode.", "cmd": "/media/" + eid + ".mpg"})
			}
			js(w, pageOf(eps, page))
		default:
			js(w, pageOf([]map[string]any{}, page))
		}
		return
	}
	category := q.Get("category")
	var items []map[string]any
	if module == "series" {
		for _, s := range [][2]string{{"2001", "Example Coast"}, {"2002", "Example Harbour"}} {
			if category == "*" || category == "5" {
				items = append(items, seriesItem(s[0], s[1], Module))
			}
		}
		js(w, pageOf(items, page))
		return
	}
	if shape != Module && (category == "*" || category == "1") {
		items = append(items, seriesItem("2001", "Example Coast", shape), seriesItem("2002", "Example Harbour", shape))
	}
	for i, f := range Films() {
		if category == "*" || category == f.Category {
			items = append(items, p.filmItem(f, i+1))
		}
	}
	for i := 1; i <= p.MoreFilms && (category == "*" || category == "1"); i++ {
		f := Film{ID: strconv.Itoa(3000 + i), Name: fmt.Sprintf("Example Film %03d", 100+i), Category: "1"}
		items = append(items, p.filmItem(f, 20+i))
	}
	js(w, pageOf(items, page))
}

// vodLink answers vod/create_link: a film by its cmd, the file of a film
// whose files are apart, an inline series' or a season's episode by series,
// or an episode on request by its own cmd.
func (p *Portal) vodLink(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	cmd := lastWord(q.Get("cmd"))
	name, ok := strings.CutPrefix(cmd, "/media/")
	name, ok2 := strings.CutSuffix(name, ".mpg")
	if !ok || !ok2 {
		js(w, map[string]string{"cmd": "", "error": "nothing_to_play"})
		return
	}
	switch {
	case name == "1003":
		js(w, map[string]string{"cmd": "", "error": "nothing_to_play"}) // its file is apart
		return
	case name == "file_9003":
		name = "1003"
	}
	p.createLink(w, r, "vod", name, q.Get("series"))
}

// createLink answers create_link with a link to /play/, unless LinkError
// says otherwise.
func (p *Portal) createLink(w http.ResponseWriter, r *http.Request, kind, what, series string) {
	if p.LinkError != "" {
		js(w, map[string]string{"cmd": "", "error": p.LinkError})
		return
	}
	if what == "" {
		js(w, map[string]string{"cmd": "", "error": "nothing_to_play"})
		return
	}
	if series != "" && series != "0" {
		what += "e" + series
	}
	token := newToken()
	p.mu.Lock()
	p.links[token] = kind + ":" + what
	p.mu.Unlock()
	ext := ".ts"
	if kind == "vod" {
		ext = ".mp4"
	}
	path := "/play/" + kind + ext + "?token=" + token
	if p.RelativeLinks {
		js(w, map[string]string{"cmd": path, "error": ""})
		return
	}
	js(w, map[string]string{"cmd": "ffmpeg " + p.URL + path, "error": ""})
}

// Content returns what the portal plays for a film or an episode: the film's
// ID, or its series' or season's cmd's name and the episode, such as
// 2001_2e1 or 2001e4.
func Content(what string) []byte {
	return bytes.Repeat([]byte("FILE "+what+" "), 2000)
}

// play serves what a link plays: a channel's endless MPEG-TS, or a film's
// file with ranges.
func (p *Portal) play(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	token := r.URL.Query().Get("token")
	what, ok := p.links[token]
	if ok {
		p.streams++
		p.most = max(p.most, p.streams)
		if p.OneUseLinks && strings.HasPrefix(what, "live:") || p.OneUseFileLinks && strings.HasPrefix(what, "vod:") {
			delete(p.links, token)
		}
	}
	p.mu.Unlock()
	if !ok {
		http.Error(w, "no such link", http.StatusForbidden)
		return
	}
	defer func() {
		p.mu.Lock()
		p.streams--
		p.mu.Unlock()
	}()
	kind, name, _ := strings.Cut(what, ":")
	if kind == "vod" {
		w.Header().Set("Content-Type", "video/mp4")
		http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(Content(name)))
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	packet := append([]byte{0x47, 0x1f, 0xff, 0x10}, bytes.Repeat([]byte{0xff}, 184)...)
	rc := http.NewResponseController(w)
	for {
		if _, err := w.Write(packet); err != nil {
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
