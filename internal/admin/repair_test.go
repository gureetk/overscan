// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepairInTheAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	chs, _ := ta.channels(c, "")
	if chs["Alpha"].Repair || chs["Alpha"].RepairOwn != nil {
		t.Fatal("Alpha is repaired from the start")
	}

	rec := ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Alpha"].ID), `{"repair": true}`, c, h...)
	var e apiEntry
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || !e.Repair || e.RepairOwn == nil || !*e.RepairOwn {
		t.Errorf("repair Alpha: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("PATCH", "lineup/channels", fmt.Sprintf(`{"ids": [%d, %d], "repair": true}`, chs["Bravo"].ID, chs["Charlie"].ID), c, h...)
	if rec.Code != http.StatusOK {
		t.Errorf("repair Bravo and Charlie: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Delta"].ID), `{"repair": "yes"}`, c, h...),
		http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "lineup/channels", `{"ids": [999999], "repair": true}`, c, h...),
		http.StatusNotFound, "not_found")

	chs, _ = ta.channels(c, "")
	for name, want := range map[string]bool{"Alpha": true, "Bravo": true, "Charlie": true, "Delta": false} {
		if chs[name].Repair != want {
			t.Errorf("%s: repair %v", name, chs[name].Repair)
		}
	}
	rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Bravo"].ID), `{"repair": false}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Repair || e.RepairOwn == nil || *e.RepairOwn {
		t.Errorf("stop repairing Bravo: status %d, %s", rec.Code, rec.Body)
	}

	// The default: Charlie follows it once given back, Bravo keeps its no.
	settings := func(rec *httptest.ResponseRecorder) (bool, bool) {
		var got map[string]any
		err := json.Unmarshal(rec.Body.Bytes(), &got)
		return got["repair_default"] == true, err == nil && rec.Code == http.StatusOK && len(got) == 20 && got["gpu_transcodes"] == 2.0 &&
			got["outputs"] == false && fmt.Sprint(got["live_transcode"]) == "map[mode:as_needed profile:<nil>]" && got["outputs_anywhere"] == false && got["guide_placeholders"] == 0.0 &&
			got["tmdb_pictures"] == false &&
			got["repair_hls"] == false && got["transcode_video"] == 0.0 && got["convert_sound"] == true &&
			got["server_transcodes"] == true && got["adult_marks"] == "lock" && got["radio_apart"] == false &&
			got["tmdb_key_set"] == false && got["tmdb_language"] == "" && got["recordings_cap"] == 0.0 &&
			got["timeshift_minutes"] == 0.0 && got["shared_tuner"] == false &&
			got["recordings_until"] == "programme" && got["recordings_max_hours"] == 4.0
	}
	if on, ok := settings(ta.apiDo("GET", "settings", "", c)); !ok || on {
		t.Errorf("settings at first: %v, %v", on, ok)
	}
	if on, ok := settings(ta.apiDo("PATCH", "settings", `{"repair_default": true}`, c, h...)); !ok || !on {
		t.Errorf("turn the default on: %v, %v", on, ok)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"repair_default": "yes"}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"repair_hls": 1}`, c, h...), http.StatusBadRequest, "bad_json")
	for _, on := range []bool{true, false} {
		rec := ta.apiDo("PATCH", "settings", fmt.Sprintf(`{"repair_hls": %v}`, on), c, h...)
		var got apiSettings
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.RepairHLS != on || !got.RepairDefault {
			t.Errorf("repair_hls set to %v: status %d, %s", on, rec.Code, rec.Body)
		}
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"colour": "teal"}`, c, h...), http.StatusBadRequest, "bad_json")
	rec = ta.apiDo("PATCH", fmt.Sprintf("lineup/channels/%d", chs["Charlie"].ID), `{"repair": null}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || !e.Repair || e.RepairOwn != nil {
		t.Errorf("give Charlie back to the default: status %d, %s", rec.Code, rec.Body)
	}
	chs, _ = ta.channels(c, "")
	for name, want := range map[string]bool{"Alpha": true, "Bravo": false, "Charlie": true, "Delta": true} {
		if chs[name].Repair != want {
			t.Errorf("with the default on, %s: repair %v", name, chs[name].Repair)
		}
	}
}

func TestRepairForms(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	chs, _ := ta.channels(c, "")
	hx := []string{"HX-Request", "true"}
	send := func(path string, form map[string][]string, want ...string) string {
		t.Helper()
		rec := ta.do("POST", path, form, c, hx...)
		if rec.Code != http.StatusOK {
			t.Errorf("%s %v: status %d", path, form, rec.Code)
		}
		mustContain(t, path, rec.Body.String(), want...)
		return rec.Body.String()
	}
	repairOf := func(name string) (bool, string) {
		t.Helper()
		e, _ := ta.channels(c, "")
		own := ""
		if o := e[name].RepairOwn; o != nil {
			own = map[bool]string{true: "on", false: "off"}[*o]
		}
		return e[name].Repair, own
	}

	send("/admin/lineup/channels", lineupForm(csrf, "", "action", "repair", "ids", fmt.Sprint(chs["Delta"].ID), "ids", fmt.Sprint(chs["Alpha"].ID)),
		"Set 2 channels to be repaired.", `title="Set to be repaired, but there is no ffmpeg to run">`)
	send("/admin/lineup/channels", lineupForm(csrf, "", "action", "norepair", "ids", fmt.Sprint(chs["Alpha"].ID)),
		"Set 1 channel to play as they are.", `title="Its stream plays as the source sends it, whatever the default">`)
	if r, own := repairOf("Alpha"); r || own != "off" {
		t.Errorf("Alpha: %v, %q", r, own)
	}
	send("/admin/lineup/channels", lineupForm(csrf, "", "action", "repairdefault", "ids", fmt.Sprint(chs["Alpha"].ID)),
		"Set 1 channel to be repaired as the default says.")
	if r, own := repairOf("Alpha"); r || own != "" {
		t.Errorf("Alpha as the default: %v, %q", r, own)
	}

	// The edit row's choice, and its warning while there is no ffmpeg.
	edit := ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", chs["Delta"].ID), nil, c).Body.String()
	mustContain(t, "the panel", edit, `name="repair" value="on" checked`, "The default is not to repair.", "There is no ffmpeg to run")
	save := lineupForm(csrf, "", "name", "", "group", "0", "visibility", "group", "number", "4", "repair", "off")
	send(fmt.Sprintf("/admin/lineup/channels/%d", chs["Delta"].ID), save, "Saved Delta.")
	if r, own := repairOf("Delta"); r || own != "off" {
		t.Errorf("Delta after saving off: %v, %q", r, own)
	}
	save.Set("repair", "")
	send(fmt.Sprintf("/admin/lineup/channels/%d", chs["Delta"].ID), save, "Saved Delta.")
	if r, own := repairOf("Delta"); r || own != "" {
		t.Errorf("Delta after saving as the default: %v, %q", r, own)
	}
	save.Set("repair", "maybe")
	if rec := ta.do("POST", fmt.Sprintf("/admin/lineup/channels/%d", chs["Delta"].ID), save, c, hx...); !strings.Contains(rec.Body.String(), "Choose how its stream is repaired.") {
		t.Errorf("a bad choice: status %d", rec.Code)
	}

	// The default, from the Settings page.
	mustContain(t, "the settings", ta.do("GET", "/admin/settings", nil, c).Body.String(),
		`id="repair-all" name="default" value="1" aria-describedby`, "There is no ffmpeg to run")
	send("/admin/settings/repair", lineupForm(csrf, "", "default", "1"), "Every channel is repaired now, unless it is set not to be.",
		`id="repair-all" name="default" value="1" checked aria-describedby`)
	if r, own := repairOf("Delta"); !r || own != "" {
		t.Errorf("Delta with the default on: %v, %q", r, own)
	}
	edit = ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", chs["Delta"].ID), nil, c).Body.String()
	mustContain(t, "the panel", edit, `name="repair" value="" checked`, "The default is to repair every channel.")
	send("/admin/settings/repair", lineupForm(csrf, ""), "Channels are repaired only where they are set to be.")
	if r, _ := repairOf("Delta"); r {
		t.Error("Delta is repaired with the default off")
	}

	// HLS too, from the Settings page.
	mustContain(t, "the settings", ta.do("GET", "/admin/settings", nil, c).Body.String(),
		`id="repair-hls" name="hls" value="1" aria-describedby`, "Off: a channel whose address is HLS plays as HLS")
	send("/admin/settings/repair-hls", lineupForm(csrf, "", "hls", "1"),
		"Repaired channels whose address is HLS are made one stream and repaired, from their next start.",
		`id="repair-hls" name="hls" value="1" checked aria-describedby`, "comes to boxes as one stream")
	if on, err := ta.st.RepairHLS(context.Background()); err != nil || !on {
		t.Errorf("HLS repaired: %v, %v", on, err)
	}
	send("/admin/settings/repair-hls", lineupForm(csrf, ""), "Channels whose address is HLS play as HLS again")
	if on, _ := ta.st.RepairHLS(context.Background()); on {
		t.Error("HLS is still repaired")
	}

	// With ffmpeg, the warning goes.
	ta.UseStreams(fakeStreams{})
	edit = ta.do("GET", fmt.Sprintf("/admin/lineup?edit=%d", chs["Delta"].ID), nil, c).Body.String()
	if strings.Contains(edit, "There is no ffmpeg to run") {
		t.Errorf("with ffmpeg, the edit row says:\n%s", edit)
	}
	send("/admin/lineup/channels", lineupForm(csrf, "", "action", "repair", "ids", fmt.Sprint(chs["Delta"].ID)))
	mustContain(t, "the page", ta.do("GET", "/admin/lineup", nil, c).Body.String(), `title="Its stream goes through ffmpeg">`)
	mustContain(t, "the settings", ta.do("GET", "/admin/settings", nil, c).Body.String(),
		"only the channels set to be repaired go through ffmpeg", `<i class="dot ok" aria-hidden="true"></i>ffmpeg is ready`)
}
