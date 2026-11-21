// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/config"
)

// serveWith serves h as the server does, with its limits, on a new
// address, until the test ends.
func serveWith(t *testing.T, h http.Handler) string {
	t.Helper()
	s := newServer(t, config.Default())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, ln, h, "listening") }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return ln.Addr().String()
}

// A body that does not arrive within bodyTimeout fails, so a slow sender
// cannot hold a connection; an answer may take longer, once the body is in.
func TestBodiesHaveADeadline(t *testing.T) {
	old := bodyTimeout
	bodyTimeout = 300 * time.Millisecond
	t.Cleanup(func() { bodyTimeout = old })

	got := make(chan error, 2)
	addr := serveWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := io.ReadAll(r.Body)
		if err == nil && r.URL.Path == "/slow-answer" {
			time.Sleep(2 * bodyTimeout) // longer than the body's deadline
			err = r.Context().Err()
		}
		got <- err
		if err == nil {
			_, _ = io.WriteString(w, "done")
		}
	}))

	// A body that stops half way.
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	fmt.Fprintf(conn, "POST /trickle HTTP/1.1\r\nHost: x\r\nContent-Length: 100\r\n\r\nhalf of it")
	select {
	case err := <-got:
		if err == nil {
			t.Error("a body that stopped half way was read whole")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a body that stopped half way held the request for 5 seconds")
	}

	// A whole body, then an answer that takes longer than the deadline.
	resp, err := http.Post("http://"+addr+"/slow-answer", "text/plain", strings.NewReader("all of it"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err := <-got; err != nil || string(body) != "done" {
		t.Errorf("a slow answer after a whole body: %v, %q", err, body)
	}
}

// Headers of more than maxHeaderBytes are refused.
func TestLargeHeadersAreRefused(t *testing.T) {
	addr := serveWith(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-Large: %s\r\n\r\n", strings.Repeat("a", 2*maxHeaderBytes))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestHeaderFieldsTooLarge {
		t.Errorf("status %d, want 431", resp.StatusCode)
	}
}
