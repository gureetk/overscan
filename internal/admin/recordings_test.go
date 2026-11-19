// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// fakeRecorder stands in for the recorder.
type fakeRecorder struct {
	dir       string
	cancelled []int64
	changed   int
}

func (f *fakeRecorder) Changed() { f.changed++ }

func (f *fakeRecorder) Dir() string { return f.dir }
func (f *fakeRecorder) Used() int64 { return 3 << 20 }
func (f *fakeRecorder) Cancel(id int64) bool {
	f.cancelled = append(f.cancelled, id)
	return true
}

// TestRecordings turns recording on and off from the Settings page and the
// API, lists the recordings, and calls them off and deletes them (D89).
func TestRecordings(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()

	// A server that cannot record says so, and cannot be turned on.
	page := ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "no recorder", page, "This server cannot record.", `<option value="0" selected>Off</option>`)
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"recordings_cap": 10}`, c, h...), http.StatusServiceUnavailable, "no_recorder")

	rec := &fakeRecorder{dir: filepath.Join(t.TempDir(), "Recordings")}
	ta.UseRecorder(rec)
	mustContain(t, "off", ta.do("GET", "/admin/settings", nil, c).Body.String(),
		"Off: boxes cannot record.", `<option value="25">Up to 25 GB</option>`)

	// A source named recordings already, which reads elsewhere.
	if _, err := ta.st.AddSource(ctx, source.Settings{Name: "recordings", URL: "http://provider.example/rec.m3u"}); err != nil {
		t.Fatal(err)
	}
	answer := ta.do("POST", "/admin/settings/recordings", lineupForm(csrf, "", "cap", "25"), c, hx...)
	mustContain(t, "on", answer.Body.String(), "Boxes can record programmes from the TV Guide, in up to 25 GB.",
		"This server records them, in up to 25 GB, and boxes play them on Movies, from the folder source that reads them.", `<option value="25" selected>Up to 25 GB</option>`,
		"They go in <code>"+rec.dir+"</code>, and take 3 MB there.", "Nothing recorded yet.")
	if n, _ := ta.st.RecordingsCap(ctx); n != 25 {
		t.Errorf("room %d GB", n)
	}
	if info, err := os.Stat(rec.dir); err != nil || !info.IsDir() {
		t.Errorf("the folder: %v", err)
	}
	folders := func() []string {
		var out []string
		sources, err := ta.st.Sources(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range sources {
			if s.Type == source.TypeFolder {
				settings, err := ta.st.SourceSettings(ctx, s.ID)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, s.Name+" "+settings.URL)
			}
		}
		return out
	}
	if got := folders(); len(got) != 1 || got[0] != "recordings-2 "+rec.dir {
		t.Errorf("folder sources %q", got)
	}
	// Once there is a source for the folder, turning recording on again
	// adds none.
	settings := ta.apiDo("PATCH", "settings", `{"recordings_cap": 50}`, c, h...)
	var v apiSettings
	if err := json.Unmarshal(settings.Body.Bytes(), &v); err != nil || v.RecordingsCap != 50 {
		t.Errorf("recordings_cap: status %d, %s", settings.Code, settings.Body)
	}
	if got := folders(); len(got) != 1 {
		t.Errorf("folder sources %q", got)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"recordings_cap": -1}`, c, h...), http.StatusBadRequest, "bad_recordings_cap")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"recordings_cap": "lots"}`, c, h...), http.StatusBadRequest, "bad_json")

	// Recordings to come, under way, made and failed.
	cs, err := ta.st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil || len(cs) == 0 {
		t.Fatalf("channels %+v, %v", cs, err)
	}
	at := time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)
	add := func(title string, start time.Time, state, file string, size int64, why string) store.Recording {
		t.Helper()
		r, err := ta.st.ScheduleRecording(ctx, store.Recording{EntryID: cs[0].EntryID, Channel: "Alpha", Title: title,
			Start: start, Stop: start.Add(time.Hour), CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		if state != store.RecordingScheduled {
			if err := ta.st.SetRecordingState(ctx, r.ID, state, file, size, why); err != nil {
				t.Fatal(err)
			}
		}
		r.State, r.File = state, file
		return r
	}
	soon := add("Evening News", at.Add(24*time.Hour), store.RecordingScheduled, "", 0, "")
	now := add("Quiz Night", at.Add(2*time.Hour), store.RecordingRecording, "Quiz Night.ts", 0, "")
	made := add("Nature Hour", at, store.RecordingDone, "Nature Hour.ts", 2<<30, "")
	failed := add("Late Film", at.Add(-24*time.Hour), store.RecordingFailed, "", 0, "its source would not play the channel (503 Service Unavailable)")
	if err := os.WriteFile(filepath.Join(rec.dir, made.File), []byte("ts"), 0o600); err != nil {
		t.Fatal(err)
	}

	page = ta.do("GET", "/admin/settings", nil, c).Body.String()
	mustContain(t, "the list", page,
		`<span class="strong">Evening News</span><div class="note">Alpha</div>`, "5 Dec 20:00 to 21:00 UTC", "To come",
		`<span class="badge bad">Recording</span>`,
		`<span class="badge ok">Done</span><div class="note">2.0 GB</div>`,
		`<span class="badge warn">Failed</span><div class="note">its source would not play the channel (503 Service Unavailable)</div>`,
		`action="/admin/recordings/1/cancel"`, `action="/admin/recordings/3/delete"`)

	// The API lists them too.
	var list struct {
		CapGB     int            `json:"cap_gb"`
		UsedBytes int64          `json:"used_bytes"`
		Upcoming  []apiRecording `json:"upcoming"`
		Recent    []apiRecording `json:"recent"`
	}
	got := ta.apiDo("GET", "recordings", "", c)
	if err := json.Unmarshal(got.Body.Bytes(), &list); err != nil || list.CapGB != 50 || list.UsedBytes != 3<<20 ||
		len(list.Upcoming) != 2 || list.Upcoming[0].Title != "Quiz Night" || len(list.Recent) != 2 ||
		list.Recent[0].Title != "Nature Hour" || list.Recent[0].Bytes != 2<<30 {
		t.Errorf("recordings: status %d, %s", got.Code, got.Body)
	}

	// Calling off the one to come, and the one under way, which the
	// recorder ends.
	answer = ta.do("POST", "/admin/recordings/1/cancel", lineupForm(csrf, ""), c, hx...)
	mustContain(t, "called off", answer.Body.String(), "Called off Evening News.", "Called off</span>")
	if r, _ := ta.st.RecordingByID(ctx, soon.ID); r.State != store.RecordingCancelled {
		t.Errorf("called off %+v", r)
	}
	got = ta.apiDo("POST", "recordings/2/cancel", "", c, h...)
	if got.Code != http.StatusOK || len(rec.cancelled) != 1 || rec.cancelled[0] != now.ID {
		t.Errorf("cancel under way: status %d, %s, %v", got.Code, got.Body, rec.cancelled)
	}
	apiErrorOf(t, ta.apiDo("POST", "recordings/3/cancel", "", c, h...), http.StatusConflict, "not_to_come")
	apiErrorOf(t, ta.apiDo("DELETE", "recordings/2", "", c, h...), http.StatusConflict, "to_come")
	apiErrorOf(t, ta.apiDo("DELETE", "recordings/99", "", c, h...), http.StatusNotFound, "not_found")
	apiErrorOf(t, ta.apiDo("DELETE", "recordings/x", "", c, h...), http.StatusNotFound, "not_found")

	// Deleting the one made deletes its file.
	answer = ta.do("POST", "/admin/recordings/3/delete", lineupForm(csrf, ""), c, hx...)
	mustContain(t, "deleted", answer.Body.String(), "Deleted Nature Hour.")
	if _, err := os.Stat(filepath.Join(rec.dir, made.File)); !os.IsNotExist(err) || rec.changed != 1 {
		t.Errorf("its file: %v, %d changes", err, rec.changed)
	}
	if _, err := ta.st.RecordingByID(ctx, made.ID); err != store.ErrNotFound {
		t.Errorf("deleted: %v", err)
	}
	if got := ta.apiDo("DELETE", "recordings/4", "", c, h...); got.Code != http.StatusNoContent {
		t.Errorf("delete the failed one: status %d, %s", got.Code, got.Body)
	}
	_ = failed

	// A recording plays as a device of its own, which the overview names.
	ta.UseStreams(fakeStreams{{Kind: hub.PlaysChannel, DeviceID: -now.ID, EntryID: cs[0].EntryID, SourceID: ta.sourceID("main"),
		Source: "main", Channel: "Alpha", Started: ta.clock.Add(-time.Minute), Playing: true}})
	mustContain(t, "the overview", ta.do("GET", "/admin/", nil, c).Body.String(), "a recording on this server", "Quiz Night")

	// The API schedules a programme from the guide, as a box does.
	src := ta.sourceID("main")
	gcs, err := ta.st.GuideChannels(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	w, err := ta.st.BeginGuide(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	for _, gc := range gcs {
		if gc.Name != "Alpha" {
			continue
		}
		for i, title := range []string{"Morning", "Noon", "Evening"} {
			start := ta.clock.Add(time.Duration(2*i-3) * time.Hour)
			if err := w.Add(ctx, store.GuideProgramme{ChannelID: gc.ID, Start: start, Stop: start.Add(2 * time.Hour), Title: title}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Commit(ctx, ta.clock, "hash"); err != nil {
		t.Fatal(err)
	}
	alpha := cs[0].EntryID
	for _, c := range cs {
		if c.Name == "Alpha" {
			alpha = c.EntryID
		}
	}
	body := func(start time.Time) string {
		return fmt.Sprintf(`{"entry_id": %d, "start": %q}`, alpha, start.Format(time.RFC3339))
	}
	var scheduled apiRecording
	got = ta.apiDo("POST", "recordings", body(ta.clock.Add(time.Hour)), c, h...)
	if err := json.Unmarshal(got.Body.Bytes(), &scheduled); err != nil || got.Code != http.StatusOK ||
		scheduled.Title != "Evening" || scheduled.State != store.RecordingScheduled || scheduled.Channel != "Alpha" {
		t.Errorf("scheduled: status %d, %s", got.Code, got.Body)
	}
	apiErrorOf(t, ta.apiDo("POST", "recordings", body(ta.clock.Add(-3*time.Hour)), c, h...), http.StatusConflict, "programme_ended")
	apiErrorOf(t, ta.apiDo("POST", "recordings", body(ta.clock.Add(30*time.Minute)), c, h...), http.StatusNotFound, "not_found")

	// Turning recording off keeps the source and the folder.
	answer = ta.do("POST", "/admin/settings/recordings", lineupForm(csrf, "", "cap", "0"), c, hx...)
	mustContain(t, "off again", answer.Body.String(), "Recording is off. The recordings made stay in their folder.")
	if got := folders(); len(got) != 1 {
		t.Errorf("folder sources %q", got)
	}
	apiErrorOf(t, ta.apiDo("POST", "recordings", body(ta.clock.Add(time.Hour)), c, h...), http.StatusConflict, "recording_off")
}
