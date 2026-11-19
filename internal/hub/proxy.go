// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bufio"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/hls"
)

const (
	// maxPlaylistBytes caps an HLS playlist.
	maxPlaylistBytes = 2 << 20

	// maxResources caps how many HLS resources a session remembers; a live
	// playlist only ever lists the last few segments.
	maxResources = 1024

	copyBufferBytes = 64 << 10
)

// proxy plays target, an upstream address, to the device as part of s.
// continuous says that the device asked for a continuous stream: if the
// source answers with an HLS playlist, the relay plays it as one (D42).
// Otherwise an HLS playlist is rewritten so that every address in it goes
// through the hub, and anything else is copied as it arrives. A continuous
// stream goes through the repeat filter (D43), and then through ffmpeg if
// its channel is set to be repaired (D57). A continuous link made for an
// HLS address so that it can be repaired (D61) is refused if the relay
// cannot play the stream.
func (h *Hub) proxy(w http.ResponseWriter, r *http.Request, s *session, target string, continuous bool) {
	h.begin(s)
	defer h.finish(s)

	// The request ends when the device goes away or the session ends.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()

	src := h.source(s.sourceID).Name
	resp, err := h.get(ctx, s, target)
	if err != nil {
		var refused *statusError
		switch {
		case ctx.Err() != nil:
		case errors.As(err, &refused):
			h.log.Warn("a stream's source refused it", "channel", s.channel, "source", src, "status", refused.status)
			http.Error(w, "the source answered "+refused.status, http.StatusBadGateway)
		default:
			h.log.Warn("cannot reach a stream", "channel", s.channel, "source", src, "error", err)
			http.Error(w, "the source cannot be reached", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()

	body := bufio.NewReaderSize(resp.Body, copyBufferBytes)
	if isPlaylist(resp, body) {
		data, err := readPlaylist(body)
		resp.Body.Close()
		switch {
		case errors.Is(err, errPlaylistTooLarge):
			h.log.Warn("an HLS playlist is too large", "channel", s.channel, "source", src)
			http.Error(w, "the playlist is too large", http.StatusBadGateway)
			return
		case err != nil:
			http.Error(w, "the source broke off", http.StatusBadGateway)
			return
		}
		if continuous {
			relayed, unsupported := h.relayHLS(ctx, w, s, target, resp.Request.URL, data)
			switch {
			case relayed:
				return
			case h.relayingToRepair(s):
				// The link is continuous only so that the stream can be
				// repaired, and the player it went to may not play a
				// playlist: refuse, so that the player asks again, and
				// gets an HLS link.
				h.cannotRelay(s, unsupported)
				http.Error(w, "this HLS stream cannot be made one stream to repair it; ask for the channel again", http.StatusBadGateway)
				return
			}
			h.log.Warn("cannot relay an HLS stream; the player gets its playlist",
				"channel", s.channel, "source", src, "reason", "it has "+unsupported)
		}
		h.writePlaylist(w, s, resp.Request.URL, data)
		return
	}

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	// A repaired stream's length is ffmpeg's to say.
	if cl := resp.Header.Get("Content-Length"); cl != "" && !(continuous && h.repairs(s)) {
		w.Header().Set("Content-Length", cl)
	}
	w.WriteHeader(http.StatusOK)
	dst := h.sink(ctx, w, s, continuous)
	defer dst.close()
	var out io.Writer = dst
	var filter *repeatFilter
	if continuous {
		filter = h.repeatFilter(dst, s)
		out = filter
	}
	buf := make([]byte, copyBufferBytes)
	started := h.now()
	var sent int64
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				return
			}
			dst.flush()
			s.bytes.Add(int64(n))
			sent += int64(n)
		}
		if rerr != nil {
			if filter == nil || filter.Close() == nil {
				dst.finish()
			}
			// A live stream should not end while its player is there: say
			// so, since the player then stalls or starts it again.
			if continuous && ctx.Err() == nil {
				after := h.now().Sub(started).Round(100 * time.Millisecond)
				if rerr == io.EOF {
					h.log.Info("the source ended a stream", "channel", s.channel, "source", src, "after", after, "bytes", sent)
				} else {
					h.log.Warn("a stream's source broke off", "channel", s.channel, "source", src, "after", after, "bytes", sent, "error", fetchError(rerr))
				}
			}
			return
		}
	}
}

