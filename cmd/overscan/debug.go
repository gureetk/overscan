// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/diagnose"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/upstream"
)

const debugUsage = `Usage:
  overscan debug playlist
  overscan debug channel [-for DURATION] [-repair on|off] NUMBER

playlist prints the address of the debug playlist: the household lineup as
an M3U playlist, for testing playback in a player such as VLC. The address
holds the admin token, so keep it to yourself.

channel checks the channel with that number in the household lineup: what
its source answers, and what a player gets through the stream hub, which
plays it for 30 seconds or DURATION, such as 2m. The report names no
addresses, so it can be shared. The check takes one of the source's
connections, which the server does not count: on a source that allows only
one, stop playing it on other devices first. On a Stalker portal, the check
signs in as the box in a session of its own, which replaces the server's
until the server signs in again. The stream goes through ffmpeg
if the channel is set to be repaired; -repair on or off decides that for
the check alone, to compare the two.

Run these as the user the server runs as, after the server has started once.
`

// maxCheck caps how long debug channel plays a channel.
const maxCheck = 10 * time.Minute

// debugCommand runs "overscan debug ..." and returns the exit code.
func debugCommand(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help" || args[0] == "help"):
		fmt.Fprint(stdout, debugUsage)
		return 0
	case len(args) == 1 && args[0] == "playlist":
		return debugPlaylist(cfg, stdout, stderr)
	case len(args) > 0 && args[0] == "channel":
		return debugChannel(ctx, cfg, args[1:], stdout, stderr)
	}
	fmt.Fprint(stderr, debugUsage)
	return 2
}

// debugPlaylist prints the address of the debug playlist.
func debugPlaylist(cfg config.Config, stdout, stderr io.Writer) int {
	path := filepath.Join(cfg.DataDir, admin.TokenFile)
	fi, err := os.Stat(path)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: no admin token at %s; start the server once to create it, or check data_dir in the configuration\n", path)
		return 1
	}
	if err := checkOwner(path, fi, os.Geteuid()); err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	token, err := admin.ReadToken(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}

	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: listen %q: %v\n", cfg.Listen, err)
		return 1
	}
	known := host != "" && !net.ParseIP(host).IsUnspecified()
	if !known {
		host = "SERVER"
	}
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(host, port), Path: "/debug/lineup.m3u", RawQuery: url.Values{"token": {token}}.Encode()}
	fmt.Fprintln(stdout, u.String())
	if !known {
		fmt.Fprintln(stderr, "Put the server's address in place of SERVER, then open the address in a player such as VLC.")
	}
	return 0
}

// debugChannel checks a channel and prints the report.
func debugChannel(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("debug channel", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	length := flags.Duration("for", 30*time.Second, "")
	repairFlag := flags.String("repair", "", "")
	// The number may come before or after the flag.
	var positional []string
	for {
		if err := flags.Parse(args); err != nil {
			fmt.Fprint(stderr, debugUsage)
			return 2
		}
		if flags.NArg() == 0 {
			break
		}
		positional = append(positional, flags.Arg(0))
		args = flags.Args()[1:]
	}
	if len(positional) != 1 {
		fmt.Fprint(stderr, debugUsage)
		return 2
	}
	if *length <= 0 || *length > maxCheck {
		fmt.Fprintf(stderr, "overscan: -for must be more than 0 and at most %s\n", maxCheck)
		return 2
	}
	which := hub.RepairSet
	switch *repairFlag {
	case "":
	case "on":
		which = hub.RepairAll
	case "off":
		which = hub.RepairNone
	default:
		fmt.Fprintln(stderr, "overscan: -repair is on or off")
		return 2
	}
	number, err := strconv.Atoi(positional[0])
	if err != nil || number <= 0 {
		fmt.Fprintf(stderr, "overscan: %q is not a channel number\n", positional[0])
		return 2
	}

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := openExistingStore(ctx, cfg, quiet)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	defer st.Close()
	if err := useKey(st, cfg); err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	entry, err := lineupEntry(ctx, st, number)
	if errors.Is(err, store.ErrNoKey) {
		fmt.Fprintf(stderr, "overscan: there is no key file at %s to read the channel's stream address with; the server makes it when it starts\n", cfg.KeyPath())
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	sources, err := st.Sources(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	known := hubSources(sources)
	sourceName := known[entry.SourceID].Name
	userAgent := known[entry.SourceID].UserAgent
	if userAgent == "" {
		userAgent = hub.DefaultUserAgent(version)
	}

	fmt.Fprintf(stderr, "Checking channel %d, %s, for %s...\n", number, entry.Name, *length)
	var logs bytes.Buffer
	hubLog := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey && len(groups) == 0 {
				return slog.Attr{}
			}
			return a
		},
	}))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	report := diagnose.Report{Number: number, Name: entry.Name, Source: sourceName, For: *length}
	streams := hub.New(st, hubLog, version, known)
	if known[entry.SourceID].Resolves {
		// A portal gives a channel's address only as it plays (D76): the
		// check asks for one in a session of its own, and the hub asks
		// for another.
		links := upstream.New(st, stalker.NewPool(nil), hubLog)
		streams.UseResolver(links)
		if res, err := links.Resolve(ctx, entry.SourceID, hub.PlaysChannel, entry.StreamURL); err != nil {
			report.Upstream = diagnose.SourceReport{Err: "the portal gave no address: " + err.Error()}
		} else {
			report.Upstream = diagnose.Source(ctx, &http.Client{Transport: transport}, res.Address, userAgent)
		}
	} else {
		report.Upstream = diagnose.Source(ctx, &http.Client{Transport: transport}, entry.StreamURL, userAgent)
	}
	repaired := which == hub.RepairAll || (which == hub.RepairSet && entry.Repair)
	switch {
	case repaired:
		if _, v := useFFmpeg(ctx, streams, cfg.FFmpeg, which, hubLog); v != "" {
			report.Repair = "Repair on: the stream goes through ffmpeg " + v + ", unless the hub's log says otherwise."
		} else {
			report.Repair = "Repair on, but there is no ffmpeg to run, so the stream goes as it is."
		}
	case entry.Repair:
		report.Repair = "Repair off for this check, though the channel is set to be repaired."
	}
	report.Player = diagnose.Player(ctx, streams, entry, *length)
	for line := range strings.Lines(logs.String()) {
		report.Log = append(report.Log, strings.TrimSpace(line))
	}
	if _, err := report.WriteTo(stdout); err != nil {
		return 1
	}
	return 0
}

// lineupEntry returns the channel with the given number in the lineup that
// the household account sees.
func lineupEntry(ctx context.Context, st *store.Store, number int) (store.PlayableEntry, error) {
	acct, err := st.Account(ctx, store.HouseholdAccount)
	if err != nil {
		return store.PlayableEntry{}, err
	}
	channels, err := st.LineupChannels(ctx, acct.LineupID)
	if err != nil {
		return store.PlayableEntry{}, err
	}
	for _, c := range channels {
		if c.Number == number {
			return st.PlayableEntry(ctx, c.EntryID)
		}
	}
	return store.PlayableEntry{}, fmt.Errorf("no channel %d in the lineup", number)
}
