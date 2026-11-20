// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"errors"
	"strings"
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

// TestEndedRecordings checks the list of an account's recordings of the
// last day that are done or failed, the latest first (D100).
func TestEndedRecordings(t *testing.T) {
	st := openTest(t, newPath(t))
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	add := func(account int64, title string, ago time.Duration, state string) {
		t.Helper()
		r, err := st.ScheduleRecording(ctx, Recording{AccountID: account, EntryID: 7, Channel: "one", Title: title,
			Start: now.Add(-ago), Stop: now.Add(-ago + time.Hour), CreatedAt: now.Add(-ago)})
		if err != nil {
			t.Fatal(err)
		}
		if state != RecordingScheduled {
			if err := st.SetRecordingState(ctx, r.ID, state, "", title+".ts", 188, ""); err != nil {
				t.Fatal(err)
			}
		}
	}
	add(1, "Done", 3*time.Hour, RecordingDone)
	add(1, "Failed", 2*time.Hour, RecordingFailed)
	add(1, "Called off", time.Hour, RecordingCancelled)
	add(1, "Old", 30*time.Hour, RecordingDone)
	add(1, "Under way", 10*time.Minute, RecordingRecording)
	add(2, "Another account's", 20*time.Minute, RecordingDone)
	got, err := st.EndedRecordings(ctx, 1, now.Add(-24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, r := range got {
		titles = append(titles, r.Title)
	}
	if strings.Join(titles, ",") != "Failed,Done" {
		t.Errorf("ended %v", titles)
	}
	if got, _ := st.EndedRecordings(ctx, 1, now.Add(-24*time.Hour), 1); len(got) != 1 || got[0].Title != "Failed" {
		t.Errorf("at most one: %+v", got)
	}
}

// TestRecordingsByTime checks that a recording set by time keeps that it
// was, runs exactly as set, and that the recordings of a lineup entry that
// overlap a time are found (D101).
func TestRecordingsByTime(t *testing.T) {
	st := openTest(t, newPath(t))
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0)
	timed, err := st.ScheduleRecording(ctx, Recording{EntryID: 7, Channel: "one", Title: "one", Start: now.Add(time.Hour),
		Stop: now.Add(2 * time.Hour), Timed: true, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if !timed.Timed || !timed.Exact() || timed.UntilStopped {
		t.Errorf("timed %+v", timed)
	}
	programme, err := st.ScheduleRecording(ctx, Recording{EntryID: 7, Channel: "one", Title: "Later", Start: now.Add(3 * time.Hour),
		Stop: now.Add(4 * time.Hour), CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if programme.Timed || programme.Exact() {
		t.Errorf("a programme's %+v", programme)
	}
	for _, c := range []struct {
		from, to time.Duration
		want     int64
	}{
		{0, time.Hour, 0},
		{30 * time.Minute, 90 * time.Minute, timed.ID},
		{90 * time.Minute, 200 * time.Minute, timed.ID},
		{2 * time.Hour, 3 * time.Hour, 0},
		{150 * time.Minute, 5 * time.Hour, programme.ID},
	} {
		got, err := st.OverlappingRecording(ctx, 7, now.Add(c.from), now.Add(c.to))
		if c.want == 0 && !errors.Is(err, ErrNotFound) || c.want != 0 && (err != nil || got.ID != c.want) {
			t.Errorf("%v to %v: %+v, %v", c.from, c.to, got, err)
		}
	}
	// Another entry's, and one called off, do not count.
	if _, err := st.OverlappingRecording(ctx, 8, now, now.Add(5*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("another entry: %v", err)
	}
	if err := st.SetRecordingState(ctx, timed.ID, RecordingCancelled, "", "", 0, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OverlappingRecording(ctx, 7, now.Add(time.Hour), now.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("called off: %v", err)
	}
}
