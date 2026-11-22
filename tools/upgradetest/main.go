// SPDX-License-Identifier: AGPL-3.0-or-later

// Command upgradetest checks that an upgrade keeps what a household set up
// (D117). For each older release it is given, it runs that release on a new
// data directory, against a fictional provider, and sets up a household
// through the release's own APIs: sources, lineup edits, devices, a box's
// favourites, settings and resume points, recordings, and, as far as the
// release has them, profiles, an end date, messages, a theme, accounts with
// logins, lineups, rules and backups. It records what the admin's API, a
// box, a Stalker client and players see, restarts the release to learn what
// changes on its own across a restart, and then runs this build on the same
// data directory. Every answer must still be there and the same; the
// database must have been backed up before its schema changed; and the
// upgraded database must keep every row it had, with the same schema as a
// new database's.
//
//	go run ./tools/upgradetest overscan-0.3.0 overscan-0.4.0 overscan-0.5.0
//
// Each argument is an older release's binary. It needs nothing else, and
// runs on Linux and macOS. With -chain, the household that the first sets
// up goes through each of the others in turn, in the order given, and then
// to this build, as the recommended path from an old release does: one
// release at a time.
//
//	go run ./tools/upgradetest -chain overscan-0.3.0 overscan-0.4.0 overscan-0.5.0
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func main() {
	newBin := flag.String("new", "", "the Overscan binary to upgrade to (default: build it from this repository)")
	keep := flag.Bool("keep", false, "keep each upgrade's folder, with the servers' logs and databases")
	flag.BoolVar(&verbose, "v", false, "also list the values left out because they change by themselves across a restart")
	chain := flag.Bool("chain", false, "upgrade the household that the first older release sets up through each of the others in turn, then to this build")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: upgradetest [-new BINARY] [-keep] [-chain] OLD_BINARY...\n\n"+
			"Upgrades a household that each older release sets up to this build, and checks that it keeps\n"+
			"what the household set up. With -chain, one household goes through the older releases in the\n"+
			"order given, then to this build.\n\n")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	bin := *newBin
	if bin == "" {
		dir, err := os.MkdirTemp("", "overscan-upgradetest-")
		if err != nil {
			fail(err)
		}
		defer os.RemoveAll(dir)
		bin = filepath.Join(dir, "overscan")
		fmt.Println("building Overscan")
		build := exec.Command("go", "build", "-o", bin, "overscan.gureet.ca/cmd/overscan")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			fail(fmt.Errorf("build Overscan (run this in the repository, or give -new): %w", err))
		}
	}
	newVersion, err := versionOf(bin)
	if err != nil {
		fail(err)
	}

	var versions []string
	for _, old := range flag.Args() {
		version, err := versionOf(old)
		if err != nil {
			fail(err)
		}
		versions = append(versions, version)
	}
	if *chain {
		fmt.Printf("upgrading from %s to %s\n", strings.Join(versions, ", then "), newVersion)
		report, err := upgrade(ctx, flag.Args(), versions, bin, *keep)
		if err != nil {
			fail(fmt.Errorf("from %s: %w", versions[0], err))
		}
		fmt.Print(report)
		return
	}
	failed := false
	for i, old := range flag.Args() {
		fmt.Printf("upgrading from %s to %s\n", versions[i], newVersion)
		report, err := upgrade(ctx, []string{old}, versions[i:i+1], bin, *keep)
		if err != nil {
			fmt.Fprintf(os.Stderr, "upgradetest: from %s: %v\n", versions[i], err)
			failed = true
			continue
		}
		fmt.Print(report)
	}
	if failed {
		os.Exit(1)
	}
}

// verbose lists what each upgrade leaves out.
var verbose bool

func fail(err error) {
	fmt.Fprintln(os.Stderr, "upgradetest:", err)
	os.Exit(1)
}