// statusError is an upstream answer other than 200 OK.
type statusError struct {
	code   int
	status string // such as "404 Not Found"
}

func (e *statusError) Error() string { return "the source answered " + e.status }

// get requests target from upstream for s, with the source's user agent.
// An answer other than 200 OK is a *statusError. Errors never quote the
// address, which can hold credentials.
func (h *Hub) get(ctx context.Context, s *session, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fetchError(err)
	}
	ua := h.source(s.sourceID).UserAgent
	if ua == "" {
		ua = h.userAgent
	}
	req.Header.Set("User-Agent", ua)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fetchError(err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &statusError{code: resp.StatusCode, status: resp.Status}
	}
	return resp, nil
}

// isPlaylist reports whether an upstream response is an HLS playlist: by its
// content type, its address, or its first bytes, since servers often label
// playlists as text or binary.
func isPlaylist(resp *http.Response, body *bufio.Reader) bool {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	switch strings.ToLower(mt) {
	case "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl":
		return true
	}
	if strings.EqualFold(path.Ext(resp.Request.URL.Path), ".m3u8") {
		return true
	}
	start, _ := body.Peek(64)
	return hls.IsPlaylist(start)
}

var errPlaylistTooLarge = errors.New("the playlist is too large")

// readPlaylist reads an HLS playlist of at most maxPlaylistBytes.
func readPlaylist(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, maxPlaylistBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPlaylistBytes {
		return nil, errPlaylistTooLarge
	}
	return data, nil
}

// uriAttr matches the URI attribute of an HLS tag, such as #EXT-X-KEY.
var uriAttr = regexp.MustCompile(`URI="([^"]*)"`)

// writePlaylist sends an HLS playlist with every address in it, resolved
// against base, replaced by a hub resource of s.
func (h *Hub) writePlaylist(w http.ResponseWriter, s *session, base *url.URL, data []byte) {
	h.mu.Lock()
	s.hls = true
	var out strings.Builder
	for line := range strings.Lines(string(data)) {
		text := strings.TrimRight(line, "\r\n")
		trimmed := strings.TrimSpace(text)
		switch {
		case trimmed == "":
			out.WriteString(text)
		case strings.HasPrefix(trimmed, "#"):
			out.WriteString(uriAttr.ReplaceAllStringFunc(text, func(m string) string {
				uri := uriAttr.FindStringSubmatch(m)[1]
				if p, ok := h.resourcePath(s, base, uri); ok {
					return `URI="` + p + `"`
				}
				return `URI=""`
			}))
		default:
			if p, ok := h.resourcePath(s, base, trimmed); ok {
				out.WriteString(p)
			}
		}
		out.WriteString("\n")
	}
	h.mu.Unlock()

	w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
	_, _ = io.WriteString(w, out.String())
}

// resourcePath returns the hub path for ref, an address in a playlist of s
// relative to base. Only http and https addresses are served. It needs h.mu.
func (h *Hub) resourcePath(s *session, base *url.URL, ref string) (string, bool) {
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	target := u.String()
	n, ok := s.indexes[target]
	if !ok {
		n = s.next
		s.next++
		s.resources[n] = target
		s.indexes[target] = n
		if old, ok := s.resources[n-maxResources]; ok {
			delete(s.resources, n-maxResources)
			delete(s.indexes, old)
		}
	}
	return "/live/" + s.token + "/r/" + strconv.Itoa(n) + extension(u.Path), true
}

// extension returns the extension of p if it is short and plain, such as
// .ts, .aac or .m3u8, which some players look at; "" otherwise.
func extension(p string) string {
	ext := strings.ToLower(path.Ext(p))
	if len(ext) < 2 || len(ext) > 6 {
		return ""
	}
	for _, r := range ext[1:] {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}
