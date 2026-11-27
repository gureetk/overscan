// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
)

// fakeStreams stands in for the hub.
type fakeStreams []hub.Stream

func (f fakeStreams) Streams() []hub.Stream                         { return f }
func (f fakeStreams) CanRepair() bool                               { return true }
func (f fakeStreams) CanConvert() bool                              { return true }
func (f fakeStreams) Encoder() hub.Encoder                          { return hub.Encoder{H264: true, ToneMap: true} }
func (f fakeStreams) StopPlayers(func(hub.Player) bool, string) int { return 0 }
func (f fakeStreams) StopSources(int64, store.Sources, string) int  { return 0 }
func (f fakeStreams) StopCut(int64, string) int                     { return 0 }
func (f fakeStreams) StopStream(id, _ string) bool {
	for _, s := range f {
		if s.ID == id {
			return true
		}
	}
	return false
}

// withActivity gives the test admin the lineup of withLineup, a device that
// plays a channel, one that waits, and a captured request.
func (ta *testAdmin) withActivity() (*http.Cookie, string) {
	ta.t.Helper()
	ctx := context.Background()
	c, h := ta.withLineup()
	d, err := ta.st.ApproveDevice(ctx, "00:1A:79:00:00:01", 1, "Living room", ta.clock)
	if err != nil {
		ta.t.Fatal(err)
	}
	if _, err := ta.st.SeeDevice(ctx, "00:1A:79:00:00:02", "MAG322", ta.clock); err != nil {
		ta.t.Fatal(err)
	}
	if _, err := ta.st.CaptureRequest(ctx, store.Capture{Module: "itv", Action: "set_fav", Params: "fav_ch,type",
		Example: "type=itv&action=set_fav&fav_ch=1&token=[redacted]", UserAgent: "MAG322", DeviceID: d.ID}, ta.clock, 100); err != nil {
		ta.t.Fatal(err)
	}
	src := ta.sourceID("main")
	ta.UseStreams(fakeStreams{
		{Kind: hub.PlaysChannel, DeviceID: d.ID, EntryID: 1, SourceID: src, Source: "main", Channel: "Alpha",
			Started: ta.clock.Add(-3 * time.Minute), Bytes: 3 << 20, Playing: true, Repaired: true},
		{Kind: hub.PlaysChannel, DeviceID: 999, EntryID: 2, SourceID: src, Source: "main", Channel: "Bravo",
			Started: ta.clock.Add(-time.Minute), HLS: true},
	})
	return c, h[1]
}

