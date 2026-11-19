// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Inputs: how ffmpeg and ffprobe read the file of a film or an episode
// (D68). The hub serves the file to them at an address of its own on the
// loopback interface, made for one turn of a session and gone with it. It
// passes their range requests on to the source, as it does a player's, so
// they can seek in the file; the source's address, which can hold
// credentials, never reaches them, and they count against the source's
// connections as the session's own requests. A file in a folder is read
// through the folder (D67).

// inputServer serves the inputs, from the first time one is opened.
type inputServer struct {
	once sync.Once
	ln   net.Listener
	srv  *http.Server
	err  error

	mu   sync.Mutex
	open map[string]*input // by secret
}

// input is a session's file, served to ffmpeg or ffprobe within a turn.
// It makes one request to the source at a time: ffmpeg, when it seeks,
// opens its next request before it closes the one before, which the hub
// then ends.
type input struct {
	ctx context.Context // the turn's
	s   *session

	mu  sync.Mutex
	cur *fileTurn // its request to the source now, nil if none
}

// openInput returns the address at which ffmpeg or ffprobe reads s's file
// while ctx lasts, and a function that takes it away.
func (h *Hub) openInput(ctx context.Context, s *session) (string, func(), error) {
	in := &h.inputs
	in.once.Do(func() {
		in.open = map[string]*input{}
		in.ln, in.err = net.Listen("tcp", "127.0.0.1:0")
		if in.err != nil {
			return
		}
		in.srv = &http.Server{Handler: http.HandlerFunc(h.serveInput), ReadHeaderTimeout: 10 * time.Second}
		go func() { _ = in.srv.Serve(in.ln) }()
	})
	if in.err != nil {
		return "", nil, in.err
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	secret := hex.EncodeToString(b)
	in.mu.Lock()
	in.open[secret] = &input{ctx: ctx, s: s}
	in.mu.Unlock()
	return "http://" + in.ln.Addr().String() + "/in/" + secret, func() {
		in.mu.Lock()
		delete(in.open, secret)
		in.mu.Unlock()
	}, nil
}

// closeInputs stops serving inputs, when the hub stops.
func (h *Hub) closeInputs() {
	in := &h.inputs
	in.once.Do(func() { in.err = errors.New("the hub has stopped") })
	if in.srv != nil {
		_ = in.srv.Close()
	}
}

// serveInput serves /in/<secret>: the file of the input with that secret,
// ranges and all, to the program on this machine that reads it.
func (h *Hub) serveInput(w http.ResponseWriter, r *http.Request) {
	secret, ok := strings.CutPrefix(r.URL.Path, "/in/")
	h.inputs.mu.Lock()
	in := h.inputs.open[secret]
	h.inputs.mu.Unlock()
	if !ok || in == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	turn := newTurn(cancel, w)
	defer context.AfterFunc(in.ctx, turn.end)()
	in.mu.Lock()
	before := in.cur
	in.cur = turn
	in.mu.Unlock()
	defer func() {
		in.mu.Lock()
		if in.cur == turn {
			in.cur = nil
		}
		in.mu.Unlock()
		turn.finish()
	}()
	if before != nil {
		// ffmpeg keeps its request before open until this one answers,
		// and no longer reads it: end it, writes and all.
		before.end()
		select {
		case <-before.done:
		case <-ctx.Done():
			return
		}
	}
	s := in.s
	if s.folder != "" {
		h.serveVideo(ctx, w, r, s)
		return
	}
	asked := r.Header.Get("Range")
	if asked != "" && (len(asked) > 200 || !rangeHeader.MatchString(asked)) {
		asked = ""
	}
	resp, idled, err := h.openFile(ctx, s, asked, r.Header.Get("If-Range"))
	if err != nil {
		var refused *statusError
		switch {
		case ctx.Err() != nil:
		case errors.As(err, &refused):
			h.log.Warn("a title's source refused it", "title", s.channel, "source", h.source(s.sourceID).Name,
				"status", refused.status)
		default:
			h.log.Warn("cannot reach a title", "title", s.channel, "source", h.source(s.sourceID).Name,
				"error", err)
		}
		http.Error(w, "the source cannot be read", http.StatusBadGateway)
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
	if r.Method == http.MethodHead {
		return
	}
	buf := make([]byte, copyBufferBytes)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			s.bytes.Add(int64(n))
		}
		if rerr != nil {
			if idled() {
				h.log.Info("a title's source sent nothing for a while, so the hub ended ffmpeg's request",
					"title", s.channel, "source", h.source(s.sourceID).Name, "silent_for", fileIdleTimeout)
			}
			return
		}
	}
}
