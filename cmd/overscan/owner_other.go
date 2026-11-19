// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !unix

package main

import "os"

// checkOwner has nothing to check where files have no Unix owner.
func checkOwner(string, os.FileInfo, int) error { return nil }

// privateFiles has no umask to set here.
func privateFiles() {}
