// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build !linux

package hub

// freeSpace does not know the disk space left on this system.
func freeSpace(string) (int64, bool) { return 0, false }