func TestOverview(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf := ta.withActivity()
	ctx := context.Background()
	if err := ta.st.SetRefreshResult(ctx, ta.sourceID("main"), ta.clock.Add(-2*time.Hour), "the server answered 403 Forbidden"); err != nil {
		t.Fatal(err)
	}
	ta.started = ta.clock.Add(-26 * time.Hour)
	body := ta.do("GET", "/admin/", nil, c).Body.String()
	mustContain(t, "the overview", body,
		"running for 26 hours", `hx-trigger="every 15s"`,
		// What needs doing: the waiting device, the source's failed read.
		`To do <span class="count quiet">2</span>`, "A device is waiting for approval", `<span class="mac">00:1A:79:00:00:02</span>`,
		`action="/admin/devices/00:1A:79:00:00:02/approve"`, `action="/admin/devices/00:1A:79:00:00:02/block"`, `name="back" value="overview"`,
		"The last read of main failed", "the server answered 403 Forbidden. Devices still see the channels from the read before.",
		`href="/admin/sources#source-1"`,
		// The tiles.
		`<span class="tile-value">2</span>`, "1 repaired, 1 from HLS", "of 4, in 2 groups", "1 waiting, 0 blocked", "0 off, 1 with trouble",
		// What is playing.
		`<span class="num-chip">1</span><div><span class="strong">Alpha</span>`, "Living room", "3 minutes ago", "3.0 MB",
		`<span class="strong">Bravo</span><div class="note warn">Waiting for the player to come back</div>`, "a removed device",
		`title="The source sends HLS">HLS</span>`, `title="It goes through ffmpeg, which rebuilds its timestamps">Repaired</span>`,
		"1 kind of request that Overscan does not support yet")

	// Approving the device from the overview answers with the overview.
	rec := ta.do("POST", "/admin/devices/00:1A:79:00:00:02/approve", url.Values{"csrf": {csrf}, "back": {"overview"}}, c, "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve: status %d", rec.Code)
	}
	mustContain(t, "the overview after approving", rec.Body.String(), `<div id="overview"`, "Approved 00:1A:79:00:00:02.",
		`To do <span class="count quiet">1</span>`, "0 waiting, 0 blocked")
	if strings.Contains(rec.Body.String(), "is waiting for approval") {
		t.Error("the approved device still waits")
	}
	// Without htmx, the browser goes back to the overview.
	rec = ta.do("POST", "/admin/devices/00:1A:79:00:00:02/block", url.Values{"csrf": {csrf}, "back": {"overview"}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("block without htmx: status %d, to %q", rec.Code, rec.Header().Get("Location"))
	}

	// Locks without a PIN, and repair everywhere without ffmpeg.
	ta.UseStreams(nil)
	if err := ta.st.SetRepairDefault(ctx, true); err != nil {
		t.Fatal(err)
	}
	lineup, _ := ta.lineupID(ctx)
	if err := ta.st.SetLineupGroupLocked(ctx, lineup, ta.groups(c)["News"].ID, true); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "the overview", ta.do("GET", "/admin/", nil, c).Body.String(),
		"The lineup's lock does nothing yet", `<a class="button" href="/admin/settings">Set a PIN</a>`,
		"Every channel is to be repaired, but there is no ffmpeg", "Nothing is playing.", "Nothing at the moment")
}

func TestOverviewShowsFilms(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withActivity()
	src := ta.sourceID("main")
	// Item IDs are not lineup entries, even where the numbers are the same.
	ta.UseStreams(fakeStreams{
		{Kind: hub.PlaysMovie, DeviceID: 999, ItemID: 1, SourceID: src, Source: "main", Channel: "The Long Field",
			Started: ta.clock.Add(-time.Hour), Playing: true, Converted: true},
		{Kind: hub.PlaysEpisode, DeviceID: 999, ItemID: 2, SourceID: src, Source: "main", Channel: "Coastline S1 E2",
			Started: ta.clock.Add(-time.Minute), Transcoded: true, TranscodedBy: hub.TranscodedBySource},
		// This server's ffmpeg transcodes it (D73).
		{Kind: hub.PlaysMovie, DeviceID: 999, ItemID: 3, SourceID: src, Source: "main", Channel: "Night Train",
			Started: ta.clock.Add(-2 * time.Minute), Playing: true, Converted: true, Transcoded: true,
			TranscodedBy: hub.TranscodedByOverscan},
	})
	body := ta.do("GET", "/admin/", nil, c).Body.String()
	mustContain(t, "the overview", body, `<span class="num-chip">Film</span><div><span class="strong">The Long Field</span>`,
		`<span class="num-chip">Ep.</span><div><span class="strong">Coastline S1 E2</span>`,
		`title="It goes through ffmpeg, which converts what the box cannot play">Converted</span>`,
		`title="Its source's server transcodes the picture, which the box cannot play">Transcoded</span>`,
		`title="This server's ffmpeg transcodes the picture, which the box cannot play">Transcoded</span>`)
	if n := strings.Count(body, ">Converted</span>"); n != 1 {
		t.Errorf("%d streams marked converted", n)
	}
	if n := strings.Count(body, ">Transcoded</span>"); n != 2 {
		t.Errorf("%d streams marked transcoded", n)
	}
	rec := ta.apiDo("GET", "streams", "", c)
	var streams struct{ Streams []apiStream }
	if err := json.Unmarshal(rec.Body.Bytes(), &streams); err != nil || len(streams.Streams) != 3 {
		t.Fatalf("streams: status %d, %s", rec.Code, rec.Body)
	}
	if s := streams.Streams[0]; s.Kind != "movie" || s.ItemID != 1 || s.EntryID != 0 || s.Channel != "The Long Field" ||
		!s.Converted || s.Repaired || s.TranscodedBy != "" {
		t.Errorf("stream %+v", s)
	}
	if s := streams.Streams[1]; s.Converted || !s.Transcoded || s.TranscodedBy != "source" {
		t.Errorf("stream %+v", s)
	}
	if s := streams.Streams[2]; !s.Converted || !s.Transcoded || s.TranscodedBy != "overscan" {
		t.Errorf("stream %+v", s)
	}
	if !strings.Contains(rec.Body.String(), `"transcoded_by": "overscan"`) || strings.Count(rec.Body.String(), "transcoded_by") != 2 {
		t.Errorf("streams: %s", rec.Body)
	}
}

