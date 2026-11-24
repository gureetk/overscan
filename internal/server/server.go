// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server runs Overscan's HTTP server.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/clientaddr"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/devices"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/playcheck"
	"overscan.gureet.ca/internal/portal/device"
	"overscan.gureet.ca/internal/portal/m3u"
	"overscan.gureet.ca/internal/portal/stalker"
	"overscan.gureet.ca/internal/portal/xtream"
	"overscan.gureet.ca/internal/recorder"
	"overscan.gureet.ca/internal/routes"
	"overscan.gureet.ca/internal/store"
)

// Limits on what a request may make the server hold. Headers have
// readHeaderTimeout to arrive in, and at most maxHeaderBytes, which no box,
// player or browser comes near; a body has bodyTimeout to arrive in, from
// its first byte, or the request fails. A connection that has been idle for
// idleTimeout is closed. Nothing limits how long an answer takes, since a
// stream goes on for as long as someone watches.
const (
	readHeaderTimeout = 10 * time.Second
	maxHeaderBytes    = 64 << 10
	idleTimeout       = 2 * time.Minute
)

// bodyTimeout is a variable so that tests can shorten it.
var bodyTimeout = time.Minute

// shutdownTimeout is how long the server waits for open requests when it stops.
// Streams can stay open far longer, so after this the remaining connections are
// closed.
const shutdownTimeout = 10 * time.Second

// Server is Overscan's HTTP server.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	version  string
	services Services
	handler  http.Handler
	// adminHandler serves the admin on admin_listen; nil when the admin
	// shares handler (D44).
	adminHandler http.Handler
	// routes and adminRoutes are handler's and adminHandler's routes, for
	// the test that checks who may call each.
	routes, adminRoutes *routes.Mux
	pages               pageLog
	admin               *admin.Admin
	themes              themeSheets // the stylesheets of installed themes (D97)
}

// Services are what the server's handlers work with.
type Services struct {
	Store *store.Store
	Hub   *hub.Hub
	// AdminToken guards the debug playlist.
	AdminToken string
	// Sources reads sources for the admin; without it, the admin cannot
	// test or read them.
	Sources admin.Sources
	// Guides reads the guides of the household's own (D139) for the
	// admin; without it, the admin can change them but cannot read them.
	Guides admin.Guides
	// Support is what the admin's support bundle needs from outside it.
	Support admin.Support
	// Library reads films and series for the device API and the Stalker
	// API; without it, boxes and clients see none.
	Library *library.Library
	// Checks makes the clips of the playback check (D70); without it,
	// boxes cannot be checked.
	Checks *playcheck.Clips
	// Recorder records programmes (D89); without it, boxes cannot record.
	Recorder *recorder.Recorder
}

// stalkerPaths are where Stalker clients look for the portal API (protocol
// notes, section 1).
var stalkerPaths = []string{"/portal.php", "/server/load.php", "/stalker_portal/server/load.php"}

