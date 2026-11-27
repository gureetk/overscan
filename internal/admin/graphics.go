// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/gpu"
	"overscan.gureet.ca/internal/store"
)

// The graphics (D148): what the test at start found that the server's
// graphics do for ffmpeg, the number of streams at a time that they may
// transcode, and a test again, on the Transcoding page and in the API.

// Graphics is what the admin needs of the hub for the graphics.
type Graphics interface {
	// GPU returns what the last test found, and whether one has run.
	GPU() (gpu.Found, bool)
	// TestGPU tests the graphics again, and uses what it finds; it
	// reports false if a test runs already.
	TestGPU(ctx context.Context) (gpu.Found, bool)
	// GPUTesting reports whether a test runs now.
	GPUTesting() bool
}

// UseGraphics gives the admin what tests the graphics. Without it, the
// admin says that this server has no graphics to transcode on.
func (a *Admin) UseGraphics(g Graphics) { a.graphics = g }

// gpuTestWait caps a test that the administrator asks for: it goes on in
// the background.
const gpuTestWait = 15 * time.Minute

// graphicsData is what the Transcoding page shows of the graphics.
type graphicsData struct {
	Found   gpu.Found
	Tested  bool   // a test has run
	Testing bool   // one runs now
	AtOnce  int    // how many streams at once they may transcode
	When    string // when the last test ran, such as "5 minutes ago"
}

// Took says how long the last test took, for people.
func (g graphicsData) Took() string { return g.Found.Took.Round(100 * time.Millisecond).String() }

// Usable reports whether the graphics transcode.
func (g graphicsData) Usable() bool { return g.Tested && g.Found.Usable() }

// On reports whether the graphics transcode, and may.
func (g graphicsData) On() bool { return g.Usable() && g.AtOnce > 0 }

// Filters names what the graphics do to pictures, for people, such as
// "deinterlace, scale and map HDR10 to SDR".
func (g graphicsData) Filters() string {
	var do []string
	if g.Found.Deinterlace {
		do = append(do, "deinterlace")
	}
	if g.Found.Scale {
		do = append(do, "scale")
	}
	if g.Found.Rate {
		do = append(do, "halve a frame rate")
	}
	if g.Found.ToneMap {
		do = append(do, "map HDR10 to SDR")
	}
	switch len(do) {
	case 0:
		return ""
	case 1:
		return do[0]
	}
	return strings.Join(do[:len(do)-1], ", ") + " and " + do[len(do)-1]
}

// Rates says which codecs the graphics keep to a bit rate, for people.
func (g graphicsData) Rates() string {
	var keep, not []string
	for _, c := range gpu.Encoded {
		e := g.Found.Encodes[c]
		switch {
		case !e.OK:
		case e.VBR:
			keep = append(keep, gpu.Names[c])
		default:
			not = append(not, gpu.Names[c])
		}
	}
	if len(not) == 0 {
		return ""
	}
	return "They keep no highest bit rate for " + strings.Join(not, " or ") + ": a profile's holds only in software."
}

// Untested names the codecs that the test could not try, for people.
func (g graphicsData) Untested() string {
	var names []string
	for _, c := range g.Found.DecodeUntested {
		names = append(names, gpu.Names[c])
	}
	return strings.Join(names, ", ")
}

// AtOnceOptions are the numbers at a time that the page offers.
func (graphicsData) AtOnceOptions() []int {
	out := make([]int, store.MaxGPUTranscodes+1)
	for i := range out {
		out[i] = i
	}
	return out
}

func (a *Admin) graphicsData(ctx context.Context) (graphicsData, error) {
	var g graphicsData
	if a.graphics != nil {
		g.Found, g.Tested = a.graphics.GPU()
		g.Testing = a.graphics.GPUTesting()
		if g.Tested {
			g.When = ago(g.Found.At, a.now())
		}
	}
	var err error
	g.AtOnce, err = a.store.GPUTranscodes(ctx)
	return g, err
}

// setGPUTranscodes sets how many streams at once the graphics may
// transcode.
func (a *Admin) setGPUTranscodes(ctx context.Context, n int) error {
	if n < 0 || n > store.MaxGPUTranscodes {
		return badEdit(http.StatusBadRequest, "bad_form", "The graphics transcode from 0 to %d streams at a time.", store.MaxGPUTranscodes)
	}
	if err := a.store.SetGPUTranscodes(ctx, n); err != nil {
		return err
	}
	a.log.Info("streams at a time on the graphics set in the admin", "at_once", n)
	return nil
}

// testGraphics starts a test of the graphics in the background, and
// reports false if one runs already, or none can.
func (a *Admin) testGraphics() bool {
	if a.graphics == nil || a.graphics.GPUTesting() {
		return false
	}
	a.log.Info("the graphics tested again in the admin")
	started := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), gpuTestWait)
		defer cancel()
		close(started)
		a.graphics.TestGPU(ctx)
	}()
	<-started
	return true
}

