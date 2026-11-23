// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// A fictional Xtream panel with five live categories of three channels
// each, a guide with a programme on every channel, and a count of the
// requests it answers by what they ask for.
type categoryPanel struct {
	mu    sync.Mutex
	asked map[string]int
	extra bool // lists a sixth category, Weather
}

var panelCategoryNames = []string{"News", "Sport", "Kids", "Films", "Music"}

func newCategoryPanel(t *testing.T) (*categoryPanel, *httptest.Server) {
	t.Helper()
	p := &categoryPanel{asked: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		defer p.mu.Unlock()
		q := r.URL.Query()
		what := r.URL.Path + "?" + q.Get("action")
		p.asked[what]++
		if q.Get("username") != "viewer" || q.Get("password") != panelPassword {
			io.WriteString(w, `{"user_info":{"auth":0}}`)
			return
		}
		names := panelCategoryNames
		if p.extra {
			names = append(append([]string{}, names...), "Weather")
		}
		switch what {
		case "/player_api.php?":
			io.WriteString(w, `{"user_info":{"auth":1,"status":"Active","max_connections":"1","allowed_output_formats":["ts"]}}`)
		case "/player_api.php?get_live_categories":
			var cats []string
			for i, name := range names {
				cats = append(cats, fmt.Sprintf(`{"category_id":"%d","category_name":%q}`, i+1, name))
			}
			io.WriteString(w, "["+strings.Join(cats, ",")+"]")
		case "/player_api.php?get_live_streams":
			var streams []string
			for i, name := range names {
				for j := 1; j <= 3; j++ {
					id := (i+1)*10 + j
					streams = append(streams, fmt.Sprintf(`{"name":"%s %d","stream_type":"live","stream_id":%d,
						"epg_channel_id":"c%d.example","category_id":"%d"}`, name, j, id, id, i+1))
				}
			}
			io.WriteString(w, "["+strings.Join(streams, ",")+"]")
		case "/xmltv.php?":
			start := time.Now().UTC().Truncate(time.Hour)
			io.WriteString(w, `<?xml version="1.0"?><tv>`)
			for i := range names {
				for j := 1; j <= 3; j++ {
					id := (i+1)*10 + j
					fmt.Fprintf(w, `<channel id="c%d.example"/><programme start="%s +0000" stop="%s +0000" channel="c%d.example"><title>Programme %d</title></programme>`,
						id, start.Format("20060102150405"), start.Add(time.Hour).Format("20060102150405"), id, id)
				}
			}
			io.WriteString(w, `</tv>`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return p, srv
}

func (p *categoryPanel) requests() map[string]int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := map[string]int{}
	for k, v := range p.asked {
		out[k] = v
	}
	return out
}

// storedChannels returns the names of the channels that a source brings,
// and of those that have programmes now.
func storedChannels(t *testing.T, st *store.Store, id int64) (names, withProgrammes []string) {
	t.Helper()
	ctx := context.Background()
	gcs, err := st.GuideChannels(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for _, c := range gcs {
		names = append(names, c.Name)
		ps, err := st.Programmes(ctx, c.ID, now.Add(-time.Hour), now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if len(ps) > 0 {
			withProgrammes = append(withProgrammes, c.Name)
		}
	}
	sort.Strings(names)
	sort.Strings(withProgrammes)
	return names, withProgrammes
}

// A source set to three of its categories keeps only their channels and
// their programmes, read with the same requests as one that brings every
// category (D135).
func TestSourceKeepsOnlyTickedCategories(t *testing.T) {
	ctx := context.Background()
	panel, srv := newCategoryPanel(t)
	st, r, logs := newStore(t)
	settings := xtreamSettings(srv)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	// Chosen before the first read, with nothing that it adds later
	// ticked: the first read keeps the categories alone.
	at := time.Now()
	if _, err := st.SetCategoryChoice(ctx, id, store.KindLive, true, false, at); err != nil {
		t.Fatal(err)
	}
	ch, err := r.Refresh(ctx, settings, id)
	if err != nil || ch.Added != 0 || ch.LeftOut != 15 {
		t.Fatalf("first read %+v, %v", ch, err)
	}
	if _, _, err := r.guide.ReadIfDue(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	if got := panel.requests()["/xmltv.php?"]; got != 0 {
		t.Errorf("the guide of a source that brings no channel was fetched %d times", got)
	}
	cats, total, err := st.SourceCategories(ctx, id, store.KindLive, "")
	if err != nil || total != 5 {
		t.Fatalf("categories %+v, %d, %v", cats, total, err)
	}
	for _, c := range cats {
		if c.Ticked || c.Items != 3 {
			t.Errorf("category %+v", c)
		}
	}

	if _, err := st.TickCategories(ctx, id, store.KindLive, map[string]bool{"News": true, "Kids": true, "Music": true},
		at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ch, err = r.Refresh(ctx, settings, id)
	if err != nil || ch.Added != 9 || ch.LeftOut != 6 {
		t.Fatalf("read with three categories %+v, %v", ch, err)
	}
	if _, _, err := r.guide.ReadIfDue(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	want := []string{"Kids 1", "Kids 2", "Kids 3", "Music 1", "Music 2", "Music 3", "News 1", "News 2", "News 3"}
	names, withProgrammes := storedChannels(t, st, id)
	if !reflect.DeepEqual(names, want) || !reflect.DeepEqual(withProgrammes, want) {
		t.Errorf("channels %v, with programmes %v", names, withProgrammes)
	}
	if !strings.Contains(logs.String(), "left_out=6") {
		t.Errorf("the log does not say what it left out:\n%s", logs.String())
	}

	// A new category is left out, as chosen; the requests are those of a
	// source that brings every category.
	panel.mu.Lock()
	panel.extra = true
	panel.mu.Unlock()
	if ch, err := r.Refresh(ctx, settings, id); err != nil || ch.LeftOut != 9 || ch.Added != 0 {
		t.Errorf("a read with Weather %+v, %v", ch, err)
	}
	got := panel.requests()
	if got["/player_api.php?"] != 3 || got["/player_api.php?get_live_categories"] != 3 ||
		got["/player_api.php?get_live_streams"] != 3 || got["/xmltv.php?"] != 1 || len(got) != 4 {
		t.Errorf("requests %v", got)
	}

	// Every category again: they all come, and the guide is due at once
	// for them.
	if _, err := st.SetCategoryChoice(ctx, id, store.KindLive, false, false, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if ch, err := r.Refresh(ctx, settings, id); err != nil || ch.Added != 9 || ch.LeftOut != 0 {
		t.Errorf("every category %+v, %v", ch, err)
	}
	if read, _, err := r.guide.ReadIfDue(ctx, settings, id); err != nil || !read {
		t.Errorf("the guide after a change of categories: read %v, %v", read, err)
	}
	if names, withProgrammes := storedChannels(t, st, id); len(names) != 18 || len(withProgrammes) != 18 {
		t.Errorf("channels %v, with programmes %v", names, withProgrammes)
	}
}
