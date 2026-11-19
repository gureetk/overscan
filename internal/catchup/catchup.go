// SPDX-License-Identifier: AGPL-3.0-or-later

// Package catchup makes the addresses at which sources play past
// programmes from their archives: an Xtream server's timeshift address, and
// the forms that playlists describe with their catch-up attributes.
// docs/protocol/catchup-notes.md says where each form comes from.
//
// An address can hold the source's login, so no error from this package
// quotes one.
package catchup

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Mode is how a playlist's channel plays its archive.
type Mode string

// Modes that Overscan plays.
const (
	// ModeDefault: the catch-up source is the archive's whole address, with
	// placeholders for the times.
	ModeDefault Mode = "default"
	// ModeAppend: the catch-up source, with its placeholders, goes after
	// the live address.
	ModeAppend Mode = "append"
	// ModeShift: utc and lutc go after the live address.
	ModeShift Mode = "shift"
	// ModeFlussonic: the live address of a Flussonic server, made into one
	// of its archive.
	ModeFlussonic Mode = "flussonic"
	// ModeXtream: the live address of an Xtream server, made into its
	// timeshift address.
	ModeXtream Mode = "xc"
)

// ParseMode returns the mode that a playlist's catchup attribute names, and
// whether it is one that Overscan plays. Other players' names for the same
// modes are accepted.
func ParseMode(s string) (Mode, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "default":
		return ModeDefault, true
	case "append":
		return ModeAppend, true
	case "shift", "timeshift":
		return ModeShift, true
	case "flussonic", "flussonic-hls", "flussonic-ts", "fs":
		return ModeFlussonic, true
	case "xc", "xtream", "xstream":
		return ModeXtream, true
	}
	return "", false
}

// Playlist is what a playlist says of how a channel plays its archive.
type Playlist struct {
	Mode Mode `json:"mode"`
	// Source is the catch-up source: the archive's address for
	// ModeDefault, what goes after the live address for ModeAppend; "" for
	// the other modes. It can hold credentials.
	Source string `json:"source,omitempty"`
	// Correction moves the times put in the address, for an archive whose
	// clock is not the guide's.
	Correction time.Duration `json:"correction,omitempty"`
}

// Archive is how a channel plays past programmes from its source's
// archive: as an Xtream server's stream, or as a playlist says. It can hold
// the source's login, so it never goes to a device or a log.
type Archive struct {
	// Xtream is the stream of an Xtream server's channel; nil for a
	// playlist's.
	Xtream *XtreamStream
	// Live is a playlist's channel's live address, and Playlist what the
	// playlist says of its archive.
	Live     string
	Playlist Playlist
	// Zone is the time zone of the dates and times in the address.
	Zone *time.Location
}

// XtreamStream is a live stream of an Xtream server: the server's address,
// the login, and the stream's ID and extension.
type XtreamStream struct {
	Server, Username, Password string
	ID, Ext                    string
}

// Address returns the address at which the archive plays from from, for
// length, at the time now.
func (a Archive) Address(from time.Time, length time.Duration, now time.Time) (string, error) {
	zone := a.Zone
	if zone == nil {
		zone = time.UTC
	}
	if x := a.Xtream; x != nil {
		return Xtream(x.Server, x.Username, x.Password, x.ID, x.Ext, from, length, zone), nil
	}
	return a.Playlist.Address(a.Live, from, length, now, zone)
}

// Limits of what a playlist may say.
const (
	// MaxDays is the most days of archive a channel is taken to keep.
	MaxDays = 60
	// maxCorrection bounds a correction, as players do: from 12 hours back
	// to 14 ahead.
	minCorrection = -12 * time.Hour
	maxCorrection = 14 * time.Hour
	// maxSource caps a catch-up source.
	maxSource = 4096
)

