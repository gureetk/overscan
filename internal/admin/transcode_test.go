// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"overscan.gureet.ca/internal/hub"
)

// transcoder stands in for a hub whose ffmpeg can do what encoder says.
type transcoder struct {
	fakeStreams
	convert bool
	encoder hub.Encoder
}

func (f transcoder) CanConvert() bool     { return f.convert }
func (f transcoder) Encoder() hub.Encoder { return f.encoder }

func TestTranscodeSetting(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	settings := func() string { return ta.do("GET", "/admin/settings", nil, c).Body.String() }

	// Without ffmpeg, or with one that lacks libx264, it cannot.
	mustContain(t, "the settings without ffmpeg", settings(), `<h2 id="transcode-title">Converting and transcoding</h2>`,
		"There is no ffmpeg and ffprobe to run, so films and episodes go to boxes as they are.",
		`<select id="transcode" name="at_once" aria-describedby="transcode-text" disabled>`,
		`<input class="switch" type="checkbox" id="convert" name="convert" value="1" checked disabled aria-describedby="convert-text">`,
		`<option value="0" selected>Off</option>`, "Off: films and episodes whose video a box cannot play")
	ta.UseStreams(transcoder{convert: true})
	mustContain(t, "the settings without libx264", settings(), "This server's ffmpeg has no libx264, so it can convert sound but not transcode video.",
		`<input class="switch" type="checkbox" id="convert" name="convert" value="1" checked aria-describedby="convert-text">`)
	ta.UseStreams(transcoder{convert: true, encoder: hub.Encoder{H264: true}})
	mustContain(t, "the settings without zscale", settings(), "This server's ffmpeg has no zscale filter, so HDR video it transcodes comes out washed out.",
		`<select id="transcode" name="at_once" aria-describedby="transcode-text">`)

	ta.UseStreams(transcoder{convert: true, encoder: hub.Encoder{H264: true, ToneMap: true}})
	rec := ta.do("POST", "/admin/settings/transcode", lineupForm(csrf, "", "at_once", "2"), c, hx...)
	mustContain(t, "turned on", rec.Body.String(), "This server transcodes up to 2 films at a time whose video a box cannot play.",
		"ffmpeg can convert sound, transcode with libx264, and map HDR to SDR", `<option value="2" selected>2 at a time</option>`,
		"come to it as 1080p H.264 that this server's ffmpeg makes as they play, 2 at a time.")
	if n, err := ta.st.Transcodes(context.Background()); err != nil || n != 2 {
		t.Errorf("transcodes %d, %v", n, err)
	}
	for _, bad := range []string{"4", "-1", "two", ""} {
		rec := ta.do("POST", "/admin/settings/transcode", lineupForm(csrf, "", "at_once", bad), c, hx...)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%q: status %d", bad, rec.Code)
		}
		mustContain(t, "a bad choice", rec.Body.String(), "Choose how many films at a time, from none to 3.")
	}
	rec = ta.do("POST", "/admin/settings/transcode", lineupForm(csrf, "", "at_once", "0"), c, hx...)
	mustContain(t, "turned off", rec.Body.String(), "Films whose video a box cannot play go to it as they are.")
	if rec = ta.do("POST", "/admin/settings/transcode", lineupForm(csrf, "", "at_once", "1"), c); rec.Code != http.StatusSeeOther {
		t.Errorf("without htmx: status %d", rec.Code)
	}

	// In the API.
	var got apiSettings
	rec = ta.apiDo("GET", "settings", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.TranscodeVideo != 1 {
		t.Errorf("settings: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", "settings", `{"transcode_video": 3}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.TranscodeVideo != 3 {
		t.Errorf("set to 3: status %d, %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"transcode_video": 4}`, `{"transcode_video": -1}`, `{"transcode_video": 1.5}`, `{"transcode_video": true}`} {
		apiErrorOf(t, ta.apiDo("PATCH", "settings", bad, c, h...), http.StatusBadRequest, "bad_json")
	}
	if n, _ := ta.st.Transcodes(context.Background()); n != 3 {
		t.Errorf("transcodes %d after bad requests", n)
	}
}

// TestConversionSwitches turns off, and on again, the conversion of films
// whose sound a box cannot play, and Jellyfin's transcoding of those whose
// video it cannot (D78), from the Settings page and in the API.
func TestConversionSwitches(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ta.UseStreams(transcoder{convert: true, encoder: hub.Encoder{H264: true, ToneMap: true}})
	ctx := context.Background()

	rec := ta.do("GET", "/admin/settings", nil, c)
	mustContain(t, "at first", rec.Body.String(),
		`<input class="switch" type="checkbox" id="convert" name="convert" value="1" checked aria-describedby="convert-text">`,
		`<input class="switch" type="checkbox" id="server-transcode" name="server" value="1" checked aria-describedby="server-transcode-text">`,
		"goes through ffmpeg, which copies its picture and converts its sound to Dolby Digital.",
		"A Jellyfin server transcodes its own films and episodes whose video a box cannot play")

	rec = ta.do("POST", "/admin/settings/convert", lineupForm(csrf, ""), c, hx...)
	mustContain(t, "convert off", rec.Body.String(), "Films whose sound or file a box cannot play go to it as they are, from their next start.",
		`<input class="switch" type="checkbox" id="convert" name="convert" value="1" aria-describedby="convert-text">`,
		"Off: films and episodes whose sound or file a box cannot play go to it as they are")
	rec = ta.do("POST", "/admin/settings/server-transcode", lineupForm(csrf, ""), c, hx...)
	mustContain(t, "server off", rec.Body.String(), "Films of Jellyfin servers whose video a box cannot play go to it as they are, from their next start.",
		"Off: Jellyfin's films and episodes whose video a box cannot play go to it as they are.")
	if on, err := ta.st.ConvertsSound(ctx); err != nil || on {
		t.Errorf("converts %v, %v", on, err)
	}
	if on, err := ta.st.ServerTranscodes(ctx); err != nil || on {
		t.Errorf("server transcodes %v, %v", on, err)
	}
	rec = ta.do("POST", "/admin/settings/convert", lineupForm(csrf, "", "convert", "1"), c, hx...)
	mustContain(t, "convert on", rec.Body.String(), "Films whose sound or file a box cannot play go through ffmpeg, which converts them, from their next start.")

	// In the API.
	var got apiSettings
	rec = ta.apiDo("GET", "settings", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || !got.ConvertSound || got.ServerTranscodes {
		t.Errorf("settings: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", "settings", `{"convert_sound": false, "server_transcodes": true}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ConvertSound || !got.ServerTranscodes {
		t.Errorf("set: status %d, %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"convert_sound": 1}`, `{"server_transcodes": "no"}`} {
		apiErrorOf(t, ta.apiDo("PATCH", "settings", bad, c, h...), http.StatusBadRequest, "bad_json")
	}
}
