// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"
)

// slowSource is a source whose streams play until their requests end, and
// then take a while to close, as a connection to a distant server can; it
// notes when each opens and closes.
type slowSource struct {
	mu     sync.Mutex
	events []string
}

func (src *slowSource) note(event string) {
	src.mu.Lock()
	defer src.mu.Unlock()
	src.events = append(src.events, event)
}

func (src *slowSource) RoundTrip(req *http.Request) (*http.Response, error) {
	src.note("open " + req.URL.Path)
	return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", ProtoMajor: 1, ProtoMinor: 1,
		Header:  http.Header{"Content-Type": {"video/mp2t"}},
		Body:    &slowBody{src: src, ctx: req.Context(), path: req.URL.Path},
		Request: req}, nil
}

type slowBody struct {
	src  *slowSource
	ctx  context.Context
	path string
	once sync.Once
}

var packet = append([]byte{0x47, 0x1f, 0xff, 0x10}, bytes.Repeat([]byte{0xff}, 184)...)

func (b *slowBody) Read(p []byte) (int, error) {
	select {
	case <-b.ctx.Done():
		time.Sleep(100 * time.Millisecond)
		return 0, errors.New("the request ended")
	case <-time.After(2 * time.Millisecond):
	}
	return copy(p, packet), nil
}

func (b *slowBody) Close() error {
	b.once.Do(func() { b.src.note("close " + b.path) })
	return nil
}

var _ io.ReadCloser = (*slowBody)(nil)

// A stream that a device's new stream replaces on a source at its limit
// closes its connection before the new stream's request goes out, however
// long the closing takes, so that the source never sees more streams than
// its limit.
func TestReplacedStreamClosesFirst(t *testing.T) {
	h := newHarness(t, 1)
	src := &slowSource{}
	h.hub.client = &http.Client{Transport: src}
	first := h.play(h.link(7, "live/1"))
	defer first.Body.Close()
	second := h.play(h.link(7, "live/2"))
	second.Body.Close()
	h.waitIdle()
	src.mu.Lock()
	events := slices.Clone(src.events)
	src.mu.Unlock()
	if want := []string{"open /live/1", "close /live/1", "open /live/2"}; len(events) < 3 || !slices.Equal(events[:3], want) {
		t.Errorf("the source saw %q, want %q first", events, want)
	}
}
