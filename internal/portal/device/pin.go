// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"sync"
	"time"
)

// The parental PIN (D55): a box gets five tries, and after a fifth wrong PIN
// within a quarter of an hour it waits five minutes.
const (
	pinTries   = 5
	pinWindow  = 15 * time.Minute
	pinLockout = 5 * time.Minute
)

// pinGuard counts each box's wrong PINs.
type pinGuard struct {
	mu       sync.Mutex
	failures map[int64][]time.Time // by device
}

// recent returns a device's wrong PINs within the window before now.
func (g *pinGuard) recent(deviceID int64, now time.Time) []time.Time {
	var out []time.Time
	for _, at := range g.failures[deviceID] {
		if now.Sub(at) < pinWindow {
			out = append(out, at)
		}
	}
	return out
}

// wait returns how long a device must wait before it may try a PIN, 0 if it
// may now.
func (g *pinGuard) wait(deviceID int64, now time.Time) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	recent := g.recent(deviceID, now)
	if len(recent) < pinTries {
		return 0
	}
	return max(0, recent[len(recent)-1].Add(pinLockout).Sub(now))
}

// fail records a wrong PIN.
func (g *pinGuard) fail(deviceID int64, now time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.failures == nil {
		g.failures = map[int64][]time.Time{}
	}
	g.failures[deviceID] = append(g.recent(deviceID, now), now)
}

// reset forgets a device's wrong PINs, after a right one.
func (g *pinGuard) reset(deviceID int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, deviceID)
}
