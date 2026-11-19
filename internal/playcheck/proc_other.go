// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package playcheck

import "os/exec"

// keepChild does nothing here: only Linux can stop a child when its parent
// stops.
func keepChild(*exec.Cmd) {}
