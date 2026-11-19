// SPDX-License-Identifier: AGPL-3.0-or-later

package playcheck

import (
	"os/exec"
	"syscall"
)

// keepChild makes ffmpeg stop if Overscan does, so that none outlives the
// server.
func keepChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
