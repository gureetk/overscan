// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/store"
)

// adminSetup creates a database the way the server would, makes password
// hashes cheap, and returns a config file that points at the database, and
// the database's path.
func adminSetup(t *testing.T) (string, string) {
	t.Helper()
	saved := hashParams
	hashParams = admin.Params{Memory: 64, Time: 1, Threads: 1}
	t.Cleanup(func() { hashParams = saved })
	dataDir := t.TempDir()
	db := filepath.Join(dataDir, store.FileName)
	st, err := store.Open(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	return configFile(t, "data_dir: \""+dataDir+"\"\n"), db
}

// adminCmd runs "overscan -config path admin args..." with stdin and returns
// the exit code and output.
func adminCmd(t *testing.T, path, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), append([]string{"-config", path, "admin"}, args...), strings.NewReader(stdin), &stdout, &stderr, noEnv)
	return code, stdout.String(), stderr.String()
}

// administrator reads the administrator from the database.
func administrator(t *testing.T, db string) store.AdminUser {
	t.Helper()
	st, err := store.Open(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	u, err := st.AdminUser(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAdminPassword(t *testing.T) {
	path, db := adminSetup(t)
	ctx := context.Background()

	code, out, errOut := adminCmd(t, path, "correct horse\n", "password")
	if code != 0 || !strings.Contains(out, "password set for admin") || !strings.Contains(out, "http://<this server>:8080/admin/") {
		t.Fatalf("exit %d, %q %q", code, out, errOut)
	}
	u := administrator(t, db)
	if ok, err := admin.CheckPassword(ctx, u.PasswordHash, "correct horse"); u.Username != "admin" || !ok || err != nil {
		t.Errorf("stored %+v: %v, %v", u, ok, err)
	}

	// -user renames the administrator, and the name sticks.
	if code, out, errOut = adminCmd(t, path, "battery staple\r\n", "password", "-user", "keeper"); code != 0 || !strings.Contains(out, "password set for keeper") {
		t.Fatalf("-user: exit %d, %q %q", code, out, errOut)
	}
	if code, out, _ = adminCmd(t, path, "battery staple again", "password"); code != 0 || !strings.Contains(out, "password set for keeper") {
		t.Errorf("without -user after -user: exit %d, %q", code, out)
	}
	u = administrator(t, db)
	if ok, _ := admin.CheckPassword(ctx, u.PasswordHash, "battery staple again"); !ok {
		t.Error("the password read without a line break does not match")
	}
}

func TestAdminPasswordEndsSessions(t *testing.T) {
	path, db := adminSetup(t)
	if code, _, errOut := adminCmd(t, path, "correct horse\n", "password"); code != 0 {
		t.Fatal(errOut)
	}
	st, err := store.Open(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := st.CreateAdminSession(context.Background(), "hash", "csrf", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := adminCmd(t, path, "battery staple\n", "password"); code != 0 {
		t.Fatal(errOut)
	}
	if _, err := st.AdminSession(context.Background(), "hash"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a session survived a new password: %v", err)
	}
}

func TestAdminPasswordErrors(t *testing.T) {
	path, _ := adminSetup(t)
	for name, tc := range map[string]struct {
		stdin string
		args  []string
		code  int
		want  string
	}{
		"short":            {"short\n", []string{"password"}, 1, "at least 8 characters"},
		"nothing":          {"", []string{"password"}, 1, "no password on standard input"},
		"empty line":       {"\n", []string{"password"}, 1, "no password on standard input"},
		"bad username":     {"correct horse\n", []string{"password", "-user", "bad name"}, 1, "a username has"},
		"extra argument":   {"correct horse\n", []string{"password", "extra"}, 2, "Usage:"},
		"unknown command":  {"", []string{"sessions"}, 2, `unknown admin command "sessions"`},
		"no command":       {"", nil, 2, "Usage:"},
		"unknown flag":     {"", []string{"password", "-x"}, 2, "flag provided but not defined"},
		"control in input": {"correct\x00horse\n", []string{"password"}, 1, "NUL"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, errOut := adminCmd(t, path, tc.stdin, tc.args...)
			if code != tc.code || !strings.Contains(errOut, tc.want) {
				t.Errorf("exit %d, %q; want %d and %q", code, errOut, tc.code, tc.want)
			}
		})
	}
	if code, out, _ := adminCmd(t, path, "", "-h"); code != 0 || !strings.Contains(out, "overscan admin password") {
		t.Errorf("help: exit %d, %q", code, out)
	}
}

func TestAdminPasswordNeedsDatabase(t *testing.T) {
	path := configFile(t, "data_dir: \""+t.TempDir()+"\"\n")
	code, _, errOut := adminCmd(t, path, "correct horse\n", "password")
	if code != 1 || !strings.Contains(errOut, "start the server once") {
		t.Errorf("exit %d, %q", code, errOut)
	}
}

func TestAdminURL(t *testing.T) {
	for _, tc := range []struct{ listen, admin, want string }{
		{":8080", "", "http://<this server>:8080/admin/"},
		{"0.0.0.0:8080", "", "http://<this server>:8080/admin/"},
		{"192.0.2.5:8080", "", "http://192.0.2.5:8080/admin/"},
		{":8080", "127.0.0.1:8081", "http://127.0.0.1:8081/admin/"},
		{":8080", "[::1]:8081", "http://[::1]:8081/admin/"},
	} {
		cfg := config.Default()
		cfg.Listen, cfg.AdminListen = tc.listen, tc.admin
		if got := adminURL(cfg); got != tc.want {
			t.Errorf("%q, %q: got %q, want %q", tc.listen, tc.admin, got, tc.want)
		}
	}
}
