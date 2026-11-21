// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/store"
)

// Films and episodes (D64). A film or an episode is a file on its source,
// which a player reads in parts: it asks for a range of bytes to start, and
// for another each time it seeks. The hub passes each range request on to
// the source and its answer back, so the player can seek, and its session
// carries the requests for as long as the player keeps playing, however
// long the film. A session makes one request to its source at a time: a new
// one ends the one before, since a source counts connections, and a player
// that seeks has no more use for the old one. A film or an episode in a
// folder on the server is a file that the hub reads itself (D67).

// ItemLink returns the path of a new link that plays a film or an episode
// for a device, as it is. It ends in the file's extension, which some
// players look at. Prepare says whether a box plays the file as it is.
func (h *Hub) ItemLink(deviceID int64, item store.PlayableItem) string {
	return "/vod/" + h.sign(h.itemClaims(deviceID, item)) + "." + item.Container
}

// serveItem serves /vod/<token>.<ext>: it starts or goes on with the
// session of a film or an episode, and serves the request as part of it.
// A link to a stream that ffmpeg makes needs the session that Prepare
// started, and serves /vod/<token>/start too (D68). Any link serves its
// session's subtitles at /vod/<token>/sub/<n>.srt (D69).
func (h *Hub) serveItem(w http.ResponseWriter, r *http.Request, rest string) {
	first, sub, hasSub := strings.Cut(rest, "/")
	token, _, _ := strings.Cut(first, ".")
	c, ok := h.verify(token)
	subtitle, isSubtitle := strings.CutPrefix(sub, "sub/")
	if !ok || c.kind != kindMovie && c.kind != kindEpisode || hasSub && !isSubtitle && (sub != "start" || !c.convert) {
		http.Error(w, "invalid link", http.StatusForbidden)
		return
	}
	s := h.lookup(c.nonce)
	if isSubtitle {
		if s == nil {
			http.Error(w, "this link has expired; ask for a new one", http.StatusForbidden)
			return
		}
		h.serveSubtitle(w, r, s, subtitle)
		return
	}
	if c.convert {
		if s == nil || s.plan.Load() == nil {
			http.Error(w, "this link has expired; ask for a new one", http.StatusForbidden)
			return
		}
		if hasSub {
			h.serveStart(w, r, s)
		} else {
			h.serveConverted(w, r, s)
		}
		return
	}
	if s == nil {
		if h.now().Unix() > c.expires {
			http.Error(w, "this link has expired; ask for a new one", http.StatusForbidden)
			return
		}
		var item store.PlayableItem
		var err error
		if c.kind == kindMovie {
			item, err = h.store.PlayableMovie(r.Context(), c.entryID)
		} else {
			item, err = h.store.PlayableEpisode(r.Context(), c.entryID)
		}
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "this title is not available", http.StatusNotFound)
			return
		}
		if err != nil {
			h.log.Error("cannot look up a title to play", "kind", playsKind(c.kind), "id", c.entryID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if !h.usesSource(w, r, c.deviceID, item.SourceID, "this title is not available") {
			return
		}
		s, err = h.openItem(r.Context(), c, token, item)
		if errors.Is(err, ErrNoLink) {
			h.resolveFailed(w, r, "title", item.Name, item.SourceID, c.deviceID, err)
			return
		}
		if err != nil {
			h.refused(w, err, "title", item.Name, item.SourceID, c.deviceID)
			return
		}
		// What the file held when it was read, if it was, so that a file
		// that has changed since is read again (D68).
		if info, ok, err := h.store.Media(r.Context(), s.mediaKey[0], s.mediaKey[1]); err == nil && ok && s.info.Load() == nil {
			s.info.Store(&info)
		}
	}
	h.serveFile(w, r, s)
}

// rangeHeader matches the byte ranges that a player may ask for.
var rangeHeader = regexp.MustCompile(`^bytes=[0-9]*-[0-9]*( *, *[0-9]*-[0-9]*)*$`)

// fileHeaders are the headers of a source's answer that the player gets.
var fileHeaders = []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified", "ETag"}

var (
	// fileIdleTimeout is how long a source may send nothing of a file while
	// the hub waits for it, before the hub ends the request: a player left
	// waiting on a silent connection can hang a box, and asks again from
	// where it got to.
	fileIdleTimeout = 30 * time.Second

	// refusedWaits are how long the hub waits before it asks a source again
	// for a file that it refused as if it had no free connection: a source
	// may still count, for a moment, the connection that the request before
	// just closed.
	refusedWaits = []time.Duration{time.Second, 2 * time.Second}
)

// busyRefusal reports whether an answer's status says that a source has no
// free connection, or none for now.
func busyRefusal(code int) bool {
	return code == http.StatusForbidden || code == http.StatusTooManyRequests ||
		code == http.StatusServiceUnavailable || code == 509 // Bandwidth Limit Exceeded, which some sources send
}

