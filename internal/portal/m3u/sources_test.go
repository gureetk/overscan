// SPDX-License-Identifier: AGPL-3.0-or-later

package m3u

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// A playlist whose account uses only some sources lists, guides and plays
// only their channels (D114).
func TestAccountSources(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	two, err := h.store.AddSource(ctx, source.Settings{Name: "two", URL: "http://provider.example/two.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.ApplyCatalog(ctx, two, []store.CatalogChannel{
		{IdentityKey: "z", Name: "Zulu", Category: "News", StreamURL: "http://streams.example/z.ts"}}, h.now); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetAccountSources(ctx, store.HouseholdAccount, store.Sources{Only: true, IDs: []int64{two}}); err != nil {
		t.Fatal(err)
	}
	_, body := h.get("/playlist/" + h.token + ".m3u")
	if !strings.Contains(body, ",Zulu") || strings.Contains(body, "Alpha") || strings.Contains(body, "Bravo") {
		t.Errorf("the playlist:\n%s", body)
	}
	_, packed := h.get("/epg/" + h.token + ".xml.gz")
	zr, err := gzip.NewReader(strings.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	guide, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(guide), "Zulu") || strings.Contains(string(guide), "<programme") {
		t.Errorf("the guide:\n%s", guide)
	}
	for _, p := range []string{
		fmt.Sprintf("/playlist/%s/%d.ts", h.token, h.ids[`Alpha "One"`]),
		fmt.Sprintf("/playlist/%s/archive/%d.ts?start=%d&end=%d", h.token, h.ids["Bravo & Co"],
			h.now.Add(-2*60*60).Unix(), h.now.Add(-60*60).Unix()),
	} {
		if resp, _ := h.get(p); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: %s", p, resp.Status)
		}
	}
}
