// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// accountsByName returns the accounts, by name.
func (ta *testAdmin) accountsByName() map[string]store.Account {
	ta.t.Helper()
	list, err := ta.st.Accounts(context.Background())
	if err != nil {
		ta.t.Fatal(err)
	}
	out := map[string]store.Account{}
	for _, a := range list {
		out[a.Name] = a
	}
	return out
}

// The Accounts page lists the accounts and adds one; each account's own
// page gives it a login, a limit, an end date and a PIN, and removes it
// once no device is approved for it (D102).
func TestAccountsPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}

	rec := ta.do("GET", "/admin/accounts", nil, c)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `<a class="account-link" href="/admin/accounts/1">Household</a>`) ||
		!strings.Contains(rec.Body.String(), `href="/admin/accounts" aria-current="page"`) {
		t.Fatalf("the page: status %d", rec.Code)
	}
	// The household's own page has no way to remove it.
	rec = ta.do("GET", "/admin/accounts/1", nil, c)
	if page := rec.Body.String(); rec.Code != http.StatusOK || !strings.Contains(page, "<h1>Household</h1>") ||
		!strings.Contains(page, `href="/admin/accounts" aria-current="page"`) || strings.Contains(page, "Remove the account") {
		t.Errorf("the household's page: status %d", rec.Code)
	}
	for _, path := range []string{"/admin/accounts/99", "/admin/accounts/one"} {
		if rec := ta.do("GET", path, nil, c); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}

	// A new account's page opens, saying that it was added.
	rec = ta.do("POST", "/admin/accounts", url.Values{"csrf": {csrf}, "name": {"Sam"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Added the account Sam.") || !strings.Contains(rec.Body.String(), "<h1>Sam</h1>") {
		t.Fatalf("add: status %d", rec.Code)
	}
	sam, ok := ta.accountsByName()["Sam"]
	if !ok {
		t.Fatal("no account Sam")
	}
	path := fmt.Sprintf("/admin/accounts/%d", sam.ID)
	if got := rec.Header().Get("HX-Push-Url"); got != path {
		t.Errorf("the address after adding: %q, want %q", got, path)
	}
	if rec := ta.do("POST", "/admin/accounts", url.Values{"csrf": {csrf}, "name": {"sam"}}, c, hx...); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), `value="sam"`) {
		t.Errorf("a second Sam: status %d", rec.Code)
	}
	// Without htmx, the browser goes to the new page, and back to it after
	// each change.
	rec = ta.do("POST", "/admin/accounts", url.Values{"csrf": {csrf}, "name": {"Kim"}}, c)
	kim := ta.accountsByName()["Kim"]
	if want := fmt.Sprintf("/admin/accounts/%d", kim.ID); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Errorf("add without htmx: status %d, to %q, want %q", rec.Code, rec.Header().Get("Location"), want)
	}
	rec = ta.do("POST", path+"/streams", url.Values{"csrf": {csrf}, "streams": {"3"}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != path {
		t.Errorf("a change without htmx: status %d, to %q", rec.Code, rec.Header().Get("Location"))
	}
	path += "/"

	for _, step := range []struct {
		action string
		form   url.Values
		status int
		says   string
	}{
		{"login", url.Values{"username": {"sam"}, "password": {"short"}}, http.StatusBadRequest, "at least 8 characters"},
		{"login", url.Values{"username": {"sam"}, "password": {"has spaces in it"}}, http.StatusBadRequest, "letters without accents"},
		{"login", url.Values{"username": {"sam"}, "password": {"correct-horse"}}, http.StatusOK, "Saved the login of Sam: sam."},
		{"login", url.Values{"username": {"sam two"}}, http.StatusBadRequest, "A username has"},
		{"streams", url.Values{"streams": {"2"}}, http.StatusOK, "Sam plays 2 streams at once now."},
		{"streams", url.Values{"streams": {"0"}}, http.StatusOK, "Sam plays any number of streams at once now."},
		{"streams", url.Values{"streams": {"101"}}, http.StatusBadRequest, "streams at once"},
		{"expiry", url.Values{"date": {"2026-12-31"}}, http.StatusOK, "Sam plays until the end of 31 December 2026."},
		{"expiry", url.Values{"date": {"31/12/2026"}}, http.StatusBadRequest, "as a day"},
		{"pin", url.Values{"pin": {"12"}}, http.StatusBadRequest, "4 to 8 digits"},
		{"pin", url.Values{"pin": {"2468"}}, http.StatusOK, "Set the parental PIN of Sam."},
		{"rename", url.Values{"name": {"Sam and Kim"}}, http.StatusOK, "Renamed the account Sam and Kim."},
	} {
		step.form.Set("csrf", csrf)
		rec := ta.do("POST", path+step.action, step.form, c, hx...)
		if rec.Code != step.status || !strings.Contains(rec.Body.String(), step.says) {
			t.Errorf("%s %v: status %d, want %d saying %q", step.action, step.form, rec.Code, step.status, step.says)
		}
	}
	got := ta.accountsByName()["Sam and Kim"]
	if got.Username != "sam" || got.PasswordHash == "" || got.PINHash == "" || got.MaxStreams != 0 || got.ExpiresAt.IsZero() {
		t.Errorf("after the changes: %+v", got)
	}
	if ok, err := CheckPassword(context.Background(), got.PasswordHash, "correct-horse"); err != nil || !ok {
		t.Errorf("the login's password: %v, %v", ok, err)
	}

	// The household's login cannot take Sam's username.
	rec = ta.do("POST", "/admin/accounts/1/login", url.Values{"csrf": {csrf}, "username": {"SAM"}, "password": {"another-one"}}, c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Another account has this username.") {
		t.Errorf("a taken username: status %d", rec.Code)
	}

	// An account with a device cannot be removed; once it has none, it can.
	ta.waitingDevices()
	if _, err := ta.approveCode(context.Background(), boxCode, got.ID, "Den"); err != nil {
		t.Fatal(err)
	}
	rec = ta.do("POST", path+"remove", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "move them to another account") {
		t.Errorf("remove with a device: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/devices/"+boxMAC+"/account", url.Values{"csrf": {csrf}, "account": {"1"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Den ("+boxMAC+") belongs to Household now") {
		t.Errorf("move the box: status %d", rec.Code)
	}
	if d := ta.device(boxMAC); d.AccountID != householdAccount {
		t.Errorf("the box's account: %d", d.AccountID)
	}
	// Once it is removed, the Accounts page says so.
	rec = ta.do("POST", path+"remove", url.Values{"csrf": {csrf}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Removed the account Sam and Kim") ||
		rec.Header().Get("HX-Push-Url") != "/admin/accounts" {
		t.Errorf("remove: status %d, to %q", rec.Code, rec.Header().Get("HX-Push-Url"))
	}
	if _, ok := ta.accountsByName()["Sam and Kim"]; ok {
		t.Error("the account is still there")
	}
	// A form of its page, open in another tab, finds it gone.
	rec = ta.do("POST", path+"pin", url.Values{"csrf": {csrf}, "pin": {"1357"}}, c, hx...)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "There is no such account.") ||
		!strings.Contains(rec.Body.String(), "<h1>Accounts</h1>") {
		t.Errorf("a removed account's form: status %d", rec.Code)
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/accounts/%d/remove", kim.ID), url.Values{"csrf": {csrf}}, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/accounts" {
		t.Errorf("remove without htmx: status %d, to %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := ta.do("POST", "/admin/accounts/1/remove", url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusConflict {
		t.Errorf("remove the household's: status %d", rec.Code)
	}
	if rec := ta.do("POST", "/admin/accounts", url.Values{"name": {"Kim"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the CSRF token: status %d", rec.Code)
	}
}

// What came of a form shows once, in the form's own part of an account's
// page, or at the top for a form whose part the page does not show.
func TestAccountPageSaysWhereItChanged(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	part := func(page, id string) string {
		start := strings.Index(page, `<section class="panel" id="`+id+`"`)
		if start < 0 {
			return ""
		}
		return page[start : start+strings.Index(page[start:], "</section>")]
	}
	for _, step := range []struct {
		action string
		form   url.Values
		status int
		part   string // "" for the top, before the first part
		says   string
	}{
		{"pin", url.Values{"pin": {"2468"}}, http.StatusOK, "account-pin", "Set the parental PIN of Household."},
		{"pin", url.Values{"pin": {"12"}}, http.StatusBadRequest, "account-pin", "A PIN has 4 to 8 digits."},
		{"streams", url.Values{"streams": {"2"}}, http.StatusOK, "account-watching", "Household plays 2 streams at once now."},
		{"expiry", url.Values{"date": {"2027-12-31"}}, http.StatusOK, "account-end", "Household plays until the end of 31 December 2027."},
		{"login", url.Values{"username": {"home"}, "password": {"short"}}, http.StatusBadRequest, "account-login", "at least 8 characters"},
		{"playlist", url.Values{"on": {"1"}}, http.StatusOK, "account-playlist", "can read its playlist and guide"},
		{"rename", url.Values{"name": {"Home"}}, http.StatusOK, "", "Renamed the account Home."},
		// The page has no part for these: there are no themes, and the
		// household's account cannot be removed.
		{"theme", url.Values{"theme": {"7"}}, http.StatusNotFound, "", "There is no such theme."},
		{"remove", url.Values{}, http.StatusConflict, "", "The household&#39;s account cannot be removed."},
	} {
		step.form.Set("csrf", csrf)
		rec := ta.do("POST", "/admin/accounts/1/"+step.action, step.form, c, "HX-Request", "true")
		page := rec.Body.String()
		where := page[:max(strings.Index(page, `<section class="panel"`), 0)]
		if step.part != "" {
			where = part(page, step.part)
		}
		if rec.Code != step.status || !strings.Contains(where, step.says) || strings.Count(page, step.says) != 1 {
			t.Errorf("%s %v: status %d, want %d saying %q once, in %q", step.action, step.form, rec.Code, step.status, step.says, step.part)
		}
	}
}

// A login's password is never the administrator's or a source's.
func TestLoginPasswordsOfTheirOwn(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct-horse-battery")
	c, csrf := ta.signIn("correct-horse-battery")
	ctx := context.Background()
	if _, err := ta.st.AddSource(ctx, source.Settings{Name: "main", Type: source.TypeXtream, URL: "http://provider.example",
		Username: "fictional", Password: "fictional-pass-1"}); err != nil {
		t.Fatal(err)
	}
	for _, pw := range []string{"correct-horse-battery", "fictional-pass-1"} {
		rec := ta.do("POST", "/admin/accounts/1/login", url.Values{"csrf": {csrf}, "username": {"home1"}, "password": {pw}}, c, "HX-Request", "true")
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Give the login a password of its own") {
			t.Errorf("password %q: status %d", pw, rec.Code)
		}
	}
	if a := ta.accountsByName()["Household"]; a.Username != "" {
		t.Errorf("a login was set: %+v", a)
	}
}

func TestAccountsAPI(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	h := []string{csrfHeader, csrf}
	decode := func(rec interface{ Result() *http.Response }, v any) int {
		t.Helper()
		res := rec.Result()
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatalf("status %d: %v", res.StatusCode, err)
		}
		return res.StatusCode
	}

	var list struct {
		Accounts []accountJSON `json:"accounts"`
	}
	if status := decode(ta.apiDo("GET", "accounts", "", c), &list); status != http.StatusOK || len(list.Accounts) != 1 ||
		list.Accounts[0].Name != "Household" || list.Accounts[0].Username != nil || list.Accounts[0].Outputs.M3U ||
		list.Accounts[0].Outputs.Xtream || list.Accounts[0].Outputs.HLS == nil || *list.Accounts[0].Outputs.HLS {
		t.Fatalf("list: status %d, %+v", status, list)
	}

	var alex accountJSON
	if status := decode(ta.apiDo("POST", "accounts", `{"name": "Alex"}`, c, h...), &alex); status != http.StatusCreated ||
		alex.Name != "Alex" || alex.Profiles != 1 || alex.MaxStreams != 0 {
		t.Fatalf("add: status %d, %+v", status, alex)
	}
	id := fmt.Sprint(alex.ID)
	apiErrorOf(t, ta.apiDo("POST", "accounts", `{"name": ""}`, c, h...), http.StatusBadRequest, "bad_name")

	var changed accountJSON
	if status := decode(ta.apiDo("PATCH", "accounts/"+id, `{"max_streams": 2, "outputs": {"m3u": true}}`, c, h...), &changed); status != http.StatusOK ||
		changed.MaxStreams != 2 || !changed.Outputs.M3U || changed.Outputs.Xtream {
		t.Errorf("change: status %d, %+v", status, changed)
	}
	// HLS for players (D153) is set with the outputs, and stays as it is
	// when a change leaves it out.
	for _, tc := range []struct {
		body string
		hls  bool
	}{
		{`{"outputs": {"m3u": true, "hls": true}}`, true},
		{`{"outputs": {"m3u": true, "xtream": false}}`, true},
		{`{"outputs": {"m3u": true, "hls": false}}`, false},
	} {
		if status := decode(ta.apiDo("PATCH", "accounts/"+id, tc.body, c, h...), &changed); status != http.StatusOK ||
			!changed.Outputs.M3U || changed.Outputs.HLS == nil || *changed.Outputs.HLS != tc.hls {
			t.Errorf("%s: status %d, %+v", tc.body, status, changed.Outputs)
		}
	}
	apiErrorOf(t, ta.apiDo("PATCH", "accounts/"+id, `{"max_streams": -1}`, c, h...), http.StatusBadRequest, "bad_streams")
	apiErrorOf(t, ta.apiDo("PATCH", "accounts/"+id, `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", "accounts/99", `{"name": "Nobody"}`, c, h...), http.StatusNotFound, "no_such_account")

	if status := decode(ta.apiDo("PUT", "accounts/"+id+"/login", `{"username": "alex", "password": "a-password-1"}`, c, h...), &changed); status != http.StatusOK ||
		changed.Username == nil || *changed.Username != "alex" {
		t.Errorf("login: status %d, %+v", status, changed)
	}
	apiErrorOf(t, ta.apiDo("PUT", "accounts/1/login", `{"username": "Alex", "password": "b-password-2"}`, c, h...), http.StatusConflict, "username_taken")
	apiErrorOf(t, ta.apiDo("PUT", "accounts/1/login", `{"username": "household"}`, c, h...), http.StatusBadRequest, "bad_password")

	apiErrorOf(t, ta.apiDo("GET", "accounts/"+id+"/token", "", c), http.StatusNotFound, "no_token")
	var token struct {
		Token string `json:"token"`
	}
	if status := decode(ta.apiDo("POST", "accounts/"+id+"/token", "", c, h...), &token); status != http.StatusOK || len(token.Token) != 43 {
		t.Fatalf("token: status %d, %+v", status, token)
	}
	var again struct {
		Token string `json:"token"`
	}
	if status := decode(ta.apiDo("GET", "accounts/"+id+"/token", "", c), &again); status != http.StatusOK || again != token {
		t.Errorf("token again: status %d, %+v", status, again)
	}
	// The list never shows a token or a password.
	rec := ta.apiDo("GET", "accounts", "", c)
	if strings.Contains(rec.Body.String(), token.Token) || strings.Contains(rec.Body.String(), "argon2") {
		t.Error("the list shows a secret")
	}

	if status := decode(ta.apiDo("PUT", "accounts/"+id+"/expiry", `{"expires_at": "2026-11-12T00:00:00Z"}`, c, h...), &changed); status != http.StatusOK ||
		!changed.Ended || changed.ExpiresAt == nil {
		t.Errorf("expiry: status %d, %+v", status, changed)
	}
	if status := decode(ta.apiDo("DELETE", "accounts/"+id+"/expiry", "", c, h...), &changed); status != http.StatusOK ||
		changed.Ended || changed.ExpiresAt != nil {
		t.Errorf("remove expiry: status %d, %+v", status, changed)
	}
	if status := decode(ta.apiDo("PUT", "accounts/"+id+"/pin", `{"pin": "1357"}`, c, h...), &changed); status != http.StatusOK || !changed.PIN {
		t.Errorf("pin: status %d, %+v", status, changed)
	}
	apiErrorOf(t, ta.apiDo("PUT", "accounts/"+id+"/pin", `{"pin": "13"}`, c, h...), http.StatusBadRequest, "bad_pin")
	if status := decode(ta.apiDo("DELETE", "accounts/"+id+"/pin", "", c, h...), &changed); status != http.StatusOK || changed.PIN {
		t.Errorf("remove pin: status %d, %+v", status, changed)
	}
	if status := decode(ta.apiDo("DELETE", "accounts/"+id+"/login", "", c, h...), &changed); status != http.StatusOK || changed.Username != nil {
		t.Errorf("remove login: status %d, %+v", status, changed)
	}

	// Devices move between accounts, and play as the new account's first
	// profile.
	ta.waitingDevices()
	if _, err := ta.approveCode(context.Background(), boxCode, householdAccount, "Den"); err != nil {
		t.Fatal(err)
	}
	var dev struct {
		AccountID *int64 `json:"account_id"`
		ProfileID *int64 `json:"profile_id"`
	}
	if status := decode(ta.apiDo("PATCH", "devices/"+boxMAC, `{"account_id": `+id+`}`, c, h...), &dev); status != http.StatusOK ||
		dev.AccountID == nil || *dev.AccountID != alex.ID {
		t.Errorf("move: status %d, %+v", status, dev)
	}
	ps, _ := ta.st.Profiles(context.Background(), alex.ID)
	if dev.ProfileID == nil || *dev.ProfileID != ps[0].ID {
		t.Errorf("the moved box plays as %v, want %d", dev.ProfileID, ps[0].ID)
	}
	// A profile of another account is not the box's to play as.
	household, _ := ta.st.Profiles(context.Background(), householdAccount)
	apiErrorOf(t, ta.apiDo("PATCH", "devices/"+boxMAC, fmt.Sprintf(`{"profile_id": %d}`, household[0].ID), c, h...), http.StatusConflict, "other_account")
	apiErrorOf(t, ta.apiDo("PATCH", "devices/"+clientMAC, `{"account_id": 1}`, c, h...), http.StatusConflict, "not_approved")
	apiErrorOf(t, ta.apiDo("DELETE", "accounts/"+id, "", c, h...), http.StatusConflict, "has_devices")
	apiErrorOf(t, ta.apiDo("DELETE", "accounts/1", "", c, h...), http.StatusConflict, "household")

	// Profiles of another account.
	var p apiProfile
	if status := decode(ta.apiDo("POST", "profiles", `{"name": "Guest", "account_id": `+id+`}`, c, h...), &p); status != http.StatusCreated || p.AccountID != alex.ID {
		t.Errorf("add a profile to Alex: status %d, %+v", status, p)
	}
	var profiles struct {
		Profiles []apiProfile `json:"profiles"`
	}
	if status := decode(ta.apiDo("GET", "profiles?account_id="+id, "", c), &profiles); status != http.StatusOK || len(profiles.Profiles) != 2 {
		t.Errorf("Alex's profiles: status %d, %+v", status, profiles)
	}
}

// The overview names each stream's account once there is more than one.
func TestOverviewNamesAccounts(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withActivity()
	d := ta.device("00:1A:79:00:00:01")
	ta.UseStreams(fakeStreams{{Kind: hub.PlaysChannel, DeviceID: d.ID, AccountID: householdAccount, EntryID: 1,
		SourceID: ta.sourceID("main"), Source: "main", Channel: "Alpha", Started: ta.clock.Add(-time.Minute), Playing: true}})
	rec := ta.do("GET", "/admin/", nil, c)
	if strings.Contains(rec.Body.String(), " · Household") {
		t.Error("a household with one account sees its name on each stream")
	}
	if _, err := ta.st.AddAccount(context.Background(), "Sam"); err != nil {
		t.Fatal(err)
	}
	rec = ta.do("GET", "/admin/", nil, c)
	if !strings.Contains(rec.Body.String(), "Household</div>") {
		t.Errorf("the overview does not name the household's account:\n%s", rec.Body)
	}
}

func TestLogins(t *testing.T) {
	ta := newTestAdmin(t)
	ctx := context.Background()
	l := NewLogins(ta.st)
	clock := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }
	hash, err := HashPassword(ctx, "correct-horse", cheap)
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetAccountLogin(ctx, householdAccount, "alex", hash); err != nil {
		t.Fatal(err)
	}
	const addr = "192.0.2.20"
	if a, err := l.Check(ctx, "ALEX", "correct-horse", addr); err != nil || a.ID != householdAccount {
		t.Fatalf("a right login: %+v, %v", a, err)
	}
	// Remembered: a second check needs no hash, and still names the account.
	if len(l.known) != 1 {
		t.Errorf("remembered logins: %d", len(l.known))
	}
	if _, err := l.Check(ctx, "alex", "correct-horse", addr); err != nil {
		t.Fatalf("again: %v", err)
	}
	// A new password makes the remembered login useless.
	other, _ := HashPassword(ctx, "another-horse", cheap)
	if err := ta.st.SetAccountLogin(ctx, householdAccount, "alex", other); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Check(ctx, "alex", "correct-horse", addr); !errors.Is(err, ErrLogin) {
		t.Errorf("the old password: %v", err)
	}
	if _, err := l.Check(ctx, "nobody", "correct-horse", addr); !errors.Is(err, ErrLogin) {
		t.Errorf("a username of no account: %v", err)
	}
	for i := 0; i < maxFailures-2; i++ {
		if _, err := l.Check(ctx, "alex", "wrong-horse", addr); !errors.Is(err, ErrLogin) {
			t.Fatalf("wrong %d: %v", i, err)
		}
	}
	// Five wrong ones: the address waits, even with the right password.
	var wait *LoginWait
	if _, err := l.Check(ctx, "alex", "another-horse", addr); !errors.As(err, &wait) || wait.For <= 0 {
		t.Errorf("after five wrong logins: %v", err)
	}
	// Another address does not.
	if _, err := l.Check(ctx, "alex", "another-horse", "192.0.2.21"); err != nil {
		t.Errorf("another address: %v", err)
	}
	clock = clock.Add(failWindow + time.Second)
	if _, err := l.Check(ctx, "alex", "another-horse", addr); err != nil {
		t.Errorf("after the wait: %v", err)
	}

	// Tokens.
	token, err := ta.st.NewAccountToken(ctx, householdAccount)
	if err != nil {
		t.Fatal(err)
	}
	if a, err := l.Token(ctx, token); err != nil || a.ID != householdAccount {
		t.Errorf("a token: %+v, %v", a, err)
	}
	if _, err := l.Token(ctx, "not-a-token"); !errors.Is(err, ErrLogin) {
		t.Errorf("a wrong token: %v", err)
	}
}

// The Accounts page turns the outputs on, and an account's playlist, which
// makes it a token and shows its addresses (D103).
func TestOutputsOnAccountsPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	ta.UseSupport(Support{Listen: ":8080"})

	rec := ta.do("GET", "/admin/accounts", nil, c)
	if !strings.Contains(rec.Body.String(), `id="outputs-on" name="on" value="1" aria-describedby`) {
		t.Error("the outputs' switch is not off at first")
	}
	rec = ta.do("POST", "/admin/accounts/outputs/on", url.Values{"csrf": {csrf}, "on": {"1"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "The outputs are on") {
		t.Errorf("turn on: status %d", rec.Code)
	}
	if on, _ := ta.st.Outputs(ctx); !on {
		t.Error("the outputs are off")
	}
	rec = ta.do("POST", "/admin/accounts/1/playlist", url.Values{"csrf": {csrf}, "on": {"1"}}, c, hx...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "can read its playlist and guide") {
		t.Errorf("the household's playlist: status %d", rec.Code)
	}
	token, err := ta.st.AccountToken(ctx, householdAccount)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rec.Body.String(), "http://example.com:8080/playlist/"+token+".m3u") ||
		!strings.Contains(rec.Body.String(), "http://example.com:8080/epg/"+token+".xml.gz") {
		t.Errorf("the addresses are not shown:\n%s", rec.Body)
	}
	// New addresses stop the old ones.
	rec = ta.do("POST", "/admin/accounts/1/token", url.Values{"csrf": {csrf}}, c, hx...)
	if newer, _ := ta.st.AccountToken(ctx, householdAccount); rec.Code != http.StatusOK || newer == token || strings.Contains(rec.Body.String(), token) {
		t.Errorf("new addresses: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/accounts/1/playlist", url.Values{"csrf": {csrf}}, c, hx...)
	if a, _ := ta.st.Account(ctx, householdAccount); rec.Code != http.StatusOK || a.Outputs.M3U || strings.Contains(rec.Body.String(), "/playlist/") {
		t.Errorf("the playlist off: status %d, %+v", rec.Code, a.Outputs)
	}
	rec = ta.do("POST", "/admin/accounts/outputs/anywhere", url.Values{"csrf": {csrf}, "on": {"1"}}, c, hx...)
	if on, _ := ta.st.OutputsAnywhere(ctx); rec.Code != http.StatusOK || !on {
		t.Errorf("anywhere: status %d, %v", rec.Code, on)
	}
	// And through the API.
	h := []string{csrfHeader, csrf}
	rec = ta.apiDo("PATCH", "settings", `{"outputs": false, "outputs_anywhere": false}`, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"outputs": false`) || !strings.Contains(rec.Body.String(), `"outputs_anywhere": false`) {
		t.Errorf("settings: status %d, %s", rec.Code, rec.Body)
	}
}

// stopRecorder is a hub that records which players the admin stops, of
// three: the household's through its playlist, on the LAN, and through its
// login, from outside; and another account's through its login.
type stopRecorder struct {
	fakeStreams
	stopped []string // each call: its reason, then the players it matched
}

func (s *stopRecorder) StopPlayers(match func(hub.Player) bool, reason string) int {
	players := []hub.Player{
		{AccountID: householdAccount, Output: "m3u", Address: "192.168.1.20"},
		{AccountID: householdAccount, Output: "xtream", Address: "203.0.113.7"},
		{AccountID: householdAccount + 1, Output: "xtream", Address: "192.168.1.21"},
	}
	var hit []string
	for i, p := range players {
		if match(p) {
			hit = append(hit, fmt.Sprint(i))
		}
	}
	s.stopped = append(s.stopped, reason+": "+strings.Join(hit, ","))
	return len(hit)
}

// StopCut records that the admin ends the HLS of an account's players.
func (s *stopRecorder) StopCut(accountID int64, reason string) int {
	s.stopped = append(s.stopped, fmt.Sprintf("%s: HLS of %d", reason, accountID))
	return 0
}

// An account's page turns its HLS for players on and off (D153); turning
// it off ends the HLS that its players play, and nothing else.
func TestHLSOnAccountPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	ctx := context.Background()
	hubs := &stopRecorder{}
	ta.UseStreams(hubs)
	post := func(form url.Values) string {
		t.Helper()
		form.Set("csrf", csrf)
		rec := ta.do("POST", "/admin/accounts/1/hls", form, c, "HX-Request", "true")
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d\n%s", rec.Code, rec.Body)
		}
		return rec.Body.String()
	}
	rec := ta.do("GET", "/admin/accounts/1", nil, c)
	if page := rec.Body.String(); !strings.Contains(page, `<label for="hls">HLS for players</label>`) ||
		!strings.Contains(page, "Off: its players get each channel as one stream, as before.") ||
		strings.Contains(page, `id="hls" name="on" value="1" checked`) {
		t.Errorf("the switch while off:\n%s", page)
	}
	page := post(url.Values{"on": {"1"}})
	if a, _ := ta.st.Account(ctx, householdAccount); !a.Outputs.HLS || a.Outputs.M3U || a.Outputs.Xtream ||
		!strings.Contains(page, "players that ask for HLS get it, from the next channel they start") ||
		!strings.Contains(page, "share its one connection to its source, even while the shared tuner is off") {
		t.Errorf("on: %+v\n%s", a.Outputs, page)
	}
	if len(hubs.stopped) != 0 {
		t.Errorf("turning it on stopped: %v", hubs.stopped)
	}
	post(url.Values{})
	if a, _ := ta.st.Account(ctx, householdAccount); a.Outputs.HLS {
		t.Errorf("off: %+v", a.Outputs)
	}
	if want := "the account's HLS for players was turned off: HLS of 1"; strings.Join(hubs.stopped, "\n") != want {
		t.Errorf("stopped: %v", hubs.stopped)
	}
}

// The Accounts page lets an account's players sign in with its login
// (D104), and shows them where; what a switch, a login or a token let a
// player open stops when it no longer would.
func TestXtreamOnAccountsPage(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	ta.UseSupport(Support{Listen: ":8080"})
	hubs := &stopRecorder{}
	ta.UseStreams(hubs)
	post := func(path string, form url.Values) string {
		t.Helper()
		form.Set("csrf", csrf)
		rec := ta.do("POST", path, form, c, hx...)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d\n%s", path, rec.Code, rec.Body)
		}
		return rec.Body.String()
	}

	post("/admin/accounts/outputs/on", url.Values{"on": {"1"}})
	page := post("/admin/accounts/1/xtream", url.Values{"on": {"1"}})
	if a, _ := ta.st.Account(ctx, householdAccount); !a.Outputs.Xtream || !strings.Contains(page, "once it has one") ||
		!strings.Contains(page, "Give it a login below first.") {
		t.Errorf("the switch without a login: %+v", a.Outputs)
	}
	page = post("/admin/accounts/1/login", url.Values{"username": {"alex"}, "password": {"correct-horse-battery"}})
	if !strings.Contains(page, `<dt>Server</dt><dd class="mono">http://example.com:8080</dd>`) ||
		!strings.Contains(page, `value="alex"`) || strings.Contains(page, "below first") {
		t.Errorf("the address is not shown:\n%s", page)
	}
	if len(hubs.stopped) != 0 {
		t.Errorf("a first login stopped players: %v", hubs.stopped)
	}
	post("/admin/accounts/1/login", url.Values{"username": {"alex"}, "password": {"another-horse-battery"}})
	post("/admin/accounts/outputs/anywhere", url.Values{"on": {"1"}})
	post("/admin/accounts/outputs/anywhere", url.Values{})
	post("/admin/accounts/1/playlist", url.Values{"on": {"1"}})
	post("/admin/accounts/1/token", url.Values{})
	post("/admin/accounts/1/xtream", url.Values{})
	post("/admin/accounts/1/remove-login", url.Values{})
	post("/admin/accounts/1/expiry", url.Values{"date": {"2001-01-01"}})
	post("/admin/accounts/outputs/on", url.Values{})
	want := []string{
		"the account's login changed: 1",
		"the outputs answer players on the server's own networks only: 1",
		"the account's playlist has new addresses: 0",
		"the account's Xtream login was turned off: 1",
		"the account's login was removed: 1",
		"the account has ended: 0,1",
		"the outputs were turned off: 0,1,2",
	}
	if strings.Join(hubs.stopped, "\n") != strings.Join(want, "\n") {
		t.Errorf("stopped:\n%s\nwant:\n%s", strings.Join(hubs.stopped, "\n"), strings.Join(want, "\n"))
	}
	// Through the API, an account's outputs turned off stop its players.
	hubs.stopped = nil
	rec := ta.apiDo("PATCH", "accounts/1", `{"outputs": {"m3u": false, "xtream": false}}`, c, csrfHeader, csrf)
	if rec.Code != http.StatusOK || len(hubs.stopped) != 1 || hubs.stopped[0] != "the account's playlist was turned off: 0" {
		t.Errorf("the API: status %d, stopped %v", rec.Code, hubs.stopped)
	}
}
