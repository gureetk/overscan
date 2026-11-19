// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package main

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
	"syscall"
)

// checkOwner refuses to work on a database that belongs to another user.
// SQLite creates files next to the database (the WAL and its index), which
// would then belong to the wrong user, and the server could no longer write
// them. This matters most for root: "sudo overscan devices ..." would succeed
// and leave the server broken.
func checkOwner(path string, fi os.FileInfo, euid int) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) == euid {
		return nil
	}
	name := strconv.FormatUint(uint64(st.Uid), 10)
	if u, err := user.LookupId(name); err == nil {
		name = u.Username
	}
	return fmt.Errorf("%s belongs to user %s; run this command as that user, for example with sudo -u %s", path, name, name)
}

// privateFiles makes the files that Overscan creates readable by their owner
// only, whatever the umask it was started with, as in a container. SQLite
// creates the database's shared-memory index with the umask's mode.
func privateFiles() { syscall.Umask(0o077) }
