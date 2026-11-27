// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/tsinfo/tstest"
)

// M8's Done when (the plan, section 14), walked end to end with this
// tool's fictional provider and clients: with fictional sources, a source
// set to three of its categories keeps only their channels and their
// programmes; a lineup started empty, with a group that takes categories of
// three sources, holds exactly their channels, and a channel that a source
// adds to one of them appears after a refresh, numbered after its group's
// last; numbers stay unique across the lineup; a merged channel plays the
// copy of the source put first, or the best copy seen; a lineup's page
// names the accounts and outputs that serve it, and its playlist and
// guide, Xtream login, a box and a Stalker client show the same. That an
// upgrade changes nothing a household set up is the rest of this tool's
// work, with the household's own groups, first numbers and the rest
// (household.setUpPlaylists).
func TestPlaylistsDoneWhen(t *testing.T) {
	w := startWalk(t)
	p, base, data, a, srv := w.p, w.base, w.data, w.a, w.srv
	call := func(method, path string, body, out any) {
		t.Helper()
		w.call(t, method, path, body, out)
	}
	// Two copies of Harbour News stream real pictures: 720p, and 1080i.
	p.usePictures(map[string][]byte{"hn": pictures(t, sps720p), "hnhd": pictures(t, sps1080i)})
	sources := map[string]int64{}
	var list struct{ Sources []idName }
	call("GET", "sources", nil, &list)
	for _, s := range list.Sources {
		sources[s.Name] = s.ID
	}
	for _, name := range []string{"two", "three"} {
		var added idName
		call("POST", "sources", map[string]any{"name": name, "type": "m3u", "url": p.url + "/" + name + ".m3u", "max_connections": 2}, &added)
		sources[name] = added.ID
	}
	if err := waitForSources(a, srv.started); err != nil {
		t.Fatal(err)
	}

	// A source set to three of its categories keeps only their channels
	// and their programmes.
	guideOf := func(source string) (channels, programmes int, readAt string) {
		t.Helper()
		var list struct {
			Sources []struct {
				Name  string `json:"name"`
				Guide struct {
					ReadAt     *string `json:"read_at"`
					Channels   int     `json:"channels"`
					Programmes int     `json:"programmes"`
				} `json:"guide"`
			} `json:"sources"`
		}
		call("GET", "sources", nil, &list)
		for _, s := range list.Sources {
			if s.Name == source && s.Guide.ReadAt != nil {
				return s.Guide.Channels, s.Guide.Programmes, *s.Guide.ReadAt
			}
		}
		return 0, 0, ""
	}
	allChannels, allProgrammes, firstRead := guideOf("main")
	time.Sleep(1100 * time.Millisecond) // guide reads are timed in seconds
	call("PUT", fmt.Sprintf("sources/%d/categories/live", sources["main"]), map[string]any{"only_ticked": true,
		"ticked": map[string]bool{"News": true, "Sport": true, "Kids": true, "Films": false, "Shop": false, "Radio": false}}, nil)
	call("POST", fmt.Sprintf("sources/%d/categories/live/read", sources["main"]), nil, nil)
	brought := map[string]bool{}
	for _, c := range mainChannels {
		brought[c.name] = c.group == "News" || c.group == "Sport" || c.group == "Kids"
	}
	household := entriesOf(t, a, "")
	for name, want := range brought {
		e, ok := household[name]
		if !ok || e.LeftOut == want {
			t.Errorf("main brings %s: %v, want %v (%+v)", name, ok && !e.LeftOut, want, e)
		}
	}
	withGuide := 0 // the channels brought that have programmes
	for _, c := range mainChannels {
		if brought[c.name] && c.guide != "" {
			withGuide++
		}
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		channels, programmes, readAt := guideOf("main")
		if readAt != firstRead {
			// The provider's guide has 60 programmes for each guide ID.
			if channels != withGuide || programmes != withGuide*60 || programmes >= allProgrammes {
				t.Errorf("main's guide after: %d channels and %d programmes, before %d and %d; want %d and %d",
					channels, programmes, allChannels, allProgrammes, withGuide, withGuide*60)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("main's guide was not read again")
		}
		time.Sleep(250 * time.Millisecond)
	}

	// A lineup started empty, with a group that takes categories of three
	// sources, holds exactly their channels, numbered from the group's
	// first number.
	var mine, desk idName
	call("POST", "lineups", map[string]any{"name": "Mine", "own_groups": true}, &mine)
	in := "?lineup=" + strconv.FormatInt(mine.ID, 10)
	if got := entriesOf(t, a, in); len(got) != 0 {
		t.Fatalf("the new lineup holds %v", got)
	}
	call("POST", "lineup/groups"+in, map[string]any{"name": "Desk"}, &desk)
	call("PUT", fmt.Sprintf("lineup/groups/%d/categories%s", desk.ID, in), map[string]any{"categories": []map[string]any{
		{"source_id": sources["main"], "name": "News"}, {"source_id": sources["two"], "name": "Extra"},
		{"source_id": sources["three"], "name": "Bulletins"}}}, nil)
	call("PATCH", fmt.Sprintf("lineup/groups/%d%s", desk.ID, in), map[string]any{"numbers_from": 100}, nil)
	var want []string
	for _, c := range slices.Concat(mainChannels, twoChannels, threeChannels) {
		if c.group == "News" || c.group == "Extra" || c.group == "Bulletins" {
			want = append(want, c.name)
		}
	}
	got := entriesOf(t, a, in)
	if names := slices.Sorted(maps.Keys(got)); !slices.Equal(names, slices.Sorted(slices.Values(want))) {
		t.Errorf("Mine holds %v, want %v", names, want)
	}
	last := 0
	for _, e := range got {
		if e.Group == nil || *e.Group != "Desk" || e.Number == nil || *e.Number < 100 {
			t.Errorf("in Mine: %+v", e)
		} else {
			last = max(last, *e.Number)
		}
	}
	if last != 100+len(want)-1 {
		t.Errorf("Desk's last number is %d, want %d", last, 100+len(want)-1)
	}

	// A channel that a source adds to one of them appears after a
	// refresh, numbered after its group's last.
	p.add("three", fakeChannel{key: "nb", name: "Night Bulletin", group: "Bulletins"})
	call("POST", fmt.Sprintf("sources/%d/refresh", sources["three"]), nil, nil)
	if e, ok := entriesOf(t, a, in)["Night Bulletin"]; !ok || e.Number == nil || *e.Number != last+1 || e.Group == nil || *e.Group != "Desk" {
		t.Errorf("the new channel: %+v", e)
	}

	// Numbers stay unique across each lineup.
	for _, l := range []string{"", in} {
		seen := map[int]string{}
		for name, e := range entriesOf(t, a, l) {
			if e.Number == nil {
				continue
			}
			if other, ok := seen[*e.Number]; ok {
				t.Errorf("lineup %q: %s and %s both have %d", l, other, name, *e.Number)
			}
			seen[*e.Number] = name
		}
	}

	// Mine is the account Guests's, with its playlist and guide, its
	// Xtream login, a box and a Stalker client.
	var guests idName
	call("POST", "accounts", map[string]any{"name": "Guests"}, &guests)
	acct := fmt.Sprintf("accounts/%d", guests.ID)
	var token struct {
		Token string `json:"token"`
	}
	call("PATCH", "settings", map[string]any{"outputs": true}, nil)
	call("PATCH", acct, map[string]any{"lineup_id": mine.ID, "outputs": map[string]bool{"m3u": true, "xtream": true}}, nil)
	call("PUT", acct+"/login", map[string]any{"username": guestUser, "password": guestPassword}, nil)
	call("POST", acct+"/token", nil, &token)
	b := newBox(base, boxMAC)
	s, err := b.session()
	if err != nil {
		t.Fatal(err)
	}
	code, _ := s["code"].(string)
	call("POST", "pairings/"+code+"/approve", map[string]any{"label": "Den"}, nil)
	call("PATCH", "devices/"+url.PathEscape(boxMAC), map[string]any{"account_id": guests.ID}, nil)
	if _, err := b.session(); err != nil || b.token == "" {
		t.Fatalf("the box: %v", err)
	}
	client := newStalker(base, clientMAC)
	_ = client.handshake()
	call("POST", "devices/"+url.PathEscape(clientMAC)+"/approve", map[string]any{"label": "Tablet"}, nil)
	call("PATCH", "devices/"+url.PathEscape(clientMAC), map[string]any{"account_id": guests.ID}, nil)
	if err := client.handshake(); err != nil {
		t.Fatal(err)
	}

	// A merged channel plays the copy of the source put first.
	var rule struct {
		ID int64 `json:"id"`
	}
	call("POST", "lineup/rules"+in, map[string]any{"match": map[string]any{}, "action": "merge", "copies": "guide",
		"sources": []int64{sources["two"]}}, &rule)
	shown := entriesOf(t, a, in)
	if e := shown["Harbour News Extra"]; e.Hidden || !shown["Harbour News"].Hidden || !shown["Harbour News HD"].Hidden {
		t.Errorf("the copy of two is not the one shown: %+v", shown)
	}
	if key := play(t, p, base, token.Token, shown["Harbour News Extra"].ID); key != "hnx" {
		t.Errorf("the merged channel played %q, want two's copy", key)
	}

	// Or the best copy seen: the 720p and the 1080i copies play once in
	// the household's lineup, through the debug playlist, and the merge
	// then prefers the best picture.
	adminToken, err := os.ReadFile(filepath.Join(data, "admin-token"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Harbour News", "Harbour News HD"} {
		readStream(t, fmt.Sprintf("%s/debug/play/%d?token=%s", base, household[name].ID, strings.TrimSpace(string(adminToken))))
	}
	deadline = time.Now().Add(30 * time.Second)
	for entriesOf(t, a, "")["Harbour News HD"].Health.Stream == nil {
		if time.Now().After(deadline) {
			t.Fatalf("the 1080i copy's details were not noted: %+v", entriesOf(t, a, "")["Harbour News HD"])
		}
		time.Sleep(200 * time.Millisecond)
	}
	call("PUT", fmt.Sprintf("lineup/rules/%d%s", rule.ID, in), map[string]any{"match": map[string]any{}, "action": "merge",
		"copies": "guide", "prefer": "best"}, nil)
	shown = entriesOf(t, a, in)
	if shown["Harbour News HD"].Hidden || !shown["Harbour News"].Hidden || !shown["Harbour News Extra"].Hidden {
		t.Errorf("the best copy is not the one shown: %+v", shown)
	}
	if s := shown["Harbour News HD"].Health.Stream; s == nil || s.Summary != "1080i H.264, AAC" {
		t.Errorf("the best copy's details: %+v", s)
	}
	if key := play(t, p, base, token.Token, shown["Harbour News HD"].ID); key != "hnhd" {
		t.Errorf("the merged channel played %q, want the best copy", key)
	}

	// The lineup's page names the accounts and outputs that serve it.
	page := adminPage(t, a, "/admin/lineup"+in)
	for _, w := range []string{"Seen by", ">Guests</a>, through 1 box, 1 Stalker client, its playlist and guide and its Xtream login"} {
		if !strings.Contains(page, w) {
			t.Errorf("Mine's page lacks %q", w)
		}
	}

	// Its playlist and guide, its Xtream login, the box and the Stalker
	// client show the same channels, with the same numbers.
	var lists = map[string][]string{}
	var playlist []byte
	if err := get(base, "/playlist/"+token.Token+".m3u", &playlist); err != nil {
		t.Fatal(err)
	}
	extinf := regexp.MustCompile(`(?m)^#EXTINF:-1 .*tvg-chno="(\d+)".*,(.*)$`)
	for _, m := range extinf.FindAllStringSubmatch(string(playlist), -1) {
		lists["playlist"] = append(lists["playlist"], m[1]+" "+m[2])
	}
	var guide []byte
	if err := get(base, "/epg/"+token.Token+".xml", &guide); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{} // the guide's channel IDs, by their numbers in the playlist
	for _, m := range regexp.MustCompile(`(?m)^#EXTINF:-1 tvg-id="(\d+)".*tvg-chno="(\d+)"`).FindAllStringSubmatch(string(playlist), -1) {
		ids[m[1]] = m[2]
	}
	for _, m := range regexp.MustCompile(`<channel id="(\d+)"><display-name>([^<]*)</display-name>`).FindAllStringSubmatch(string(guide), -1) {
		lists["guide"] = append(lists["guide"], ids[m[1]]+" "+m[2])
	}
	var live []struct {
		Num  any    `json:"num"`
		Name string `json:"name"`
	}
	login := "?username=" + guestUser + "&password=" + url.QueryEscape(guestPassword)
	if err := get(base, "/player_api.php"+login+"&action=get_live_streams", &live); err != nil {
		t.Fatal(err)
	}
	for _, c := range live {
		lists["Xtream login"] = append(lists["Xtream login"], fmt.Sprint(c.Num)+" "+c.Name)
	}
	var boxList struct {
		Channels []struct {
			Number int    `json:"number"`
			Name   string `json:"name"`
		} `json:"channels"`
	}
	if err := b.call("GET", "channels", nil, &boxList); err != nil {
		t.Fatal(err)
	}
	for _, c := range boxList.Channels {
		lists["box"] = append(lists["box"], fmt.Sprint(c.Number)+" "+c.Name)
	}
	var stalker struct {
		Data []struct {
			Number string `json:"number"`
			Name   string `json:"name"`
		} `json:"data"`
	}
	if err := client.call("itv", "get_all_channels", nil, &stalker); err != nil {
		t.Fatal(err)
	}
	for _, c := range stalker.Data {
		lists["Stalker client"] = append(lists["Stalker client"], c.Number+" "+c.Name)
	}
	var expected []string
	for name, e := range entriesOf(t, a, in) {
		if !e.Hidden && e.Number != nil {
			expected = append(expected, fmt.Sprint(*e.Number)+" "+name)
		}
	}
	slices.Sort(expected)
	// Every copy of Harbour News but one is hidden, and Night Bulletin
	// joined.
	if len(expected) != len(want)-2+1 {
		t.Errorf("Mine shows %v", expected)
	}
	for who, l := range lists {
		slices.Sort(l)
		if !slices.Equal(l, expected) {
			t.Errorf("%s shows %v, want %v", who, l, expected)
		}
	}
}

// A walk is this repository's server, built and started with the
// fictional provider's source main, and signed in as the administrator:
// where the walks through milestones' Done when start.
type walk struct {
	p          *provider
	base, data string
	bin, cfg   string
	log        string // the server's log file
	a          *admin
	srv        *instance
}

// startWalk builds and starts a walk's server, which stops, with the
// provider, when the test ends; its log is shown if the test failed.
func startWalk(t *testing.T) *walk {
	t.Helper()
	if testing.Short() {
		t.Skip("builds Overscan and runs it")
	}
	ctx := context.Background()
	dir := t.TempDir()
	w := &walk{bin: filepath.Join(dir, "overscan"), cfg: filepath.Join(dir, "overscan.yaml"), data: filepath.Join(dir, "data")}
	build := exec.Command("go", "build", "-o", w.bin, "overscan.gureet.ca/cmd/overscan")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		t.Fatalf("build Overscan: %v", err)
	}
	p, err := startProvider()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	w.p = p
	port, err := freePort()
	if err != nil {
		t.Fatal(err)
	}
	w.base = fmt.Sprintf("http://127.0.0.1:%d", port)
	logPath := filepath.Join(dir, "overscan.log")
	w.log = logPath
	if err := writeConfig(w.cfg, fmt.Sprintf("127.0.0.1:%d", port), w.data, p); err != nil {
		t.Fatal(err)
	}
	if w.srv, err = start(ctx, w.bin, w.cfg, w.base, logPath); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		w.srv.stop()
		if t.Failed() {
			log, _ := os.ReadFile(logPath)
			t.Logf("the server's log:\n%s", log)
		}
	})
	if _, err := command(w.bin, w.cfg, adminPassword+"\n", "admin", "password"); err != nil {
		t.Fatal(err)
	}
	if w.a, err = signIn(w.base, "admin", adminPassword); err != nil {
		t.Fatal(err)
	}
	if err := waitForSources(w.a, w.srv.started); err != nil {
		t.Fatal(err)
	}
	return w
}

