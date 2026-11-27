// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/store"
)

// An account's page and the API choose its output profile; the Devices page
// offers a device's own once this server transcodes (D110).
func TestOutputProfiles(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	if _, err := ta.st.ApproveDevice(ctx, "00:1A:79:00:00:01", store.HouseholdAccount, "Lounge", ta.clock); err != nil {
		t.Fatal(err)
	}

	// Without ffmpeg: the account's page offers it, and says why it would do nothing.
	page := ta.do("GET", "/admin/accounts/1", nil, c).Body.String()
	mustContain(t, "the account's page", page, `<label for="live">Live streams</label>`, `<option value="source" selected>As the source sends it</option>`,
		`<option value="h264-720p">H.264, up to 720p</option>`, "This server cannot transcode: it lacks ffmpeg and ffprobe")
	if strings.Contains(ta.do("GET", "/admin/devices", nil, c).Body.String(), "<th>Live streams</th>") {
		t.Error("the devices offer their own output profiles while the server cannot transcode")
	}

	rec := ta.do("POST", "/admin/accounts/1/live", lineupForm(csrf, "", "output_profile", "h264-720p"), c, hx...)
	mustContain(t, "after choosing", rec.Body.String(),
		"Household&#39;s devices and players get live channels made H.264, up to 720p by this server, from the next channel they start.",
		"· live as H.264, up to 720p", `<option value="h264-720p" selected>`)
	if !strings.Contains(logs.String(), `msg="an account's output profile set in the admin" account=Household output_profile=h264-720p`) {
		t.Errorf("logs:\n%s", logs.String())
	}
	if rec := ta.do("POST", "/admin/accounts/1/live", lineupForm(csrf, "", "output_profile", "8k"), c, hx...); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "Choose an output profile: source, h264-720p, h264-1080p, or one made on the Transcoding page.") {
		t.Errorf("an unknown profile: status %d", rec.Code)
	}

	// With transcoding on, the devices offer their own.
	ta.UseStreams(transcoder{convert: true, encoder: hub.Encoder{H264: true}})
	if err := ta.st.SetTranscodes(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if page := ta.do("GET", "/admin/accounts/1", nil, c).Body.String(); strings.Contains(page, "This server cannot transcode") ||
		strings.Contains(page, "Transcoding is off") {
		t.Error("the account's page still says that it would do nothing")
	}
	page = ta.do("GET", "/admin/devices", nil, c).Body.String()
	mustContain(t, "the devices", page, "<th>Live streams</th>", `<option value="" selected>As its account's</option>`)
	rec = ta.do("POST", "/admin/devices/00:1A:79:00:00:01/live", lineupForm(csrf, "", "output_profile", "source"), c, hx...)
	mustContain(t, "a device's own", rec.Body.String(),
		"Lounge (00:1A:79:00:00:01) gets live channels as their sources send them, from the next channel it starts.")
	if d, _ := ta.st.Device(ctx, "00:1A:79:00:00:01"); d.OutputProfile != "source" {
		t.Errorf("the device: %+v", d)
	}

	// The API.
	rec = ta.apiDo("PATCH", "accounts/1", `{"output_profile": "h264-1080p"}`, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"output_profile": "h264-1080p"`) {
		t.Errorf("PATCH the account: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", "devices/00:1A:79:00:00:01", `{"output_profile": ""}`, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"output_profile": ""`) {
		t.Errorf("PATCH the device: status %d, %s", rec.Code, rec.Body)
	}
	for path, body := range map[string]string{"accounts/1": `{"output_profile": ""}`, "devices/00:1A:79:00:00:01": `{"output_profile": "4k"}`} {
		if rec := ta.apiDo("PATCH", path, body, c, h...); rec.Code != http.StatusBadRequest ||
			!strings.Contains(rec.Body.String(), `"bad_output_profile"`) {
			t.Errorf("PATCH %s %s: status %d, %s", path, body, rec.Code, rec.Body)
		}
	}
}
