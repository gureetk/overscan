// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// fakeLists stands in for the library's reads of lists.
type fakeLists struct {
	mu    sync.Mutex
	read  []string // "<source> <kind>"
	block bool     // a read takes until its context is done
	err   error
}

func (f *fakeLists) ReadList(ctx context.Context, sourceID int64, kind string) error {
	f.mu.Lock()
	f.read = append(f.read, kind)
	block, err := f.block, f.err
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeLists) reads() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.read...)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// categoriesOfAPI decodes the API's answer about a source's categories.
func categoriesOfAPI(t *testing.T, body []byte) apiCategories {
	t.Helper()
	var out apiCategories
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return out
}

func TestSourceCategoriesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withSources()
	h := []string{csrfHeader, csrf}
	id := ta.sourceID("main")
	base := "sources/" + itoa(id) + "/categories/"

	rec := ta.apiDo("GET", base+"live", "", c)
	got := categoriesOfAPI(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.OnlyTicked || got.Total != 2 || got.Ticked != 2 || got.Pending || got.ReadAt == nil ||
		len(got.Categories) != 2 || got.Categories[0].Key != "News" || *got.Categories[0].Items != 2 || !got.Categories[0].Ticked {
		t.Fatalf("live: %d %+v", rec.Code, got)
	}
	rec = ta.apiDo("GET", base+"live?q=SPO", "", c)
	if got := categoriesOfAPI(t, rec.Body.Bytes()); got.Total != 1 || got.Categories[0].Key != "Sports" {
		t.Errorf("search: %+v", got)
	}
	// A playlist has no films or series, and no source has radio.
	apiErrorOf(t, ta.apiDo("GET", base+"movies", "", c), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("GET", base+"radio", "", c), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("GET", "sources/99/categories/live", "", c), http.StatusNotFound, "not_found")

	// A change takes effect at the next read: it is pending until then.
	ta.clock = ta.clock.Add(time.Minute)
	rec = ta.apiDo("PUT", base+"live", `{"only_ticked": true, "ticked": {"Sports": false, "Nowhere": true}}`, c, h...)
	got = categoriesOfAPI(t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || !got.OnlyTicked || got.TickNew || got.Ticked != 1 || !got.Pending || got.ChangedAt == nil {
		t.Errorf("put: %d %+v", rec.Code, got)
	}
	if n := len(f.refreshd); n != 0 {
		t.Errorf("a PUT read the source %d times", n)
	}
	rec = ta.apiDo("PUT", base+"live", `{"tick_new": true}`, c, h...)
	if got := categoriesOfAPI(t, rec.Body.Bytes()); !got.OnlyTicked || !got.TickNew || got.Ticked != 1 {
		t.Errorf("put tick_new: %+v", got)
	}
	apiErrorOf(t, ta.apiDo("PUT", base+"live", `{"ticked": {"News": "yes"}}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PUT", base+"live", `{"every": true}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PUT", base+"live", `{"only_ticked": false}`, c), http.StatusForbidden, "csrf")

	// The read: the refresher's, with what it left out.
	rec = ta.apiDo("POST", base+"live/read", "", c, h...)
	var read struct {
		Reading  bool
		Channels *int
		LeftOut  *int `json:"left_out"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil || rec.Code != http.StatusOK || read.Channels == nil ||
		*read.Channels != 4 || read.LeftOut == nil || read.Reading {
		t.Errorf("read: %d %s", rec.Code, rec.Body)
	}
	if len(f.refreshd) != 1 || f.refreshd[0] != id {
		t.Errorf("refreshed %v", f.refreshd)
	}
	f.readErr = errors.New("the server answered 503 Service Unavailable")
	apiErrorOf(t, ta.apiDo("POST", base+"live/read", "", c, h...), http.StatusBadGateway, "source_failed")
	if err := ta.st.SetSourceEnabled(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	apiErrorOf(t, ta.apiDo("POST", base+"live/read", "", c, h...), http.StatusConflict, "source_off")

	// The source's own body sums it up.
	rec = ta.apiDo("GET", "sources", "", c)
	var list struct{ Sources []apiSource }
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Sources) != 1 ||
		list.Sources[0].Categories["live"] != (apiBrings{OnlyTicked: true, Listed: 2, Ticked: 1}) || len(list.Sources[0].Categories) != 1 {
		t.Errorf("sources: %s", rec.Body)
	}
}

func TestSourceCategoriesOfFilms(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, _ := ta.withSources()
	h := []string{csrfHeader, csrf}
	lists := &fakeLists{}
	ta.UseLists(lists)
	ctx := context.Background()
	id, err := ta.st.AddSource(ctx, source.Settings{Name: "xt", Type: source.TypeXtream, URL: "http://provider.example:8080",
		Username: "viewer", Password: "pw", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	base := "sources/" + itoa(id) + "/categories/"
	rec := ta.apiDo("GET", base+"movies", "", c)
	if got := categoriesOfAPI(t, rec.Body.Bytes()); rec.Code != http.StatusOK || got.Total != 0 || got.ReadAt != nil {
		t.Errorf("films before a read: %d %+v", rec.Code, got)
	}
	readBodyOf := func(body []byte) map[string]any {
		var out map[string]any
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	rec = ta.apiDo("POST", base+"series/read", "", c, h...)
	if got := readBodyOf(rec.Body.Bytes()); rec.Code != http.StatusOK || len(got) != 1 || got["reading"] != false {
		t.Errorf("read series: %d %s", rec.Code, rec.Body)
	}
	// A read that takes long goes on after the answer.
	defer func(d time.Duration) { listWait = d }(listWait)
	listWait = 20 * time.Millisecond
	lists.block = true
	rec = ta.apiDo("POST", base+"movies/read", "", c, h...)
	if got := readBodyOf(rec.Body.Bytes()); rec.Code != http.StatusAccepted || len(got) != 1 || got["reading"] != true {
		t.Errorf("a long read: %d %s", rec.Code, rec.Body)
	}
	if got := lists.reads(); len(got) != 2 || got[0] != store.KindSeries || got[1] != store.KindMovie {
		t.Errorf("reads %v", got)
	}
	// A folder has no categories to choose.
	folder, err := ta.st.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeFolder, URL: "/srv/films"})
	if err != nil {
		t.Fatal(err)
	}
	apiErrorOf(t, ta.apiDo("GET", "sources/"+itoa(folder)+"/categories/movies", "", c), http.StatusNotFound, "not_found")
	if rec := ta.do("GET", "/admin/sources/"+itoa(folder)+"/categories", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("a folder's page: %d", rec.Code)
	}
}

func TestSourceCategoriesPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, sessionCSRF, f := ta.withSources()
	id := ta.sourceID("main")
	page := "/admin/sources/" + itoa(id) + "/categories"

	body := ta.do("GET", page, nil, c).Body.String()
	mustContain(t, "the page", body, "<h1>What main brings</h1>", `name="bring" value="every" checked`,
		`<input type="checkbox" name="tick" value="News" checked>`, "2 channels", "2 of 2 ticked", "It brings all 4 channels.")
	if strings.Contains(body, `class="lineup-tabs bring-tabs"`) {
		t.Error("a playlist's page has tabs for films and series")
	}

	form := url.Values{"csrf": {sessionCSRF}, "kind": {"live"}, "bring": {"ticked"}, "listed": {"News", "Sports"}, "tick": {"News", "Elsewhere"}}
	ta.clock = ta.clock.Add(time.Minute)
	rec := ta.do("POST", page, form, c, "HX-Request", "true")
	if rec.Code != http.StatusOK {
		t.Fatalf("save: %d %s", rec.Code, rec.Body)
	}
	mustContain(t, "after saving", rec.Body.String(), "Saved, and read main again: it brings 4 channels.",
		`name="bring" value="ticked" checked`, `<input type="checkbox" name="tick" value="Sports">`, "1 of 2 ticked")
	if len(f.refreshd) != 1 {
		t.Errorf("refreshed %v", f.refreshd)
	}
	// The same again changes nothing, and reads nothing.
	rec = ta.do("POST", page, form, c, "HX-Request", "true")
	mustContain(t, "saving the same", rec.Body.String(), "Saved.</p>")
	if len(f.refreshd) != 1 {
		t.Errorf("refreshed %v", f.refreshd)
	}
	// Ticking what it adds later reads nothing either. Without htmx, the
	// page comes back.
	form.Set("tick_new", "1")
	rec = ta.do("POST", page, form, c)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != page+"?kind=live" || len(f.refreshd) != 1 {
		t.Errorf("without htmx: %d %q, refreshed %v", rec.Code, rec.Header().Get("Location"), f.refreshd)
	}
	if choice, err := ta.st.CategoryChoice(context.Background(), id, store.KindLive); err != nil || !choice.TickNew {
		t.Errorf("tick new: %+v, %v", choice, err)
	}
	// A failed read says that the change waits for the next.
	f.readErr = errors.New("the server answered 503 Service Unavailable")
	form.Add("tick", "Sports")
	rec = ta.do("POST", page, form, c, "HX-Request", "true")
	mustContain(t, "a failed read", rec.Body.String(), "Saved, but reading main again failed, so it takes effect at its next read.")

	// The card on the list sums it up, and links here.
	body = ta.do("GET", "/admin/sources", nil, c).Body.String()
	mustContain(t, "the list", body, "<dt>Categories it brings</dt>", "Live TV: 2 of 2 categories", `href="`+page+`"`)

	// Read now, from the page.
	f.readErr = nil
	rec = ta.do("POST", page+"/read", url.Values{"csrf": {sessionCSRF}, "kind": {"live"}}, c, "HX-Request", "true")
	mustContain(t, "read now", rec.Body.String(), "Read main: it brings 4 channels.")
	if rec := ta.do("POST", page, url.Values{"csrf": {sessionCSRF}, "kind": {"movies"}}, c); rec.Code != http.StatusNotFound {
		t.Errorf("a kind that the source lacks: %d", rec.Code)
	}
}

func TestAddASourceThatBringsTickedCategories(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, _ := ta.withSources()
	h := []string{csrfHeader, csrf}
	ctx := context.Background()
	rec := ta.apiDo("POST", "sources", `{"name": "two", "type": "xtream", "url": "http://provider.example:8080",
		"username": "viewer", "password": "pw", "bring": "ticked"}`, c, h...)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add: %d %s", rec.Code, rec.Body)
	}
	var added apiSource
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"live", "movies", "series"} {
		if b, ok := added.Categories[kind]; !ok || !b.OnlyTicked {
			t.Errorf("%s: %+v", kind, added.Categories)
		}
	}
	for _, k := range []string{store.KindLive, store.KindMovie, store.KindSeries} {
		if choice, err := ta.st.CategoryChoice(ctx, added.ID, k); err != nil || !choice.OnlyTicked || choice.TickNew {
			t.Errorf("%s: %+v, %v", k, choice, err)
		}
	}
	apiErrorOf(t, ta.apiDo("POST", "sources", `{"name": "three", "url": "http://provider.example/three.m3u", "bring": "some"}`, c, h...),
		http.StatusBadRequest, "bad_bring")
	apiErrorOf(t, ta.apiDo("PATCH", "sources/"+itoa(added.ID), `{"bring": "every"}`, c, h...), http.StatusBadRequest, "bad_bring")

	// From the form: a Jellyfin server brings ticked films and series; a
	// folder ignores the choice.
	form := sourceFormValues(csrf, "name", "den", "type", "jellyfin", "jf_server", "http://jellyfin.example:8096",
		"jf_key", "k3y", "bring", "ticked")
	rec = ta.do("POST", "/admin/sources", form, c, "HX-Request", "true")
	mustContain(t, "add from the form", rec.Body.String(), "Its films and series bring only the categories you tick")
	den := ta.sourceID("den")
	if choice, err := ta.st.CategoryChoice(ctx, den, store.KindMovie); err != nil || !choice.OnlyTicked {
		t.Errorf("den's films: %+v, %v", choice, err)
	}
	if choice, err := ta.st.CategoryChoice(ctx, den, store.KindLive); err != nil || choice.OnlyTicked {
		t.Errorf("den's live channels: %+v, %v", choice, err)
	}
	form = sourceFormValues(csrf, "name", "shelf", "type", "folder", "folder_path", "/srv/films", "bring", "ticked")
	ta.do("POST", "/admin/sources", form, c, "HX-Request", "true")
	if choice, err := ta.st.CategoryChoice(ctx, ta.sourceID("shelf"), store.KindMovie); err != nil || choice.OnlyTicked {
		t.Errorf("a folder's films: %+v, %v", choice, err)
	}
}

// Channels that a source no longer brings are marked as left out, not as
// missing ones.
func TestLeftOutChannels(t *testing.T) {
	ta := newTestAdmin(t)
	c, _, _ := ta.withSources()
	ctx := context.Background()
	id := ta.sourceID("main")
	if _, err := ta.st.SetCategoryChoice(ctx, id, store.KindLive, true, false, ta.clock); err != nil {
		t.Fatal(err)
	}
	if _, err := ta.st.TickCategories(ctx, id, store.KindLive, map[string]bool{"Sports": false}, ta.clock); err != nil {
		t.Fatal(err)
	}
	var chs []store.CatalogChannel
	for _, spec := range []string{"Alpha/News", "Bravo/Sports", "Delta/Sports"} {
		name, category, _ := strings.Cut(spec, "/")
		chs = append(chs, store.CatalogChannel{IdentityKey: name, Name: name, Category: category, StreamURL: "http://streams.example/" + name})
	}
	if _, err := ta.st.ApplyCatalog(ctx, id, chs, ta.clock.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	entries, _ := ta.channels(c, "")
	if e := entries["Bravo"]; !e.Missing || !e.LeftOut {
		t.Errorf("Bravo %+v", e)
	}
	if e := entries["Charlie"]; !e.Missing || e.LeftOut {
		t.Errorf("Charlie %+v", e)
	}
	if e := entries["Alpha"]; e.Missing || e.LeftOut {
		t.Errorf("Alpha %+v", e)
	}
	body := ta.do("GET", "/admin/lineup", nil, c).Body.String()
	mustContain(t, "Live TV", body, "Left out</span>", "Missing</span>")
}