func versionOf(bin string) (string, error) {
	out, err := exec.Command(bin, "-version").Output()
	if err != nil {
		return "", fmt.Errorf("%s -version: %w", bin, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// writeConfig writes a configuration file for a data directory, with the
// provider's playlist as the source main.
func writeConfig(path, listen, data string, p *provider) error {
	text := fmt.Sprintf("listen: %q\ndata_dir: %q\nlog:\n  level: debug\n", listen, data)
	if p != nil {
		text += fmt.Sprintf("sources:\n  - name: main\n    type: m3u\n    url: %q\n    max_connections: 4\n", p.url+"/main.m3u")
	}
	return os.WriteFile(path, []byte(text), 0o600)
}

// maxProblems is how many problems an upgrade reports, at most. A change
// early in a list moves the rest of it, which would report every element.
const maxProblems = 40

// upgrade sets up a household with the first of the older releases in
// path, whose versions are versions, upgrades it through the others in
// turn, then to newBin, and returns what it checked.
func upgrade(ctx context.Context, path, versions []string, newBin string, keep bool) (string, error) {
	oldBin, oldVersion := path[0], versions[0]
	dir, err := os.MkdirTemp("", "overscan-upgrade-")
	if err != nil {
		return "", err
	}
	if keep {
		defer fmt.Printf("  its folder: %s\n", dir)
	} else {
		defer os.RemoveAll(dir)
	}
	media := filepath.Join(dir, "media", "films")
	if err := writeLibrary(media); err != nil {
		return "", err
	}
	p, err := startProvider()
	if err != nil {
		return "", err
	}
	defer p.Close()
	port, err := freePort()
	if err != nil {
		return "", err
	}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	data := filepath.Join(dir, "data")
	cfg := filepath.Join(dir, "overscan.yaml")
	if err := writeConfig(cfg, fmt.Sprintf("127.0.0.1:%d", port), data, p); err != nil {
		return "", err
	}
	h := &household{f: featuresOf(oldVersion), p: p, media: media}

	// The household, as the old release sets it up.
	srv, err := start(ctx, oldBin, cfg, base, filepath.Join(dir, "old.log"))
	if err != nil {
		return "", err
	}
	before, order, err := func() (map[string]any, []string, error) {
		defer srv.stop()
		if _, err := command(oldBin, cfg, adminPassword+"\n", "admin", "password"); err != nil {
			return nil, nil, err
		}
		a, err := signIn(base, "admin", adminPassword)
		if err != nil {
			return nil, nil, err
		}
		if err := h.setUp(a, base, srv.started); err != nil {
			return nil, nil, err
		}
		return h.snapshot(a, base)
	}()
	if err != nil {
		return "", fmt.Errorf("%s: %w (its log: %s)", oldVersion, err, filepath.Join(dir, "old.log"))
	}

	// The old release again, to learn what changes across a restart by
	// itself, such as when a device was last seen. Times in answers count
	// in seconds, so a second at least goes by first.
	time.Sleep(1100 * time.Millisecond)
	again, err := restartAndLook(ctx, h, oldBin, cfg, base, filepath.Join(dir, "old-again.log"))
	if err != nil {
		return "", fmt.Errorf("%s, started again: %w", oldVersion, err)
	}
	skip := map[string]bool{}
	for _, name := range order {
		changed(name, before[name], again[name], skip)
	}
	if verbose {
		for _, p := range slices.Sorted(maps.Keys(skip)) {
			fmt.Printf("  left out: %s\n", p)
		}
	}
	oldDB := filepath.Join(dir, "old.db")
	if _, err := command(oldBin, cfg, "", "backup", oldDB); err != nil {
		return "", err
	}
	oldSchema, err := userVersion(oldDB)
	if err != nil {
		return "", err
	}

	// The releases on the way, each started once on the household's data,
	// which migrates it as that release's upgrade would.
	lastSchema := oldSchema
	for i := 1; i < len(path); i++ {
		logPath := filepath.Join(dir, fmt.Sprintf("step%d.log", i))
		if err := restartAndWait(ctx, path[i], cfg, base, logPath); err != nil {
			return "", fmt.Errorf("upgrading to %s on the way: %w (its log: %s)", versions[i], err, logPath)
		}
		if lastSchema, err = userVersion(filepath.Join(data, "overscan.db")); err != nil {
			return "", err
		}
	}

	// The upgrade.
	now, err := restartAndLook(ctx, h, newBin, cfg, base, filepath.Join(dir, "new.log"))
	if err != nil {
		return "", fmt.Errorf("after the upgrade: %w (its log: %s)", err, filepath.Join(dir, "new.log"))
	}
	var answers []string
	for _, name := range order {
		if _, ok := now[name]; !ok {
			answers = append(answers, name+": no answer")
			continue
		}
		covers(name, again[name], now[name], skip, &answers)
	}
	var problems []string

	// A new database, for its schema.
	freshData := filepath.Join(dir, "fresh")
	freshCfg := filepath.Join(dir, "fresh.yaml")
	freshPort, err := freePort()
	if err != nil {
		return "", err
	}
	if err := writeConfig(freshCfg, fmt.Sprintf("127.0.0.1:%d", freshPort), freshData, nil); err != nil {
		return "", err
	}
	fresh, err := start(ctx, newBin, freshCfg, fmt.Sprintf("http://127.0.0.1:%d", freshPort), filepath.Join(dir, "fresh.log"))
	if err != nil {
		return "", err
	}
	fresh.stop()
	newSchema, err := userVersion(filepath.Join(freshData, "overscan.db"))
	if err != nil {
		return "", err
	}

	// The backup that this build made before it changed the schema.
	backup := "none was needed"
	backups, _ := filepath.Glob(filepath.Join(data, "backups", fmt.Sprintf("before-upgrade-*-schema%d.db", lastSchema)))
	switch {
	case lastSchema == newSchema && len(backups) != 0:
		problems = append(problems, fmt.Sprintf("the schema stayed at %d, yet the database was backed up as if it had changed: %v", lastSchema, backups))
	case lastSchema != newSchema && len(backups) != 1:
		problems = append(problems, fmt.Sprintf("the schema went from %d to %d, and the backups folder holds %d copies made before it, not one", lastSchema, newSchema, len(backups)))
	case lastSchema != newSchema:
		v, err := userVersion(backups[0])
		if err != nil {
			return "", err
		}
		if v != lastSchema {
			problems = append(problems, fmt.Sprintf("the backup %s has schema %d, not %d", filepath.Base(backups[0]), v, lastSchema))
		}
		backup = filepath.Base(backups[0])
	}

	// Every row the old database held, in a copy upgraded by the releases
	// on the way and this build alone, before a server changes anything in
	// it.
	copyData := filepath.Join(dir, "copy")
	if err := os.MkdirAll(copyData, 0o700); err != nil {
		return "", err
	}
	upgraded := filepath.Join(copyData, "overscan.db")
	if err := copyFile(oldDB, upgraded); err != nil {
		return "", err
	}
	copyCfg := filepath.Join(dir, "copy.yaml")
	if err := writeConfig(copyCfg, "127.0.0.1:0", copyData, nil); err != nil {
		return "", err
	}
	for _, bin := range append(slices.Clone(path[1:]), newBin) {
		if _, err := command(bin, copyCfg, "", "devices", "list"); err != nil {
			return "", err
		}
	}
	lost, err := keptRows(oldDB, upgraded)
	if err != nil {
		return "", err
	}
	problems = append(problems, lost...)
	differs, err := sameSchema(upgraded, filepath.Join(freshData, "overscan.db"))
	if err != nil {
		return "", err
	}
	problems = append(problems, differs...)
	// What the database lost explains the answers that changed, so it
	// comes first.
	problems = append(problems, answers...)

	if len(problems) > 0 {
		if len(problems) > maxProblems {
			problems = append(problems[:maxProblems], fmt.Sprintf("and %d more", len(problems)-maxProblems))
		}
		keepNote := ""
		if !keep {
			keepNote = "; -keep keeps the folder with the logs and databases"
		}
		return "", fmt.Errorf("the upgrade lost or changed what the household set up:\n  %s\n(%d answers compared%s)",
			strings.Join(problems, "\n  "), len(order), keepNote)
	}
	rows := 0
	old, err := readDatabase(oldDB)
	if err != nil {
		return "", err
	}
	for _, r := range old.rows {
		rows += total(r)
	}
	return fmt.Sprintf("  kept everything: %d answers the same (%d values left out that change by themselves across a restart),\n"+
		"  %d rows of %d tables, schema %d to %d, the same as a new database's; backup before the upgrade: %s\n",
		len(order), len(skip), rows, len(old.columns), oldSchema, newSchema, backup), nil
}

// restartAndWait starts bin on the household's data directory, waits for
// its sources and stops it, as a release on the way of an upgrade.
func restartAndWait(ctx context.Context, bin, cfg, base, logPath string) error {
	srv, err := start(ctx, bin, cfg, base, logPath)
	if err != nil {
		return err
	}
	defer srv.stop()
	a, err := signIn(base, "admin", adminPassword)
	if err != nil {
		return fmt.Errorf("the administrator's password: %w", err)
	}
	return waitForSources(a, srv.started)
}

// restartAndLook starts bin on the household's data directory, waits for
// its sources, takes the household's snapshot and stops it.
func restartAndLook(ctx context.Context, h *household, bin, cfg, base, logPath string) (map[string]any, error) {
	srv, err := start(ctx, bin, cfg, base, logPath)
	if err != nil {
		return nil, err
	}
	defer srv.stop()
	a, err := signIn(base, "admin", adminPassword)
	if err != nil {
		return nil, fmt.Errorf("the administrator's password: %w", err)
	}
	if err := waitForSources(a, srv.started); err != nil {
		return nil, err
	}
	answers, _, err := h.snapshot(a, base)
	return answers, err
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return errors.Join(out.Sync(), out.Close())
}
