// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"strings"

	"overscan.gureet.ca/internal/gpu"
	"overscan.gureet.ca/internal/streaminfo"
	"overscan.gureet.ca/internal/streamprofile"
)

// Graphics (D148). Where the test at start found that the server's
// graphics make a profile's codec, the hub transcodes a live stream on
// them, up to their own number at a time, which the administrator sets.
// Where the graphics also deinterlace and scale, the pictures go through
// those filters on them; where they also decode the stream's codec, as its
// channel last showed it, the pictures stay on them from decoding to
// encoding, and an HDR10 picture is mapped to SDR where the profile says
// so.
//
// A stream that finds the graphics full is transcoded in software where
// the processor's number at a time allows (D73), and otherwise plays as
// its source sends it. A transcode that fails to start on the graphics
// tries the next way: with decoding in software, then in software. One
// that stops part way while the graphics decode starts again, once, with
// decoding in software, and the channel is decoded in software from then
// on, until the graphics are tested again.

// UseGPUTest gives the hub the options that it tests the graphics with,
// for TestGPU. It is called before the hub serves anything.
func (h *Hub) UseGPUTest(o gpu.Options) { h.gpuTest = o }

// TestGPU tests what the server's graphics can do, logs it, and uses it. It
// reports false, without testing, if a test runs already.
func (h *Hub) TestGPU(ctx context.Context) (gpu.Found, bool) {
	if !h.gpuTesting.CompareAndSwap(false, true) {
		return gpu.Found{}, false
	}
	defer h.gpuTesting.Store(false)
	f := gpu.Test(ctx, h.gpuTest)
	switch {
	case f.Off:
		h.log.Info("the graphics are off in the configuration, so ffmpeg transcodes in software")
	case !f.Usable():
		h.log.Info("no graphics for ffmpeg to transcode on, so it transcodes in software", "why", f.Why, "took", f.Took)
	default:
		var failed []string
		for _, st := range f.Steps {
			if !st.OK {
				failed = append(failed, st.Name)
			}
		}
		h.log.Info("graphics tested for ffmpeg to transcode on", "device", f.Device, "driver", f.Driver,
			"encodes", f.EncodedNames(), "decodes", f.DecodedNames(), "deinterlaces", f.Deinterlace, "scales", f.Scale,
			"frame_rate", f.Rate, "hdr_to_sdr", f.ToneMap, "failed", strings.Join(failed, "; "), "took", f.Took)
	}
	h.UseGPU(f)
	return f, true
}

// GPUTesting reports whether the graphics are being tested now.
func (h *Hub) GPUTesting() bool { return h.gpuTesting.Load() }

// UseGPU tells the hub what the server's graphics can do, as the test
// found. It can be called again after another test, which forgets the
// channels that the graphics failed to decode.
func (h *Hub) UseGPU(f gpu.Found) {
	h.gpu.Store(&f)
	h.encodeMu.Lock()
	h.gpuDecodeFailed = nil
	h.encodeMu.Unlock()
}

// GPU returns what the test found of the server's graphics, and whether it
// has run.
func (h *Hub) GPU() (gpu.Found, bool) {
	f := h.gpu.Load()
	if f == nil {
		return gpu.Found{}, false
	}
	return *f, true
}

// gpuCap returns how many streams at once the graphics may transcode, as
// the administrator set it.
func (h *Hub) gpuCap(ctx context.Context) int {
	n, err := h.store.GPUTranscodes(ctx)
	if err != nil {
		h.log.Warn("cannot look up how many streams the graphics may transcode at once", "error", err)
	}
	return n
}

// gpuMakes reports whether the graphics make a profile's codec.
func (h *Hub) gpuMakes(p streamprofile.Profile) bool {
	f := h.gpu.Load()
	return f != nil && f.Usable() && f.Encodes[p.VideoCodec()].OK
}

// gpuCodec names a stream's video as the test does, "" if not known, and
// "-" for a codec that the test does not try.
func gpuCodec(d streaminfo.Details) string {
	switch d.Video {
	case "":
		return ""
	case "H.264":
		if d.BitDepth > 8 {
			return "-" // the test tries 8 bits only
		}
		return gpu.H264
	case "HEVC":
		if d.BitDepth > 8 {
			return gpu.HEVC10
		}
		return gpu.HEVC
	case "MPEG-2 video":
		return gpu.MPEG2
	}
	return "-"
}

// streamDetails returns what s's stream holds: as read from it now, if
// already read, or else as its channel last showed it.
func (h *Hub) streamDetails(ctx context.Context, s *session) streaminfo.Details {
	if d := s.details.Load(); d != nil && d.Video != "" {
		return *d
	}
	if s.channelID == 0 {
		return streaminfo.Details{}
	}
	health, err := h.store.HealthOf(ctx, s.channelID)
	if err != nil {
		h.log.Warn("cannot look up what a channel's stream held", "channel", s.channel, "error", err)
	}
	return health.Details
}

