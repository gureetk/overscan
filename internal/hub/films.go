// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/gpu"
	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
)

// Films and series through the same profiles and graphics as live TV
// (D150). All films and series, a shelf and a title are each transcoded as
// needed, always with a profile, or never; the narrowest that says
// something wins, and a series' episodes follow it.
//
// As needed is D73's plan from the box's playback check: a video that the
// box cannot play is transcoded to H.264, by the source's server where it
// transcodes (D71), and else by Overscan's ffmpeg, now on the graphics
// where they make H.264. Always transcodes the title with its profile,
// whether or not the box plays it, on the graphics where they make the
// profile's codec, and else in software. Never sends its video as it is;
// the conversion of its sound still follows its own switch (D78).
//
// A transcode on the graphics takes one of their own number at a time; one
// that finds them full is transcoded in software where D73's number allows,
// and one that fails on them before sending anything is tried again with
// decoding in software, then in software.

// titleTranscode returns what is transcoded of a film or an episode, and
// where it comes from.
func (h *Hub) titleTranscode(ctx context.Context, item store.PlayableItem) (store.Transcode, string) {
	t, by, err := h.store.TitleTranscode(ctx, item.TitleID)
	if err != nil {
		h.log.Warn("cannot look up what is transcoded of a title; it is transcoded as needed", "title", item.Name, "error", err)
		return store.Transcode{}, ""
	}
	return t, by
}

// alwaysProfile returns the profile that a title's setting transcodes it
// with always, if this server can make it: on the graphics or in software.
// It logs why not, and the title is then transcoded as needed.
func (h *Hub) alwaysProfile(ctx context.Context, item store.PlayableItem, t store.Transcode, by string) (streamprofile.Profile, bool) {
	p, err := h.store.TranscodeProfile(ctx, t.Profile)
	if err != nil {
		h.log.Error("cannot look up the profile that a title is transcoded with; it is transcoded as needed",
			"title", item.Name, "profile", t.Profile, "error", err)
		return streamprofile.Profile{}, false
	}
	if !p.Made {
		p = p.AsMade()
	}
	if !h.CanConvert() || (!h.softwareEncodes(ctx, p) && !h.gpuTranscodes(ctx, p)) {
		h.log.Warn("a title is set to be transcoded always, but this server cannot transcode with its profile now; it is transcoded as needed",
			"title", item.Name, "profile", p.Name, "set_by", by, "needs", p.Encoder())
		return streamprofile.Profile{}, false
	}
	return p, true
}

// softwareEncodes reports whether ffmpeg may transcode a title to p in
// software now: it has p's encoder, and D73's number at a time is not 0.
func (h *Hub) softwareEncodes(ctx context.Context, p streamprofile.Profile) bool {
	return h.encoder.Has(p.Encoder()) && h.transcodeCap(ctx) > 0
}

// roomToEncode reports whether a transcode of a title to p could start now
// for deviceID: on the graphics, or in software, each below its number at a
// time, as the transcodes of other devices count.
func (h *Hub) roomToEncode(ctx context.Context, deviceID int64, p streamprofile.Profile) bool {
	if h.gpuTranscodes(ctx, p) && h.gpuOthers(deviceID) < h.gpuCap(ctx) {
		return true
	}
	return h.softwareEncodes(ctx, p) && h.encodesElsewhere(deviceID) < h.transcodeCap(ctx)
}

// gpuOthers counts the transcodes on the graphics of devices other than
// deviceID.
func (h *Hub) gpuOthers(deviceID int64) int {
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	n := 0
	for id, c := range h.gpuEncodes {
		if id != deviceID {
			n += c
		}
	}
	return n
}

// filmCodec names a file's video as the test of the graphics does, "-" for
// one that it does not try: the test tries HEVC at 8 and 10 bits, and the
// others at 8 bits only.
func filmCodec(v *media.Stream) string {
	if v.Codec == "hevc" {
		if v.BitDepth > 8 {
			return gpu.HEVC10
		}
		return gpu.HEVC
	}
	if v.BitDepth > 8 {
		return "-"
	}
	switch v.Codec {
	case "h264":
		return gpu.H264
	case "mpeg2video":
		return gpu.MPEG2
	case "av1":
		return gpu.AV1
	case "vp9":
		return gpu.VP9
	}
	return "-"
}

