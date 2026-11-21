// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"net/url"
	"testing"

	"overscan.gureet.ca/internal/store"
)

// A Stalker client whose account uses only some sources lists and plays
// only their channels (D114).
func TestAccountSources(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.addChannels()
	token := h.handshake(macA)
	ctx := context.Background()
	var all struct{ Data []itvChannel }
	h.js(h.call("type=itv&action=get_all_channels", macA, token), &all)
	if len(all.Data) != 22 {
		t.Fatalf("channels %d", len(all.Data))
	}
	sources, err := h.store.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var two int64
	for _, s := range sources {
		if s.Name == "two" {
			two = s.ID
		}
	}
	if err := h.store.SetAccountSources(ctx, store.HouseholdAccount, store.Sources{Only: true, IDs: []int64{two}}); err != nil {
		t.Fatal(err)
	}
	var some struct{ Data []itvChannel }
	h.js(h.call("type=itv&action=get_all_channels", macA, token), &some)
	if len(some.Data) != 2 || some.Data[0].Name != "aardvark news" {
		t.Errorf("channels %+v", some.Data)
	}
	var genres []genre
	h.js(h.call("type=itv&action=get_genres", macA, token), &genres)
	if len(genres) != 2 || genres[1].Title != "News" {
		t.Errorf("genres %+v", genres)
	}
	var got struct {
		Cmd   string `json:"cmd"`
		Error string `json:"error"`
	}
	h.js(h.call("type=itv&action=create_link&cmd="+url.QueryEscape(all.Data[0].Cmd), macA, token), &got)
	if got.Error != "nothing_to_play" || got.Cmd != "" {
		t.Errorf("a channel of one: %+v", got)
	}
	h.js(h.call("type=itv&action=create_link&cmd="+url.QueryEscape(some.Data[0].Cmd), macA, token), &got)
	if got.Error != "" || got.Cmd == "" {
		t.Errorf("a channel of two: %+v", got)
	}
}
