// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The household that the old release sets up, through its own APIs: what a
// household sets up in the admin and on its boxes, as far as that release
// has it. Every name, address and password is made up.

const (
	adminPassword = "correct horse battery"
	boxMAC        = "00:1A:79:00:00:11" // runs Overscan's page
	clientMAC     = "00:1A:79:00:00:12" // a Stalker client
	blockedMAC    = "00:1A:79:00:00:13" // a box that the admin blocked
	guestUser     = "sam"
	guestPassword = "battery-staple-horse"
)

// features says what a release has, by its version.
type features struct {
	households bool // v0.4.0: profiles, end dates, messages, themes, the shared tuner
	hub        bool // v0.5.0: accounts with logins, lineups, rules, backups, outputs
	// playlists is v1.1.0's: the categories a source brings, lineups of
	// the household's own groups, groups' first numbers, merges by source
	// and by the best picture, and channels hidden until they play.
	playlists bool
	// guides is v1.2.0's: guides of the household's own (from its first
	// pre-release on), handGuides channels' guides chosen by hand (from
	// v1.2.0-alpha.2 on), logos logos set by hand and guides' logos
	// (from alpha.3 on), placeholders placeholder programmes (from
	// alpha.4 on), and pictures TMDB's pictures for providers' titles
	// (from alpha.5 on).
	guides, handGuides, logos, placeholders, pictures bool
}

func featuresOf(version string) features {
	v := strings.TrimPrefix(strings.TrimSpace(version), "v")
	major, minor := 99, 0 // "dev" and other builds of this repository have everything
	release, pre, _ := strings.Cut(v, "-")
	if parts := strings.SplitN(release, ".", 3); len(parts) == 3 {
		if m, err := strconv.Atoi(parts[0]); err == nil {
			major = m
		}
		minor, _ = strconv.Atoi(parts[1])
	}
	at := func(want int) bool { return major > 0 || minor >= want }
	from := func(wantMajor, wantMinor int) bool {
		return major > wantMajor || major == wantMajor && minor >= wantMinor
	}
	// An alpha of 1.2.0 has what the alphas before it brought.
	alpha, _ := strconv.Atoi(strings.TrimPrefix(pre, "alpha."))
	since := func(n int) bool {
		return from(1, 2) && !(major == 1 && minor == 2 && strings.HasPrefix(pre, "alpha.") && alpha < n)
	}
	return features{households: at(4), hub: at(5), playlists: from(1, 1), guides: from(1, 2), handGuides: since(2), logos: since(3),
		placeholders: since(4), pictures: since(5)}
}

// household holds what the setup made, by name, so that the probes can
// name the same things on every release.
type household struct {
	f        features
	p        *provider
	media    string // the folder source's folder
	sources  map[string]int64
	entries  map[string]int64 // the household lineup's channels, by the provider's name
	groups   map[string]int64
	films    map[string]int64 // the device API's films and series, by name
	episodes []int64
	guests   int64 // the account "Guests"
	kids     int64 // the lineup "Kids"
	mine     int64 // the lineup "Mine", of the household's own groups
	guide    int64 // the guide of the household's own, "extra"
	token    string
	box      *box
	client   *stalkerClient
	blocked  *box
}

type idName struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	ChannelName string `json:"channel_name"`
}

// lineupChannels returns the IDs of a lineup's channels by their names at
// their source.
func lineupChannels(a *admin, lineup int64) (map[string]int64, error) {
	path := "lineup/channels?limit=1000"
	if lineup != 0 {
		path += "&lineup=" + strconv.FormatInt(lineup, 10)
	}
	var list struct {
		Channels []idName `json:"channels"`
	}
	if err := a.call("GET", path, nil, &list); err != nil {
		return nil, err
	}
	out := map[string]int64{}
	for _, c := range list.Channels {
		name := c.ChannelName
		if name == "" {
			name = c.Name
		}
		out[name] = c.ID
	}
	return out, nil
}

