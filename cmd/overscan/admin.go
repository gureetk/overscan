// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/term"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/store"
)

const adminUsage = `Usage:
  overscan admin password [-user NAME]

password sets the administrator's password, which signs in to the admin at
/admin/. It asks for the new password twice; from a pipe, it reads one line
instead. -user also sets the username, which is "admin" unless it was set
before. A new password signs out every session. Run it as the user the server
runs as, after the server has started once.
`

// defaultAdminUsername is the administrator's username until one is chosen.
const defaultAdminUsername = "admin"

// adminUsername matches a username the administrator may choose.
var adminUsername = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,63}$`)

// hashParams are the costs of new password hashes; tests lower them.
var hashParams = admin.DefaultParams

// adminCommand runs "overscan admin ..." and returns the exit code.
func adminCommand(ctx context.Context, cfg config.Config, log *slog.Logger, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, adminUsage)
		return 2
	}
	switch args[0] {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, adminUsage)
		return 0
	case "password":
	default:
		fmt.Fprintf(stderr, "overscan: unknown admin command %q\n\n%s", args[0], adminUsage)
		return 2
	}

	flags := flag.NewFlagSet("overscan admin password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, adminUsage) }
	user := flags.String("user", "", "the administrator's username; \"admin\" unless set before")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return 2
	}
	if *user != "" && !adminUsername.MatchString(*user) {
		fmt.Fprintln(stderr, "overscan: a username has 1 to 64 letters, digits, dots, hyphens, underscores or @, starting with a letter or digit")
		return 1
	}

	st, err := openExistingStore(ctx, cfg, log)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	defer st.Close()

	username := *user
	if username == "" {
		username = defaultAdminUsername
		if u, err := st.AdminUser(ctx); err == nil {
			username = u.Username
		} else if !errors.Is(err, store.ErrNotFound) {
			fmt.Fprintf(stderr, "overscan: %v\n", err)
			return 1
		}
	}

	password, err := readNewPassword(stdin, stderr)
	if err == nil {
		err = admin.CheckNewPassword(password)
	}
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	hash, err := admin.HashPassword(ctx, password, hashParams)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	if err := st.SetAdminPassword(ctx, username, hash, time.Now(), 0); err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "password set for %s; every admin session is signed out\nsign in at %s\n", username, adminURL(cfg))
	return 0
}

// readNewPassword asks for a new password twice on a terminal, without
// showing it, and otherwise reads one line.
func readNewPassword(stdin io.Reader, prompt io.Writer) (string, error) {
	if f, ok := stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		read := func(label string) (string, error) {
			fmt.Fprint(prompt, label)
			b, err := term.ReadPassword(int(f.Fd()))
			fmt.Fprintln(prompt)
			return string(b), err
		}
		first, err := read("New password: ")
		if err != nil {
			return "", err
		}
		second, err := read("Again: ")
		if err != nil {
			return "", err
		}
		if first != second {
			return "", errors.New("the passwords differ")
		}
		return first, nil
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, admin.MaxPasswordBytes+2)).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if line == "" {
		return "", errors.New("no password on standard input")
	}
	return line, nil
}

// adminURL says where the admin is, as far as the configuration tells.
func adminURL(cfg config.Config) string {
	addr := cfg.Listen
	if cfg.AdminListen != "" {
		addr = cfg.AdminListen
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "/admin/"
	}
	switch {
	case host == "" || host == "0.0.0.0" || host == "::":
		host = "<this server>"
	case strings.Contains(host, ":"):
		host = "[" + host + "]"
	}
	return "http://" + host + ":" + port + "/admin/"
}
