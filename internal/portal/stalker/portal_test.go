// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/library"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/upstream"
)

// withPortal gives the API a Stalker portal's films and series, read a
// page at a time (D77), and the hub that plays them.
func (h *harness) withPortal(p *stalkertest.Portal) *httptest.Server {
	h.t.Helper()
	ctx := context.Background()
	src, err := h.store.AddSource(ctx, source.Settings{Name: "portal", Type: source.TypeStalker,
		URL: p.URL + "/stalker_portal/c/", Device: source.Device{MAC: stalkertest.MAC}, MaxConnections: 1})
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
	h.hub.SetSources(map[int64]hub.Source{src: {Name: "portal", MaxConnections: 1, Resolves: true}})
	h.hub.UseResolver(upstream.New(h.store, pool, log))
	front := httptest.NewServer(h.hub)
	h.t.Cleanup(front.Close)
	return front
}

// A portal's shelf's pages are the portal's (D77): its films in vod, its
// series in the series module, and none of its series among its films.
func TestPortalPages(t *testing.T) {
	h := newHarness(t)
	p := stalkertest.New(t)
	p.Series = stalkertest.Inline
	p.MoreFilms = 10 // Drama: 2 series and 20 films
	front := h.withPortal(p)
	h.approve(macA)
	token := h.handshake(macA)

	ids, titles := h.categories("vod", macA, token)
	if !reflect.DeepEqual(titles, []string{"All", "Drama", "Comedy"}) {
		t.Fatalf("categories %q", titles)
	}
	query := "type=vod&action=get_ordered_list&genre=*&sortby=name&fav=0&category=" + ids["Drama"]
	first := h.titles(query+"&p=1", macA, token)
	if first.TotalItems != 22 || len(first.Data) != 12 || first.Data[0].Name != "Example Film 02" ||
		first.Data[0].CategoryID != ids["Drama"] || strings.Contains(itemNames(first.Data), "Example Coast") {
		t.Errorf("page 1: %d, %q", first.TotalItems, itemNames(first.Data))
	}
	second := h.titles(query+"&p=2", macA, token)
	if len(second.Data) != 8 || second.Data[7].Name != "Example Film 110" {
		t.Errorf("page 2: %q", itemNames(second.Data))
	}
	if n := p.Calls("vod/get_ordered_list"); n != 2 {
		t.Errorf("%d pages read, want 2", n)
	}
	// What was read is searched, with the rest.
	found := h.titles("type=vod&action=get_ordered_list&genre=*&fav=0&search=film+10&category="+ids["Drama"]+"&p=1", macA, token)
	if itemNames(found.Data) != "Example Film 10, Example Film 101, Example Film 102, Example Film 103, Example Film 104, Example Film 105, Example Film 106, Example Film 107, Example Film 108, Example Film 109" {
		t.Errorf("found %q", itemNames(found.Data))
	}

	// A film plays through the hub, which asks the portal for its link.
	answer := h.link("vod", first.Data[0].Cmd, "", macA, token)
	if code, body := fetch(t, front, answer.Cmd); code != 206 || body != string(stalkertest.Content("1002")[100:200]) {
		t.Errorf("film 1002: %d %q", code, body)
	}
}
