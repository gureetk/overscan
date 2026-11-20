// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	mustContain(t, "on", answer.Body.String(), "Boxes can record, in up to 25 GB.",
		"This server records, in up to 25 GB, and boxes play the recordings on Movies, from the folder source that reads them.", `<option value="25" selected>Up to 25 GB</option>`,
		`<option value="programme" selected>Stops when its programme ends</option>`, `<option value="4" selected>4 hours</option>`,
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
			if err := ta.st.SetRecordingState(ctx, r.ID, state, "", file, size, why); err != nil {
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
		`<span class="strong">Evening News</span><div class="note">Alpha · <span class="nowrap">5 Dec 20:00 to 21:00 UTC</span></div>`, "To come",
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

// TestRecordingsFolderMoved changes the recordings folder, as a new
// recordings_dir and a restart do: the recordings source reads the new
// folder from the start, and recordings made before are deleted from the
// folder they were made in, or from the new one if they were moved there.
func TestRecordingsFolderMoved(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	ctx := context.Background()
	before := filepath.Join(t.TempDir(), "Recordings")
	rec := &fakeRecorder{dir: before}
	ta.UseRecorder(rec)
	if err := ta.setRecordingsCap(ctx, 10); err != nil {
		t.Fatal(err)
	}
	cs, err := ta.st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil || len(cs) == 0 {
		t.Fatalf("channels %+v, %v", cs, err)
	}
	at := time.Date(2026, 12, 4, 20, 0, 0, 0, time.UTC)
	made := func(title string, start time.Time, dir string) store.Recording {
		t.Helper()
		r, err := ta.st.ScheduleRecording(ctx, store.Recording{EntryID: cs[0].EntryID, Channel: "Alpha", Title: title,
			Start: start, Stop: start.Add(time.Hour), CreatedAt: at})
		if err != nil {
			t.Fatal(err)
		}
		if err := ta.st.SetRecordingState(ctx, r.ID, store.RecordingDone, dir, title+".ts", 2, ""); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(before, title+".ts"), []byte("ts"), 0o600); err != nil {
			t.Fatal(err)
		}
		r.File, r.Dir = title+".ts", dir
		return r
	}
	stays := made("Nature Hour", at, before)
	moves := made("Quiz Night", at.Add(time.Hour), "")

	// The configuration names another folder, and the server starts again.
	after := filepath.Join(t.TempDir(), "TV")
	rec.dir = after
	ta.PrepareRecordings(ctx)
	sources, err := ta.st.Sources(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var folders []string
	for _, s := range sources {
		if s.Type == source.TypeFolder {
			settings, _ := ta.st.SourceSettings(ctx, s.ID)
			folders = append(folders, s.Name+" "+settings.URL)
		}
	}
	if len(folders) != 1 || folders[0] != "recordings "+after {
		t.Errorf("folder sources %q", folders)
	}
	if info, err := os.Stat(after); err != nil || !info.IsDir() {
		t.Errorf("the new folder: %v", err)
	}
	// Starting again changes nothing more.
	ta.PrepareRecordings(ctx)
	if again, _ := ta.st.Sources(ctx); len(again) != len(sources) {
		t.Errorf("%d sources, then %d", len(sources), len(again))
	}

	// One stays where it was made; the other was moved by hand.
	if err := os.Rename(filepath.Join(before, moves.File), filepath.Join(after, moves.File)); err != nil {
		t.Fatal(err)
	}
	for _, r := range []store.Recording{stays, moves} {
		got := ta.apiDo("DELETE", fmt.Sprintf("recordings/%d", r.ID), "", c, h...)
		if got.Code != http.StatusNoContent {
			t.Errorf("delete %s: status %d, %s", r.Title, got.Code, got.Body)
		}
	}
	for _, p := range []string{filepath.Join(before, stays.File), filepath.Join(after, moves.File)} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s: %v", p, err)
		}
	}
}

// What a recording that a box starts on a channel runs until, and the
// longest one that runs until it is stopped may run, from the Settings page
// and the API (D98).
func TestRecordingEnd(t *testing.T) {
	ta := newTestAdmin(t)
	ta.setPassword("correct horse")
	c, csrf := ta.signIn("correct horse")
	hx := []string{"HX-Request", "true"}
	ctx := context.Background()
	rec := &fakeRecorder{dir: filepath.Join(t.TempDir(), "Recordings")}
	ta.UseRecorder(rec)
	if strings.Contains(ta.do("GET", "/admin/settings", nil, c).Body.String(), `id="recordings-until"`) {
		t.Error("the choices show while recording is off")
	}
	if err := ta.st.SetRecordingsCap(ctx, 10); err != nil {
		t.Fatal(err)
	}
	answer := ta.do("POST", "/admin/settings/recordings/end", lineupForm(csrf, "", "until", "stopped", "hours", "8"), c, hx...)
	mustContain(t, "until stopped", answer.Body.String(), "A recording that a box starts on a channel runs until it is stopped, or for at most 8 hours.",
		`<option value="stopped" selected>Runs until it is stopped</option>`, `<option value="8" selected>8 hours</option>`)
	if v, _ := ta.st.RecordUntil(ctx); v != store.RecordUntilStopped {
		t.Errorf("until %q", v)
	}
	if h, _ := ta.st.RecordingHours(ctx); h != 8 {
		t.Errorf("hours %d", h)
	}
	for _, bad := range [][]string{{"until", "never", "hours", "4"}, {"until", "programme", "hours", "0"}, {"until", "programme", "hours", "25"}} {
		if answer := ta.do("POST", "/admin/settings/recordings/end", lineupForm(csrf, "", bad...), c, hx...); answer.Code != http.StatusBadRequest {
			t.Errorf("%v: status %d", bad, answer.Code)
		}
	}
	h := []string{csrfHeader, csrf}
	var got apiSettings
	if rec := ta.apiDo("PATCH", "settings", `{"recordings_until": "programme", "recordings_max_hours": 2}`, c, h...); rec.Code != http.StatusOK ||
		json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.RecordUntil != "programme" || got.RecordingHours != 2 {
		t.Errorf("API: %d %s", rec.Code, rec.Body)
	}
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"recordings_until": "soon"}`, c, h...), http.StatusBadRequest, "bad_recordings_until")
	apiErrorOf(t, ta.apiDo("PATCH", "settings", `{"recordings_max_hours": "4"}`, c, h...), http.StatusBadRequest, "bad_json")
}
