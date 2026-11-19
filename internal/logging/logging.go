// SPDX-License-Identifier: AGPL-3.0-or-later

// Package logging builds Overscan's structured logger.
package logging

import (
	"fmt"
	"io"
	"log/slog"
)

// New returns a logger that writes to w at the given level (debug, info, warn
// or error, in any case) in the given format (text or json).
func New(level, format string, w io.Writer) (*slog.Logger, error) {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("log level %q: want debug, info, warn or error", level)
	}
	opts := &slog.HandlerOptions{Level: lvl}
	switch format {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("log format %q: want text or json", format)
}
