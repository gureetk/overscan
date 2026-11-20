// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/xtream"
	"overscan.gureet.ca/internal/catalog"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// fakeSources stands in for the refresher.
type fakeSources struct {
	mu       sync.Mutex
	reloads  int
	tested   []source.Settings
	testErr  error
	account  *xtream.Account // what a test finds about an Xtream account
	refreshd []int64
	readErr  error
}

func (f *fakeSources) Test(_ context.Context, s source.Settings) (catalog.Tested, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tested = append(f.tested, s)
	if f.testErr != nil {
		return catalog.Tested{Account: f.account}, f.testErr
	}
	if s.Type == source.TypeJellyfin {
		return catalog.Tested{Server: &catalog.Server{Name: "Den", Version: "10.11.2"}, Libraries: []string{"Films", "Shows", "Home"}}, nil
	}
	if s.Type == source.TypePlex {
		return catalog.Tested{Server: &catalog.Server{Name: "Lounge", Version: "1.42.2.10156"}, Libraries: []string{"Films"}}, nil
	}
	if s.Type == source.TypeFolder {
		return catalog.Tested{Folder: path.Base(s.URL), Films: 1204, Series: 1}, nil
	}
	return catalog.Tested{Channels: 5471, Skipped: 3, Account: f.account}, nil
}

func (f *fakeSources) RefreshNow(_ context.Context, id int64) (store.CatalogChanges, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshd = append(f.refreshd, id)
	if f.readErr != nil {
		return store.CatalogChanges{}, f.readErr
	}
	return store.CatalogChanges{Added: 1, Updated: 3, Missing: 2}, nil
}

func (f *fakeSources) Reload() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reloads++
}

func (f *fakeSources) reloaded() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reloads
}

// withSources gives the test admin the lineup of withLineup and a fake
// reader of sources.
func (ta *testAdmin) withSources() (*http.Cookie, string, *fakeSources) {
	ta.t.Helper()
	c, h := ta.withLineup()
	f := &fakeSources{}
	ta.UseSources(f)
	return c, h[1], f
}

// sourceID returns the ID of the source with the given name.
func (ta *testAdmin) sourceID(name string) int64 {
	ta.t.Helper()
	srcs, err := ta.st.Sources(context.Background())
	if err != nil {
		ta.t.Fatal(err)
	}
	for _, s := range srcs {
		if s.Name == name {
			return s.ID
		}
	}
	ta.t.Fatalf("no source %q", name)
	return 0
}

// sourceForm is a form from the sources page.
func sourceFormValues(csrf string, kv ...string) url.Values {
	v := url.Values{"csrf": {csrf}, "max_connections": {"1"}, "refresh": {"21600"}, "numbers": {"playlist"}}
	for i := 0; i+1 < len(kv); i += 2 {
		v.Set(kv[i], kv[i+1])
	}
	return v
}

const secretAddress = "http://provider.example:8080/get.php?username=u&password=s3cret"

func TestSourcesPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, _, _ := ta.withSources()
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the page", body, `<a href="/admin/sources" aria-current="page" title="Sources">`, `<h2 id="source-1-name">main</h2>`,
		"provider.example", "<dd>4</dd>", "Every 6 hours", "In playlist order", "Add a source", "Read now", "Turn off")
	if strings.Contains(body, "main.m3u") {
		t.Error("the page shows the saved address")
	}

	// A source turned off, with an address saved under another key and a
	// failed read.
	id := ta.sourceID("main")
	if err := ta.st.SetSourceEnabled(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.st.AddSource(context.Background(), source.Settings{Name: "other", URL: "/srv/other.m3u", MaxConnections: 1}); err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetRefreshResult(context.Background(), ta.sourceID("other"), ta.clock, "the server answered 403 Forbidden"); err != nil {
		t.Fatal(err)
	}
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the page", body, `<span class="state-line"><i class="dot off" aria-hidden="true"></i>Off</span>`, "Turn on", "file other.m3u",
		"The last read failed: the server answered 403 Forbidden")
}

func TestAddASource(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	hx := []string{"HX-Request", "true"}

	// A test reads the address and saves nothing.
	rec := ta.do("POST", "/admin/sources", sourceFormValues(csrf, "name", "second", "url", secretAddress, "test", "1"), c, hx...)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || len(f.tested) != 1 || f.tested[0].URL != secretAddress || f.tested[0].Name != "second" {
		t.Fatalf("test: status %d, tested %+v", rec.Code, f.tested)
	}
	mustContain(t, "the test", body, "The address works: it lists 5,471 channels.", "It would leave out 3 other entries", "Nothing has been saved yet.",
		`value="second"`)
	if srcs, _ := ta.st.Sources(context.Background()); len(srcs) != 1 {
		t.Errorf("a test saved a source: %+v", srcs)
	}
	f.testErr = errors.New("the server answered 401 Unauthorized")
	rec = ta.do("POST", "/admin/sources", sourceFormValues(csrf, "name", "second", "url", secretAddress, "test", "1"), c, hx...)
	mustContain(t, "a failed test", rec.Body.String(), "The source could not be read: the server answered 401 Unauthorized.")

	// Adding saves it, and the reader takes it.
	rec = ta.do("POST", "/admin/sources", sourceFormValues(csrf, "name", "second", "url", secretAddress, "max_connections", "2",
		"refresh", "3600", "user_agent", "TestPlayer/1.0"), c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/sources" || f.reloaded() != 1 {
		t.Fatalf("add: status %d, reloads %d", rec.Code, f.reloaded())
	}
	settings, err := ta.st.SourceSettings(context.Background(), ta.sourceID("second"))
	if err != nil || settings.URL != secretAddress || settings.MaxConnections != 2 || settings.Refresh.Hours() != 1 || settings.UserAgent != "TestPlayer/1.0" {
		t.Errorf("saved %+v, %v", settings, err)
	}

	// Mistakes come back with the form as it was sent.
	for _, tc := range []struct {
		form   url.Values
		status int
		text   string
	}{
		{sourceFormValues(csrf, "name", "Bad Name", "url", "/srv/x.m3u"), http.StatusBadRequest, "A source&#39;s name is 1 to 32"},
		{sourceFormValues(csrf, "name", "second", "url", "/srv/x.m3u"), http.StatusConflict, "Another source has that name."},
		{sourceFormValues(csrf, "name", "third", "url", "ftp://provider.example/s3cret"), http.StatusBadRequest, "The address must be"},
		{sourceFormValues(csrf, "name", "third", "url", "/srv/x.m3u", "refresh", "60"), http.StatusBadRequest, "is read again every"},
		{sourceFormValues(csrf, "name", "third", "url", "/srv/x.m3u", "max_connections", "many"), http.StatusBadRequest, "Streams at once"},
	} {
		rec := ta.do("POST", "/admin/sources", tc.form, c, hx...)
		if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.text) ||
			!strings.Contains(rec.Body.String(), `value="`+tc.form.Get("name")+`"`) {
			t.Errorf("%v: status %d, want %d with %q", tc.form, rec.Code, tc.status, tc.text)
		}
	}
	if f.reloaded() != 1 {
		t.Errorf("%d reloads after mistakes", f.reloaded())
	}
}

