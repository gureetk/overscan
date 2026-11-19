// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"fmt"
	"testing"
	"time"
)

func TestLimiterSlowsAnAddress(t *testing.T) {
	l := newLimiter()
	start := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for i := range maxFailures {
		if w := l.wait("192.0.2.10", start); w != 0 {
			t.Fatalf("waits %v after %d failures", w, i)
		}
		l.fail("192.0.2.10", start.Add(time.Duration(i)*time.Minute))
	}
	now := start.Add(5 * time.Minute)
	if w := l.wait("192.0.2.10", now); w != 10*time.Minute {
		t.Errorf("after %d failures: wait %v, want 10m, until the first failure leaves the window", maxFailures, w)
	}
	if w := l.wait("192.0.2.11", now); w != 0 {
		t.Errorf("another address waits %v", w)
	}
	if w := l.wait("192.0.2.10", start.Add(failWindow).Add(time.Second)); w != 0 {
		t.Errorf("once the first failure has left the window: wait %v", w)
	}

	l.fail("192.0.2.12", now)
	l.succeed("192.0.2.12")
	if _, ok := l.failures["192.0.2.12"]; ok {
		t.Error("a sign-in did not forget the address's failures")
	}
}

func TestLimiterSlowsEveryone(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for i := range maxAllFailures {
		l.fail(fmt.Sprintf("192.0.2.%d", i%200), now)
	}
	if w := l.wait("198.51.100.1", now); w != failWindow {
		t.Errorf("after %d failures from many addresses, a new address waits %v, want %v", maxAllFailures, w, failWindow)
	}
}

func TestLimiterForgetsOldAddresses(t *testing.T) {
	l := newLimiter()
	now := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	for i := range maxAddresses + 10 {
		l.fail(fmt.Sprintf("addr-%d", i), now.Add(time.Duration(i)*time.Millisecond))
	}
	if len(l.failures) != maxAddresses {
		t.Errorf("remembers %d addresses, want %d", len(l.failures), maxAddresses)
	}
	if _, ok := l.failures["addr-0"]; ok {
		t.Error("the address whose failure is oldest was kept")
	}
	if _, ok := l.failures[fmt.Sprintf("addr-%d", maxAddresses+9)]; !ok {
		t.Error("the newest address was dropped")
	}
}
