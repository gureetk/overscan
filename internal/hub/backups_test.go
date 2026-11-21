// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// backupHarness is a hub with two fictional sources, one and two, each
// with a connection limit of one, whose channels play from the upstream
// fixture: on one, live/1, live/3 and missing, which its source does not
// send; on two, live/2 and missing2. Channels are named "<source>:<path>".
func backupHarness(t *testing.T) *harness {
	t.Helper()
	ctx := context.Background()
	base := upstream(t).URL
	h := &harness{t: t, logs: &syncBuffer{}, entries: map[string]store.PlayableEntry{}}
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	sources := map[int64]Source{}
	for name, paths := range map[string][]string{"one": {"/live/1", "/live/3", "/missing"}, "two": {"/live/2", "/missing2"}} {
		id, err := st.AddSource(ctx, source.Settings{Name: name, URL: "http://" + name + ".example/list.m3u", MaxConnections: 1})
		if err != nil {
			t.Fatal(err)
		}
		sources[id] = Source{Name: name, MaxConnections: 1}
		var channels []store.CatalogChannel
		for _, p := range paths {
			channels = append(channels, store.CatalogChannel{IdentityKey: p, Name: name + ":" + p[1:], StreamURL: base + p})
		}
		if _, err := st.ApplyCatalog(ctx, id, channels, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h.clock.Store(time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC).UnixNano())
	h.hub = New(st, slog.New(slog.NewTextHandler(h.logs, nil)), "test", sources)
	h.hub.now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.front = httptest.NewServer(h.hub)
	t.Cleanup(h.front.Close)
	h.reload()
	return h
}

// reload reads the lineup's entries again, with their backups.
func (h *harness) reload() {
	h.t.Helper()
	ctx := context.Background()
	lineup, err := h.hub.store.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		h.t.Fatal(err)
	}
	for _, c := range lineup {
		e, err := h.hub.store.PlayableEntry(ctx, c.EntryID)
		if err != nil {
			h.t.Fatal(err)
		}
		h.entries[c.Name] = e
	}
}

// backups gives a channel its backups, by name.
func (h *harness) backups(name string, backups ...string) {
	h.t.Helper()
	ids := make([]int64, len(backups))
	for i, b := range backups {
		ids[i] = h.entries[b].EntryID
	}
	if err := h.hub.store.SetEntryBackups(context.Background(), store.DefaultLineupID, h.entries[name].EntryID, ids); err != nil {
		h.t.Fatal(err)
	}
	h.reload()
}

// A channel whose source is at its limit plays from its backup, of another
// source; with both full, it is refused.
func TestBackupWhenTheSourceIsFull(t *testing.T) {
	h := backupHarness(t)
	h.backups("one:live/1", "two:live/2")
	ctx := context.Background()
	if err := h.hub.RoomForChannel(ctx, h.entries["one:live/1"], 2); err != nil {
		t.Errorf("room before: %v", err)
	}
	first := h.play(h.link(1, "one:live/3"))
	defer first.Body.Close()
	if err := h.hub.RoomForChannel(ctx, h.entries["one:live/1"], 2); err != nil {
		t.Errorf("room with one full, two free: %v", err)
	}
	second := h.play(h.link(2, "one:live/1"))
	defer second.Body.Close()
	logs := h.logs.String()
	for _, want := range []string{
		`msg="a channel's stream cannot start; the hub tries its next stream" channel=one:live/1 source=one device=2`,
		`msg="stream started" channel=one:live/1 source=two device=2`,
		`msg="a channel plays from a backup" channel=one:live/1 source=two device=2`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s:\n%s", want, logs)
		}
	}
	if err := h.hub.RoomForChannel(ctx, h.entries["one:live/1"], 3); !errors.Is(err, ErrLimit) {
		t.Errorf("room with both full: %v", err)
	}
	resp := get(t, h.link(3, "one:live/1"))
	body(t, resp)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a third device: %s", resp.Status)
	}
}

// A channel whose source does not send it plays from its backup in the
// same request, and goes after its backup for a while.
func TestBackupWhenTheStreamFails(t *testing.T) {
	h := backupHarness(t)
	h.backups("one:missing", "two:live/2")
	resp := h.play(h.link(1, "one:missing"))
	resp.Body.Close()
	h.waitIdle()
	logs := h.logs.String()
	for _, want := range []string{
		`msg="a stream's source refused it" channel=one:missing source=one status="404 Not Found"`,
		`msg="stream ended" channel=one:missing source=one device=1 reason="its source did not send it, so the channel's next stream plays"`,
		`msg="a channel plays from a backup" channel=one:missing source=two device=1`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs lack %s:\n%s", want, logs)
		}
	}
	if !h.hub.Available(h.entries["one:missing"].SourceID, 9) {
		t.Error("the failed stream holds its source's connection")
	}

	// Asked again, it plays from the backup without trying its own.
	h.advance(time.Minute)
	resp = h.play(h.link(2, "one:missing"))
	resp.Body.Close()
	h.waitIdle()
	if n := strings.Count(h.logs.String(), "a stream's source refused it"); n != 1 {
		t.Errorf("its own stream was tried %d times", n)
	}
	// Once that while is over, its own comes first again.
	h.advance(failingFor)
	resp = h.play(h.link(3, "one:missing"))
	resp.Body.Close()
	h.waitIdle()
	if n := strings.Count(h.logs.String(), "a stream's source refused it"); n != 2 {
		t.Errorf("its own stream was tried %d times", n)
	}
}