func TestChangeASource(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	id := ta.sourceID("main")
	path := fmt.Sprintf("/admin/sources/%d", id)
	before, _ := ta.st.SourceSettings(ctx, id)

	// The page never shows the saved address.
	body := ta.do("GET", path, nil, c).Body.String()
	mustContain(t, "the page", body, "Change main", `value="main"`, "Saved: provider.example.", `<option value="21600" selected>`)
	if strings.Contains(body, before.URL) {
		t.Error("the page shows the saved address")
	}

	// A test without an address reads the saved one.
	ta.do("POST", path, sourceFormValues(csrf, "name", "main", "test", "1"), c)
	if len(f.tested) != 1 || f.tested[0].URL != before.URL {
		t.Errorf("tested %+v", f.tested)
	}

	// Saving without an address keeps it; with one, it changes.
	rec := ta.do("POST", path, sourceFormValues(csrf, "name", "renamed", "refresh", "2700", "numbers", "provider"), c)
	if rec.Code != http.StatusSeeOther || f.reloaded() != 1 {
		t.Fatalf("save: status %d, reloads %d", rec.Code, f.reloaded())
	}
	after, _ := ta.st.SourceSettings(ctx, id)
	if after.Name != "renamed" || after.URL != before.URL || after.Refresh.Minutes() != 45 || after.Numbers != "provider" {
		t.Errorf("after saving: %+v", after)
	}
	// An interval that the form does not offer is kept as a choice.
	mustContain(t, "the page", ta.do("GET", path, nil, c).Body.String(), `<option value="2700" selected>Every 45 minutes</option>`)
	ta.do("POST", path, sourceFormValues(csrf, "name", "renamed", "url", secretAddress), c)
	if after, _ = ta.st.SourceSettings(ctx, id); after.URL != secretAddress {
		t.Errorf("new address: %+v", after)
	}

	if rec := ta.do("GET", "/admin/sources/9999", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown source: status %d", rec.Code)
	}
	if rec := ta.do("POST", "/admin/sources/9999", sourceFormValues(csrf, "name", "x"), c); rec.Code != http.StatusNotFound {
		t.Errorf("saving an unknown source: status %d", rec.Code)
	}
}

func TestNewCategoriesForm(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, _ := ta.withSources()
	id := ta.sourceID("main")
	hx := []string{"HX-Request", "true"}
	mustContain(t, "the page", ta.do("GET", "/admin/sources", nil, c).Body.String(),
		"<dt>New categories</dt>", "<span>Start shown</span>", fmt.Sprintf(`action="/admin/sources/%d/categories-hidden"`, id))
	rec := ta.do("POST", fmt.Sprintf("/admin/sources/%d/categories-hidden", id), url.Values{"csrf": {csrf}}, c, hx...)
	mustContain(t, "the answer", rec.Body.String(), "New categories from main will start hidden.", "<span>Start hidden</span>",
		fmt.Sprintf(`action="/admin/sources/%d/categories-shown"`, id))
	if srcs, _ := ta.st.Sources(context.Background()); !srcs[0].NewCategoriesHidden {
		t.Error("the setting did not stay")
	}
	rec = ta.do("POST", fmt.Sprintf("/admin/sources/%d/categories-shown", id), url.Values{"csrf": {csrf}}, c, hx...)
	mustContain(t, "the answer", rec.Body.String(), "New categories from main will start shown.")
	if srcs, _ := ta.st.Sources(context.Background()); srcs[0].NewCategoriesHidden {
		t.Error("the setting did not go back")
	}
	if rec = ta.do("POST", "/admin/sources/9999/categories-hidden", url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown source: status %d", rec.Code)
	}
}

