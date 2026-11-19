// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

type fakeRecorder struct{ cancelled []int64 }

func (f *fakeRecorder) Cancel(id int64) bool {
	f.cancelled = append(f.cancelled, id)
	return true
}

// A box schedules a programme to come, sees it, and calls it off; one that
// has ended, or a server that does not record, is refused (D89).
func TestRecordings(t *testing.T) {
	h := newHarness(t)
	ids := h.addGuide()
	h.approve(macA)
	token := h.session(macA).Token
	ctx := context.Background()
	ch := ids["Channel 0"]
	next := h.now.Add(1).Truncate(3600e9).Add(3600e9).Unix() // P3 begins at the next hour

	// A server without a recorder does not record, whatever the setting.
	if err := h.store.SetRecordingsCap(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, fmt.Sprintf(`{"channel":%d,"start":%d}`, ch, next)); code != http.StatusConflict || e != errRecordingOff {
		t.Errorf("no recorder: %d %s", code, e)
	}
	if err := h.store.SetRecordingsCap(ctx, 0); err != nil {
		t.Fatal(err)
	}
	rec := &fakeRecorder{}
	h.api.UseRecorder(rec)

	var list struct {
		On         bool
		Recordings []recording
	}
	h.call("GET", Prefix+"recordings", token, "", &list)
	if list.On || len(list.Recordings) != 0 {
		t.Errorf("at first %+v", list)
	}
	body := fmt.Sprintf(`{"channel":%d,"start":%d}`, ch, next)
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, body); code != http.StatusConflict || e != errRecordingOff {
		t.Errorf("off: %d %s", code, e)
	}
	if err := h.store.SetRecordingsCap(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var got recording
	if code := h.call("POST", Prefix+"recordings", token, body, &got); code != http.StatusOK || got.Title != "P3" ||
		got.State != store.RecordingScheduled || got.Channel != ch || got.Stop-got.Start != 3600 {
		t.Fatalf("scheduled: %d %+v", code, got)
	}
	// Asking again gives the same one.
	var again recording
	if h.call("POST", Prefix+"recordings", token, body, &again); again.ID != got.ID {
		t.Errorf("again %+v", again)
	}
	h.call("GET", Prefix+"recordings", token, "", &list)
	if !list.On || len(list.Recordings) != 1 || list.Recordings[0].ID != got.ID {
		t.Errorf("listed %+v", list)
	}
	// A programme that has ended, and one that is not in the guide.
	past := fmt.Sprintf(`{"channel":%d,"start":%d}`, ch, h.now.Add(-2*3600e9).Unix())
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, past); code != http.StatusConflict || e != errEnded {
		t.Errorf("ended: %d %s", code, e)
	}
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, fmt.Sprintf(`{"channel":%d,"start":%d}`, ch, next+60)); code != http.StatusNotFound || e != errNotFound {
		t.Errorf("no such programme: %d %s", code, e)
	}

	// Calling it off.
	var off recording
	if code := h.call("DELETE", fmt.Sprintf("%srecordings/%d", Prefix, got.ID), token, "", &off); code != http.StatusOK || off.State != store.RecordingCancelled {
		t.Errorf("called off: %d %+v", code, off)
	}
	h.call("GET", Prefix+"recordings", token, "", &list)
	if len(list.Recordings) != 0 {
		t.Errorf("after calling off %+v", list)
	}
	// One under way is ended by the recorder.
	if h.call("POST", Prefix+"recordings", token, body, &got); got.State != store.RecordingScheduled {
		t.Fatalf("scheduled again %+v", got)
	}
	if err := h.store.SetRecordingState(ctx, got.ID, store.RecordingRecording, "", "f.ts", 0, ""); err != nil {
		t.Fatal(err)
	}
	h.call("DELETE", fmt.Sprintf("%srecordings/%d", Prefix, got.ID), token, "", &off)
	if len(rec.cancelled) != 1 || rec.cancelled[0] != got.ID {
		t.Errorf("cancelled %v", rec.cancelled)
	}
	// What it wrote is kept, so the programme has been recorded.
	if err := h.store.SetRecordingState(ctx, got.ID, store.RecordingDone, "", "f.ts", 188, ""); err != nil {
		t.Fatal(err)
	}
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, body); code != http.StatusConflict || e != errRecorded {
		t.Errorf("recorded: %d %s", code, e)
	}
	// A household may have a hundred recordings to come.
	for i := range maxUpcoming {
		if _, err := h.store.ScheduleRecording(ctx, store.Recording{AccountID: 1, EntryID: ch, Channel: "Channel 0",
			Title: "Many", Start: h.now.Add(time.Duration(48+i) * time.Hour), Stop: h.now.Add(time.Duration(49+i) * time.Hour),
			CreatedAt: h.now}); err != nil {
			t.Fatal(err)
		}
	}
	later := fmt.Sprintf(`{"channel":%d,"start":%d}`, ch, next+3600)
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, later); code != http.StatusConflict || e != errTooMany {
		t.Errorf("too many: %d %s", code, e)
	}

	// An unknown recording.
	if code, e := h.errorCode("DELETE", fmt.Sprintf("%srecordings/%d", Prefix, 999), token); code != http.StatusNotFound || e != errNotFound {
		t.Errorf("unknown: %d %s", code, e)
	}
}
