// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"overscan.gureet.ca/internal/store"
)

// The outputs serve players that are not devices of their own: a player
// signs in as an account, with its playlist token or its login (D103,
// D104). The hub knows each one, in memory, by its account, the output it
// came through, its address and its user agent, and gives it a device ID of
// PlayerBase or more, so that its streams count against its account's
// limit as a device's do (D102), and a player that changes channel replaces
// its own stream at a full source or account, as a box does (D36). Two
// players on one address with the same user agent count as one, as they do
// behind a proxy.

// PlayerBase is the lowest device ID of a player that an output serves.
// The store's devices never reach it.
const PlayerBase int64 = 1 << 40

// Player is a player that an output serves, as the hub knows it.
type Player struct {
	ID        int64
	AccountID int64
	Output    string // the output it came through, such as "m3u" or "xtream"
	Address   string // its IP address
	Agent     string // its user agent, cut short; "" if it sent none
	Seen      time.Time
}

// How long the hub remembers a player that asks for nothing, and how many
// players it remembers at most.
const (
	playerForgetAfter = 24 * time.Hour
	maxPlayers        = 1024
	maxAgentChars     = 80
)

// IsPlayer reports whether a device ID is a player's.
func IsPlayer(deviceID int64) bool { return deviceID >= PlayerBase }

// playerKey is how the hub finds a player again.
type playerKey struct {
	account int64
	output  string
	address string
	agent   string
}

// Player returns the device ID of the player of an account that came
// through an output, at an address with a user agent, remembering a new
// one.
func (h *Hub) Player(accountID int64, output, address, agent string) int64 {
	agent = cleanAgent(agent)
	k := playerKey{accountID, output, address, agent}
	now := h.now()
	h.playersMu.Lock()
	defer h.playersMu.Unlock()
	if p, ok := h.players[k]; ok {
		p.Seen = now
		return p.ID
	}
	if len(h.players) >= maxPlayers {
		// Forget those not seen for a day, or else the one seen longest
		// ago.
		var oldest *Player
		for key, p := range h.players {
			if now.Sub(p.Seen) > playerForgetAfter {
				delete(h.players, key)
				delete(h.playerByID, p.ID)
				continue
			}
			if oldest == nil || p.Seen.Before(oldest.Seen) {
				oldest = p
			}
		}
		if len(h.players) >= maxPlayers && oldest != nil {
			delete(h.players, playerKey{oldest.AccountID, oldest.Output, oldest.Address, oldest.Agent})
			delete(h.playerByID, oldest.ID)
		}
	}
	h.nextPlayer++
	p := &Player{ID: PlayerBase + h.nextPlayer, AccountID: accountID, Output: output, Address: address, Agent: agent,
		Seen: now}
	h.players[k] = p
	h.playerByID[p.ID] = p
	return p.ID
}

// PlayerOf returns the player with a device ID, if the hub knows it.
func (h *Hub) PlayerOf(id int64) (Player, bool) {
	h.playersMu.Lock()
	defer h.playersMu.Unlock()
	p, ok := h.playerByID[id]
	if !ok {
		return Player{}, false
	}
	return *p, true
}

// cleanAgent cuts a user agent short and drops what is not printable.
func cleanAgent(agent string) string {
	agent = strings.TrimSpace(strings.Map(func(r rune) rune {
		if !unicode.IsPrint(r) {
			return -1
		}
		return r
	}, agent))
	if r := []rune(agent); len(r) > maxAgentChars {
		agent = string(r[:maxAgentChars])
	}
	return agent
}

// Describe names a player for the admin: its user agent's first word, or
// "A player", and its address.
func (p Player) Describe() string {
	name := "A player"
	if f := strings.Fields(p.Agent); len(f) > 0 {
		name = f[0]
	}
	return name + " at " + p.Address
}

// StopPlayers ends the streams of the players that match, and returns how
// many it ended: when an output is turned off, or a token or a login that
// opened them no longer opens anything, what they play stops too.
func (h *Hub) StopPlayers(match func(Player) bool, reason string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	var stop []*session
	for _, s := range h.sessions {
		if s.shared || !IsPlayer(s.deviceID) {
			continue
		}
		p, ok := h.PlayerOf(s.deviceID)
		if !ok {
			// One that the hub no longer remembers is matched by its
			// account alone.
			p = Player{ID: s.deviceID, AccountID: s.account}
		}
		if match(p) {
			stop = append(stop, s)
		}
	}
	for _, s := range stop {
		h.end(s, reason)
	}
	return len(stop)
}

// stopEndedPlayers ends the streams of the players whose account has ended
// or is gone (D95, D105): a box stops by itself when its account ends, and
// a player is stopped.
func (h *Hub) stopEndedPlayers(ctx context.Context) {
	accounts := map[int64]bool{}
	h.mu.Lock()
	for _, s := range h.sessions {
		if IsPlayer(s.deviceID) && !s.shared && s.account != 0 {
			accounts[s.account] = true
		}
	}
	h.mu.Unlock()
	now := h.now()
	for id := range accounts {
		acct, err := h.store.Account(ctx, id)
		reason := "the account has ended"
		switch {
		case errors.Is(err, store.ErrNotFound):
			reason = "the account was removed"
		case err != nil:
			h.log.Warn("cannot look up an account whose players play", "account", id, "error", err)
			continue
		case !acct.Ended(now):
			continue
		}
		h.StopPlayers(func(p Player) bool { return p.AccountID == id }, reason)
	}
}

// IsToken reports whether s is the token of a link that the hub made: an
// output whose paths share the hub's /live/ prefix hands such a path to
// the hub.
func (h *Hub) IsToken(s string) bool {
	_, ok := h.verify(s)
	return ok
}

// ServeLink serves a link that the hub made for a player, as if the
// player had asked for it, without the request's query: an output serves
// a channel at an address of its own that never changes, and does not
// send the player elsewhere, which some players do not follow (D103).
func (h *Hub) ServeLink(w http.ResponseWriter, r *http.Request, link string) {
	r2 := r.Clone(r.Context())
	u := *r.URL
	u.Path, u.RawPath, u.RawQuery = link, "", ""
	r2.URL = &u
	r2.RequestURI = link
	h.ServeHTTP(w, r2)
}
