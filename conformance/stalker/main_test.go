// SPDX-License-Identifier: Apache-2.0

// Package stalker_test checks a server against the Stalker portal API as
// clients see it, following the protocol notes
// (docs/protocol/stalker-protocol-notes.md, section 10). It is black-box: it
// speaks HTTP, as a client does, and uses the server's command line only to
// approve and block devices.
//
// It builds Overscan from this repository and runs it with a fictional
// lineup of three channels in two genres (testdata/lineup.m3u), which a fake
// provider streams, with a guide for two of them and an archive of one, and
// a fictional library of films and a series in a folder.
package stalker_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// srv is the server under test.
var srv *server

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "conformance:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "overscan-conformance-")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/guide.xml" {
			guide(w)
			return
		}
		stream(w, r)
	}))
	defer provider.Close()

	s, err := start(dir, provider.URL)
	if err != nil {
		return 0, err
	}
	defer s.stop()
	srv = s
	code := m.Run()
	if code != 0 {
		fmt.Fprintf(os.Stderr, "server log:\n%s", s.log.String())
	}
	return code, nil
}

// archiveAsked holds the start times, in Unix seconds, that the fake
// provider was asked for its archive from.
var archiveAsked struct {
	sync.Mutex
	starts []string
}

// stream is the fake provider: an endless MPEG-TS stream of empty packets,
// for a channel's live stream and for its archive, which a request asks
// for with utc and lutc.
func stream(w http.ResponseWriter, r *http.Request) {
	if utc := r.URL.Query().Get("utc"); utc != "" {
		archiveAsked.Lock()
		archiveAsked.starts = append(archiveAsked.starts, utc)
		archiveAsked.Unlock()
	}
	w.Header().Set("Content-Type", "video/mp2t")
	packet := append([]byte{0x47}, bytes.Repeat([]byte{0xff}, 187)...)
	rc := http.NewResponseController(w)
	for {
		if _, err := w.Write(packet); err != nil {
			return
		}
		_ = rc.Flush()
		select {
		case <-r.Context().Done():
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// guideStart is the start of the fixture guide: two hours before the
// suite started, on the hour.
var guideStart = time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour)

// guide is the fake provider's XMLTV guide: hour-long programmes on Example
// News, News 0 to News 29, and two-hour ones on Example Sports, Sports 0
// to Sports 14, from guideStart. Example Weather has none.
func guide(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/xml")
	const layout = "20060102150405 -0700"
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<tv>\n")
	b.WriteString(`<channel id="news.example"><display-name>Example News</display-name></channel>` + "\n")
	b.WriteString(`<channel id="sports.example"><display-name>Example Sports</display-name></channel>` + "\n")
	for _, ch := range []struct {
		id, title string
		length    time.Duration
		count     int
	}{{"news.example", "News", time.Hour, 30}, {"sports.example", "Sports", 2 * time.Hour, 15}} {
		for i := range ch.count {
			start := guideStart.Add(time.Duration(i) * ch.length)
			fmt.Fprintf(&b, `<programme start="%s" stop="%s" channel="%s"><title>%s %d</title><desc>A fictional programme.</desc></programme>`+"\n",
				start.Format(layout), start.Add(ch.length).Format(layout), ch.id, ch.title, i)
		}
	}
	b.WriteString("</tv>\n")
	_, _ = w.Write([]byte(b.String()))
}

// server is an Overscan process.
type server struct {
	bin    string
	config string
	base   string // http://host:port
	cmd    *exec.Cmd
	exited chan struct{}
	log    *syncBuffer
}

// start builds Overscan into dir and runs it there, with the fixture lineup
// streamed from provider and the fixture library, and waits until it has
// read the lineup.
func start(dir, provider string) (*server, error) {
	s := &server{bin: filepath.Join(dir, "overscan"), config: filepath.Join(dir, "overscan.yaml"), log: &syncBuffer{}}
	build := exec.Command(goTool(), "build", "-o", s.bin, "overscan.gureet.ca/cmd/overscan")
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return nil, fmt.Errorf("build Overscan: %w", err)
	}

	lineup, err := os.ReadFile(filepath.Join("testdata", "lineup.m3u"))
	if err != nil {
		return nil, err
	}
	lineupPath := filepath.Join(dir, "lineup.m3u")
	if err := os.WriteFile(lineupPath, bytes.ReplaceAll(lineup, []byte("PROVIDER"), []byte(provider)), 0o600); err != nil {
		return nil, err
	}
	library, err := writeLibrary(dir)
	if err != nil {
		return nil, err
	}
	addr, err := freeAddress()
	if err != nil {
		return nil, err
	}
	s.base = "http://" + addr
	config := fmt.Sprintf("listen: %q\ndata_dir: %q\nsources:\n  - name: fixture\n    type: m3u\n    url: %q\n    max_connections: 1\n"+
		"  - name: films\n    type: folder\n    url: %q\n",
		addr, filepath.Join(dir, "data"), lineupPath, library)
	if err := os.WriteFile(s.config, []byte(config), 0o600); err != nil {
		return nil, err
	}

	s.cmd = exec.Command(s.bin, "-config", s.config)
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	if err := s.cmd.Start(); err != nil {
		return nil, err
	}
	s.exited = make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(s.exited)
	}()
	deadline := time.After(30 * time.Second)
	// Overscan reads its sources before it listens: wait for both.
	ready := func() bool {
		log := s.log.String()
		return strings.Contains(log, "source refreshed") && strings.Contains(log, "guide read") && strings.Contains(log, "msg=listening")
	}
	for !ready() {
		select {
		case <-s.exited:
			return nil, fmt.Errorf("Overscan exited:\n%s", s.log.String())
		case <-deadline:
			s.stop()
			return nil, fmt.Errorf("Overscan did not read the lineup and its guide, and listen, in time:\n%s", s.log.String())
		case <-time.After(50 * time.Millisecond):
		}
	}
	return s, nil
}

// stop stops the server, gracefully if it can.
func (s *server) stop() {
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case <-s.exited:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
	}
}

// devices runs "overscan devices" with args, as the administrator does.
func (s *server) devices(t *testing.T, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, s.bin, append([]string{"-config", s.config, "devices"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("overscan devices %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// goTool returns the go command that is running the tests.
func goTool() string {
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	return filepath.Join(os.Getenv("GOROOT"), "bin", "go")
}

// freeAddress returns a local address with a port that is free now.
func freeAddress() (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := ln.Addr().String()
	return addr, ln.Close()
}

// syncBuffer is a buffer that the server's output and the tests share.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

var errNoJS = errors.New(`the answer has no "js" member`)
