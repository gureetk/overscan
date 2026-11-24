// SPDX-License-Identifier: AGPL-3.0-or-later

// Package source describes a source of channels, films or series: its
// settings, and the rules they follow wherever they come from, the admin or
// the configuration file.
package source

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Types of source.
const (
	TypeM3U      = "m3u"      // an M3U playlist, at an address or in a file
	TypeXtream   = "xtream"   // a server that answers the Xtream API
	TypeJellyfin = "jellyfin" // a Jellyfin media server, for its films and series (D66)
	TypeFolder   = "folder"   // a folder of films and series on this server (D67)
	TypePlex     = "plex"     // a Plex Media Server, for its films and series (D72)
	TypeStalker  = "stalker"  // an upstream Stalker portal, read as the box that the provider registered (D76)
)

// StalkerKeyPrefix starts the identity key of a Stalker portal's channel,
// before the portal's own ID for it (D76).
const StalkerKeyPrefix = "stalker:"

// How a source's channels are numbered (D40).
const (
	NumbersPlaylist = "playlist" // in the playlist's order
	NumbersProvider = "provider" // the numbers the playlist gives them, where it gives any
)

// Defaults and limits.
const (
	DefaultRefresh        = 6 * time.Hour
	MinRefresh            = 15 * time.Minute
	MaxRefresh            = 7 * 24 * time.Hour
	DefaultMaxConnections = 1
	MaxConnections        = 1000
	maxUserAgent          = 200
	maxURL                = 4096
	maxLogin              = 256 // bytes of an Xtream username or password, a Jellyfin API key or user, or a Plex token
	maxDetail             = 256 // bytes of a box's serial number, device ID or signature
	maxModel              = 32  // bytes of a box's model
)

// Settings are a source's settings. URL can hold the credentials of the
// account at the source, and Username and Password are credentials, so none
// of them is ever shown, logged or put in an error; the admin shows
// Host(URL) instead.
type Settings struct {
	// Name identifies the source. Channels keep their numbers when the
	// address changes under the same name.
	Name string
	// Type is the kind of source: TypeM3U, TypeXtream, TypeJellyfin,
	// TypePlex or TypeFolder.
	Type string
	// URL is where the source is. For an M3U source, it is an http or https
	// address, or the absolute path of a file. For an Xtream, a Jellyfin
	// or a Plex source, it is the server's http or https address, such as
	// http://provider.example:8080. For a folder, it is the folder's
	// absolute path.
	URL string
	// Username and Password are an Xtream source's login. For a Jellyfin
	// source, Password is the API key and Username the user whose libraries
	// to read, "" for all of them. For a Plex source, Password is the token
	// of an account that sees the libraries, and Username is "". For a
	// Stalker portal, they are the login that the portal asks for, if it
	// asks for one. An M3U source has neither; its address holds any
	// credentials it needs.
	Username string
	Password string
	// Device is, for a Stalker portal, the box that the provider
	// registered, which Overscan signs in as; zero for other sources. Its
	// details identify an account, so they are kept as the login is.
	Device Device
	// UserAgent is sent when Overscan fetches the playlist, for a provider
	// that expects a particular player; "" for Overscan's own.
	UserAgent string
	// MaxConnections is how many streams the source allows at once; 0 for no
	// limit.
	MaxConnections int
	// Refresh is how often the source is read again.
	Refresh time.Duration
	// Numbers says how its channels are numbered: NumbersPlaylist or
	// NumbersProvider.
	Numbers string
	// CatchupZone is, for an Xtream server or a playlist, the time zone of
	// the dates and times in the addresses of its channels' archives
	// (D81): "" for the one that an Xtream server gives, or else UTC; or an
	// IANA name, such as UTC or Europe/London.
	CatchupZone string
	// PublicOnly keeps every request that Overscan makes for the source off
	// the server's own networks (D121): its address, guide, streams and
	// pictures, and every address they lead to, may be public ones only.
	// A folder is on the server itself, so it never is.
	PublicOnly bool
}

// Device is a box as a provider registered it with a Stalker portal: its MAC
// address and the details that the portal may check besides it.
type Device struct {
	MAC       string // such as 00:1A:79:12:34:56
	Serial    string // its serial number, "" if the portal does not check it
	DeviceID  string
	DeviceID2 string
	Signature string
	Model     string // such as MAG250, "" for that
}

// WithDefaults returns the settings with the defaults filled in where they
// are not set: the type, refresh and numbering.
func (s Settings) WithDefaults() Settings {
	if s.Type == "" {
		s.Type = TypeM3U
	}
	if s.Refresh == 0 {
		s.Refresh = DefaultRefresh
	}
	if s.Numbers == "" {
		s.Numbers = NumbersPlaylist
	}
	if s.Type == TypeFolder {
		s.PublicOnly = false
	}
	return s
}

