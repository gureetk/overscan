// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"io"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/gpu"
)

const gpuUsage = `Usage:
  overscan gpu

Tests what the server's graphics can do for ffmpeg through VA-API, as the
server does when it starts, and prints what it found: which codecs they
encode and decode, and which filters work on them. It opens no channel:
every picture is one that ffmpeg makes. The configuration's ffmpeg and gpu
settings say which ffmpeg and which device.

Run it as the user that the server runs as, in the same container, so that
it sees the graphics as the server does. A server started by systemd may
see less than this command, since the unit's sandbox hides devices that are
not given to it: docs/transcoding.md says how to give it the graphics.
`

// gpuCommand tests the graphics and prints what it found. It exits with 1
// if there are none to transcode on.
func gpuCommand(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help"):
		fmt.Fprint(stdout, gpuUsage)
		return 0
	case len(args) > 0:
		fmt.Fprint(stderr, gpuUsage)
		return 2
	}
	path, _, err := findFFmpeg(ctx, cfg.FFmpeg)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: cannot run ffmpeg, which the graphics need: %v\n", err)
		return 1
	}
	f := gpu.Test(ctx, gpu.Options{FFmpeg: path, Device: cfg.GPU})
	f.Report(stdout)
	if !f.Usable() {
		return 1
	}
	return 0
}
