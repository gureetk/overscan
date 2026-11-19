// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"testing"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/upstream"
)

// withPortal gives the harness a Stalker portal's films and series, a
// library that reads them a page at a time, and a hub that plays them.
// It returns a token of an approved box.
func (h *harness) withPortal(p *stalkertest.Portal) string {
	h.t.Helper()
	ctx := context.Background()
	src, err := h.store.AddSource(ctx, source.Settings{Name: "portal", Type: source.TypeStalker,
		URL: p.URL + "/stalker_portal/c/", Device: source.Device{MAC: stalkertest.MAC}})
	if err != nil {
		h.t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	pool := stalker.NewPool(nil)
	lib := library.New(h.store, log, "test")
	lib.UseStalker(pool)
	runCtx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		lib.Run(runCtx)
		close(done)
	}()
	h.t.Cleanup(func() {
		cancel()
		<-done
	})
	h.api.UseLibrary(lib)
	hb := hub.New(h.store, log, "test", map[int64]hub.Source{src: {Name: "portal", MaxConnections: 1, Resolves: true}})
	hb.UseResolver(upstream.New(h.store, pool, log))
	h.api.hub = hb
	mux := http.NewServeMux()
	mux.Handle(Prefix, h.api)
	mux.Handle("GET /vod/", hb)
	h.front.Config.Handler = mux
	h.approve(macA)
	return h.session(macA).Token
}

// A portal's shelf comes a part at a time, which says where the next one
// starts, and holds its series among its films where it has no series
// module (D77).
func TestPortalShelf(t *testing.T) {
	h := newHarness(t)
	p := stalkertest.New(t)
	p.Series = stalkertest.Inline
	p.MoreFilms = 60 // Drama: 2 series, 10 films and these, 72 in all
	token := h.withPortal(p)

	var movies libraryResponse
	if code := h.call("GET", Prefix+"movies", token, "", &movies); code != http.StatusOK || movies.State != libraryReady {
		t.Fatalf("movies: %d %+v", code, movies)
	}
	drama := shelfID(t, movies, "Drama")
	for _, s := range movies.Shelves {
		if s.ID == drama && s.Count != -1 {
			t.Errorf("Drama counts %d before a part of it was read", s.Count)
		}
	}
	var part shelfResponse
	if code := h.call("GET", Prefix+"movies/shelves/"+drama+"?count=200&order=name", token, "", &part); code != http.StatusOK {
		t.Fatalf("part: %d", code)
	}
	if part.Total != 72 || part.From != 0 || len(part.Titles) != 56 || part.Next != 56 ||
		part.Titles[0].Kind != "series" || part.Titles[0].Name != "Example Coast" || part.Titles[2].Kind != "movie" {
		t.Errorf("first part: total %d, next %d, %d titles, first %+v", part.Total, part.Next, len(part.Titles), part.Titles[0])
	}
	if code := h.call("GET", Prefix+"movies/shelves/"+drama+"?count=200&from="+strconv.Itoa(part.Next), token, "", &part); code != http.StatusOK ||
		len(part.Titles) != 16 || part.Next != -1 || part.From != 56 {
		t.Errorf("second part: %d, %d titles, next %d", code, len(part.Titles), part.Next)
	}
	h.call("GET", Prefix+"movies", token, "", &movies)
	for _, s := range movies.Shelves {
		if s.ID == drama && s.Count != 72 {
			t.Errorf("Drama counts %d", s.Count)
		}
	}

	// A series on a shelf of films opens as a series.
	h.call("GET", Prefix+"movies/shelves/"+drama+"?count=1", token, "", &part)
	var series titleResponse
	id := strconv.FormatInt(part.Titles[0].ID, 10)
	if code := h.call("GET", Prefix+"series/"+id, token, "", &series); code != http.StatusOK || series.Kind != "series" ||
		len(series.Seasons) != 1 || len(series.Seasons[0].Episodes) != 5 {
		t.Errorf("series: %d %+v", code, series)
	}
	if code := h.call("GET", Prefix+"movies/"+id, token, "", nil); code != http.StatusNotFound {
		t.Errorf("a series as a film: %d", code)
	}
}