// openFile requests a film or an episode from its source for s, as getFile
// does, and returns its answer with a body that ends the request if the
// source sends nothing for fileIdleTimeout, which idle then reports.
func (h *Hub) openFile(ctx context.Context, s *session, rangeAsked, ifRange string) (*http.Response, func() bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	resp, err := h.getFile(ctx, s, rangeAsked, ifRange)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	var idled atomic.Bool
	timer := time.AfterFunc(fileIdleTimeout, func() {
		idled.Store(true)
		cancel()
	})
	timer.Stop()
	resp.Body = &idleBody{ReadCloser: resp.Body, timer: timer, cancel: cancel}
	return resp, idled.Load, nil
}

// idleBody is an answer's body whose reads may wait for the source for
// fileIdleTimeout at most; closing it ends its request.
type idleBody struct {
	io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (b *idleBody) Read(p []byte) (int, error) {
	b.timer.Reset(fileIdleTimeout)
	n, err := b.ReadCloser.Read(p)
	b.timer.Stop()
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// takeTurn takes s's turn to make a request to its source, from a
// request with context parent that w answers, if any: it ends the request
// before, and waits until it is done (D64). It returns the turn's context,
// which ends when the next turn starts or s ends, and a function that gives
// the turn back; or false if parent ended while it waited.
func (h *Hub) takeTurn(parent context.Context, s *session, w http.ResponseWriter) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancel(parent)
	turn := newTurn(cancel, w)
	stop := context.AfterFunc(s.ctx, turn.end)
	h.mu.Lock()
	before := s.turn
	s.turn = turn
	h.mu.Unlock()
	release := func() {
		h.mu.Lock()
		if s.turn == turn {
			s.turn = nil
		}
		h.mu.Unlock()
		turn.finish()
		stop()
		cancel()
	}
	if before != nil {
		before.end()
		select {
		case <-before.done:
		case <-ctx.Done():
			release()
			return nil, nil, false
		}
	}
	return ctx, release, true
}

// serveFile serves a player's request for a film or an episode, as part of
// s: it takes s's turn to make a request to the source, ending the request
// before, passes on the player's range, and copies the source's answer
// back. A HEAD request gets the answer's headers alone.
func (h *Hub) serveFile(w http.ResponseWriter, r *http.Request, s *session) {
	ctx, release, ok := h.takeTurn(r.Context(), s, w)
	if !ok {
		return
	}
	defer release()
	h.begin(s)
	defer h.finish(s)
	if s.folder != "" {
		h.serveVideo(ctx, w, r, s)
		return
	}
	src := h.source(s.sourceID).Name
	asked := r.Header.Get("Range")
	if asked != "" && (len(asked) > 200 || !rangeHeader.MatchString(asked)) {
		asked = "" // the whole file, rather than a range the source may take wrongly
	}
	h.log.Debug("a player asked for a file", s.what(), s.channel, "source", src, "device", s.deviceID, "range", asked)
	resp, idled, err := h.openFile(ctx, s, asked, r.Header.Get("If-Range"))
	if err != nil {
		var refused *statusError
		switch {
		case ctx.Err() != nil:
		case errors.As(err, &refused):
			h.log.Warn("a title's source refused it", s.what(), s.channel, "source", src, "status", refused.status)
			http.Error(w, "the source answered "+refused.status, http.StatusBadGateway)
		default:
			h.log.Warn("cannot reach a title", s.what(), s.channel, "source", src, "error", err)
			http.Error(w, "the source cannot be reached", http.StatusBadGateway)
		}
		return
	}
	defer resp.Body.Close()
	h.checkSize(s, resp)
	for _, k := range fileHeaders {
		if v := resp.Header.Get(k); v != "" {
			w.Header().Set(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead || resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return
	}
	buf := make([]byte, copyBufferBytes)
	started := h.now()
	var sent int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			s.bytes.Add(int64(n))
			sent += int64(n)
		}
		if rerr == io.EOF {
			return
		}
		if rerr != nil {
			switch {
			case idled():
				h.log.Info("a title's source sent nothing for a while, so the hub ended the request; the player can ask again from where it got to",
					s.what(), s.channel, "source", src, "after", h.now().Sub(started).Round(100*time.Millisecond), "bytes", sent,
					"silent_for", fileIdleTimeout)
			case ctx.Err() == nil:
				h.log.Info("a title's source broke off; the player can ask again from where it got to", s.what(), s.channel,
					"source", src, "after", h.now().Sub(started).Round(100*time.Millisecond), "bytes", sent,
					"error", fetchError(rerr))
			}
			return
		}
	}
}

// getFile requests a film or an episode from its source for s, with the
// range a player asked for, if any. An answer other than the whole file,
// the part asked for, or a refusal of the range, is a *statusError. Errors
// never quote the address, which can hold credentials. On a source that
// resolves, an address that its source no longer takes is asked for again,
// once, since a portal's link can lapse (D77).
func (h *Hub) getFile(ctx context.Context, s *session, rangeAsked, ifRange string) (*http.Response, error) {
	resp, err := h.fetchFile(ctx, s, s.address(), rangeAsked, ifRange)
	var refused *statusError
	for i, wait := range refusedWaits {
		if !errors.As(err, &refused) || !busyRefusal(refused.code) || s.ref != "" && lapsed(refused.code) {
			break
		}
		if i == 0 {
			h.log.Info("a title's source refused it, as if it had no free connection; the hub asks again in a moment",
				s.what(), s.channel, "source", h.source(s.sourceID).Name, "status", refused.status)
		}
		if sleepContext(ctx, wait) != nil {
			return nil, err
		}
		resp, err = h.fetchFile(ctx, s, s.address(), rangeAsked, ifRange)
	}
	if s.ref == "" || !errors.As(err, &refused) || !lapsed(refused.code) {
		return resp, err
	}
	res, rerr := h.resolve(ctx, s.sourceID, s.refKind, s.ref)
	if rerr != nil {
		if ctx.Err() == nil {
			h.log.Warn("cannot ask a title's source for its address again", s.what(), s.channel,
				"source", h.source(s.sourceID).Name, "error", rerr)
		}
		return nil, err
	}
	h.log.Info("a title's source no longer took its address, and gave another", s.what(), s.channel,
		"source", h.source(s.sourceID).Name, "status", refused.status)
	s.readdress.Store(&res.Address)
	return h.fetchFile(ctx, s, res.Address, rangeAsked, ifRange)
}

// lapsed reports whether an answer's status says that the address it was
// asked at no longer plays, as when a portal's link lapses.
func lapsed(code int) bool {
	return code == http.StatusUnauthorized || code == http.StatusForbidden || code == http.StatusNotFound ||
		code == http.StatusGone
}

// fetchFile requests a film or an episode for s at address, as getFile
// says.
func (h *Hub) fetchFile(ctx context.Context, s *session, address, rangeAsked, ifRange string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, fetchError(err)
	}
	ua := h.source(s.sourceID).UserAgent
	if ua == "" {
		ua = h.userAgent
	}
	req.Header.Set("User-Agent", ua)
	if rangeAsked != "" {
		req.Header.Set("Range", rangeAsked)
		if ifRange != "" && len(ifRange) <= 200 && !strings.ContainsAny(ifRange, "\r\n") {
			req.Header.Set("If-Range", ifRange)
		}
	}
	resp, err := h.clientFor(s).Do(req)
	if err != nil {
		return nil, fetchError(err)
	}
	switch resp.StatusCode {
	case http.StatusOK, http.StatusPartialContent, http.StatusRequestedRangeNotSatisfiable:
		return resp, nil
	}
	resp.Body.Close()
	return nil, &statusError{code: resp.StatusCode, status: resp.Status}
}

