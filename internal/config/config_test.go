// SPDX-License-Identifier: AGPL-3.0-or-later

package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

func env(vars map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := vars[name]
		return v, ok
	}
}

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overscan.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestMissingOptionalFileGivesDefaults(t *testing.T) {
	cfg, readFile, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if readFile {
		t.Error("readFile = true for a missing file")
	}
	if !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("got %+v, want the defaults %+v", cfg, Default())
	}
}

func TestMissingRequiredFileIsAnError(t *testing.T) {
	if _, _, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), true, env(nil)); err == nil {
		t.Error("no error for a missing required file")
	}
}

func TestFileOverridesDefaults(t *testing.T) {
	path := writeFile(t, "listen: \"127.0.0.1:9090\"\nlog:\n  level: debug\n")
	cfg, readFile, err := Load(path, true, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !readFile {
		t.Error("readFile = false")
	}
	want := Default()
	want.Listen = "127.0.0.1:9090"
	want.Log.Level = "debug"
	if !reflect.DeepEqual(cfg, want) {
		t.Errorf("got %+v, want %+v", cfg, want)
	}
}

func TestEmptyAndNullValuesKeepDefaults(t *testing.T) {
	for name, content := range map[string]string{
		"empty file":             "",
		"only comments":          "# listen: \":9090\"\n",
		"null value":             "listen:\n",
		"section with no values": "log:\n  # level: debug\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, readFile, err := Load(writeFile(t, content), true, env(nil))
			if err != nil {
				t.Fatal(err)
			}
			if !readFile || !reflect.DeepEqual(cfg, Default()) {
				t.Errorf("got %+v (readFile %v), want the defaults", cfg, readFile)
			}
		})
	}
}

func TestEnvironmentOverridesFile(t *testing.T) {
	path := writeFile(t, "listen: \":9090\"\ndata_dir: /srv/overscan\n")
	cfg, _, err := Load(path, true, env(map[string]string{
		"OVERSCAN_LISTEN":     ":7070",
		"OVERSCAN_LOG_FORMAT": "json",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":7070" || cfg.DataDir != "/srv/overscan" || cfg.Log.Format != "json" {
		t.Errorf("got %+v", cfg)
	}
}

func TestUnknownKeyIsAnError(t *testing.T) {
	_, _, err := Load(writeFile(t, "lisen: \":9090\"\n"), true, env(nil))
	if err == nil || !strings.Contains(err.Error(), "lisen") {
		t.Errorf("got %v, want an error naming the unknown key", err)
	}
}

func TestInvalidSettingsAreErrors(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"listen without port": {"OVERSCAN_LISTEN": "8080"},
		"empty listen":        {"OVERSCAN_LISTEN": ""},
		"empty data dir":      {"OVERSCAN_DATA_DIR": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(vars)); err == nil {
				t.Error("no error")
			}
		})
	}
}

func TestAdminListen(t *testing.T) {
	cfg, _, err := Load(writeFile(t, "admin_listen: \"127.0.0.1:8081\"\n"), true, env(nil))
	if err != nil || cfg.AdminListen != "127.0.0.1:8081" {
		t.Fatalf("from the file: %q, %v", cfg.AdminListen, err)
	}
	cfg, _, err = Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(map[string]string{"OVERSCAN_ADMIN_LISTEN": ":9091"}))
	if err != nil || cfg.AdminListen != ":9091" {
		t.Fatalf("from the environment: %q, %v", cfg.AdminListen, err)
	}
	if cfg, _, err = Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(nil)); err != nil || cfg.AdminListen != "" {
		t.Errorf("default: %q, %v; want none", cfg.AdminListen, err)
	}
	for name, vars := range map[string]map[string]string{
		"no port":        {"OVERSCAN_ADMIN_LISTEN": "8081"},
		"same as listen": {"OVERSCAN_ADMIN_LISTEN": ":8080"},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(vars))
			if err == nil || !strings.Contains(err.Error(), "admin_listen") {
				t.Errorf("got %v, want an error about admin_listen", err)
			}
		})
	}
}