// DefaultLimit returns how many streams at once a source of the given type
// allows unless its settings say: one for a playlist, an Xtream account or
// a Stalker portal's box, which providers limit, and no limit for a media
// server or a folder, which are the administrator's own.
func DefaultLimit(typ string) int {
	if typ == TypeJellyfin || typ == TypePlex || typ == TypeFolder {
		return 0
	}
	return DefaultMaxConnections
}

// IsFile reports whether the source is a local file rather than an address.
func (s Settings) IsFile() bool { return s.Type == TypeM3U && strings.HasPrefix(s.URL, "/") }

// HasChannels reports whether the source lists live channels: a media
// server or a folder has only films and series. A Stalker portal has
// channels.
func (s Settings) HasChannels() bool {
	return s.Type != TypeJellyfin && s.Type != TypePlex && s.Type != TypeFolder
}

// HasLibrary reports whether devices see the source's films and series:
// those of an Xtream, a Jellyfin or a Plex server, a folder or a Stalker
// portal (D77).
func (s Settings) HasLibrary() bool {
	return s.Type == TypeXtream || s.Type == TypeJellyfin || s.Type == TypePlex || s.Type == TypeFolder ||
		s.Type == TypeStalker
}

// Host returns what the admin shows of the source's address: Host(URL), or
// for a folder, its name. A Stalker portal's MAC address is not shown.
func (s Settings) Host() string {
	if s.Type == TypeFolder {
		return path.Base(path.Clean(s.URL))
	}
	return Host(s.URL)
}

// Error is a setting that breaks a rule. Its text never quotes the address
// or the login.
type Error struct {
	Field string // the setting: name, type, url, username, password, mac, device, user_agent, max_connections, refresh, numbers or catchup_zone
	Text  string // what is wrong, as a sentence
}

func (e *Error) Error() string { return e.Text }

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// Check reports the first setting that breaks a rule, as an *Error.
func (s Settings) Check() error {
	bad := func(field, format string, args ...any) error {
		return &Error{Field: field, Text: fmt.Sprintf(format, args...)}
	}
	switch {
	case !namePattern.MatchString(s.Name):
		return bad("name", "A source's name is 1 to 32 lower-case letters, digits, hyphens or underscores, starting with a letter or digit.")
	case s.Type != TypeM3U && s.Type != TypeXtream && s.Type != TypeJellyfin && s.Type != TypePlex && s.Type != TypeFolder &&
		s.Type != TypeStalker:
		return bad("type", "The type %q is not supported; use m3u, xtream, jellyfin, plex, folder or stalker.", s.Type)
	case s.Type != TypeStalker && s.Device != (Device{}):
		return bad("mac", "Only a Stalker portal has a MAC address and a box's details.")
	case s.Type == TypeM3U && !validURL(s.URL):
		return bad("url", "The address must be an http or https address, or the absolute path of a file.")
	case s.Type == TypeM3U && (s.Username != "" || s.Password != ""):
		return bad("username", "An M3U source has no username or password of its own; its address holds any that it needs.")
	case s.Type == TypeXtream && !validServer(s.URL):
		return bad("url", "The server's address is an http or https address such as http://provider.example:8080, without a username, a password or a page.")
	case s.Type == TypeXtream && !validLogin(s.Username):
		return bad("username", "Enter the username: at most %d bytes of text, without control characters.", maxLogin)
	case s.Type == TypeXtream && !validLogin(s.Password):
		return bad("password", "Enter the password: at most %d bytes of text, without control characters.", maxLogin)
	case s.Type == TypeJellyfin && !validServer(s.URL):
		return bad("url", "The server's address is an http or https address such as http://jellyfin.example:8096, without a username, a password or a page.")
	case s.Type == TypeJellyfin && (!validLogin(s.Password) || strings.ContainsAny(s.Password, " \"\\")):
		return bad("password", "Enter an API key, which Jellyfin's dashboard makes under API Keys.")
	case s.Type == TypeJellyfin && s.Username != "" && !validLogin(s.Username):
		return bad("username", "A user's name is at most %d bytes of text, without control characters.", maxLogin)
	case s.Type == TypePlex && !validServer(s.URL):
		return bad("url", "The server's address is an http or https address such as http://plex.example:32400, without a username, a password or a page.")
	case s.Type == TypePlex && (!validLogin(s.Password) || strings.ContainsAny(s.Password, " \"\\")):
		return bad("password", "Enter a token: the X-Plex-Token of a Plex account that sees the libraries.")
	case s.Type == TypePlex && s.Username != "":
		return bad("username", "A Plex server takes a token, without a username.")
	case s.Type == TypeFolder && !validFolder(s.URL):
		return bad("url", "The folder is the absolute path of a folder on this server, such as /srv/media/films.")
	case s.Type == TypeFolder && (s.Username != "" || s.Password != ""):
		return bad("username", "A folder has no username or password.")
	case s.Type == TypeStalker && !validPortal(s.URL):
		return bad("url", "The portal's address is an http or https address, as the provider gave it, such as http://portal.example/c/, without a username or a password.")
	case s.Type == TypeStalker && !ValidMAC(s.Device.MAC):
		return bad("mac", "Enter the MAC address that the provider registered: six pairs of hex digits, such as 00:1A:79:12:34:56.")
	case s.Type == TypeStalker && (!validDetail(s.Device.Serial, maxDetail) || !validDetail(s.Device.DeviceID, maxDetail) ||
		!validDetail(s.Device.DeviceID2, maxDetail) || !validDetail(s.Device.Signature, maxDetail) || !validDetail(s.Device.Model, maxModel)):
		return bad("device", "A box's serial number, device IDs and signature are at most %d bytes of text each, and its model at most %d, without control characters.", maxDetail, maxModel)
	case s.Type == TypeStalker && s.Password != "" && s.Username == "":
		return bad("username", "Enter the login that goes with the password.")
	case s.Type == TypeStalker && s.Username != "" && (!validLogin(s.Username) || s.Password != "" && !validLogin(s.Password)):
		return bad("username", "A login and its password are at most %d bytes of text each, without control characters.", maxLogin)
	case s.Refresh < MinRefresh || s.Refresh > MaxRefresh:
		return bad("refresh", "A source is read again every %v at the most often, and every %v at the least.", MinRefresh, MaxRefresh)
	case s.MaxConnections < 0 || s.MaxConnections > MaxConnections:
		return bad("max_connections", "The streams at once run from 0, for no limit, to %d.", MaxConnections)
	case len(s.UserAgent) > maxUserAgent || !utf8.ValidString(s.UserAgent) || hasControl(s.UserAgent):
		return bad("user_agent", "A user agent is at most %d bytes of text, without control characters.", maxUserAgent)
	case s.Numbers != NumbersPlaylist && s.Numbers != NumbersProvider:
		return bad("numbers", "Numbers come from the playlist's order (playlist) or from the playlist's own numbers (provider).")
	case s.CatchupZone != "" && s.Type != TypeXtream && s.Type != TypeM3U:
		return bad("catchup_zone", "Only an Xtream server or a playlist has a time zone for its catch-up.")
	case s.CatchupZone != "" && !ValidZone(s.CatchupZone):
		return bad("catchup_zone", "A time zone is a name such as UTC or Europe/London, or empty for the server's own.")
	}
	return nil
}

