// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
	"overscan.gureet.ca/internal/source"
)

func TestPool(t *testing.T) {
	pool := NewPool(http.DefaultClient)
	cfg := Config{Portal: "http://portal.example/c/", MAC: stalkertest.MAC}
	a, err := pool.Client(1, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := pool.Client(1, cfg); b != a {
		t.Error("the same source and config gave another client")
	}
	if b, _ := pool.Client(2, cfg); b == a {
		t.Error("another source shares the client")
	}
	changed := cfg
	changed.Serial = "SN0001"
	b, _ := pool.Client(1, changed)
	if b == a {
		t.Error("a changed config kept the client")
	}
	if c, _ := pool.Client(1, changed); c != b {
		t.Error("the new client was not kept")
	}
	if _, err := pool.Client(3, Config{Portal: "http://portal.example/c/", MAC: "not a MAC"}); err == nil {
		t.Error("a bad MAC gave a client")
	}
	pool.Forget(func(id int64) bool { return id != 1 })
	if c, _ := pool.Client(1, changed); c == b {
		t.Error("a forgotten source kept its client")
	}
}

func TestFromSettings(t *testing.T) {
	s := source.Settings{Type: source.TypeStalker, URL: "http://portal.example/c/", Username: "u", Password: "p",
		UserAgent: "UA", Device: source.Device{MAC: stalkertest.MAC, Serial: "SN", DeviceID: "D1", DeviceID2: "D2",
			Signature: "SIG", Model: "MAG254"}}
	got := FromSettings(s)
	want := Config{Portal: s.URL, MAC: stalkertest.MAC, Serial: "SN", DeviceID: "D1", DeviceID2: "D2", Signature: "SIG",
		Model: "MAG254", Login: "u", Password: "p", UserAgent: "UA", TimeZone: localZone()}
	if got != want {
		t.Errorf("config %+v, want %+v", got, want)
	}
}

func TestHold(t *testing.T) {
	p := stalkertest.New(t)
	c := newClient(t, p, "/stalker_portal/c/")
	c.watchEvery = 10 * time.Millisecond
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
		}
	}
	if c.Held() {
		t.Fatal("a new client is held")
	}
	first := c.Hold(nil)
	second := c.Hold(nil)
	waitFor("no keep-alive while held", func() bool { return p.Calls("watchdog/get_events") >= 2 })
	first()
	first() // twice is harmless
	if !c.Held() {
		t.Fatal("letting go of one hold let go of both")
	}
	before := p.Calls("watchdog/get_events")
	waitFor("the keep-alive stopped while one hold was left", func() bool { return p.Calls("watchdog/get_events") > before })
	second()
	if c.Held() {
		t.Fatal("still held")
	}
	time.Sleep(30 * time.Millisecond) // a keep-alive that had started ends
	stopped := p.Calls("watchdog/get_events")
	time.Sleep(50 * time.Millisecond)
	if n := p.Calls("watchdog/get_events"); n != stopped {
		t.Errorf("%d keep-alives after the last hold was let go", n-stopped)
	}

	// Failures are heard of.
	var mu sync.Mutex
	var failures []error
	blocked := stalkertest.New(t)
	blocked.Blocked = true
	c = newClient(t, blocked, "/stalker_portal/c/")
	c.watchEvery = 10 * time.Millisecond
	release := c.Hold(func(err error) {
		mu.Lock()
		failures = append(failures, err)
		mu.Unlock()
	})
	defer release()
	waitFor("a failed keep-alive was not heard of", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(failures) > 0
	})
	mu.Lock()
	defer mu.Unlock()
	if !errors.Is(failures[0], ErrBlocked) {
		t.Errorf("failure %v, want ErrBlocked", failures[0])
	}
}

// A guide that takes a while to come holds up neither a link nor the
// keep-alive: the client's calls share the session.
func TestCallsShareTheSession(t *testing.T) {
	p := stalkertest.New(t)
	p.GuideWait = make(chan struct{})
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	if _, err := c.SignIn(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.Guide(ctx, 3, func(string, Programme) error { return nil })
		done <- err
	}()
	for p.Calls("itv/get_epg_info") == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := c.ChannelLink(lctx, "ffrt http://localhost/ch/101_"); err != nil {
		t.Errorf("link while the guide comes: %v", err)
	}
	if _, err := c.Watchdog(lctx); err != nil {
		t.Errorf("keep-alive while the guide comes: %v", err)
	}
	close(p.GuideWait)
	if err := <-done; err != nil {
		t.Errorf("guide: %v", err)
	}
	if n := p.Calls("stb/handshake"); n != 1 {
		t.Errorf("%d handshakes, want 1", n)
	}
}

// Calls that the portal refuses at once sign in again once between them.
func TestRefusedCallsSignInOnce(t *testing.T) {
	p := stalkertest.New(t)
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	if _, err := c.SignIn(ctx); err != nil {
		t.Fatal(err)
	}
	p.ForgetToken()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Genres(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := p.Calls("stb/handshake"); n != 2 {
		t.Errorf("%d handshakes, want 2", n)
	}
}

// A call waits for a sign-in that another call makes only as long as its
// context lets it.
func TestWaitingForASignIn(t *testing.T) {
	stall := make(chan struct{})
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-stall:
		case <-r.Context().Done():
		}
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(slow)
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stall) })
	c, err := New(Config{Portal: srv.URL + "/c/", MAC: stalkertest.MAC}, http.DefaultClient)
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = c.Genres(context.Background()) }() // signs in, and waits
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.Genres(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error %v", err)
	}
	if waited := time.Since(start); waited > 2*time.Second {
		t.Errorf("waited %v", waited)
	}
}
