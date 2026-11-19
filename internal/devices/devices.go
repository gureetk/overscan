// SPDX-License-Identifier: AGPL-3.0-or-later

// Package devices admits devices to Overscan's portals. A device that
// contacts Overscan for the first time is recorded as pending until the
// administrator approves it, and each contact is noted as the device's last.
package devices

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"overscan.gureet.ca/internal/store"
)

const (
	// MaxPending caps how many unknown devices are recorded as pending, so a
	// flood of made-up MAC addresses cannot fill the database.
	MaxPending = 100

	// seenInterval is how often a device's last contact is written at most.
	seenInterval = time.Minute
)

// Registry admits devices. The portals share one, so that the cap on pending
// devices is reported once.
type Registry struct {
	store *store.Store
	log   *slog.Logger

	// Now returns the current time. Tests replace it.
	Now func() time.Time

	mu         sync.Mutex
	lastWarned time.Time // when the pending cap was last reported
}

// New returns a registry that keeps devices in st.
func New(st *store.Store, log *slog.Logger) *Registry {
	return &Registry{store: st, log: log, Now: time.Now}
}

// Contact returns the device with the given MAC address, which reports model
// ("" if it reports none), and records the contact. An unknown device is
// recorded as pending, unless MaxPending devices are pending already, and is
// returned as pending either way.
func (g *Registry) Contact(ctx context.Context, mac, model string) (store.Device, error) {
	d, err := g.Known(ctx, mac, model)
	if errors.Is(err, store.ErrNotFound) {
		return g.notePending(ctx, mac, model)
	}
	return d, err
}

// Known returns the device with the given MAC address, which reports model
// ("" if it reports none), and records the contact, or store.ErrNotFound if
// the device is unknown. Unlike Contact, it records no new device.
func (g *Registry) Known(ctx context.Context, mac, model string) (store.Device, error) {
	d, err := g.store.Device(ctx, mac)
	if err != nil {
		return store.Device{}, err
	}
	return g.see(ctx, d, model), nil
}

// notePending records an unknown device as pending, unless too many are
// pending already.
func (g *Registry) notePending(ctx context.Context, mac, model string) (store.Device, error) {
	unrecorded := store.Device{MAC: mac, State: store.DevicePending, Model: model}
	n, err := g.store.CountDevices(ctx, store.DevicePending)
	if err != nil {
		return store.Device{}, err
	}
	if n >= MaxPending {
		g.mu.Lock()
		warn := g.Now().Sub(g.lastWarned) > time.Minute
		if warn {
			g.lastWarned = g.Now()
		}
		g.mu.Unlock()
		if warn {
			g.log.Warn("too many devices are waiting for approval; new ones are refused without being recorded",
				"pending", n, "hint", "approve, block or remove some with overscan devices")
		}
		return unrecorded, nil
	}
	d, err := g.store.SeeDevice(ctx, mac, model, g.Now())
	if err != nil {
		return store.Device{}, err
	}
	g.log.Info("new device waiting for approval", "mac", d.MAC, "model", d.Model,
		"hint", "overscan devices approve "+d.MAC)
	return d, nil
}

// see records a contact from d, at most once per seenInterval unless the
// device reports a different model, and returns the updated device.
func (g *Registry) see(ctx context.Context, d store.Device, model string) store.Device {
	now := g.Now()
	if now.Sub(d.LastSeen) < seenInterval && (model == "" || model == d.Model) {
		return d
	}
	updated, err := g.store.SeeDevice(ctx, d.MAC, model, now)
	if err != nil {
		g.log.Warn("cannot record a device's contact", "mac", d.MAC, "error", err)
		return d
	}
	return updated
}
