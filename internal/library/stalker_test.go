// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// newPortalLibrary returns a library that reads p as the box that its
// provider registered.
func newPortalLibrary(t *testing.T, p *stalkertest.Portal) (*Library, *store.Store, *syncBuffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	if _, err := st.AddSource(context.Background(), source.Settings{Name: "portal", Type: source.TypeStalker,
		URL: p.URL + "/stalker_portal/c/", Device: source.Device{MAC: stalkertest.MAC}}); err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	l := New(st, slog.New(slog.NewTextHandler(logs, nil)), "test")
	l.UseStalker(stalker.NewPool(nil))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return l, st, logs
}

// portalShelves returns the portal's shelves of a kind by name, once its
// categories are read.
func portalShelves(t *testing.T, l *Library, st *store.Store, kind string) map[string]store.PortalCategory {
	t.Helper()
	ctx := context.Background()
	state, err := l.Want(ctx, kind, 5*time.Second)
	if err != nil || !state.Ready() {
		t.Fatalf("want %s: %+v, %v", kind, state, err)
	}
	shelves, err := st.Shelves(ctx, kind)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.PortalCategory{}
	for _, s := range shelves {
		cat, err := st.PortalCategory(ctx, kind, s.ID)
		if err != nil || !s.Portal {
			t.Fatalf("shelf %+v: %v", s, err)
		}
		out[s.Name] = cat
	}
	return out
}

func partNames(p Part) []string {
	var out []string
	for _, t := range p.Titles {
		out = append(out, t.Kind+" "+t.Name)
	}
	return out
}

func TestPortalFilms(t *testing.T) {
	p := stalkertest.New(t)
	p.MoreFilms = 40 // Drama: 10 films and these 40, four pages
	l, st, logs := newPortalLibrary(t, p)
	ctx := context.Background()
	shelves := portalShelves(t, l, st, store.KindMovie)
	drama, comedy := shelves["Drama"], shelves["Comedy"]
	if len(shelves) != 2 || drama.Total != -1 || drama.Key != "vod:1" || comedy.Key != "vod:2" {
		t.Fatalf("shelves %+v", shelves)
	}
	if n := p.Calls("vod/get_ordered_list"); n != 0 {
		t.Errorf("%d pages read before a device asked for any", n)
	}

	// A part reads the pages that it covers.
	part, err := l.PortalPart(ctx, drama, 0, 20)
	if err != nil {
		t.Fatal(err)
	}
	if part.Total != 50 || part.Next != 20 || len(part.Titles) != 20 || part.Titles[0].Name != "Example Film 02" ||
		part.Titles[19].Name != "Example Film 110" || p.Calls("vod/get_ordered_list") != 2 {
		t.Errorf("part %q, total %d, next %d, %d pages read", partNames(part), part.Total, part.Next,
			p.Calls("vod/get_ordered_list"))
	}
	// The next part reads only the pages not read yet, and at most four.
	part, err = l.PortalPart(ctx, drama, 20, 200)
	if err != nil || part.Next != -1 || len(part.Titles) != 30 || p.Calls("vod/get_ordered_list") != 4 {
		t.Errorf("the rest: %d titles, next %d, %v, %d pages read", len(part.Titles), part.Next, err,
			p.Calls("vod/get_ordered_list"))
	}
	if part, _ := l.PortalPart(ctx, drama, 50, 14); len(part.Titles) != 0 || part.Next != -1 ||
		p.Calls("vod/get_ordered_list") != 4 {
		t.Errorf("past the end: %q, next %d", partNames(part), part.Next)
	}
	big := stalkertest.New(t)
	big.MoreFilms = 100
	lb, sb, _ := newPortalLibrary(t, big)
	bigDrama := portalShelves(t, lb, sb, store.KindMovie)["Drama"]
	if part, err := lb.PortalPart(ctx, bigDrama, 0, 1000); err != nil || len(part.Titles) != 4*14 ||
		part.Next != 4*14 || big.Calls("vod/get_ordered_list") != 4 {
		t.Errorf("a long part: %d titles, next %d, %v", len(part.Titles), part.Next, err)
	}

	// The shelf counts what the portal said.
	if cat, _ := st.PortalCategory(ctx, store.KindMovie, drama.ID); cat.Total != 50 {
		t.Errorf("Drama's total %d", cat.Total)
	}

	// A page is kept a day.
	l.now = func() time.Time { return time.Now().Add(ListTTL - time.Minute) }
	if _, err := l.PortalPart(ctx, drama, 0, 14); err != nil || p.Calls("vod/get_ordered_list") != 4 {
		t.Errorf("a page read again within a day: %v, %d", err, p.Calls("vod/get_ordered_list"))
	}
	l.now = func() time.Time { return time.Now().Add(ListTTL + time.Minute) }
	if _, err := l.PortalPart(ctx, drama, 0, 14); err != nil || p.Calls("vod/get_ordered_list") != 5 {
		t.Errorf("a page a day old: %v, %d", err, p.Calls("vod/get_ordered_list"))
	}

	// A film's details are what its page said, without asking the portal.
	part, _ = l.PortalPart(ctx, drama, 0, 14)
	before := p.Calls("vod/get_ordered_list")
	o, err := l.Open(ctx, part.Titles[0].ID)
	if err != nil || o.Details == nil || o.Details.Plot != "A fictional film." || o.Details.Cast != "A. Actor" ||
		o.Details.Duration != 95*60 || o.Year != 2002 || o.Rating != 74 {
		t.Errorf("opened %+v, %v", o, err)
	}
	if n := p.Calls("vod/get_ordered_list"); n != before {
		t.Errorf("opening a film read %d pages", n-before)
	}
	item, err := st.PlayableMovie(ctx, part.Titles[0].ID)
	if play, ok := stalker.ParsePlay(item.StreamURL); err != nil || !ok || play != (stalker.Play{Cmd: "/media/1002.mpg"}) {
		t.Errorf("playable %+v, %v", item, err)
	}

	// Comedy holds the film whose file is asked for apart.
	part, _ = l.PortalPart(ctx, comedy, 0, 14)
	if got := partNames(part); len(got) != 10 || got[1] != "movie Example Film 03" {
		t.Fatalf("Comedy %q", got)
	}
	item, _ = st.PlayableMovie(ctx, part.Titles[1].ID)
	if play, ok := stalker.ParsePlay(item.StreamURL); !ok || play != (stalker.Play{Film: "1003"}) {
		t.Errorf("film 1003 plays with %q", item.StreamURL)
	}
	if log := logs.String(); !strings.Contains(log, `msg="read a page of a portal's shelf" source=portal shelf=Drama page=1 titles=14 total=50`) ||
		strings.Contains(log, stalkertest.MAC) {
		t.Errorf("log:\n%s", log)
	}
}

