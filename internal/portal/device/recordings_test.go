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

type fakeRecorder struct {
	cancelled []int64
	woken     int
}

func (f *fakeRecorder) Cancel(id int64) bool {
	f.cancelled = append(f.cancelled, id)
	return true
}

func (f *fakeRecorder) Wake() { f.woken++ }

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

// A box records a channel now: to the end of the programme that the guide
// shows, or until it is stopped when the household says so or no guide
// shows one; a channel that records already gives that recording, and one
// stopped early can record the rest of its programme (D98).
func TestRecordingNow(t *testing.T) {
	h := newHarness(t)
	ids := h.addGuide()
	h.approve(macA)
	token := h.session(macA).Token
	ctx := context.Background()
	if err := h.store.SetRecordingsCap(ctx, 10); err != nil {
		t.Fatal(err)
	}
	rec := &fakeRecorder{}
	h.api.UseRecorder(rec)
	var bare int64 // a channel that no guide shows
	for name, id := range ids {
		if name != "Channel 0" && name != "Channel 1" {
			bare = id
		}
	}
	if bare == 0 {
		t.Fatalf("no channel without a guide in %v", ids)
	}
	now := h.now.Unix()
	start := func(ch int64) recording {
		t.Helper()
		var got recording
		if code := h.call("POST", Prefix+"recordings", token, fmt.Sprintf(`{"channel":%d,"now":true}`, ch), &got); code != http.StatusOK {
			t.Fatalf("channel %d: %d", ch, code)
		}
		return got
	}

	// The programme on now ends it, as at first.
	match := start(ids["Channel 1"])
	if match.Title != "Match" || match.Start != now || match.Stop != now+60 || match.UntilStopped || match.State != store.RecordingScheduled {
		t.Errorf("to the programme's end: %+v", match)
	}
	if rec.woken != 1 {
		t.Errorf("woken %d times", rec.woken)
	}
	// Asking again, or for the programme from the guide, gives the same one.
	if again := start(ids["Channel 1"]); again.ID != match.ID {
		t.Errorf("again %+v", again)
	}
	var fromGuide recording
	h.call("POST", Prefix+"recordings", token, fmt.Sprintf(`{"channel":%d,"start":%d}`, ids["Channel 1"], now-60), &fromGuide)
	if fromGuide.ID != match.ID {
		t.Errorf("from the guide %+v", fromGuide)
	}

	// Without a guide, until it is stopped, for the household's most hours.
	bareRec := start(bare)
	if !bareRec.UntilStopped || bareRec.Stop != now+4*3600 || bareRec.Title == "" {
		t.Errorf("without a guide: %+v", bareRec)
	}
	// The household can record until stopped while the guide shows a
	// programme too: it keeps the programme's title.
	if err := h.store.SetRecordUntil(ctx, store.RecordUntilStopped); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRecordingHours(ctx, 2); err != nil {
		t.Fatal(err)
	}
	p2 := start(ids["Channel 0"])
	if !p2.UntilStopped || p2.Stop != now+2*3600 || p2.Title != "P2" {
		t.Errorf("until stopped: %+v", p2)
	}
	var list struct{ Recordings []recording }
	h.call("GET", Prefix+"recordings", token, "", &list)
	if len(list.Recordings) != 3 {
		t.Errorf("listed %+v", list.Recordings)
	}

	// Stopping one under way keeps what it recorded; the rest of its
	// programme is a recording of its own.
	if err := h.store.SetRecordingState(ctx, match.ID, store.RecordingRecording, "", "m.ts", 0, ""); err != nil {
		t.Fatal(err)
	}
	var stopped recording
	h.call("DELETE", fmt.Sprintf("%srecordings/%d", Prefix, match.ID), token, "", &stopped)
	if len(rec.cancelled) != 1 || rec.cancelled[0] != match.ID {
		t.Errorf("stopped %v", rec.cancelled)
	}
	if err := h.store.SetRecordingState(ctx, match.ID, store.RecordingDone, "", "m.ts", 188, ""); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(30 * time.Second)
	var rest recording
	h.call("POST", Prefix+"recordings", token, fmt.Sprintf(`{"channel":%d,"start":%d}`, ids["Channel 1"], now-60), &rest)
	if rest.ID == match.ID || rest.Stop != now+60 || rest.Title != "Match" || rest.State != store.RecordingScheduled {
		t.Errorf("the rest %+v", rest)
	}
	// The programme from the guide, recorded and stopped early in turn:
	// what is left of it starts now.
	if err := h.store.SetRecordingState(ctx, rest.ID, store.RecordingDone, "", "r.ts", 188, ""); err != nil {
		t.Fatal(err)
	}
	h.now = h.now.Add(10 * time.Second)
	var last recording
	h.call("POST", Prefix+"recordings", token, fmt.Sprintf(`{"channel":%d,"start":%d}`, ids["Channel 1"], now-60), &last)
	if last.ID == rest.ID || last.Start != now+40 || last.Stop != now+60 || last.State != store.RecordingScheduled {
		t.Errorf("what is left %+v", last)
	}

	// A bad request.
	if code, e := h.errorCodeBody("POST", Prefix+"recordings", token, `{"channel":0,"now":true}`); code != http.StatusBadRequest || e != errBadRequest {
		t.Errorf("no channel: %d %s", code, e)
	}
}
