// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/streaminfo"
)

// The channels that failed: listed on a page of their own and counted on
// the overview, hidden until they play again one by one or all at once,
// and shown on Live TV with what their streams held (D138).
func TestChannelHealthPages(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	ctx := context.Background()
	chans, _ := ta.channels(c, "")
	alpha, bravo, charlie := chans["Alpha"], chans["Bravo"], chans["Charlie"]
	for _, id := range []int64{alpha.ChannelID, alpha.ChannelID, bravo.ChannelID} {
		if err := ta.st.NoteStreamFailed(ctx, id, ta.clock.Add(-5*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ta.st.NoteStreamPlayed(ctx, charlie.ChannelID, ta.clock.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	d := streaminfo.Details{Video: "H.264", Width: 1920, Height: 1080, Interlaced: true, FrameRate: 25, DynamicRange: "SDR",
		Audio: []string{"AC-3"}}
	if err := ta.st.NoteStreamDetails(ctx, charlie.ChannelID, d, ta.clock.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}

	mustContain(t, "the overview", ta.do("GET", "/admin/", nil, c).Body.String(),
		"2 channels failed to start the last time they were played.", `href="/admin/health"`)
	body := ta.do("GET", "/admin/health", nil, c).Body.String()
	mustContain(t, "the page", body, "<h1>Channels that failed</h1>", `<span class="strong">Alpha</span>`, "5 minutes ago",
		`<div class="note">2 times in a row</div>`, `<span class="strong">Bravo</span>`, "Not seen playing",
		`name="op" value="hide"`, fmt.Sprintf(`name="channel" value="%d"`, alpha.ChannelID), "Hide all 2 until they play again")
	if strings.Contains(body, "Charlie") {
		t.Error("a channel that plays is listed")
	}

	// One hidden until it plays: devices do not see it, and Live TV says so.
	form := url.Values{"csrf": {csrf}, "op": {"hide"}, "channel": {fmt.Sprint(alpha.ChannelID)}}
	rec := ta.do("POST", "/admin/health", form, c, "HX-Request", "true")
	mustContain(t, "after hiding", rec.Body.String(), "Hid Alpha until it plays again.", "Hidden until it plays again",
		"Show it again", "Hide it until it plays again", "Show the hidden one again")
	if got, _ := ta.channels(c, ""); !got["Alpha"].Hidden || !got["Alpha"].Health.HiddenUntilPlayed || got["Alpha"].Health.Failures != 2 {
		t.Errorf("Alpha %+v", got["Alpha"])
	}
	lineup := ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "Live TV", lineup, "Hidden until it plays</span>", `title="Its stream failed to start 5 minutes ago">`,
		`title="1080i H.264, AC-3; 25 frames a second; seen 2 hours ago">1080i</span>`)
	panel := ta.do("GET", fmt.Sprintf("/admin/lineup/channels/%d/edit", alpha.ID), nil, c).Body.String()
	mustContain(t, "Alpha's panel", panel, "Its stream", "5 minutes ago, 2 times in a row",
		fmt.Sprintf(`action="/admin/lineup/channels/%d/health"`, alpha.ID), `name="held" value="0"`, "Show it again")

	// Shown again from its panel.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/channels/%d/health", alpha.ID), url.Values{"csrf": {csrf}, "held": {"0"}}, c, "HX-Request", "true")
	mustContain(t, "after showing", rec.Body.String(), "Showed it again.", `name="held" value="1"`, "Hide until it plays again")
	if got, _ := ta.channels(c, ""); got["Alpha"].Hidden || got["Alpha"].Health.HiddenUntilPlayed {
		t.Errorf("Alpha %+v", got["Alpha"])
	}
	// Without htmx, back to the panel.
	rec = ta.do("POST", fmt.Sprintf("/admin/lineup/channels/%d/health", alpha.ID), url.Values{"csrf": {csrf}, "held": {"1"}}, c)
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), fmt.Sprintf("edit=%d", alpha.ID)) {
		t.Errorf("without htmx: %d, %q", rec.Code, rec.Header().Get("Location"))
	}

	// All at once, and all shown again.
	rec = ta.do("POST", "/admin/health", url.Values{"csrf": {csrf}, "op": {"hide-all"}}, c, "HX-Request", "true")
	mustContain(t, "after hiding all", rec.Body.String(), "Hid 1 channel until it plays again.", "Show all 2 hidden again")
	mustContain(t, "the overview", ta.do("GET", "/admin/", nil, c).Body.String(), "2 are hidden until they play again.")
	rec = ta.do("POST", "/admin/health", url.Values{"csrf": {csrf}, "op": {"show-all"}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/health" {
		t.Errorf("without htmx: %d, %q", rec.Code, rec.Header().Get("Location"))
	}
	if got, _ := ta.channels(c, ""); got["Alpha"].Hidden || got["Bravo"].Hidden {
		t.Errorf("after showing all: %+v %+v", got["Alpha"], got["Bravo"])
	}

	// What is wrong.
	if rec := ta.do("POST", "/admin/health", url.Values{"csrf": {csrf}, "op": {"hide"}, "channel": {"9999"}}, c, "HX-Request", "true"); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "There is no such channel.") {
		t.Errorf("a channel that is not: %d", rec.Code)
	}
	if rec := ta.do("POST", "/admin/health", url.Values{"csrf": {csrf}, "op": {"what"}}, c); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown op: %d", rec.Code)
	}
	if rec := ta.do("POST", "/admin/health", url.Values{"csrf": {"wrong"}, "op": {"hide-all"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the token: %d", rec.Code)
	}

	// Once Alpha plays, it no longer counts as failing.
	if _, err := ta.st.NoteStreamPlayed(ctx, alpha.ChannelID, ta.clock); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.st.NoteStreamPlayed(ctx, bravo.ChannelID, ta.clock); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "the page with none", ta.do("GET", "/admin/health", nil, c).Body.String(),
		"Every channel played the last time someone watched it.")
}

// A stream stopped from the overview, and through the API; the overview
// says what each stream holds (D138).
func TestStopAStream(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf := ta.withActivity()
	streams := ta.streams.(fakeStreams)
	streams[0].ID, streams[0].Details = "a1", "1080i H.264, AC-3"
	streams[1].ID = "b2"
	stopped := &streamStopper{fakeStreams: streams}
	ta.UseStreams(stopped)

	body := ta.do("GET", "/admin/", nil, c).Body.String()
	mustContain(t, "the overview", body, `action="/admin/streams/a1/stop"`, `action="/admin/streams/b2/stop"`,
		`<div class="note" title="What the stream holds, as Overscan read it passing through">1080i H.264, AC-3</div>`)
	rec := ta.do("POST", "/admin/streams/a1/stop", url.Values{"csrf": {csrf}}, c, "HX-Request", "true")
	mustContain(t, "after stopping", rec.Body.String(), "Stopped Alpha on Living room.")
	if fmt.Sprint(stopped.stopped) != "[a1:stopped in the admin]" {
		t.Errorf("stopped %v", stopped.stopped)
	}
	rec = ta.do("POST", "/admin/streams/zz/stop", url.Values{"csrf": {csrf}}, c, "HX-Request", "true")
	mustContain(t, "a stream that ended", rec.Body.String(), "That stream had already ended.")
	if rec := ta.do("POST", "/admin/streams/b2/stop", url.Values{"csrf": {csrf}}, c); rec.Code != http.StatusSeeOther {
		t.Errorf("without htmx: %d", rec.Code)
	}

	rec = ta.apiDo("GET", "streams", "", c)
	var list struct{ Streams []apiStream }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Streams) != 2 || list.Streams[0].ID != "a1" ||
		list.Streams[0].Details != "1080i H.264, AC-3" {
		t.Fatalf("streams: %s", rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "streams/a1/stop", "", c), http.StatusForbidden, "csrf")
	if rec := ta.apiDo("POST", "streams/a1/stop", "", c, csrfHeader, csrf); rec.Code != http.StatusNoContent {
		t.Errorf("stop: %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "streams/zz/stop", "", c, csrfHeader, csrf), http.StatusNotFound, "not_found")
	if fmt.Sprint(stopped.stopped) != "[a1:stopped in the admin b2:stopped in the admin a1:stopped through the API]" {
		t.Errorf("stopped %v", stopped.stopped)
	}

	// A recording's stream is called off, not stopped: the recorder would
	// play it again.
	stopped.fakeStreams = append(stopped.fakeStreams, hub.Stream{ID: "c3", Kind: hub.PlaysChannel, DeviceID: -1, EntryID: 1,
		Channel: "Alpha", Started: ta.clock})
	if body := ta.do("GET", "/admin/", nil, c).Body.String(); strings.Contains(body, "/admin/streams/c3/stop") {
		t.Error("a recording has a Stop button")
	}
	rec = ta.do("POST", "/admin/streams/c3/stop", url.Values{"csrf": {csrf}}, c, "HX-Request", "true")
	mustContain(t, "stopping a recording", rec.Body.String(), "A recording is called off on Settings, under Recordings.")
	apiErrorOf(t, ta.apiDo("POST", "streams/c3/stop", "", c, csrfHeader, csrf), http.StatusConflict, "recording")
	if len(stopped.stopped) != 3 {
		t.Errorf("stopped %v", stopped.stopped)
	}
}

// streamStopper is fakeStreams that notes what it is asked to stop.
type streamStopper struct {
	fakeStreams
	stopped []string
}

func (s *streamStopper) StopStream(id, reason string) bool {
	if !s.fakeStreams.StopStream(id, reason) {
		return false
	}
	s.stopped = append(s.stopped, id+":"+reason)
	return true
}

var _ Streams = (*streamStopper)(nil)

func TestHealthAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	ctx := context.Background()
	chans, _ := ta.channels(c, "")
	alpha := chans["Alpha"]
	if err := ta.st.NoteStreamFailed(ctx, alpha.ChannelID, ta.clock.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	d := streaminfo.Details{Video: "HEVC", Width: 3840, Height: 2160, BitDepth: 10, FrameRate: 50, DynamicRange: "HLG", Audio: []string{"AAC"}}
	if err := ta.st.NoteStreamDetails(ctx, alpha.ChannelID, d, ta.clock.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	rec := ta.apiDo("GET", "health", "", c)
	var list struct {
		Channels []apiFailedChannel
		Total    int
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || list.Total != 1 || len(list.Channels) != 1 {
		t.Fatalf("health: %s", rec.Body)
	}
	if got := list.Channels[0]; got.Name != "Alpha" || got.Source != "main" || got.Failures != 1 || got.FailedAt == nil ||
		got.PlayedAt != nil || got.HiddenUntilPlayed || got.Stream == nil || got.Stream.Summary != "2160p HEVC HLG, AAC" ||
		got.Stream.BitDepth != 10 || got.Stream.FrameRate != 50 {
		t.Errorf("Alpha %+v, %+v", got, got.Stream)
	}
	if !strings.Contains(rec.Body.String(), `"played_at": null`) {
		t.Errorf("health: %s", rec.Body)
	}

	path := fmt.Sprintf("health/%d", alpha.ChannelID)
	apiErrorOf(t, ta.apiDo("PUT", path, `{"hidden_until_played": true}`, c), http.StatusForbidden, "csrf")
	apiErrorOf(t, ta.apiDo("PUT", path, `{}`, c, h...), http.StatusBadRequest, "bad_request")
	apiErrorOf(t, ta.apiDo("PUT", path, `{"hidden": true}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PUT", "health/9999", `{"hidden_until_played": true}`, c, h...), http.StatusNotFound, "not_found")
	rec = ta.apiDo("PUT", path, `{"hidden_until_played": true}`, c, h...)
	var one apiHealth
	if err := json.Unmarshal(rec.Body.Bytes(), &one); err != nil || rec.Code != http.StatusOK || !one.HiddenUntilPlayed {
		t.Fatalf("hide: %d, %s", rec.Code, rec.Body)
	}
	if got, _ := ta.channels(c, ""); !got["Alpha"].Hidden || got["Alpha"].Health.Stream == nil || got["Alpha"].Health.Stream.Height != 2160 {
		t.Errorf("Alpha %+v", got["Alpha"])
	}

	// All at once.
	if err := ta.st.NoteStreamFailed(ctx, chans["Bravo"].ChannelID, ta.clock); err != nil {
		t.Fatal(err)
	}
	for _, c2 := range []struct {
		body    string
		changed int
	}{{`{"hidden_until_played": true}`, 1}, {`{"hidden_until_played": false}`, 2}, {`{"hidden_until_played": false}`, 0}} {
		rec = ta.apiDo("PATCH", "health", c2.body, c, h...)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"changed": %d`, c2.changed)) {
			t.Errorf("%s: %d, %s", c2.body, rec.Code, rec.Body)
		}
	}
}
