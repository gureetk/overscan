// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config builds Overscan's configuration from defaults, a YAML file
// and environment variables, each overriding the one before.
//
// The file holds settings for the server itself, such as addresses, paths and
// logging. Settings that the admin edits live in the database. A source
// declared in the file is imported into the database once, by name (D46).
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/goccy/go-yaml"

	"overscan.gureet.ca/internal/clientaddr"
	"overscan.gureet.ca/internal/source"
)

// DefaultPath is where Overscan looks for its configuration file when none is
// named on the command line.
const DefaultPath = "/etc/overscan/overscan.yaml"

// Config is Overscan's configuration.
type Config struct {
	// Listen is the address of the device-facing HTTP server.
	Listen string `yaml:"listen"`
	// AdminListen, if set, is the address of the admin (D44), which is then
	// served there alone. If not set, the admin shares Listen.
	AdminListen string `yaml:"admin_listen"`
	// DataDir holds the database and the other files Overscan writes.
	DataDir string `yaml:"data_dir"`
	// KeyFile is the file of the key that encrypts the sources' secrets in
	// the database (D46); KeyFileName in DataDir if not set. Overscan makes
	// it at its first start.
	KeyFile string `yaml:"key_file"`
	// RecordingsDir is the folder that recordings are written in (D89);
	// RecordingsDirName in DataDir if not set.
	RecordingsDir string `yaml:"recordings_dir"`
	// Log controls logging.
	Log Log `yaml:"log"`
	// FFmpeg is the ffmpeg program that repairs the streams of channels set
	// to be repaired (D57) and converts the sound of films and episodes that
	// a box cannot play (D68), with the ffprobe beside it: an absolute
	// path, or a name to look for in PATH. "ffmpeg" if not set; "off" turns
	// repair and conversion off.
	FFmpeg string `yaml:"ffmpeg"`
	// TrustedProxies are the reverse proxies whose X-Forwarded-For and
	// X-Forwarded-Proto headers say whom a request is for and whether it
	// came over HTTPS (D119): addresses or ranges, each on the server's own
	// networks. Without any, those headers are ignored.
	TrustedProxies []string `yaml:"trusted_proxies"`
	// ProbeReports lets the probe page save its reports in DataDir/probe,
	// from the server's own networks (D120); off if not set.
	ProbeReports bool `yaml:"probe_reports"`
	// Sources are imported into the database at start, each once (D46).
	Sources []Source `yaml:"sources"`
}

// Source is a source of channels, films or series, to import
// (source.Settings).
type Source struct {
	// Name identifies the source.
	Name string `yaml:"name"`
	// Type is the kind of source: m3u, the default, xtream (D50), jellyfin
	// (D66), plex (D72), folder (D67) or stalker (D76).
	Type string `yaml:"type"`
	// URL is, for an M3U source, an http or https address or the absolute
	// path of a file; for an Xtream, a Jellyfin or a Plex source, the
	// server's address; for a folder, its absolute path; for a Stalker
	// portal, its address as the provider gave it. It can hold credentials,
	// so it is never logged.
	URL string `yaml:"url"`
	// Username and Password are an Xtream source's login, or the login
	// that a Stalker portal asks for, if any. They are never logged.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	// APIKey is a Jellyfin source's API key, which is never logged, and
	// User the Jellyfin user whose libraries boxes see; every library if
	// not set.
	APIKey string `yaml:"api_key"`
	User   string `yaml:"user"`
	// Token is a Plex source's token, the X-Plex-Token of an account that
	// sees the libraries; it is never logged.
	Token string `yaml:"token"`
	// MAC, Serial, DeviceID, DeviceID2, Signature and Model are, for a
	// Stalker portal, the box that the provider registered: its MAC address
	// and the details that the portal may check besides it (D76). They are
	// never logged.
	MAC       string `yaml:"mac"`
	Serial    string `yaml:"serial"`
	DeviceID  string `yaml:"device_id"`
	DeviceID2 string `yaml:"device_id2"`
	Signature string `yaml:"signature"`
	Model     string `yaml:"model"`
	// Refresh is how often the source is read again; at least 15m, 6h if
	// not set.
	Refresh time.Duration `yaml:"refresh"`
	// MaxConnections is how many streams the source allows at once, 0 for
	// no limit; if not set, 1, or no limit for a media server or a
	// folder.
	MaxConnections *int `yaml:"max_connections"`
	// UserAgent is sent when Overscan fetches the playlist, for a provider
	// that expects a particular player.
	UserAgent string `yaml:"user_agent"`
	// Numbers says how the source's channels are numbered (D40): playlist,
	// the default, numbers them in playlist order; provider uses the
	// numbers the playlist gives them (tvg-chno) where it gives any.
	Numbers string `yaml:"numbers"`
	// CatchupZone is, for an Xtream server or a playlist, the time zone of
	// the dates and times in its catch-up addresses (D81): an IANA name such
	// as UTC or Europe/London; if not set, the one that an Xtream server
	// gives, or else UTC.
	CatchupZone string `yaml:"catchup_zone"`
}

