// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

func noEnv(string) (string, bool) { return "", false }

func configFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "overscan.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestVersionFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-version"}, nil, &stdout, &stderr, noEnv); code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}
	if stdout.String() != "dev\n" {
		t.Errorf("printed %q", stdout.String())
	}
}

func TestMissingNamedConfigFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	if code := run(context.Background(), []string{"-config", missing}, nil, &stdout, &stderr, noEnv); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "absent.yaml") {
		t.Errorf("stderr does not name the file: %q", stderr.String())
	}
}

func TestInvalidLogLevelFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	path := configFile(t, "log:\n  level: loud\n")
	if code := run(context.Background(), []string{"-config", path}, nil, &stdout, &stderr, noEnv); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
}

func TestStartsAndStops(t *testing.T) {
	var stdout, stderr bytes.Buffer
	dataDir := filepath.Join(t.TempDir(), "data")
	path := configFile(t, "listen: \"127.0.0.1:0\"\ndata_dir: \""+dataDir+"\"\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already done: the server starts, then shuts straight down
	if code := run(ctx, []string{"-config", path}, nil, &stdout, &stderr, noEnv); code != 0 {
		t.Fatalf("exit code %d: %s", code, stderr.String())
	}
	for _, want := range []string{"starting", "database ready", "listening", "stopped"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("log lacks %q: %s", want, stderr.String())
		}
	}

	fi, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o700 {
		t.Errorf("data directory has mode %o, want 700", perm)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "overscan.db")); err != nil {
		t.Errorf("no database: %v", err)
	}
}

func TestUnusableDataDirFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	path := configFile(t, "listen: \"127.0.0.1:0\"\ndata_dir: \""+filepath.Join(file, "data")+"\"\n")
	if code := run(context.Background(), []string{"-config", path}, nil, &stdout, &stderr, noEnv); code != 1 {
		t.Errorf("exit code %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "data directory") {
		t.Errorf("log does not mention the data directory: %s", stderr.String())
	}
}

func TestKeyFile(t *testing.T) {
	ctx := context.Background()
	dataDir := filepath.Join(t.TempDir(), "data")
	keyPath := filepath.Join(dataDir, "secret.key")
	path := configFile(t, "listen: \"127.0.0.1:0\"\ndata_dir: \""+dataDir+"\"\n")
	start := func() (int, string) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		done, cancel := context.WithCancel(ctx)
		cancel() // the server starts, then shuts straight down
		code := run(done, []string{"-config", path}, nil, &stdout, &stderr, noEnv)
		return code, stderr.String()
	}

	// The first start makes the key file, readable by its owner only.
	code, log := start()
	if code != 0 || !strings.Contains(log, "made the key file") {
		t.Fatalf("first start: exit %d: %s", code, log)
	}
	fi, err := os.Stat(keyPath)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("key file: %v, %v", fi, err)
	}
	first, _ := os.ReadFile(keyPath)
	if code, log = start(); code != 0 || strings.Contains(log, "made the key file") {
		t.Errorf("second start: exit %d: %s", code, log)
	}
	if again, _ := os.ReadFile(keyPath); !bytes.Equal(again, first) {
		t.Error("the key file changed")
	}

	// A channel sealed with that key; then the key file holds another.
	key, _, err := secrets.LoadKey(keyPath, false)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.NewBox(key)
	st, err := store.Open(ctx, filepath.Join(dataDir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(box)
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1})
	if err == nil {
		_, err = st.ApplyCatalog(ctx, srcID, []store.CatalogChannel{{IdentityKey: "a", Name: "Alpha", StreamURL: "http://streams.example/a"}}, time.Now())
	}
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(secrets.NewKey())+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, log = start(); code != 0 || !strings.Contains(log, "encrypted with another key file") || !strings.Contains(log, "source=main channels=1") {
		t.Errorf("another key: exit %d: %s", code, log)
	}

	// A key file that others can read is reported; a damaged one stops the
	// server, and stays as it is.
	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if code, log = start(); code != 0 || !strings.Contains(log, "others than its owner can read the key file") {
		t.Errorf("readable by others: exit %d: %s", code, log)
	}
	if err := os.WriteFile(keyPath, []byte("damaged\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, log = start(); code != 1 || !strings.Contains(log, "cannot use the key file") {
		t.Errorf("damaged: exit %d: %s", code, log)
	}
	if data, _ := os.ReadFile(keyPath); string(data) != "damaged\n" {
		t.Error("the damaged key file was replaced")
	}
}

