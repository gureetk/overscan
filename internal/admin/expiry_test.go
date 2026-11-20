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
)

// expiresAt returns when the household's account ends.
func (ta *testAdmin) expiresAt() time.Time {
	ta.t.Helper()
	acct, err := ta.st.Account(context.Background(), householdAccount)
	if err != nil {
		ta.t.Fatal(err)
	}
	return acct.ExpiresAt
}

func TestExpiryOnTheSettingsPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}

	body := ta.do("GET", "/admin/settings", nil, c).Body.String()
	for _, want := range []string{`<section class="panel" id="household"`, "No end date", "The household's account never ends.", "Set an end date"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}

	// The account plays until the end of the day chosen, in the server's
	// time zone.
	rec := ta.do("POST", "/admin/settings/expiry", url.Values{"csrf": {csrf}, "date": {"2027-12-31"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "account plays until the end of 31 December 2027.") {
		t.Fatalf("set: status %d", rec.Code)
	}
	if got, want := ta.expiresAt(), time.Date(2028, 1, 1, 0, 0, 0, 0, time.Local); !got.Equal(want) {
		t.Errorf("ends at %v, want %v", got, want)
	}
	body = rec.Body.String()
	for _, want := range []string{"Until 31 December 2027", `value="2027-12-31"`, "Remove the end date"} {
		if !strings.Contains(body, want) {
			t.Errorf("after setting it, the page lacks %q", want)
		}
	}
	if home := ta.do("GET", "/admin/", nil, c).Body.String(); strings.Contains(home, "account has ended") {
		t.Error("the overview says the account ended before its end date")
	}

	for _, bad := range []string{"31/12/2027", "2027-02-30", "1999-12-31", "tomorrow"} {
		rec = ta.do("POST", "/admin/settings/expiry", url.Values{"csrf": {csrf}, "date": {bad}}, c, hx...)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Give the end date as a day") {
			t.Errorf("%q: status %d", bad, rec.Code)
		}
	}

	// A day that has passed ends the account at once, and the overview
	// says so.
	rec = ta.do("POST", "/admin/settings/expiry", url.Values{"csrf": {csrf}, "date": {"2026-11-01"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "account has ended: its boxes play nothing") ||
		!strings.Contains(rec.Body.String(), `<span class="badge bad">Ended</span>`) {
		t.Errorf("a day that has passed: status %d", rec.Code)
	}
	home := ta.do("GET", "/admin/", nil, c).Body.String()
	if !strings.Contains(home, "The household's account has ended") || !strings.Contains(home, "Its end date was 1 November 2026.") {
		t.Error("the overview does not say that the account has ended")
	}

	rec = ta.do("POST", "/admin/settings/expiry", url.Values{"csrf": {csrf}, "remove": {"1"}}, c, hx...)
	if rec.Code != http.StatusOK || !ta.expiresAt().IsZero() || !strings.Contains(rec.Body.String(), "Removed the end date") {
		t.Errorf("remove: status %d, ends %v", rec.Code, ta.expiresAt())
	}
	if rec = ta.do("POST", "/admin/settings/expiry", url.Values{"date": {"2027-12-31"}}, c); rec.Code != http.StatusForbidden || !ta.expiresAt().IsZero() {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}
}

func TestExpiryAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	h := []string{csrfHeader, csrf}
	read := func(rec interface{ Result() *http.Response }) (apiExpiry, int) {
		t.Helper()
		res := rec.Result()
		var body apiExpiry
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("status %d: %v", res.StatusCode, err)
		}
		return body, res.StatusCode
	}

	if got, status := read(ta.apiDo("GET", "expiry", "", c)); status != http.StatusOK || got.ExpiresAt != nil || got.Ended {
		t.Errorf("at first: %+v, status %d", got, status)
	}
	got, status := read(ta.apiDo("PUT", "expiry", `{"expires_at": "2028-01-01T00:00:00Z"}`, c, h...))
	if status != http.StatusOK || got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC)) || got.Ended {
		t.Errorf("set: %+v, status %d", got, status)
	}
	if got, _ = read(ta.apiDo("PUT", "expiry", `{"expires_at": "2026-11-13T10:00:00Z"}`, c, h...)); !got.Ended {
		t.Errorf("a time that has come: %+v", got)
	}
	for _, body := range []string{`{"expires_at": null}`, `{"expires_at": "2028-01-01"}`, `{"expires_at": 1800000000}`, `{}`} {
		apiErrorOf(t, ta.apiDo("PUT", "expiry", body, c, h...), http.StatusBadRequest, "bad_date")
	}
	apiErrorOf(t, ta.apiDo("PUT", "expiry", `{"expires_at": "2028-01-01T00:00:00Z"}`, c), http.StatusForbidden, "csrf")
	if got, status = read(ta.apiDo("DELETE", "expiry", "", c, h...)); status != http.StatusOK || got.ExpiresAt != nil || got.Ended || !ta.expiresAt().IsZero() {
		t.Errorf("remove: %+v, status %d", got, status)
	}
}
