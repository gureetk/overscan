// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/admin"
	"overscan.gureet.ca/internal/artwork"
	"overscan.gureet.ca/internal/store"
)

func TestFavouritesAndRecent(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	ids := map[string]int64{}
	var got channelsResponse
	if code := h.call("GET", Prefix+"channels", token, "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, c := range got.Channels {
		ids[c.Name] = c.ID
	}
	if got.Version == "" || got.Favourites == nil || len(got.Favourites) != 0 || got.Recent == nil || len(got.Recent) != 0 {
		t.Errorf("a new box's lists: %+v", got)
	}
	var version struct{ Version string }
	if h.call("GET", Prefix+"channels/version", token, "", &version); version.Version != got.Version {
		t.Errorf("version %q, channels say %q", version.Version, got.Version)
	}

	var fav struct {
		ID        int64
		Favourite bool
	}
	path := fmt.Sprintf("%schannels/%d/favourite", Prefix, ids["Channel 1"])
	if code := h.call("PUT", path, token, "", &fav); code != http.StatusOK || fav.ID != ids["Channel 1"] || !fav.Favourite {
		t.Errorf("mark: %d %+v", code, fav)
	}
	if code, e := h.errorCode("PUT", Prefix+"channels/999999/favourite", token); code != http.StatusNotFound || e != "not_found" {
		t.Errorf("an unknown channel: %d %s", code, e)
	}
	if code := h.call("DELETE", Prefix+"channels/999999/favourite", token, "", &fav); code != http.StatusOK || fav.Favourite {
		t.Errorf("unmark an unknown channel: %d %+v", code, fav)
	}
	var link playResponse
	if code := h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, ids["Channel 0"]), token, "", &link); code != http.StatusOK {
		t.Fatalf("play: %d", code)
	}
	h.call("GET", Prefix+"channels", token, "", &got)
	if !reflect.DeepEqual(got.Favourites, []int64{ids["Channel 1"]}) || !reflect.DeepEqual(got.Recent, []int64{ids["Channel 0"]}) {
		t.Errorf("favourites %v, recent %v", got.Favourites, got.Recent)
	}

	// Another box of the account shares the favourites, not what was watched.
	h.approve(macB)
	other := h.session(macB).Token
	var theirs channelsResponse
	h.call("GET", Prefix+"channels", other, "", &theirs)
	if !reflect.DeepEqual(theirs.Favourites, []int64{ids["Channel 1"]}) || len(theirs.Recent) != 0 {
		t.Errorf("the other box: favourites %v, recent %v", theirs.Favourites, theirs.Recent)
	}

	// A renamed channel changes the version.
	before := got.Version
	if err := h.store.RenameEntry(context.Background(), store.DefaultLineupID, ids["Channel 2"], "Renamed"); err != nil {
		t.Fatal(err)
	}
	if h.call("GET", Prefix+"channels/version", token, "", &version); version.Version == before {
		t.Error("the version did not change with the lineup")
	}
	if code := h.call("DELETE", path, token, "", &fav); code != http.StatusOK || fav.Favourite {
		t.Errorf("unmark: %d %+v", code, fav)
	}
	if h.call("GET", Prefix+"channels", token, "", &got); len(got.Favourites) != 0 {
		t.Errorf("after unmarking: %v", got.Favourites)
	}
}

func TestGuideForSomeChannels(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.session(macA).Token
	ch1 := fmt.Sprint(entries["Channel 1"])

	var got guideResponse
	if code := h.call("GET", Prefix+"guide?ids="+ch1+",999999", token, "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(got.Channels) != 1 || titles(got.Channels[ch1]) != "Match" {
		t.Errorf("guide %+v", got.Channels)
	}
	many := strings.TrimSuffix(strings.Repeat("1,", maxGuideIDs+1), ",")
	for _, q := range []string{"?ids=x", "?ids=1,,2", "?ids=-1", "?ids=" + many} {
		if code, e := h.errorCode("GET", Prefix+"guide"+q, token); code != http.StatusBadRequest || e != "bad_request" {
			t.Errorf("%.30s: %d %s", q, code, e)
		}
	}
}

func TestSettings(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	h.approve(macB)
	token, other := h.session(macA).Token, h.session(macB).Token

	var got settings
	if code := h.call("GET", Prefix+"settings", token, "", &got); code != http.StatusOK || got != defaultSettings {
		t.Errorf("defaults: %d %+v", code, got)
	}
	want := settings{Home: "picture", Picture: "dusk", Accent: "blue", Clock: "24", Numbers: "groups", Groups: "ok"}
	if code := h.call("PATCH", Prefix+"settings", token, `{"home":"picture","accent":"blue","numbers":"groups","groups":"ok"}`, &got); code != http.StatusOK || got != want {
		t.Errorf("patch: %d %+v", code, got)
	}
	if h.call("GET", Prefix+"settings", token, "", &got); got != want {
		t.Errorf("after the patch: %+v", got)
	}
	for _, body := range []string{`{"home":"tv"}`, `{"accent":"red"}`, `{"clock":"25"}`, `{"picture":"../x"}`, `{"numbers":"letters"}`, `{"groups":"never"}`, `{"volume":1}`, `[]`, `x`} {
		var e struct{ Error string }
		if code := h.call("PATCH", Prefix+"settings", token, body, &e); code != http.StatusBadRequest || e.Error != "bad_request" {
			t.Errorf("%s: %d %s", body, code, e.Error)
		}
	}
	if h.call("GET", Prefix+"settings", token, "", &got); got != want {
		t.Errorf("after refused patches: %+v", got)
	}
	if h.call("GET", Prefix+"settings", other, "", &got); got != defaultSettings {
		t.Errorf("another box: %+v", got)
	}

	// Settings saved before a setting existed read back with its default.
	d, err := h.store.Device(context.Background(), macB)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetDeviceSettings(context.Background(), d.ID, `{"home":"picture","picture":"dusk","accent":"blue","clock":"12"}`); err != nil {
		t.Fatal(err)
	}
	old := settings{Home: "picture", Picture: "dusk", Accent: "blue", Clock: "12", Numbers: "lineup", Groups: "preview"}
	if h.call("GET", Prefix+"settings", other, "", &got); got != old {
		t.Errorf("older settings: %+v", got)
	}
}