// filmHardware returns how the graphics transcode a title as plan says,
// and whether they do: they make the profile's codec; they decode the
// video where they decode its codec and filter it; and they map its HDR10
// to SDR where the plan asks for it and they can. A video with a high
// dynamic range that they cannot map, as HLG, is left to software where it
// can map it and may transcode.
func (h *Hub) filmHardware(ctx context.Context, plan *media.Plan, info *media.Info) (streamprofile.Hardware, bool) {
	f := h.gpu.Load()
	prof := plan.EncodeProfile()
	if f == nil || !f.Usable() {
		return streamprofile.Hardware{}, false
	}
	e := f.Encodes[prof.VideoCodec()]
	if !e.OK {
		return streamprofile.Hardware{}, false
	}
	hw := streamprofile.Hardware{Device: f.Device, LowPower: e.LowPower, ICQ: e.ICQ, VBR: e.VBR, Filters: f.Filters(), Rate: f.Rate}
	v := info.First(media.Video)
	if v == nil {
		return hw, true
	}
	hw.Decode = hw.Filters && f.Decodes[filmCodec(v)]
	if prof.ToneMap && v.HDR != "" {
		switch {
		case v.HDR == media.HDRPQ && f.ToneMap && hw.Filters:
			hw.ToneMap = true
		case h.encoder.ToneMap && h.softwareEncodes(ctx, prof):
			return streamprofile.Hardware{}, false
		}
	}
	return hw, true
}

// filmAttempt is one way to transcode a title: on the graphics, decoding
// there or in software, or in software.
type filmAttempt struct {
	plan media.Plan
	gpu  bool
	how  string // "on the graphics" or "in software", for the log
	take func() bool
	give func()
}

// filmAttempts returns the ways to transcode s's title as plan says, in
// the order to try them: on the graphics, decoding there where they can,
// then decoding in software; then in software, where ffmpeg has the
// encoder. A link made before the administrator turned transcoding off
// still plays in software, as the one transcode that may run, unless the
// graphics may transcode it.
func (h *Hub) filmAttempts(ctx context.Context, s *session, plan *media.Plan, info *media.Info) []filmAttempt {
	prof := plan.EncodeProfile()
	var out []filmAttempt
	if hw, ok := h.filmHardware(ctx, plan, info); ok {
		if limit := h.gpuCap(ctx); limit > 0 {
			add := func(hw streamprofile.Hardware) {
				p := *plan
				p.Hardware = &hw
				out = append(out, filmAttempt{plan: p, gpu: true, how: "on the graphics",
					take: func() bool { return h.takeGPU(s.deviceID, limit) },
					give: func() { h.endGPU(s.deviceID) }})
			}
			add(hw)
			if prof.DecodesOnGPU(hw) {
				hw.Decode = false
				add(hw)
			}
		}
	}
	// With transcoding in software off, a title that the graphics may
	// transcode is never transcoded in software instead.
	if limit := h.transcodeCap(ctx); h.encoder.Has(prof.Encoder()) && (limit > 0 || len(out) == 0) {
		limit = max(limit, 1)
		out = append(out, filmAttempt{plan: *plan, how: "in software",
			take: func() bool { return h.takeEncode(s.deviceID, limit) },
			give: func() { h.endEncode(s.deviceID) }})
	}
	return out
}

// encodeMost says, for the log, how many titles the graphics and the
// software may transcode at once to p, such as "2 on the graphics, 1 in
// software", or "1" without the graphics.
func (h *Hub) encodeMost(ctx context.Context, p streamprofile.Profile) string {
	software := max(h.transcodeCap(ctx), 1)
	if !h.gpuTranscodes(ctx, p) {
		return strconv.Itoa(software)
	}
	parts := []string{fmt.Sprintf("%d on the graphics", h.gpuCap(ctx))}
	if h.encoder.Has(p.Encoder()) {
		parts = append(parts, fmt.Sprintf("%d in software", software))
	}
	return strings.Join(parts, ", ")
}

// gpuMapsHDR reports whether the graphics map a video's high dynamic range
// to the standard one: HDR10, where the test found that they can.
func (h *Hub) gpuMapsHDR(v *media.Stream) bool {
	f := h.gpu.Load()
	return f != nil && f.Usable() && f.ToneMap && f.Filters() && v.HDR == media.HDRPQ
}