// With no stream left to try, the player is told why, as without backups.
func TestNoBackupLeft(t *testing.T) {
	h := backupHarness(t)
	h.backups("one:missing", "two:missing2")
	resp := get(t, h.link(1, "one:missing"))
	body(t, resp)
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status %s", resp.Status)
	}
	h.waitIdle()
	logs := h.logs.String()
	if !strings.Contains(logs, "channel=one:missing source=two status=\"404 Not Found\"") ||
		strings.Contains(logs, "a channel plays from a backup") {
		t.Errorf("logs:\n%s", logs)
	}
	h.hub.mu.Lock()
	n := len(h.hub.sessions)
	h.hub.mu.Unlock()
	if n != 1 {
		// The last stream's session stays, as a session that failed always
		// did, until it is swept away.
		t.Logf("%d sessions", n)
	}
}

// With the shared tuner on, a channel whose source is full plays from its
// backup's tuner.
func TestBackupOfASharedChannel(t *testing.T) {
	h := backupHarness(t)
	if err := h.hub.UseTimeshift(filepath.Join(t.TempDir(), "timeshift")); err != nil {
		t.Fatal(err)
	}
	if err := h.hub.store.SetSharedTuner(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		h.hub.mu.Lock()
		for _, s := range h.hub.sessions {
			h.hub.end(s, "the test ended")
		}
		h.hub.mu.Unlock()
		h.hub.tuners.Wait()
	})
	h.backups("one:live/1", "two:live/2")
	first := h.play(h.link(1, "one:live/3"))
	defer first.Body.Close()
	second := h.play(h.link(2, "one:live/1"))
	defer second.Body.Close()
	h.hub.mu.Lock()
	tuner := h.hub.shared[h.entries["two:live/2"].ChannelID]
	h.hub.mu.Unlock()
	if tuner == nil || tuner.sourceID != h.entries["two:live/2"].SourceID {
		t.Fatalf("no tuner on two's channel:\n%s", h.logs)
	}
	if !strings.Contains(h.logs.String(), `msg="a channel plays from a backup" channel=one:live/1 source=two device=2`) {
		t.Errorf("logs:\n%s", h.logs)
	}
}

// An account that uses only some sources plays a channel from those of its
// streams that come from them, without trying the others, and nothing of a
// channel without one; changing its sources stops what it plays from the
// others (D114).
func TestSourcesOfAnAccount(t *testing.T) {
	h := backupHarness(t)
	h.backups("one:live/1", "two:live/2")
	ctx := context.Background()
	sam, devs := h.accountDevices("Sam", 0, "00:1A:79:00:00:01", "00:1A:79:00:00:02")
	one, two := h.entries["one:live/1"].SourceID, h.entries["two:live/2"].SourceID
	if err := h.hub.store.SetAccountSources(ctx, sam, store.Sources{Only: true, IDs: []int64{two}}); err != nil {
		t.Fatal(err)
	}
	resp := h.play(h.link(devs[0], "one:live/1"))
	logs := h.logs.String()
	if want := `msg="stream started" channel=one:live/1 source=two device=` + strconv.FormatInt(devs[0], 10); !strings.Contains(logs, want) {
		t.Errorf("logs lack %s:\n%s", want, logs)
	}
	if strings.Contains(logs, "source=one") {
		t.Errorf("a stream of one was tried:\n%s", logs)
	}
	resp.Body.Close()
	h.waitIdle()
	refused := get(t, h.link(devs[1], "one:live/3"))
	if text := body(t, refused); refused.StatusCode != http.StatusNotFound || !strings.Contains(text, "not available") {
		t.Errorf("a channel of one alone: %s %q", refused.Status, text)
	}

	// With both sources, Sam plays from both; with one, what played from
	// two stops.
	if err := h.hub.store.SetAccountSources(ctx, sam, store.Sources{Only: true, IDs: []int64{one, two}}); err != nil {
		t.Fatal(err)
	}
	first := h.play(h.link(devs[0], "one:live/3"))
	defer first.Body.Close()
	second := h.play(h.link(devs[1], "two:live/2"))
	defer second.Body.Close()
	// It ends the stream that plays from two, and the session of Sam's first
	// stream, which waits for its player to come back.
	if n := h.hub.StopSources(sam, store.Sources{Only: true, IDs: []int64{one}}, "the account no longer uses the stream's source"); n != 2 {
		t.Errorf("StopSources ended %d streams, want 2", n)
	}
	if n := h.hub.AccountStreams(sam); n != 1 {
		t.Errorf("Sam plays %d streams, want 1", n)
	}
	if !strings.Contains(h.logs.String(), `channel=two:live/2 source=two device=`+strconv.FormatInt(devs[1], 10)+` reason="the account no longer uses the stream's source"`) {
		t.Errorf("the log does not say why:\n%s", h.logs.String())
	}
}
