// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"overscan.gureet.ca/internal/store"
)

// The support bundle (plan, section 18): a zip file of what helps with a
// bug report: the server's version and settings, the state of its sources,
// lineup and devices, what is playing, the capture log and the last lines of
// the log. It holds no addresses, logins or MAC addresses beyond their
// maker's part, so that it can be attached to a report, but the page that
// offers it says to read it first.

// Support is what the support bundle needs from outside the admin.
type Support struct {
	// Log returns the server's last log lines, oldest first.
	Log func() []string
	// Facts are about the server: its configuration, and what it found
	// when it started.
	Facts []Fact
	// Listen is the address the server listens on for devices, such as
	// ":8080", from which the Settings page says where boxes find it.
	Listen string
}

// Fact is something about the server, named.
type Fact struct{ Name, Value string }

// UseSupport gives the admin what the support bundle needs from outside it.
// Without it, the bundle has no log and no facts.
func (a *Admin) UseSupport(s Support) { a.support = s }

// supportBundle serves the bundle as a download.
func (a *Admin) supportBundle(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	data, err := a.bundle(r.Context())
	if err != nil {
		a.log.Error("cannot make the support bundle", "error", err)
		a.message(w, r, http.StatusInternalServerError, "Something went wrong", "The server's log says what.", &s)
		return
	}
	name := "overscan-support-" + a.now().UTC().Format("20060102-150405") + ".zip"
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// bundle makes the support bundle.
func (a *Admin) bundle(ctx context.Context) ([]byte, error) {
	now := a.now().UTC()
	files := []struct {
		name string
		make func(context.Context, time.Time) (string, error)
	}{
		{"README.txt", a.bundleReadme},
		{"overscan.txt", a.bundleServer},
		{"sources.txt", a.bundleSources},
		{"lineup.txt", a.bundleLineup},
		{"devices.txt", a.bundleDevices},
		{"playing.txt", a.bundlePlaying},
		{"capture.txt", a.bundleCapture},
		{"log.txt", a.bundleLog},
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		text, err := f.make(ctx, now)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.name, err)
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: f.name, Method: zip.Deflate, Modified: now})
		if err != nil {
			return nil, err
		}
		if _, err := fw.Write([]byte(redact(text))); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (a *Admin) bundleReadme(_ context.Context, now time.Time) (string, error) {
	return "Overscan support bundle, made " + now.Format(time.RFC3339) + `.

It holds the server's version and settings, the state of its sources,
lineup and devices, what boxes' playback checks found, what was playing,
the capture log, and the last lines of the server's log. Source addresses, logins, IP addresses and host names are
left out, and MAC addresses keep only their first half, which names the
maker. Channel names, device labels and source names stay in.

Read it before you share it.
`, nil
}