// FromPlaylist reads how a playlist's channel plays its archive, from its
// entry's attributes and, where the entry has none of its own, the
// header's, given the channel's live address. It returns the days the
// channel keeps, and false for a channel without an archive that Overscan
// can play.
//
// The days are catchup-days, or else the legacy timeshift or tvg-rec, or
// else catchup-time in seconds; a channel that names a mode but no days is
// taken to keep one day. The mode is catchup (or catchup-type); without
// one, a timeshift attribute means shift, a catch-up source means default,
// or append when it starts with ? or &, and a live address on an Xtream
// server means xc.
func FromPlaylist(entry, header map[string]string, live string) (int, Playlist, bool) {
	get := func(name string) string {
		if v, ok := entry[name]; ok && strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
		return strings.TrimSpace(header[name])
	}
	days := 0
	for _, name := range []string{"catchup-days", "timeshift", "tvg-rec"} {
		if n, err := strconv.Atoi(get(name)); err == nil && n > 0 {
			days = n
			break
		}
	}
	if days == 0 {
		if secs, err := strconv.ParseInt(get("catchup-time"), 10, 64); err == nil && secs > 0 {
			days = int(min((secs+86399)/86400, MaxDays))
		}
	}

	p := Playlist{Source: get("catchup-source")}
	if len(p.Source) > maxSource || strings.ContainsFunc(p.Source, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		p.Source = ""
	}
	named := get("catchup")
	if named == "" {
		named = get("catchup-type")
	}
	mode, ok := ParseMode(named)
	switch {
	case ok:
	case named != "":
		return 0, Playlist{}, false // a mode that Overscan does not play, such as vod
	case strings.TrimSpace(entry["timeshift"]) != "":
		mode = ModeShift
	case strings.HasPrefix(p.Source, "?") || strings.HasPrefix(p.Source, "&"):
		mode = ModeAppend
	case p.Source != "":
		mode = ModeDefault
	case days > 0 && isXtreamLive(live):
		mode = ModeXtream
	default:
		return 0, Playlist{}, false
	}
	p.Mode = mode
	if mode != ModeDefault && mode != ModeAppend {
		p.Source = ""
	}
	if (mode == ModeDefault || mode == ModeAppend) && p.Source == "" {
		return 0, Playlist{}, false
	}
	if hours, err := strconv.ParseFloat(get("catchup-correction"), 64); err == nil && !math.IsNaN(hours) {
		c := time.Duration(hours * float64(time.Hour)).Round(time.Second)
		p.Correction = max(minCorrection, min(maxCorrection, c))
	}
	if days == 0 {
		days = 1
	}
	return min(days, MaxDays), p, true
}

// ErrUnsupported is returned for an archive's address that Overscan cannot
// make, such as a template that needs the guide's own catch-up ID.
var ErrUnsupported = errors.New("the source's catch-up cannot be played")

// Address returns the address at which a playlist's channel plays its
// archive from from, for length, given the channel's live address, the
// time now, and the time zone of the dates and times in the address.
func (p Playlist) Address(live string, from time.Time, length time.Duration, now time.Time, zone *time.Location) (string, error) {
	from = from.Add(p.Correction)
	switch p.Mode {
	case ModeDefault:
		return fill(p.Source, from, length, now, zone)
	case ModeAppend:
		tail, err := fill(p.Source, from, length, now, zone)
		if err != nil {
			return "", err
		}
		if strings.HasPrefix(tail, "?") && strings.Contains(live, "?") {
			tail = "&" + tail[1:]
		}
		return live + tail, nil
	case ModeShift:
		sep := "?"
		if strings.Contains(live, "?") {
			sep = "&"
		}
		return live + sep + "utc=" + unix(from) + "&lutc=" + unix(now), nil
	case ModeFlussonic:
		return flussonic(live, from, length)
	case ModeXtream:
		return xtreamFromLive(live, from, length, zone)
	}
	return "", ErrUnsupported
}

// unix returns t as Unix seconds.
func unix(t time.Time) string { return strconv.FormatInt(t.Unix(), 10) }

// Xtream returns an Xtream server's timeshift address (docs/protocol/
// xtream-api-notes.md, section 6) of a stream: the server's address, the
// login, the stream's ID and extension, from from in zone, for length,
// rounded up to whole minutes.
func Xtream(server, username, password, streamID, ext string, from time.Time, length time.Duration, zone *time.Location) string {
	return strings.TrimRight(server, "/") + "/timeshift/" + url.PathEscape(username) + "/" + url.PathEscape(password) +
		"/" + timeshiftTail(streamID, ext, from, length, zone)
}

