// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// TestPlaceholders checks the setting of placeholder programmes, on the
// guides page and in the API (D142).
func TestPlaceholders(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	c, csrf, _ := ta.withGuides()
	ctx := context.Background()
	body := ta.do("GET", "/admin/guides", nil, c).Body.String()
	mustContain(t, "at first", body, "Placeholder programmes", `<option value="0" selected>Nothing</option>`,
		"Off: a channel shows no programme where its guide has none")

	hx := []string{"HX-Request", "true"}
	rec := ta.do("POST", "/admin/guides/placeholders", url.Values{"csrf": {csrf}, "placeholders": {"60"}}, c, hx...)
	mustContain(t, "hour-long", rec.Body.String(), "Channels show a placeholder in every hour in which they have no programme.",
		`<option value="60" selected>Hour-long placeholders</option>`, "a programme named after the channel in every hour")
	if block, err := ta.st.Placeholders(ctx); block != time.Hour || err != nil {
		t.Errorf("after the form: %v, %v", block, err)
	}
	if !strings.Contains(logs.String(), `msg="placeholder programmes set in the admin" minutes=60`) {
		t.Errorf("the log: %s", logs.String())
	}
	rec = ta.do("POST", "/admin/guides/placeholders", url.Values{"csrf": {csrf}, "placeholders": {"45"}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Choose how long placeholder programmes are") {
		t.Errorf("45 minutes: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/guides/placeholders", url.Values{"csrf": {csrf}, "placeholders": {"120"}}, c, hx...)
	mustContain(t, "two hours", rec.Body.String(), "in every 2 hours in which they have no programme")

	// The API.
	h := []string{csrfHeader, csrf}
	var got apiSettings
	rec = ta.apiDo("GET", "settings", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Placeholders != 120 {
		t.Errorf("the API: %s", rec.Body)
	}
	for _, bad := range []string{`"60"`, "45", "-30", "240"} {
		apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"guide_placeholders": `+bad+`}`, c, h...), http.StatusBadRequest, "bad_json")
	}
	rec = ta.apiDo("PATCH", "settings", `{"guide_placeholders": 0}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Placeholders != 0 {
		t.Errorf("off: %s", rec.Body)
	}
	if block, err := ta.st.Placeholders(ctx); block != 0 || err != nil {
		t.Errorf("off: %v, %v", block, err)
	}
}
