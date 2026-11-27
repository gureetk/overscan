// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// The API's profiles and what is transcoded of live TV, a group and a
// channel (D147).
func TestTranscodingInTheAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()

	// At first, the fixed profiles only, and everything as needed.
	var list struct{ Profiles []apiTranscodeProfile }
	rec := ta.apiDo("GET", "transcode-profiles", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Profiles) != 3 || !list.Profiles[0].Fixed ||
		list.Profiles[1].Name != "h264-720p" || list.Profiles[1].Codec != nil {
		t.Fatalf("profiles at first: %d %s", rec.Code, rec.Body)
	}
	chs, _ := ta.channels(c, "")
	if a := chs["Alpha"]; a.Transcode.Mode != "as_needed" || a.Transcode.SetBy != nil || a.TranscodeOwn.Mode != "as_needed" {
		t.Errorf("Alpha at first: %+v", a)
	}
	if g := ta.groups(c)["News"]; g.Transcode.Mode != "as_needed" || g.Transcode.Profile != nil {
		t.Errorf("News at first: %+v", g.Transcode)
	}

	// Make one; what is left out takes its default.
	rec = ta.apiDo("POST", "transcode-profiles", `{"name": "kitchen", "codec": "hevc", "max_lines": 720, "max_kbps": 3000}`, c, h...)
	var p apiTranscodeProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusCreated || p.Fixed || *p.Codec != "hevc" ||
		*p.MaxLines != 720 || *p.MaxKbps != 3000 || *p.Quality != "medium" || *p.FrameRate != "source" || !*p.Deinterlace ||
		!*p.HDRToSDR || *p.Sound != "aac" || p.Describes != "HEVC, up to 720p, medium quality, at most 3 Mbit/s, AAC in stereo" {
		t.Fatalf("made: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "transcode-profiles", `{"name": "kitchen"}`, c, h...), http.StatusConflict, "name_taken")
	apiErrorOf(t, ta.apiDo("POST", "transcode-profiles", `{"name": "Kitchen 2"}`, c, h...), http.StatusBadRequest, "bad_profile")
	apiErrorOf(t, ta.apiDo("POST", "transcode-profiles", `{"name": "source"}`, c, h...), http.StatusBadRequest, "bad_profile")
	apiErrorOf(t, ta.apiDo("POST", "transcode-profiles", `{"name": "big", "max_lines": 2160}`, c, h...), http.StatusBadRequest, "bad_profile")
	apiErrorOf(t, ta.apiDo("POST", "transcode-profiles", `{"name": "x", "sound": "dts"}`, c, h...), http.StatusBadRequest, "bad_profile")
	apiErrorOf(t, ta.apiDo("PATCH", "transcode-profiles/h264-720p", `{"quality": "low"}`, c, h...), http.StatusConflict, "fixed_profile")
	apiErrorOf(t, ta.apiDo("PATCH", "transcode-profiles/nobody", `{"quality": "low"}`, c, h...), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("GET", "transcode-profiles/nobody", "", c), http.StatusNotFound, "not_found")

	// Live TV, a group and a channel name it, or never.
	rec = ta.apiDo("PATCH", "settings", `{"live_transcode": {"mode": "always", "profile": "kitchen"}}`, c, h...)
	var settings apiSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil || rec.Code != http.StatusOK ||
		settings.LiveTranscode.Mode != "always" || *settings.LiveTranscode.Profile != "kitchen" {
		t.Errorf("live TV: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"live_transcode": {"mode": "always", "profile": "source"}}`, c, h...), http.StatusBadRequest, "bad_transcode")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"live_transcode": {"mode": "always"}}`, c, h...), http.StatusBadRequest, "bad_transcode")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"live_transcode": {"mode": "sometimes"}}`, c, h...), http.StatusBadRequest, "bad_transcode")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"live_transcode": "never"}`, c, h...), http.StatusBadRequest, "bad_transcode")
	news := ta.groups(c)["News"]
	rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/groups/%d", news.ID), `{"transcode": {"mode": "never"}}`, c, h...)
	var g apiGroup
	if err := json.Unmarshal(rec.Body.Bytes(), &g); err != nil || rec.Code != http.StatusOK || g.Transcode.Mode != "never" {
		t.Errorf("News: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("lineup/groups/%d", news.ID), `{"transcode": {"mode": "always", "profile": "nobody"}}`, c, h...),
		http.StatusBadRequest, "bad_transcode")
	rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Charlie"].ID), `{"transcode": {"mode": "always", "profile": "h264-1080p"}}`, c, h...)
	var e apiEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || rec.Code != http.StatusOK || e.TranscodeOwn.Mode != "always" ||
		*e.Transcode.Profile != "h264-1080p" || *e.Transcode.SetBy != "channel" {
		t.Errorf("Charlie: %d %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", "lineup/channels", fmt.Sprintf(`{"ids": [%d], "transcode": {"mode": "never"}}`, chs["Delta"].ID), c, h...)
	if rec.Code != http.StatusOK {
		t.Errorf("Delta: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Bravo"].ID), `{"transcode": {"mode": "always"}}`, c, h...),
		http.StatusBadRequest, "bad_json")
	chs, _ = ta.channels(c, "")
	for name, want := range map[string]string{"Alpha": "never group", "Bravo": "always live_tv", "Charlie": "always channel", "Delta": "never channel"} {
		got := chs[name].Transcode.Mode + " " + *chs[name].Transcode.SetBy
		if got != want {
			t.Errorf("%s: %s, want %s", name, got, want)
		}
	}

	// In use, it stays; renamed, everything follows.
	apiErrorOf(t, ta.apiDo("DELETE", "transcode-profiles/kitchen", "", c, h...), http.StatusConflict, "profile_in_use")
	rec = ta.apiDo("PATCH", "transcode-profiles/kitchen", `{"name": "small", "quality": "low"}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != http.StatusOK || p.Name != "small" || *p.Quality != "low" ||
		!p.Uses.LiveTV || *p.Codec != "hevc" {
		t.Errorf("renamed: %d %s", rec.Code, rec.Body)
	}
	chs, _ = ta.channels(c, "")
	if b := chs["Bravo"]; *b.Transcode.Profile != "small" {
		t.Errorf("Bravo after the rename: %+v", b.Transcode)
	}
	rec = ta.apiDo("PATCH", "accounts/1", `{"output_profile": "small"}`, c, h...)
	if rec.Code != http.StatusOK {
		t.Errorf("the account takes it: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "accounts/1", `{"output_profile": "nobody"}`, c, h...), http.StatusBadRequest, "bad_output_profile")

	// Taken off everything, it goes.
	for _, call := range [][3]string{
		{"PATCH", "settings", `{"live_transcode": {"mode": "as_needed"}}`},
		{"PATCH", "accounts/1", `{"output_profile": "source"}`},
	} {
		if rec := ta.apiDo(call[0], call[1], call[2], c, h...); rec.Code != http.StatusOK {
			t.Fatalf("%v: %d %s", call, rec.Code, rec.Body)
		}
	}
	if rec := ta.apiDo("DELETE", "transcode-profiles/small", "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("removed: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("DELETE", "transcode-profiles/h264-720p", "", c, h...), http.StatusConflict, "fixed_profile")
	apiErrorOf(t, ta.apiDo("DELETE", "transcode-profiles/small", "", c, h...), http.StatusNotFound, "not_found")
}