// timeshiftTail is what follows the login in a timeshift address.
func timeshiftTail(streamID, ext string, from time.Time, length time.Duration, zone *time.Location) string {
	minutes := max(1, int64((length+time.Minute-1)/time.Minute))
	return strconv.FormatInt(minutes, 10) + "/" + from.In(zone).Format("2006-01-02:15-04") + "/" + streamID + "." + ext
}

// XtreamRef returns the stream ID and extension of an Xtream source's live
// channel from what its catalog keeps, such as live/1234.ts.
func XtreamRef(ref string) (id, ext string, ok bool) {
	kind, file, found := strings.Cut(ref, "/")
	if !found || kind != "live" {
		return "", "", false
	}
	id, ext, _ = strings.Cut(file, ".")
	if !digits(id) {
		return "", "", false
	}
	if ext == "" {
		ext = "ts"
	}
	return id, ext, ext == "ts" || ext == "m3u8"
}

// isXtreamLive reports whether live is the address of a live stream on an
// Xtream server: .../live/USER/PASS/ID.ts or .m3u8, or .../USER/PASS/ID.
func isXtreamLive(live string) bool {
	_, _, _, ok := xtreamParts(live)
	return ok
}

// xtreamParts splits an Xtream server's live address into the part before
// the login, the login as written, and the stream's ID and extension.
func xtreamParts(live string) (base *url.URL, login []string, file [2]string, ok bool) {
	u, err := url.Parse(live)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, nil, file, false
	}
	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	n := len(segs)
	if n < 3 {
		return nil, nil, file, false
	}
	id, ext, _ := strings.Cut(segs[n-1], ".")
	if !digits(id) || (ext != "" && ext != "ts" && ext != "m3u8") {
		return nil, nil, file, false
	}
	prefix := segs[:n-3]
	if len(prefix) > 0 && prefix[len(prefix)-1] == "live" {
		prefix = prefix[:len(prefix)-1]
	} else if n > 3 {
		return nil, nil, file, false
	}
	if ext == "" {
		ext = "ts"
	}
	b := *u
	b.RawQuery, b.Fragment = "", ""
	b.Path, b.RawPath = "", ""
	if len(prefix) > 0 {
		b.RawPath = "/" + strings.Join(prefix, "/")
		if p, err := url.PathUnescape(b.RawPath); err == nil {
			b.Path = p
		}
	}
	return &b, segs[n-3 : n-1], [2]string{id, ext}, true
}

// xtreamFromLive makes an Xtream server's timeshift address from the live
// address of one of its streams.
func xtreamFromLive(live string, from time.Time, length time.Duration, zone *time.Location) (string, error) {
	base, login, file, ok := xtreamParts(live)
	if !ok {
		return "", ErrUnsupported
	}
	return strings.TrimRight(base.String(), "/") + "/timeshift/" + login[0] + "/" + login[1] + "/" +
		timeshiftTail(file[0], file[1], from, length, zone), nil
}

// flussonic makes a Flussonic server's archive address from the live
// address of a stream: for HLS, NAME.m3u8 becomes NAME-<from>-<seconds>.m3u8,
// the archive between two times; for MPEG-TS, .../mpegts becomes
// .../timeshift_abs-<from>.ts, the stream from a time on.
func flussonic(live string, from time.Time, length time.Duration) (string, error) {
	u, err := url.Parse(live)
	if err != nil || u.Path == "" {
		return "", ErrUnsupported
	}
	dir, last := "", u.EscapedPath()
	if i := strings.LastIndexByte(last, '/'); i >= 0 {
		dir, last = last[:i+1], last[i+1:]
	}
	switch {
	case last == "mpegts":
		last = "timeshift_abs-" + unix(from) + ".ts"
	case strings.HasSuffix(last, ".m3u8") && len(last) > len(".m3u8"):
		secs := max(1, int64((length+time.Second-1)/time.Second))
		last = strings.TrimSuffix(last, ".m3u8") + "-" + unix(from) + "-" + strconv.FormatInt(secs, 10) + ".m3u8"
	default:
		return "", ErrUnsupported
	}
	path, err := url.PathUnescape(dir + last)
	if err != nil {
		return "", ErrUnsupported
	}
	u.Path, u.RawPath = path, dir+last
	return u.String(), nil
}

