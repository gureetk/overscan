// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/gpu"
	"overscan.gureet.ca/internal/hub"
)

// syncBuffer is a buffer that the test and a test in the background can
// both write to.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeGraphics stands in for the hub's graphics: it finds what next says
// when tested.
type fakeGraphics struct {
	mu     sync.Mutex
	found  gpu.Found
	tested bool
	next   gpu.Found
	tests  int
	done   chan struct{}
}

func (f *fakeGraphics) GPU() (gpu.Found, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.found, f.tested
}

func (f *fakeGraphics) TestGPU(context.Context) (gpu.Found, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.found, f.tested = f.next, true
	f.tests++
	if f.done != nil {
		close(f.done)
		f.done = nil
	}
	return f.found, true
}

func (f *fakeGraphics) GPUTesting() bool { return false }

// lowPowerIntel is what the test finds of Intel graphics that encode only
// in low-power mode, HEVC without a bit rate, and decode most codecs.
var lowPowerIntel = gpu.Found{Device: "/dev/dri/renderD128", Driver: "Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 ()",
	Encodes: map[string]gpu.Encode{gpu.H264: {OK: true, LowPower: true, ICQ: true, VBR: true},
		gpu.HEVC: {OK: true, LowPower: true, ICQ: true}},
	Decodes:        map[string]bool{gpu.H264: true, gpu.HEVC: true, gpu.HEVC10: true, gpu.MPEG2: true, gpu.AV1: true},
	DecodeUntested: []string{gpu.VP9},
	Deinterlace:    true, Scale: true, Rate: true, ToneMap: true,
	Steps: []gpu.Step{{Name: "Open /dev/dri/renderD128", OK: true},
		{Name: "Encode HEVC", Note: "[hevc_vaapi @ 0x1] No usable encoding entrypoint found for profile VAProfileHEVCMain (17)."},
		{Name: "Encode HEVC, low-power", OK: true}},
	At: time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC), Took: 3200 * time.Millisecond}

func TestGraphicsPage(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	var logs syncBuffer
	ta.log = slog.New(slog.NewTextHandler(&logs, nil))
	csrf := h[1]
	hx := []string{"HX-Request", "true"}
	page := func() string { return ta.do("GET", "/admin/transcoding", nil, c).Body.String() }

	// Without the hub's graphics, none.
	mustContain(t, "without graphics", page(), `<h2 id="graphics-title">Graphics</h2>`,
		"This server has not tested its graphics, so ffmpeg transcodes in software.")

	g := &fakeGraphics{}
	ta.UseGraphics(g)
	g.found, g.tested = gpu.Found{Why: "Overscan sees no graphics: /dev/dri is not there.", At: lowPowerIntel.At}, true
	mustContain(t, "no graphics", page(), "No graphics to transcode on, so ffmpeg transcodes in software.",
		"Overscan sees no graphics: /dev/dri is not there. The documentation on transcoding says how")
	g.found = gpu.Found{Off: true, Why: "The configuration turns the graphics off.", At: lowPowerIntel.At}
	mustContain(t, "off", page(), "The configuration file turns the graphics off, so ffmpeg transcodes in software.")

	// Graphics that transcode: what they do, and the number at a time.
	g.found = lowPowerIntel
	body := page()
	mustContain(t, "graphics", body, "Intel iHD driver for Intel(R) Gen Graphics - 24.1.0 (), at /dev/dri/renderD128",
		"<dt>They make</dt><dd>H.264 (low-power), HEVC (low-power)</dd>",
		"<dt>They decode</dt><dd>H.264, HEVC, HEVC at 10 bits, MPEG-2, AV1; not tested: VP9</dd>",
		"<dt>They also</dt><dd>deinterlace, scale, halve a frame rate and map HDR10 to SDR</dd>",
		"They keep no highest bit rate for HEVC: a profile&#39;s holds only in software.",
		`<option value="2" selected>2 at a time</option>`, "Up to 2 streams are transcoded on the graphics",
		"Fails</span> Encode HEVC<code>[hevc_vaapi @ 0x1] No usable encoding entrypoint found for profile VAProfileHEVCMain (17).</code>",
		"It took 3.2s, and opened no channel.", "Test again")
	// Transcoding in software is off: the graphics alone.
	mustContain(t, "graphics alone", body, "This server transcodes up to 2 streams at a time, on its graphics; transcoding with its processor is off on the Settings page.")
	if err := ta.st.SetTranscodes(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	ta.UseStreams(transcoder{convert: true, encoder: hub.Encoder{H264: true}})
	mustContain(t, "both", page(), "This server transcodes up to 2 streams at a time on its graphics, and 1 more with its processor where its graphics are busy.")

	// One at a time, then off.
	rec := ta.do("POST", "/admin/transcoding/graphics", lineupForm(csrf, "", "at_once", "1"), c, hx...)
	mustContain(t, "one", rec.Body.String(), "The graphics transcode up to 1 stream at a time.", `<option value="1" selected>1 at a time</option>`)
	rec = ta.do("POST", "/admin/transcoding/graphics", lineupForm(csrf, "", "at_once", "0"), c, hx...)
	mustContain(t, "off", rec.Body.String(), "The graphics transcode nothing", "Off: streams are transcoded in software",
		"This server transcodes up to 1 stream at a time, with its processor")
	if rec := ta.do("POST", "/admin/transcoding/graphics", lineupForm(csrf, "", "at_once", "9"), c, hx...); rec.Code != http.StatusBadRequest {
		t.Errorf("9 at a time: %d", rec.Code)
	}
	if n, err := ta.st.GPUTranscodes(context.Background()); err != nil || n != 0 {
		t.Errorf("at once %d, %v", n, err)
	}
	if !strings.Contains(logs.String(), `msg="streams at a time on the graphics set in the admin" at_once=0`) {
		t.Errorf("logs:\n%s", logs.String())
	}

	// Test again.
	g.mu.Lock()
	g.next, g.done = gpu.Found{Why: "gone", At: lowPowerIntel.At}, make(chan struct{})
	done := g.done
	g.mu.Unlock()
	rec = ta.do("POST", "/admin/transcoding/graphics/test", lineupForm(csrf, ""), c, hx...)
	mustContain(t, "test again", rec.Body.String(), "Overscan is testing the graphics now")
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the test did not run")
	}
	mustContain(t, "after the test", page(), "No graphics to transcode on")
}

