// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build unix

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"overscan.gureet.ca/internal/store"
)

func TestPrivateFiles(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	privateFiles()

	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := os.WriteFile(filepath.Join(dir, "other"), nil, 0o666); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{store.FileName + "-shm", store.FileName + "-wal", "other"} {
		if fi, err := os.Stat(filepath.Join(dir, name)); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v", name, fi.Mode(), err)
		}
	}
}