// digits reports whether s is a run of 1 to 18 decimal digits.
func digits(s string) bool {
	if s == "" || len(s) > 18 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// fill puts the times into a catch-up source's placeholders:
//
//   - {utc} and ${start}: from, in Unix seconds; {utcend} and ${end}: from
//     plus length; {lutc}, ${now} and ${timestamp}: now;
//   - each of these with a format, such as {utc:Y-m-d} or ${start:YmdHM},
//     where Y, m, d, H, M and S are the year, month, day, hour, minute and
//     second, in zone, and other characters are kept;
//   - {Y}, {m}, {d}, {H}, {M} and {S}: from's, in zone;
//   - {duration} and ${duration}: length in seconds, and {duration:X} that
//     divided by X; {offset:X}: now less from, in seconds, divided by X, and
//     ${offset} that in seconds.
//
// A source that needs {catchup-id}, which the guide would give, cannot be
// filled. Anything else in braces is kept as it is.
func fill(source string, from time.Time, length time.Duration, now time.Time, zone *time.Location) (string, error) {
	end := from.Add(length)
	secs := int64(length / time.Second)
	offset := int64(now.Sub(from) / time.Second)
	var b strings.Builder
	for i := 0; i < len(source); {
		start := i
		dollar := source[i] == '$' && i+1 < len(source) && source[i+1] == '{'
		if dollar {
			i++
		}
		if source[i] != '{' {
			b.WriteByte(source[start])
			i = start + 1
			continue
		}
		close := strings.IndexByte(source[i:], '}')
		if close < 0 {
			b.WriteString(source[start:])
			break
		}
		token := source[i+1 : i+close]
		i += close + 1
		name, format, hasFormat := strings.Cut(token, ":")
		var t time.Time
		var number int64
		isTime, isNumber := false, false
		switch {
		case name == "utc" && !dollar, name == "start" && dollar:
			t, isTime = from, true
		case name == "utcend" && !dollar, name == "end" && dollar:
			t, isTime = end, true
		case name == "lutc" && !dollar, (name == "now" || name == "timestamp") && dollar:
			t, isTime = now, true
		case name == "duration" && !hasFormat:
			number, isNumber = secs, true
		case name == "duration" && !dollar:
			number, isNumber = divide(secs, format)
		case name == "offset" && dollar && !hasFormat:
			number, isNumber = offset, true
		case name == "offset" && !dollar && hasFormat:
			number, isNumber = divide(offset, format)
		case len(name) == 1 && !dollar && !hasFormat && strings.Contains("YmdHMS", name):
			b.WriteString(datePart(from.In(zone), name[0]))
			continue
		case name == "catchup-id" && !dollar:
			return "", ErrUnsupported
		}
		switch {
		case isTime && hasFormat:
			b.WriteString(dateFormat(t.In(zone), format))
		case isTime:
			b.WriteString(unix(t))
		case isNumber:
			b.WriteString(strconv.FormatInt(number, 10))
		default:
			b.WriteString(source[start:i]) // not a placeholder of ours
		}
	}
	return b.String(), nil
}

// divide returns n divided by the divisor that s gives, if it is a positive
// whole number.
func divide(n int64, s string) (int64, bool) {
	d, err := strconv.ParseInt(s, 10, 64)
	if err != nil || d <= 0 {
		return 0, false
	}
	return n / d, true
}

// dateFormat writes t in a format where Y, m, d, H, M and S stand for its
// parts.
func dateFormat(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		if strings.IndexByte("YmdHMS", format[i]) >= 0 {
			b.WriteString(datePart(t, format[i]))
		} else {
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// datePart returns one part of t: Y the year in four digits, m the month,
// d the day, H the hour, M the minute and S the second, in two.
func datePart(t time.Time, c byte) string {
	switch c {
	case 'Y':
		return fmt.Sprintf("%04d", t.Year())
	case 'm':
		return fmt.Sprintf("%02d", int(t.Month()))
	case 'd':
		return fmt.Sprintf("%02d", t.Day())
	case 'H':
		return fmt.Sprintf("%02d", t.Hour())
	case 'M':
		return fmt.Sprintf("%02d", t.Minute())
	}
	return fmt.Sprintf("%02d", t.Second())
}