func TestKeyFile(t *testing.T) {
	cfg, _, err := Load(writeFile(t, "data_dir: /srv/overscan\n"), true, env(nil))
	if err != nil || cfg.KeyPath() != "/srv/overscan/secret.key" {
		t.Fatalf("default: %q, %v", cfg.KeyPath(), err)
	}
	cfg, _, err = Load(writeFile(t, "key_file: /etc/overscan/secret.key\n"), true, env(nil))
	if err != nil || cfg.KeyPath() != "/etc/overscan/secret.key" {
		t.Fatalf("from the file: %q, %v", cfg.KeyPath(), err)
	}
	cfg, _, err = Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(map[string]string{"OVERSCAN_KEY_FILE": "/run/keys/overscan"}))
	if err != nil || cfg.KeyPath() != "/run/keys/overscan" {
		t.Fatalf("from the environment: %q, %v", cfg.KeyPath(), err)
	}
	_, _, err = Load(writeFile(t, "key_file: secret.key\n"), true, env(nil))
	if err == nil || !strings.Contains(err.Error(), "key_file") {
		t.Errorf("a relative path: %v", err)
	}
}

func TestRecordingsDir(t *testing.T) {
	cfg, _, err := Load(writeFile(t, "data_dir: /srv/overscan\n"), true, env(nil))
	if err != nil || cfg.RecordingsPath() != "/srv/overscan/Recordings" {
		t.Fatalf("default: %q, %v", cfg.RecordingsPath(), err)
	}
	cfg, _, err = Load(writeFile(t, "recordings_dir: /srv/recordings\n"), true, env(nil))
	if err != nil || cfg.RecordingsPath() != "/srv/recordings" {
		t.Fatalf("from the file: %q, %v", cfg.RecordingsPath(), err)
	}
	cfg, _, err = Load(filepath.Join(t.TempDir(), "absent.yaml"), false, env(map[string]string{"OVERSCAN_RECORDINGS_DIR": "/mnt/tv"}))
	if err != nil || cfg.RecordingsPath() != "/mnt/tv" {
		t.Fatalf("from the environment: %q, %v", cfg.RecordingsPath(), err)
	}
	_, _, err = Load(writeFile(t, "recordings_dir: tv\n"), true, env(nil))
	if err == nil || !strings.Contains(err.Error(), "recordings_dir") {
		t.Errorf("a relative path: %v", err)
	}
}

