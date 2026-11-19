// SPDX-License-Identifier: AGPL-3.0-or-later

// Package web holds the browser files that Overscan serves, embedded in the
// binary.
package web

import "embed"

// Probe holds the probe page, which reports what a box's browser supports.
//
//go:embed probe
var Probe embed.FS

// MAG holds the MAG page, built from mag/src into mag/dist by mag/build.mjs.
//
//go:embed mag/dist
var MAG embed.FS

// Admin holds the admin's page templates and static files.
//
//go:embed admin/templates admin/static
var Admin embed.FS
