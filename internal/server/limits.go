// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"errors"
	"io"
	"net/http"
	"time"
)

// bodyDeadline gives the body of a request bodyTimeout to arrive in, and
// then lifts the deadline, so that a slow sender cannot hold a connection
// open by trickling a body, while the answer takes as long as it needs: a
// deadline left in place would also end the request once it passed.
func bodyDeadline(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
			h.ServeHTTP(w, r)
			return
		}
		rc := http.NewResponseController(w)
		if err := rc.SetReadDeadline(time.Now().Add(bodyTimeout)); err != nil {
			// A writer that cannot take deadlines, as in tests.
			h.ServeHTTP(w, r)
			return
		}
		r.Body = &timedBody{ReadCloser: r.Body, rc: rc}
		h.ServeHTTP(w, r)
	})
}

// timedBody lifts the read deadline once the body has been read.
type timedBody struct {
	io.ReadCloser
	rc   *http.ResponseController
	done bool
}

func (b *timedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && !b.done {
		b.done = true
		if errors.Is(err, io.EOF) {
			_ = b.rc.SetReadDeadline(time.Time{})
		}
	}
	return n, err
}
