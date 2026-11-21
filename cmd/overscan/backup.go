// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/store"
)

const backupUsage = `Usage:
  overscan backup [FILE]

Copies the database to FILE, which must not exist, or else to a new file in
the backups folder of the data directory. The copy is consistent even while
the server runs, and readable by its owner only. Run it as the user the
server runs as.

The copy holds the sources' addresses and logins encrypted with the key
file, so it is of no use without the key file: keep a copy of that too,
apart from the backups. docs/backup.md says how to restore one.
`

// backupCommand copies the database.
func backupCommand(ctx context.Context, cfg config.Config, args []string, stdout, stderr io.Writer) int {
	switch {
	case len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help"):
		fmt.Fprint(stdout, backupUsage)
		return 0
	case len(args) > 1 || (len(args) == 1 && (args[0] == "" || args[0][0] == '-')):
		fmt.Fprint(stderr, backupUsage)
		return 2
	}

	path := filepath.Join(cfg.DataDir, store.FileName)
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(stderr, "overscan: no database at %s; check data_dir in the configuration\n", path)
		return 1
	}
	if err == nil {
		err = checkOwner(path, fi, os.Geteuid())
	}
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}

	var dest string
	if len(args) == 1 {
		dest = args[0]
	} else {
		dir := filepath.Join(cfg.DataDir, store.BackupsFolder)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintf(stderr, "overscan: %v\n", err)
			return 1
		}
		dest = filepath.Join(dir, "overscan-"+time.Now().UTC().Format("20060102-150405")+".db")
	}
	version, err := store.Backup(ctx, path, dest)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	size := "?"
	if fi, err := os.Stat(dest); err == nil {
		size = fmt.Sprintf("%.1f MB", float64(fi.Size())/(1<<20))
	}
	fmt.Fprintf(stdout, "Copied the database (schema %d) to %s, %s.\n", version, dest, size)
	fmt.Fprintf(stdout, "Its sources' addresses and logins are encrypted with the key file %s;\nkeep a copy of the key file too, apart from the backups.\n", cfg.KeyPath())
	return 0
}
