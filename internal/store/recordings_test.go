// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

func TestRecordings(t *testing.T) {
	st := openTest(t, newPath(t))
	ctx := context.Background()
	if n, err := st.RecordingsCap(ctx); err != nil || n != 0 {
		t.Fatalf("room at first %d, %v", n, err)
	}
	if err := st.SetRecordingsCap(ctx, MaxRecordingsCap+1); err == nil {
		t.Error("a room past the most")
	}
	if err := st.SetRecordingsCap(ctx, 25); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.RecordingsCap(ctx); n != 25 {
		t.Errorf("room %d", n)
	}

	src, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, src, []CatalogChannel{{IdentityKey: "1", Name: "Example News",
		StreamURL: "http://streams.example/1.ts"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	cs, err := st.LineupChannels(ctx, DefaultLineupID)
	if err != nil || len(cs) != 1 {
		t.Fatalf("channels %+v, %v", cs, err)
	}
	at := time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)
	schedule := func(title string, start time.Time) Recording {
		t.Helper()
		r, err := st.ScheduleRecording(ctx, Recording{AccountID: 1, DeviceID: 2, EntryID: cs[0].EntryID, Channel: "Example News",
			Title: title, Start: start, Stop: start.Add(time.Hour), CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := schedule("News", at)
	if first.State != RecordingScheduled || first.Title != "News" || !first.Start.Equal(at) {
		t.Fatalf("scheduled %+v", first)
	}
	// Asking again keeps the one scheduled, as it is.
	if again := schedule("News again", at); again.ID != first.ID || again.Title != "News" {
		t.Errorf("again %+v", again)
	}
	// One called off is scheduled again; one made stays made.
	if err := st.SetRecordingState(ctx, first.ID, RecordingCancelled, "", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	if again := schedule("News", at); again.ID != first.ID || again.State != RecordingScheduled {
		t.Errorf("called off, then again %+v", again)
	}
	if err := st.SetRecordingState(ctx, first.ID, RecordingDone, "/srv/recordings", "News.ts", 188, ""); err != nil {
		t.Fatal(err)
	}
	if again := schedule("News", at); again.State != RecordingDone || again.File != "News.ts" || again.Size != 188 ||
		again.Dir != "/srv/recordings" {
		t.Errorf("made, then again %+v", again)
	}

	later := schedule("Later", at.Add(2*time.Hour))
	failed := schedule("Failed", at.Add(-2*time.Hour))
	if err := st.SetRecordingState(ctx, failed.ID, RecordingFailed, "", "", 0, "its source would not play the channel"); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Recordings(ctx, 0, RecordingScheduled, RecordingRecording); len(list) != 1 || list[0].ID != later.ID {
		t.Errorf("to come %+v", list)
	}
	if list, _ := st.RecentRecordings(ctx, 10); len(list) != 2 || list[0].ID != first.ID || list[1].ID != failed.ID {
		t.Errorf("recent %+v", list)
	}
	counts, err := st.RecordingCounts(ctx)
	if err != nil || counts[RecordingScheduled] != 1 || counts[RecordingDone] != 1 || counts[RecordingFailed] != 1 {
		t.Errorf("counts %v, %v", counts, err)
	}

	// Pruning forgets the failed one with no file, once it is old enough.
	if n, err := st.PruneRecordings(ctx, at.Add(-2*time.Hour)); err != nil || n != 0 {
		t.Errorf("pruned %d, %v", n, err)
	}
	if n, err := st.PruneRecordings(ctx, at); err != nil || n != 1 {
		t.Errorf("pruned %d, %v", n, err)
	}
	if _, err := st.RecordingByID(ctx, failed.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("pruned: %v", err)
	}
	if err := st.DeleteRecording(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRecording(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted twice: %v", err)
	}
}

// How long a recording that a box starts on a channel runs, and the one a
// channel has under way or due now (D98).
func TestRecordNow(t *testing.T) {
	st := openTest(t, newPath(t))
	ctx := context.Background()
	if v, err := st.RecordUntil(ctx); err != nil || v != RecordUntilProgramme {
		t.Fatalf("until at first %q, %v", v, err)
	}
	if h, err := st.RecordingHours(ctx); err != nil || h != DefaultRecordingHours {
		t.Fatalf("hours at first %d, %v", h, err)
	}
	if err := st.SetRecordUntil(ctx, "forever"); err == nil {
		t.Error("an unknown end")
	}
	for _, bad := range []int{0, MaxRecordingHours + 1} {
		if err := st.SetRecordingHours(ctx, bad); err == nil {
			t.Errorf("%d hours", bad)
		}
	}
	if err := st.SetRecordUntil(ctx, RecordUntilStopped); err != nil {
		t.Fatal(err)
	}
	if err := st.SetRecordingHours(ctx, 6); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.RecordUntil(ctx); v != RecordUntilStopped {
		t.Errorf("until %q", v)
	}
	if h, _ := st.RecordingHours(ctx); h != 6 {
		t.Errorf("hours %d", h)
	}

	now := time.Unix(1_800_000_000, 0)
	if _, err := st.ActiveRecording(ctx, 7, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("none: %v", err)
	}
	later, err := st.ScheduleRecording(ctx, Recording{EntryID: 7, Channel: "one", Title: "Later", Start: now.Add(time.Hour),
		Stop: now.Add(2 * time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	open, err := st.ScheduleRecording(ctx, Recording{EntryID: 7, Channel: "one", Title: "one", Start: now,
		Stop: now.Add(4 * time.Hour), UntilStopped: true, CreatedAt: now})
	if err != nil || !open.UntilStopped {
		t.Fatalf("until stopped %+v, %v", open, err)
	}
	if got, err := st.ActiveRecording(ctx, 7, now); err != nil || got.ID != open.ID {
		t.Errorf("due now %+v, %v", got, err)
	}
	if got, err := st.ActiveRecording(ctx, 7, now.Add(90*time.Minute)); err != nil || got.ID != later.ID {
		t.Errorf("an hour and a half later %+v, %v", got, err)
	}
	if err := st.SetRecordingState(ctx, open.ID, RecordingDone, "", "one.ts", 188, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ActiveRecording(ctx, 7, now); !errors.Is(err, ErrNotFound) {
		t.Errorf("after it was made: %v", err)
	}
}
