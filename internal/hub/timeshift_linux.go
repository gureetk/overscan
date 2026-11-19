// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import "syscall"

// freeSpace returns the disk space left to the server in dir's file
// system.
func freeSpace(dir string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, false
	}
	// Bsize's type differs from one architecture to another.
	return int64(st.Bavail) * int64(st.Bsize), true
}