// call calls the admin API, and fails the test if that fails.
func (w *walk) call(t *testing.T, method, path string, body, out any) {
	t.Helper()
	if err := w.a.call(method, path, body, out); err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
}

// Parameter sets that ffmpeg wrote for fictional test pictures at 25 a
// second, as internal/streaminfo's tests use them.
const (
	sps720p  = "6764001facd9405005bb011000000300100000030320f1831960"
	sps1080i = "67640028acd94078044fde0220000003002000000643e2c5b2c0"
)

// pictures returns a short MPEG-TS stream of H.264 pictures with the given
// parameter set, and AAC sound.
func pictures(t *testing.T, sps string) []byte {
	t.Helper()
	set, err := hex.DecodeString(sps)
	if err != nil {
		t.Fatal(err)
	}
	m := tstest.New()
	for i := range 10 {
		picture := append(append([]byte{0, 0, 0, 1}, set...), 0, 0, 0, 1, 0x65)
		m.Picture(int64(i)*tstest.Frame, append(picture, tstest.Content(i, "donewhen")...))
	}
	return m.Bytes()
}

// entry is a lineup's channel as the admin API gives it.
type entry struct {
	ID          int64   `json:"id"`
	ChannelID   int64   `json:"channel_id"`
	Source      string  `json:"source"`
	Name        string  `json:"name"`
	ChannelName string  `json:"channel_name"`
	Logo        *string `json:"logo"`
	Number      *int    `json:"number"`
	Group       *string `json:"group"`
	Hidden      bool    `json:"hidden"`
	LeftOut     bool    `json:"left_out"`
	Health      struct {
		Stream *struct {
			Summary string `json:"summary"`
		} `json:"stream"`
	} `json:"health"`
}

