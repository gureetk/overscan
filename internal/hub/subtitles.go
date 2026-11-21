// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/subtitles"
)

// Subtitles (D69). A film's or an episode's session holds the text subtitle
// tracks that its device may load: files beside it in a folder, or tracks
// that a media server gives. The hub reads a track the first time the
// device asks for it, through the folder or from the server's address,
// which stays on the server, and sends it as SubRip in UTF-8, the form that
// the box loads from an address. For a stream through ffmpeg, which starts
// part way through, the device asks with ?t= and the time it starts at, and
// gets the subtitles moved earlier by it (D68).

// subtitleTimeout caps reading a subtitle track from a server.
const subtitleTimeout = 30 * time.Second

// serveSubtitle serves /vod/<token>/sub/<n>.srt?t=<seconds>: subtitle
// track n of s, moved earlier by t.
func (h *Hub) serveSubtitle(w http.ResponseWriter, r *http.Request, s *session, name string) {
	tracks := s.subtitles.Load()
	digits, ok := strings.CutSuffix(name, ".srt")
	n, err := strconv.Atoi(digits)
	if !ok || err != nil || tracks == nil || n < 0 || n >= len(*tracks) || strconv.Itoa(n) != digits {
		http.NotFound(w, r)
		return
	}
	at, ok := timeParam(r, nil)
	if !ok {
		http.Error(w, "t must be a time in seconds", http.StatusBadRequest)
		return
	}
	cues, err := h.subtitleCues(r.Context(), s, n, (*tracks)[n])
	if err != nil {
		if r.Context().Err() == nil {
			h.log.Warn("cannot read a title's subtitles", "title", s.channel, "source", h.source(s.sourceID).Name,
				"track", n, "error", err)
		}
		http.Error(w, "the subtitles cannot be read", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "application/x-subrip; charset=utf-8")
	if r.Method == http.MethodHead {
		return
	}
	_ = subtitles.WriteSRT(w, subtitles.Shift(cues, time.Duration(at*float64(time.Second))))
}

// subtitleCues returns the cues of subtitle track n of s, reading them the
// first time.
func (h *Hub) subtitleCues(ctx context.Context, s *session, n int, t subtitles.Track) ([]subtitles.Cue, error) {
	s.cuesMu.Lock()
	cues, ok := s.cues[n]
	s.cuesMu.Unlock()
	if ok {
		return cues, nil
	}
	var data []byte
	var err error
	switch {
	case t.File != "" && s.folder != "":
		data, err = folder.ReadSidecar(s.folder, t.File)
	case t.URL != "":
		data, err = h.fetchSubtitles(ctx, s, t.URL)
	default:
		err = errors.New("the track has no file or address")
	}
	if err != nil {
		return nil, err
	}
	if cues, err = subtitles.Parse(data, t.Format, t.Language); err != nil {
		return nil, err
	}
	s.cuesMu.Lock()
	if s.cues == nil {
		s.cues = map[int][]subtitles.Cue{}
	}
	s.cues[n] = cues
	s.cuesMu.Unlock()
	return cues, nil
}

// fetchSubtitles reads a subtitle track from a server's address, which
// errors never quote.
func (h *Hub) fetchSubtitles(ctx context.Context, s *session, address string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, subtitleTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("the subtitles' address is not valid")
	}
	ua := h.source(s.sourceID).UserAgent
	if ua == "" {
		ua = h.userAgent
	}
	req.Header.Set("User-Agent", ua)
	resp, err := h.clientFor(s).Do(req)
	if err != nil {
		return nil, fetchError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the source answered %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, subtitles.MaxBytes+1))
	if err != nil {
		return nil, fetchError(err)
	}
	if len(data) > subtitles.MaxBytes {
		return nil, errors.New("the subtitles are too large")
	}
	return data, nil
}
