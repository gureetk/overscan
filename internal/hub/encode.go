// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bufio"
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
)

// Films and episodes whose video a box cannot play, from a source whose
// server does not transcode (D73). When the administrator lets it, the
// hub's own ffmpeg makes H.264 of the video, in the stream that it makes
// of the file (D68), scaled down to 1080p and mapped from a high dynamic
// range to the standard one. That takes a strong processor, so it starts
// off, and the setting caps how many titles it transcodes at once. A
// device's own transcodes do not count against the cap: a box plays one
// title at a time, and the stream it starts ends the one before.

const (
	// encodeJudgeAfter is how long ffmpeg works on a transcode before the
	// hub judges whether it keeps up with the player.
	encodeJudgeAfter = 20 * time.Second
	// encodeSpeedAfter is how long ffmpeg must have worked for the log to
	// say how fast it went.
	encodeSpeedAfter = 5 * time.Second
	// progressFD is where ffmpeg writes its progress: the first of its
	// extra files.
	progressFD = "pipe:3"
)

// ErrBusy is why a film or an episode cannot start: its video must be
// transcoded, and the hub transcodes as many titles as it may at once.
var ErrBusy = errors.New("the server transcodes as many titles as it may at once")

// Encoder is what the hub's ffmpeg can do to transcode video (D73, D147).
type Encoder struct {
	H264    bool // it encodes H.264, with libx264
	HEVC    bool // it encodes HEVC, with libx265
	ToneMap bool // it maps a high dynamic range to the standard one, with the zscale and tonemap filters
}

// Has reports whether the hub's ffmpeg has a software encoder, by its name:
// libx264 or libx265.
func (e Encoder) Has(name string) bool {
	switch name {
	case "libx264":
		return e.H264
	case "libx265":
		return e.HEVC
	}
	return false
}

// UseEncoder tells the hub what its ffmpeg can do to transcode video. It
// is called before the hub serves anything.
func (h *Hub) UseEncoder(e Encoder) { h.encoder = e }

// Encoder returns what the hub's ffmpeg can do to transcode video.
func (h *Hub) Encoder() Encoder { return h.encoder }

// CanTranscode reports whether the hub's ffmpeg can transcode the video of
// films and episodes that a box cannot play, when the administrator lets
// it.
func (h *Hub) CanTranscode() bool { return h.CanConvert() && h.encoder.H264 }

// transcodeLimit returns how many titles at once the hub's ffmpeg may
// transcode, if it transcodes item's video as plan has it; 0 if it does
// not: the video plays, the source's server transcodes it (D71), ffmpeg
// cannot, or the administrator has not let it.
func (h *Hub) transcodeLimit(ctx context.Context, item store.PlayableItem, plan media.Plan) int {
	if plan.VideoNote == "" || item.TranscodeURL != "" || !h.CanConvert() {
		return 0
	}
	n := 0
	if h.encoder.H264 {
		n = h.transcodeCap(ctx)
	}
	// The graphics transcode it too, where they make H.264 (D150).
	if h.gpuTranscodes(ctx, media.D73Profile) {
		n += h.gpuCap(ctx)
	}
	return n
}

// convertsSound reports whether a film or an episode whose sound or file a
// box cannot play goes through ffmpeg, as the administrator set it (D78).
func (h *Hub) convertsSound(ctx context.Context) bool {
	on, err := h.store.ConvertsSound(ctx)
	if err != nil {
		h.log.Warn("cannot look up whether Overscan converts films", "error", err)
	}
	return on
}

// serverTranscodes reports whether a film or an episode whose video a box
// cannot play goes through its source's server where it transcodes, as
// the administrator set it (D78).
func (h *Hub) serverTranscodes(ctx context.Context) bool {
	on, err := h.store.ServerTranscodes(ctx)
	if err != nil {
		h.log.Warn("cannot look up whether sources' servers transcode films", "error", err)
	}
	return on
}

// transcodeCap returns how many titles at once the hub's ffmpeg may
// transcode, as the administrator set it.
func (h *Hub) transcodeCap(ctx context.Context) int {
	n, err := h.store.Transcodes(ctx)
	if err != nil {
		h.log.Warn("cannot look up how many titles Overscan may transcode at once", "error", err)
	}
	return n
}

// encodesElsewhere returns how many transcodes run for devices other than
// deviceID.
func (h *Hub) encodesElsewhere(deviceID int64) int {
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	return h.otherEncodes(deviceID)
}