// entriesOf returns a lineup's channels by their names at their sources;
// query picks the lineup, "" for the household's.
func entriesOf(t *testing.T, a *admin, query string) map[string]entry {
	t.Helper()
	path := "lineup/channels?limit=1000"
	if query != "" {
		path += "&" + strings.TrimPrefix(query, "?")
	}
	var list struct {
		Channels []entry `json:"channels"`
	}
	if err := a.call("GET", path, nil, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]entry{}
	for _, e := range list.Channels {
		out[e.ChannelName] = e
	}
	return out
}

// play plays a channel of the account's playlist for a moment, and returns
// the key of the provider's stream that it asked for.
func play(t *testing.T, p *provider, base, token string, entryID int64) string {
	t.Helper()
	var playlist []byte
	if err := get(base, "/playlist/"+token+".m3u", &playlist); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(playlist), "\n")
	for i, line := range lines {
		if !strings.Contains(line, fmt.Sprintf(`tvg-id="%d"`, entryID)) {
			continue
		}
		for _, next := range lines[i+1:] {
			if next = strings.TrimSpace(next); next != "" && !strings.HasPrefix(next, "#") {
				before := len(p.asked())
				readStream(t, next)
				if asked := p.asked(); len(asked) > before {
					return asked[len(asked)-1]
				}
				return ""
			}
		}
	}
	t.Fatalf("the playlist has no channel %d", entryID)
	return ""
}

// readStream reads the first part of a stream, and lets it go.
func readStream(t *testing.T, address string) {
	t.Helper()
	req, err := http.NewRequest("GET", address, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s", req.URL.Path, resp.Status)
	}
	if _, err := io.CopyN(io.Discard, resp.Body, 256<<10); err != nil {
		t.Fatalf("%s: %v", req.URL.Path, err)
	}
}

// adminPage returns an admin page's HTML, as the signed-in administrator
// sees it.
func adminPage(t *testing.T, a *admin, path string) string {
	t.Helper()
	resp, err := a.http.Get(a.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("%s: %s, %v", path, resp.Status, err)
	}
	return string(body)
}
