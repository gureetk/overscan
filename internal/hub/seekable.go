// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
)

// Transcodes that players seek in by bytes (D151). A player of an output
// or a Stalker client seeks in a film by asking for a range of bytes, not
// for a time, as the box's page does. A film that its setting transcodes
// always is made for such a player at a constant rate (media.MakeSeekable),
// so that each byte of the stream is at the time that its place says: the
// stream says how long it is, as a file would, and a range from a byte is
// ffmpeg's transcode from that byte's time, timestamped from it: not the
// same bytes as the stream from the start has there, but the same
// pictures, at the same times. What ffmpeg makes short of the length is
// padding, MPEG-TS's null packets, which players leave out.

// seekable makes plan a transcode that a player can seek in, where its file
// has a known length and the graphics or the processor can keep to a bit
// rate; otherwise it plays from its start, and the log says why.
func (h *Hub) seekable(ctx context.Context, plan *media.Plan, info media.Info, title, src string) {
	before := *plan
	if !plan.MakeSeekable(info) {
		h.log.Warn("a player cannot seek in a title's transcode, since the length of its file is not known; it plays from its start",
			"title", title, "source", src)
		return
	}
	prof := plan.EncodeProfile()
	if !h.gpuKeepsRate(ctx, plan) && !h.softwareEncodes(ctx, prof) {
		*plan = before
		h.log.Warn("a player cannot seek in a title's transcode, since the graphics keep no bit rate for its codec and "+
			"this server does not transcode in software; it plays from its start", "title", title, "source", src,
			"codec", prof.Codec)
	}
}

// gpuKeepsRate reports whether the graphics may transcode plan, keeping to
// a highest bit rate, as a seekable transcode needs.
func (h *Hub) gpuKeepsRate(ctx context.Context, plan *media.Plan) bool {
	prof := plan.EncodeProfile()
	f := h.gpu.Load()
	return h.gpuTranscodes(ctx, prof) && f != nil && f.Encodes[prof.VideoCodec()].VBR
}

// seekRange answers the headers of a request for a seekable transcode and
// returns the writer that sends the range that it asks for, and the time
// that ffmpeg starts from. It reports false when it has answered already:
// a HEAD request, or a range that the stream does not hold.
func seekRange(w http.ResponseWriter, r *http.Request, plan *media.Plan, info *media.Info) (*rangeWriter, float64, bool) {
	total := plan.SeekableLength(info.Duration)
	h := w.Header()
	h.Set("Content-Type", "video/mp2t")
	h.Set("Accept-Ranges", "bytes")
	start, end, partial, ok := byteRange(r.Header.Get("Range"), total)
	if !ok {
		h.Set("Content-Range", "bytes */"+strconv.FormatInt(total, 10))
		http.Error(w, "the stream holds no such range", http.StatusRequestedRangeNotSatisfiable)
		return nil, 0, false
	}
	rw := &rangeWriter{w: w, status: http.StatusOK, pos: start, skip: start % tsPacket, left: end - start + 1}
	if partial {
		rw.status = http.StatusPartialContent
		rw.contentRange = "bytes " + strconv.FormatInt(start, 10) + "-" + strconv.FormatInt(end, 10) + "/" + strconv.FormatInt(total, 10)
	}
	if r.Method == http.MethodHead {
		rw.start()
		return nil, 0, false
	}
	return rw, plan.SeekableTime(start), true
}

// byteRange reads a Range header for a stream of total bytes: the first
// range of bytes it asks for, which partial says it is; the whole stream
// where it asks for none, or in a form that it does not know, which RFC
// 9110 lets a server leave out. ok is false for a range that starts past
// the end.
func byteRange(header string, total int64) (start, end int64, partial, ok bool) {
	if header == "" || !rangeHeader.MatchString(header) {
		return 0, total - 1, false, true
	}
	first, _, _ := strings.Cut(strings.TrimPrefix(header, "bytes="), ",")
	from, to, _ := strings.Cut(strings.TrimSpace(first), "-")
	switch {
	case from == "" && to == "":
		return 0, total - 1, false, true
	case from == "":
		n, err := strconv.ParseInt(to, 10, 64)
		if err != nil || n <= 0 {
			return 0, 0, false, false
		}
		return max(total-n, 0), total - 1, true, true
	}
	start, err := strconv.ParseInt(from, 10, 64)
	if err != nil || start >= total {
		return 0, 0, false, false
	}
	end = total - 1
	if to != "" {
		e, err := strconv.ParseInt(to, 10, 64)
		if err != nil || e < start {
			return 0, total - 1, false, true // not a range: the whole
		}
		end = min(e, total-1)
	}
	return start, end, true, true
}

// errRangeSent ends ffmpeg's run once the range that the player asked for
// is all sent.
var errRangeSent = errors.New("the range is sent")

// rangeWriter sends a range of a seekable transcode: it leaves out the
// bytes of ffmpeg's output before the range's first, which ffmpeg starts at
// the packet that holds it, and stops at its last; pad fills what ffmpeg
// did not make with null packets. Its headers go out with its first byte,
// so that a run that fails before sending anything can still answer with
// an error, or leave the request to the next way to transcode.
type rangeWriter struct {
	w            http.ResponseWriter
	status       int
	contentRange string
	pos          int64 // where the next byte is in the stream
	skip         int64 // bytes of ffmpeg's output still to leave out
	left         int64 // bytes still to send
	started      bool
	passed       bool // an error answer, which goes as it is
}

