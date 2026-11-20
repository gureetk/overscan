// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// accountDevices adds an account with a limit of streams, approves boxes
// for it, and returns their device IDs.
func (h *harness) accountDevices(name string, limit int, macs ...string) (int64, []int64) {
	h.t.Helper()
	ctx := context.Background()
	acct, err := h.hub.store.AddAccount(ctx, name)
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.hub.store.SetAccountStreams(ctx, acct.ID, limit); err != nil {
		h.t.Fatal(err)
	}
	var ids []int64
	for _, mac := range macs {
		d, err := h.hub.store.ApproveDevice(ctx, mac, acct.ID, "", time.Now())
		if err != nil {
			h.t.Fatal(err)
		}
		ids = append(ids, d.ID)
	}
	return acct.ID, ids
}

// An account's limit holds across its devices: another of its devices is
// refused, a device's new stream replaces its own, and other accounts play
// on (D102).
func TestAccountLimit(t *testing.T) {
	h := newHarness(t, 0)
	ctx := context.Background()
	sam, devs := h.accountDevices("Sam", 1, "00:1A:79:00:00:01", "00:1A:79:00:00:02")
	household, err := h.hub.store.ApproveDevice(ctx, "00:1A:79:00:00:03", store.HouseholdAccount, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	src := h.entries["live/1"].SourceID

	first := h.play(h.link(devs[0], "live/1"))
	defer first.Body.Close()
	if err := h.hub.Room(ctx, src, devs[1]); !errors.Is(err, ErrAccountLimit) {
		t.Errorf("Room for Sam's other box: %v, want ErrAccountLimit", err)
	}
	if err := h.hub.Room(ctx, src, devs[0]); err != nil {
		t.Errorf("Room for the box that plays: %v, want nil, since its new stream replaces its own", err)
	}
	if err := h.hub.RoomForChannel(ctx, h.entries["live/2"], devs[1]); !errors.Is(err, ErrAccountLimit) {
		t.Errorf("RoomForChannel for Sam's other box: %v", err)
	}
	refused := get(t, h.link(devs[1], "live/2"))
	if text := body(t, refused); refused.StatusCode != http.StatusServiceUnavailable || !strings.Contains(text, "account") {
		t.Errorf("Sam's other box: %s %q", refused.Status, text)
	}
	if !strings.Contains(h.logs.String(), "stream refused: the account plays as many streams as it may") {
		t.Errorf("the log does not say why:\n%s", h.logs.String())
	}

	// The household has no limit.
	other := h.play(h.link(household.ID, "live/2"))
	defer other.Body.Close()

	streams := h.hub.Streams()
	if len(streams) != 2 {
		t.Fatalf("streams: %+v", streams)
	}
	for _, s := range streams {
		want := sam
		if s.DeviceID == household.ID {
			want = store.HouseholdAccount
		}
		if s.AccountID != want {
			t.Errorf("stream of device %d names account %d, want %d", s.DeviceID, s.AccountID, want)
		}
	}

	// Sam's box changes channel: its new stream replaces its old one.
	second := h.play(h.link(devs[0], "live/2"))
	defer second.Body.Close()
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, first.Body); done <- err }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("Sam's first stream did not end")
	}
	if !strings.Contains(h.logs.String(), "replaced by a new stream from the same device") {
		t.Errorf("the log does not say the first stream was replaced:\n%s", h.logs.String())
	}

	// With no limit, both of Sam's boxes play.
	if err := h.hub.store.SetAccountStreams(ctx, sam, 0); err != nil {
		t.Fatal(err)
	}
	third := h.play(h.link(devs[1], "live/1"))
	defer third.Body.Close()
}

// A paused film holds none of its account's streams, as it holds none of
// its source's connections; a stream that has stopped counts no more.
func TestAccountLimitCountsWhatPlays(t *testing.T) {
	h := newHarness(t, 0)
	_, devs := h.accountDevices("Alex", 1, "00:1A:79:00:00:04", "00:1A:79:00:00:05")
	first := h.play(h.link(devs[0], "live/1"))
	first.Body.Close()
	h.waitIdle()
	h.advance(reconnectGrace + sweepInterval)
	again := h.play(h.link(devs[1], "live/2"))
	again.Body.Close()
}