func TestPortalSeriesModule(t *testing.T) {
	p := stalkertest.New(t)
	l, st, _ := newPortalLibrary(t, p)
	ctx := context.Background()
	shelves := portalShelves(t, l, st, store.KindSeries)
	docs, ok := shelves["Documentary"]
	if len(shelves) != 1 || !ok || docs.Key != "series:5" {
		t.Fatalf("shelves %+v", shelves)
	}
	part, err := l.PortalPart(ctx, docs, 0, 14)
	if got := partNames(part); err != nil || !reflect.DeepEqual(got, []string{"series Example Coast", "series Example Harbour"}) {
		t.Fatalf("part %q, %v", got, err)
	}
	o, err := l.Open(ctx, part.Titles[0].ID)
	if err != nil || o.ReadError != "" || len(o.Episodes) != 5 || len(o.Details.Seasons) != 2 {
		t.Fatalf("opened %+v, %v", o, err)
	}
	e := o.Episodes[4]
	item, err := st.PlayableEpisode(ctx, e.ID)
	if play, ok := stalker.ParsePlay(item.StreamURL); err != nil || !ok || e.Season != 2 || e.Number != 2 ||
		play != (stalker.Play{Cmd: "/media/2001_2.mpg", Episode: 2}) {
		t.Errorf("episode %+v plays with %q, %v", e, item.StreamURL, err)
	}

	// Films are in vod, without series.
	films := portalShelves(t, l, st, store.KindMovie)
	part, _ = l.PortalPart(ctx, films["Drama"], 0, 14)
	if got := partNames(part); len(got) != 10 || strings.Contains(strings.Join(got, ","), "series") {
		t.Errorf("Drama %q", got)
	}
}

