// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// sourceStopper is a hub that records what the admin stops by source.
type sourceStopper struct {
	fakeStreams
	stopped []string // each call: its account, its sources and its reason
}

func (s *sourceStopper) StopSources(accountID int64, u store.Sources, reason string) int {
	s.stopped = append(s.stopped, fmt.Sprintf("%d %v %v: %s", accountID, u.Only, u.IDs, reason))
	return 0
}

// An account's card chooses the sources that it uses: every source, or
// those ticked; the change is logged, and what the account plays from
// another source stops (D114).
func TestAccountSourcesOnAccountsPage(t *testing.T) {
	ta := newTestAdmin(t)
	var logs bytes.Buffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	stopper := &sourceStopper{}
	ta.UseStreams(stopper)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	one, err := ta.st.AddSource(ctx, source.Settings{Name: "one", URL: "http://provider.example/one.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ta.st.AddSource(ctx, source.Settings{Name: "two", URL: "http://provider.example/two.m3u"}); err != nil {
		t.Fatal(err)
	}
	sam, err := ta.st.AddAccount(ctx, "Sam")
	if err != nil {
		t.Fatal(err)
	}

	rec := ta.do("GET", "/admin/accounts", nil, c)
	page := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(page, "<h3>Sources</h3>") ||
		!strings.Contains(page, `<input type="checkbox" name="every" value="1" checked> Every source, and those added later`) ||
		!strings.Contains(page, fmt.Sprintf(`<input type="checkbox" name="source" value="%d" checked> one <span class="muted">M3U playlist</span>`, one)) {
		t.Fatalf("the page: status %d", rec.Code)
	}

	path := fmt.Sprintf("/admin/accounts/%d/sources", sam.ID)
	rec = ta.do("POST", path, url.Values{"csrf": {csrf}, "source": {fmt.Sprint(one)}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sam uses one now. What its devices and players played from another source has stopped.") ||
		!strings.Contains(rec.Body.String(), " · 1 source") {
		t.Errorf("one source: status %d", rec.Code)
	}
	if got := ta.accountsByName()["Sam"].Sources; !got.Only || !slices.Equal(got.IDs, []int64{one}) {
		t.Errorf("Sam's sources %+v", got)
	}
	if want := fmt.Sprintf("%d true [%d]: the account no longer uses the stream's source", sam.ID, one); !slices.Equal(stopper.stopped, []string{want}) {
		t.Errorf("stopped %q, want %q", stopper.stopped, want)
	}
	if !strings.Contains(logs.String(), `msg="an account's sources set in the admin" account=Sam sources=one`) {
		t.Errorf("the log:\n%s", logs.String())
	}

	// Saving the same again logs and stops nothing.
	ta.do("POST", path, url.Values{"csrf": {csrf}, "source": {fmt.Sprint(one)}}, c, hx...)
	if len(stopper.stopped) != 1 || strings.Count(logs.String(), "an account's sources set") != 1 {
		t.Errorf("the same sources again: %q\n%s", stopper.stopped, logs.String())
	}

	rec = ta.do("POST", path, url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Tick one source at least, or choose every source.") {
		t.Errorf("none: status %d", rec.Code)
	}
	rec = ta.do("POST", path, url.Values{"csrf": {csrf}, "source": {"99"}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Choose among this server") {
		t.Errorf("no such source: status %d", rec.Code)
	}
	rec = ta.do("POST", path, url.Values{"csrf": {csrf}, "every": {"1"}, "source": {fmt.Sprint(one)}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Sam uses every source now.") {
		t.Errorf("every source: status %d", rec.Code)
	}
	if got := ta.accountsByName()["Sam"].Sources; got.Only {
		t.Errorf("Sam's sources %+v", got)
	}
	if len(stopper.stopped) != 1 {
		t.Errorf("every source stopped something: %q", stopper.stopped)
	}
}

// The API's accounts give their sources, and change them: null for every
// source, or a list of IDs (D114).
func TestAccountSourcesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	h := []string{csrfHeader, csrf}
	ctx := context.Background()
	one, err := ta.st.AddSource(ctx, source.Settings{Name: "one", URL: "http://provider.example/one.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	read := func(rec interface{ Result() *http.Response }) (int, map[string]json.RawMessage) {
		t.Helper()
		var out map[string]json.RawMessage
		res := rec.Result()
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("status %d: %v", res.StatusCode, err)
		}
		return res.StatusCode, out
	}
	path := fmt.Sprintf("accounts/%d", store.HouseholdAccount)
	if status, got := read(ta.apiDo("GET", path, "", c)); status != http.StatusOK || string(got["sources"]) != "null" {
		t.Errorf("at first: %d, sources %s", status, got["sources"])
	}
	status, got := read(ta.apiDo("PATCH", path, fmt.Sprintf(`{"sources": [%d]}`, one), c, h...))
	var ids []int64
	if err := json.Unmarshal(got["sources"], &ids); err != nil || status != http.StatusOK || !slices.Equal(ids, []int64{one}) {
		t.Errorf("one: %d, sources %s, %v", status, got["sources"], err)
	}
	for _, body := range []string{`{"sources": []}`, `{"sources": [99]}`, `{"sources": "one"}`, `{"sources": [0]}`} {
		apiErrorOf(t, ta.apiDo("PATCH", path, body, c, h...), http.StatusBadRequest, "bad_sources")
	}
	if status, got := read(ta.apiDo("PATCH", path, `{"sources": null}`, c, h...)); status != http.StatusOK || string(got["sources"]) != "null" {
		t.Errorf("every source: %d, sources %s", status, got["sources"])
	}
}