func TestOverviewWithoutSources(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, _ := ta.signIn("correct horse")
	body := ta.do("GET", "/admin/", nil, c).Body.String()
	mustContain(t, "an empty overview", body, `To do <span class="count quiet">1</span>`, "<strong>Add a source</strong>",
		`href="/admin/sources#add-source"`, "None yet")
}

func TestCaptureLogPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf := ta.withActivity()
	body := ta.do("GET", "/admin/capture", nil, c).Body.String()
	mustContain(t, "the capture log", body, `<a href="/admin/capture" aria-current="page" title="Capture log">`,
		"<code>itv</code> · <code>set_fav</code>", "<code>fav_ch</code>, <code>type</code>",
		"type=itv&amp;action=set_fav&amp;fav_ch=1&amp;token=[redacted]", "Living room", "Clear the log")

	if rec := ta.do("POST", "/admin/capture/clear", url.Values{"csrf": {"wrong"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the token: status %d", rec.Code)
	}
	rec := ta.do("POST", "/admin/capture/clear", url.Values{"csrf": {csrf}}, c, "HX-Request", "true")
	mustContain(t, "after clearing", rec.Body.String(), "Cleared the log of 1 kind of request.", "Nothing yet")
	if rec = ta.do("POST", "/admin/capture/clear", url.Values{"csrf": {csrf}}, c); rec.Code != http.StatusSeeOther {
		t.Errorf("without htmx: status %d", rec.Code)
	}
}

func TestStreamsAndCaptureAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf := ta.withActivity()

	rec := ta.apiDo("GET", "streams", "", c)
	var streams struct{ Streams []apiStream }
	if err := json.Unmarshal(rec.Body.Bytes(), &streams); err != nil || len(streams.Streams) != 2 {
		t.Fatalf("streams: status %d, %s", rec.Code, rec.Body)
	}
	if s := streams.Streams[0]; s.Channel != "Alpha" || s.Device != "Living room" || s.Bytes != 3<<20 || !s.Playing ||
		!s.StartedAt.Equal(ta.clock.Add(-3*time.Minute)) {
		t.Errorf("stream %+v", s)
	}

	rec = ta.apiDo("GET", "capture", "", c)
	var captures struct{ Requests []apiCapture }
	if err := json.Unmarshal(rec.Body.Bytes(), &captures); err != nil || len(captures.Requests) != 1 {
		t.Fatalf("capture: status %d, %s", rec.Code, rec.Body)
	}
	if r := captures.Requests[0]; r.Action != "set_fav" || strings.Join(r.Params, ",") != "fav_ch,type" || r.Count != 1 || r.DeviceID == nil {
		t.Errorf("request %+v", r)
	}
	apiErrorOf(t, ta.apiDo("DELETE", "capture", "", c), http.StatusForbidden, "csrf")
	if rec = ta.apiDo("DELETE", "capture", "", c, csrfHeader, csrf); rec.Code != http.StatusNoContent {
		t.Errorf("clear: status %d, %s", rec.Code, rec.Body)
	}
	if rec = ta.apiDo("GET", "capture", "", c); !strings.Contains(rec.Body.String(), `"requests": []`) {
		t.Errorf("after clearing: %s", rec.Body)
	}
}