func TestGraphicsInTheAPI(t *testing.T) {
	ta := newTestAdmin(t)
	c, h := ta.withLineup()
	apiErrorOf(t, ta.apiDo("POST", "graphics/test", "", c, h...), http.StatusConflict, "no_graphics")
	g := &fakeGraphics{found: lowPowerIntel, tested: true, next: lowPowerIntel}
	ta.UseGraphics(g)
	var got apiGraphics
	rec := ta.apiDo("GET", "graphics", "", c)
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || !got.Usable || !got.Tested ||
		got.Device != "/dev/dri/renderD128" || len(got.Encodes) != 2 || got.Encodes[1] != (apiGraphicsCodec{Codec: "hevc", LowPower: true, ICQ: true}) ||
		strings.Join(got.Decodes, ",") != "h264,hevc,hevc10,mpeg2,av1" || strings.Join(got.DecodeUntested, ",") != "vp9" ||
		!got.HDRToSDR || len(got.Steps) != 3 || got.Steps[1].OK || got.TookMS != 3200 || got.AtOnce != 2 || got.TestedAt == nil {
		t.Fatalf("graphics: %d %s", rec.Code, rec.Body)
	}
	g.mu.Lock()
	g.done = make(chan struct{})
	done := g.done
	g.mu.Unlock()
	if rec := ta.apiDo("POST", "graphics/test", "", c, h...); rec.Code != http.StatusAccepted {
		t.Errorf("test: %d %s", rec.Code, rec.Body)
	}
	<-done
	var settings apiSettings
	rec = ta.apiDo("PATCH", "settings", `{"gpu_transcodes": 3}`, c, h...)
	if err := json.Unmarshal(rec.Body.Bytes(), &settings); err != nil || settings.GPUTranscodes != 3 {
		t.Errorf("settings: %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{"gpu_transcodes": 5}`, `{"gpu_transcodes": -1}`, `{"gpu_transcodes": "2"}`} {
		apiErrorOf(t, ta.apiDo("PATCH", "settings", bad, c, h...), http.StatusBadRequest, "bad_json")
	}

	// The support bundle says what the test found.
	rec = ta.do("GET", "/admin/support-bundle", nil, c)
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for _, f := range zr.File {
		if f.Name == "graphics.txt" {
			r, _ := f.Open()
			b, _ := io.ReadAll(r)
			text = string(b)
		}
	}
	mustContain(t, "graphics.txt", text, "Streams at a time on the graphics (D148): 3", "Device: /dev/dri/renderD128",
		"Encodes: H.264 (low-power), HEVC (low-power)", "Keeps to a bit rate (VBR): H.264\n",
		"Decodes: H.264, HEVC, HEVC at 10 bits, MPEG-2, AV1\n", "Not tested, since ffmpeg cannot make a clip of it: VP9",
		"Deinterlaces: yes; scales: yes; halves a frame rate: yes; maps HDR10 to SDR: yes",
		"Tested: 2026-12-04T10:00:00Z, in 3.2s", "  FAIL Encode HEVC: [hevc_vaapi @ 0x1] No usable encoding entrypoint")
}
