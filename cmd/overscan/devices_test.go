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
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/store"
)

// A fictional MAC address.
const testMAC = "00:1A:79:00:00:01"

// deviceSetup creates a database the way the server would and returns a
// config file that points at it.
func deviceSetup(t *testing.T) string {
	t.Helper()
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dataDir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return configFile(t, "data_dir: \""+dataDir+"\"\n")
}

// devices runs "overscan -config path devices args..." and returns the exit
// code and output.
func devices(t *testing.T, path string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"-config", path, "devices"}, args...), nil, &stdout, &stderr, noEnv)
	return code, stdout.String(), stderr.String()
}

func TestDevicesApproveListBlockRemove(t *testing.T) {
	path := deviceSetup(t)

	code, out, errOut := devices(t, path, "list")
	if code != 0 || !strings.Contains(out, "No devices yet") {
		t.Fatalf("empty list: exit %d, %q %q", code, out, errOut)
	}

	code, out, errOut = devices(t, path, "approve", "-label", "Living room", "00-1a-79-00-00-01")
	if code != 0 || !strings.Contains(out, "approved "+testMAC+` for account "Household"`) {
		t.Fatalf("approve: exit %d, %q %q", code, out, errOut)
	}

	code, out, _ = devices(t, path, "list")
	for _, want := range []string{testMAC, "approved", "Household", "Living room", "never"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Errorf("list lacks %q: exit %d, %q", want, code, out)
		}
	}

	if code, out, errOut = devices(t, path, "block", testMAC); code != 0 || !strings.Contains(out, "blocked "+testMAC) {
		t.Errorf("block: exit %d, %q %q", code, out, errOut)
	}
	if code, out, _ = devices(t, path, "list"); !strings.Contains(out, "blocked") {
		t.Errorf("list after block: exit %d, %q", code, out)
	}

	if code, out, errOut = devices(t, path, "remove", testMAC); code != 0 || !strings.Contains(out, "removed "+testMAC) {
		t.Errorf("remove: exit %d, %q %q", code, out, errOut)
	}
	if code, _, errOut = devices(t, path, "remove", testMAC); code != 1 || !strings.Contains(errOut, "no device") {
		t.Errorf("remove again: exit %d, %q", code, errOut)
	}
}

func TestDevicesErrors(t *testing.T) {
	path := deviceSetup(t)
	for _, c := range []struct {
		args []string
		code int
		want string
	}{
		{[]string{"approve", "-account", "42", testMAC}, 1, "no account 42"},
		{[]string{"approve", "not-a-mac"}, 1, "not a MAC address"},
		{[]string{"approve", "-label", "a\x1b[31mb", testMAC}, 1, "control characters"},
		{[]string{"approve", "-label", strings.Repeat("x", 101), testMAC}, 1, "at most 100"},
		{[]string{"approve"}, 2, "Usage"},
		{[]string{"block", "-label", "x", testMAC}, 2, "Usage"},
		{[]string{"list", "extra"}, 2, "Usage"},
		{[]string{"rename", testMAC}, 2, "unknown devices command"},
		{nil, 2, "Usage"},
	} {
		code, _, errOut := devices(t, path, c.args...)
		if code != c.code || !strings.Contains(errOut, c.want) {
			t.Errorf("%q: exit %d, %q; want exit %d and %q", c.args, code, errOut, c.code, c.want)
		}
	}
}

func TestDevicesHelp(t *testing.T) {
	path := deviceSetup(t)
	if code, out, _ := devices(t, path, "-h"); code != 0 || !strings.Contains(out, "devices approve") {
		t.Errorf("help: exit %d, %q", code, out)
	}
}

func TestDevicesNeedsDatabase(t *testing.T) {
	path := configFile(t, "data_dir: \""+t.TempDir()+"\"\n")
	code, _, errOut := devices(t, path, "list")
	if code != 1 || !strings.Contains(errOut, "no database") {
		t.Errorf("exit %d, %q", code, errOut)
	}
}

func TestCheckOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwner(path, fi, os.Geteuid()); err != nil {
		t.Errorf("own file: %v", err)
	}
	if err := checkOwner(path, fi, os.Geteuid()+1); err == nil || !strings.Contains(err.Error(), "belongs to user") {
		t.Errorf("another user's file: %v", err)
	}
}

func TestCellHidesControlCharacters(t *testing.T) {
	if got := cell("MAG\x1b[2J322\t"); got != "MAG?[2J322?" {
		t.Errorf("cell = %q", got)
	}
	if got := cell(""); got != "-" {
		t.Errorf("empty cell = %q", got)
	}
}

func TestUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := configFile(t, "")
	if code := run(context.Background(), []string{"-config", path, "serve-now"}, nil, &stdout, &stderr, noEnv); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), `unknown command "serve-now"`) {
		t.Errorf("stderr %q", stderr.String())
	}
}

func TestDebugPlaylistCommand(t *testing.T) {
	dataDir := t.TempDir()
	path := configFile(t, "listen: \"192.0.2.10:8080\"\ndata_dir: \""+dataDir+"\"\n")
	run1 := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), append([]string{"-config", path}, args...), nil, &stdout, &stderr, noEnv)
		return code, stdout.String(), stderr.String()
	}

	if code, _, errOut := run1("debug", "playlist"); code != 1 || !strings.Contains(errOut, "no admin token") {
		t.Errorf("before the server ran: exit %d, %q", code, errOut)
	}
	token, err := admin.Token(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	code, out, errOut := run1("debug", "playlist")
	if want := "http://192.0.2.10:8080/debug/lineup.m3u?token=" + token + "\n"; code != 0 || out != want {
		t.Errorf("exit %d, %q %q; want %q", code, out, errOut, want)
	}
	if code, _, _ := run1("debug"); code != 2 {
		t.Errorf("no subcommand: exit %d", code)
	}
}

func TestDevicesApproveByCode(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dataDir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RequestPairing(context.Background(), testMAC, "MAG322", "123456", store.HashSecret("secret"), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	path := configFile(t, "data_dir: \""+dataDir+"\"\n")

	if code, out, _ := devices(t, path, "list"); code != 0 || !strings.Contains(out, "code 123456") {
		t.Errorf("list: exit %d, %q", code, out)
	}
	if code, _, errOut := devices(t, path, "approve", "654321"); code != 1 || !strings.Contains(errOut, "no box is waiting with the code 654321") {
		t.Errorf("a wrong code: exit %d, %q", code, errOut)
	}
	code, out, errOut := devices(t, path, "approve", "-label", "Living room", "123456")
	if code != 0 || out != "approved "+testMAC+" for account \"Household\", paired with its secret\n" {
		t.Errorf("approve by code: exit %d, %q %q", code, out, errOut)
	}
	if code, out, _ := devices(t, path, "list"); code != 0 || !strings.Contains(out, "paired") || strings.Contains(out, "code 123456") {
		t.Errorf("list after approval: exit %d, %q", code, out)
	}
}