// Ways of numbering a source's channels.
const (
	NumbersPlaylist = source.NumbersPlaylist
	NumbersProvider = source.NumbersProvider
)

// Source defaults.
const (
	DefaultRefresh        = source.DefaultRefresh
	DefaultMaxConnections = source.DefaultMaxConnections
)

// FFmpegOff is the ffmpeg setting that turns stream repair and conversion
// off.
const FFmpegOff = "off"

// KeyFileName is the key file's name in the data directory, where it is kept
// unless key_file names another place.
const KeyFileName = "secret.key"

// RecordingsDirName is the recordings folder's name in the data directory,
// where it is unless recordings_dir names another place.
const RecordingsDirName = "Recordings"

// RecordingsPath returns the path of the recordings folder.
func (c Config) RecordingsPath() string {
	if c.RecordingsDir != "" {
		return c.RecordingsDir
	}
	return filepath.Join(c.DataDir, RecordingsDirName)
}

// KeyPath returns the path of the key file.
func (c Config) KeyPath() string {
	if c.KeyFile != "" {
		return c.KeyFile
	}
	return filepath.Join(c.DataDir, KeyFileName)
}

// IsFile reports whether the source is a local file rather than an address.
func (s Source) IsFile() bool { return s.Settings().IsFile() }

// Settings returns the source's settings, with the defaults that Load fills
// in.
func (s Source) Settings() source.Settings {
	max := source.DefaultLimit(s.Type)
	if s.MaxConnections != nil {
		max = *s.MaxConnections
	}
	username, password := s.Username, s.Password
	switch s.Type {
	case source.TypeJellyfin:
		username, password = s.User, s.APIKey
	case source.TypePlex:
		username, password = "", s.Token
	}
	return source.Settings{
		Name:           s.Name,
		Type:           s.Type,
		URL:            s.URL,
		Username:       username,
		Password:       password,
		UserAgent:      s.UserAgent,
		MaxConnections: max,
		Refresh:        s.Refresh,
		Numbers:        s.Numbers,
		CatchupZone:    s.CatchupZone,
		Device: source.Device{MAC: s.MAC, Serial: s.Serial, DeviceID: s.DeviceID, DeviceID2: s.DeviceID2,
			Signature: s.Signature, Model: s.Model},
	}
}

// Log controls logging.
type Log struct {
	// Level is debug, info, warn or error.
	Level string `yaml:"level"`
	// Format is text or json.
	Format string `yaml:"format"`
}

// Default returns the configuration used where nothing overrides it.
func Default() Config {
	return Config{
		Listen:  ":8080",
		DataDir: "/var/lib/overscan",
		Log:     Log{Level: "info", Format: "text"},
		FFmpeg:  "ffmpeg",
	}
}

// envVars are the environment variables that override settings.
var envVars = []struct {
	name string
	set  func(*Config, string) error
}{
	{"OVERSCAN_LISTEN", func(c *Config, v string) error { c.Listen = v; return nil }},
	{"OVERSCAN_ADMIN_LISTEN", func(c *Config, v string) error { c.AdminListen = v; return nil }},
	{"OVERSCAN_DATA_DIR", func(c *Config, v string) error { c.DataDir = v; return nil }},
	{"OVERSCAN_KEY_FILE", func(c *Config, v string) error { c.KeyFile = v; return nil }},
	{"OVERSCAN_RECORDINGS_DIR", func(c *Config, v string) error { c.RecordingsDir = v; return nil }},
	{"OVERSCAN_LOG_LEVEL", func(c *Config, v string) error { c.Log.Level = v; return nil }},
	{"OVERSCAN_LOG_FORMAT", func(c *Config, v string) error { c.Log.Format = v; return nil }},
	{"OVERSCAN_FFMPEG", func(c *Config, v string) error { c.FFmpeg = v; return nil }},
	// A list, its entries split by commas or spaces.
	{"OVERSCAN_TRUSTED_PROXIES", func(c *Config, v string) error {
		c.TrustedProxies = strings.FieldsFunc(v, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
		return nil
	}},
	{"OVERSCAN_PROBE_REPORTS", func(c *Config, v string) error {
		on, err := strconv.ParseBool(v)
		if err != nil {
			return fmt.Errorf("OVERSCAN_PROBE_REPORTS must be true or false, not %q", v)
		}
		c.ProbeReports = on
		return nil
	}},
}

// Load returns the defaults, overridden first by the YAML file at path and then
// by the environment variables that lookupEnv finds. A missing file is an error
// only if required is true; readFile reports whether the file was read.
// Unknown keys in the file are errors, so that a typo cannot go unnoticed.
func Load(path string, required bool, lookupEnv func(string) (string, bool)) (cfg Config, readFile bool, err error) {
	cfg = Default()
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		// A file that is empty or holds only comments is a null document,
		// which the decoder would turn into a zeroed Config, so skip it.
		var doc any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return Config{}, false, fmt.Errorf("config %s: %w", path, err)
		}
		if doc != nil {
			if err := yaml.UnmarshalWithOptions(data, &cfg, yaml.DisallowUnknownField()); err != nil {
				return Config{}, false, fmt.Errorf("config %s: %w", path, err)
			}
		}
		readFile = true
	case errors.Is(err, fs.ErrNotExist) && !required:
		// Carry on with the defaults.
	default:
		return Config{}, false, fmt.Errorf("config: %w", err)
	}

	for _, v := range envVars {
		if value, ok := lookupEnv(v.name); ok {
			if err := v.set(&cfg, value); err != nil {
				return Config{}, false, fmt.Errorf("config: %w", err)
			}
		}
	}
	for i := range cfg.Sources {
		src := &cfg.Sources[i]
		if src.Refresh == 0 {
			src.Refresh = DefaultRefresh
		}
		if src.MaxConnections == nil {
			n := source.DefaultLimit(src.Type)
			src.MaxConnections = &n
		}
		if src.Numbers == "" {
			src.Numbers = NumbersPlaylist
		}
	}

	if err := cfg.validate(); err != nil {
		return Config{}, false, fmt.Errorf("config: %w", err)
	}
	return cfg, readFile, nil
}

