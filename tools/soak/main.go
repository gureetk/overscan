// SPDX-License-Identifier: AGPL-3.0-or-later

// Command soak runs Overscan under load for a while and records its memory
// and CPU use: the soak test of the plan (section 15). It serves a fake
// provider with a large lineup of endless streams, runs Overscan against it,
// and simulates boxes that sign in, load the channel list, watch channels
// and switch between them, half through the Stalker API and half through
// the device API.
//
//	go run ./tools/soak -duration 10m -boxes 4
//
// It prints a summary, writes the samples to a CSV file, and fails if the
// server breaks the budgets given as flags. It reads the server's use from
// /proc, so it runs on Linux only.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// clockTicks is the kernel's USER_HZ, the unit of the CPU times in
// /proc/<pid>/stat: 100 on every Linux architecture Overscan targets.
const clockTicks = 100

type options struct {
	duration  time.Duration
	boxes     int
	channels  int
	bitrate   int
	zap       time.Duration
	settle    time.Duration
	sample    time.Duration
	bin       string
	out       string
	idleMB    float64
	peakMB    float64
	cpuBudget float64
}

func main() {
	var o options
	flag.DurationVar(&o.duration, "duration", 10*time.Minute, "how long the boxes run")
	flag.IntVar(&o.boxes, "boxes", 4, "how many boxes to simulate, each watching one stream")
	flag.IntVar(&o.channels, "channels", 20000, "how many channels the fake provider lists")
	flag.IntVar(&o.bitrate, "bitrate", 5_000_000, "each stream's rate, in bits per second")
	flag.DurationVar(&o.zap, "zap", 2*time.Minute, "how long a box stays on a channel, on average")
	flag.DurationVar(&o.settle, "settle", 30*time.Second, "how long the server idles with the lineup loaded before the boxes start")
	flag.DurationVar(&o.sample, "sample", 5*time.Second, "how often memory and CPU are sampled")
	flag.StringVar(&o.bin, "bin", "", "the Overscan binary to test (default: build it from this repository)")
	flag.StringVar(&o.out, "out", "soak.csv", "where to write the samples")
	flag.Float64Var(&o.idleMB, "idle-mb", 60, "fail if the idle server's resident memory exceeds this, in MB (0: no limit)")
	flag.Float64Var(&o.peakMB, "peak-mb", 150, "fail if resident memory ever exceeds this, in MB (0: no limit)")
	flag.Float64Var(&o.cpuBudget, "cpu", 0, "fail if average CPU use under load exceeds this percentage of one core (0: no limit)")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, o); err != nil {
		fmt.Fprintln(os.Stderr, "soak:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, o options) error {
	dir, err := os.MkdirTemp("", "overscan-soak-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	bin := o.bin
	if bin == "" {
		bin = filepath.Join(dir, "overscan")
		fmt.Println("building Overscan")
		build := exec.Command("go", "build", "-o", bin, "overscan.gureet.ca/cmd/overscan")
		build.Stdout, build.Stderr = os.Stderr, os.Stderr
		if err := build.Run(); err != nil {
			return fmt.Errorf("build Overscan (run this in the repository, or give -bin): %w", err)
		}
	}

	provider, err := startProvider(o.channels, o.bitrate)
	if err != nil {
		return err
	}
	defer provider.Close()

	srv, err := startServer(ctx, dir, bin, provider.URL)
	if err != nil {
		return err
	}
	defer srv.stop()

	macs := make([]string, o.boxes)
	for i := range macs {
		macs[i] = fmt.Sprintf("02:00:00:00:%02X:%02X", (i+1)>>8, (i+1)&0xff) // locally administered: no real card has them
		if err := srv.devices("approve", "-label", fmt.Sprintf("Soak box %d", i+1), macs[i]); err != nil {
			return err
		}
	}

	m := &meter{pid: srv.cmd.Process.Pid}
	if _, err := m.read(); err != nil {
		return fmt.Errorf("reading the server's use from /proc: %w", err)
	}
	fmt.Printf("lineup of %d channels loaded; idling for %s\n", o.channels, o.settle)
	if err := sleep(ctx, o.settle); err != nil {
		return err
	}
	idle, err := m.read()
	if err != nil {
		return err
	}

	fmt.Printf("running %d boxes for %s\n", o.boxes, o.duration)
	var st stats
	runCtx, cancel := context.WithTimeout(ctx, o.duration)
	defer cancel()
	var wg sync.WaitGroup
	for i, mac := range macs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := &box{base: srv.base, mac: mac, zap: o.zap, stats: &st, rng: rand.New(rand.NewPCG(uint64(i), 1))}
			if i%2 == 0 {
				b.runStalker(runCtx)
			} else {
				b.runDevice(runCtx)
			}
		}()
	}

	samples := []sample{idle}
	ticker := time.NewTicker(o.sample)
	for runCtx.Err() == nil {
		select {
		case <-runCtx.Done():
		case <-ticker.C:
			s, err := m.read()
			if err != nil {
				cancel()
				wg.Wait()
				return fmt.Errorf("the server stopped: %w\n%s", err, srv.log.String())
			}
			samples = append(samples, s)
		}
	}
	ticker.Stop()
	wg.Wait()
	end, err := m.read()
	if err != nil {
		return err
	}
	samples = append(samples, end)
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if err := writeCSV(o.out, samples); err != nil {
		return err
	}
	return report(o, idle, samples, &st)
}