// hardware returns how the graphics transcode s's stream to its profile,
// and whether they make its codec.
func (h *Hub) hardware(ctx context.Context, s *session) (streamprofile.Hardware, bool) {
	f := h.gpu.Load()
	if f == nil || !f.Usable() {
		return streamprofile.Hardware{}, false
	}
	e := f.Encodes[s.profile.VideoCodec()]
	if !e.OK {
		return streamprofile.Hardware{}, false
	}
	hw := streamprofile.Hardware{Device: f.Device, LowPower: e.LowPower, ICQ: e.ICQ, VBR: e.VBR,
		Filters: f.Filters(), Rate: f.Rate}
	if !hw.Filters {
		return hw, true
	}
	d := h.streamDetails(ctx, s)
	codec := gpuCodec(d)
	h.encodeMu.Lock()
	failed := h.gpuDecodeFailed[s.channelID]
	h.encodeMu.Unlock()
	// A codec not yet known is tried on the graphics: a transcode whose
	// decoding fails there starts with decoding in software.
	hw.Decode = !failed && (codec == "" || f.Decodes[codec])
	hw.ToneMap = s.profile.Made && s.profile.ToneMap && f.ToneMap && d.DynamicRange == "HDR10"
	return hw, true
}

// noteGPUDecodeFailed remembers that the graphics failed to decode a
// channel's stream, so that they do not try again until the next test.
func (h *Hub) noteGPUDecodeFailed(channelID int64) {
	if channelID == 0 {
		return
	}
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	if h.gpuDecodeFailed == nil {
		h.gpuDecodeFailed = map[int64]bool{}
	}
	h.gpuDecodeFailed[channelID] = true
}

// takeGPU counts a transcode on the graphics for deviceID, if fewer than
// limit run for other devices, and reports whether it did; endGPU counts
// it out. A device's own transcodes do not count against it, so that a
// box that changes channel is not held up by the stream that it leaves.
func (h *Hub) takeGPU(deviceID int64, limit int) bool {
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	n := 0
	for id, c := range h.gpuEncodes {
		if id != deviceID {
			n += c
		}
	}
	if n >= limit {
		return false
	}
	h.gpuEncodes[deviceID]++
	return true
}

func (h *Hub) endGPU(deviceID int64) {
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	if h.gpuEncodes[deviceID]--; h.gpuEncodes[deviceID] <= 0 {
		delete(h.gpuEncodes, deviceID)
	}
}

// gpuJob returns the job of transcoding s's stream on the graphics, as hw
// says. instead says what is done when the graphics are full.
func (h *Hub) gpuJob(s *session, hw streamprofile.Hardware, limit int, about []any, instead string) ffmpegJob {
	decode := "software"
	if s.profile.DecodesOnGPU(hw) {
		decode = "graphics"
	}
	about = append(append([]any{}, about[6:]...), "on", "graphics", "decode", decode)
	return ffmpegJob{args: s.profile.HardwareArgs(hw), doing: "transcoding", does: "transcode", how: "on the graphics",
		about: about, watch: true, gpu: true,
		take: func(about []any) bool {
			if !h.takeGPU(s.deviceID, limit) {
				h.log.Info("the graphics transcode as many streams as they may; "+instead, append(about, "most", limit)...)
				return false
			}
			s.encoding.Add(1)
			s.graphics.Add(1)
			return true
		},
		give: func() {
			h.endGPU(s.deviceID)
			s.encoding.Add(-1)
			s.graphics.Add(-1)
		},
	}
}

// gpuJobs returns the jobs that transcode s's stream on the graphics, in
// the order to try them, if the graphics make its profile's codec and may
// transcode: with the stream decoded on the graphics, where they decode
// it, and then decoded in software. instead says what is done when the
// graphics are full.
func (h *Hub) gpuJobs(ctx context.Context, s *session, about []any, instead string) []ffmpegJob {
	hw, ok := h.hardware(ctx, s)
	if !ok {
		return nil
	}
	limit := h.gpuCap(ctx)
	if limit == 0 {
		return nil
	}
	first := h.gpuJob(s, hw, limit, about, instead)
	if !s.profile.DecodesOnGPU(hw) {
		return []ffmpegJob{first}
	}
	soft := hw
	soft.Decode = false
	second := h.gpuJob(s, soft, limit, about, instead)
	second.instead = "it is transcoded on the graphics, decoded in software"
	channel := s.channelID
	first.failed = func() { h.noteGPUDecodeFailed(channel) }
	first.retry = func() (ffmpegJob, bool) {
		h.noteGPUDecodeFailed(channel)
		return second, true
	}
	return []ffmpegJob{first, second}
}

// gpuTranscodes reports whether the graphics may transcode a stream to p
// now: they make its codec, and their number at a time is not 0.
func (h *Hub) gpuTranscodes(ctx context.Context, p streamprofile.Profile) bool {
	return p.Transcodes() && h.gpuMakes(p) && h.gpuCap(ctx) > 0
}