// ValidName reports whether s can name a source, or a guide of the
// household's own (D139): 1 to 32 lower-case letters, digits, hyphens or
// underscores, starting with a letter or digit.
func ValidName(s string) bool { return namePattern.MatchString(s) }

// ValidAddress reports whether s can be the address of a playlist, or of
// a guide of the household's own (D139): an http or https address with a
// host, or the clean absolute path of a file.
func ValidAddress(s string) bool { return validURL(s) }

// ValidUserAgent reports whether s can be a user agent to send: at most
// 200 bytes of text, without control characters; "" is Overscan's own.
func ValidUserAgent(s string) bool {
	return len(s) <= maxUserAgent && utf8.ValidString(s) && !hasControl(s)
}

// ValidZone reports whether s names a time zone that Overscan knows, such
// as UTC or Europe/London.
func ValidZone(s string) bool {
	if s == "" || len(s) > 64 || s == "Local" || hasControl(s) {
		return false
	}
	_, err := time.LoadLocation(s)
	return err == nil
}

// validPortal reports whether s can be a Stalker portal's address: http or
// https, with a host, and with no credentials or fragment of its own.
func validPortal(s string) bool {
	if s == "" || len(s) > maxURL || hasControl(s) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == ""
}

// ValidMAC reports whether s is a MAC address: six pairs of hex digits,
// with colons or hyphens between them.
func ValidMAC(s string) bool {
	if len(s) != 17 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i%3 == 2 {
			if c != ':' && c != '-' {
				return false
			}
			continue
		}
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// validDetail reports whether s can be a detail of a box: at most n bytes
// of text, without control characters; "" is allowed.
func validDetail(s string, n int) bool {
	return len(s) <= n && utf8.ValidString(s) && !hasControl(s)
}

// validFolder reports whether s is a clean absolute path other than the
// top of the file system.
func validFolder(s string) bool {
	return s != "" && s != "/" && len(s) <= maxURL && !hasControl(s) && strings.HasPrefix(s, "/") &&
		path.Clean(s) == s && utf8.ValidString(s)
}

// validURL reports whether s is an http or https address with a host, or a
// clean absolute path.
func validURL(s string) bool {
	if s == "" || len(s) > maxURL || hasControl(s) {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return path.Clean(s) == s
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != ""
}

// validServer reports whether s is an Xtream server's address: http or
// https, with a host, and with no credentials, query or page of its own.
func validServer(s string) bool {
	if s == "" || len(s) > maxURL || hasControl(s) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil &&
		u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && !strings.HasSuffix(strings.ToLower(u.Path), ".php")
}

// validLogin reports whether s can be an Xtream username or password.
func validLogin(s string) bool {
	return s != "" && len(s) <= maxLogin && utf8.ValidString(s) && !hasControl(s)
}

func hasControl(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f })
}

