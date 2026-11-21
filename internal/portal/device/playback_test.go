// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// TestPlaybackReport logs how a film started on the box, as the page tells
// it, and refuses what the page could not have sent.
func TestPlaybackReport(t *testing.T) {
	h := newHarness(t)
	h.approve(macA)
	token := h.session(macA).Token
	ctx := context.Background()
	films, err := h.store.AddSource(ctx, source.Settings{Name: "films", Type: source.TypeXtream, URL: "http://provider.example",
		Username: "viewer", Password: "fictional"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.ApplyLibrary(ctx, films, store.KindMovie, nil, []store.LibraryTitle{
		{Key: "5001", Name: "The Long Field", Container: "mkv"}}, h.now); err != nil {
		t.Fatal(err)
	}
	found, err := h.store.SearchTitles(ctx, "Long", 5, store.EverySource)
	if err != nil || len(found) != 1 {
		t.Fatalf("titles %+v, %v", found, err)
	}
	id := found[0].ID
	report := func(outcome, extra string) string {
		return fmt.Sprintf(`{"kind":"movie","id":%d,"outcome":%q,"link_ms":2300,"moved_ms":5400,"event_ms":-1,"wait_ms":12000,`+
			`"nudged":true,"converted":false,"from":60,"events":"\"2\" \"7\""%s}`, id, outcome, extra)
	}
	var answer map[string]any
	if code := h.call("POST", Prefix+"playback", token, report("began", ""), &answer); code != http.StatusOK || len(answer) != 0 {
		t.Fatalf("status %d, %v", code, answer)
	}
	if code := h.call("POST", Prefix+"playback", token, report("stalled", ""), nil); code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	logs := h.logs.String()
	if !strings.Contains(logs, `level=INFO msg="the box began playing a title once the page paused it and played it again" title="The Long Field" device=`) ||
		!strings.Contains(logs, `from=60 converted=false link_ms=2300 moved_ms=5400 event_ms=-1 nudged=true events="\"2\" \"7\""`) ||
		!strings.Contains(logs, `level=WARN msg="a title stood still on the box and did not go on; the page starts it again" title="The Long Field"`) ||
		!strings.Contains(logs, `wait_ms=12000`) {
		t.Errorf("logs:\n%s", logs)
	}

	for _, bad := range []string{
		report("finished", ""),
		report("began", `,"extra":1`),
		strings.Replace(report("began", ""), `"kind":"movie"`, `"kind":"channel"`, 1),
		strings.Replace(report("began", ""), `"link_ms":2300`, `"link_ms":-5`, 1),
		strings.Replace(report("began", ""), `\"2\" \"7\"`, `\u0007`, 1),
		strings.Replace(report("began", ""), `\"2\" \"7\"`, strings.Repeat("4 ", 101), 1),
		`not json`,
	} {
		if code, _ := h.errorCodeBody("POST", Prefix+"playback", token, bad); code != http.StatusBadRequest {
			t.Errorf("%s: status %d", bad, code)
		}
	}
	if code, e := h.errorCodeBody("POST", Prefix+"playback", token, strings.Replace(report("began", ""),
		fmt.Sprintf(`"id":%d`, id), `"id":99999`, 1)); code != http.StatusNotFound || e != errNotFound {
		t.Errorf("a title that has gone: %d %s", code, e)
	}
	if code, _ := h.errorCodeBody("POST", Prefix+"playback", "", report("began", "")); code != http.StatusUnauthorized {
		t.Errorf("without a session: %d", code)
	}

	// A box sends a few for each film; one that sends many is told to wait.
	busy := 0
	for range reportsPerMinute {
		if code, e := h.errorCodeBody("POST", Prefix+"playback", token, report("waited", "")); code == http.StatusTooManyRequests && e == errBusy {
			busy++
		}
	}
	if busy == 0 {
		t.Errorf("no report was refused")
	}
}