func (a *Admin) bundleServer(ctx context.Context, now time.Time) (string, error) {
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Version\t%s\n", a.version)
	fmt.Fprintf(tw, "Built with\t%s, for %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(tw, "Started\t%s, %s ago\n", a.started.Format(time.RFC3339), now.Sub(a.started).Round(time.Second))
	v, err := a.store.SchemaVersion(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(tw, "Database schema\t%d\n", v)
	for _, f := range a.support.Facts {
		fmt.Fprintf(tw, "%s\t%s\n", f.Name, f.Value)
	}
	fmt.Fprintf(tw, "Stream repair\t%s\n", map[bool]string{true: "available", false: "not available"}[a.canRepair()])
	transcoding, err := a.transcodeFact(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(tw, "Transcoding\t%s\n", transcoding)
	convert, err := a.store.ConvertsSound(ctx)
	if err != nil {
		return "", err
	}
	server, err := a.store.ServerTranscodes(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(tw, "Sound conversion\t%s\n", map[bool]string{true: "on", false: "off"}[convert])
	fmt.Fprintf(tw, "Jellyfin transcodes\t%s\n", map[bool]string{true: "on", false: "off"}[server])
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(tw, "Memory\t%.1f MB in use, %.1f MB from the system\n", float64(m.HeapAlloc)/(1<<20), float64(m.Sys)/(1<<20))
	fmt.Fprintf(tw, "Goroutines\t%d\n", runtime.NumGoroutine())
	fmt.Fprintf(tw, "CPUs\t%d\n", runtime.NumCPU())
	return flushed(&b, tw)
}

// transcodeFact says whether this server's ffmpeg transcodes the video of
// films that a box cannot play (D73), and how many at a time.
func (a *Admin) transcodeFact(ctx context.Context) (string, error) {
	can, toneMap, missing := a.transcoder()
	if !can {
		return "not available: no " + missing, nil
	}
	n, err := a.store.Transcodes(ctx)
	if err != nil {
		return "", err
	}
	fact := "available, with tone mapping"
	if !toneMap {
		fact = "available, without tone mapping (no zscale filter)"
	}
	if n == 0 {
		return fact + "; off", nil
	}
	return fmt.Sprintf("%s; %d at a time", fact, n), nil
}

func (a *Admin) bundleSources(ctx context.Context, now time.Time) (string, error) {
	srcs, err := a.store.Sources(ctx)
	if err != nil {
		return "", err
	}
	if len(srcs) == 0 {
		return "No sources.\n", nil
	}
	reads, err := a.libraryReads(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, src := range srcs {
		row := sourceRow{Source: src, Now: now, Films: reads[src.ID][store.KindMovie], Series: reads[src.ID][store.KindSeries]}
		fmt.Fprintf(&b, "%s (%s, ID %d)\n", src.Name, src.Type, src.ID)
		tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "  Enabled\t%v\n", src.Enabled)
		fmt.Fprintf(tw, "  Address\t%s\n", map[store.AddressState]string{
			store.AddressSaved: "saved", store.AddressMissing: "missing", store.AddressLocked: "saved with another key file",
		}[src.Address])
		fmt.Fprintf(tw, "  Own user agent\t%v\n", src.UserAgent != "")
		fmt.Fprintf(tw, "  Streams at once\t%s\n", row.Limit())
		fmt.Fprintf(tw, "  Read every\t%s\n", row.Every())
		fmt.Fprintf(tw, "  Numbers\t%s\n", src.Numbers)
		fmt.Fprintf(tw, "  New categories hidden\t%v\n", src.NewCategoriesHidden)
		fmt.Fprintf(tw, "  Channels\t%s\n", count(src.Channels))
		if src.RefreshedAt.IsZero() {
			fmt.Fprintf(tw, "  Last read\tnever\n")
		} else {
			fmt.Fprintf(tw, "  Last read\t%s (%s)\n", src.RefreshedAt.UTC().Format(time.RFC3339), row.LastRead())
		}
		if src.RefreshError != "" {
			fmt.Fprintf(tw, "  Last error\t%s\n", src.RefreshError)
		}
		if line := row.AccountLine(); line != "" {
			fmt.Fprintf(tw, "  Account\t%s\n", line)
		}
		if src.Account.TimeZone != "" {
			fmt.Fprintf(tw, "  Server's time zone\t%s\n", src.Account.TimeZone)
		}
		if src.CatchupZone != "" {
			fmt.Fprintf(tw, "  Catch-up time zone\t%s\n", src.CatchupZone)
		}
		if g := src.Guide; !row.LibraryOnly() {
			fmt.Fprintf(tw, "  Guide\t%s\n", row.GuideLine())
			if !g.ReadAt.IsZero() {
				fmt.Fprintf(tw, "  Guide's programmes\t%s, for %s channels\n", count(g.Programmes), count(g.Channels))
			}
			if g.Error != "" {
				fmt.Fprintf(tw, "  Guide's last error\t%s\n", g.Error)
			}
		}
		if row.HasLibrary() {
			fmt.Fprintf(tw, "  Films\t%s\n", row.FilmsLine())
			fmt.Fprintf(tw, "  Series\t%s\n", row.SeriesLine())
		}
		if err := tw.Flush(); err != nil {
			return "", err
		}
		b.WriteString("\n")
	}
	return b.String(), nil
}

func (a *Admin) bundleLineup(ctx context.Context, _ time.Time) (string, error) {
	lineup, err := a.lineupID(ctx)
	if err != nil {
		return "", err
	}
	groups, err := a.store.LineupGroups(ctx, lineup)
	if err != nil {
		return "", err
	}
	entries, _, err := a.store.LineupEntries(ctx, lineup, store.EntryQuery{})
	if err != nil {
		return "", err
	}
	var shown, missing, pinned, renamed, moved, locked, repaired, repairOn, repairOff, adult, radio int
	for _, e := range entries {
		count := func(n *int, yes bool) {
			if yes {
				*n++
			}
		}
		count(&shown, !e.Hidden && !e.AdultHidden)
		count(&missing, e.Missing)
		count(&pinned, e.Pinned)
		count(&renamed, e.Renamed)
		count(&moved, e.Moved)
		count(&locked, e.Locked || e.GroupLocked || e.AdultLocked)
		count(&adult, e.Adult)
		count(&radio, e.Radio)
		count(&repaired, e.Repair)
		count(&repairOn, e.RepairOwn != nil && *e.RepairOwn)
		count(&repairOff, e.RepairOwn != nil && !*e.RepairOwn)
	}
	repairDefault, err := a.store.RepairDefault(ctx)
	if err != nil {
		return "", err
	}
	repairHLS, err := a.store.RepairHLS(ctx)
	if err != nil {
		return "", err
	}
	pin, err := a.pinSet(ctx)
	if err != nil {
		return "", err
	}
	marks, err := a.store.AdultMarks(ctx)
	if err != nil {
		return "", err
	}
	libraryLocks, err := a.store.LibraryLocks(ctx)
	if err != nil {
		return "", err
	}
	radioApart, err := a.store.RadioApart(ctx)
	if err != nil {
		return "", err
	}
	tmdbKey, err := a.store.TMDBKey(ctx)
	if err != nil {
		return "", err
	}
	tmdbFound, tmdbMissed, err := a.store.TMDBCounts(ctx)
	if err != nil {
		return "", err
	}
	recordingsCap, err := a.store.RecordingsCap(ctx)
	if err != nil {
		return "", err
	}
	recordings, err := a.store.RecordingCounts(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Channels: %s, %s shown, %s missing from their source\n", count(len(entries)), count(shown), count(missing))
	fmt.Fprintf(&b, "Edited: %d numbers pinned, %d renamed, %d moved to another group\n", pinned, renamed, moved)
	fmt.Fprintf(&b, "Locked: %d channels, parental PIN set: %v\n", locked, pin)
	fmt.Fprintf(&b, "Marked for adults by their sources: %d channels; what marks do: %s\n", adult, marks)
	fmt.Fprintf(&b, "Locks on shelves of films and series, and on titles: %d\n", libraryLocks)
	fmt.Fprintf(&b, "Radio stations: %d; on the Radio screen alone: %v\n", radio, radioApart)
	fmt.Fprintf(&b, "Details from TMDB: key set: %v; titles found: %d, without a match: %d\n", tmdbKey != "", tmdbFound, tmdbMissed)
	fmt.Fprintf(&b, "Recordings: room: %d GB (0 is off); the server records: %v; to come: %d, under way: %d, made: %d, failed: %d, called off: %d\n",
		recordingsCap, a.recorder != nil, recordings[store.RecordingScheduled], recordings[store.RecordingRecording],
		recordings[store.RecordingDone], recordings[store.RecordingFailed], recordings[store.RecordingCancelled])
	fmt.Fprintf(&b, "Stream repair: every channel by default: %v; HLS too: %v; %d channels set to be repaired, %d set not to be; %s repaired in all\n",
		repairDefault, repairHLS, repairOn, repairOff, count(repaired))
	fmt.Fprintf(&b, "\nGroups: %d\n", len(groups))
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tCHANNELS\tSHOWN\tHIDDEN\tLOCKED\tNAME")
	for _, g := range groups {
		fmt.Fprintf(tw, "%d\t%d\t%d\t%v\t%v\t%s\n", g.ID, g.Channels, g.Shown, g.Hidden, g.Locked, g.Name)
	}
	return flushed(&b, tw)
}

func (a *Admin) bundleDevices(ctx context.Context, now time.Time) (string, error) {
	devices, err := a.store.Devices(ctx)
	if err != nil {
		return "", err
	}
	if len(devices) == 0 {
		return "No devices.\n", nil
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tMAC\tSTATE\tPAIRED\tMODEL\tLAST SEEN\tLABEL")
	byID := map[int64]store.Device{}
	for _, d := range devices {
		byID[d.ID] = d
		fmt.Fprintf(tw, "%d\t%s\t%s\t%v\t%s\t%s\t%s\n", d.ID, d.MAC, d.State, d.Paired(), d.Model, ago(d.LastSeen, now), d.Label)
	}
	if err := tw.Flush(); err != nil {
		return "", err
	}
	checks, err := a.store.PlaybackChecks(ctx)
	if err != nil {
		return "", err
	}
	writeChecks(&b, checks, byID, now)
	return b.String(), nil
}

// writeChecks writes what boxes' playback checks found (D70), the latest
// first, with what the page found of each clip.
func writeChecks(b *strings.Builder, checks map[int64]store.PlaybackCheck, devices map[int64]store.Device, now time.Time) {
	if len(checks) == 0 {
		return
	}
	list := slices.SortedFunc(maps.Values(checks), func(x, y store.PlaybackCheck) int {
		return y.CheckedAt.Compare(x.CheckedAt)
	})
	b.WriteString("\nPlayback checks, the latest first:\n")
	for _, c := range list {
		firmware := c.Firmware
		if firmware == "" {
			firmware = "not said"
		}
		fmt.Fprintf(b, "\n%s, %s, firmware %s, checked %s\n", deviceName(devices[c.DeviceID]), c.Model, firmware, ago(c.CheckedAt, now))
		v := viewOf(c)
		fmt.Fprintf(b, "  Plays: %s\n  Does not play: %s\n", strings.Join(v.Plays, ", "), strings.Join(v.Not, ", "))
		var results []struct {
			ID       string  `json:"id"`
			Answer   string  `json:"answer"`
			Started  bool    `json:"started"`
			Failed   bool    `json:"failed"`
			Ms       int     `json:"ms"`
			Width    int     `json:"width"`
			Height   int     `json:"height"`
			Tracks   int     `json:"tracks"`
			Position float64 `json:"position"`
			Video    string  `json:"video"`
			Audio    string  `json:"audio"`
			Events   string  `json:"events"`
		}
		if err := json.Unmarshal(c.Results, &results); err != nil {
			continue
		}
		for _, r := range results {
			answer := r.Answer
			if answer == "" {
				answer = "not answered"
			}
			switch {
			case r.Failed:
				fmt.Fprintf(b, "  %s: %s; the box could not open it\n", r.ID, answer)
			case !r.Started:
				fmt.Fprintf(b, "  %s: %s; the box did not start it in %d ms\n", r.ID, answer, r.Ms)
			default:
				fmt.Fprintf(b, "  %s: %s; started in %d ms, picture %dx%d, %d %s, at %.1f s\n",
					r.ID, answer, r.Ms, r.Width, r.Height, r.Tracks, plural(r.Tracks, "sound track"), r.Position)
			}
			if r.Video != "" || r.Audio != "" {
				fmt.Fprintf(b, "    the player said %s of the picture and %s of the sound\n", r.Video, r.Audio)
			}
			if r.Events != "" {
				fmt.Fprintf(b, "    the player's events: %s\n", r.Events)
			}
		}
	}
}

func (a *Admin) bundlePlaying(ctx context.Context, _ time.Time) (string, error) {
	rows, err := a.streamRows(ctx)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "Nothing was playing.\n", nil
	}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "CHANNEL\tDEVICE\tSOURCE\tSTARTED\tRECEIVED\tHLS\tREPAIRED\tCONVERTED\tTRANSCODED\tPLAYING")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%v\t%v\t%v\t%v\t%v\n", r.Channel, r.Device, r.Source, r.Since(), r.Received(),
			r.HLS, r.Repaired, r.Converted, r.Transcoded, r.Playing)
	}
	return flushed(&b, tw)
}

func (a *Admin) bundleCapture(ctx context.Context, _ time.Time) (string, error) {
	caps, err := a.store.Captures(ctx)
	if err != nil {
		return "", err
	}
	if len(caps) == 0 {
		return "No requests in the capture log.\n", nil
	}
	var b strings.Builder
	for _, c := range caps {
		fmt.Fprintf(&b, "%s/%s (%s): %d times, %s to %s\n", c.Module, c.Action, c.Params, c.Count,
			c.FirstSeen.UTC().Format(time.RFC3339), c.LastSeen.UTC().Format(time.RFC3339))
		fmt.Fprintf(&b, "  %s\n  user agent: %s\n", c.Example, c.UserAgent)
	}
	return b.String(), nil
}

func (a *Admin) bundleLog(context.Context, time.Time) (string, error) {
	if a.support.Log == nil {
		return "No log kept.\n", nil
	}
	lines := a.support.Log()
	if len(lines) == 0 {
		return "Nothing logged yet.\n", nil
	}
	return strings.Join(lines, "\n") + "\n", nil
}

// What redact leaves out.
var (
	macPattern   = regexp.MustCompile(`(?i)\b((?:[0-9a-f]{2}[:-]){2}[0-9a-f]{2})([:-])[0-9a-f]{2}[:-][0-9a-f]{2}[:-][0-9a-f]{2}\b`)
	ipv4Pattern  = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Pattern  = regexp.MustCompile(`\[[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*\]`)
	urlPattern   = regexp.MustCompile(`(?i)\b(?:https?|rtmp|rtsp|udp|ftp)://[^\s"']+`)
	lookupHost   = regexp.MustCompile(`\blookup [^\s":]+`)
	certHosts    = regexp.MustCompile(`certificate is valid for [^"]*?, not [^\s"]+`)
	secretParams = regexp.MustCompile(`(?i)\b(\w*(?:password|passwd|username|token|auth|signature|secret)\w*|user|key|mac|sn)=([^&\s"]+)`)
)

// redact leaves out of text what could identify a provider, an account or
// a household: addresses, logins and most of each MAC address.
func redact(text string) string {
	text = urlPattern.ReplaceAllString(text, "[address]")
	text = secretParams.ReplaceAllStringFunc(text, func(m string) string {
		name, value, _ := strings.Cut(m, "=")
		if value == "[redacted]" {
			return m
		}
		if macPattern.MatchString(value) {
			return name + "=" + macPattern.ReplaceAllString(value, "${1}${2}xx${2}xx${2}xx")
		}
		return name + "=[redacted]"
	})
	text = macPattern.ReplaceAllString(text, "${1}${2}xx${2}xx${2}xx")
	text = ipv6Pattern.ReplaceAllString(text, "[IPv6 address]")
	text = ipv4Pattern.ReplaceAllStringFunc(text, func(m string) string {
		ip := net.ParseIP(m)
		switch {
		case ip == nil:
			return m
		case ip.IsLoopback():
			return "[loopback]"
		case ip.IsPrivate() || ip.IsLinkLocalUnicast():
			return "[local address]"
		}
		return "[address]"
	})
	text = lookupHost.ReplaceAllString(text, "lookup [host]")
	return certHosts.ReplaceAllString(text, "certificate is valid for [hosts], not [host]")
}

// flushed flushes tw, which writes to b, and returns what b holds.
func flushed(b *strings.Builder, tw *tabwriter.Writer) (string, error) {
	if err := tw.Flush(); err != nil {
		return "", err
	}
	return b.String(), nil
}
