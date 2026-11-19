// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"sync"
	"time"
)

// Failed sign-ins slow down further tries (D44). An address that has failed
// maxFailures times within failWindow waits until the oldest of those
// failures leaves the window. Failures from all addresses together are held
// to maxAllFailures in the window, against guessing from many addresses.
const (
	failWindow     = 15 * time.Minute
	maxFailures    = 5
	maxAllFailures = 50
	maxAddresses   = 1024 // addresses remembered at once
)

// limiter counts failed sign-ins by address.
type limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time // by address, oldest first, within the window
	all      []time.Time            // every address, oldest first, within the window
}

func newLimiter() *limiter {
	return &limiter{failures: map[string][]time.Time{}}
}

// wait returns how long addr must wait before it may try to sign in, or 0.
func (l *limiter) wait(addr string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forget(now)
	var d time.Duration
	if fs := l.failures[addr]; len(fs) >= maxFailures {
		d = fs[len(fs)-maxFailures].Add(failWindow).Sub(now)
	}
	if len(l.all) >= maxAllFailures {
		d = max(d, l.all[len(l.all)-maxAllFailures].Add(failWindow).Sub(now))
	}
	return max(d, 0)
}

// fail records a failed sign-in from addr.
func (l *limiter) fail(addr string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.forget(now)
	if _, ok := l.failures[addr]; !ok && len(l.failures) >= maxAddresses {
		// Make room by forgetting the address whose last failure is oldest.
		var oldest string
		var at time.Time
		for a, fs := range l.failures {
			if last := fs[len(fs)-1]; oldest == "" || last.Before(at) {
				oldest, at = a, last
			}
		}
		delete(l.failures, oldest)
	}
	l.failures[addr] = append(l.failures[addr], now)
	l.all = append(l.all, now)
}

// succeed forgets the failures of addr, which has signed in.
func (l *limiter) succeed(addr string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, addr)
}

// forget drops failures older than the window.
func (l *limiter) forget(now time.Time) {
	cutoff := now.Add(-failWindow)
	for a, fs := range l.failures {
		i := 0
		for i < len(fs) && !fs[i].After(cutoff) {
			i++
		}
		if i == len(fs) {
			delete(l.failures, a)
		} else if i > 0 {
			l.failures[a] = fs[i:]
		}
	}
	i := 0
	for i < len(l.all) && !l.all[i].After(cutoff) {
		i++
	}
	l.all = l.all[i:]
}
