// SPDX-License-Identifier: AGPL-3.0-or-later

package source

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestCheck(t *testing.T) {
	good := Settings{Name: "main", URL: "https://provider.example/get.php?username=u&password=s3cret"}.WithDefaults()
	if good.Type != TypeM3U || good.Refresh != DefaultRefresh || good.Numbers != NumbersPlaylist {
		t.Fatalf("defaults %+v", good)
	}
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []func(*Settings){
		func(s *Settings) { s.URL = "/srv/playlists/main.m3u" },
		func(s *Settings) { s.URL = "http://192.0.2.10:8080/list.m3u" },
		func(s *Settings) { s.MaxConnections = 0 },
		func(s *Settings) { s.Refresh = MinRefresh },
		func(s *Settings) { s.UserAgent = "TestPlayer/1.0 (Linux)" },
		func(s *Settings) { s.Numbers = NumbersProvider },
		func(s *Settings) { s.Name = "0-a_b" },
	} {
		s := good
		ok(&s)
		if err := s.Check(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	for field, bad := range map[string]func(*Settings){
		"name":            func(s *Settings) { s.Name = "Main" },
		"type":            func(s *Settings) { s.Type = "tvheadend" },
		"mac":             func(s *Settings) { s.Device.MAC = "00:1A:79:00:00:51" },
		"username":        func(s *Settings) { s.Username = "u" },
		"url":             func(s *Settings) { s.URL = "ftp://provider.example/s3cret" },
		"refresh":         func(s *Settings) { s.Refresh = time.Minute },
		"max_connections": func(s *Settings) { s.MaxConnections = -1 },
		"user_agent":      func(s *Settings) { s.UserAgent = "a\nb" },
		"numbers":         func(s *Settings) { s.Numbers = "tvg" },
	} {
		s := good
		bad(&s)
		err := s.Check()
		var e *Error
		if !errors.As(err, &e) || e.Field != field || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: %v", field, err)
		}
	}
	for _, url := range []string{"", "playlists/main.m3u", "/srv/../etc/passwd", "http:///s3cret", "http://provider.example/\x00",
		"mailto:someone@provider.example", strings.Repeat("a", 5000)} {
		s := good
		s.URL = url
		if s.Check() == nil {
			t.Errorf("address %q taken", url)
		}
	}
}

func TestCheckStalker(t *testing.T) {
	good := Settings{Name: "portal", Type: TypeStalker, URL: "http://portal.example/stalker_portal/c/",
		Device: Device{MAC: "00:1A:79:00:00:51"}}.WithDefaults()
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	if good.Host() != "portal.example" || !good.HasChannels() || DefaultLimit(TypeStalker) != 1 {
		t.Errorf("host %q, channels %v", good.Host(), good.HasChannels())
	}
	if ref, err := good.StreamURL("ffrt http://localhost/ch/101_"); err != nil || ref != "ffrt http://localhost/ch/101_" {
		t.Errorf("stream %q, %v", ref, err)
	}
	for _, ok := range []func(*Settings){
		func(s *Settings) { s.URL = "https://portal.example:8443/c/" },
		func(s *Settings) { s.URL = "http://portal.example/portal.php" },
		func(s *Settings) { s.Device.MAC = "00-1a-79-00-00-51" },
		func(s *Settings) {
			s.Device = Device{MAC: "00:1A:79:00:00:51", Serial: "FICTIONAL0001", DeviceID: strings.Repeat("A", 64),
				DeviceID2: strings.Repeat("B", 64), Signature: "made-up", Model: "MAG322"}
		},
		func(s *Settings) { s.Username, s.Password = "viewer", "s3cret" },
		func(s *Settings) { s.Username = "viewer" },
	} {
		s := good
		ok(&s)
		if err := s.Check(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	for field, bad := range map[string]func(*Settings){
		"url":      func(s *Settings) { s.URL = "http://u:s3cret@portal.example/c/" },
		"mac":      func(s *Settings) { s.Device.MAC = "" },
		"device":   func(s *Settings) { s.Device.Serial = "a\ns3cret" },
		"username": func(s *Settings) { s.Password = "s3cret" },
	} {
		s := good
		bad(&s)
		err := s.Check()
		var e *Error
		if !errors.As(err, &e) || e.Field != field || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: %v", field, err)
		}
	}
	for _, mac := range []string{"00:1A:79:00:00", "00:1A:79:00:00:5G", "001A79000051", "00:1A:79:00:00:51:00", "00:1A-79:00:00;51"} {
		if ValidMAC(mac) {
			t.Errorf("%q taken as a MAC address", mac)
		}
	}
	for _, url := range []string{"http://portal.example/c/?mac=1", "ftp://portal.example/c/", "http:///c/", "portal.example/c/"} {
		s := good
		s.URL = url
		if s.Check() == nil {
			t.Errorf("address %q taken", url)
		}
	}
}

func TestHost(t *testing.T) {
	for address, want := range map[string]string{
		"http://user:s3cret@provider.example:8080/get.php?password=s3cret": "provider.example:8080",
		"https://provider.example/list.m3u":                                "provider.example",
		"/srv/playlists/main.m3u":                                          "file main.m3u",
	} {
		if got := Host(address); got != want {
			t.Errorf("Host(%q) = %q, want %q", address, got, want)
		}
	}
}

func TestCheckXtream(t *testing.T) {
	good := Settings{Name: "main", Type: TypeXtream, URL: "http://provider.example:8080", Username: "viewer",
		Password: "s3cret"}.WithDefaults()
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	for _, ok := range []func(*Settings){
		func(s *Settings) { s.URL = "https://provider.example/" },
		func(s *Settings) { s.URL = "http://192.0.2.10:25461/panel" },
		func(s *Settings) { s.Password = "p@ss/word with spaces&more" },
	} {
		s := good
		ok(&s)
		if err := s.Check(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	for field, bad := range map[string]func(*Settings){
		"url":      func(s *Settings) { s.URL = "/srv/playlists/main.m3u" },
		"username": func(s *Settings) { s.Username = "" },
		"password": func(s *Settings) { s.Password = "s3cret\n" },
	} {
		s := good
		bad(&s)
		err := s.Check()
		var e *Error
		if !errors.As(err, &e) || e.Field != field || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: %v", field, err)
		}
	}
	for _, url := range []string{
		"http://provider.example:8080/get.php?username=viewer&password=s3cret",
		"http://provider.example:8080/player_api.php",
		"http://viewer:s3cret@provider.example:8080",
		"http://provider.example:8080/#top",
		"ftp://provider.example",
	} {
		s := good
		s.URL = url
		err := s.Check()
		var e *Error
		if !errors.As(err, &e) || e.Field != "url" || strings.Contains(err.Error(), "s3cret") {
			t.Errorf("address %q: %v", url, err)
		}
	}
	if good.IsFile() {
		t.Error("an Xtream source is a file")
	}
	long := good
	long.Password = strings.Repeat("p", maxLogin+1)
	if long.Check() == nil {
		t.Error("a long password taken")
	}
}

func TestStreamURL(t *testing.T) {
	m3u := Settings{Name: "main", URL: "/srv/main.m3u"}.WithDefaults()
	if got, err := m3u.StreamURL("http://cdn.example/1.ts"); err != nil || got != "http://cdn.example/1.ts" {
		t.Errorf("M3U: %q, %v", got, err)
	}
	x := Settings{Name: "main", Type: TypeXtream, URL: "http://provider.example:8080/", Username: "view er",
		Password: "p/ss?#%"}
	got, err := x.StreamURL("live/1234.ts")
	if want := "http://provider.example:8080/live/view%20er/p%2Fss%3F%23%25/1234.ts"; err != nil || got != want {
		t.Errorf("Xtream: %q, %v; want %q", got, err, want)
	}
	for _, ref := range []string{"", "1234.ts", "live/", "/1234.ts", "live/a/b.ts"} {
		if got, err := x.StreamURL(ref); err == nil || strings.Contains(err.Error(), "p/ss") {
			t.Errorf("%q: %q, %v", ref, got, err)
		}
	}
	j := Settings{Name: "den", Type: TypeJellyfin, URL: "http://jellyfin.example:8096/media/", Password: "k&y"}
	for ref, want := range map[string]string{
		"movie/4f2c.mkv": "http://jellyfin.example:8096/media/Videos/4f2c/stream.mkv?ApiKey=k%26y&static=true",
		"series/e1":      "http://jellyfin.example:8096/media/Videos/e1/stream?ApiKey=k%26y&static=true",
	} {
		if got, err := j.StreamURL(ref); err != nil || got != want {
			t.Errorf("Jellyfin %s: %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := j.StreamURL("4f2c.mkv"); err == nil {
		t.Error("Jellyfin without a kind")
	}
}

func TestTranscodeURL(t *testing.T) {
	j := Settings{Name: "den", Type: TypeJellyfin, URL: "http://jellyfin.example:8096/media/", Password: "k&y"}
	for ref, want := range map[string]string{
		"movie/4f2c.mkv": "http://jellyfin.example:8096/media/Videos/4f2c/stream.ts?ApiKey=k%26y",
		"series/e1":      "http://jellyfin.example:8096/media/Videos/e1/stream.ts?ApiKey=k%26y",
	} {
		if got, err := j.TranscodeURL(ref); err != nil || got != want {
			t.Errorf("Jellyfin %s: %q, %v; want %q", ref, got, err, want)
		}
	}
	if _, err := j.TranscodeURL("4f2c.mkv"); err == nil {
		t.Error("Jellyfin without a kind")
	}
	x := Settings{Name: "main", Type: TypeXtream, URL: "http://provider.example:8080/", Username: "u", Password: "p"}
	if got, err := x.TranscodeURL("movie/1.mkv"); err != nil || got != "" {
		t.Errorf("Xtream transcodes: %q, %v", got, err)
	}
}

func TestJellyfinSettings(t *testing.T) {
	good := Settings{Name: "den", Type: TypeJellyfin, URL: "http://jellyfin.example:8096", Password: "0123456789abcdef"}.WithDefaults()
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	if good.HasChannels() || !good.HasLibrary() || good.IsFile() {
		t.Error("a Jellyfin source has a library and no channels")
	}
	withUser := good
	withUser.Username = "Anna"
	if err := withUser.Check(); err != nil {
		t.Error(err)
	}
	for field, s := range map[string]Settings{
		"url":      {Name: "den", Type: TypeJellyfin, URL: "/srv/jellyfin", Password: "k"},
		"password": {Name: "den", Type: TypeJellyfin, URL: "http://jellyfin.example"},
		"username": {Name: "den", Type: TypeJellyfin, URL: "http://jellyfin.example", Password: "k", Username: "a\x00b"},
	} {
		var serr *Error
		if err := s.WithDefaults().Check(); !errors.As(err, &serr) || serr.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	bad := good
	bad.Password = "two words"
	if err := bad.Check(); err == nil {
		t.Error("a key with a space")
	}
	xt := Settings{Type: TypeXtream}
	m3u := Settings{Type: TypeM3U, URL: "/srv/a.m3u"}
	if !xt.HasChannels() || !xt.HasLibrary() || !m3u.HasChannels() || m3u.HasLibrary() || !m3u.IsFile() {
		t.Error("Xtream and M3U")
	}
	if DefaultLimit(TypeJellyfin) != 0 || DefaultLimit(TypeXtream) != 1 || DefaultLimit("") != 1 {
		t.Error("default limits")
	}
}

func TestPlexSettings(t *testing.T) {
	good := Settings{Name: "den", Type: TypePlex, URL: "http://plex.example:32400", Password: "fictional-token"}.WithDefaults()
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	if good.HasChannels() || !good.HasLibrary() || good.IsFile() || DefaultLimit(TypePlex) != 0 {
		t.Error("a Plex source has a library, no channels and no limit")
	}
	for field, s := range map[string]Settings{
		"url":      {Name: "den", Type: TypePlex, URL: "/srv/plex", Password: "t"},
		"password": {Name: "den", Type: TypePlex, URL: "http://plex.example:32400", Password: "two words"},
		"username": {Name: "den", Type: TypePlex, URL: "http://plex.example:32400", Password: "t", Username: "anna"},
	} {
		var serr *Error
		if err := s.WithDefaults().Check(); !errors.As(err, &serr) || serr.Field != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	p := Settings{Name: "den", Type: TypePlex, URL: "http://plex.example:32400/", Password: "t&k"}
	part := url.PathEscape("/library/parts/12/1700000000/file.mkv")
	if got, err := p.StreamURL("movie/" + part); err != nil ||
		got != "http://plex.example:32400/library/parts/12/1700000000/file.mkv?X-Plex-Token=t%26k" {
		t.Errorf("Plex film: %q, %v", got, err)
	}
	for _, bad := range []string{"movie/12.mkv", "movie/" + url.PathEscape("/library/metadata/12"),
		"movie/" + url.PathEscape("/library/parts/12/../../x"), "movie/" + url.PathEscape("/library/parts/1?a=b"),
		"movie/%zz", part} {
		if got, err := p.StreamURL(bad); err == nil {
			t.Errorf("Plex %q: %q", bad, got)
		}
	}
	if got, err := p.TranscodeURL("movie/" + part); err != nil || got != "" {
		t.Errorf("Plex transcodes: %q, %v", got, err)
	}
}

func TestFolderSettings(t *testing.T) {
	good := Settings{Name: "films", Type: TypeFolder, URL: "/srv/media/Films"}.WithDefaults()
	if err := good.Check(); err != nil {
		t.Fatal(err)
	}
	if good.HasChannels() || !good.HasLibrary() || good.IsFile() || good.Host() != "Films" || DefaultLimit(TypeFolder) != 0 {
		t.Error("a folder has a library and no channels")
	}
	if _, err := good.StreamURL("movie/a.mkv"); err == nil {
		t.Error("a folder's stream address")
	}
	for field, s := range map[string]Settings{
		"url":      {Name: "f", Type: TypeFolder, URL: "srv/media"},
		"url ":     {Name: "f", Type: TypeFolder, URL: "/"},
		"url  ":    {Name: "f", Type: TypeFolder, URL: "/srv/../etc"},
		"url   ":   {Name: "f", Type: TypeFolder, URL: "http://example.com/films"},
		"username": {Name: "f", Type: TypeFolder, URL: "/srv/media", Password: "x"},
	} {
		var serr *Error
		if err := s.WithDefaults().Check(); !errors.As(err, &serr) || serr.Field != strings.TrimSpace(field) {
			t.Errorf("%s: %v", field, err)
		}
	}
	if (Settings{Type: TypeM3U, URL: "/srv/a.m3u"}).Host() != "file a.m3u" {
		t.Error("a playlist file's host")
	}
}
