// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/config"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tsinfo/tstest"
)

// secret stands for the credentials in a source's addresses.
const secret = "s3cret"

func TestDebugChannelCommand(t *testing.T) {
	// A fictional source whose stream goes back five seconds and sends them
	// again, then ends; its address redirects with a token.
	m := tstest.New()
	m.Pictures(0, 250, "")
	m.Pictures(125, 300, "")
	stream := m.Bytes()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/play/1":
			http.Redirect(w, r, "/stream/1.ts?token=abc", http.StatusFound)
		case "/stream/1.ts":
			w.Header().Set("Content-Type", "video/mp2t")
			w.Write(stream)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()

	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// The key file that the server would make.
	keyPath := filepath.Join(dataDir, config.KeyFileName)
	key, _, err := secrets.LoadKey(keyPath, true)
	if err != nil {
		t.Fatal(err)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(box)
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	channels := []store.CatalogChannel{
		{IdentityKey: "a", Name: "Example", StreamURL: up.URL + "/play/1?u=user&p=" + secret},
		{IdentityKey: "b", Name: "Gone", StreamURL: up.URL + "/missing?p=" + secret},
	}
	if _, err := st.ApplyCatalog(ctx, srcID, channels, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	path := configFile(t, "data_dir: \""+dataDir+"\"\n")
	run1 := func(args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := run(ctx, append([]string{"-config", path, "debug", "channel"}, args...), nil, &stdout, &stderr, noEnv)
		return code, stdout.String(), stderr.String()
	}

	code, out, errOut := run1("-for", "5s", "1")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	for _, want := range []string{
		"Channel 1: Example, from main\n",
		"The channel's address has no extension, so a box gets a .ts link and plays it with ffrt.",
		"1. 302 Found: on to the same host, to an address ending in .ts (",
		"2. 200 OK (video/mp2t",
		"It sends MPEG-TS.",
		"What a player gets through Overscan, over 5.0s",
		"the stream ended after",
		"Streams: H.264 video (PID 256); AAC audio (PID 257, eng).",
		// The hub leaves the repeat out, and says so.
		"The clock, from the H.264 video: 12.0s of new pictures.",
		"It ran on smoothly, without jumps.",
		"No packets missing, scrambled or out of step.",
		`msg="the source sent a stretch again; the hub left it out" channel=Example source=main back=5s bytes=`,
		`msg="the source ended a stream" channel=Example source=main`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("no %q in\n%s", want, out)
		}
	}
	if strings.Contains(out+errOut, secret) || strings.Contains(out+errOut, "abc") || strings.Contains(out+errOut, up.Listener.Addr().String()) {
		t.Errorf("the report gives away an address:\n%s%s", errOut, out)
	}

	// The number may come first, and a source that refuses is reported.
	code, out, errOut = run1("2", "-for", "1s")
	if code != 0 || !strings.Contains(out, "1. 404 Not Found") || !strings.Contains(out, "Overscan answered 502: the source answered 404 Not Found") {
		t.Errorf("exit %d: %s\n%s", code, errOut, out)
	}

	// Repair on for the check, with no ffmpeg to run, and off.
	noFFmpeg := configFile(t, "data_dir: \""+dataDir+"\"\nffmpeg: /nonexistent/ffmpeg\n")
	var stdout, stderr bytes.Buffer
	code = run(ctx, []string{"-config", noFFmpeg, "debug", "channel", "-for", "1s", "-repair", "on", "1"}, nil, &stdout, &stderr, noEnv)
	if out := stdout.String(); code != 0 || !strings.Contains(out, "Repair on, but there is no ffmpeg to run, so the stream goes as it is.") ||
		!strings.Contains(out, `msg="cannot run ffmpeg, so channels set to be repaired play as they are`) {
		t.Errorf("repair on: exit %d: %s\n%s", code, stderr.String(), out)
	}
	if code, out, _ := run1("-for", "1s", "-repair", "off", "1"); code != 0 || strings.Contains(out, "Repair") {
		t.Errorf("repair off: exit %d\n%s", code, out)
	}

	for _, args := range [][]string{{}, {"x"}, {"1", "2"}, {"-for", "1h", "1"}, {"-for", "0s", "1"}, {"-bad", "1"}, {"-repair", "maybe", "1"}} {
		if code, _, _ := run1(args...); code != 2 {
			t.Errorf("%q: exit %d", args, code)
		}
	}
	if code, _, errOut := run1("9"); code != 1 || !strings.Contains(errOut, "no channel 9 in the lineup") {
		t.Errorf("an unknown number: exit %d, %q", code, errOut)
	}

	// Without the key file, the stream address cannot be read.
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := run1("1"); code != 1 || !strings.Contains(errOut, "there is no key file at "+keyPath) {
		t.Errorf("no key file: exit %d, %q", code, errOut)
	}
	if _, err := os.Stat(keyPath); err == nil {
		t.Error("the command made a key file; only the server does")
	}
}