// gpuTranscodesForm sets from the Transcoding page how many streams at
// once the graphics may transcode.
func (a *Admin) gpuTranscodesForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	n, err := strconv.Atoi(r.PostFormValue("at_once"))
	if err != nil {
		n = -1
	}
	err = a.setGPUTranscodes(r.Context(), n)
	notice := fmt.Sprintf("The graphics transcode up to %d %s at a time.", n, map[bool]string{true: "stream", false: "streams"}[n == 1])
	if n == 0 {
		notice = "The graphics transcode nothing: streams are transcoded in software, where the Settings page lets them."
	}
	a.transcodingDone(w, r, s, err, notice, transcodeProfileForm{P: newTranscodeProfile()}, false)
}

// testGraphicsForm tests the graphics again from the Transcoding page.
func (a *Admin) testGraphicsForm(w http.ResponseWriter, r *http.Request, s store.AdminSession) {
	notice := "Overscan is testing the graphics now; this page shows what it finds in a few seconds."
	if !a.testGraphics() {
		notice = "Overscan is testing the graphics already."
	}
	a.transcodingDone(w, r, s, nil, notice, transcodeProfileForm{P: newTranscodeProfile()}, false)
}

// apiGraphics is the graphics in the API.
type apiGraphics struct {
	Tested         bool               `json:"tested"`
	Testing        bool               `json:"testing"`
	Usable         bool               `json:"usable"`
	Off            bool               `json:"off"`
	Device         string             `json:"device,omitempty"`
	Driver         string             `json:"driver,omitempty"`
	Why            string             `json:"why,omitempty"`
	Encodes        []apiGraphicsCodec `json:"encodes"`
	Decodes        []string           `json:"decodes"`
	DecodeUntested []string           `json:"decode_untested"`
	Deinterlace    bool               `json:"deinterlace"`
	Scale          bool               `json:"scale"`
	FrameRate      bool               `json:"frame_rate"`
	HDRToSDR       bool               `json:"hdr_to_sdr"`
	HDRUntested    bool               `json:"hdr_to_sdr_untested"`
	Steps          []apiGraphicsStep  `json:"steps"`
	TestedAt       *time.Time         `json:"tested_at,omitempty"`
	TookMS         int64              `json:"took_ms"`
	AtOnce         int                `json:"at_once"`
}

type apiGraphicsCodec struct {
	Codec    string `json:"codec"`
	LowPower bool   `json:"low_power"`
	ICQ      bool   `json:"icq"`
	VBR      bool   `json:"vbr"`
}

type apiGraphicsStep struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Note string `json:"note,omitempty"`
}

func graphicsBody(g graphicsData) apiGraphics {
	f := g.Found
	out := apiGraphics{Tested: g.Tested, Testing: g.Testing, Usable: g.Usable(), Off: f.Off, Device: f.Device, Driver: f.Driver,
		Why: f.Why, Encodes: []apiGraphicsCodec{}, Decodes: []string{}, DecodeUntested: []string{}, Deinterlace: f.Deinterlace,
		Scale: f.Scale, FrameRate: f.Rate, HDRToSDR: f.ToneMap, HDRUntested: f.ToneMapUntested, Steps: []apiGraphicsStep{},
		TookMS: f.Took.Milliseconds(), AtOnce: g.AtOnce}
	for _, c := range gpu.Encoded {
		if e := f.Encodes[c]; e.OK {
			out.Encodes = append(out.Encodes, apiGraphicsCodec{Codec: c, LowPower: e.LowPower, ICQ: e.ICQ, VBR: e.VBR})
		}
	}
	for _, c := range gpu.Decoded {
		if f.Decodes[c] {
			out.Decodes = append(out.Decodes, c)
		}
	}
	out.DecodeUntested = append(out.DecodeUntested, f.DecodeUntested...)
	for _, s := range f.Steps {
		out.Steps = append(out.Steps, apiGraphicsStep{Name: s.Name, OK: s.OK, Note: s.Note})
	}
	if g.Tested {
		at := f.At.UTC()
		out.TestedAt = &at
	}
	return out
}

func (a *Admin) apiGraphics(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	g, err := a.graphicsData(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, graphicsBody(g))
}

// apiTestGraphics starts a test of the graphics; GET /graphics says what
// it found once testing is false.
func (a *Admin) apiTestGraphics(w http.ResponseWriter, r *http.Request, _ store.AdminSession) {
	if a.graphics == nil {
		apiError(w, http.StatusConflict, "no_graphics", "This server cannot test graphics.")
		return
	}
	a.testGraphics()
	g, err := a.graphicsData(r.Context())
	if a.editAnswer(w, err) {
		return
	}
	writeJSON(w, http.StatusAccepted, graphicsBody(g))
}

// bundleGraphics is the support bundle's graphics.txt: what the last test
// found, step by step.
func (a *Admin) bundleGraphics(ctx context.Context, _ time.Time) (string, error) {
	var b strings.Builder
	g, err := a.graphicsData(ctx)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "Streams at a time on the graphics (D148): %d\n", g.AtOnce)
	if g.Testing {
		fmt.Fprintln(&b, "A test runs now.")
	}
	g.Found.Report(&b)
	return b.String(), nil
}