// validate checks the settings that this package owns. Logging settings are
// checked where the logger is built.
func (c Config) validate() error {
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return fmt.Errorf("listen %q: %w", c.Listen, err)
	}
	if c.AdminListen != "" {
		if _, _, err := net.SplitHostPort(c.AdminListen); err != nil {
			return fmt.Errorf("admin_listen %q: %w", c.AdminListen, err)
		}
		if c.AdminListen == c.Listen {
			return errors.New("admin_listen must differ from listen; leave it out to serve the admin on listen")
		}
	}
	if c.DataDir == "" {
		return errors.New("data_dir must not be empty")
	}
	if c.KeyFile != "" && !filepath.IsAbs(c.KeyFile) {
		return errors.New("key_file must be an absolute path")
	}
	if c.RecordingsDir != "" && !filepath.IsAbs(c.RecordingsDir) {
		return errors.New("recordings_dir must be an absolute path")
	}
	if c.FFmpeg == "" {
		return errors.New(`ffmpeg must not be empty; set it to "off" to turn stream repair off`)
	}
	if strings.ContainsRune(c.FFmpeg, '/') && !filepath.IsAbs(c.FFmpeg) {
		return errors.New("ffmpeg must be an absolute path, or a name to look for in PATH")
	}
	if _, err := clientaddr.Parse(c.TrustedProxies); err != nil {
		return fmt.Errorf("trusted_proxies: %w", err)
	}
	names := map[string]bool{}
	for i, src := range c.Sources {
		if err := src.validate(); err != nil {
			if src.Name == "" {
				return fmt.Errorf("sources[%d]: %w", i, err)
			}
			return fmt.Errorf("source %q: %w", src.Name, err)
		}
		if names[src.Name] {
			return fmt.Errorf("source %q is declared twice", src.Name)
		}
		names[src.Name] = true
	}
	return nil
}

// validate checks a source by the rules that the admin follows too. Messages
// never quote the URL, which can hold credentials.
func (s Source) validate() error {
	jellyfin, plex := s.Type == source.TypeJellyfin, s.Type == source.TypePlex
	switch {
	case jellyfin && (s.Username != "" || s.Password != ""):
		return errors.New("a Jellyfin source takes api_key, and user if any, instead of username and password")
	case !jellyfin && (s.APIKey != "" || s.User != ""):
		return errors.New("api_key and user are for a Jellyfin source")
	case plex && (s.Username != "" || s.Password != ""):
		return errors.New("a Plex source takes token instead of username and password")
	case !plex && s.Token != "":
		return errors.New("token is for a Plex source")
	case s.Type != source.TypeStalker && (s.MAC != "" || s.Serial != "" || s.DeviceID != "" || s.DeviceID2 != "" ||
		s.Signature != "" || s.Model != ""):
		return errors.New("mac, serial, device_id, device_id2, signature and model are for a Stalker portal")
	}
	if err := s.Settings().Check(); err != nil {
		var bad *source.Error
		if errors.As(err, &bad) {
			field := bad.Field
			var names map[string]string
			switch {
			case jellyfin:
				names = map[string]string{"password": "api_key", "username": "user"}
			case plex:
				names = map[string]string{"password": "token"}
			}
			if name := names[field]; name != "" {
				field = name
			}
			return fmt.Errorf("%s: %s", field, bad.Text)
		}
		return err
	}
	return nil
}