// otherEncodes is encodesElsewhere for a caller that holds h.encodeMu.
func (h *Hub) otherEncodes(deviceID int64) int {
	n := 0
	for id, c := range h.encodes {
		if id != deviceID {
			n += c
		}
	}
	return n
}

// takeEncode counts a transcode for deviceID, if fewer than limit run for
// other devices, and reports whether it did; endEncode counts it out.
func (h *Hub) takeEncode(deviceID int64, limit int) bool {
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	if h.otherEncodes(deviceID) >= limit {
		return false
	}
	h.encodes[deviceID]++
	return true
}

func (h *Hub) endEncode(deviceID int64) {
	h.encodeMu.Lock()
	defer h.encodeMu.Unlock()
	if h.encodes[deviceID]--; h.encodes[deviceID] <= 0 {
		delete(h.encodes, deviceID)
	}
}

// encodeProgress follows how far a transcode has got, from what ffmpeg
// writes with -progress, and how long the hub has waited for it.
type encodeProgress struct {
	made atomic.Int64 // microseconds of video made, as ffmpeg's out_time_us says
	done chan struct{}

	// The hub's own: how long it waited for ffmpeg's output once the first
	// came, which is how long ffmpeg worked; the wait for the player to
	// take what it sent does not count, as a paused box does not slow
	// ffmpeg down.
	output bool
	waited time.Duration
	judged bool
}

// followProgress reads what ffmpeg writes with -progress from r until it
// ends, keeping how much video it has made in p.
func followProgress(r io.Reader) *encodeProgress {
	p := &encodeProgress{done: make(chan struct{})}
	go func() {
		defer close(p.done)
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "out_time_us="); ok {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
					p.made.Store(n)
				}
			}
		}
		// Whatever went wrong, ffmpeg must not wait for its progress to be
		// read.
		_, _ = io.Copy(io.Discard, r)
	}()
	return p
}

// read counts a wait of d for ffmpeg's output, which brought n bytes.
func (p *encodeProgress) read(n int, d time.Duration) {
	if p.output {
		p.waited += d
	}
	if n > 0 {
		p.output = true
	}
}

// speed returns how many seconds of video ffmpeg made in each second it
// worked, and whether it has worked long enough to say.
func (p *encodeProgress) speed(after time.Duration) (float64, bool) {
	if p.waited < after || p.waited <= 0 {
		return 0, false
	}
	return float64(p.made.Load()) / float64(p.waited.Microseconds()), true
}

// judgeEncode warns, once, when ffmpeg has worked on a transcode long
// enough to tell that it makes the video more slowly than the box plays it.
func (h *Hub) judgeEncode(p *encodeProgress, about []any) {
	if p.judged {
		return
	}
	speed, ok := p.speed(encodeJudgeAfter)
	if !ok {
		return
	}
	p.judged = true
	if speed < 1 {
		h.log.Warn("ffmpeg transcodes a title more slowly than the box plays it, so the box will stop to wait for it",
			append(slices.Clip(about), "speed", formatSpeed(speed))...)
	}
}

// encodeEnded logs, as a transcode ends, how much of the video ffmpeg made,
// and how fast.
func (h *Hub) encodeEnded(p *encodeProgress, about []any) {
	select {
	case <-p.done:
	case <-time.After(time.Second):
	}
	end := append(slices.Clip(about), "made", clockTime(float64(p.made.Load())/1e6))
	if speed, ok := p.speed(encodeSpeedAfter); ok {
		end = append(end, "speed", formatSpeed(speed))
	}
	h.log.Info("ffmpeg's transcode of a title ended", end...)
}

// formatSpeed writes a transcode's speed for the log, such as "1.25x".
func formatSpeed(speed float64) string {
	return strconv.FormatFloat(speed, 'f', 2, 64) + "x"
}

// encodeSummary describes what a transcode makes of the video for the log,
// such as "h264 1920x800, mapped to the standard dynamic range".
func encodeSummary(plan media.Plan) string {
	d := plan.EncodeProfile().VideoCodec()
	if plan.Width > 0 && plan.Height > 0 {
		d += " " + strconv.Itoa(plan.Width) + "x" + strconv.Itoa(plan.Height)
	}
	if plan.ToneMap {
		d += ", mapped to the standard dynamic range"
	}
	return d
}