// sleep waits for d, or until ctx is done.
func sleep(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// The fake provider.

// startProvider serves a lineup of n channels in 200 groups at
// /playlist.m3u, and each channel as an endless MPEG-TS stream at bitrate.
func startProvider(n, bitrate int) (*httpServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		bw := bufio.NewWriter(w)
		bw.WriteString("#EXTM3U\n")
		for i := 1; i <= n; i++ {
			fmt.Fprintf(bw, "#EXTINF:-1 tvg-id=\"ch%d.example\" group-title=\"Group %d\",Soak channel %d\n", i, i%200, i)
			fmt.Fprintf(bw, "http://%s/live/%d.ts\n", r.Host, i)
		}
		bw.Flush()
	})
	mux.HandleFunc("GET /live/{n}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		const tick = 50 * time.Millisecond
		perTick := bitrate / 8 / int(time.Second/tick) / 188 * 188
		chunk := bytes.Repeat(append([]byte{0x47}, bytes.Repeat([]byte{0xff}, 187)...), max(perTick/188, 1))
		rc := http.NewResponseController(w)
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			if _, err := w.Write(chunk); err != nil {
				return
			}
			_ = rc.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-t.C:
			}
		}
	})
	return listen(mux)
}

type httpServer struct {
	URL string
	srv *http.Server
}

