// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"

	"overscan.gureet.ca/internal/source"
)

// watchdogWait is how long a keep-alive may take.
const watchdogWait = 30 * time.Second

// Pool gives each source one Client, so that whatever reads a portal, and
// every stream that plays from it, shares the box's session: a handshake
// replaces the box's token (section 3), so two clients of one box would
// keep signing each other out.
type Pool struct {
	http *http.Client

	mu      sync.Mutex
	clients map[int64]pooled // by source ID
}

type pooled struct {
	config Config // as given, before New fills in defaults
	client *Client
}

// NewPool returns a pool whose clients make their requests with client,
// or if it is nil, with one that gives up on a portal that does not answer
// within half a minute but has no limit on how long an answer may take to
// read, since a large guide takes a while: callers bound their calls with
// their contexts.
func NewPool(client *http.Client) *Pool {
	if client == nil {
		client = &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
			MaxIdleConnsPerHost:   2,
			IdleConnTimeout:       90 * time.Second,
		}}
	}
	return &Pool{http: client, clients: map[int64]pooled{}}
}

// NewClient returns a client outside the pool, which makes its requests as
// the pool's clients do, as for a test of a source's settings.
func (p *Pool) NewClient(config Config) (*Client, error) { return New(config, p.http) }

// Client returns the source's client: the one it has, or a new one if it
// has none or its config has changed since. A client that a config change
// replaces goes on for the streams that hold it.
func (p *Pool) Client(id int64, config Config) (*Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[id]; ok && c.config == config {
		return c.client, nil
	}
	c, err := New(config, p.http)
	if err != nil {
		return nil, err
	}
	p.clients[id] = pooled{config: config, client: c}
	return c, nil
}

// Forget drops the clients of the sources that keep says no to, such as
// those that have been deleted.
func (p *Pool) Forget(keep func(id int64) bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.clients {
		if !keep(id) {
			delete(p.clients, id)
		}
	}
}

// FromSettings returns the config of a source of type source.TypeStalker.
// The time zone is the server's, by the IANA name that the TZ variable
// gives it; New makes it UTC where the server's zone has no such name.
func FromSettings(s source.Settings) Config {
	return Config{
		Portal:    s.URL,
		MAC:       s.Device.MAC,
		Serial:    s.Device.Serial,
		DeviceID:  s.Device.DeviceID,
		DeviceID2: s.Device.DeviceID2,
		Signature: s.Device.Signature,
		Model:     s.Device.Model,
		Login:     s.Username,
		Password:  s.Password,
		UserAgent: s.UserAgent,
		TimeZone:  localZone(),
	}
}

// localZone returns the IANA name of the server's time zone, "" if it has
// none, as when it comes from /etc/localtime.
func localZone() string {
	name := time.Local.String()
	if name == "Local" || name == "" {
		return ""
	}
	if _, err := time.LoadLocation(name); err != nil {
		return ""
	}
	return name
}

// Hold keeps the box's session alive while a stream plays, as a box does
// (section 7): until every release that it returns has been called, the
// client calls the watchdog every WatchdogEvery. failed, if not nil, hears
// of each keep-alive that fails; the first hold's is the one used until
// the client is let go.
func (c *Client) Hold(failed func(error)) (release func()) {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	c.holds++
	if c.holds == 1 {
		ctx, cancel := context.WithCancel(context.Background())
		c.stopHold = cancel
		go c.watch(ctx, failed)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.holdMu.Lock()
			defer c.holdMu.Unlock()
			c.holds--
			if c.holds == 0 {
				c.stopHold()
				c.stopHold = nil
			}
		})
	}
}

// Held reports whether a stream holds the client.
func (c *Client) Held() bool {
	c.holdMu.Lock()
	defer c.holdMu.Unlock()
	return c.holds > 0
}

// watch calls the watchdog on its schedule until ctx is done.
func (c *Client) watch(ctx context.Context, failed func(error)) {
	for {
		every := c.watchEvery
		if every == 0 {
			every = c.WatchdogEvery()
		}
		t := time.NewTimer(every)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		wctx, cancel := context.WithTimeout(ctx, watchdogWait)
		_, err := c.Watchdog(wctx)
		cancel()
		if err != nil && ctx.Err() == nil && failed != nil {
			failed(err)
		}
	}
}