// A screen on a shared tuner counts against its account, though it takes no
// connection of its own (D93, D102).
func TestAccountLimitOnSharedTuner(t *testing.T) {
	h, u := sharedHarness(t, 0, 0)
	_, devs := h.accountDevices("Sam", 1, "00:1A:79:00:00:06", "00:1A:79:00:00:07")
	u.bursts <- tsBurst(1, 9)
	first := get(t, h.link(devs[0], "tv/1"))
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first: %s", first.Status)
	}
	defer first.Body.Close()
	readPackets(t, first.Body, 2)
	second := get(t, h.link(devs[1], "tv/1"))
	if text := body(t, second); second.StatusCode != http.StatusServiceUnavailable || !strings.Contains(text, "account") {
		t.Errorf("Sam's other box on the shared channel: %s %q", second.Status, text)
	}
	if err := h.hub.RoomForChannel(context.Background(), h.entries["tv/1"], devs[1]); !errors.Is(err, ErrAccountLimit) {
		t.Errorf("RoomForChannel: %v", err)
	}
}

// Players of the outputs count against their account's limit, and what
// they play stops when the administrator stops them: by account and
// output, leaving the others (D104).
func TestStopPlayers(t *testing.T) {
	h := newHarness(t, 0)
	sam, _ := h.accountDevices("Sam", 0)
	viaPlaylist := h.hub.Player(store.HouseholdAccount, "m3u", "192.0.2.10", "Kodi/21.1 (fictional)")
	viaLogin := h.hub.Player(store.HouseholdAccount, "xtream", "192.0.2.10", "Kodi/21.1 (fictional)")
	samPlayer := h.hub.Player(sam, "xtream", "192.0.2.11", "VLC/3.0 (fictional)")
	if viaPlaylist == viaLogin || !IsPlayer(viaPlaylist) || !IsPlayer(samPlayer) {
		t.Fatalf("players %d, %d, %d", viaPlaylist, viaLogin, samPlayer)
	}
	for _, p := range []int64{viaPlaylist, viaLogin, samPlayer} {
		resp := h.play(h.link(p, "live/1"))
		defer resp.Body.Close()
	}
	if n := h.hub.AccountStreams(store.HouseholdAccount); n != 2 {
		t.Errorf("the household's streams: %d, want 2", n)
	}
	if n := h.hub.AccountStreams(sam); n != 1 {
		t.Errorf("Sam's streams: %d, want 1", n)
	}
	n := h.hub.StopPlayers(func(p Player) bool { return p.AccountID == store.HouseholdAccount && p.Output == "xtream" },
		"the account's login changed")
	if n != 1 {
		t.Errorf("stopped %d streams, want 1", n)
	}
	if n := h.hub.AccountStreams(store.HouseholdAccount); n != 1 {
		t.Errorf("the household's streams after: %d, want 1", n)
	}
	if n := h.hub.AccountStreams(sam); n != 1 {
		t.Errorf("Sam's streams after: %d, want 1", n)
	}
	if !strings.Contains(h.logs.String(), `reason="the account's login changed"`) {
		t.Errorf("the log does not say why:\n%s", h.logs.String())
	}
}

// IsToken tells the hub's tokens from anything else, such as a username in
// an output's path that shares the hub's /live/ prefix.
func TestIsToken(t *testing.T) {
	h := newHarness(t, 0)
	link := h.hub.Link(1, h.entries["live/1"])
	token := strings.TrimSuffix(strings.TrimPrefix(link, "/live/"), ".ts")
	if !h.hub.IsToken(token) {
		t.Errorf("IsToken(%q) = false", token)
	}
	for _, s := range []string{"", "alex", token[:len(token)-1], strings.Repeat("A", len(token))} {
		if h.hub.IsToken(s) {
			t.Errorf("IsToken(%q) = true", s)
		}
	}
}

// A player whose account ends is stopped within a sweep (D105).
func TestPlayersOfAnEndedAccount(t *testing.T) {
	h := newHarness(t, 0)
	ctx := context.Background()
	sam, _ := h.accountDevices("Sam", 0)
	player := h.hub.Player(sam, "xtream", "192.0.2.11", "VLC/3.0 (fictional)")
	household := h.hub.Player(store.HouseholdAccount, "m3u", "192.0.2.10", "Kodi/21.1 (fictional)")
	for _, p := range []int64{player, household} {
		resp := h.play(h.link(p, "live/1"))
		defer resp.Body.Close()
	}
	h.hub.stopEndedPlayers(ctx)
	if n := len(h.hub.Streams()); n != 2 {
		t.Fatalf("streams before Sam's end: %d", n)
	}
	if err := h.hub.store.SetAccountExpiry(ctx, sam, h.hub.now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	h.hub.stopEndedPlayers(ctx)
	streams := h.hub.Streams()
	if len(streams) != 1 || streams[0].DeviceID != household {
		t.Errorf("streams after Sam's end: %+v", streams)
	}
	if !strings.Contains(h.logs.String(), `reason="the account has ended"`) {
		t.Errorf("the log does not say why:\n%s", h.logs.String())
	}
}
