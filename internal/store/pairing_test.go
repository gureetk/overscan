// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHashSecret(t *testing.T) {
	h := HashSecret("secret")
	if len(h) != 64 || h != HashSecret("secret") || h == HashSecret("other") {
		t.Errorf("HashSecret = %q", h)
	}
}

func TestPairingByMAC(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	t0 := time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)

	// A new box asks to be paired; it is added as pending.
	d, err := s.RequestPairing(ctx, macA, "MAG322", "111111", HashSecret("first"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DevicePending || d.Paired() || d.PairingCode != "111111" || d.PairingHash != HashSecret("first") ||
		!d.PairingAt.Equal(t0) || !d.LastSeen.Equal(t0) || d.Model != "MAG322" {
		t.Errorf("after the request: %+v", d)
	}

	// A new request replaces it.
	t1 := t0.Add(time.Minute)
	if d, err = s.RequestPairing(ctx, macA, "", "222222", HashSecret("second"), t1); err != nil {
		t.Fatal(err)
	}
	if d.PairingCode != "222222" || d.PairingHash != HashSecret("second") || d.Model != "MAG322" {
		t.Errorf("after a second request: %+v", d)
	}

	// Approving the box by its MAC address pairs it with the request.
	t2 := t1.Add(time.Minute)
	if d, err = s.ApproveDevice(ctx, macA, 1, "", t2); err != nil {
		t.Fatal(err)
	}
	if d.State != DeviceApproved || d.SecretHash != HashSecret("second") || !d.PairedAt.Equal(t2) ||
		d.PairingCode != "" || d.PairingHash != "" || !d.PairingAt.IsZero() {
		t.Errorf("after approval: %+v", d)
	}

	// A paired box keeps its secret when approved again by MAC address,
	// and a new request waits for approval by its code.
	if _, err = s.RequestPairing(ctx, macA, "", "333333", HashSecret("third"), t2); err != nil {
		t.Fatal(err)
	}
	if d, err = s.ApproveDevice(ctx, macA, 1, "Living room", t2.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if d.SecretHash != HashSecret("second") || !d.PairedAt.Equal(t2) || d.PairingCode != "333333" || d.Label != "Living room" {
		t.Errorf("approved again: %+v", d)
	}
}

func TestApprovePairing(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	t0 := time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)
	if _, err := s.RequestPairing(ctx, macA, "", "111111", HashSecret("first"), t0); err != nil {
		t.Fatal(err)
	}
	d, err := s.ApprovePairing(ctx, "111111", 1, "Bedroom", t0)
	if err != nil {
		t.Fatal(err)
	}
	if d.MAC != macA || d.State != DeviceApproved || d.AccountID != 1 || d.Label != "Bedroom" ||
		d.SecretHash != HashSecret("first") || d.PairingCode != "" {
		t.Errorf("approved: %+v", d)
	}

	// Approving a later request by its code replaces the secret.
	t1 := t0.Add(time.Hour)
	if _, err := s.RequestPairing(ctx, macA, "", "222222", HashSecret("second"), t1); err != nil {
		t.Fatal(err)
	}
	if d, err = s.ApprovePairing(ctx, "222222", 1, "", t1); err != nil {
		t.Fatal(err)
	}
	if d.SecretHash != HashSecret("second") || !d.PairedAt.Equal(t1) || d.Label != "Bedroom" {
		t.Errorf("paired again: %+v", d)
	}

	if _, err := s.ApprovePairing(ctx, "222222", 1, "", t1); !errors.Is(err, ErrNotFound) {
		t.Errorf("a used code: %v", err)
	}
	if _, err := s.RequestPairing(ctx, macA, "", "444444", HashSecret("x"), t1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApprovePairing(ctx, "444444", 42, "", t1); !errors.Is(err, ErrNotFound) {
		t.Errorf("a missing account: %v", err)
	}
}

func TestPairingCodesAreUnique(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	if _, err := s.RequestPairing(ctx, macA, "", "111111", HashSecret("a"), time.Unix(0, 0)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestPairing(ctx, macB, "", "111111", HashSecret("b"), time.Unix(0, 0)); !errors.Is(err, ErrCodeTaken) {
		t.Errorf("a code in use: %v", err)
	}
	if _, err := s.Device(ctx, macB); !errors.Is(err, ErrNotFound) {
		t.Errorf("the refused request added its device: %v", err)
	}
}

func TestAdoptSecret(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	t0 := time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)

	if _, err := s.SeeDevice(ctx, macB, "", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AdoptSecret(ctx, macB, HashSecret("b"), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a pending device adopted a secret: %v", err)
	}

	if _, err := s.ApproveDevice(ctx, macA, 1, "", t0); err != nil {
		t.Fatal(err)
	}
	d, err := s.AdoptSecret(ctx, macA, HashSecret("a"), t0)
	if err != nil || d.SecretHash != HashSecret("a") || !d.PairedAt.Equal(t0) {
		t.Errorf("adopted: %+v, %v", d, err)
	}
	if _, err := s.AdoptSecret(ctx, macA, HashSecret("other"), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("a paired device adopted another secret: %v", err)
	}
}

func TestBlockingDropsARequestToPair(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	t0 := time.Date(2026, 11, 13, 5, 0, 0, 0, time.UTC)
	if _, err := s.RequestPairing(ctx, macA, "", "111111", HashSecret("a"), t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApproveDevice(ctx, macA, 1, "", t0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RequestPairing(ctx, macA, "", "222222", HashSecret("b"), t0); err != nil {
		t.Fatal(err)
	}
	d, err := s.BlockDevice(ctx, macA)
	if err != nil {
		t.Fatal(err)
	}
	if d.State != DeviceBlocked || d.PairingCode != "" || d.PairingHash != "" || d.SecretHash != HashSecret("a") {
		t.Errorf("blocked: %+v", d)
	}
}
