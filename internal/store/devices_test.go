// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// Fictional MAC addresses (the 00:1A:79 prefix with made-up device parts).
const (
	macA = "00:1A:79:00:00:01"
	macB = "00:1A:79:00:00:02"
)

func TestParseMAC(t *testing.T) {
	for in, want := range map[string]string{
		"00:1a:79:00:00:01":   macA,
		"00-1A-79-00-00-01":   macA,
		"001a.7900.0001":      macA,
		"001A79000001":        macA,
		" 00:1A:79:00:00:01 ": macA,
	} {
		got, err := ParseMAC(in)
		if err != nil || got != want {
			t.Errorf("ParseMAC(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "00:1A:79:00:00", "00:1A:79:00:00:01:02:03", "00:1A:79:00:00:GG", "00:1A:79:00:00:01x", "001A7900000"} {
		if got, err := ParseMAC(in); err == nil {
			t.Errorf("ParseMAC(%q) = %q, want an error", in, got)
		}
	}
}

func TestSeeDeviceAddsPendingDevice(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	at := time.Date(2026, 11, 13, 1, 2, 3, 0, time.UTC)

	d, err := s.SeeDevice(ctx, "00-1a-79-00-00-01", "MAG322", at)
	if err != nil {
		t.Fatal(err)
	}
	if d.MAC != macA || d.State != DevicePending || d.AccountID != 0 || d.Model != "MAG322" || !d.LastSeen.Equal(at) {
		t.Errorf("device %+v", d)
	}

	// A later contact updates the time, and keeps the model if none is given.
	later := at.Add(time.Hour)
	d, err = s.SeeDevice(ctx, macA, "", later)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DevicePending || d.Model != "MAG322" || !d.LastSeen.Equal(later) {
		t.Errorf("device after a second contact %+v", d)
	}
}

func TestApproveDevice(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.SeeDevice(ctx, macA, "MAG322", time.Now()); err != nil {
		t.Fatal(err)
	}

	d, err := s.ApproveDevice(ctx, macA, 1, "Living room", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DeviceApproved || d.AccountID != 1 || d.Label != "Living room" || d.Model != "MAG322" {
		t.Errorf("approved device %+v", d)
	}

	// Approving again without a label keeps the label.
	if d, err = s.ApproveDevice(ctx, macA, 1, "", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if d.Label != "Living room" {
		t.Errorf("label %q after approving again", d.Label)
	}

	// Seeing an approved device does not change its state.
	if d, err = s.SeeDevice(ctx, macA, "MAG322", time.Now()); err != nil {
		t.Fatal(err)
	}
	if d.State != DeviceApproved {
		t.Errorf("state %q after a contact", d.State)
	}
}

func TestApproveUnknownDeviceAddsIt(t *testing.T) {
	s := openTest(t, newPath(t))
	d, err := s.ApproveDevice(context.Background(), macB, 1, "", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DeviceApproved || !d.LastSeen.IsZero() {
		t.Errorf("device %+v", d)
	}
}

func TestApproveForMissingAccount(t *testing.T) {
	s := openTest(t, newPath(t))
	if _, err := s.ApproveDevice(context.Background(), macA, 42, "", time.Unix(0, 0)); !errors.Is(err, ErrNotFound) {
		t.Errorf("error %v, want ErrNotFound", err)
	}
}

func TestBlockAndRemoveDevice(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.ApproveDevice(ctx, macA, 1, "Kitchen", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}

	d, err := s.BlockDevice(ctx, macA)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DeviceBlocked || d.Label != "Kitchen" {
		t.Errorf("blocked device %+v", d)
	}

	if err := s.RemoveDevice(ctx, macA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Device(ctx, macA); !errors.Is(err, ErrNotFound) {
		t.Errorf("device after removal: %v", err)
	}
	if err := s.RemoveDevice(ctx, macA); !errors.Is(err, ErrNotFound) {
		t.Errorf("removing again: %v, want ErrNotFound", err)
	}

	// A removed device that comes back is pending again.
	if d, err = s.SeeDevice(ctx, macA, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if d.State != DevicePending {
		t.Errorf("state %q after coming back", d.State)
	}
}

func TestDevicesListsPendingFirst(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.ApproveDevice(ctx, macA, 1, "", time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SeeDevice(ctx, macB, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	ds, err := s.Devices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 || ds[0].MAC != macB || ds[1].MAC != macA {
		t.Errorf("devices %+v", ds)
	}
}

func TestInvalidMACIsRejected(t *testing.T) {
	s := openTest(t, newPath(t))
	if _, err := s.SeeDevice(context.Background(), "not a mac", "", time.Now()); !errors.Is(err, ErrInvalidMAC) {
		t.Errorf("error %v, want ErrInvalidMAC", err)
	}
}

func TestSeeDeviceCutsLongModel(t *testing.T) {
	s := openTest(t, newPath(t))
	d, err := s.SeeDevice(context.Background(), macA, strings.Repeat("é", 80), time.Now()) // 160 bytes
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Model) > maxModelLen || !utf8.ValidString(d.Model) || d.Model == "" {
		t.Errorf("model %q (%d bytes)", d.Model, len(d.Model))
	}
}

func TestSchemaRejectsApprovedDeviceWithoutAccount(t *testing.T) {
	s := openTest(t, newPath(t))
	_, err := s.db.ExecContext(context.Background(), "INSERT INTO devices (mac, state) VALUES (?, 'approved')", macA)
	if err == nil {
		t.Error("an approved device without an account was accepted")
	}
	_, err = s.db.ExecContext(context.Background(), "INSERT INTO devices (mac) VALUES ('00:1a:79:00:00:01')")
	if err == nil {
		t.Error("a lower-case MAC address was accepted")
	}
}

func TestSetDeviceLabel(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.ApproveDevice(ctx, "00:1A:79:00:00:31", 1, "Kitchen", time.Now()); err != nil {
		t.Fatal(err)
	}
	d, err := s.SetDeviceLabel(ctx, "00-1a-79-00-00-31", "Living room")
	if err != nil || d.Label != "Living room" || d.State != DeviceApproved {
		t.Fatalf("got %+v, %v", d, err)
	}
	if d, err = s.SetDeviceLabel(ctx, "00:1A:79:00:00:31", ""); err != nil || d.Label != "" {
		t.Errorf("clearing: %+v, %v", d, err)
	}
	if _, err := s.SetDeviceLabel(ctx, "00:1A:79:00:00:32", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown device: %v", err)
	}
	if _, err := s.SetDeviceLabel(ctx, "not a mac", "x"); !errors.Is(err, ErrInvalidMAC) {
		t.Errorf("not a MAC address: %v", err)
	}
}

func TestPendingDevices(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if n, err := s.PendingDevices(ctx); err != nil || n != 0 {
		t.Fatalf("%d, %v", n, err)
	}
	for _, mac := range []string{"00:1A:79:00:00:41", "00:1A:79:00:00:42"} {
		if _, err := s.SeeDevice(ctx, mac, "MAG322", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.ApproveDevice(ctx, "00:1A:79:00:00:42", 1, "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, err := s.PendingDevices(ctx); err != nil || n != 1 {
		t.Errorf("%d pending, %v; want 1", n, err)
	}
}

// FuzzParseMAC reads MAC addresses as devices send them, in cookies and
// bodies: what it accepts comes out in the one form the store keeps, which
// reads back as itself.
func FuzzParseMAC(f *testing.F) {
	for _, s := range []string{"00:1A:79:00:00:01", "00-1a-79-00-00-01", "001a.7900.0001", "001A79000001", " 00:1A:79:00:00:01 ", "00:1A:79:00:00:01:02:03"} {
		f.Add(s)
	}
	form := regexp.MustCompile(`^([0-9A-F]{2}:){5}[0-9A-F]{2}$`)
	f.Fuzz(func(t *testing.T, s string) {
		mac, err := ParseMAC(s)
		if err != nil {
			return
		}
		if !form.MatchString(mac) {
			t.Errorf("ParseMAC(%q) = %q", s, mac)
		}
		if again, err := ParseMAC(mac); err != nil || again != mac {
			t.Errorf("ParseMAC(%q) = %q, %v", mac, again, err)
		}
	})
}