func listen(h http.Handler) (*httpServer, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &httpServer{URL: "http://" + ln.Addr().String(), srv: &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

func (s *httpServer) Close() { _ = s.srv.Close() }

// The server under test.

type server struct {
	bin, config, base string
	cmd               *exec.Cmd
	exited            chan struct{}
	log               *syncBuffer
}

func startServer(ctx context.Context, dir, bin, provider string) (*server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()
	ln.Close()
	s := &server{bin: bin, config: filepath.Join(dir, "overscan.yaml"), base: "http://" + addr, log: &syncBuffer{}}
	config := fmt.Sprintf("listen: %q\ndata_dir: %q\nsources:\n  - name: soak\n    type: m3u\n    url: %q\n    max_connections: 0\n",
		addr, filepath.Join(dir, "data"), provider+"/playlist.m3u")
	if err := os.WriteFile(s.config, []byte(config), 0o600); err != nil {
		return nil, err
	}
	s.cmd = exec.Command(bin, "-config", s.config)
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	if err := s.cmd.Start(); err != nil {
		return nil, err
	}
	s.exited = make(chan struct{})
	go func() {
		_ = s.cmd.Wait()
		close(s.exited)
	}()
	for !strings.Contains(s.log.String(), "source refreshed") {
		select {
		case <-ctx.Done():
			s.stop()
			return nil, ctx.Err()
		case <-s.exited:
			return nil, fmt.Errorf("Overscan exited:\n%s", s.log.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return s, nil
}

func (s *server) stop() {
	_ = s.cmd.Process.Signal(os.Interrupt)
	select {
	case <-s.exited:
	case <-time.After(15 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
	}
}

func (s *server) devices(args ...string) error {
	out, err := exec.Command(s.bin, append([]string{"-config", s.config, "devices"}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("overscan devices %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

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

// Simulated boxes.

type stats struct {
	requests, failedRequests atomic.Int64
	streams, failedStreams   atomic.Int64
	bytes                    atomic.Int64
	listings                 atomic.Int64
}

type box struct {
	base, mac string
	zap       time.Duration
	stats     *stats
	rng       *rand.Rand
	token     string
}

// runStalker acts as a Stalker client: it signs in, loads every channel,
// then watches channels one after another, with the keep-alive call every
// 80 seconds, two thirds of the watchdog timeout.
func (b *box) runStalker(ctx context.Context) {
	var cmds []string
	for ctx.Err() == nil && len(cmds) == 0 {
		var hs struct{ Token string }
		if b.stalker(ctx, &hs, "stb", "handshake", "token", "") != nil {
			b.pause(ctx)
			continue
		}
		b.token = hs.Token
		var all struct {
			Data []struct{ Cmd string }
		}
		if b.stalker(ctx, &struct{ ID string }{}, "stb", "get_profile", "stb_type", "MAG250") != nil ||
			b.stalker(ctx, &all, "itv", "get_all_channels") != nil {
			b.pause(ctx)
			continue
		}
		b.stats.listings.Add(1)
		for _, c := range all.Data {
			cmds = append(cmds, c.Cmd)
		}
	}
	go func() {
		for sleep(ctx, 80*time.Second) == nil {
			_ = b.stalker(ctx, &struct{}{}, "watchdog", "get_events", "init", "0", "cur_play_type", "1", "event_active_id", "0")
		}
	}()
	for ctx.Err() == nil {
		var l struct{ Cmd, Error string }
		if b.stalker(ctx, &l, "itv", "create_link", "cmd", cmds[b.rng.IntN(len(cmds))]) != nil || l.Error != "" {
			b.stats.failedStreams.Add(1)
			b.pause(ctx)
			continue
		}
		fields := strings.Fields(l.Cmd)
		b.watch(ctx, fields[len(fields)-1])
	}
}

// runDevice acts as the MAG page: a session, the channel list, then
// channels one after another.
func (b *box) runDevice(ctx context.Context) {
	var ids []int64
	for ctx.Err() == nil && len(ids) == 0 {
		var s struct{ State, Token string }
		if b.device(ctx, &s, "POST", "session", `{"mac":"`+b.mac+`","model":"soak"}`) != nil || s.State != "approved" {
			b.pause(ctx)
			continue
		}
		b.token = s.Token
		var lineup struct {
			Channels []struct{ ID int64 }
		}
		if b.device(ctx, &lineup, "GET", "channels", "") != nil {
			b.pause(ctx)
			continue
		}
		b.stats.listings.Add(1)
		for _, c := range lineup.Channels {
			ids = append(ids, c.ID)
		}
	}
	for ctx.Err() == nil {
		var p struct{ URL string }
		if b.device(ctx, &p, "POST", fmt.Sprintf("channels/%d/play", ids[b.rng.IntN(len(ids))]), "") != nil {
			b.stats.failedStreams.Add(1)
			b.pause(ctx)
			continue
		}
		b.watch(ctx, p.URL)
	}
}

// watch plays a stream for a while, as long on average as the zap time.
func (b *box) watch(ctx context.Context, link string) {
	d := time.Duration(b.rng.ExpFloat64() * float64(b.zap))
	wctx, cancel := context.WithTimeout(ctx, max(d, 5*time.Second))
	defer cancel()
	req, err := http.NewRequestWithContext(wctx, http.MethodGet, link, nil)
	if err != nil {
		b.stats.failedStreams.Add(1)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if wctx.Err() == nil {
			b.stats.failedStreams.Add(1)
			b.pause(ctx)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b.stats.failedStreams.Add(1)
		b.pause(ctx)
		return
	}
	b.stats.streams.Add(1)
	n, err := io.Copy(io.Discard, resp.Body)
	b.stats.bytes.Add(n)
	if err != nil && wctx.Err() == nil {
		b.stats.failedStreams.Add(1) // the stream broke off by itself
	}
}

// pause waits a second after a failure.
func (b *box) pause(ctx context.Context) { _ = sleep(ctx, time.Second) }

// stalker makes a Stalker call and decodes its "js" member into v.
func (b *box) stalker(ctx context.Context, v any, module, action string, params ...string) error {
	q := url.Values{"type": {module}, "action": {action}, "JsHttpRequest": {"1-xml"}}
	for i := 0; i+1 < len(params); i += 2 {
		q.Set(params[i], params[i+1])
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+"/stalker_portal/server/load.php?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Cookie", "mac="+url.QueryEscape(b.mac)+"; stb_lang=en")
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	var answer struct{ JS json.RawMessage }
	if err := b.do(req, &answer); err != nil {
		return err
	}
	return json.Unmarshal(answer.JS, v)
}

// device makes a device API call and decodes its answer into v.
func (b *box) device(ctx context.Context, v any, method, path, body string) error {
	req, err := http.NewRequestWithContext(ctx, method, b.base+"/api/device/v1/"+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	if b.token != "" {
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return b.do(req, v)
}

func (b *box) do(req *http.Request, v any) error {
	b.stats.requests.Add(1)
	resp, err := http.DefaultClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = fmt.Errorf("%s %s: %s", req.Method, req.URL.Path, resp.Status)
		} else {
			err = json.NewDecoder(resp.Body).Decode(v)
		}
	}
	if err != nil && req.Context().Err() == nil {
		b.stats.failedRequests.Add(1)
	}
	return err
}

// Measuring.

type sample struct {
	at      time.Time
	rssKB   int64
	cpuTick int64 // user and system time, in clock ticks
}

type meter struct{ pid int }

// read samples the server's resident memory and CPU time from /proc.
func (m *meter) read() (sample, error) {
	s := sample{at: time.Now()}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", m.pid))
	if err != nil {
		return s, err
	}
	// The command name, in parentheses, can hold spaces: count fields from
	// after it. utime and stime are fields 14 and 15.
	rest := string(stat[bytes.LastIndexByte(stat, ')')+2:])
	f := strings.Fields(rest)
	if len(f) < 13 {
		return s, errors.New("short /proc stat")
	}
	utime, _ := strconv.ParseInt(f[11], 10, 64)
	stime, _ := strconv.ParseInt(f[12], 10, 64)
	s.cpuTick = utime + stime
	status, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", m.pid))
	if err != nil {
		return s, err
	}
	for line := range strings.Lines(string(status)) {
		if v, ok := strings.CutPrefix(line, "VmRSS:"); ok {
			s.rssKB, _ = strconv.ParseInt(strings.Fields(v)[0], 10, 64)
		}
	}
	return s, nil
}

func writeCSV(path string, samples []sample) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{"seconds", "rss_mb", "cpu_percent"})
	start := samples[0].at
	for i, s := range samples {
		cpu := ""
		if i > 0 {
			cpu = strconv.FormatFloat(cpuPercent(samples[i-1], s), 'f', 2, 64)
		}
		_ = w.Write([]string{strconv.FormatFloat(s.at.Sub(start).Seconds(), 'f', 0, 64), strconv.FormatFloat(mb(s.rssKB), 'f', 1, 64), cpu})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// cpuPercent is the CPU use between two samples, as a percentage of one core.
func cpuPercent(a, b sample) float64 {
	secs := b.at.Sub(a.at).Seconds()
	if secs <= 0 {
		return 0
	}
	return float64(b.cpuTick-a.cpuTick) / clockTicks / secs * 100
}

func mb(kb int64) float64 { return float64(kb) / 1024 }

func report(o options, idle sample, samples []sample, st *stats) error {
	var peak int64
	peakCPU := 0.0
	for i, s := range samples {
		peak = max(peak, s.rssKB)
		if i > 0 {
			peakCPU = max(peakCPU, cpuPercent(samples[i-1], s))
		}
	}
	last := samples[len(samples)-1]
	avgCPU := cpuPercent(samples[0], last)
	fmt.Printf("\nSoak test: %s, %d boxes, %d channels, %.1f Mbit/s a stream, a new channel every %s on average\n",
		o.duration, o.boxes, o.channels, float64(o.bitrate)/1e6, o.zap)
	fmt.Printf("Resident memory: %.1f MB idle with the lineup loaded, %.1f MB at the peak, %.1f MB at the end\n",
		mb(idle.rssKB), mb(peak), mb(last.rssKB))
	fmt.Printf("CPU: %.1f%% of one core on average under load, %.1f%% at the peak over %s\n", avgCPU, peakCPU, o.sample)
	fmt.Printf("Streams: %d played, %d failed, %.2f GB proxied; channel lists loaded: %d\n",
		st.streams.Load(), st.failedStreams.Load(), float64(st.bytes.Load())/1e9, st.listings.Load())
	fmt.Printf("API calls: %d, %d failed\n", st.requests.Load(), st.failedRequests.Load())
	fmt.Printf("Samples: %s\n", o.out)

	var broken []string
	if o.idleMB > 0 && mb(idle.rssKB) > o.idleMB {
		broken = append(broken, fmt.Sprintf("idle memory %.1f MB is over %.0f MB", mb(idle.rssKB), o.idleMB))
	}
	if o.peakMB > 0 && mb(peak) > o.peakMB {
		broken = append(broken, fmt.Sprintf("peak memory %.1f MB is over %.0f MB", mb(peak), o.peakMB))
	}
	if o.cpuBudget > 0 && avgCPU > o.cpuBudget {
		broken = append(broken, fmt.Sprintf("average CPU %.1f%% is over %.1f%%", avgCPU, o.cpuBudget))
	}
	if st.failedStreams.Load() > 0 || st.failedRequests.Load() > 0 {
		broken = append(broken, "some streams or calls failed")
	}
	if len(broken) > 0 {
		return errors.New("over budget: " + strings.Join(broken, "; "))
	}
	return nil
}
