// SPDX-License-Identifier: AGPL-3.0-or-later

// Command overscan runs the Overscan portal server, and manages some of it
// from the command line: devices, the administrator's password and the debug
// tools.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	_ "time/tzdata" // devices name their time zones, and a container may lack the system's

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/catalog"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/logging"
	"overscan.gureet.ca/internal/playcheck"
	"overscan.gureet.ca/internal/recorder"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/server"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/upstream"
)

// version is replaced at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage:
  overscan [-config FILE]                  run the server
  overscan [-config FILE] devices COMMAND  manage devices; "overscan devices -h" lists the commands
  overscan [-config FILE] admin password   set the administrator's password for the admin at /admin/
  overscan [-config FILE] debug COMMAND    check a channel, or print the debug playlist's address;
                                           "overscan debug -h" says how
  overscan [-config FILE] backup [FILE]    copy the database to FILE, or into the data directory's backups
  overscan -version                        print the version
  overscan help [COMMAND]                  print this help, or a command's

Run the commands as the user that the server runs as. Each command's own
help, with -h, says more.

Flags:
`

// commands are the commands that run takes, with their help.
var commands = map[string]string{
	"devices": devicesUsage,
	"admin":   adminUsage,
	"debug":   debugUsage,
	"backup":  backupUsage,
}

// helpFor reports whether args, after the flags, ask for help, and which:
// "help" or "help COMMAND", or a command with -h, -help or --help among its
// arguments, or with help as its first. text is "" for the general help.
// The help answers before the configuration is read, so that it shows
// whatever the file holds and whoever runs it.
func helpFor(args []string) (text string, ok bool) {
	if len(args) == 0 {
		return "", false
	}
	if args[0] == "help" {
		if len(args) > 1 {
			if text, known := commands[args[1]]; known {
				return text, true
			}
		}
		return "", true
	}
	text, known := commands[args[0]]
	if !known {
		return "", false
	}
	for i, arg := range args[1:] {
		// backup's only argument is a file, which may be called help.
		if arg == "-h" || arg == "-help" || arg == "--help" || (i == 0 && arg == "help" && args[0] != "backup") {
			return text, true
		}
	}
	return "", false
}

func main() {
	privateFiles()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.LookupEnv))
}

// run is main without its process-wide side effects, so that tests can call it.
// Without a command it serves until ctx is done. It returns the process exit
// code.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, lookupEnv func(string) (string, bool)) int {
	flags := flag.NewFlagSet("overscan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	// printUsage writes the general help to w: to stdout when it was asked
	// for, to stderr after a mistake.
	printUsage := func(w io.Writer) {
		fmt.Fprint(w, usage)
		flags.SetOutput(w)
		flags.PrintDefaults()
		flags.SetOutput(stderr)
	}
	flags.Usage = func() {}
	configPath := flags.String("config", config.DefaultPath, "path to the YAML configuration file")
	showVersion := flags.Bool("version", false, "print the version and exit")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printUsage(stdout)
			return 0
		}
		fmt.Fprintln(stderr)
		printUsage(stderr)
		return 2
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return 0
	}
	command := flags.Args()
	if text, ok := helpFor(command); ok {
		if text == "" {
			printUsage(stdout)
		} else {
			fmt.Fprint(stdout, text)
		}
		return 0
	}
	if len(command) > 0 {
		if _, known := commands[command[0]]; !known {
			fmt.Fprintf(stderr, "overscan: unknown command %q\n\n", command[0])
			printUsage(stderr)
			return 2
		}
	}

	// A config file named on the command line must exist; the default one may not.
	explicit := false
	flags.Visit(func(f *flag.Flag) { explicit = explicit || f.Name == "config" })

	cfg, readFile, err := config.Load(*configPath, explicit, lookupEnv)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	recent := logging.NewRecent(recentLogLines)
	log, err := logging.New(cfg.Log.Level, cfg.Log.Format, io.MultiWriter(stderr, recent))
	if err != nil {
		fmt.Fprintf(stderr, "overscan: config: %v\n", err)
		return 1
	}

	if len(command) == 0 {
		if !readFile {
			log.Info("no configuration file, using defaults and environment", "path", *configPath)
		}
		return serve(ctx, cfg, log, recent)
	}
	switch command[0] {
	case "devices":
		return devicesCommand(ctx, cfg, log, command[1:], stdout, stderr)
	case "admin":
		return adminCommand(ctx, cfg, log, command[1:], stdin, stdout, stderr)
	case "debug":
		return debugCommand(ctx, cfg, command[1:], stdout, stderr)
	case "backup":
		return backupCommand(ctx, cfg, command[1:], stdout, stderr)
	}
	return 2 // not reached: run checked the command against commands
}

// recentLogLines is how many of the log's last lines the support bundle
// holds.
const recentLogLines = 1000

// serve runs the server until ctx is done. recent keeps the log's last
// lines, for the support bundle.
func serve(ctx context.Context, cfg config.Config, log *slog.Logger, recent *logging.Recent) int {
	log.Info("starting", "version", version)

	// The data directory holds the database and, later, keys and caches, so
	// a new one is private to the service's user.
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		log.Error("cannot create the data directory", "error", err)
		return 1
	}
	box, err := loadKey(cfg, log)
	if err != nil {
		log.Error("cannot use the key file", "path", cfg.KeyPath(), "error", err)
		return 1
	}
	// Migrations run to completion even if a stop signal arrives meanwhile;
	// the server then shuts straight down.
	openCtx := context.WithoutCancel(ctx)
	dbPath := filepath.Join(cfg.DataDir, store.FileName)
	st, err := store.Open(openCtx, dbPath, log)
	if err != nil {
		log.Error("cannot open the database", "path", dbPath, "error", err)
		return 1
	}
	defer func() {
		if err := st.Close(); err != nil {
			log.Warn("closing the database", "error", err)
		}
	}()
	if v, err := st.SchemaVersion(openCtx); err == nil {
		log.Info("database ready", "path", dbPath, "schema", v)
	}
	if err := sealSecrets(openCtx, st, box, log); err != nil {
		log.Error("cannot seal the stream addresses", "error", err)
		return 1
	}

	// Sources live in the database (D46); those in the configuration file
	// are imported once. Each enabled source is refreshed in the background,
	// now and then on its schedule, and again when its settings change.
	if err := importSources(openCtx, st, cfg.Sources, log); err != nil {
		log.Error("cannot import the configuration file's sources", "error", err)
		return 1
	}
	sources, err := st.Sources(openCtx)
	if err != nil {
		log.Error("cannot read the sources", "error", err)
		return 1
	}
	if len(sources) == 0 {
		log.Info("no sources yet; add one in the admin")
	}
	streams := hub.New(st, log, version, hubSources(sources))
	ffmpegPath, ffmpegVersion := useFFmpeg(ctx, streams, cfg.FFmpeg, hub.RepairSet, log)
	ffprobePath, ffprobeVersion := useFFprobe(ctx, streams, ffmpegPath, log)
	// Boxes pause live TV and go back in it through buffers on disk, while
	// the admin's timeshift setting is on (D92).
	if err := streams.UseTimeshift(filepath.Join(cfg.DataDir, "timeshift")); err != nil {
		log.Warn("cannot make the folder for timeshift buffers, so boxes cannot pause live TV", "error", err)
	}
	useEncoder(ctx, streams, ffmpegPath, ffprobePath, log)
	refresher := catalog.NewRefresher(st, log, version)
	refresher.AllowEmpty(cfg.RecordingsPath())
	// Guides of the household's own fill the channels whose sources give
	// them none, matched again whenever sources change (D139).
	ownGuides := guide.NewOwn(st, log, version)
	refresher.UseOwnGuides(ownGuides)
	lib := library.New(st, log, version)
	// Stalker portals are read, and their streams played, in one session
	// for each source (D76).
	portals := stalker.NewPool(nil)
	refresher.UseStalker(portals)
	lib.UseStalker(portals)
	streams.UseResolver(upstream.New(st, portals, log))
	refresher.OnChange(func(sources []store.Source) {
		streams.SetSources(hubSources(sources))
		portals.Forget(func(id int64) bool {
			return slices.ContainsFunc(sources, func(s store.Source) bool { return s.ID == id && s.Type == source.TypeStalker })
		})
	})
	adminToken, err := admin.Token(cfg.DataDir)
	if err != nil {
		log.Error("cannot set up the admin token", "error", err)
		return 1
	}

	// Background work stops with the server, and the database closes only
	// after it has.
	bgCtx, stopBackground := context.WithCancel(ctx)
	var background sync.WaitGroup
	background.Go(func() { refresher.Run(bgCtx) })
	background.Go(func() { ownGuides.Run(bgCtx) })
	background.Go(func() { streams.Run(bgCtx) })
	background.Go(func() { lib.Run(bgCtx) })
	// Programmes that boxes schedule are recorded through the hub, into the
	// recordings folder, once the admin turns recording on (D89).
	rec := recorder.New(st, streams, log, cfg.RecordingsPath())
	rec.OnChange(func() { lib.ReadFolderAgain(bgCtx, cfg.RecordingsPath()) })
	background.Go(func() { rec.Run(bgCtx) })

	// The playback check's clips are made with ffmpeg when a box first
	// asks for them (D70).
	var checks *playcheck.Clips
	if ffmpegPath != "" {
		checks = playcheck.New(bgCtx, filepath.Join(cfg.DataDir, "check"), ffmpegPath, log)
	}

	code := 0
	support := admin.Support{Log: recent.Lines, Facts: serverFacts(cfg, ffmpegPath, ffmpegVersion, ffprobePath, ffprobeVersion), Listen: cfg.Listen}
	svc := server.Services{Store: st, Hub: streams, AdminToken: adminToken, Sources: refresher, Guides: ownGuides,
		Support: support, Library: lib, Checks: checks, Recorder: rec}
	if err := server.New(cfg, log, version, svc).Run(ctx); err != nil {
		log.Error("server failed", "error", err)
		code = 1
	}
	stopBackground()
	background.Wait()
	log.Info("stopped")
	return code
}

// loadKey returns a box for the key in the key file, which it makes at the
// first start (D46).
func loadKey(cfg config.Config, log *slog.Logger) (*secrets.Box, error) {
	key, info, err := secrets.LoadKey(cfg.KeyPath(), true)
	if err != nil {
		return nil, err
	}
	if info.Created {
		log.Info("made the key file that encrypts the sources' secrets; keep a copy of it apart from the database's backups",
			"path", cfg.KeyPath())
	}
	if info.Exposed {
		log.Warn("others than its owner can read the key file; make it readable by its owner only, with chmod 600",
			"path", cfg.KeyPath())
	}
	return secrets.NewBox(key)
}

// sealSecrets gives the store its key, seals the stream addresses that older
// versions left as they were, and says which channels' addresses another key
// sealed.
func sealSecrets(ctx context.Context, st *store.Store, box *secrets.Box, log *slog.Logger) error {
	st.UseKey(box)
	n, err := st.SealStreamURLs(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		log.Info("encrypted the channels' stream addresses with the key file", "channels", n)
	}
	elsewhere, err := st.StreamsSealedElsewhere(ctx)
	if err != nil {
		return err
	}
	for name, n := range elsewhere {
		log.Warn("channels' stream addresses were encrypted with another key file; they play again once their source is read",
			"source", name, "channels", n)
	}
	return nil
}

// useKey gives a command's store the key in the key file, if the server has
// made one, for reading secrets.
func useKey(st *store.Store, cfg config.Config) error {
	key, _, err := secrets.LoadKey(cfg.KeyPath(), false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return err
	}
	st.UseKey(box)
	return nil
}

// importSources imports the configuration file's sources into the
// database, each once (D46).
func importSources(ctx context.Context, st *store.Store, sources []config.Source, log *slog.Logger) error {
	specs := make([]source.Settings, len(sources))
	for i, src := range sources {
		specs[i] = src.Settings()
	}
	imports, err := st.ImportSources(ctx, specs)
	if err != nil {
		return err
	}
	for _, imp := range imports {
		switch imp.Outcome {
		case store.Imported:
			log.Info("imported a source from the configuration file; the admin manages it from now on, and its entry in the file can be deleted",
				"source", imp.Name)
		case store.ImportedBefore:
			log.Warn("the configuration file's source was imported before; the admin manages it, so the file's entry is ignored and can be deleted",
				"source", imp.Name)
		case store.NameTaken:
			log.Warn("a source that the admin added has the configuration file's source's name; the file's entry is ignored",
				"source", imp.Name)
		}
	}
	return nil
}

// hubSources returns what the stream hub needs to know about the sources,
// by their IDs. A Stalker portal gives a link for each stream as it starts,
// and its streams are fetched as the box (D76).
func hubSources(sources []store.Source) map[int64]hub.Source {
	out := make(map[int64]hub.Source, len(sources))
	for _, src := range sources {
		hs := hub.Source{Name: src.Name, MaxConnections: src.StreamLimit(), UserAgent: src.UserAgent, PublicOnly: src.PublicOnly}
		if src.Type == source.TypeStalker {
			hs.Resolves = true
			if hs.UserAgent == "" {
				hs.UserAgent = stalker.DefaultUserAgent
			}
		}
		out[src.ID] = hs
	}
	return out
}
