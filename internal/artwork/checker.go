// SPDX-License-Identifier: AGPL-3.0-or-later

package artwork

import (
	"context"
	"log/slog"
	"time"

	"overscan.gureet.ca/internal/store"
)

// Checking whether logos load (D141). A channel's logo is its own, from
// its source, if it loads, else its guide's, if it loads; so Overscan
// needs to know which load before devices ask. The checker fetches, in
// the background and one at a time, the logos that channels choose
// between: the own and the guide's of every channel that has a guide's
// logo to fall back on or to take first. A fetch keeps the logo in the
// cache, so devices get it at once later. What a fetch finds is kept only
// when it is settled: the image is there, or its server says that it is
// not, or it is no image; a fetch that fails for want of a network
// changes nothing, so that a while offline does not take logos away.

const (
	// CheckEvery is how often the checker looks for logos to check.
	CheckEvery = 5 * time.Minute
	// RecheckAfter is how long a logo that did not load waits to be
	// checked again.
	RecheckAfter = 24 * time.Hour
	// ReloadAfter is how long a logo that loaded waits to be checked
	// again: a kept logo is checked without a fetch.
	ReloadAfter = 30 * 24 * time.Hour
	// checksAtOnce is how many logos a round checks at most.
	checksAtOnce = 200
	// checkPause is the pause between two fetches, to be gentle with the
	// logos' servers.
	checkPause = 200 * time.Millisecond
	// retryUnsettled is how long a logo whose fetch failed for want of a
	// network waits to be fetched again.
	retryUnsettled = time.Hour
)

// Checker checks whether logos load.
type Checker struct {
	store *store.Store
	cache *Cache
	log   *slog.Logger
	now   func() time.Time
	pause time.Duration
	wake  chan struct{}
	// later holds the logos whose fetches failed for want of a network,
	// and when they may be fetched again.
	later map[string]time.Time
}

// NewChecker returns a checker of logos that fetches into cache.
func NewChecker(st *store.Store, cache *Cache, log *slog.Logger) *Checker {
	return &Checker{store: st, cache: cache, log: log, now: time.Now, pause: checkPause, wake: make(chan struct{}, 1),
		later: map[string]time.Time{}}
}

// Poke says that logos to check may have come: a source or a guide was
// read, or a setting changed.
func (k *Checker) Poke() {
	select {
	case k.wake <- struct{}{}:
	default:
	}
}

// Run checks logos until ctx is done. It runs alone: rounds are never run
// at once.
func (k *Checker) Run(ctx context.Context) {
	tick := time.NewTicker(CheckEvery)
	defer tick.Stop()
	for {
		// A full round leaves more to check: go on at once.
		for ctx.Err() == nil && k.Round(ctx) == checksAtOnce {
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-k.wake:
		}
	}
}

// Round checks up to checksAtOnce logos that are due, and returns how
// many it tried.
func (k *Checker) Round(ctx context.Context) int {
	now := k.now()
	for address, at := range k.later {
		if !now.Before(at) {
			delete(k.later, address)
		}
	}
	found, err := k.store.LogosToCheck(ctx, now, RecheckAfter, ReloadAfter, checksAtOnce+len(k.later))
	if err != nil {
		if ctx.Err() == nil {
			k.log.Error("cannot check logos", "error", err)
		}
		return 0
	}
	var due []store.LogoFetch
	for _, f := range found {
		if _, wait := k.later[f.Address]; !wait && len(due) < checksAtOnce {
			due = append(due, f)
		}
	}
	loaded, failed := 0, 0
	for i, f := range due {
		if ctx.Err() != nil {
			return i
		}
		if i > 0 && k.pause > 0 {
			select {
			case <-ctx.Done():
				return i
			case <-time.After(k.pause):
			}
		}
		fctx, cancel := context.WithTimeout(ctx, 2*fetchTimeout)
		_, _, err := k.cache.GetWith(fctx, LogoKind, f.Address, func() ([]byte, error) {
			return k.cache.get(f.Address, f.UserAgent, f.PublicOnly)
		})
		cancel()
		if err != nil && !Settled(err) {
			k.later[f.Address] = k.now().Add(retryUnsettled)
			continue
		}
		if err := k.store.SetLogoCheck(ctx, f.Address, err == nil, k.now()); err != nil {
			if ctx.Err() == nil {
				k.log.Error("cannot record a logo's check", "error", err)
			}
			return i
		}
		if err == nil {
			loaded++
		} else {
			failed++
		}
	}
	if failed > 0 {
		k.log.Info("logos checked", "checked", len(due), "load", loaded, "do_not_load", failed)
	}
	// Checks of logos no longer in use are forgotten once they would be
	// due again.
	if err := k.store.ForgetLogoChecks(ctx, now.Add(-2*ReloadAfter)); err != nil && ctx.Err() == nil {
		k.log.Error("cannot forget old logos' checks", "error", err)
	}
	return len(due)
}
