// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"overscan.gureet.ca/internal/adapters/jellyfin"
	"overscan.gureet.ca/internal/media"
)

// Films and episodes whose video a box cannot play, from a source whose
// server transcodes (D71): a Jellyfin server makes H.264 of the video in
// MPEG-TS, with one sound track, and the hub passes it on as it passes on
// a file. Like a stream through ffmpeg (D68), it has no byte positions: the
// device asks for it from a time, and the server starts its transcode
// there, at that very time; a sound track is picked the same way.

const (
	// transcodeStartTimeout is how long a source's server may take to
	// start sending a stream that it transcodes.
	transcodeStartTimeout = 45 * time.Second
	// transcodeStopTimeout caps asking the server to stop a transcode.
	transcodeStopTimeout = 5 * time.Second
)

// trackParam returns the sound track that a request's a parameter picks,
// as its place among plan's tracks: 0 if there is none, and false if it
// makes no sense.
func trackParam(r *http.Request, plan *media.Plan) (int, bool) {
	v := r.URL.Query().Get("a")
	if v == "" {
		return 0, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n >= max(len(plan.Tracks), 1) {
		return 0, false
	}
	return n, true
}

// serveTranscoded serves /vod/<token>.ts?t=<seconds>&a=<track>: the stream
// that s's source's server transcodes, from that time, with that sound
// track. A new request ends the one before, as a seek does, and the hub
// asks the server to stop the one before at once.
func (h *Hub) serveTranscoded(w http.ResponseWriter, r *http.Request, s *session, plan *media.Plan, info *media.Info) {
	from, ok := timeParam(r, info)
	if !ok {
		http.Error(w, "t must be a time in seconds", http.StatusBadRequest)
		return
	}
	track, ok := trackParam(r, plan)
	if !ok {
		http.Error(w, "a must be one of the sound tracks", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.Header().Set("Accept-Ranges", "none")
	if r.Method == http.MethodHead {
		return
	}
	ctx, release, ok := h.takeTurn(r.Context(), s, w)
	if !ok {
		return
	}
	defer release()
	h.begin(s)
	defer h.finish(s)
	s.transcoding.Add(1)
	defer s.transcoding.Add(-1)

	src := h.source(s.sourceID)
	about := []any{"title", s.channel, "source", src.Name, "device", s.deviceID}
	audio := -1
	if len(plan.Tracks) > 0 {
		audio = plan.Tracks[track].Index
	}
	device, playSession := "overscan-"+strconv.FormatInt(s.deviceID, 10), newPlaySession()
	address, err := jellyfin.TranscodeURL(s.transcodeURL, jellyfin.Transcode{From: from, Audio: audio,
		AudioCodecs: plan.Sound, DeviceID: device, PlaySessionID: playSession})
	if err != nil {
		h.log.Error("cannot ask a title's source to transcode it", append(about, "error", err)...)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		h.log.Error("cannot ask a title's source to transcode it", append(about, "error", fetchError(err))...)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	ua := src.UserAgent
	if ua == "" {
		ua = h.userAgent
	}
	req.Header.Set("User-Agent", ua)
	h.log.Info("a title's source transcodes it", append(about, "from", clockTime(from), "track", track)...)
	started := h.now()
	resp, err := h.transcodes.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("cannot reach a title's source to transcode it", append(about, "error", fetchError(err))...)
			http.Error(w, "the source cannot be reached", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()
	// However the stream ends, the server stops making it; asking it
	// spares it the few seconds it waits for the request to come back.
	defer h.stopTranscode(s, device, playSession)
	if resp.StatusCode != http.StatusOK {
		h.log.Warn("a title's source would not transcode it", append(about, "status", resp.Status)...)
		http.Error(w, "the source answered "+resp.Status, http.StatusBadGateway)
		return
	}
	rc := http.NewResponseController(w)
	buf := make([]byte, copyBufferBytes)
	var sent int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // the player went away
			}
			_ = rc.Flush()
			s.bytes.Add(int64(n))
			sent += int64(n)
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			if ctx.Err() == nil {
				h.log.Info("a title's source broke off its transcode; the player can ask again from where it got to",
					append(about, "after", h.now().Sub(started).Round(100*time.Millisecond), "bytes", sent,
						"error", fetchError(rerr))...)
			}
			return
		}
	}
}

// stopTranscode asks s's source's server to stop the stream it transcodes
// for a play session, in the background.
func (h *Hub) stopTranscode(s *session, device, playSession string) {
	address, err := jellyfin.StopURL(s.transcodeURL, device, playSession)
	if err != nil {
		return
	}
	ua := h.source(s.sourceID).UserAgent
	if ua == "" {
		ua = h.userAgent
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), transcodeStopTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, address, nil)
		if err != nil {
			return
		}
		req.Header.Set("User-Agent", ua)
		resp, err := h.client.Do(req)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				h.log.Debug("cannot ask a title's source to stop transcoding it", "title", s.channel, "error", fetchError(err))
			}
			return
		}
		resp.Body.Close()
	}()
}

// newPlaySession returns a name for a stream that a source's server
// transcodes, apart from every other.
func newPlaySession() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // never fails; see crypto/rand
	return hex.EncodeToString(b)
}
