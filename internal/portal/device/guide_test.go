// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// addGuide gives Channel 0 an hour-long programme every hour from two hours
// before h.now, P0 to P29, and Channel 1 one programme around h.now. It
// returns the channels' IDs by name.
func (h *harness) addGuide() map[string]int64 {
	h.t.Helper()
	ctx := context.Background()
	gcs, err := h.store.GuideChannels(ctx, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	byName := map[string]int64{}
	for _, c := range gcs {
		byName[c.Name] = c.ID
	}
	w, err := h.store.BeginGuide(ctx, 1)
	if err != nil {
		h.t.Fatal(err)
	}
	for i := range 30 {
		at := h.now.Add(time.Duration(i-2) * time.Hour)
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: byName["Channel 0"], Start: at, Stop: at.Add(time.Hour),
			Title: fmt.Sprintf("P%d", i), SubTitle: "Part", Description: "About it", Category: "Talk"}); err != nil {
			h.t.Fatal(err)
		}
	}
	if err := w.Add(ctx, store.GuideProgramme{ChannelID: byName["Channel 1"], Start: h.now.Add(-time.Minute),
		Stop: h.now.Add(time.Minute), Title: "Match"}); err != nil {
		h.t.Fatal(err)
	}
	if err := w.Commit(ctx, h.now, "hash"); err != nil {
		h.t.Fatal(err)
	}
	cs, err := h.store.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		h.t.Fatal(err)
	}
	entries := map[string]int64{}
	for _, c := range cs {
		entries[c.Name] = c.EntryID
	}
	return entries
}

func titles(ps []programme) string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Title)
	}
	return strings.Join(out, " ")
}

func TestGuide(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.session(macA).Token

	var got guideResponse
	if code := h.call("GET", Prefix+"guide", token, "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	ch0, ch1 := fmt.Sprint(entries["Channel 0"]), fmt.Sprint(entries["Channel 1"])
	if got.From != h.now.Unix() || got.To != h.now.Add(3*time.Hour).Unix() || len(got.Channels) != 2 ||
		titles(got.Channels[ch0]) != "P2 P3 P4" || titles(got.Channels[ch1]) != "Match" {
		t.Fatalf("guide %+v", got)
	}
	if p := got.Channels[ch0][0]; p.Start != h.now.Unix() || p.Stop != h.now.Add(time.Hour).Unix() || p.Description != "" || p.ID == 0 {
		t.Errorf("programme %+v", p)
	}
	from := h.now.Add(5 * time.Hour).Unix()
	var later guideResponse
	h.call("GET", fmt.Sprintf("%sguide?from=%d&to=%d", Prefix, from, from+3600), token, "", &later)
	if len(later.Channels) != 1 || titles(later.Channels[ch0]) != "P7" {
		t.Errorf("a later window: %+v", later.Channels)
	}
	for _, q := range []string{"?from=x", fmt.Sprintf("?from=%d&to=%d", from, from), fmt.Sprintf("?to=%d", h.now.Add(7*time.Hour).Unix())} {
		if code, e := h.errorCode("GET", Prefix+"guide"+q, token); code != http.StatusBadRequest || e != "bad_request" {
			t.Errorf("%s: %d %s", q, code, e)
		}
	}
}

func TestChannelGuide(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.session(macA).Token
	path := fmt.Sprintf("%schannels/%d/guide", Prefix, entries["Channel 0"])

	var got channelGuideResponse
	if code := h.call("GET", path, token, "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(got.Programmes) != 24 || got.Programmes[0].Title != "P2" || got.Programmes[0].Description != "About it" ||
		got.Programmes[0].SubTitle != "Part" || got.Programmes[0].Category != "Talk" {
		t.Errorf("programmes %d, first %+v", len(got.Programmes), got.Programmes[0])
	}
	var raw map[string]json.RawMessage
	h.call("GET", fmt.Sprintf("%schannels/%d/guide", Prefix, entries["Ungrouped"]), token, "", &raw)
	if string(raw["programmes"]) != "[]" {
		t.Errorf("a channel without a guide: %s", raw["programmes"])
	}
	for path, want := range map[string]int{
		Prefix + "channels/999999/guide": http.StatusNotFound,
		Prefix + "channels/x/guide":      http.StatusNotFound,
		path + fmt.Sprintf("?from=%d&to=%d", h.now.Unix(), h.now.Add(49*time.Hour).Unix()): http.StatusBadRequest,
	} {
		if code, _ := h.errorCode("GET", path, token); code != want {
			t.Errorf("%s: %d, want %d", path, code, want)
		}
	}
	if code, _ := h.errorCode("GET", path, ""); code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", code)
	}
}
