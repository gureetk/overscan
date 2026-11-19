// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/store"
)

const devicesUsage = `Usage:
  overscan devices list
  overscan devices approve [-account ID] [-label TEXT] MAC|CODE
  overscan devices block MAC
  overscan devices remove MAC

A device appears as pending when it first contacts the server. Approving or
blocking a device that has not connected yet adds it. A box running the MAG
page shows a six-digit code while it waits: approve it by the code or by its
MAC address. Approving by the code also pairs a box again after it has lost
its secret. Run these commands as the user the server runs as, since the
database belongs to that user.
`

// pairingCode matches the code a box shows while it waits to be paired.
var pairingCode = regexp.MustCompile(`^[0-9]{6}$`)

// maxLabelLen caps a device label, in characters.
const maxLabelLen = 100

// devicesCommand runs "overscan devices ..." and returns the exit code.
func devicesCommand(ctx context.Context, cfg config.Config, log *slog.Logger, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, devicesUsage)
		return 2
	}
	sub, args := args[0], args[1:]
	switch sub {
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, devicesUsage)
		return 0
	case "list", "approve", "block", "remove":
	default:
		fmt.Fprintf(stderr, "overscan: unknown devices command %q\n\n%s", sub, devicesUsage)
		return 2
	}

	flags := flag.NewFlagSet("overscan devices "+sub, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() { fmt.Fprint(stderr, devicesUsage) }
	account := flags.Int64("account", 1, "ID of the account to approve the device for (approve only)")
	label := flags.String("label", "", "label for the device, such as \"Living room\" (approve only)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	wantArgs := 1
	if sub == "list" {
		wantArgs = 0
	}
	if flags.NArg() != wantArgs || (sub != "approve" && (*label != "" || *account != 1)) {
		flags.Usage()
		return 2
	}
	if err := checkLabel(*label); err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	mac := flags.Arg(0)

	st, err := openExistingStore(ctx, cfg, log)
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	defer st.Close()

	switch sub {
	case "list":
		err = listDevices(ctx, st, stdout)
	case "approve":
		err = approveDevice(ctx, st, mac, *account, *label, stdout)
	case "block":
		var d store.Device
		if d, err = st.BlockDevice(ctx, mac); err == nil {
			fmt.Fprintf(stdout, "blocked %s\n", d.MAC)
		}
	case "remove":
		if err = st.RemoveDevice(ctx, mac); err == nil {
			m, _ := store.ParseMAC(mac)
			fmt.Fprintf(stdout, "removed %s\n", m)
		} else if errors.Is(err, store.ErrNotFound) {
			err = fmt.Errorf("no device %s", mac)
		}
	}
	if err != nil {
		fmt.Fprintf(stderr, "overscan: %v\n", err)
		return 1
	}
	return 0
}

// approveDevice approves a device by its MAC address or by the code of its
// request to pair, and says what it did.
func approveDevice(ctx context.Context, st *store.Store, arg string, account int64, label string, stdout io.Writer) error {
	var d store.Device
	var err error
	byCode := pairingCode.MatchString(arg)
	if byCode {
		d, err = st.ApprovePairing(ctx, arg, account, label, time.Now())
	} else {
		d, err = st.ApproveDevice(ctx, arg, account, label, time.Now())
	}
	if errors.Is(err, store.ErrNotFound) {
		if _, aerr := st.Account(ctx, account); errors.Is(aerr, store.ErrNotFound) {
			return fmt.Errorf("no account %d", account)
		}
		if byCode {
			return fmt.Errorf("no box is waiting with the code %s; overscan devices list shows the codes", arg)
		}
	}
	if err != nil {
		return err
	}
	a, err := st.Account(ctx, d.AccountID)
	if err != nil {
		return err
	}
	paired := ""
	if d.Paired() {
		paired = ", paired with its secret"
	}
	fmt.Fprintf(stdout, "approved %s for account %q%s\n", d.MAC, a.Name, paired)
	if d.PairingCode != "" {
		fmt.Fprintf(stdout, "a box with this MAC address but without its secret asks to be paired with the code %s;\n"+
			"approve that code instead if it should replace the paired box\n", d.PairingCode)
	}
	return nil
}

// checkLabel accepts a label of up to 100 printable characters.
func checkLabel(label string) error {
	if utf8.RuneCountInString(label) > maxLabelLen {
		return fmt.Errorf("a label can have at most %d characters", maxLabelLen)
	}
	if strings.ContainsFunc(label, func(r rune) bool { return !unicode.IsPrint(r) }) {
		return fmt.Errorf("a label cannot contain control characters")
	}
	return nil
}

// openExistingStore opens the database the server created. It does not
// create one, and it refuses a database that belongs to another user.
func openExistingStore(ctx context.Context, cfg config.Config, log *slog.Logger) (*store.Store, error) {
	path := filepath.Join(cfg.DataDir, store.FileName)
	fi, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("no database at %s; start the server once to create it, or check data_dir in the configuration", path)
	}
	if err != nil {
		return nil, err
	}
	if err := checkOwner(path, fi, os.Geteuid()); err != nil {
		return nil, err
	}
	return store.Open(ctx, path, log)
}

// listDevices prints a table of the devices.
func listDevices(ctx context.Context, st *store.Store, w io.Writer) error {
	ds, err := st.Devices(ctx)
	if err != nil {
		return err
	}
	if len(ds) == 0 {
		fmt.Fprintln(w, "No devices yet. A device appears here, pending, when it first contacts the server.")
		return nil
	}
	accounts := map[int64]string{}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MAC\tSTATE\tPAIRING\tACCOUNT\tLABEL\tMODEL\tLAST SEEN")
	for _, d := range ds {
		account := "-"
		if d.AccountID != 0 {
			name, ok := accounts[d.AccountID]
			if !ok {
				a, err := st.Account(ctx, d.AccountID)
				if err != nil {
					return err
				}
				name = a.Name
				accounts[d.AccountID] = name
			}
			account = name
		}
		lastSeen := "never"
		if !d.LastSeen.IsZero() {
			lastSeen = d.LastSeen.Format("2006-01-02 15:04 UTC")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			d.MAC, d.State, pairing(d), cell(account), cell(d.Label), cell(d.Model), lastSeen)
	}
	return tw.Flush()
}

// pairing says whether a device is paired and gives the code of a request
// to pair that waits.
func pairing(d store.Device) string {
	var parts []string
	if d.Paired() {
		parts = append(parts, "paired")
	}
	if d.PairingCode != "" {
		parts = append(parts, "code "+d.PairingCode)
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, ", ")
}

// cell makes s safe to print in a table: a device reports its own model, so
// control characters, which could move the cursor or recolour a terminal,
// become "?".
func cell(s string) string {
	if s == "" {
		return "-"
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return '?'
	}, s)
}
