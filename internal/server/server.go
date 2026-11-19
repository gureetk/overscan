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
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/devices"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/playcheck"
	"overscan.gureet.ca/internal/portal/device"
	"overscan.gureet.ca/internal/portal/stalker"
	"overscan.gureet.ca/internal/store"
)

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
	pages        pageLog
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
	// Support is what the admin's support bundle needs from outside it.
	Support admin.Support
	// Library reads films and series for the device API and the Stalker
	// API; without it, boxes and clients see none.
	Library *library.Library
	// Checks makes the clips of the playback check (D70); without it,
	// boxes cannot be checked.
	Checks *playcheck.Clips
}

// stalkerPaths are where Stalker clients look for the portal API (protocol
// notes, section 1).
var stalkerPaths = []string{"/portal.php", "/server/load.php", "/stalker_portal/server/load.php"}

// New returns a server for cfg. The health check reports version.
func New(cfg config.Config, log *slog.Logger, version string, svc Services) *Server {
	s := &Server{cfg: cfg, log: log, version: version, services: svc}
	mux := http.NewServeMux()
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
	mux.Handle(device.Prefix, dev)
	mux.Handle("GET /artwork/", artwork.Handler(svc.Store, artwork.New(filepath.Join(cfg.DataDir, "artwork"), log, version), log))
	for _, path := range magPaths {
		mux.Handle("GET "+path, s.magFiles(path))
	}
	mux.HandleFunc("GET /{$}", s.handleRoot)
	mux.Handle("GET /live/", svc.Hub)
	mux.Handle("GET /vod/", svc.Hub)
	mux.Handle("GET /archive/", svc.Hub)
	mux.HandleFunc("GET /debug/lineup.m3u", s.handleDebugLineup)
	mux.HandleFunc("GET /debug/play/{entry}", s.handleDebugPlay)
	s.handler = mux

	// The admin shares the main address unless admin_listen names one of
	// its own, which then serves it alone (D44).
	a := admin.New(svc.Store, log, version)
	if svc.Sources != nil {
		a.UseSources(svc.Sources)
	}
	if svc.Hub != nil {
		a.UseStreams(svc.Hub)
	}
	a.UseSupport(svc.Support)
	adm := a.Handler()
	amux := mux
	if cfg.AdminListen != "" {
		amux = http.NewServeMux()
		amux.Handle("GET /{$}", http.RedirectHandler("/admin/", http.StatusFound))
		s.adminHandler = amux
	}
	amux.Handle("/admin/", adm)
	amux.Handle(admin.APIPrefix, adm)
	return s
}

// Handler returns the server's HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// AdminHandler returns the handler for admin_listen, or nil if the admin
// shares Handler.
func (s *Server) AdminHandler() http.Handler { return s.adminHandler }

// Run listens on the configured addresses and serves until ctx is done.
func (s *Server) Run(ctx context.Context) error {
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
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
		// No WriteTimeout: a stream stays open for as long as someone watches.
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
