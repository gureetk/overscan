// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"os/exec"
	"syscall"
)

// keepChild makes the child stop if Overscan does, so that no ffmpeg outlives
// the stream it repairs.
func keepChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
