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
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/store"
)

// fakeGuides stands in for what reads the guides of the household's own.
type fakeGuides struct {
	mu      sync.Mutex
	changes int
	read    []int64
	readErr error
	slow    bool // a read outlasts the admin's wait
	reading int64
	// options are what channels' guides can be chosen from: Search finds
	// those whose names hold the text, and Suggest those that suggest
	// gives by channel.
	options []guide.Option
	suggest map[int64][]guide.Option
}

func (f *fakeGuides) Suggest(_ context.Context, channels []guide.SuggestFor, limit int) (map[int64][]guide.Option, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int64][]guide.Option{}
	for _, c := range channels {
		os := f.suggest[c.ChannelID]
		if limit > 0 && len(os) > limit {
			os = os[:limit]
		}
		out[c.ChannelID] = os
	}
	return out, nil
}

func (f *fakeGuides) Search(_ context.Context, text string, _ int) ([]guide.Option, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []guide.Option
	for _, o := range f.options {
		if strings.Contains(strings.ToLower(o.Name()), strings.ToLower(text)) {
			out = append(out, o)
		}
	}
	return out, nil
}

func (f *fakeGuides) ReadNow(ctx context.Context, id int64) (guide.OwnResult, error) {
	f.mu.Lock()
	f.read = append(f.read, id)
	slow, err := f.slow, f.readErr
	f.mu.Unlock()
	if slow {
		<-ctx.Done()
		return guide.OwnResult{}, ctx.Err()
	}
	return guide.OwnResult{Listed: 1200, Fills: 34, ByName: 5, Channels: 30, Programmes: 4100}, err
}

func (f *fakeGuides) Changed() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.changes++
}

func (f *fakeGuides) Reading() int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reading
}

func (f *fakeGuides) changed() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.changes
}

// withGuides gives the test admin the lineup of withLineup and a fake
// reader of guides.
func (ta *testAdmin) withGuides() (*http.Cookie, string, *fakeGuides) {
	ta.t.Helper()
	c, h := ta.withLineup()
	f := &fakeGuides{}
	ta.UseGuides(f)
	return c, h[1], f
}

const secretGuide = "http://guides.example:8080/epg.xml.gz?token=s3cret"

func TestGuidesPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withGuides()
	ctx := context.Background()
	body := ta.do("GET", "/admin/guides", nil, c).Body.String()
	mustContain(t, "the empty page", body, `<a href="/admin/guides" aria-current="page" title="Guides">`, "<h1>Guides</h1>",
		"No guides of your own yet", "Add a guide", `name="address"`)

	// Add one through the form.
	hx := []string{"HX-Request", "true"}
	rec := ta.do("POST", "/admin/guides", url.Values{"csrf": {csrf}, "name": {"extra"}, "address": {secretGuide},
		"public_only": {"1"}}, c, hx...)
	mustContain(t, "after adding", rec.Body.String(), "Added extra. Overscan is reading it now", `<h2 id="guide-1-name">extra</h2>`,
		"guides.example:8080", "Not read yet")
	if strings.Contains(rec.Body.String(), "s3cret") {
		t.Error("the page shows the saved address")
	}
	if f.changed() != 1 {
		t.Errorf("%d changes, want 1", f.changed())
	}
	g, err := ta.st.Guide(ctx, 1)
	if err != nil || !g.PublicOnly || g.Name != "extra" {
		t.Fatalf("the guide %+v, %v", g, err)
	}
	// Mistakes show the form again, as it was sent.
	rec = ta.do("POST", "/admin/guides", url.Values{"csrf": {csrf}, "name": {"extra"}, "address": {"http://other.example/"}}, c, hx...)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Another guide has that name.") {
		t.Errorf("a name taken: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/guides", url.Values{"csrf": {csrf}, "name": {"second"}, "address": {"guide.xml"}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must be an http or https address") ||
		!strings.Contains(rec.Body.String(), `value="guide.xml"`) {
		t.Errorf("a bad address: status %d", rec.Code)
	}
	rec = ta.do("POST", "/admin/guides", url.Values{"csrf": {csrf}, "name": {"second"}}, c, hx...)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Enter the guide&#39;s address") {
		t.Errorf("no address: status %d", rec.Code)
	}

	// A read, its result on the card, and a second guide in a file.
	if _, err := ta.st.AddGuide(ctx, store.GuideSettings{Name: "second", Address: "/srv/guides/second.xml"}, ta.clock); err != nil {
		t.Fatal(err)
	}
	w, err := ta.st.BeginGuideRead(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.ListChannels(ctx, []store.GuideListing{{XMLTVID: "a.example", Names: []string{"A"}}}, ta.clock); err != nil {
		t.Fatal(err)
	}
	if err := w.Commit(ctx, ta.clock, "x", store.GuideRead{Listed: 1}); err != nil {
		t.Fatal(err)
	}
	f.reading = 2
	body = ta.do("GET", "/admin/guides", nil, c).Body.String()
	mustContain(t, "with two guides", body, "None of the channels without programmes of their own",
		"1 channel; it keeps none of their programmes, since none of them fills a channel",
		"second.xml, a file on this server", "Reading it now", "First: where two guides could fill a channel, this one does",
		"2nd of 2", "Move down", "Move up")

	// Its own page changes it, keeping the address unless one is given.
	body = ta.do("GET", "/admin/guides/1", nil, c).Body.String()
	mustContain(t, "its page", body, "<h1>Change extra</h1>", "Saved: guides.example:8080", "Leave empty to keep the saved address")
	rec = ta.do("POST", "/admin/guides/1", url.Values{"csrf": {csrf}, "name": {"first"}, "user_agent": {"Player/2"}}, c, hx...)
	mustContain(t, "saved", rec.Body.String(), "Saved first.")
	if s, err := ta.st.GuideSettings(ctx, 1); err != nil || s.Address != secretGuide || s.Name != "first" || s.UserAgent != "Player/2" || s.PublicOnly {
		t.Errorf("after saving: %+v, %v", s, err)
	}
	if rec := ta.do("GET", "/admin/guides/99", nil, c); rec.Code != http.StatusNotFound {
		t.Errorf("no such guide: status %d", rec.Code)
	}
}

func TestGuideActions(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withGuides()
	ctx := context.Background()
	for _, name := range []string{"one", "two"} {
		if _, err := ta.st.AddGuide(ctx, store.GuideSettings{Name: name, Address: secretGuide}, ta.clock); err != nil {
			t.Fatal(err)
		}
	}
	hx := []string{"HX-Request", "true"}
	act := func(id int64, action string) string {
		t.Helper()
		return ta.do("POST", fmt.Sprintf("/admin/guides/%d/%s", id, action), url.Values{"csrf": {csrf}}, c, hx...).Body.String()
	}
	mustContain(t, "read now", act(1, "read"), "Read one: it lists 1,200 channels, and fills 34 channels.")
	f.readErr = errors.New("the guide's server answered 404 Not Found")
	mustContain(t, "a failed read", act(1, "read"), "The guide could not be read: the guide&#39;s server answered 404 Not Found.")
	f.readErr = nil
	f.slow = true
	was := guideWait
	guideWait = 50 * time.Millisecond
	defer func() { guideWait = was }()
	mustContain(t, "a slow read", act(1, "read"), "Overscan is still reading one.")
	f.slow = false

	mustContain(t, "move down", act(1, "down"), "Moved one down.")
	if g, _ := ta.st.Guide(ctx, 1); g.Position != 2 {
		t.Errorf("moved down: position %d", g.Position)
	}
	mustContain(t, "move up", act(1, "up"), "Moved one up.")
	mustContain(t, "turn off", act(1, "off"), "Turned one off")
	if g, _ := ta.st.Guide(ctx, 1); g.Enabled {
		t.Error("still on")
	}
	mustContain(t, "turn on", act(1, "on"), "Turned one on.")
	mustContain(t, "remove", act(1, "delete"), "Removed one.")
	if gs, _ := ta.st.Guides(ctx); len(gs) != 1 || gs[0].Name != "two" || gs[0].Position != 1 {
		t.Errorf("after removing: %+v", gs)
	}
	if f.changed() != 5 {
		t.Errorf("%d changes, want 5", f.changed())
	}
	if rec := ta.do("POST", "/admin/guides/1/read", url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("a removed guide: status %d", rec.Code)
	}
	if rec := ta.do("POST", "/admin/guides/2/explode", url.Values{"csrf": {csrf}}, c, hx...); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown action: status %d", rec.Code)
	}
	if rec := ta.do("POST", "/admin/guides/2/off", url.Values{"csrf": {"wrong"}}, c); rec.Code != http.StatusForbidden {
		t.Errorf("without the token: status %d", rec.Code)
	}
}

func TestGuidesWithoutAReader(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	if _, err := ta.st.AddGuide(context.Background(), store.GuideSettings{Name: "one", Address: secretGuide}, ta.clock); err != nil {
		t.Fatal(err)
	}
	rec := ta.do("POST", "/admin/guides/1/read", url.Values{"csrf": {h[1]}}, c, "HX-Request", "true")
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "cannot read guides") {
		t.Errorf("status %d", rec.Code)
	}
}

func TestGuidesAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, csrf, f := ta.withGuides()
	h := []string{csrfHeader, csrf}

	rec := ta.apiDo("POST", "guides", `{"name": "extra", "address": "`+secretGuide+`", "user_agent": "Player/1", "public_only": true}`, c, h...)
	var added apiOwnGuide
	if err := json.Unmarshal(rec.Body.Bytes(), &added); err != nil || rec.Code != http.StatusCreated || added.Name != "extra" ||
		added.Host != "guides.example:8080" || added.Address != "saved" || !added.PublicOnly || !added.Enabled || added.Position != 1 ||
		added.ReadAt != nil || strings.Contains(rec.Body.String(), "s3cret") {
		t.Fatalf("add: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "guides", `{"name": "extra", "address": "http://other.example/"}`, c, h...), http.StatusConflict, "name_taken")
	apiErrorOf(t, ta.apiDo("POST", "guides", `{"name": "third"}`, c, h...), http.StatusBadRequest, "bad_address")
	apiErrorOf(t, ta.apiDo("POST", "guides", `{"name": "third", "address": "ftp://guide.example/"}`, c, h...), http.StatusBadRequest, "bad_address")
	apiErrorOf(t, ta.apiDo("POST", "guides", `{"name": "Third", "address": "/srv/g.xml"}`, c, h...), http.StatusBadRequest, "bad_name")
	apiErrorOf(t, ta.apiDo("POST", "guides", `{"name": "third", "address": "/srv/g.xml", "position": 1}`, c, h...), http.StatusBadRequest, "bad_position")
	apiErrorOf(t, ta.apiDo("POST", "guides", `{"name": "third", "address": "/srv/g.xml", "url": "x"}`, c, h...), http.StatusBadRequest, "bad_json")

	rec = ta.apiDo("POST", "guides", `{"name": "second", "address": "/srv/guides/second.xml", "enabled": false}`, c, h...)
	var second apiOwnGuide
	if err := json.Unmarshal(rec.Body.Bytes(), &second); err != nil || second.Enabled || second.Position != 2 || second.Host != "file second.xml" {
		t.Fatalf("add a second: status %d, %s", rec.Code, rec.Body)
	}
	rec = ta.apiDo("GET", "guides", "", c)
	var list struct {
		Guides       []apiOwnGuide
		WithoutGuide int `json:"without_guide"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Guides) != 2 || list.Guides[0].Name != "extra" ||
		list.WithoutGuide != 4 {
		t.Fatalf("list: status %d, %s", rec.Code, rec.Body)
	}

	path := fmt.Sprintf("guides/%d", second.ID)
	rec = ta.apiDo("PATCH", path, `{"enabled": true, "position": 1, "user_agent": "Player/2"}`, c, h...)
	var changed apiOwnGuide
	if err := json.Unmarshal(rec.Body.Bytes(), &changed); err != nil || !changed.Enabled || changed.Position != 1 || changed.UserAgent != "Player/2" {
		t.Errorf("change: status %d, %s", rec.Code, rec.Body)
	}
	if s, _ := ta.st.GuideSettings(context.Background(), second.ID); s.Address != "/srv/guides/second.xml" {
		t.Errorf("the change lost the address: %+v", s)
	}
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"address": ""}`, c, h...), http.StatusBadRequest, "bad_address")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"position": 0}`, c, h...), http.StatusBadRequest, "bad_position")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{}`, c, h...), http.StatusBadRequest, "bad_json")
	apiErrorOf(t, ta.apiDo("PATCH", path, `{"name": "extra"}`, c, h...), http.StatusConflict, "name_taken")
	apiErrorOf(t, ta.apiDo("PATCH", "guides/9999", `{"enabled": false}`, c, h...), http.StatusNotFound, "not_found")
	if rec = ta.apiDo("GET", path, "", c); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name": "second"`) {
		t.Errorf("get: status %d, %s", rec.Code, rec.Body)
	}

	rec = ta.apiDo("POST", path+"/read", "", c, h...)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"fills": 34`) || !strings.Contains(rec.Body.String(), `"reading": false`) {
		t.Errorf("read: status %d, %s", rec.Code, rec.Body)
	}
	f.slow = true
	was := guideWait
	guideWait = 50 * time.Millisecond
	rec = ta.apiDo("POST", path+"/read", "", c, h...)
	guideWait = was
	f.slow = false
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"reading": true`) {
		t.Errorf("a slow read: status %d, %s", rec.Code, rec.Body)
	}
	f.readErr = guide.ErrBusy
	apiErrorOf(t, ta.apiDo("POST", path+"/read", "", c, h...), http.StatusServiceUnavailable, "busy")
	f.readErr = errors.New("the guide is not XMLTV")
	apiErrorOf(t, ta.apiDo("POST", path+"/read", "", c, h...), http.StatusBadGateway, "guide_failed")
	apiErrorOf(t, ta.apiDo("POST", "guides/9999/read", "", c, h...), http.StatusNotFound, "not_found")

	if rec = ta.apiDo("DELETE", path, "", c, h...); rec.Code != http.StatusNoContent {
		t.Errorf("delete: status %d, %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("DELETE", path, "", c, h...), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("PATCH", fmt.Sprintf("guides/%d", added.ID), `{"enabled": false}`, c), http.StatusForbidden, "csrf")
}
