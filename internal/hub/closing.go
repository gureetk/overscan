// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync"
	"sync/atomic"
	"time"
)

// A source's limit counts its streams as its provider does: by the
// connections that carry them (D36). A stream that ends, such as one that
// a device's new stream replaces, closes its connection to the source, but
// the transport closes it a moment after the session ends, so the next
// stream's request could reach the provider first, which would then see
// one connection more than the limit. So each session counts its answers
// from the source that are open, an answer cut short closes its connection
// as it closes, and a request to a source first waits, for closeWait at
// most, until no ended session's answer from that source is open.

// closeWait is how long a request to a source waits at most for the
// answers of ended sessions from that source to close.
const closeWait = 2 * time.Second

// upstreamBody is an answer from a source, open until it is closed.
type upstreamBody struct {
	io.ReadCloser
	s    *session
	conn net.Conn // the connection that carries it, if it carries nothing else
	once sync.Once
	eof  atomic.Bool
}

func (b *upstreamBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err == io.EOF {
		b.eof.Store(true)
	}
	return n, err
}

// Close closes the answer; one cut short also closes its connection there
// and then, so that the source sees the stream end before anything else
// is asked of it.
func (b *upstreamBody) Close() error {
	var err error
	b.once.Do(func() {
		if !b.eof.Load() && b.conn != nil {
			b.conn.Close()
		}
		err = b.ReadCloser.Close()
		b.s.upstream.Add(-1)
	})
	return err
}

// traceConn adds to ctx a trace that keeps the connection that a request
// gets in *conn.
func traceConn(ctx context.Context, conn *net.Conn) context.Context {
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) { *conn = info.Conn },
	})
}

// opened counts an answer from the source as open for s until it is
// closed, and returns it.
func opened(s *session, resp *http.Response, conn net.Conn) *http.Response {
	if resp.ProtoMajor != 1 {
		conn = nil // a connection that carries other streams too stays
	}
	resp.Body = &upstreamBody{ReadCloser: resp.Body, s: s, conn: conn}
	return resp
}

// waitClosed waits until no ended session's answer from s's source is open,
// for closeWait at most, or until ctx is done.
func (h *Hub) waitClosed(ctx context.Context, s *session) {
	deadline := h.now().Add(closeWait)
	for {
		if !h.closingOn(s) || !h.now().Before(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// closingOn reports whether an ended session other than s still has an
// answer from s's source open, forgetting those that have none.
func (h *Hub) closingOn(s *session) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	busy := false
	for c := range h.closing {
		switch {
		case c.upstream.Load() <= 0:
			delete(h.closing, c)
		case c != s && c.sourceID == s.sourceID:
			busy = true
		}
	}
	return busy
}
