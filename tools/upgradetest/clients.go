// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// An instance is an Overscan binary serving a data directory.
type instance struct {
	bin, config string
	base        string // http://127.0.0.1:port
	cmd         *exec.Cmd
	log         *os.File
	started     time.Time
}

// freePort returns a port on the loopback address that nothing listens on
// now.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

// start runs bin with config, logging to logPath, and waits until it
// answers /healthz.
func start(ctx context.Context, bin, config, base, logPath string) (*instance, error) {
	log, err := os.OpenFile(logPath, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	i := &instance{bin: bin, config: config, base: base, log: log, started: time.Now().Add(-time.Second)}
	i.cmd = exec.Command(bin, "-config", config)
	i.cmd.Stdout, i.cmd.Stderr = log, log
	if err := i.cmd.Start(); err != nil {
		_ = log.Close()
		return nil, err
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(base + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return i, nil
			}
		}
		if time.Now().After(deadline) {
			i.stop()
			return nil, fmt.Errorf("%s did not answer /healthz within a minute; its log is %s", bin, logPath)
		}
		select {
		case <-ctx.Done():
			i.stop()
			return nil, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// stop ends the instance as systemd would, and waits for it.
func (i *instance) stop() {
	if i.cmd.Process != nil {
		_ = i.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = i.cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			_ = i.cmd.Process.Kill()
			<-done
		}
	}
	_ = i.log.Close()
}

// command runs one of bin's commands with config, giving it stdin, and
// returns what it printed.
func command(bin, config, stdin string, args ...string) (string, error) {
	cmd := exec.Command(bin, append([]string{"-config", config}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s %s: %w: %s", bin, strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

// statusError is an answer that was not what a call expected.
type statusError struct {
	call   string
	status int
	body   string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("%s: status %d: %.400s", e.call, e.status, e.body)
}

// do sends a request with a JSON body, if body is not nil, and decodes a
// JSON answer into out, if out is not nil. An answer outside 2xx is a
// statusError.
func do(c *http.Client, req *http.Request, body, out any) error {
	call := req.Method + " " + req.URL.Path
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.Body = io.NopCloser(bytes.NewReader(data))
		req.ContentLength = int64(len(data))
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %w", call, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("%s: %w", call, err)
	}
	if resp.StatusCode/100 != 2 {
		return &statusError{call: call, status: resp.StatusCode, body: string(data)}
	}
	if out == nil {
		return nil
	}
	if raw, ok := out.(*[]byte); ok {
		*raw = data
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("%s: %w: %.200s", call, err, data)
	}
	return nil
}

// admin is a signed-in session on the admin's API.
type admin struct {
	base string
	http *http.Client
	csrf string
}

func signIn(base, username, password string) (*admin, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	a := &admin{base: base, http: &http.Client{Jar: jar, Timeout: 60 * time.Second}}
	var s struct {
		CSRF string `json:"csrf_token"`
	}
	if err := a.call("POST", "session", map[string]string{"username": username, "password": password}, &s); err != nil {
		return nil, fmt.Errorf("signing in to the admin: %w", err)
	}
	a.csrf = s.CSRF
	return a, nil
}

func (a *admin) call(method, path string, body, out any) error {
	req, err := http.NewRequest(method, a.base+"/api/admin/v1/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", a.base)
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	return do(a.http, req, body, out)
}

// box is a box running Overscan's page, through the device API.
type box struct {
	base, mac, secret, token string
	http                     *http.Client
}

func newBox(base, mac string) *box {
	return &box{base: base, mac: mac, http: &http.Client{Timeout: 60 * time.Second}}
}

// session starts a session as the page does, keeping the secret that the
// server gives, and the token once the box is approved. It returns the
// answer.
func (b *box) session() (map[string]any, error) {
	req, err := http.NewRequest("POST", b.base+"/api/device/v1/session", nil)
	if err != nil {
		return nil, err
	}
	var s map[string]any
	if err := do(b.http, req, map[string]string{"mac": b.mac, "model": "MAG322", "secret": b.secret}, &s); err != nil {
		return nil, err
	}
	if secret, ok := s["secret"].(string); ok && secret != "" {
		b.secret = secret
	}
	b.token, _ = s["token"].(string)
	return s, nil
}

func (b *box) call(method, path string, body, out any) error {
	req, err := http.NewRequest(method, b.base+"/api/device/v1/"+path, nil)
	if err != nil {
		return err
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return do(b.http, req, body, out)
}

// stalkerClient is a Stalker client, as an app that speaks the Stalker
// portal protocol would be.
type stalkerClient struct {
	base, mac, token string
	http             *http.Client
}

func newStalker(base, mac string) *stalkerClient {
	return &stalkerClient{base: base, mac: mac, http: &http.Client{Timeout: 60 * time.Second}}
}

// errPending is a Stalker client's handshake while it waits for approval.
var errPending = errors.New("the device waits for approval")

func (s *stalkerClient) handshake() error {
	s.token = ""
	var hs struct {
		Token string `json:"token"`
	}
	if err := s.call("stb", "handshake", url.Values{"token": {""}, "prehash": {"0"}}, &hs); err != nil {
		var se *statusError
		if errors.As(err, &se) && se.status == http.StatusUnauthorized {
			return errPending
		}
		return err
	}
	s.token = hs.Token
	return s.call("stb", "get_profile", url.Values{"sn": {"000000000012"}, "stb_type": {"MAG250"}, "hd": {"1"}}, nil)
}

func (s *stalkerClient) call(module, action string, params url.Values, out any) error {
	q := url.Values{"type": {module}, "action": {action}, "JsHttpRequest": {"1-xml"}}
	for k, v := range params {
		q[k] = v
	}
	req, err := http.NewRequest("GET", s.base+"/stalker_portal/server/load.php?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (QtEmbedded; U; Linux; C) AppleWebKit/533.3 (KHTML, like Gecko) MAG250 stbapp ver: 2 rev: 250 Safari/533.3")
	req.Header.Set("Cookie", "mac="+url.QueryEscape(s.mac)+"; stb_lang=en; timezone=UTC")
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	var answer struct {
		JS json.RawMessage `json:"js"`
	}
	if err := do(s.http, req, nil, &answer); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(answer.JS))
	dec.UseNumber()
	return dec.Decode(out)
}

// get fetches an address of the outputs, which players read, as JSON into
// out, or as text into a *[]byte.
func get(base, path string, out any) error {
	req, err := http.NewRequest("GET", base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "FictionalPlayer/1.0")
	return do(&http.Client{Timeout: 60 * time.Second}, req, nil, out)
}
