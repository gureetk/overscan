// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/store"
)

func TestBackupCommand(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	path := configFile(t, "data_dir: \""+dataDir+"\"\n")
	backup := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, append([]string{"-config", path, "backup"}, args...), nil, &stdout, &stderr, noEnv)
		return code, stdout.String(), stderr.String()
	}

	if code, _, errOut := backup(); code != 1 || !strings.Contains(errOut, "no database at") {
		t.Errorf("no database: exit %d, %q", code, errOut)
	}
	st, err := store.Open(ctx, filepath.Join(dataDir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Into the backups folder, while the database is open.
	code, out, errOut := backup()
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "overscan-") {
		t.Fatalf("backups %v, %v", entries, err)
	}
	made := filepath.Join(dataDir, "backups", entries[0].Name())
	for _, want := range []string{"Copied the database (schema ", made, "the key file " + filepath.Join(dataDir, "secret.key")} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in %q", want, out)
		}
	}
	if fi, err := os.Stat(filepath.Join(dataDir, "backups")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the backups folder: %v, %v", fi.Mode(), err)
	}

	// To a file named, never over one.
	named := filepath.Join(t.TempDir(), "copy.db")
	if code, out, errOut := backup(named); code != 0 || !strings.Contains(out, named) {
		t.Errorf("a named file: exit %d, %q, %q", code, out, errOut)
	}
	if code, _, errOut := backup(named); code != 1 || !strings.Contains(errOut, "file exists") {
		t.Errorf("over a file: exit %d, %q", code, errOut)
	}
	for _, args := range [][]string{{"a", "b"}, {"-x"}} {
		if code, _, _ := backup(args...); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	if code, out, _ := backup("-h"); code != 0 || !strings.Contains(out, "overscan backup [FILE]") {
		t.Errorf("help: exit %d, %q", code, out)
	}
}