// waitForSources waits until every enabled source has been read since
// since, and its guide, where it lists one, at least once: a server reads
// a guide again only once it is some hours old.
func waitForSources(a *admin, since time.Time) error {
	deadline := time.Now().Add(90 * time.Second)
	for {
		var list struct {
			Sources []struct {
				Name      string  `json:"name"`
				Enabled   bool    `json:"enabled"`
				Refreshed *string `json:"refreshed_at"`
				Error     string  `json:"refresh_error"`
				Guide     *struct {
					Listed bool    `json:"listed"`
					ReadAt *string `json:"read_at"`
				} `json:"guide"`
			} `json:"sources"`
		}
		if err := a.call("GET", "sources", nil, &list); err != nil {
			return err
		}
		waiting := ""
		for _, s := range list.Sources {
			if !s.Enabled {
				continue
			}
			if s.Error != "" {
				return fmt.Errorf("source %s: %s", s.Name, s.Error)
			}
			if !readSince(s.Refreshed, since) {
				waiting = s.Name
			} else if s.Guide != nil && s.Guide.Listed && s.Guide.ReadAt == nil {
				waiting = s.Name + "'s guide"
			}
		}
		if waiting == "" {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s was not read within 90 seconds", waiting)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func readSince(at *string, since time.Time) bool {
	if at == nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, *at)
	return err == nil && !t.Before(since.Truncate(time.Second))
}

// setUp makes the household through the admin's API, the device API and
// the Stalker API of the release that serves at base.
func (h *household) setUp(a *admin, base string, since time.Time) error {
	steps := []struct {
		name string
		run  func() error
	}{
		{"sources", func() error { return h.setUpSources(a, since) }},
		{"the lineup", func() error { return h.setUpLineup(a) }},
		{"devices", func() error { return h.setUpDevices(a, base) }},
		{"the box", func() error { return h.setUpBox(a) }},
		{"films and series", func() error { return h.setUpLibrary(a) }},
		{"recordings", func() error { return h.setUpRecordings(a) }},
	}
	if h.f.households {
		steps = append(steps, struct {
			name string
			run  func() error
		}{"profiles, the end date, messages and themes", func() error { return h.setUpHouseholds(a) }})
	}
	if h.f.hub {
		steps = append(steps, struct {
			name string
			run  func() error
		}{"accounts, lineups, rules and backups", func() error { return h.setUpHub(a) }})
	}
	if h.f.playlists {
		steps = append(steps, struct {
			name string
			run  func() error
		}{"what sources bring, own groups, first numbers and channels hidden until they play", func() error { return h.setUpPlaylists(a) }})
	}
	if h.f.guides {
		steps = append(steps, struct {
			name string
			run  func() error
		}{"a guide of the household's own", func() error { return h.setUpGuides(a) }})
	}
	if h.f.handGuides {
		steps = append(steps, struct {
			name string
			run  func() error
		}{"channels' guides chosen by hand", func() error { return h.setUpHandGuides(a) }})
	}
	if h.f.logos {
		steps = append(steps, struct {
			name string
			run  func() error
		}{"logos", func() error { return h.setUpLogos(a) }})
	}
	if h.f.placeholders {
		// Two-hour placeholders; the settings' probe sees that they stay.
		steps = append(steps, struct {
			name string
			run  func() error
		}{"placeholder programmes", func() error {
			return a.call("PATCH", "settings", map[string]any{"guide_placeholders": 120}, nil)
		}})
	}
	if h.f.pictures {
		// TMDB's pictures on, without a key: nothing is looked up, and
		// the settings' probe sees that the switch stays.
		steps = append(steps, struct {
			name string
			run  func() error
		}{"TMDB's pictures", func() error {
			return a.call("PATCH", "settings", map[string]any{"tmdb_pictures": true}, nil)
		}})
	}
	for _, s := range steps {
		if err := s.run(); err != nil {
			return fmt.Errorf("setting up %s: %w", s.name, err)
		}
	}
	return nil
}

func (h *household) setUpSources(a *admin, since time.Time) error {
	var list struct {
		Sources []idName `json:"sources"`
	}
	if err := a.call("GET", "sources", nil, &list); err != nil {
		return err
	}
	h.sources = map[string]int64{}
	for _, s := range list.Sources {
		h.sources[s.Name] = s.ID
	}
	if h.sources["main"] == 0 {
		return errors.New("the configuration file's source main is not there")
	}
	publicOnly := false
	for _, s := range []map[string]any{
		{"name": "two", "type": "m3u", "url": h.p.url + "/two.m3u", "max_connections": 2, "numbers": "provider"},
		{"name": "films", "type": "folder", "url": h.media},
	} {
		var added struct {
			ID         int64 `json:"id"`
			PublicOnly *bool `json:"public_only"` // from schema 43
		}
		if err := a.call("POST", "sources", s, &added); err != nil {
			return err
		}
		h.sources[s["name"].(string)] = added.ID
		publicOnly = publicOnly || added.PublicOnly != nil
	}
	if publicOnly {
		// A source kept to public addresses (D121), turned off, since
		// nothing on the internet answers the test.
		var added idName
		if err := a.call("POST", "sources", map[string]any{"name": "kept", "type": "m3u",
			"url": "http://provider.example/kept.m3u", "enabled": false, "public_only": true}, &added); err != nil {
			return err
		}
		h.sources[added.Name] = added.ID
	}
	if err := a.call("PATCH", fmt.Sprintf("sources/%d", h.sources["main"]),
		map[string]any{"refresh_seconds": 7200, "user_agent": "FictionalPlayer/1.0", "new_categories_hidden": true}, nil); err != nil {
		return err
	}
	return waitForSources(a, since)
}

func (h *household) setUpLineup(a *admin) error {
	var err error
	if h.entries, err = lineupChannels(a, 0); err != nil {
		return err
	}
	for _, name := range []string{"Harbour News", "Granite News", "Copper News", "Summit News", "Meadow Sport", "Willow Kids",
		"Lantern Films", "Orchard Films", "Harbour News Extra", "Meadow FM"} {
		if h.entries[name] == 0 {
			return fmt.Errorf("the lineup has no channel %s", name)
		}
	}
	var groups struct {
		Groups []idName `json:"groups"`
	}
	if err := a.call("GET", "lineup/groups", nil, &groups); err != nil {
		return err
	}
	h.groups = map[string]int64{}
	for _, g := range groups.Groups {
		h.groups[g.Name] = g.ID
	}
	var evening idName
	if err := a.call("POST", "lineup/groups", map[string]any{"name": "Evening"}, &evening); err != nil {
		return err
	}
	h.groups["Evening"] = evening.ID
	calls := []struct {
		method, path string
		body         any
	}{
		{"PATCH", fmt.Sprintf("lineup/groups/%d", h.groups["Sport"]), map[string]any{"name": "Sports Desk"}},
		{"PATCH", fmt.Sprintf("lineup/groups/%d", h.groups["Kids"]), map[string]any{"locked": true}},
		{"PATCH", fmt.Sprintf("lineup/groups/%d", h.groups["Shop"]), map[string]any{"hidden": true}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", h.entries["Harbour News"]), map[string]any{"name": "Kitchen News"}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", h.entries["Granite News"]), map[string]any{"number": 501}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", h.entries["Copper News"]), map[string]any{"visibility": "hidden"}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", h.entries["Summit News"]), map[string]any{"repair": true}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", h.entries["Meadow Sport"]), map[string]any{"group_id": evening.ID}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", h.entries["Willow Kids"]), map[string]any{"locked": true}},
		{"PATCH", "lineup/channels", map[string]any{"ids": []int64{h.entries["Lantern Films"], h.entries["Orchard Films"]}, "group_id": evening.ID}},
		{"PUT", "pin", map[string]any{"pin": "2468"}},
		{"PATCH", "settings", map[string]any{"repair_hls": true, "convert_sound": false, "adult_marks": "hide", "radio_apart": true,
			"recordings_cap": 1, "timeshift_minutes": 15}},
	}
	for _, c := range calls {
		if err := a.call(c.method, c.path, c.body, nil); err != nil {
			return err
		}
	}
	return nil
}

