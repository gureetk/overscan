// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// TestTimeshift turns timeshift on and off from the Settings page and the
// API (D92).
func TestTimeshift(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()

	page := ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "off", page, "Pause live TV", "Off: boxes play live TV as it comes.",
		`<option value="0" selected>Off</option>`, `<option value="60">an hour</option>`)

	answer := ta.do("POST", "/admin/settings/timeshift", lineupForm(csrf, "", "minutes", "30"), c, hx...)
	mustContain(t, "on", answer.Body.String(),
		"Boxes can pause live TV and go back up to 30 minutes in it, from the next channel they play.",
		"go back up to 30 minutes in it: this server keeps the channel on disk while a box watches it.",
		`<option value="30" selected>30 minutes</option>`)
	if n, _ := ta.st.Timeshift(ctx); n != 30 {
		t.Errorf("%d minutes", n)
	}

	rec := ta.apiDo("PATCH", "settings", `{"timeshift_minutes": 45}`, c, h...)
	var v apiSettings
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || v.TimeshiftMinutes != 45 {
		t.Errorf("timeshift_minutes: status %d, %s", rec.Code, rec.Body)
	}
	// One that the page does not offer shows among its choices.
	mustContain(t, "45", ta.do("GET", "/admin/settings", nil, c).Body.String(), `<option value="45" selected>45 minutes</option>`)
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"timeshift_minutes": 241}`, c, h...), http.StatusBadRequest, "bad_timeshift")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"timeshift_minutes": "long"}`, c, h...), http.StatusBadRequest, "bad_json")
	mustContain(t, "bad form", ta.do("POST", "/admin/settings/timeshift", lineupForm(csrf, "", "minutes", "x"), c, hx...).Body.String(),
		"Choose how much to keep, or Off.")

	answer = ta.do("POST", "/admin/settings/timeshift", lineupForm(csrf, "", "minutes", "0"), c, hx...)
	mustContain(t, "off again", answer.Body.String(), "Boxes play live TV as it comes, from the next channel they play.")
	if n, _ := ta.st.Timeshift(ctx); n != 0 {
		t.Errorf("%d minutes after turning it off", n)
	}
}

func TestMinutesText(t *testing.T) {
	for minutes, want := range map[int]string{1: "a minute", 15: "15 minutes", 60: "an hour", 90: "90 minutes", 120: "2 hours"} {
		if got := minutesText(minutes); got != want {
			t.Errorf("%d: %q, want %q", minutes, got, want)
		}
	}
}
