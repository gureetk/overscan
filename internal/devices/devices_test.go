// SPDX-License-Identifier: AGPL-3.0-or-later

package devices

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// A fictional MAC address.
const testMAC = "00:1A:79:00:00:01"

func newRegistry(t *testing.T, log io.Writer) (*Registry, *store.Store, *time.Time) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(log, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	now := time.Date(2026, 11, 13, 2, 0, 0, 0, time.UTC)
	g := New(st, logger)
	g.Now = func() time.Time { return now }
	return g, st, &now
}

func TestContactRecordsUnknownDevicesAsPending(t *testing.T) {
	var log bytes.Buffer
	g, st, now := newRegistry(t, &log)
	ctx := context.Background()

	d, err := g.Contact(ctx, "00-1a-79-00-00-01", "MAG322")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == 0 || d.MAC != testMAC || d.State != store.DevicePending || d.Model != "MAG322" || !d.LastSeen.Equal(*now) {
		t.Errorf("device %+v", d)
	}
	if got, err := st.Device(ctx, testMAC); err != nil || got.ID != d.ID {
		t.Errorf("stored device %+v, %v", got, err)
	}
	if !strings.Contains(log.String(), "new device waiting for approval") || !strings.Contains(log.String(), "overscan devices approve "+testMAC) {
		t.Errorf("log %q", log.String())
	}

	log.Reset()
	if d, err := g.Contact(ctx, testMAC, ""); err != nil || d.State != store.DevicePending || d.Model != "MAG322" {
		t.Errorf("second contact: %+v, %v", d, err)
	}
	if log.Len() != 0 {
		t.Errorf("a known device was logged again: %q", log.String())
	}
}

func TestKnownRecordsNoNewDevice(t *testing.T) {
	g, st, now := newRegistry(t, io.Discard)
	ctx := context.Background()
	if _, err := g.Known(ctx, testMAC, "MAG322"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown device: %v", err)
	}
	if n, err := st.CountDevices(ctx, store.DevicePending); err != nil || n != 0 {
		t.Errorf("%d pending devices, %v", n, err)
	}
	if _, err := st.ApproveDevice(ctx, testMAC, 1, "Living room", *now); err != nil {
		t.Fatal(err)
	}
	if d, err := g.Known(ctx, testMAC, ""); err != nil || d.State != store.DeviceApproved || d.Label != "Living room" {
		t.Errorf("approved device: %+v, %v", d, err)
	}
}

func TestPendingDevicesAreCapped(t *testing.T) {
	var log bytes.Buffer
	g, st, now := newRegistry(t, &log)
	ctx := context.Background()
	for i := range MaxPending {
		if _, err := st.SeeDevice(ctx, fmt.Sprintf("00:1A:79:00:%02X:%02X", i/256, i%256), "", *now); err != nil {
			t.Fatal(err)
		}
	}
	const extra = "00:1A:79:FF:FF:FF"
	d, err := g.Contact(ctx, extra, "MAG322")
	if err != nil || d.ID != 0 || d.State != store.DevicePending || d.MAC != extra {
		t.Errorf("device past the cap: %+v, %v", d, err)
	}
	if _, err := st.Device(ctx, extra); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("a device past the cap was recorded: %v", err)
	}
	if strings.Count(log.String(), "too many devices") != 1 {
		t.Errorf("log %q", log.String())
	}

	// The warning repeats at most once a minute.
	if _, err := g.Contact(ctx, extra, ""); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(2 * time.Minute)
	if _, err := g.Contact(ctx, extra, ""); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(log.String(), "too many devices"); n != 2 {
		t.Errorf("warned %d times, want 2: %q", n, log.String())
	}
}

func TestLastSeenIsWrittenAtMostOnceAMinute(t *testing.T) {
	g, st, now := newRegistry(t, io.Discard)
	ctx := context.Background()
	start := *now
	if _, err := g.Contact(ctx, testMAC, "MAG322"); err != nil {
		t.Fatal(err)
	}

	*now = start.Add(30 * time.Second)
	if d, err := g.Known(ctx, testMAC, ""); err != nil || !d.LastSeen.Equal(start) {
		t.Errorf("after 30 s: last seen %v, %v; want %v", d.LastSeen, err, start)
	}
	// A different model is written at once.
	if d, err := g.Known(ctx, testMAC, "MAG324"); err != nil || d.Model != "MAG324" || !d.LastSeen.Equal(*now) {
		t.Errorf("new model: %+v, %v", d, err)
	}

	*now = start.Add(2 * time.Minute)
	if _, err := g.Known(ctx, testMAC, ""); err != nil {
		t.Fatal(err)
	}
	if d, err := st.Device(ctx, testMAC); err != nil || !d.LastSeen.Equal(*now) {
		t.Errorf("after 2 min: last seen %v, %v; want %v", d.LastSeen, err, *now)
	}
}
