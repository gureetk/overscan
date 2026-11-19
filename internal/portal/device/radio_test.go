// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// A radio station comes marked in the lineup, and radio_apart says, only
// when the lineup has one, whether the box shows radio stations on their
// screen alone; the setting changes the lineup's version (D87).
func TestRadioStations(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.catalog[1].Radio = true
	if _, err := h.store.ApplyCatalog(ctx, h.srcID, h.catalog, h.now); err != nil {
		t.Fatal(err)
	}
	h.approve(macA)
	token := h.session(macA).Token

	var list channelsResponse
	h.call("GET", Prefix+"channels", token, "", &list)
	var station int64
	for _, c := range list.Channels {
		if c.Radio != (c.Name == "Channel 1") {
			t.Errorf("%s: radio %v", c.Name, c.Radio)
		}
		if c.Radio {
			station = c.ID
		}
	}
	if list.RadioApart || station == 0 {
		t.Fatalf("lineup %+v", list)
	}
	before := list.Version

	if err := h.store.SetRadioApart(ctx, true); err != nil {
		t.Fatal(err)
	}
	list = channelsResponse{}
	h.call("GET", Prefix+"channels", token, "", &list)
	if !list.RadioApart || list.Version == before || len(list.Channels) != len(h.catalog) {
		t.Errorf("apart: %v, version %s then %s, %d channels", list.RadioApart, before, list.Version, len(list.Channels))
	}

	// A station plays as a channel does.
	var link playResponse
	if code := h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, station), token, "", &link); code != http.StatusOK || link.URL == "" {
		t.Errorf("play: %d %+v", code, link)
	}

	// Without a station, the lineup leaves the setting out.
	h.catalog[1].Radio = false
	if _, err := h.store.ApplyCatalog(ctx, h.srcID, h.catalog, h.now); err != nil {
		t.Fatal(err)
	}
	list = channelsResponse{}
	h.call("GET", Prefix+"channels", token, "", &list)
	if list.RadioApart {
		t.Error("radio_apart without a radio station")
	}
}