func TestSourceActions(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	id := ta.sourceID("main")
	hx := []string{"HX-Request", "true"}
	act := func(action string) *http.Response {
		t.Helper()
		return ta.do("POST", fmt.Sprintf("/admin/sources/%d/%s", id, action), url.Values{"csrf": {csrf}}, c, hx...).Result()
	}
	read := func(action string) string {
		t.Helper()
		rec := ta.do("POST", fmt.Sprintf("/admin/sources/%d/%s", id, action), url.Values{"csrf": {csrf}}, c, hx...)
		return rec.Body.String()
	}

	mustContain(t, "read now", read("refresh"), "Read main: 4 channels, 1 new, 2 gone missing, 0 removed.")
	f.readErr = errors.New("fetch: connection refused")
	mustContain(t, "a failed read", read("refresh"), "The source could not be read: fetch: connection refused.")

	mustContain(t, "turn off", read("off"), "Turned main off: devices no longer see its channels.")
	if src, _ := ta.st.Source(ctx, id); src.Enabled {
		t.Error("still on")
	}
	mustContain(t, "turn on", read("on"), "Turned main on.")
	mustContain(t, "delete", read("delete"), "Deleted main, with its channels.")
	if srcs, _ := ta.st.Sources(ctx); len(srcs) != 0 {
		t.Errorf("sources after the delete: %+v", srcs)
	}
	if f.reloaded() != 3 {
		t.Errorf("%d reloads, want 3", f.reloaded())
	}
	if res := act("refresh"); res.StatusCode != http.StatusNotFound {
		t.Errorf("a deleted source: status %d", res.StatusCode)
	}
	if res := act("explode"); res.StatusCode != http.StatusNotFound {
		t.Errorf("an unknown action: status %d", res.StatusCode)
	}
	if rec := ta.do("POST", fmt.Sprintf("/admin/sources/%d/off", id), url.Values{"csrf": {"wrong"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the token: status %d", rec.Code)
	}
}

func TestSourcesWithoutAReader(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	id := ta.sourceID("main")
	rec := ta.do("POST", fmt.Sprintf("/admin/sources/%d/refresh", id), url.Values{"csrf": {h[1]}}, c, "HX-Request", "true")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "cannot read sources") {
		t.Errorf("status %d", rec.Code)
	}
}

func TestSourcesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	h := []string{csrfHeader, csrf}
	id := ta.sourceID("main")

	rec := ta.apiDo("GET", "sources", "", c)
	var list struct{ Sources []apiSource }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Sources) != 1 {
		t.Fatalf("status %d, %s", rec.Code, rec.Body)
	}
	if src := list.Sources[0]; src.Name != "main" || src.Host != "provider.example" || src.Address != "saved" || src.Channels != 4 ||
		src.RefreshSeconds != 21600 || src.MaxConnections != 1 || src.Numbers != "playlist" || src.RefreshedAt != nil {
		t.Errorf("source %+v", src)
	}
	if strings.Contains(rec.Body.String(), "main.m3u") {
		t.Error("the API shows the saved address")
	}

	// Add, then change.
	rec = ta.apiDo("POST", "sources", `{"name": "second", "url": "`+secretAddress+`", "max_connections": 0, "enabled": false}`, c, h...)
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || rec.Code != http.StatusCreated || added.Enabled ||
		added.MaxConnections != 0 || added.Host != "provider.example:8080" || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "second", "url": "/srv/x.m3u"}`, c, h...), http.StatusConflict, "name_taken")
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "third"}`, c, h...), http.StatusBadRequest, "bad_url")
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "third", "url": "/srv/x.m3u", "refresh_seconds": 60}`, c, h...), http.StatusBadRequest, "bad_refresh")
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "third", "url": "/srv/x.m3u", "type": "tvheadend"}`, c, h...), http.StatusBadRequest, "bad_type")
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "third", "url": "/srv/x.m3u", "type": "xtream"}`, c, h...), http.StatusBadRequest, "bad_url")

	path := fmt.Sprintf("sources/%d", added.ID)
	rec = ta.apiDo("PATCH", path, `{"refresh_seconds": 3600, "enabled": true, "new_categories_hidden": true}`, c, h...)
	var changed apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil || !changed.Enabled || changed.RefreshSeconds != 3600 || !changed.NewCategoriesHidden {
		t.Errorf("change: status %d, %s", rec.Code, rec.Body)
	}
	if settings, _ := ta.st.SourceSettings(context.Background(), added.ID); settings.URL != secretAddress {
		t.Errorf("the change lost the address: %+v", settings)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"url": ""}`, c, h...), http.StatusBadRequest, "bad_url")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"name": "main"}`, c, h...), http.StatusConflict, "name_taken")
	apiErrorOf(t, ta.apiDo("PATCH", "sources/9999", `{"enabled": false}`, c, h...), http.StatusNotFound, "not_found")

	// Test and read.
	rec = ta.apiDo("POST", "sources/test", `{"url": "/srv/test.m3u"}`, c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"channels": 5471`) {
		t.Errorf("test: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("POST", fmt.Sprintf("sources/%d/test", id), "", c, h...)
	if rec.Code != http.StatusOK || f.tested[len(f.tested)-1].URL != "http://provider.example/main.m3u" {
		t.Errorf("test a saved source: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("POST", fmt.Sprintf("sources/%d/refresh", id), "", c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"added": 1`) {
		t.Errorf("read: status %d, %s", rec.Code, rec.Body)
	}
	f.readErr = errors.New("the playlist lists no channels")
	apiErrorOf(t, ta.apiDo("POST", fmt.Sprintf("sources/%d/refresh", id), "", c, h...), http.StatusBadGateway, "source_failed")

	if rec = ta.apiDo("DELETE", path, "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("DELETE", path, "", c, h...), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("sources/%d", id), `{"enabled": false}`, c), http.StatusForbidden, "csrf")
}

func TestXtreamSourceForm(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the form", body, `<input type="radio" name="type" value="xtream">`, `name="server"`, `name="username"`,
		`name="password" type="password"`, "without get.php or player_api.php")

	// A test finds the account, and keeps what was typed.
	expires := time.Date(2027, 3, 3, 0, 0, 0, 0, time.UTC)
	f.account = &xtream.Account{Status: "Active", ExpiresAt: expires, MaxConnections: 2}
	form := sourceFormValues(csrf, "name", "tv", "type", "xtream", "server", " http://provider.example:8080 ",
		"username", " viewer ", "password", "s3cret ", "url", "/srv/ignored.m3u")
	form.Set("test", "1")
	rec := ta.do("POST", "/admin/sources", form, c, hx...)
	mustContain(t, "the test", rec.Body.String(), "The server accepted the login. The account runs until 3 March 2027, "+
		"and allows 2 streams at once. It lists 5,471 live channels.", `<input type="radio" name="type" value="xtream" checked>`,
		`value="http://provider.example:8080"`, `value="viewer"`)
	if got := f.tested[0]; got.Type != source.TypeXtream || got.URL != "http://provider.example:8080" || got.Username != "viewer" ||
		got.Password != "s3cret" {
		t.Errorf("tested %+v", got)
	}
	f.testErr = &xtream.AccountError{Status: "Expired"}
	mustContain(t, "a failed test", ta.do("POST", "/admin/sources", form, c, hx...).Body.String(),
		"The source could not be read: the server says the account is Expired.")
	f.testErr = nil

	// Adding saves the login, which the page never shows.
	form.Del("test")
	if rec := ta.do("POST", "/admin/sources", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	id := ta.sourceID("tv")
	saved, err := ta.st.SourceSettings(ctx, id)
	if err != nil || saved.Type != source.TypeXtream || saved.URL != "http://provider.example:8080" || saved.Username != "viewer" ||
		saved.Password != "s3cret" {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	if _, err := ta.st.SetSourceAccount(ctx, id, store.SourceAccount{Status: "Active", ExpiresAt: expires, MaxConnections: 2}); err != nil {
		t.Fatal(err)
	}
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the list", body, "<dt>Server</dt><dd>provider.example:8080</dd>", "<dd>Active until 3 Mar 2027</dd>",
		"<dt>Streams at once</dt><dd>1</dd>", "In the server's order")
	if strings.Contains(body, "s3cret") || strings.Contains(body, "viewer") {
		t.Error("the list shows the login")
	}

	// A change of only the password keeps the rest of the login.
	path := fmt.Sprintf("/admin/sources/%d", id)
	body = ta.do("GET", path, nil, c).Body.String()
	mustContain(t, "the change page", body, "Saved: provider.example:8080. Enter only what changes",
		`placeholder="Leave empty to keep the saved password"`, `<input type="radio" name="type" value="xtream" checked>`)
	if strings.Contains(body, "s3cret") || strings.Contains(body, "viewer") {
		t.Error("the change page shows the login")
	}
	change := sourceFormValues(csrf, "name", "tv", "type", "xtream", "password", "n3w", "max_connections", "0")
	if rec := ta.do("POST", path, change, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ = ta.st.SourceSettings(ctx, id); saved.URL != "http://provider.example:8080" || saved.Username != "viewer" ||
		saved.Password != "n3w" || saved.MaxConnections != 0 {
		t.Errorf("after the change %+v", saved)
	}
	mustContain(t, "the list", ta.do("GET", "/admin/sources", nil, c).Body.String(),
		"<dt>Streams at once</dt><dd>2, the account&#39;s limit</dd>")

	// A playlist source needs the whole login to become an Xtream one.
	main := fmt.Sprintf("/admin/sources/%d", ta.sourceID("main"))
	rec = ta.do("POST", main, sourceFormValues(csrf, "name", "main", "type", "xtream", "password", "x"), c, hx...)
	mustContain(t, "half a login", rec.Body.String(), "Enter the server&#39;s address, the username and the password.")
}

func TestXtreamSourceAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	h := []string{csrfHeader, csrf}
	rec := ta.apiDo("POST", "sources", `{"name": "tv", "type": "xtream", "url": "http://provider.example:8080",
		"username": "viewer", "password": "s3cret"}`, c, h...)
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || rec.Code != http.StatusCreated || added.Type != "xtream" ||
		added.Host != "provider.example:8080" || added.Account != nil || strings.Contains(rec.Body.String(), "s3cret") ||
		strings.Contains(rec.Body.String(), "viewer") {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	ctx := context.Background()
	if _, err := ta.st.SetSourceAccount(ctx, added.ID, store.SourceAccount{Status: "Active",
		ExpiresAt: time.Date(2027, 3, 3, 0, 0, 0, 0, time.UTC), MaxConnections: 2, TimeZone: "Europe/London"}); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("sources/%d", added.ID)
	rec = ta.apiDo("PATCH", path, `{"password": "n3w", "max_connections": 0}`, c, h...)
	var changed apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil || changed.StreamLimit != 2 || changed.Account == nil ||
		changed.Account.Status != "Active" || *changed.Account.ExpiresAt != "2027-03-03T00:00:00Z" || changed.Account.TimeZone != "Europe/London" {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ := ta.st.SourceSettings(ctx, added.ID); saved.Password != "n3w" || saved.Username != "viewer" {
		t.Errorf("saved %+v", saved)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"password": ""}`, c, h...), http.StatusBadRequest, "bad_password")

	f.account = &xtream.Account{Status: "Active", MaxConnections: 1}
	rec = ta.apiDo("POST", path+"/test", `{"username": "other"}`, c, h...)
	var tested apiTested
	if err := json.Unmarshal(rec.Body.Bytes(), &tested); err != nil || tested.Account == nil || tested.Account.ExpiresAt != nil ||
		tested.Account.MaxConnections != 1 {
		t.Fatalf("test: status %d, %s", rec.Code, rec.Body)
	}
	if got := f.tested[len(f.tested)-1]; got.Username != "other" || got.Password != "n3w" || got.URL != "http://provider.example:8080" {
		t.Errorf("tested %+v", got)
	}
}

func TestSourceGuideOnTheCard(t *testing.T) {
	ta := newTestAdmin(t)
	c, _, _ := ta.withSources()
	ctx := context.Background()
	id := ta.sourceID("main")
	mustContain(t, "no guide", ta.do("GET", "/admin/sources", nil, c).Body.String(), "<dt>Guide</dt><dd>None named</dd>")
	if err := ta.st.SetListedGuide(ctx, id, "http://guide.example/epg.xml?key=s3cret"); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "named", ta.do("GET", "/admin/sources", nil, c).Body.String(), "<dt>Guide</dt><dd>Not read yet</dd>")
	gcs, err := ta.st.GuideChannels(ctx, id)
	if err != nil || len(gcs) == 0 {
		t.Fatalf("channels %+v, %v", gcs, err)
	}
	w, err := ta.st.BeginGuide(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range gcs[:2] {
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: ch.ID, Start: ta.clock, Stop: ta.clock.Add(time.Hour), Title: "News"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Commit(ctx, ta.clock.Add(-2*time.Hour), "hash"); err != nil {
		t.Fatal(err)
	}
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "read", body, "<dt>Guide</dt><dd>2 channels, read 2 hours ago</dd>")
	if strings.Contains(body, "s3cret") || strings.Contains(body, "guide.example") {
		t.Error("the page shows the guide's address")
	}
	w, _ = ta.st.BeginGuide(ctx, id)
	if err := w.Abort(ctx, ta.clock, "hash", "the guide's server answered 403 Forbidden"); err != nil {
		t.Fatal(err)
	}
	mustContain(t, "failed", ta.do("GET", "/admin/sources", nil, c).Body.String(),
		"The last read of the guide failed: the guide&#39;s server answered 403 Forbidden", "<dd>2 channels, read just now</dd>")
	rec := ta.apiDo("GET", "sources", "", c)
	var list struct{ Sources []apiSource }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Sources) != 1 {
		t.Fatalf("status %d, %s", rec.Code, rec.Body)
	}
	if g := list.Sources[0].Guide; !g.Listed || g.Channels != 2 || g.Programmes != 2 || g.ReadAt == nil || g.Error == "" {
		t.Errorf("guide %+v", g)
	}
}

func TestSourceLibraryOnTheCard(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, _ := ta.withSources()
	rec := ta.apiDo("POST", "sources", `{"name": "tv", "type": "xtream", "url": "http://provider.example:8080",
		"username": "viewer", "password": "s3cret"}`, c, csrfHeader, csrf)
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || added.Films == nil || added.Films.ReadAt != nil ||
		added.Films.Titles != 0 || added.Series == nil {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	mustContain(t, "not read", ta.do("GET", "/admin/sources", nil, c).Body.String(),
		"<dt>Films</dt><dd>Not read yet: Overscan reads them when a box first opens Movies.</dd>",
		"<dt>Series</dt><dd>Not read yet: Overscan reads them when a box first opens Series.</dd>")

	ctx := context.Background()
	if _, err := ta.st.ApplyLibrary(ctx, added.ID, store.KindMovie, nil, []store.LibraryTitle{
		{Key: "1", Name: "Evening Light"}, {Key: "2", Name: "Field Notes"}}, ta.clock.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetLibraryFailed(ctx, added.ID, store.KindSeries, ta.clock.Add(-5*time.Minute), "the server answered 503 Service Unavailable"); err != nil {
		t.Fatal(err)
	}
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "read", body, "<dt>Films</dt><dd>2 films, read 3 hours ago</dd>",
		"<dt>Series</dt><dd>The last read failed, 5 minutes ago: the server answered 503 Service Unavailable</dd>")
	if strings.Count(body, "<dt>Films</dt>") != 1 {
		t.Error("a playlist source shows films")
	}

	rec = ta.apiDo("GET", "sources", "", c)
	var list struct{ Sources []apiSource }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Sources) != 2 {
		t.Fatalf("status %d, %s", rec.Code, rec.Body)
	}
	for _, s := range list.Sources {
		switch s.Type {
		case "xtream":
			if s.Films == nil || s.Films.Titles != 2 || s.Films.ReadAt == nil || s.Series == nil || s.Series.ReadAt != nil ||
				s.Series.Error == "" || s.Series.TriedAt == nil {
				t.Errorf("xtream %+v %+v", s.Films, s.Series)
			}
		default:
			if s.Films != nil || s.Series != nil {
				t.Errorf("playlist %+v %+v", s.Films, s.Series)
			}
		}
	}
	if !strings.Contains(rec.Body.String(), `"films": null`) {
		t.Errorf("a playlist's films are not null: %s", rec.Body)
	}
	bundle, err := ta.bundleSources(ctx, ta.clock)
	if err != nil {
		t.Fatal(err)
	}
	mustContain(t, "bundle", bundle, "\n  Films ", " 2 films, read 3 hours ago\n", "\n  Series ",
		" The last read failed, 5 minutes ago: the server answered 503 Service Unavailable\n")
}

func TestJellyfinSourceForm(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}
	const key = "0123456789abcdef"
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the form", body, `<input type="radio" name="type" value="jellyfin">`, `name="jf_server"`,
		`name="jf_key" type="password"`, `name="jf_user"`, `name="library_max_connections" type="number" min="0" max="1000" value="0"`)

	// A test says what the server is and which libraries boxes would see.
	form := sourceFormValues(csrf, "name", "den", "type", "jellyfin", "jf_server", " http://jellyfin.example:8096 ",
		"jf_key", key+" ", "jf_user", "Anna", "library_max_connections", "0", "url", "/srv/ignored.m3u", "server", "http://x.example",
		"username", "ignored", "test", "1")
	rec := ta.do("POST", "/admin/sources", form, c, hx...)
	mustContain(t, "the test", rec.Body.String(), "The server took the API key. It is Jellyfin 10.11.2, named Den. "+
		"Boxes would see the films and series in its 3 libraries: Films, Shows and Home. Nothing has been saved yet.",
		`<input type="radio" name="type" value="jellyfin" checked>`)
	if got := f.tested[0]; got.Type != source.TypeJellyfin || got.URL != "http://jellyfin.example:8096" || got.Password != key ||
		got.Username != "Anna" || got.MaxConnections != 0 {
		t.Errorf("tested %+v", got)
	}

	// Adding saves the key, which the page never shows; the card says
	// what a Jellyfin source has.
	form.Del("test")
	if rec := ta.do("POST", "/admin/sources", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	id := ta.sourceID("den")
	saved, err := ta.st.SourceSettings(ctx, id)
	if err != nil || saved.Type != source.TypeJellyfin || saved.Password != key || saved.Username != "Anna" || saved.MaxConnections != 0 {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the card", body, `<span class="badge">Jellyfin server</span>`, "Not checked yet", "Check now",
		"<dt>Server</dt><dd>jellyfin.example:8096</dd>", "<dt>Last checked</dt>", "<dt>Streams at once</dt><dd>No limit</dd>",
		"Not read yet: Overscan reads them when a box first opens Movies.",
		"Its films and series leave the boxes, with how far each profile got in them.")
	card := body[strings.Index(body, `id="source-`+fmt.Sprint(id)+`"`):]
	card = card[:strings.Index(card, "</section>")]
	for _, absent := range []string{key, "<dt>Channels</dt>", "<dt>Guide</dt>", "<dt>Numbers</dt>", "New categories"} {
		if strings.Contains(card, absent) {
			t.Errorf("the card shows %q", absent)
		}
	}

	// The change page shows the user, never the key; leaving the key and
	// address empty keeps them, and an empty user is every library.
	path := fmt.Sprintf("/admin/sources/%d", id)
	body = ta.do("GET", path, nil, c).Body.String()
	mustContain(t, "the change page", body, `name="jf_user" value="Anna"`, "Saved: jellyfin.example:8096.",
		`placeholder="Leave empty to keep the saved key"`)
	if strings.Contains(body, key) {
		t.Error("the change page shows the key")
	}
	change := sourceFormValues(csrf, "name", "den", "type", "jellyfin", "jf_user", "", "library_max_connections", "2")
	if rec := ta.do("POST", path, change, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ = ta.st.SourceSettings(ctx, id); saved.URL != "http://jellyfin.example:8096" || saved.Password != key ||
		saved.Username != "" || saved.MaxConnections != 2 {
		t.Errorf("after the change %+v", saved)
	}
	// A test of the saved source fills in its address and key.
	change.Set("test", "1")
	ta.do("POST", path, change, c, hx...)
	if got := f.tested[len(f.tested)-1]; got.URL != "http://jellyfin.example:8096" || got.Password != key || got.Username != "" {
		t.Errorf("tested %+v", got)
	}

	// Checking it now.
	rec = ta.do("POST", path+"/refresh", url.Values{"csrf": {csrf}}, c, hx...)
	mustContain(t, "a check", rec.Body.String(), "Checked den: the server answers, and takes the API key.")

	// A playlist needs an address and a key to become a Jellyfin server.
	main := fmt.Sprintf("/admin/sources/%d", ta.sourceID("main"))
	rec = ta.do("POST", main, sourceFormValues(csrf, "name", "main", "type", "jellyfin", "jf_key", key), c, hx...)
	mustContain(t, "no address", rec.Body.String(), "Enter the server&#39;s address and the API key.")
	mustContain(t, "the hint", ta.do("GET", main, nil, c).Body.String(),
		"A media server or a folder has no channels: this source's channels would leave the lineup.")
}

func TestJellyfinSourceAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	h := []string{csrfHeader, csrf}
	ctx := context.Background()
	rec := ta.apiDo("POST", "sources", `{"name": "den", "type": "jellyfin", "url": "http://jellyfin.example:8096",
		"username": "Anna", "password": "0123456789abcdef"}`, c, h...)
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || rec.Code != http.StatusCreated || added.Type != "jellyfin" ||
		added.MaxConnections != 0 || added.Films == nil || added.Series == nil || strings.Contains(rec.Body.String(), "0123456789abcdef") {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	// A change that leaves the user out keeps it.
	path := fmt.Sprintf("sources/%d", added.ID)
	if rec := ta.apiDo("PATCH", path, `{"max_connections": 3}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ := ta.st.SourceSettings(ctx, added.ID); saved.Username != "Anna" || saved.MaxConnections != 3 {
		t.Errorf("saved %+v", saved)
	}
	// No user is every library.
	if rec := ta.apiDo("PATCH", path, `{"username": ""}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("no user: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ := ta.st.SourceSettings(ctx, added.ID); saved.Username != "" || saved.Password != "0123456789abcdef" {
		t.Errorf("saved %+v", saved)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"password": ""}`, c, h...), http.StatusBadRequest, "bad_password")

	rec = ta.apiDo("POST", path+"/test", "", c, h...)
	var tested apiTested
	if err := json.Unmarshal(rec.Body.Bytes(), &tested); err != nil || tested.Server == nil || tested.Server.Version != "10.11.2" ||
		len(tested.Libraries) != 3 || tested.Account != nil {
		t.Fatalf("test: status %d, %s", rec.Code, rec.Body)
	}
	if got := f.tested[len(f.tested)-1]; got.URL != "http://jellyfin.example:8096" || got.Password != "0123456789abcdef" {
		t.Errorf("tested %+v", got)
	}
}

func TestPlexSourceForm(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}
	const token = "fictional-token"
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the form", body, `<input type="radio" name="type" value="plex">`, `name="px_server"`,
		`name="px_token" type="password"`)

	form := sourceFormValues(csrf, "name", "lounge", "type", "plex", "px_server", " http://plex.example:32400 ",
		"px_token", token+" ", "library_max_connections", "0", "jf_server", "http://ignored.example", "jf_user", "ignored",
		"test", "1")
	rec := ta.do("POST", "/admin/sources", form, c, hx...)
	mustContain(t, "the test", rec.Body.String(), "The server took the token. It is Plex Media Server 1.42.2.10156, named Lounge. "+
		"Boxes would see the films and series in its library: Films. Nothing has been saved yet.",
		`<input type="radio" name="type" value="plex" checked>`)
	if got := f.tested[0]; got.Type != source.TypePlex || got.URL != "http://plex.example:32400" || got.Password != token ||
		got.Username != "" || got.MaxConnections != 0 {
		t.Errorf("tested %+v", got)
	}

	form.Del("test")
	if rec := ta.do("POST", "/admin/sources", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	id := ta.sourceID("lounge")
	saved, err := ta.st.SourceSettings(ctx, id)
	if err != nil || saved.Type != source.TypePlex || saved.Password != token || saved.Username != "" || saved.MaxConnections != 0 {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the card", body, `<span class="badge">Plex server</span>`, "Not checked yet", "Check now",
		"<dt>Server</dt><dd>plex.example:32400</dd>", "<dt>Streams at once</dt><dd>No limit</dd>",
		"Its films and series leave the boxes, with how far each profile got in them.")
	card := body[strings.Index(body, `id="source-`+fmt.Sprint(id)+`"`):]
	card = card[:strings.Index(card, "</section>")]
	for _, absent := range []string{token, "<dt>Channels</dt>", "<dt>Guide</dt>", "<dt>Numbers</dt>", "New categories"} {
		if strings.Contains(card, absent) {
			t.Errorf("the card shows %q", absent)
		}
	}

	// The change page never shows the token; leaving it empty keeps it.
	path := fmt.Sprintf("/admin/sources/%d", id)
	body = ta.do("GET", path, nil, c).Body.String()
	mustContain(t, "the change page", body, "Saved: plex.example:32400.", `placeholder="Leave empty to keep the saved token"`)
	if strings.Contains(body, token) {
		t.Error("the change page shows the token")
	}
	change := sourceFormValues(csrf, "name", "lounge", "type", "plex", "px_server", "https://plex.example:32400",
		"library_max_connections", "2")
	if rec := ta.do("POST", path, change, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ = ta.st.SourceSettings(ctx, id); saved.URL != "https://plex.example:32400" || saved.Password != token ||
		saved.MaxConnections != 2 {
		t.Errorf("changed %+v", saved)
	}

	// The API takes the token as the password, and never gives it back.
	h := []string{csrfHeader, csrf}
	rec = ta.apiDo("POST", "sources", `{"name": "den", "type": "plex", "url": "http://plex.example:32400",
		"password": "`+token+`2"}`, c, h...)
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || rec.Code != http.StatusCreated || added.Type != "plex" ||
		added.MaxConnections != 0 || added.Films == nil || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("add through the API: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("POST", fmt.Sprintf("sources/%d/test", added.ID), "", c, h...)
	var tested apiTested
	if err := json.Unmarshal(rec.Body.Bytes(), &tested); err != nil || tested.Server == nil || tested.Server.Name != "Lounge" {
		t.Fatalf("test through the API: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "den2", "type": "plex", "url": "http://plex.example:32400",
		"username": "anna", "password": "t"}`, c, h...), http.StatusBadRequest, "bad_username")
}

func TestFolderSourceForm(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the form", body, `<input type="radio" name="type" value="folder">`, `name="folder_path"`,
		"does not see folders under /home")

	form := sourceFormValues(csrf, "name", "films", "type", "folder", "folder_path", " /srv/media/Films ",
		"library_max_connections", "0", "test", "1")
	rec := ta.do("POST", "/admin/sources", form, c, hx...)
	mustContain(t, "the test", rec.Body.String(),
		"Overscan can read the folder Films: it holds 1,204 films and 1 series. Nothing has been saved yet.",
		`<input type="radio" name="type" value="folder" checked>`)
	if got := f.tested[0]; got.Type != source.TypeFolder || got.URL != "/srv/media/Films" || got.MaxConnections != 0 {
		t.Errorf("tested %+v", got)
	}
	form.Del("test")
	if rec := ta.do("POST", "/admin/sources", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	id := ta.sourceID("films")
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the card", body, `<span class="badge">Folder</span>`, "<dt>Folder</dt><dd>Films</dd>", "Check now",
		"<dt>Last checked</dt>", "<dt>Streams at once</dt><dd>No limit</dd>", "The folder and its files stay as they are.")
	card := body[strings.Index(body, `id="source-`+fmt.Sprint(id)+`"`):]
	card = card[:strings.Index(card, "</section>")]
	for _, absent := range []string{"/srv/media", "<dt>Channels</dt>", "<dt>Guide</dt>", "New categories"} {
		if strings.Contains(card, absent) {
			t.Errorf("the card shows %q", absent)
		}
	}
	// The change page keeps the saved folder when it is left empty.
	path := fmt.Sprintf("/admin/sources/%d", id)
	mustContain(t, "the change page", ta.do("GET", path, nil, c).Body.String(), "Saved: Films. Enter a new path only to change it.")
	change := sourceFormValues(csrf, "name", "films", "type", "folder", "library_max_connections", "3")
	if rec := ta.do("POST", path, change, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ := ta.st.SourceSettings(ctx, id); saved.URL != "/srv/media/Films" || saved.MaxConnections != 3 {
		t.Errorf("after the change %+v", saved)
	}
	rec = ta.do("POST", path+"/refresh", url.Values{"csrf": {csrf}}, c, hx...)
	mustContain(t, "a check", rec.Body.String(), "Checked films: Overscan can read the folder.")

	// The API.
	h := []string{csrfHeader, csrf}
	rec = ta.apiDo("POST", "sources/test", `{"type": "folder", "url": "/srv/media/Kids"}`, c, h...)
	var tested apiTested
	if err := json.Unmarshal(rec.Body.Bytes(), &tested); err != nil || tested.Folder == nil || tested.Folder.Name != "Kids" ||
		tested.Folder.Films != 1204 {
		t.Fatalf("test: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "kids", "type": "folder", "url": "media/Kids"}`, c, h...),
		http.StatusBadRequest, "bad_url")
}

func TestStalkerSourceForm(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	ctx := context.Background()
	hx := []string{"HX-Request", "true"}
	body := ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the form", body, `<input type="radio" name="type" value="stalker">`, `name="portal"`, `name="mac"`,
		`name="serial"`, `name="device_id"`, `name="device_id2"`, `name="signature"`, `name="model"`, `name="portal_login"`,
		`name="portal_password" type="password"`, "Overscan uses only this one, and never tries others.")

	// A test finds the account, and keeps what was typed.
	f.account = &xtream.Account{Status: "Active", ExpiresAt: time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)}
	form := sourceFormValues(csrf, "name", "box", "type", "stalker", "portal", " http://portal.example/stalker_portal/c/ ",
		"mac", " 00:1a:79:00:00:51 ", "serial", "FICTIONAL0001", "model", "MAG254", "url", "/srv/ignored.m3u")
	form.Set("test", "1")
	rec := ta.do("POST", "/admin/sources", form, c, hx...)
	mustContain(t, "the test", rec.Body.String(), "The portal took the box. The account runs until 12 March 2027. "+
		"It lists 5,471 live channels.", `<input type="radio" name="type" value="stalker" checked>`,
		`value="http://portal.example/stalker_portal/c/"`, `value="FICTIONAL0001"`)
	want := source.Device{MAC: "00:1a:79:00:00:51", Serial: "FICTIONAL0001", Model: "MAG254"}
	if got := f.tested[0]; got.Type != source.TypeStalker || got.URL != "http://portal.example/stalker_portal/c/" || got.Device != want {
		t.Errorf("tested %+v", got)
	}

	// Adding saves the box, which the page never shows.
	form.Del("test")
	if rec := ta.do("POST", "/admin/sources", form, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	id := ta.sourceID("box")
	saved, err := ta.st.SourceSettings(ctx, id)
	if err != nil || saved.Type != source.TypeStalker || saved.Device != want || saved.MaxConnections != 1 {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	if _, err := ta.st.SetSourceAccount(ctx, id, store.SourceAccount{Status: "Active",
		ExpiresAt: time.Date(2027, 3, 12, 0, 0, 0, 0, time.UTC)}); err != nil {
		t.Fatal(err)
	}
	if err := ta.st.ApplyPortalCategories(ctx, id, store.KindMovie, []store.LibraryCategory{{Key: "vod:1", Name: "Drama"}},
		time.Now()); err != nil {
		t.Fatal(err)
	}
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the list", body, `<span class="badge">Stalker portal</span>`, "<dt>Portal</dt><dd>portal.example</dd>",
		"<dd>Active until 12 Mar 2027</dd>", "In the portal's order", "<dt>Guide</dt><dd>Not read yet</dd>",
		"<dt>Films</dt><dd>0 films read so far, a page at a time as boxes browse them; its categories read ",
		"<dt>Series</dt><dd>Not read yet: Overscan reads them when a box first opens Series.</dd>")
	path := fmt.Sprintf("/admin/sources/%d", id)
	page := ta.do("GET", path, nil, c).Body.String()
	mustContain(t, "the change page", page, "Saved: portal.example. Enter only what changes",
		`placeholder="Leave empty to keep the saved MAC address"`, `<input type="radio" name="type" value="stalker" checked>`,
		"to clear a detail, enter the MAC address again")
	for _, secret := range []string{"00:1a:79:00:00:51", "00:1A:79:00:00:51", "FICTIONAL0001"} {
		if strings.Contains(body, secret) || strings.Contains(page, secret) {
			t.Errorf("a page shows %s", secret)
		}
	}

	// A change of only the model keeps the rest of the box, and a test
	// of the change page's form tests the saved box with it.
	change := sourceFormValues(csrf, "name", "box", "type", "stalker", "model", "MAG322")
	change.Set("test", "1")
	ta.do("POST", path, change, c, hx...)
	want.Model = "MAG322"
	if got := f.tested[len(f.tested)-1]; got.Device != want || got.URL != "http://portal.example/stalker_portal/c/" {
		t.Errorf("tested the change %+v", got)
	}
	change.Del("test")
	if rec := ta.do("POST", path, change, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ = ta.st.SourceSettings(ctx, id); saved.Device != want || saved.URL != "http://portal.example/stalker_portal/c/" {
		t.Errorf("after the change %+v", saved)
	}

	// The MAC address entered again describes the box again.
	again := sourceFormValues(csrf, "name", "box", "type", "stalker", "mac", "00:1A:79:00:00:51")
	if rec := ta.do("POST", path, again, c); rec.Code != http.StatusSeeOther {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	if saved, _ = ta.st.SourceSettings(ctx, id); saved.Device != (source.Device{MAC: "00:1A:79:00:00:51"}) {
		t.Errorf("after the box was entered again %+v", saved.Device)
	}

	// A playlist source needs the portal's address and the MAC address to
	// become a portal.
	main := fmt.Sprintf("/admin/sources/%d", ta.sourceID("main"))
	rec = ta.do("POST", main, sourceFormValues(csrf, "name", "main", "type", "stalker", "mac", "00:1A:79:00:00:51"), c, hx...)
	mustContain(t, "half a portal", rec.Body.String(), "Enter the portal&#39;s address and the box&#39;s MAC address.")
	rec = ta.do("POST", "/admin/sources", sourceFormValues(csrf, "name", "two", "type", "stalker",
		"portal", "http://portal.example/c/", "mac", "00:1A:79:00:00"), c, hx...)
	mustContain(t, "a bad MAC address", rec.Body.String(), "Enter the MAC address that the provider registered")
}

func TestStalkerSourceAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	h := []string{csrfHeader, csrf}
	rec := ta.apiDo("POST", "sources", `{"name": "box", "type": "stalker", "url": "http://portal.example/c/",
		"mac": "00:1A:79:00:00:51", "serial": "FICTIONAL0001", "signature": "made-up"}`, c, h...)
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || rec.Code != http.StatusCreated || added.Type != "stalker" ||
		added.Host != "portal.example" || added.MaxConnections != 1 || strings.Contains(rec.Body.String(), "00:1A:79") ||
		strings.Contains(rec.Body.String(), "FICTIONAL") {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	ctx := context.Background()
	path := fmt.Sprintf("sources/%d", added.ID)
	if rec = ta.apiDo("PATCH", path, `{"model": "MAG322"}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("change: status %d, %s", rec.Code, rec.Body)
	}
	want := source.Device{MAC: "00:1A:79:00:00:51", Serial: "FICTIONAL0001", Signature: "made-up", Model: "MAG322"}
	if saved, _ := ta.st.SourceSettings(ctx, added.ID); saved.Device != want {
		t.Errorf("saved %+v", saved.Device)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"mac": ""}`, c, h...), http.StatusBadRequest, "bad_mac")
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "two", "type": "stalker", "url": "http://portal.example/c/"}`, c, h...),
		http.StatusBadRequest, "bad_mac")
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "two", "type": "m3u", "url": "http://provider.example/two.m3u",
		"mac": "00:1A:79:00:00:51"}`, c, h...), http.StatusBadRequest, "bad_mac")

	f.account = &xtream.Account{Status: "Active"}
	if rec = ta.apiDo("POST", path+"/test", `{"serial": "OTHER"}`, c, h...); rec.Code != http.StatusOK {
		t.Fatalf("test: status %d, %s", rec.Code, rec.Body)
	}
	want.Serial = "OTHER"
	if got := f.tested[len(f.tested)-1]; got.Device != want || got.URL != "http://portal.example/c/" {
		t.Errorf("tested %+v", got)
	}
}