func TestSearch(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	entries := h.addGuide()
	token := h.session(macA).Token

	var got searchResponse
	if code := h.call("GET", Prefix+"search?q=MATCH", token, "", &got); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	if len(got.Channels) != 0 || len(got.Programmes) != 1 || got.Programmes[0].Title != "Match" ||
		got.Programmes[0].Channel != entries["Channel 1"] || got.Programmes[0].Start != h.now.Unix()-60 {
		t.Errorf("match: %+v", got)
	}
	h.call("GET", Prefix+"search?q=channel", token, "", &got)
	want := []int64{entries["Channel 0"], entries["Channel 1"], entries["Channel 2"], entries["HLS channel"]}
	if !reflect.DeepEqual(got.Channels, want) {
		t.Errorf("channels %v, want %v", got.Channels, want)
	}
	// P0 and P1 ended before now; P2, starting now, is the first one on.
	if h.call("GET", Prefix+"search?q=p", token, "", &got); len(got.Programmes) == 0 || got.Programmes[0].Title != "P2" {
		t.Errorf("programmes %+v", got.Programmes)
	}
	for _, q := range []string{"", "%20", strings.Repeat("a", maxQueryLen+1)} {
		if code, e := h.errorCode("GET", Prefix+"search?q="+q, token); code != http.StatusBadRequest || e != "bad_request" {
			t.Errorf("%q: %d %s", q, code, e)
		}
	}
}

func TestLogos(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	var before channelsResponse
	h.call("GET", Prefix+"channels", token, "", &before)

	const logo = "http://img.example/logo-0.png"
	catalog := append([]store.CatalogChannel(nil), h.catalog...)
	catalog[0].Logo = logo
	if _, err := h.store.ApplyCatalog(context.Background(), h.srcID, catalog, h.now); err != nil {
		t.Fatal(err)
	}
	var got channelsResponse
	h.call("GET", Prefix+"channels", token, "", &got)
	for _, c := range got.Channels {
		want := ""
		if c.Name == "Channel 0" {
			want = artwork.LogoPath(c.ID, logo)
		}
		if c.Logo != want {
			t.Errorf("%s: logo %q, want %q", c.Name, c.Logo, want)
		}
	}
	if got.Version == before.Version {
		t.Error("a new logo did not change the version")
	}
}

func TestParentalPIN(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	ctx := context.Background()
	var got channelsResponse
	h.call("GET", Prefix+"channels", token, "", &got)
	ids := map[string]int64{}
	for _, c := range got.Channels {
		ids[c.Name] = c.ID
	}
	if err := h.store.SetEntriesLocked(ctx, store.DefaultLineupID, []int64{ids["Channel 0"]}, true); err != nil {
		t.Fatal(err)
	}
	play := func(body string) (int, string) {
		t.Helper()
		var e struct{ Error string }
		code := h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, ids["Channel 0"]), token, body, &e)
		return code, e.Error
	}

	// Without a PIN, a lock asks for nothing.
	if h.call("GET", Prefix+"channels", token, "", &got); got.Channels[0].Locked {
		t.Error("locked without a PIN")
	}
	if code, e := play(""); code != http.StatusOK {
		t.Errorf("without a PIN: %d %s", code, e)
	}

	hash, err := admin.HashPassword(ctx, "2468", admin.Params{Memory: 64, Time: 1, Threads: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetAccountPIN(ctx, 1, hash); err != nil {
		t.Fatal(err)
	}
	h.call("GET", Prefix+"channels", token, "", &got)
	for _, c := range got.Channels {
		if c.Locked != (c.ID == ids["Channel 0"]) {
			t.Errorf("%s: locked %v", c.Name, c.Locked)
		}
	}
	for body, want := range map[string]string{"": "pin_required", `{"pin":""}`: "pin_required", `{"pin":"1357"}`: "pin_wrong"} {
		if code, e := play(body); code != http.StatusForbidden || e != want {
			t.Errorf("%s: %d %s, want %s", body, code, e, want)
		}
	}
	if code, _ := play(`{"pin":"2468"}`); code != http.StatusOK {
		t.Errorf("the right PIN: %d", code)
	}
	// Another channel needs no PIN.
	var link playResponse
	if code := h.call("POST", fmt.Sprintf("%schannels/%d/play", Prefix, ids["Channel 1"]), token, "", &link); code != http.StatusOK {
		t.Errorf("an unlocked channel: %d", code)
	}

	// Five wrong PINs, then a wait of five minutes, even for the right one.
	for range pinTries {
		play(`{"pin":"0000"}`)
	}
	if code, e := play(`{"pin":"2468"}`); code != http.StatusTooManyRequests || e != "pin_locked" {
		t.Errorf("after five wrong PINs: %d %s", code, e)
	}
	h.now = h.now.Add(pinLockout)
	h.api.now = func() time.Time { return h.now }
	if code, _ := play(`{"pin":"2468"}`); code != http.StatusOK {
		t.Errorf("after the wait: %d", code)
	}
	if !strings.Contains(h.logs.String(), "wrong parental PIN") || strings.Contains(h.logs.String(), "2468") {
		t.Errorf("logs: %s", h.logs.String())
	}
}