// StreamURL returns the address of one of the source's streams from ref,
// what the source's catalog keeps for it. For an M3U source, ref is the
// address itself. For an Xtream source, it is the stream's kind and file,
// such as live/1234.ts, and the address puts the login between them. For a
// Jellyfin source, it is the kind and the item's file, such as
// movie/4f2c….mkv, and the address plays the file as it is, with the API
// key. For a Plex source, it is the kind and the path of the file's part,
// escaped as one path segment, such as
// movie/%2Flibrary%2Fparts%2F12%2F1700000000%2Ffile.mkv, and the address
// adds the token to the path. For a Stalker portal, it is what the portal's
// create_link takes, which the stream hub asks the portal for an address
// with as the stream starts (D76), so StreamURL returns it as it is.
func (s Settings) StreamURL(ref string) (string, error) {
	switch s.Type {
	case TypeM3U:
		return ref, nil
	case TypeStalker:
		// The portal makes the address when the stream plays (D76).
		return ref, nil
	case TypeFolder:
		return "", fmt.Errorf("source %s: a folder's videos are files, without an address", s.Name)
	}
	kind, file, ok := strings.Cut(ref, "/")
	if !ok || kind == "" || file == "" || strings.Contains(file, "/") {
		return "", fmt.Errorf("source %s: a stream reference is not in the form kind/file", s.Name)
	}
	if s.Type == TypePlex {
		part, err := url.PathUnescape(file)
		if err != nil || !validPart(part) {
			return "", fmt.Errorf("source %s: a stream reference is not a Plex file's path", s.Name)
		}
		return strings.TrimRight(s.URL, "/") + part + "?" + url.Values{"X-Plex-Token": {s.Password}}.Encode(), nil
	}
	if s.Type == TypeJellyfin {
		id, ext, _ := strings.Cut(file, ".")
		path := "/Videos/" + url.PathEscape(id) + "/stream"
		if ext != "" {
			path += "." + url.PathEscape(ext)
		}
		return strings.TrimRight(s.URL, "/") + path + "?" + url.Values{"static": {"true"}, "ApiKey": {s.Password}}.Encode(), nil
	}
	return strings.TrimRight(s.URL, "/") + "/" + kind + "/" + url.PathEscape(s.Username) + "/" +
		url.PathEscape(s.Password) + "/" + file, nil
}

// TranscodeURL returns the address at which the source's server makes a
// stream of a film or an episode that it transcodes (D71), given the same
// reference as StreamURL: for a Jellyfin server, its stream in MPEG-TS,
// with the key, to which the hub adds what to make. Other sources do not
// transcode, and give "".
func (s Settings) TranscodeURL(ref string) (string, error) {
	if s.Type != TypeJellyfin {
		return "", nil
	}
	_, file, ok := strings.Cut(ref, "/")
	if !ok || file == "" || strings.Contains(file, "/") {
		return "", fmt.Errorf("source %s: a stream reference is not in the form kind/file", s.Name)
	}
	id, _, _ := strings.Cut(file, ".")
	return strings.TrimRight(s.URL, "/") + "/Videos/" + url.PathEscape(id) + "/stream.ts?" +
		url.Values{"ApiKey": {s.Password}}.Encode(), nil
}

// validPart reports whether p is the path of a Plex file's part, as the
// server gives it: under /library/parts/, without a query, a fragment or a
// step up.
func validPart(p string) bool {
	return strings.HasPrefix(p, "/library/parts/") && len(p) <= 512 && !strings.ContainsAny(p, "?#\\") &&
		!strings.Contains(p, "/../") && !strings.HasSuffix(p, "/..") &&
		!strings.ContainsFunc(p, func(r rune) bool { return r <= ' ' || r == 0x7f })
}

// Host returns what the admin shows of an address: its host and port, or
// for a file, its name. It holds no credentials.
func Host(address string) string {
	if strings.HasPrefix(address, "/") {
		return "file " + path.Base(address)
	}
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	return u.Host
}