func (h *household) setUpDevices(a *admin, base string) error {
	h.box = newBox(base, boxMAC)
	s, err := h.box.session()
	if err != nil {
		return err
	}
	code, _ := s["code"].(string)
	if s["state"] != "pending" || code == "" {
		return fmt.Errorf("a new box got %v, not a pairing code", s)
	}
	if err := a.call("POST", "pairings/"+code+"/approve", map[string]any{"label": "Living room"}, nil); err != nil {
		return err
	}
	if s, err = h.box.session(); err != nil {
		return err
	}
	if s["state"] != "approved" || h.box.token == "" {
		return fmt.Errorf("the approved box got %v", s)
	}

	h.client = newStalker(base, clientMAC)
	if err := h.client.handshake(); !errors.Is(err, errPending) {
		return fmt.Errorf("a new Stalker client's handshake: %v, want it to wait for approval", err)
	}
	if err := a.call("POST", "devices/"+url.PathEscape(clientMAC)+"/approve", map[string]any{"label": "Bedroom"}, nil); err != nil {
		return err
	}
	if err := h.client.handshake(); err != nil {
		return fmt.Errorf("the approved Stalker client: %w", err)
	}

	h.blocked = newBox(base, blockedMAC)
	if _, err := h.blocked.session(); err != nil {
		return err
	}
	return a.call("POST", "devices/"+url.PathEscape(blockedMAC)+"/block", nil, nil)
}

func (h *household) setUpBox(a *admin) error {
	for _, name := range []string{"Harbour News", "Granite News"} {
		if err := h.box.call("PUT", fmt.Sprintf("channels/%d/favourite", h.entries[name]), nil, nil); err != nil {
			return err
		}
	}
	if err := h.box.call("POST", fmt.Sprintf("channels/%d/play", h.entries["Orchard Films"]), map[string]any{}, nil); err != nil {
		return err
	}
	return h.box.call("PATCH", "settings", map[string]any{"home": "picture", "clock": "12", "numbers": "groups", "groups": "ok"}, nil)
}

