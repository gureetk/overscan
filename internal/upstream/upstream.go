// SPDX-License-Identifier: AGPL-3.0-or-later

// Package upstream gives the stream hub the addresses of the streams of
// sources that give a link for each stream as it starts: Stalker portals,
// which Overscan reads as the box that the provider registered (D76). Each
// portal has one session, which the catalog's and the guide's reads share
// through a stalker.Pool, and which the streams that play keep going with
// the portal's keep-alive.
package upstream

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Links is the hub's Resolver for Stalker portals.
type Links struct {
	store *store.Store
	pool  *stalker.Pool
	log   *slog.Logger
}

// New returns links that read the sources' settings from st and talk to
// their portals in the sessions of pool.
func New(st *store.Store, pool *stalker.Pool, log *slog.Logger) *Links {
	return &Links{store: st, pool: pool, log: log}
}

// Resolve asks a source's portal for the address that plays ref: for a
// channel, its cmd, with itv/create_link; for a film or an episode, what
// its page said plays it (stalker.Play, D77), with vod/create_link, after
// the film's first file's cmd for a film whose file is asked for apart.
// While the stream plays, its Hold keeps the box's session going with the
// portal's keep-alive (section 7 of the protocol notes). A portal that
// gives no link because the box plays as many streams as it may gives an
// error that wraps hub.ErrLimit.
func (l *Links) Resolve(ctx context.Context, sourceID int64, kind, ref string) (hub.Resolved, error) {
	settings, err := l.store.SourceSettings(ctx, sourceID)
	if err != nil {
		return hub.Resolved{}, err
	}
	if settings.Type != source.TypeStalker {
		return hub.Resolved{}, errors.New("the source is not a Stalker portal")
	}
	client, err := l.pool.Client(sourceID, stalker.FromSettings(settings))
	if err != nil {
		return hub.Resolved{}, err
	}
	var address string
	switch kind {
	case hub.PlaysChannel:
		address, err = client.ChannelLink(ctx, ref)
	case hub.PlaysMovie, hub.PlaysEpisode:
		p, ok := stalker.ParsePlay(ref)
		if !ok {
			return hub.Resolved{}, errors.New("the portal gave nothing to play it with")
		}
		cmd := p.Cmd
		if p.Film != "" {
			cmd, err = client.MovieFile(ctx, p.Film)
		}
		if err == nil {
			address, err = client.VODLink(ctx, cmd, p.Episode)
		}
	default:
		return hub.Resolved{}, fmt.Errorf("a Stalker portal cannot play a %s", kind)
	}
	var lerr *stalker.LinkError
	if errors.As(err, &lerr) && lerr.Limit() {
		err = fmt.Errorf("%w: %w", hub.ErrLimit, err)
	}
	if err != nil {
		return hub.Resolved{}, err
	}
	name := settings.Name
	return hub.Resolved{Address: address, Hold: func() func() {
		return client.Hold(func(err error) {
			l.log.Warn("a Stalker portal's keep-alive failed; the stream may stop", "source", name, "error", err)
		})
	}}, nil
}