func TestFFmpeg(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yaml")
	if cfg, _, err := Load(absent, false, env(nil)); err != nil || cfg.FFmpeg != "ffmpeg" {
		t.Fatalf("default: %q, %v", cfg.FFmpeg, err)
	}
	for content, want := range map[string]string{
		"ffmpeg: /usr/lib/jellyfin-ffmpeg/ffmpeg\n": "/usr/lib/jellyfin-ffmpeg/ffmpeg",
		"ffmpeg: \"off\"\n":                         FFmpegOff,
	} {
		if cfg, _, err := Load(writeFile(t, content), true, env(nil)); err != nil || cfg.FFmpeg != want {
			t.Errorf("%q: %q, %v", content, cfg.FFmpeg, err)
		}
	}
	if cfg, _, err := Load(absent, false, env(map[string]string{"OVERSCAN_FFMPEG": "/opt/ffmpeg/bin/ffmpeg"})); err != nil ||
		cfg.FFmpeg != "/opt/ffmpeg/bin/ffmpeg" {
		t.Fatalf("from the environment: %q, %v", cfg.FFmpeg, err)
	}
	for _, bad := range []string{"", "bin/ffmpeg"} {
		_, _, err := Load(absent, false, env(map[string]string{"OVERSCAN_FFMPEG": bad}))
		if err == nil || !strings.Contains(err.Error(), "ffmpeg") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestTrustedProxies(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yaml")
	if cfg, _, err := Load(absent, false, env(nil)); err != nil || cfg.TrustedProxies != nil {
		t.Fatalf("default: %q, %v", cfg.TrustedProxies, err)
	}
	cfg, _, err := Load(writeFile(t, "trusted_proxies: [192.168.1.2, 172.16.0.0/24, \"fd00::/8\"]\n"), true, env(nil))
	if want := []string{"192.168.1.2", "172.16.0.0/24", "fd00::/8"}; err != nil || !reflect.DeepEqual(cfg.TrustedProxies, want) {
		t.Errorf("from the file: %q, %v", cfg.TrustedProxies, err)
	}
	cfg, _, err = Load(absent, false, env(map[string]string{"OVERSCAN_TRUSTED_PROXIES": " 127.0.0.1, ::1  192.168.1.0/24,"}))
	if want := []string{"127.0.0.1", "::1", "192.168.1.0/24"}; err != nil || !reflect.DeepEqual(cfg.TrustedProxies, want) {
		t.Errorf("from the environment: %q, %v", cfg.TrustedProxies, err)
	}
	// The environment's empty value trusts none, over the file's.
	cfg, _, err = Load(writeFile(t, "trusted_proxies: [192.168.1.2]\n"), true, env(map[string]string{"OVERSCAN_TRUSTED_PROXIES": ""}))
	if err != nil || len(cfg.TrustedProxies) != 0 {
		t.Errorf("an empty variable: %q, %v", cfg.TrustedProxies, err)
	}
	for _, bad := range []string{"proxy.example", "0.0.0.0/0", "192.0.2.10", "192.168.1.0/33"} {
		_, _, err := Load(absent, false, env(map[string]string{"OVERSCAN_TRUSTED_PROXIES": bad}))
		if err == nil || !strings.Contains(err.Error(), "trusted_proxies") {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestProbeReports(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yaml")
	if cfg, _, err := Load(absent, false, env(nil)); err != nil || cfg.ProbeReports {
		t.Fatalf("default: %v, %v", cfg.ProbeReports, err)
	}
	if cfg, _, err := Load(writeFile(t, "probe_reports: true\n"), true, env(nil)); err != nil || !cfg.ProbeReports {
		t.Errorf("from the file: %v, %v", cfg.ProbeReports, err)
	}
	if cfg, _, err := Load(absent, false, env(map[string]string{"OVERSCAN_PROBE_REPORTS": "true"})); err != nil || !cfg.ProbeReports {
		t.Errorf("from the environment: %v, %v", cfg.ProbeReports, err)
	}
	if cfg, _, err := Load(writeFile(t, "probe_reports: true\n"), true, env(map[string]string{"OVERSCAN_PROBE_REPORTS": "0"})); err != nil || cfg.ProbeReports {
		t.Errorf("the environment over the file: %v, %v", cfg.ProbeReports, err)
	}
	if _, _, err := Load(absent, false, env(map[string]string{"OVERSCAN_PROBE_REPORTS": "yes please"})); err == nil ||
		!strings.Contains(err.Error(), "OVERSCAN_PROBE_REPORTS") {
		t.Errorf("a value that is not true or false: %v", err)
	}
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, readFile, err := Load(filepath.Join("..", "..", "deploy", "overscan.example.yaml"), true, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !readFile || !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("the example should spell out the defaults; got %+v", cfg)
	}
}

func TestSources(t *testing.T) {
	path := writeFile(t, `sources:
  - name: main
    type: m3u
    url: https://provider.example/get.php?username=u&password=p
  - name: local-file
    type: m3u
    url: /srv/playlists/local.m3u
    refresh: 30m
    max_connections: 0
    user_agent: TestPlayer/1.0
    numbers: provider
  - name: server
    type: xtream
    url: http://provider.example:8080
    username: viewer
    password: p
  - name: den
    type: jellyfin
    url: http://jellyfin.example:8096
    api_key: 0123456789abcdef
    user: Anna
  - name: films
    type: folder
    url: /srv/media/Films
  - name: lounge
    type: plex
    url: http://plex.example:32400
    token: fictional-token
`)
	cfg, _, err := Load(path, true, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sources) != 6 {
		t.Fatalf("sources %+v", cfg.Sources)
	}
	if x := cfg.Sources[2].Settings(); x.Type != "xtream" || x.Username != "viewer" || x.Password != "p" || x.IsFile() {
		t.Errorf("server %+v", x)
	}
	if jf := cfg.Sources[3].Settings(); jf.Type != "jellyfin" || jf.Username != "Anna" || jf.Password != "0123456789abcdef" ||
		jf.MaxConnections != 0 || jf.Refresh != DefaultRefresh {
		t.Errorf("den %+v", jf)
	}
	if f := cfg.Sources[4].Settings(); f.Type != "folder" || f.URL != "/srv/media/Films" || f.MaxConnections != 0 || f.IsFile() {
		t.Errorf("films %+v", f)
	}
	if px := cfg.Sources[5].Settings(); px.Type != "plex" || px.Password != "fictional-token" || px.Username != "" ||
		px.MaxConnections != 0 {
		t.Errorf("lounge %+v", px)
	}
	remote, local := cfg.Sources[0], cfg.Sources[1]
	if remote.Refresh != DefaultRefresh || *remote.MaxConnections != DefaultMaxConnections || remote.IsFile() || remote.Numbers != NumbersPlaylist {
		t.Errorf("remote %+v", remote)
	}
	if local.Refresh != 30*time.Minute || *local.MaxConnections != 0 || !local.IsFile() || local.UserAgent != "TestPlayer/1.0" ||
		local.Numbers != NumbersProvider {
		t.Errorf("local-file %+v", local)
	}
}

func TestStalkerSource(t *testing.T) {
	path := writeFile(t, `sources:
  - name: portal
    type: stalker
    url: http://portal.example/stalker_portal/c/
    mac: 00:1A:79:00:00:51
    serial: FICTIONAL0001
    device_id: made-up-1
    device_id2: made-up-2
    signature: made-up-3
    model: MAG322
    username: viewer
    password: p
`)
	cfg, _, err := Load(path, true, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	st := cfg.Sources[0].Settings()
	want := source.Device{MAC: "00:1A:79:00:00:51", Serial: "FICTIONAL0001", DeviceID: "made-up-1", DeviceID2: "made-up-2",
		Signature: "made-up-3", Model: "MAG322"}
	if st.Type != source.TypeStalker || st.Device != want || st.Username != "viewer" || st.Password != "p" ||
		st.MaxConnections != 1 || st.URL != "http://portal.example/stalker_portal/c/" {
		t.Errorf("portal %+v", st)
	}
}

func TestInvalidSources(t *testing.T) {
	const secret = "s3cret"
	for name, src := range map[string]string{
		"no name":        "type: m3u\n    url: http://provider.example/" + secret,
		"bad name":       "name: Main\n    type: m3u\n    url: http://provider.example/" + secret,
		"bad type":       "name: main\n    type: tvheadend\n    url: http://provider.example/" + secret,
		"stalker mac":    "name: portal\n    type: stalker\n    url: http://portal.example/c/\n    mac: 00:1A:79:" + secret,
		"stalker page":   "name: portal\n    type: stalker\n    url: http://portal.example/c/?" + secret + "\n    mac: 00:1A:79:00:00:51",
		"xtream mac":     "name: main\n    type: xtream\n    url: http://provider.example\n    username: viewer\n    password: p\n    mac: " + secret,
		"relative path":  "name: main\n    type: m3u\n    url: playlists/" + secret + ".m3u",
		"ftp":            "name: main\n    type: m3u\n    url: ftp://provider.example/" + secret,
		"no host":        "name: main\n    type: m3u\n    url: http:///" + secret,
		"short refresh":  "name: main\n    type: m3u\n    url: http://provider.example/" + secret + "\n    refresh: 1m",
		"negative limit": "name: main\n    type: m3u\n    url: http://provider.example/" + secret + "\n    max_connections: -1",
		"user agent":     "name: main\n    type: m3u\n    url: http://provider.example/" + secret + "\n    user_agent: \"a\\nb\"",
		"numbers":        "name: main\n    type: m3u\n    url: http://provider.example/" + secret + "\n    numbers: tvg",
		"xtream login":   "name: main\n    type: xtream\n    url: http://provider.example/" + secret,
		"xtream page":    "name: main\n    type: xtream\n    url: http://provider.example/get.php?password=" + secret + "\n    username: viewer\n    password: " + secret,
		"jellyfin key":   "name: den\n    type: jellyfin\n    url: http://jellyfin.example/" + secret,
		"jellyfin login": "name: den\n    type: jellyfin\n    url: http://jellyfin.example\n    username: viewer\n    password: " + secret,
		"xtream api key": "name: main\n    type: xtream\n    url: http://provider.example\n    username: viewer\n    password: p\n    api_key: " + secret,
		"folder path":    "name: films\n    type: folder\n    url: media/" + secret,
		"folder login":   "name: films\n    type: folder\n    url: /srv/media\n    password: " + secret,
		"plex login":     "name: lounge\n    type: plex\n    url: http://plex.example\n    username: viewer\n    password: " + secret,
		"plex page":      "name: lounge\n    type: plex\n    url: http://plex.example/web?" + secret + "\n    token: t",
		"xtream token":   "name: main\n    type: xtream\n    url: http://provider.example\n    username: viewer\n    password: p\n    token: " + secret,
	} {
		t.Run(name, func(t *testing.T) {
			_, _, err := Load(writeFile(t, "sources:\n  - "+src+"\n"), true, env(nil))
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Errorf("the error quotes the URL: %v", err)
			}
		})
	}

	_, _, err := Load(writeFile(t, "sources:\n  - name: den\n    type: jellyfin\n    url: http://jellyfin.example\n"), true, env(nil))
	if err == nil || !strings.Contains(err.Error(), "api_key: ") {
		t.Errorf("a Jellyfin source without a key: %v", err)
	}

	_, _, err = Load(writeFile(t, "sources:\n  - name: lounge\n    type: plex\n    url: http://plex.example:32400\n"), true, env(nil))
	if err == nil || !strings.Contains(err.Error(), "token: ") {
		t.Errorf("a Plex source without a token: %v", err)
	}

	dup := "sources:\n  - name: main\n    type: m3u\n    url: /a.m3u\n  - name: main\n    type: m3u\n    url: /b.m3u\n"
	if _, _, err := Load(writeFile(t, dup), true, env(nil)); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Errorf("duplicate names: %v", err)
	}
}

// A mistake in the file is named by its line and the file's own keys, and
// never quotes the file's values, which can hold a source's credentials.
func TestFileMistakesAreNamedByLineAndKey(t *testing.T) {
	const secret = "s3cret-value"
	source := "sources:\n  - name: one\n    type: xtream\n    url: http://192.0.2.1\n    username: viewer\n    password: " + secret + "\n"
	for name, tc := range map[string]struct{ file, want string }{
		"unknown key":      {"listne: \":8080\"\n", `line 1: there is no setting "listne"; did you mean "listen"?`},
		"unknown, nested":  {"log:\n  levle: debug\n", `line 2: there is no setting "levle"; did you mean "level"?`},
		"unknown, far":     {"bananas: 1\n", `line 1: there is no setting "bananas"`},
		"wrong kind":       {"probe_reports: maybe\n", "line 1: probe_reports takes true or false, not text"},
		"number":           {source + "    max_connections: lots\n", "line 7: max_connections takes a whole number, not text"},
		"too large":        {source + "    max_connections: 99999999999999999999\n", "line 7: max_connections takes a smaller number"},
		"list":             {"trusted_proxies: 192.0.2.1\n", "line 1: trusted_proxies takes a list"},
		"settings":         {"log: verbose\n", "line 1: log takes settings of its own"},
		"no space":         {"data_dir:/srv/overscan\n", "line 1: each line takes a key, a colon and a space"},
		"twice":            {"listen: \":8080\"\nlisten: \":8081\"\n", `line 2: "listen" is set a second time`},
		"tab":              {"log:\n\tlevel: debug\n", "line 2: found character"},
		"indentation":      {source + "      extra: x\n", "line 6: mapping value is not allowed in this context; check the indentation"},
		"quotes":           {"data_dir: \"/srv/overscan\n", "line 1: could not find end character of double-quoted text; check the indentation"},
		"refresh":          {source + "    refresh: 6 hours\n", "a source's refresh takes a number and a unit, such as 30m, 6h or 24h"},
		"near a secret":    {"sources:\n  - name: one\n    password: " + secret + "\n      extra: x\n", "line 3: "},
		"secret, the kind": {"sources:\n  - name: one\n    password: [" + secret + "]\n", "line 3: password takes text, not a list"},
	} {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, tc.file)
			_, _, err := Load(path, true, env(nil))
			if err == nil {
				t.Fatal("no error")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "config "+path) {
				t.Errorf("got %q, want %q", err, tc.want)
			}
			if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "\n") {
				t.Errorf("the message quotes the file: %q", err)
			}
		})
	}
	if strings.Contains(fmt.Sprint(settingNames()), "\"") {
		t.Error("a setting's name holds a quote")
	}
}