// shelf returns the titles of the device API's first shelf of kind, after
// waiting for the first read of the films and series.
func (h *household) titles(kind string) (map[string]int64, error) {
	deadline := time.Now().Add(60 * time.Second)
	for {
		var list struct {
			State   string `json:"state"`
			Error   string `json:"error"`
			Shelves []struct {
				ID string `json:"id"`
			} `json:"shelves"`
		}
		if err := h.box.call("GET", kind, nil, &list); err != nil {
			return nil, err
		}
		switch list.State {
		case "ready":
			out := map[string]int64{}
			for _, s := range list.Shelves {
				if s.ID == "continue" || s.ID == "recent" {
					continue
				}
				var shelf struct {
					Titles []idName `json:"titles"`
				}
				if err := h.box.call("GET", kind+"/shelves/"+s.ID, nil, &shelf); err != nil {
					return nil, err
				}
				for _, t := range shelf.Titles {
					out[t.Name] = t.ID
				}
			}
			return out, nil
		case "loading":
		default:
			return nil, fmt.Errorf("%s: %s %s", kind, list.State, list.Error)
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s were still loading after a minute", kind)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (h *household) setUpLibrary(a *admin) error {
	films, err := h.titles("movies")
	if err != nil {
		return err
	}
	series, err := h.titles("series")
	if err != nil {
		return err
	}
	h.films = map[string]int64{}
	for name, id := range films {
		h.films[name] = id
	}
	for name, id := range series {
		h.films[name] = id
	}
	for _, name := range []string{"Quiet Harbour", "Copper Field", "Silver Tide", "Coastline"} {
		if h.films[name] == 0 {
			return fmt.Errorf("the folder has no title %s: %v", name, h.films)
		}
	}
	var coastline struct {
		Seasons []struct {
			Episodes []struct {
				ID int64 `json:"id"`
			} `json:"episodes"`
		} `json:"seasons"`
	}
	if err := h.box.call("GET", fmt.Sprintf("series/%d", h.films["Coastline"]), nil, &coastline); err != nil {
		return err
	}
	for _, s := range coastline.Seasons {
		for _, e := range s.Episodes {
			h.episodes = append(h.episodes, e.ID)
		}
	}
	if len(h.episodes) != 3 {
		return fmt.Errorf("Coastline has %d episodes, want 3", len(h.episodes))
	}
	calls := []struct {
		method, path string
		body         any
	}{
		{"PUT", fmt.Sprintf("movies/%d/progress", h.films["Quiet Harbour"]), map[string]any{"position": 1260, "duration": 6730}},
		{"PUT", fmt.Sprintf("episodes/%d/progress", h.episodes[0]), map[string]any{"finished": true}},
		{"PUT", fmt.Sprintf("episodes/%d/progress", h.episodes[1]), map[string]any{"position": 600, "duration": 2700}},
	}
	for _, c := range calls {
		if err := h.box.call(c.method, c.path, c.body, nil); err != nil {
			return err
		}
	}
	var shelves struct {
		Shelves []idName `json:"shelves"`
	}
	if err := a.call("GET", "library/movies/shelves", nil, &shelves); err != nil {
		return err
	}
	if len(shelves.Shelves) == 0 {
		return errors.New("the admin lists no shelf of films")
	}
	for _, c := range []struct {
		path string
		body any
	}{
		{fmt.Sprintf("library/movies/shelves/%d", shelves.Shelves[0].ID), map[string]any{"name": "Our films"}},
		{fmt.Sprintf("library/titles/%d", h.films["Silver Tide"]), map[string]any{"hidden": true}},
		{fmt.Sprintf("library/titles/%d", h.films["Copper Field"]), map[string]any{"locked": true}},
	} {
		if err := a.call("PATCH", c.path, c.body, nil); err != nil {
			return err
		}
	}
	return nil
}

func (h *household) setUpRecordings(a *admin) error {
	// From the box, a programme three hours ahead; from the admin, one five
	// hours ahead, and one six hours ahead that is then called off.
	if err := h.box.call("POST", "recordings", map[string]any{"channel": h.entries["Granite News"], "start": h.p.programme(3).Unix()}, nil); err != nil {
		return err
	}
	for i, hours := range []int{5, 6} {
		var r idName
		if err := a.call("POST", "recordings", map[string]any{"entry_id": h.entries["Lantern Films"], "start": h.p.programme(hours).Format(time.RFC3339)}, &r); err != nil {
			return err
		}
		if i == 1 {
			if err := a.call("POST", fmt.Sprintf("recordings/%d/cancel", r.ID), nil, nil); err != nil {
				return err
			}
		}
	}
	return nil
}

func (h *household) setUpHouseholds(a *admin) error {
	var alex, sam idName
	if err := a.call("POST", "profiles", map[string]any{"name": "Alex"}, &alex); err != nil {
		return err
	}
	if err := a.call("POST", "profiles", map[string]any{"name": "Sam"}, &sam); err != nil {
		return err
	}
	theme := map[string]any{"overscan_theme": 1, "name": "Evening", "background": "#0e1512", "text": "#eef4f0", "accent": "#5bd69c",
		"focus": "#f4d35e", "focus_text": "#1a1405", "corners": "small", "brand": "The Den"}
	var installed idName
	if err := a.call("POST", "themes", theme, &installed); err != nil {
		return err
	}
	calls := []struct {
		method, path string
		body         any
	}{
		{"PATCH", fmt.Sprintf("profiles/%d", alex.ID), map[string]any{"pin": "1357", "theme_id": installed.ID}},
		{"PUT", "theme", map[string]any{"theme_id": installed.ID}},
		{"PATCH", "devices/" + url.PathEscape(boxMAC), map[string]any{"profile_id": sam.ID}},
		{"PUT", "expiry", map[string]any{"expires_at": "2030-01-01T00:00:00Z"}},
		{"POST", "messages", map[string]any{"text": "Dinner at eight", "mac": boxMAC}},
		{"POST", "messages", map[string]any{"text": "The server restarts on Sunday"}},
		{"PATCH", "settings", map[string]any{"shared_tuner": true, "recordings_until": "stopped", "recordings_max_hours": 3}},
	}
	for _, c := range calls {
		if err := a.call(c.method, c.path, c.body, nil); err != nil {
			return err
		}
	}
	// The box now plays as Sam, with favourites and a resume point of
	// Sam's own, and its own way of ending recordings.
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PUT", fmt.Sprintf("channels/%d/favourite", h.entries["Meadow Sport"]), nil},
		{"PUT", fmt.Sprintf("movies/%d/progress", h.films["Copper Field"]), map[string]any{"position": 3000, "duration": 5400}},
		{"PATCH", "settings", map[string]any{"record": "programme"}},
		{"POST", "recordings", map[string]any{"channel": h.entries["Willow Sport"], "start": h.p.programme(8).Unix(), "stop": h.p.programme(8).Add(45 * time.Minute).Unix()}},
	} {
		if err := h.box.call(c.method, c.path, c.body, nil); err != nil {
			return err
		}
	}
	// A recording of a few seconds, which makes a file in the recordings
	// folder.
	var rec struct {
		ID int64 `json:"id"`
	}
	if err := h.box.call("POST", "recordings", map[string]any{"channel": h.entries["Orchard Films"], "now": true}, &rec); err != nil {
		return err
	}
	time.Sleep(4 * time.Second)
	if err := h.box.call("DELETE", fmt.Sprintf("recordings/%d", rec.ID), nil, nil); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		var list struct {
			Recent []struct {
				ID    int64  `json:"id"`
				State string `json:"state"`
				Size  int64  `json:"size"`
				Error string `json:"error"`
			} `json:"recent"`
		}
		if err := h.box.call("GET", "recordings", nil, &list); err != nil {
			return err
		}
		for _, r := range list.Recent {
			if r.ID == rec.ID && r.State == "done" && r.Size > 0 {
				return nil
			}
			if r.ID == rec.ID && r.State == "failed" {
				return fmt.Errorf("the recording failed: %s", r.Error)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the recording was not done with a file within 30 seconds: %+v", list.Recent)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (h *household) setUpHub(a *admin) error {
	var guests, kids idName
	if err := a.call("POST", "accounts", map[string]any{"name": "Guests"}, &guests); err != nil {
		return err
	}
	h.guests = guests.ID
	if err := a.call("POST", "lineups", map[string]any{"name": "Kids", "copy_of": 1}, &kids); err != nil {
		return err
	}
	h.kids = kids.ID
	var token struct {
		Token string `json:"token"`
	}
	acct := fmt.Sprintf("accounts/%d", h.guests)
	kidsRules := "lineup/rules?lineup=" + strconv.FormatInt(h.kids, 10)
	calls := []struct {
		method, path string
		body, out    any
	}{
		{"PATCH", acct, map[string]any{"max_streams": 2, "outputs": map[string]bool{"m3u": true, "xtream": true}, "lineup_id": h.kids,
			"output_profile": "h264-720p", "sources": []int64{h.sources["main"], h.sources["films"]}}, nil},
		{"PUT", acct + "/login", map[string]any{"username": guestUser, "password": guestPassword}, nil},
		{"POST", acct + "/token", nil, &token},
		{"PUT", acct + "/expiry", map[string]any{"expires_at": "2031-06-30T00:00:00Z"}, nil},
		{"PUT", acct + "/pin", map[string]any{"pin": "9753"}, nil},
		{"PATCH", "settings", map[string]any{"outputs": true}, nil},
		{"POST", kidsRules, map[string]any{"match": map[string]any{"group": "Sport*"}, "action": "exclude"}, nil},
		{"POST", kidsRules, map[string]any{"match": map[string]any{"group": "Kids"}, "action": "renumber", "from": 100}, nil},
		{"POST", kidsRules, map[string]any{"match": map[string]any{"name": "Harbour*"}, "action": "rename", "replace": " HD", "with": ""}, nil},
		{"POST", "lineup/rules", map[string]any{"match": map[string]any{}, "action": "merge", "copies": "guide"}, nil},
		{"POST", "lineup/rules", map[string]any{"match": map[string]any{"name": "* +1"}, "action": "guide", "replace": " +1", "with": ""}, nil},
		{"POST", "lineup/rules", map[string]any{"match": map[string]any{"name": "* +1"}, "action": "timeshift", "minutes": 60}, nil},
		{"PUT", fmt.Sprintf("lineup/channels/%d/backups", h.entries["Granite News"]), map[string]any{"entries": []int64{h.entries["Harbour News Extra"]}}, nil},
		{"PATCH", "devices/" + url.PathEscape(clientMAC), map[string]any{"account_id": h.guests}, nil},
		{"PATCH", "devices/" + url.PathEscape(boxMAC), map[string]any{"output_profile": "h264-1080p"}, nil},
	}
	for _, c := range calls {
		if err := a.call(c.method, c.path, c.body, c.out); err != nil {
			return err
		}
	}
	h.token = token.Token
	if h.token == "" {
		return errors.New("the account's new token is empty")
	}
	return nil
}

// setUpPlaylists sets up what v1.1.0 brought: a source that brings only
// its ticked categories (D135), a lineup of the household's own groups
// whose group takes categories of two sources (D136) and numbers from 300
// (D137), a merge that puts a source first and prefers the best picture,
// and a channel hidden until it plays again (D138).
func (h *household) setUpPlaylists(a *admin) error {
	var channels struct {
		Channels []struct {
			ChannelID   int64  `json:"channel_id"`
			ChannelName string `json:"channel_name"`
		} `json:"channels"`
	}
	if err := a.call("GET", "lineup/channels?limit=1000", nil, &channels); err != nil {
		return err
	}
	var summit int64
	for _, c := range channels.Channels {
		if c.ChannelName == "Summit News" {
			summit = c.ChannelID
		}
	}
	if summit == 0 {
		return errors.New("the lineup has no channel Summit News")
	}
	var mine, desk idName
	if err := a.call("POST", "lineups", map[string]any{"name": "Mine", "own_groups": true}, &mine); err != nil {
		return err
	}
	h.mine = mine.ID
	in := "?lineup=" + strconv.FormatInt(h.mine, 10)
	if err := a.call("POST", "lineup/groups"+in, map[string]any{"name": "Desk"}, &desk); err != nil {
		return err
	}
	calls := []struct {
		method, path string
		body         any
	}{
		{"PUT", fmt.Sprintf("sources/%d/categories/live", h.sources["two"]),
			map[string]any{"only_ticked": true, "tick_new": true, "ticked": map[string]bool{"Extra": true}}},
		{"PUT", fmt.Sprintf("lineup/groups/%d/categories%s", desk.ID, in), map[string]any{"categories": []map[string]any{
			{"source_id": h.sources["main"], "name": "News"}, {"source_id": h.sources["two"], "name": "Extra"}}}},
		{"PATCH", fmt.Sprintf("lineup/groups/%d%s", desk.ID, in), map[string]any{"numbers_from": 300}},
		{"POST", "lineup/rules" + in, map[string]any{"match": map[string]any{}, "action": "merge", "copies": "guide", "prefer": "best",
			"sources": []int64{h.sources["two"], h.sources["main"]}}},
		{"PUT", fmt.Sprintf("health/%d", summit), map[string]any{"hidden_until_played": true}},
	}
	for _, c := range calls {
		if err := a.call(c.method, c.path, c.body, nil); err != nil {
			return err
		}
	}
	return nil
}

// setUpGuides adds a guide of the household's own (D139), which fills the
// channels that their sources' guide leaves out, by name, and waits for its
// first read.
func (h *household) setUpGuides(a *admin) error {
	var added idName
	if err := a.call("POST", "guides", map[string]any{"name": "extra", "address": h.p.url + "/extra.xml"}, &added); err != nil {
		return err
	}
	h.guide = added.ID
	deadline := time.Now().Add(60 * time.Second)
	for {
		var g struct {
			Reading   bool    `json:"reading"`
			ReadAt    *string `json:"read_at"`
			ReadError string  `json:"read_error"`
			Fills     int     `json:"fills"`
			Waiting   int     `json:"waiting"`
		}
		if err := a.call("GET", fmt.Sprintf("guides/%d", h.guide), nil, &g); err != nil {
			return err
		}
		switch {
		case g.ReadError != "":
			return fmt.Errorf("the guide extra: %s", g.ReadError)
		case g.ReadAt != nil && !g.Reading && g.Waiting == 0:
			if g.Fills != len(extraFills) {
				return fmt.Errorf("the guide extra fills %d channels, not %d", g.Fills, len(extraFills))
			}
			return nil
		case time.Now().After(deadline):
			return errors.New("the guide extra was not read within 60 seconds")
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// setUpHandGuides chooses channels' guides by hand (D140): Juniper Shop
// takes the guide of its own's Granite News, Granite Kids +1 that of
// Granite Kids, and Meadow Sport none; and waits for the guide's read that
// the first brings.
func (h *household) setUpHandGuides(a *admin) error {
	var channels struct {
		Channels []struct {
			ChannelID   int64  `json:"channel_id"`
			ChannelName string `json:"channel_name"`
		} `json:"channels"`
	}
	if err := a.call("GET", "lineup/channels?limit=1000", nil, &channels); err != nil {
		return err
	}
	ids := map[string]int64{}
	for _, c := range channels.Channels {
		ids[c.ChannelName] = c.ChannelID
	}
	var found struct {
		Options []struct {
			GuideChannelID int64  `json:"guide_channel_id"`
			Guide          string `json:"guide"`
		} `json:"options"`
	}
	if err := a.call("GET", "guide-channels?find="+url.QueryEscape("Granite News"), nil, &found); err != nil {
		return err
	}
	var granite int64
	for _, o := range found.Options {
		if o.Guide == "extra" {
			granite = o.GuideChannelID
		}
	}
	for _, name := range []string{"Juniper Shop", "Granite Kids +1", "Granite Kids", "Meadow Sport"} {
		if ids[name] == 0 {
			return fmt.Errorf("the lineup has no channel %s", name)
		}
	}
	if granite == 0 {
		return errors.New("the guide extra lists no Granite News")
	}
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"Juniper Shop", map[string]any{"guide_channel_id": granite}},
		{"Granite Kids +1", map[string]any{"channel_id": ids["Granite Kids"]}},
		{"Meadow Sport", map[string]any{"none": true}},
	} {
		if err := a.call("PUT", fmt.Sprintf("channel-guides/%d", ids[c.name]), c.body, nil); err != nil {
			return err
		}
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		var g struct {
			Reading bool `json:"reading"`
			Fills   int  `json:"fills"`
			ByHand  int  `json:"by_hand"`
			Waiting int  `json:"waiting"`
		}
		if err := a.call("GET", fmt.Sprintf("guides/%d", h.guide), nil, &g); err != nil {
			return err
		}
		if !g.Reading && g.Waiting == 0 && g.ByHand == 1 && g.Fills == len(extraFills)+1 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the guide extra did not read Granite News for Juniper Shop within 60 seconds: %+v", g)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// setUpLogos sets logos (D141): Cobalt Extra's by hand, none for Willow
// Kids, the guide of the household's own's icons before channels' own, and
// the source main's guide's icons where a channel's own is missing.
func (h *household) setUpLogos(a *admin) error {
	var channels struct {
		Channels []struct {
			ID          int64  `json:"id"`
			ChannelName string `json:"channel_name"`
		} `json:"channels"`
	}
	if err := a.call("GET", "lineup/channels?limit=1000", nil, &channels); err != nil {
		return err
	}
	ids := map[string]int64{}
	for _, c := range channels.Channels {
		ids[c.ChannelName] = c.ID
	}
	if ids["Cobalt Extra"] == 0 || ids["Willow Kids"] == 0 {
		return errors.New("the lineup lacks Cobalt Extra or Willow Kids")
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", fmt.Sprintf("lineup/channels/%d", ids["Cobalt Extra"]), map[string]any{"logo": h.p.url + "/logo/by-hand.png"}},
		{"PATCH", fmt.Sprintf("lineup/channels/%d", ids["Willow Kids"]), map[string]any{"logo": ""}},
		{"PATCH", fmt.Sprintf("guides/%d", h.guide), map[string]any{"logos": "first"}},
		{"PATCH", fmt.Sprintf("sources/%d", h.sources["main"]), map[string]any{"guide_logos": "fill"}},
	} {
		if err := a.call(c.method, c.path, c.body, nil); err != nil {
			return err
		}
	}
	return nil
}

// A probe is one request whose answer the upgrade must keep.
type probe struct {
	name string
	get  func() (any, error)
}

// probes lists what the household's admin, box, Stalker client and players
// see, as far as the old release has it.
func (h *household) probes(a *admin, base string) []probe {
	var out []probe
	adminGet := func(path string) {
		out = append(out, probe{"admin GET " + path, func() (any, error) {
			var v any
			return v, a.call("GET", path, nil, &v)
		}})
	}
	boxGet := func(b *box, who, path string) {
		out = append(out, probe{who + " GET " + path, func() (any, error) {
			var v any
			return v, b.call("GET", path, nil, &v)
		}})
	}
	for _, p := range []string{"sources", "lineup/groups", "lineup/channels?limit=1000", "pin", "settings", "devices", "recordings",
		"library/movies/shelves", "library/series/shelves", "library/movies/titles?limit=1000", "library/series/titles?limit=1000"} {
		adminGet(p)
	}
	if h.f.households {
		for _, p := range []string{"profiles", "expiry", "themes", "messages"} {
			adminGet(p)
		}
	}
	if h.f.hub {
		kids := strconv.FormatInt(h.kids, 10)
		for _, p := range []string{"accounts", fmt.Sprintf("accounts/%d", h.guests), fmt.Sprintf("accounts/%d/token", h.guests), "lineups",
			"lineup/rules", "lineup/groups?lineup=" + kids, "lineup/channels?limit=1000&lineup=" + kids, "lineup/rules?lineup=" + kids,
			fmt.Sprintf("lineup/channels/%d/backups", h.entries["Granite News"])} {
			adminGet(p)
		}
	}
	if h.f.playlists {
		mine := strconv.FormatInt(h.mine, 10)
		for _, p := range []string{"health", fmt.Sprintf("sources/%d/categories/live", h.sources["two"]), "lineup/groups?lineup=" + mine,
			"lineup/channels?limit=1000&lineup=" + mine, "lineup/rules?lineup=" + mine} {
			adminGet(p)
		}
	}

	if h.f.guides {
		for _, p := range []string{"guides", fmt.Sprintf("guides/%d", h.guide)} {
			adminGet(p)
		}
	}
	if h.f.handGuides {
		// The lists say where each channel's programmes come from, without
		// what is on, which changes by itself.
		for _, p := range []string{"channel-guides?limit=500", "channel-guides?show=by_hand"} {
			adminGet(p)
		}
	}

	// The box: its session, with the secret it kept, and what it shows.
	out = append(out, probe{"box POST session", func() (any, error) { return h.box.session() }})
	for _, p := range []string{"channels", "settings", "recordings", "movies", "series",
		fmt.Sprintf("movies/%d", h.films["Quiet Harbour"]), fmt.Sprintf("movies/%d", h.films["Copper Field"]),
		fmt.Sprintf("series/%d", h.films["Coastline"])} {
		boxGet(h.box, "box", p)
	}
	if h.f.households {
		boxGet(h.box, "box", "profiles")
	}
	out = append(out, probe{"blocked box POST session", func() (any, error) { return h.blocked.session() }})

	// The Stalker client.
	out = append(out, probe{"Stalker client handshake", func() (any, error) { return nil, h.client.handshake() }})
	for _, call := range [][2]string{{"itv", "get_genres"}, {"itv", "get_all_channels"}, {"account_info", "get_main_info"}, {"vod", "get_categories"}, {"series", "get_categories"}} {
		out = append(out, probe{"Stalker client " + call[0] + "/" + call[1], func() (any, error) {
			var v any
			return v, h.client.call(call[0], call[1], nil, &v)
		}})
	}

	// Players, through the outputs.
	if h.f.hub {
		login := "?username=" + guestUser + "&password=" + url.QueryEscape(guestPassword)
		for _, action := range []string{"", "get_live_categories", "get_live_streams", "get_vod_categories", "get_vod_streams", "get_series"} {
			path := "/player_api.php" + login
			if action != "" {
				path += "&action=" + action
			}
			out = append(out, probe{"player GET player_api.php action=" + action, func() (any, error) {
				var v any
				return v, get(base, path, &v)
			}})
		}
		out = append(out, probe{"player GET the account's playlist", func() (any, error) {
			var text []byte
			if err := get(base, "/playlist/"+h.token+".m3u", &text); err != nil {
				return nil, err
			}
			return strings.Split(strings.TrimSpace(string(text)), "\n"), nil
		}})
	}
	return out
}

// snapshot takes every probe's answer, after signing in again as each
// device would after a restart.
func (h *household) snapshot(a *admin, base string) (map[string]any, []string, error) {
	h.box.base, h.client.base, h.blocked.base = base, base, base
	answers := map[string]any{}
	var order []string
	for _, p := range h.probes(a, base) {
		v, err := p.get()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p.name, err)
		}
		// Round-trip through JSON, so that every answer has the same types.
		data, err := json.Marshal(v)
		if err != nil {
			return nil, nil, err
		}
		var norm any
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		if err := dec.Decode(&norm); err != nil {
			return nil, nil, err
		}
		answers[p.name] = norm
		order = append(order, p.name)
	}
	return answers, order, nil
}