func TestImportsTheConfigurationsSources(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	playlist := filepath.Join(dir, "main.m3u")
	if err := os.WriteFile(playlist, []byte("#EXTM3U\n#EXTINF:-1,Alpha\nhttp://streams.example/a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := configFile(t, "listen: \"127.0.0.1:0\"\ndata_dir: \""+dataDir+"\"\nsources:\n  - name: main\n    type: m3u\n    url: "+playlist+"\n")
	start := func() string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		done, cancel := context.WithCancel(ctx)
		cancel()
		if code := run(done, []string{"-config", path}, nil, &stdout, &stderr, noEnv); code != 0 {
			t.Fatalf("exit %d: %s", code, stderr.String())
		}
		return stderr.String()
	}

	if log := start(); !strings.Contains(log, "imported a source from the configuration file") || !strings.Contains(log, "source=main") ||
		strings.Contains(log, "level=ERROR") {
		t.Errorf("first start: %s", log)
	}
	if log := start(); !strings.Contains(log, "level=WARN msg=\"the configuration file's source was imported before") {
		t.Errorf("second start: %s", log)
	}

	key, _, err := secrets.LoadKey(filepath.Join(dataDir, "secret.key"), false)
	if err != nil {
		t.Fatal(err)
	}
	box, _ := secrets.NewBox(key)
	st, err := store.Open(ctx, filepath.Join(dataDir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	st.UseKey(box)
	srcs, err := st.Sources(ctx)
	if err != nil || len(srcs) != 1 || srcs[0].Name != "main" || srcs[0].Host != "file main.m3u" || srcs[0].Address != store.AddressSaved {
		t.Fatalf("sources %+v, %v", srcs, err)
	}
	if settings, err := st.SourceSettings(ctx, srcs[0].ID); err != nil || settings.URL != playlist {
		t.Errorf("settings %+v, %v", settings, err)
	}
}

func TestHubSources(t *testing.T) {
	got := hubSources([]store.Source{
		{ID: 1, Name: "main", Type: source.TypeM3U, MaxConnections: 2},
		{ID: 2, Name: "portal", Type: source.TypeStalker, MaxConnections: 1},
		{ID: 3, Name: "two", Type: source.TypeStalker, UserAgent: "SomePlayer/1.0"},
	})
	want := map[int64]hub.Source{
		1: {Name: "main", MaxConnections: 2},
		2: {Name: "portal", MaxConnections: 1, UserAgent: stalker.DefaultUserAgent, Resolves: true},
		3: {Name: "two", UserAgent: "SomePlayer/1.0", Resolves: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hub sources %+v, want %+v", got, want)
	}
}

// Help answers on stdout, before the configuration is read, so that it
// shows whatever the file holds and whoever runs it.
func TestHelp(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.yaml")
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-h"}, "overscan help [COMMAND]"},
		{[]string{"--help"}, "overscan [-config FILE] debug COMMAND"},
		{[]string{"help"}, "-config string"},
		{[]string{"help", "devices"}, "overscan devices approve"},
		{[]string{"help", "nothing"}, "overscan [-config FILE] backup [FILE]"},
		{[]string{"-config", missing, "devices", "-h"}, "overscan devices list"},
		{[]string{"-config", missing, "devices", "help"}, "overscan devices list"},
		{[]string{"-config", missing, "devices", "approve", "--help"}, "overscan devices approve"},
		{[]string{"-config", missing, "admin", "password", "-h"}, "overscan admin password"},
		{[]string{"-config", missing, "debug", "channel", "-help"}, "overscan debug channel"},
		{[]string{"-config", missing, "backup", "-h"}, "overscan backup [FILE]"},
		{[]string{"-config", missing, "gpu", "-h"}, "Tests what the server's graphics can do for ffmpeg"},
		{[]string{"help", "gpu"}, "overscan gpu"},
	} {
		var stdout, stderr bytes.Buffer
		code := run(context.Background(), tc.args, nil, &stdout, &stderr, noEnv)
		if code != 0 || !strings.Contains(stdout.String(), tc.want) || stderr.Len() != 0 {
			t.Errorf("%q: exit %d, stdout %q, stderr %q", tc.args, code, stdout.String(), stderr.String())
		}
	}

	// backup's argument is a file, which may be called help.
	if _, ok := helpFor([]string{"backup", "help"}); ok {
		t.Error("backup help asks for help")
	}

	// A mistake gives the help on stderr.
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"-nope"}, nil, &stdout, &stderr, noEnv); code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("-nope: exit %d, stdout %q, stderr %q", code, stdout.String(), stderr.String())
	}
	// An unknown command is named before the configuration is read.
	stdout.Reset()
	stderr.Reset()
	if code := run(context.Background(), []string{"-config", missing, "halp"}, nil, &stdout, &stderr, noEnv); code != 2 ||
		!strings.Contains(stderr.String(), `unknown command "halp"`) || !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("halp: exit %d, stderr %q", code, stderr.String())
	}
}