func TestFileThatCannotBeRead(t *testing.T) {
	absent := filepath.Join(t.TempDir(), "absent.yaml")
	if _, _, err := Load(absent, true, env(nil)); err == nil || err.Error() != "config: there is no file at "+absent {
		t.Errorf("absent: %v", err)
	}
	if os.Geteuid() == 0 {
		t.Skip("root reads any file")
	}
	path := writeFile(t, "listen: \":8080\"\n")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	for _, required := range []bool{true, false} {
		if _, _, err := Load(path, required, env(nil)); err == nil || !strings.Contains(err.Error(), "may not be read by this user; run the command as the user that the server runs as") {
			t.Errorf("unreadable, required %v: %v", required, err)
		}
	}
}

func TestListenAddresses(t *testing.T) {
	for value, want := range map[string]string{
		":8080":          "",
		"192.0.2.1:8080": "",
		"[::1]:8080":     "",
		":0":             "",
		":http":          "",
		"8080":           `listen "8080": give a port after a colon, such as ":8080"`,
		":99999":         `listen ":99999": the port must be a number up to 65535`,
		":-1":            "the port must be a number up to 65535",
	} {
		_, _, err := Load(writeFile(t, "listen: \""+value+"\"\n"), true, env(nil))
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", value, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: got %v, want %q", value, err, want)
		}
	}
	if _, _, err := Load(writeFile(t, "admin_listen: \":99999\"\n"), true, env(nil)); err == nil || !strings.Contains(err.Error(), "admin_listen") {
		t.Errorf("admin_listen: %v", err)
	}
}

func TestDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"listen", "listen", 0}, {"listne", "listen", 1}, {"lisen", "listen", 1}, {"levle", "level", 1},
		{"data-dir", "data_dir", 1}, {"datadir", "data_dir", 1}, {"", "log", 3}, {"bananas", "sources", 6},
	} {
		if got := distance(tc.a, tc.b); got != tc.want {
			t.Errorf("distance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}