func TestPortalSeriesAmongFilms(t *testing.T) {
	for _, shape := range []string{stalkertest.Inline, stalkertest.OnRequest} {
		t.Run(shape, func(t *testing.T) {
			p := stalkertest.New(t)
			p.Series = shape
			l, st, _ := newPortalLibrary(t, p)
			ctx := context.Background()
			if shelves := portalShelves(t, l, st, store.KindSeries); len(shelves) != 0 {
				t.Errorf("a portal without a series module has series shelves %+v", shelves)
			}
			drama := portalShelves(t, l, st, store.KindMovie)["Drama"]
			part, err := l.PortalPart(ctx, drama, 0, 14)
			got := partNames(part)
			if err != nil || len(got) != 12 || got[0] != "series Example Coast" || got[2] != "movie Example Film 02" {
				t.Fatalf("Drama %q, %v", got, err)
			}
			o, err := l.Open(ctx, part.Titles[0].ID)
			if err != nil || o.ReadError != "" || o.Kind != store.KindSeries {
				t.Fatalf("opened %+v, %v", o, err)
			}
			var eps []string
			for _, e := range o.Episodes {
				item, err := st.PlayableEpisode(ctx, e.ID)
				if err != nil {
					t.Fatal(err)
				}
				play, _ := stalker.ParsePlay(item.StreamURL)
				eps = append(eps, fmt.Sprintf("S%dE%d %s %d", e.Season, e.Number, play.Cmd, play.Episode))
			}
			want := []string{"S1E1 /media/2001.mpg 1", "S1E2 /media/2001.mpg 2", "S1E3 /media/2001.mpg 3",
				"S1E4 /media/2001.mpg 4", "S1E5 /media/2001.mpg 5"}
			if shape == stalkertest.OnRequest {
				want = []string{"S1E1 /media/2001101.mpg 0", "S1E2 /media/2001102.mpg 0", "S1E3 /media/2001103.mpg 0",
					"S2E1 /media/2001201.mpg 0", "S2E2 /media/2001202.mpg 0"}
				if o.Episodes[1].Name != "Episode 2" || o.Episodes[1].Duration != 42*60 {
					t.Errorf("episode %+v", o.Episodes[1])
				}
			}
			if !reflect.DeepEqual(eps, want) {
				t.Errorf("episodes %q, want %q", eps, want)
			}
		})
	}
}

func TestPortalPartFailures(t *testing.T) {
	p := stalkertest.New(t)
	l, st, _ := newPortalLibrary(t, p)
	ctx := context.Background()
	drama := portalShelves(t, l, st, store.KindMovie)["Drama"]
	if _, err := l.PortalPart(ctx, drama, 0, 14); err != nil {
		t.Fatal(err)
	}
	// The portal refuses the box from now on: a page read before stays,
	// and one never read is an error.
	p.ForgetToken()
	blocked := stalkertest.New(t)
	blocked.Blocked = true
	if err := st.UpdateSource(ctx, drama.SourceID, source.Settings{Name: "portal", Type: source.TypeStalker,
		URL: blocked.URL + "/stalker_portal/c/", Device: source.Device{MAC: stalkertest.MAC}}); err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return time.Now().Add(ListTTL + time.Minute) }
	part, err := l.PortalPart(ctx, drama, 0, 14)
	if err != nil || len(part.Titles) != 10 {
		t.Errorf("a page that cannot be read again: %d titles, %v", len(part.Titles), err)
	}
	comedy := portalShelvesNoWant(t, st, store.KindMovie)["Comedy"]
	if _, err := l.PortalPart(ctx, comedy, 0, 14); err == nil {
		t.Error("a page never read and that cannot be read is no error")
	}
}

// portalShelvesNoWant returns the portal's shelves of a kind by name, as
// the store has them.
func portalShelvesNoWant(t *testing.T, st *store.Store, kind string) map[string]store.PortalCategory {
	t.Helper()
	ctx := context.Background()
	shelves, err := st.Shelves(ctx, kind)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]store.PortalCategory{}
	for _, s := range shelves {
		if cat, err := st.PortalCategory(ctx, kind, s.ID); err == nil {
			out[s.Name] = cat
		}
	}
	return out
}

// A portal's film that it marks for adults is locked when the setting says
// so (D86).
func TestPortalAdultMarks(t *testing.T) {
	p := stalkertest.New(t)
	p.Censored = []string{"1002"}
	l, st, _ := newPortalLibrary(t, p)
	ctx := context.Background()
	if err := st.SetAdultMarks(ctx, store.AdultLock); err != nil {
		t.Fatal(err)
	}
	drama := portalShelves(t, l, st, store.KindMovie)["Drama"]
	part, err := l.PortalPart(ctx, drama, 0, 14)
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range part.Titles {
		if want := title.Name == "Example Film 02"; title.Locked != want {
			t.Errorf("%s: locked %v", title.Name, title.Locked)
		}
	}
}