func (rw *rangeWriter) Header() http.Header         { return rw.w.Header() }
func (rw *rangeWriter) Unwrap() http.ResponseWriter { return rw.w }

// WriteHeader passes on an error's answer, sent before any of the stream.
func (rw *rangeWriter) WriteHeader(code int) {
	if !rw.started {
		rw.started, rw.passed = true, true
		rw.w.WriteHeader(code)
	}
}

// start sends the range's headers.
func (rw *rangeWriter) start() {
	rw.started = true
	h := rw.w.Header()
	h.Set("Content-Length", strconv.FormatInt(rw.left, 10))
	if rw.contentRange != "" {
		h.Set("Content-Range", rw.contentRange)
	}
	rw.w.WriteHeader(rw.status)
}

func (rw *rangeWriter) Write(p []byte) (int, error) {
	if rw.passed {
		return rw.w.Write(p)
	}
	if !rw.started {
		rw.start()
	}
	n := len(p)
	if rw.skip > 0 {
		k := min(rw.skip, int64(len(p)))
		p, rw.skip = p[k:], rw.skip-k
	}
	if int64(len(p)) > rw.left {
		p = p[:rw.left]
	}
	if len(p) > 0 {
		if _, err := rw.w.Write(p); err != nil {
			return 0, err
		}
		rw.pos += int64(len(p))
		rw.left -= int64(len(p))
	}
	if rw.left == 0 {
		return n, errRangeSent
	}
	return n, nil
}

// nullPackets are MPEG-TS packets of the null PID, which a player leaves
// out.
var nullPackets = func() []byte {
	b := make([]byte, 64*tsPacket)
	for i := range b {
		b[i] = 0xff
	}
	for i := 0; i < len(b); i += tsPacket {
		b[i], b[i+1], b[i+2], b[i+3] = 0x47, 0x1f, 0xff, 0x10
	}
	return b
}()

// pad sends null packets to the end of the range, in step with the
// stream's packets.
func (rw *rangeWriter) pad() error {
	if rw.passed {
		return nil
	}
	if !rw.started {
		rw.start()
	}
	for rw.left > 0 {
		off := rw.pos % tsPacket
		n := min(int64(len(nullPackets))-off, rw.left)
		if _, err := rw.w.Write(nullPackets[off : off+n]); err != nil {
			return err
		}
		rw.pos += n
		rw.left -= n
	}
	return nil
}

// seekTail returns the end of s's seekable transcode, from tailFrom to its
// length, which it makes once, as a range of the stream: ffmpeg's transcode
// from tailFrom's time, cut or padded to the length. It reports false where
// it could not be made now; the next request tries again.
func (h *Hub) seekTail(ctx context.Context, s *session, plan *media.Plan, info *media.Info, tailFrom int64,
	about []any) ([]byte, bool) {
	s.tailMu.Lock()
	defer s.tailMu.Unlock()
	if s.tail != nil {
		return s.tail, true
	}
	mem := &memWriter{header: http.Header{}}
	rw := &rangeWriter{w: mem, status: http.StatusOK, pos: tailFrom, left: plan.SeekableLength(info.Duration) - tailFrom}
	h.convert(ctx, rw, s, plan, info, plan.SeekableTime(tailFrom), append(slices.Clip(about), "part", "end"))
	if rw.passed || rw.left > 0 || ctx.Err() != nil {
		return nil, false
	}
	s.tail = mem.buf.Bytes()
	return s.tail, true
}

// memWriter keeps what is written to it, for seekTail.
type memWriter struct {
	header http.Header
	buf    bytes.Buffer
}

func (m *memWriter) Header() http.Header         { return m.header }
func (m *memWriter) WriteHeader(int)             {}
func (m *memWriter) Write(p []byte) (int, error) { return m.buf.Write(p) }

// playerTitle names a player's transcode of a film or an episode.
type playerTitle struct {
	player int64
	kind   string
	id     int64
}

// playerLink returns the path of a player's transcode of item that still
// plays, so that its next request goes on with the same session, and with
// it the stream's end, which it has made once (D151); "" if none.
func (h *Hub) playerLink(playerID int64, item store.PlayableItem) string {
	key := playerTitle{playerID, item.Kind, item.ID}
	h.mu.Lock()
	token, ok := h.playerLinks[key]
	h.mu.Unlock()
	if !ok {
		return ""
	}
	if c, ok := h.verify(token); ok {
		if s := h.lookup(c.nonce); s != nil && s.plan.Load() != nil {
			return "/vod/" + token + ".ts"
		}
	}
	h.mu.Lock()
	delete(h.playerLinks, key)
	h.mu.Unlock()
	return ""
}

// keepPlayerLink keeps the path of a player's transcode of item for
// playerLink, and forgets those whose sessions have ended.
func (h *Hub) keepPlayerLink(playerID int64, item store.PlayableItem, link string) {
	token, _, _ := strings.Cut(strings.TrimPrefix(link, "/vod/"), ".")
	h.mu.Lock()
	defer h.mu.Unlock()
	for k, t := range h.playerLinks {
		if c, ok := h.verify(t); !ok || h.sessions[c.nonce] == nil || h.sessions[c.nonce].ended {
			delete(h.playerLinks, k)
		}
	}
	h.playerLinks[playerTitle{playerID, item.Kind, item.ID}] = token
}