// serveVideo serves a player's request for a video in a folder, as part of
// s, ranges and all. The video is opened through the folder, so its path
// cannot lead outside it, and the request ends with s's turn.
func (h *Hub) serveVideo(ctx context.Context, w http.ResponseWriter, r *http.Request, s *session) {
	src := h.source(s.sourceID).Name
	f, err := folder.OpenVideo(s.folder, s.file)
	if errors.Is(err, folder.ErrNoTitle) {
		h.log.Warn("a title's file is gone from its folder", s.what(), s.channel, "source", src)
		http.Error(w, "the title's file is gone", http.StatusNotFound)
		return
	}
	if err != nil {
		h.log.Warn("cannot open a title's file", s.what(), s.channel, "source", src, "error", err)
		http.Error(w, "the title's file cannot be read", http.StatusBadGateway)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "the title's file cannot be read", http.StatusBadGateway)
		return
	}
	h.log.Debug("a player asked for a file", s.what(), s.channel, "source", src, "device", s.deviceID,
		"range", r.Header.Get("Range"))
	w.Header().Set("Content-Type", fileType(s.file))
	http.ServeContent(&videoWriter{ResponseWriter: w, ctx: ctx, s: s}, r, "", info.ModTime(), f)
}

// fileType returns the content type of a video by its extension.
func fileType(name string) string {
	if t := mime.TypeByExtension(path.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

// videoWriter counts what a video's answer sends, and stops it once its
// turn is over.
type videoWriter struct {
	http.ResponseWriter
	ctx context.Context
	s   *session
}

func (v *videoWriter) Write(p []byte) (int, error) {
	if err := v.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := v.ResponseWriter.Write(p)
	v.s.bytes.Add(int64(n))
	return n, err
}