// New returns a server for cfg. The health check reports version.
func New(cfg config.Config, log *slog.Logger, version string, svc Services) *Server {
	s := &Server{cfg: cfg, log: log, version: version, services: svc}
	mux := routes.NewMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /probe/", probeFiles())
	mux.HandleFunc("POST /probe/report", s.handleProbeReport)
	reg := devices.New(svc.Store, log)
	api := stalker.New(svc.Store, reg, svc.Hub, log)
	if svc.Library != nil {
		api.UseLibrary(svc.Library)
	}
	for _, path := range stalkerPaths {
		mux.Handle(path, api)
	}
	dev := device.New(svc.Store, reg, svc.Hub, log, version)
	if svc.Library != nil {
		dev.UseLibrary(svc.Library)
	}
	if svc.Checks != nil {
		dev.UseChecks(svc.Checks)
	}
	if svc.Recorder != nil {
		dev.UseRecorder(svc.Recorder)
	}
	mux.Handle(device.Prefix, dev)
	mux.Handle("GET /artwork/", artwork.Handler(svc.Store, artwork.New(filepath.Join(cfg.DataDir, "artwork"), log, version), log))
	for _, path := range magPaths {
		mux.Handle("GET "+path, s.magFiles(path))
	}
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.Handle("GET /live/", svc.Hub)
	mux.Handle("GET /vod/", svc.Hub)
	mux.Handle("GET /archive/", svc.Hub)
	// The outputs that players use (D103), off until the administrator
	// turns them on.
	logins := admin.NewLogins(svc.Store)
	m3u.New(svc.Store, svc.Hub, logins, log).Register(mux)
	xo := xtream.New(svc.Store, svc.Hub, logins, log)
	if svc.Library != nil {
		xo.UseLibrary(svc.Library)
	}
	xo.Register(mux)
	mux.HandleFunc("GET /debug/lineup.m3u", s.handleDebugLineup)
	mux.HandleFunc("GET /debug/play/{entry}", s.handleDebugPlay)
	// Requests through a trusted reverse proxy are known by the client
	// that it names (D119). The configuration is checked as it loads.
	proxies, err := clientaddr.Parse(cfg.TrustedProxies)
	if err != nil {
		log.Warn("trusting no reverse proxy", "err", err)
	} else if proxies.Len() > 0 {
		log.Info("trusting reverse proxies to say whom requests are for", "proxies", strings.Join(cfg.TrustedProxies, " "))
	}
	s.handler = proxies.Middleware(mux)
	s.routes = mux

	// The admin shares the main address unless admin_listen names one of
	// its own, which then serves it alone (D44).
	a := admin.New(svc.Store, log, version)
	s.admin = a
	if svc.Sources != nil {
		a.UseSources(svc.Sources)
	}
	if svc.Guides != nil {
		a.UseGuides(svc.Guides)
	}
	if svc.Hub != nil {
		a.UseStreams(svc.Hub)
	}
	if svc.Recorder != nil {
		a.UseRecorder(svc.Recorder)
	}
	if svc.Library != nil {
		a.UseFolders(svc.Library)
		a.UseLists(svc.Library)
	}
	a.UseSupport(svc.Support)
	adm := a.Handler()
	amux := mux
	if cfg.AdminListen != "" {
		amux = routes.NewMux()
		amux.Handle("GET /{$}", http.RedirectHandler("/admin/", http.StatusFound))
		s.adminHandler = proxies.Middleware(amux)
		s.adminRoutes = amux
	}
	amux.Handle("/admin/", adm)
	amux.Handle(admin.APIPrefix, adm)
	return s
}

// Patterns returns the patterns of the routes on the main address, and of
// those on the admin's own address, if it has one.
func (s *Server) Patterns() (main, admin []string) {
	main = s.routes.Patterns()
	if s.adminRoutes != nil {
		admin = s.adminRoutes.Patterns()
	}
	return main, admin
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// AdminHandler returns the handler for admin_listen, or nil if the admin
// shares Handler.
func (s *Server) AdminHandler() http.Handler { return s.adminHandler }

// Run listens on the configured addresses and serves until ctx is done.
func (s *Server) Run(ctx context.Context) error {
	s.admin.PrepareRecordings(ctx)
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	if s.adminHandler == nil {
		return s.Serve(ctx, ln)
	}
	aln, err := net.Listen("tcp", s.cfg.AdminListen)
	if err != nil {
		_ = ln.Close()
		return err
	}
	// When one server fails, the other stops too.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	served := make(chan error, 2)
	go func() { served <- s.Serve(ctx, ln) }()
	go func() { served <- s.ServeAdmin(ctx, aln) }()
	err = <-served
	cancel()
	if err2 := <-served; err == nil {
		err = err2
	}
	return err
}

// Serve serves on ln until ctx is done, then shuts down: it stops accepting
// connections, waits up to shutdownTimeout for open requests and closes the
// rest. It returns an error only if serving fails.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	return s.serve(ctx, ln, s.handler, "listening")
}

// ServeAdmin serves the admin on ln, as Serve does, when it has an address
// of its own.
func (s *Server) ServeAdmin(ctx context.Context, ln net.Listener) error {
	if s.adminHandler == nil {
		return errors.New("server: the admin has no address of its own")
	}
	return s.serve(ctx, ln, s.adminHandler, "admin listening")
}

func (s *Server) serve(ctx context.Context, ln net.Listener, handler http.Handler, what string) error {
	srv := &http.Server{
		Handler:           bodyDeadline(handler),
		ReadHeaderTimeout: readHeaderTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		// No ReadTimeout or WriteTimeout: a stream stays open for as long
		// as someone watches.
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	s.log.Info(what, "address", ln.Addr().String())

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		s.log.Warn("closing connections still open at shutdown", "error", err)
		_ = srv.Close()
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}{"ok", s.version})
}
