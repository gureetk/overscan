// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/hub"
)

// ffmpegCheckTimeout caps asking ffmpeg for its version.
const ffmpegCheckTimeout = 10 * time.Second

var errFFmpegOff = errors.New("stream repair is off in the configuration")

// findFFmpeg returns the path of the ffmpeg program that the setting names,
// and its version, if it runs.
func findFFmpeg(ctx context.Context, setting string) (path, version string, err error) {
	if setting == config.FFmpegOff {
		return "", "", errFFmpegOff
	}
	path, err = exec.LookPath(setting)
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, ffmpegCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-version")
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("%s -version: %w", path, err)
	}
	// The first line is like "ffmpeg version 6.1.1-3ubuntu5 Copyright (c) ...".
	first, _, _ := strings.Cut(string(out), "\n")
	version = strings.TrimSpace(first)
	if v, ok := strings.CutPrefix(version, "ffmpeg version "); ok {
		version, _, _ = strings.Cut(v, " ")
	}
	return path, version, nil
}

// findFFprobe returns the path of the ffprobe program that comes with the
// ffmpeg at ffmpegPath, beside it or else in PATH, and its version, if it
// runs.
func findFFprobe(ctx context.Context, ffmpegPath string) (path, version string, err error) {
	path, err = exec.LookPath(filepath.Join(filepath.Dir(ffmpegPath), "ffprobe"))
	if err != nil {
		if path, err = exec.LookPath("ffprobe"); err != nil {
			return "", "", err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, ffmpegCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-version")
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("%s -version: %w", path, err)
	}
	first, _, _ := strings.Cut(string(out), "\n")
	version = strings.TrimSpace(first)
	if v, ok := strings.CutPrefix(version, "ffprobe version "); ok {
		version, _, _ = strings.Cut(v, " ")
	}
	return path, version, nil
}

// useFFprobe gives the hub the ffprobe that comes with the ffmpeg at
// ffmpegPath, if it runs, to read what the files of films and episodes hold
// so that ffmpeg can convert those a box cannot play (D68), and logs what
// it found. It returns ffprobe's path and version, or "" for both.
func useFFprobe(ctx context.Context, h *hub.Hub, ffmpegPath string, log *slog.Logger) (path, version string) {
	if ffmpegPath == "" {
		return "", ""
	}
	path, version, err := findFFprobe(ctx, ffmpegPath)
	if err != nil {
		log.Warn("cannot run ffprobe, so films and episodes play as they are, even those a box cannot play; install the ffprobe that comes with ffmpeg",
			"error", err)
		return "", ""
	}
	log.Info("ffprobe found to read what films' files hold, for ffmpeg to convert what a box cannot play",
		"path", path, "version", version)
	h.UseFFprobe(path)
	return path, version
}

// ffmpegList returns the names in one of the lists that ffmpeg prints,
// such as its encoders or its filters: the second word of each line.
func ffmpegList(ctx context.Context, path, list string) (map[string]bool, error) {
	ctx, cancel := context.WithTimeout(ctx, ffmpegCheckTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "-hide_banner", "-"+list)
	cmd.Env = []string{}
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s -%s: %w", path, list, err)
	}
	names := map[string]bool{}
	for line := range strings.Lines(string(out)) {
		if f := strings.Fields(line); len(f) >= 2 {
			names[f[1]] = true
		}
	}
	return names, nil
}

// encoderOf returns what the ffmpeg at path can do to transcode video
// (D73): encode H.264 with libx264, and map a high dynamic range to the
// standard one with the zscale and tonemap filters.
func encoderOf(ctx context.Context, path string) (hub.Encoder, error) {
	encoders, err := ffmpegList(ctx, path, "encoders")
	if err != nil {
		return hub.Encoder{}, err
	}
	filters, err := ffmpegList(ctx, path, "filters")
	if err != nil {
		return hub.Encoder{}, err
	}
	return hub.Encoder{H264: encoders["libx264"], ToneMap: filters["zscale"] && filters["tonemap"]}, nil
}

// useEncoder tells the hub what the ffmpeg at ffmpegPath can do to
// transcode the video of films and episodes that a box cannot play (D73),
// which it does with ffprobe, at ffprobePath, and logs it.
func useEncoder(ctx context.Context, h *hub.Hub, ffmpegPath, ffprobePath string, log *slog.Logger) {
	if ffmpegPath == "" || ffprobePath == "" {
		return
	}
	e, err := encoderOf(ctx, ffmpegPath)
	switch {
	case err != nil:
		log.Warn("cannot ask ffmpeg what it can transcode with, so it does not transcode", "error", err)
		return
	case !e.H264:
		log.Info("this ffmpeg has no libx264, so it cannot transcode the video of films that a box cannot play")
	case !e.ToneMap:
		log.Info("ffmpeg can transcode the video of films that a box cannot play, where the Settings page lets it, but has no zscale filter to map a high dynamic range to the standard one")
	default:
		log.Info("ffmpeg can transcode the video of films that a box cannot play, where the Settings page lets it")
	}
	h.UseEncoder(e)
}

// useFFmpeg gives the hub the ffmpeg that the configuration names, if it
// runs, to repair streams with, and logs what it found. It returns ffmpeg's
// path and version, or "" for both if there is none to use.
func useFFmpeg(ctx context.Context, h *hub.Hub, setting string, which hub.Repair, log *slog.Logger) (path, version string) {
	path, version, err := findFFmpeg(ctx, setting)
	switch {
	case errors.Is(err, errFFmpegOff):
		log.Info("stream repair is off in the configuration")
	case err != nil:
		log.Warn("cannot run ffmpeg, so channels set to be repaired play as they are; install ffmpeg, or set its path in the configuration file",
			"ffmpeg", setting, "error", err)
	default:
		log.Info("ffmpeg found to repair streams with", "path", path, "version", version)
		h.UseFFmpeg(path, which)
		return path, version
	}
	return "", ""
}

// serverFacts are what the support bundle says about the configuration,
// ffmpeg and ffprobe, given the paths and versions that useFFmpeg and
// useFFprobe returned.
func serverFacts(cfg config.Config, ffmpegPath, ffmpegVersion, ffprobePath, ffprobeVersion string) []admin.Fact {
	adminAt := "the devices' address"
	if cfg.AdminListen != "" {
		adminAt = cfg.AdminListen
	}
	ffmpeg := ffmpegVersion + " at " + ffmpegPath
	switch {
	case cfg.FFmpeg == config.FFmpegOff:
		ffmpeg = "off in the configuration"
	case ffmpegPath == "":
		ffmpeg = "none runs, from the setting " + cfg.FFmpeg
	}
	ffprobe := ffprobeVersion + " at " + ffprobePath
	if ffprobePath == "" {
		ffprobe = "none runs, so films and episodes play as they are"
	}
	return []admin.Fact{
		{Name: "Listens on", Value: cfg.Listen},
		{Name: "Admin on", Value: adminAt},
		{Name: "Data directory", Value: cfg.DataDir},
		{Name: "Key file", Value: cfg.KeyPath()},
		{Name: "Log", Value: cfg.Log.Level + ", " + cfg.Log.Format},
		{Name: "ffmpeg", Value: ffmpeg},
		{Name: "ffprobe", Value: ffprobe},
	}
}
