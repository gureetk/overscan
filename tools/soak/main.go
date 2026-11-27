// SPDX-License-Identifier: AGPL-3.0-or-later

// Command soak runs Overscan under load for a while and records its memory
// and CPU use: the soak test of the plan (section 15). It serves a fake
// provider with a large lineup of endless streams and its guide, writes a
// folder of films and series, runs Overscan against them, and simulates
// boxes that sign in, load the channel list, watch channels and switch
// between them, half through the Stalker API and half through the device
// API, which also browse the films. With -own-guide, it adds a large
// XMLTV guide of the household's own (D139) that fills the half of the
// channels that the provider's guide leaves out, and with -placeholders,
// it turns placeholder programmes on (D142) and reads the playlist's XMLTV
// guide once, all before the server idles. With -transcode, the first
// channels stream pictures that ffmpeg makes as they are watched, live TV
// is set to be transcoded always with the profile named (D147), and the
// boxes watch only those channels; the server's ffmpegs are measured too.
//
//	go run ./tools/soak -duration 10m -boxes 4
//	go run ./tools/soak -own-guide 40000 -own-hours 168 -placeholders 60
//	go run ./tools/soak -transcode small -boxes 3 -channels 2000
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
	"image"
	"image/color"
	"image/png"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/cookiejar"
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
	guide     int // hours of programmes for each channel
	films     int
	series    int
	bitrate   int
	zap       time.Duration
	settle    time.Duration
	sample    time.Duration
	bin       string
	out       string
	idleMB    float64
	peakMB    float64
	ownGuide  int // channels that the guide of the household's own lists (0: none)
	ownHours  int // hours of one-hour programmes for each of them
	holders   int // the length of placeholder programmes, in minutes (0: none)
	cpuBudget float64
	// transcode names the profile that live TV is transcoded with, ""
	// for none; pictures counts the channels that stream pictures that
	// ffmpeg makes, of pictureSize; ffmpegMB is the budget of one
	// transcode's ffmpeg.
	transcode   string
	pictures    int
	pictureSize string
	ffmpegMB    float64
}

func main() {
	var o options
	flag.DurationVar(&o.duration, "duration", 10*time.Minute, "how long the boxes run")
	flag.IntVar(&o.boxes, "boxes", 4, "how many boxes to simulate, each watching one stream")
	flag.IntVar(&o.channels, "channels", 20000, "how many channels the fake provider lists")
	flag.IntVar(&o.guide, "guide", 24, "how many hours of one-hour programmes the guide has for each channel (0: no guide)")
	flag.IntVar(&o.films, "films", 10000, "how many films the folder of films holds")
	flag.IntVar(&o.series, "series", 500, "how many series the folder holds, each of two seasons of ten episodes")
	flag.IntVar(&o.bitrate, "bitrate", 5_000_000, "each stream's rate, in bits per second")
	flag.DurationVar(&o.zap, "zap", 2*time.Minute, "how long a box stays on a channel, on average")
	flag.DurationVar(&o.settle, "settle", 30*time.Second, "how long the server idles with the lineup loaded before the boxes start")
	flag.DurationVar(&o.sample, "sample", 5*time.Second, "how often memory and CPU are sampled")
	flag.StringVar(&o.bin, "bin", "", "the Overscan binary to test (default: build it from this repository)")
	flag.StringVar(&o.out, "out", "soak.csv", "where to write the samples")
	flag.Float64Var(&o.idleMB, "idle-mb", 60, "fail if the idle server's resident memory exceeds this, in MB (0: no limit)")
	flag.Float64Var(&o.peakMB, "peak-mb", 150, "fail if resident memory ever exceeds this, in MB (0: no limit)")
	flag.Float64Var(&o.cpuBudget, "cpu", 0, "fail if average CPU use under load exceeds this percentage of one core (0: no limit)")
	flag.IntVar(&o.ownGuide, "own-guide", 0, "how many channels a guide of the household's own lists, filling the half of the lineup that the provider's guide leaves out (0: none)")
	flag.IntVar(&o.ownHours, "own-hours", 168, "how many hours of one-hour programmes the guide of the household's own has for each channel")
	flag.IntVar(&o.holders, "placeholders", 0, "turn placeholder programmes of this many minutes on, and read the XMLTV guide once (0: off)")
	flag.StringVar(&o.transcode, "transcode", "", "transcode live TV always with this profile: h264-720p, h264-1080p, or small or hevc-720p, which the soak makes (default: none)")
	flag.IntVar(&o.pictures, "pictures", 0, "how many channels, the first, stream pictures that ffmpeg makes as they are watched (default: as many as the boxes, with -transcode)")
	flag.StringVar(&o.pictureSize, "picture-size", "1920x1080", "the size of those pictures")
	flag.Float64Var(&o.ffmpegMB, "ffmpeg-mb", 0, "fail if one transcode's ffmpeg ever takes more resident memory than this, in MB (0: no limit)")
	flag.Parse()
	if o.transcode != "" && o.pictures == 0 {
		o.pictures = o.boxes
	}

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

	pictures := pictureSource{n: o.pictures, size: o.pictureSize, bitrate: o.bitrate}
	if o.pictures > 0 {
		if pictures.ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
			return fmt.Errorf("-pictures and -transcode need ffmpeg: %w", err)
		}
	}
	provider, err := startProvider(o.channels, o.guide, o.bitrate, o.ownGuide, o.ownHours, pictures)
	if err != nil {
		return err
	}
	defer provider.Close()
	media := filepath.Join(dir, "media")
	if err := makeLibrary(media, o.films, o.series); err != nil {
		return err
	}

	srv, err := startServer(dir, bin, provider.URL, media)
	if err != nil {
		return err
	}
	defer srv.stop()

	// Memory is sampled from the start, so that the peak takes in reading
	// the lineup, its guide and the films, as well as the boxes.
	m := &meter{pid: srv.cmd.Process.Pid}
	first, err := m.read()
	if err != nil {
		return fmt.Errorf("reading the server's use from /proc: %w", err)
	}
	var loadMu sync.Mutex
	loading := []sample{first}
	loaded := make(chan struct{})
	var loadWG sync.WaitGroup
	loadWG.Add(1)
	go func() {
		defer loadWG.Done()
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-loaded:
				return
			case <-t.C:
				if s, err := m.read(); err == nil {
					loadMu.Lock()
					loading = append(loading, s)
					loadMu.Unlock()
				}
			}
		}
	}()
	if err := srv.waitReady(ctx, o.guide > 0); err != nil {
		close(loaded)
		loadWG.Wait()
		return err
	}
	ready, err := m.read()
	if err != nil {
		return err
	}
	if o.ownGuide > 0 || o.holders > 0 {
		if err := srv.guides(ctx, provider.URL, o); err != nil {
			close(loaded)
			loadWG.Wait()
			return err
		}
	}
	if o.transcode != "" {
		if err := srv.transcoding(o); err != nil {
			close(loaded)
			loadWG.Wait()
			return err
		}
	}

	macs := make([]string, o.boxes)
	for i := range macs {
		macs[i] = fmt.Sprintf("02:00:00:00:%02X:%02X", (i+1)>>8, (i+1)&0xff) // locally administered: no real card has them
		if err := srv.devices("approve", "-label", fmt.Sprintf("Soak box %d", i+1), macs[i]); err != nil {
			return err
		}
	}

	fmt.Printf("lineup of %d channels loaded, with %d hours of guide, %d films and %d series; idling for %s\n",
		o.channels, o.guide, o.films, o.series, o.settle)
	err = sleep(ctx, o.settle)
	close(loaded)
	loadWG.Wait()
	if err != nil {
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
			b := &box{base: srv.base, mac: mac, zap: o.zap, stats: &st, rng: rand.New(rand.NewPCG(uint64(i), 1)), only: o.pictures}
			if i%2 == 0 {
				b.runStalker(runCtx)
			} else {
				b.runDevice(runCtx)
			}
		}()
	}

	samples := append(loading, idle)
	ticker := time.NewTicker(o.sample)
	var tc transcodes
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
			if o.transcode != "" {
				tc.sample(m.pid, s.at)
				tc.speeds(srv)
			}
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
	return report(o, ready, idle, samples, &st, &tc)
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
// /playlist.m3u, each channel as an endless MPEG-TS stream at bitrate, and,
// unless guideHours is 0, their guide at /guide.xml: a programme an hour,
// from the hour before it was asked for.
func startProvider(n, guideHours, bitrate, ownGuide, ownHours int, pictures pictureSource) (*httpServer, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /playlist.m3u", func(w http.ResponseWriter, r *http.Request) {
		bw := bufio.NewWriter(w)
		if guideHours > 0 {
			fmt.Fprintf(bw, "#EXTM3U x-tvg-url=\"http://%s/guide.xml\"\n", r.Host)
		} else {
			bw.WriteString("#EXTM3U\n")
		}
		for i := 1; i <= n; i++ {
			fmt.Fprintf(bw, "#EXTINF:-1 tvg-id=\"ch%d.example\" group-title=\"Group %d\",Soak channel %d\n", i, i%200, i)
			fmt.Fprintf(bw, "http://%s/live/%d.ts\n", r.Host, i)
		}
		bw.Flush()
	})
	mux.HandleFunc("GET /guide.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		bw := bufio.NewWriter(w)
		bw.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<tv>\n")
		// With a guide of the household's own, the provider's guide
		// leaves out the second half of the channels, which it fills.
		listed := n
		if ownGuide > 0 {
			listed = n / 2
		}
		for i := 1; i <= listed; i++ {
			fmt.Fprintf(bw, "<channel id=\"ch%d.example\"><display-name>Soak channel %d</display-name></channel>\n", i, i)
		}
		start := time.Now().UTC().Truncate(time.Hour).Add(-time.Hour)
		for i := 1; i <= listed; i++ {
			for h := range guideHours {
				from := start.Add(time.Duration(h) * time.Hour)
				fmt.Fprintf(bw, "<programme start=\"%s\" stop=\"%s\" channel=\"ch%d.example\"><title>Programme %d of channel %d</title>"+
					"<desc>A programme of the soak test's guide, with a line of description as guides have.</desc></programme>\n",
					from.Format("20060102150405 -0700"), from.Add(time.Hour).Format("20060102150405 -0700"), i, h+1, i)
			}
		}
		bw.WriteString("</tv>\n")
		bw.Flush()
	})
	mux.HandleFunc("GET /own.xml", func(w http.ResponseWriter, r *http.Request) {
		// The guide of the household's own: the provider's second half of
		// channels, by name under guide IDs of its own, then channels that
		// fill nothing, ownGuide in all, each with ownHours of programmes.
		w.Header().Set("Content-Type", "application/xml")
		bw := bufio.NewWriter(w)
		bw.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<tv>\n")
		name := func(k int) string {
			if i := n/2 + k; i <= n {
				return fmt.Sprintf("Soak channel %d", i)
			}
			return fmt.Sprintf("Other channel %d", k)
		}
		for k := 1; k <= ownGuide; k++ {
			fmt.Fprintf(bw, "<channel id=\"own%d.example\"><display-name>%s</display-name><icon src=\"http://%s/icon/%d.png\"/></channel>\n",
				k, name(k), r.Host, k)
		}
		start := time.Now().UTC().Truncate(time.Hour).Add(-24 * time.Hour)
		for k := 1; k <= ownGuide; k++ {
			for h := range ownHours {
				from := start.Add(time.Duration(h) * time.Hour)
				fmt.Fprintf(bw, "<programme start=\"%s\" stop=\"%s\" channel=\"own%d.example\"><title>Own programme %d of %s</title>"+
					"<desc>A programme of the household's own guide, with a line of description as guides have.</desc></programme>\n",
					from.Format("20060102150405 -0700"), from.Add(time.Hour).Format("20060102150405 -0700"), k, h+1, name(k))
			}
		}
		bw.WriteString("</tv>\n")
		bw.Flush()
	})
	// The icons of the guide of the household's own: one small picture,
	// which Overscan's check of logos (D141) fetches into its copies.
	icon, err := iconPNG()
	if err != nil {
		return nil, err
	}
	mux.HandleFunc("GET /icon/{k}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(icon)
	})
	mux.HandleFunc("GET /live/{n}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		if k, err := strconv.Atoi(strings.TrimSuffix(r.PathValue("n"), ".ts")); err == nil && k <= pictures.n {
			pictures.serve(w, r)
			return
		}
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

// makeLibrary writes a folder of empty videos: films, each with its year,
// and series of two seasons of ten episodes each, in folders as a library
// of films and series has them. Nothing plays them; boxes browse them.
func makeLibrary(dir string, films, series int) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for i := 1; i <= films; i++ {
		name := fmt.Sprintf("Soak film %05d (%d).mkv", i, 1950+i%75)
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			return err
		}
	}
	for i := 1; i <= series; i++ {
		show := fmt.Sprintf("Soak series %04d", i)
		for season := 1; season <= 2; season++ {
			folder := filepath.Join(dir, fmt.Sprintf("%s (%d)", show, 1990+i%35), fmt.Sprintf("Season %d", season))
			if err := os.MkdirAll(folder, 0o700); err != nil {
				return err
			}
			for episode := 1; episode <= 10; episode++ {
				name := fmt.Sprintf("%s S%02dE%02d.mkv", show, season, episode)
				if err := os.WriteFile(filepath.Join(folder, name), nil, 0o600); err != nil {
					return err
				}
			}
		}
	}
	return nil
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
	admin             *adminClient // once signed in
}

// signIn sets the administrator's password and signs in to the admin,
// once.
func (s *server) signIn() (*adminClient, error) {
	if s.admin != nil {
		return s.admin, nil
	}
	const password = "soak-test-password"
	cmd := exec.Command(s.bin, "-config", s.config, "admin", "password")
	cmd.Stdin = strings.NewReader(password + "\n")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("overscan admin password: %w\n%s", err, out)
	}
	a, err := newAdminClient(s.base, "admin", password)
	if err != nil {
		return nil, err
	}
	s.admin = a
	return a, nil
}

// startServer starts Overscan with the provider's playlist and the folder
// as its sources.
func startServer(dir, bin, provider, media string) (*server, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	addr := ln.Addr().String()
	ln.Close()
	s := &server{bin: bin, config: filepath.Join(dir, "overscan.yaml"), base: "http://" + addr, log: &syncBuffer{}}
	config := fmt.Sprintf("listen: %q\ndata_dir: %q\nsources:\n  - name: soak\n    type: m3u\n    url: %q\n    max_connections: 0\n"+
		"  - name: films\n    type: folder\n    url: %q\n",
		addr, filepath.Join(dir, "data"), provider+"/playlist.m3u", media)
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
	return s, nil
}

// waitReady waits until the server has read the playlist, and its guide if
// it has one.
func (s *server) waitReady(ctx context.Context, guide bool) error {
	ready := func(log string) bool {
		return strings.Contains(log, "source refreshed") && (!guide || strings.Contains(log, "guide read"))
	}
	for !ready(s.log.String()) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.exited:
			return fmt.Errorf("Overscan exited:\n%s", s.log.String())
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
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

// guides adds the provider's guide of the household's own, and waits for
// its read, and turns placeholder programmes on and reads the playlist's
// XMLTV guide once, as the options say.
func (s *server) guides(ctx context.Context, provider string, o options) error {
	a, err := s.signIn()
	if err != nil {
		return err
	}
	if o.ownGuide > 0 {
		start := time.Now()
		if err := a.call("POST", "guides", map[string]any{"name": "own", "address": provider + "/own.xml"}, nil); err != nil {
			return err
		}
		for !strings.Contains(s.log.String(), "msg=\"guide read\" guide=own") {
			if strings.Contains(s.log.String(), "guide=own") && strings.Contains(s.log.String(), "guide read failed") {
				return fmt.Errorf("the guide of the household's own was not read:\n%s", s.log.String())
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-s.exited:
				return fmt.Errorf("Overscan exited:\n%s", s.log.String())
			case <-time.After(200 * time.Millisecond):
			}
		}
		fmt.Printf("the guide of the household's own, %d channels with %d hours each, read in %s: %s\n", o.ownGuide, o.ownHours,
			time.Since(start).Round(time.Millisecond), lastLine(s.log.String(), "msg=\"guide read\" guide=own"))
	}
	if o.holders > 0 {
		var token struct {
			Token string `json:"token"`
		}
		if err := a.call("PATCH", "settings", map[string]any{"outputs": true, "guide_placeholders": o.holders}, nil); err != nil {
			return err
		}
		if err := a.call("PATCH", "accounts/1", map[string]any{"outputs": map[string]bool{"m3u": true}}, nil); err != nil {
			return err
		}
		if err := a.call("POST", "accounts/1/token", nil, &token); err != nil {
			return err
		}
		start := time.Now()
		resp, err := http.Get(s.base + "/epg/" + token.Token + ".xml")
		if err != nil {
			return err
		}
		n, err := io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the XMLTV guide: %s, %v", resp.Status, err)
		}
		fmt.Printf("the XMLTV guide, with placeholders of %d minutes, read in %s: %.1f MB\n", o.holders,
			time.Since(start).Round(time.Millisecond), float64(n)/(1<<20))
	}
	return nil
}

// iconPNG returns a small picture, a channel's icon.
func iconPNG() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for y := range 16 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{0x3d, 0xd9, 0xc1, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// lastLine returns the log's last line that holds text, "" if none.
func lastLine(log, text string) string {
	lines := strings.Split(log, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], text) {
			return strings.TrimSpace(lines[i])
		}
	}
	return ""
}

// adminClient calls the admin API as the administrator.
type adminClient struct {
	base, csrf string
	http       *http.Client
}

func newAdminClient(base, username, password string) (*adminClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	a := &adminClient{base: base, http: &http.Client{Jar: jar, Timeout: 5 * time.Minute}}
	var s struct {
		CSRF string `json:"csrf_token"`
	}
	if err := a.call("POST", "session", map[string]string{"username": username, "password": password}, &s); err != nil {
		return nil, fmt.Errorf("signing in to the admin: %w", err)
	}
	a.csrf = s.CSRF
	return a, nil
}

func (a *adminClient) call(method, path string, body, out any) error {
	var r io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		r = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.base+"/api/admin/v1/"+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Origin", a.base)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.csrf != "" {
		req.Header.Set("X-CSRF-Token", a.csrf)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, data)
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
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
	shelves                  atomic.Int64 // shelves of films read
}

type box struct {
	base, mac string
	zap       time.Duration
	stats     *stats
	rng       *rand.Rand
	token     string
	only      int // watch only the channels numbered up to this, 0 for any
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
			Data []struct{ Cmd, Number string }
		}
		if b.stalker(ctx, &struct{ ID string }{}, "stb", "get_profile", "stb_type", "MAG250") != nil ||
			b.stalker(ctx, &all, "itv", "get_all_channels") != nil {
			b.pause(ctx)
			continue
		}
		b.stats.listings.Add(1)
		for _, c := range all.Data {
			if n, _ := strconv.Atoi(c.Number); b.only == 0 || n <= b.only {
				cmds = append(cmds, c.Cmd)
			}
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
			Channels []struct{ ID, Number int64 }
		}
		if b.device(ctx, &lineup, "GET", "channels", "") != nil {
			b.pause(ctx)
			continue
		}
		b.stats.listings.Add(1)
		for _, c := range lineup.Channels {
			if b.only == 0 || c.Number <= int64(b.only) {
				ids = append(ids, c.ID)
			}
		}
	}
	for ctx.Err() == nil {
		b.browseFilms(ctx)
		var p struct{ URL string }
		if b.device(ctx, &p, "POST", fmt.Sprintf("channels/%d/play", ids[b.rng.IntN(len(ids))]), "") != nil {
			b.stats.failedStreams.Add(1)
			b.pause(ctx)
			continue
		}
		b.watch(ctx, p.URL)
	}
}

// browseFilms opens Movies, as a box does between channels, and reads the
// first page of the shelves that list films.
func (b *box) browseFilms(ctx context.Context) {
	var lib struct {
		State   string
		Shelves []struct{ ID string }
	}
	if b.device(ctx, &lib, "GET", "movies", "") != nil {
		return
	}
	for _, sh := range lib.Shelves {
		if _, err := strconv.ParseInt(sh.ID, 10, 64); err != nil {
			continue // continue watching, or added lately
		}
		var page json.RawMessage
		if b.device(ctx, &page, "GET", "movies/shelves/"+sh.ID, "") == nil {
			b.stats.shelves.Add(1)
		}
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
	hwmKB   int64 // the most resident memory so far, which samples taken a second apart can miss
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
		if v, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			s.hwmKB, _ = strconv.ParseInt(strings.Fields(v)[0], 10, 64)
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
	_ = w.Write([]string{"seconds", "rss_mb", "cpu_percent", "peak_mb"})
	start := samples[0].at
	for i, s := range samples {
		cpu := ""
		if i > 0 {
			cpu = strconv.FormatFloat(cpuPercent(samples[i-1], s), 'f', 2, 64)
		}
		_ = w.Write([]string{strconv.FormatFloat(s.at.Sub(start).Seconds(), 'f', 0, 64), strconv.FormatFloat(mb(s.rssKB), 'f', 1, 64), cpu,
			strconv.FormatFloat(mb(s.hwmKB), 'f', 1, 64)})
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

// report prints the summary. ready is a sample taken once the sources
// were read, before the guides of the household's own, and idle the one
// before the boxes start.
func report(o options, ready, idle sample, samples []sample, st *stats, tc *transcodes) error {
	// The samples before idle are those taken while the server read its
	// sources; CPU counts from idle on, under the boxes' load. The peaks
	// are the kernel's high-water marks where it has them: the most
	// resident memory by idle is the peak while reading, and a higher mark
	// at the end was reached under load.
	var peak, loadPeak, runPeak int64
	peakCPU := 0.0
	for i, s := range samples {
		peak = max(peak, s.rssKB, s.hwmKB)
		if s.at.Before(idle.at) {
			loadPeak = max(loadPeak, s.rssKB)
			continue
		}
		runPeak = max(runPeak, s.rssKB)
		if i > 0 && !samples[i-1].at.Before(idle.at) {
			peakCPU = max(peakCPU, cpuPercent(samples[i-1], s))
		}
	}
	last := samples[len(samples)-1]
	loadPeak = max(loadPeak, idle.hwmKB)
	if last.hwmKB > idle.hwmKB {
		runPeak = max(runPeak, last.hwmKB)
	}
	avgCPU := cpuPercent(idle, last)
	fmt.Printf("\nSoak test: %s, %d boxes, %d channels with %d hours of guide, %d films and %d series, %.1f Mbit/s a stream, a new channel every %s on average\n",
		o.duration, o.boxes, o.channels, o.guide, o.films, o.series, float64(o.bitrate)/1e6, o.zap)
	fmt.Printf("Resident memory: %.1f MB at the peak while reading the sources, %.1f MB idle with them loaded, %.1f MB at the peak under load, %.1f MB at the end\n",
		mb(loadPeak), mb(idle.rssKB), mb(runPeak), mb(last.rssKB))
	if (o.ownGuide > 0 || o.holders > 0) && ready.hwmKB > 0 {
		fmt.Printf("Of the peak while reading: %.1f MB before the guide of the household's own and the XMLTV guide\n", mb(ready.hwmKB))
	}
	fmt.Printf("CPU: %.1f%% of one core on average under load, %.1f%% at the peak over %s\n", avgCPU, peakCPU, o.sample)
	fmt.Printf("Streams: %d played, %d failed, %.2f GB proxied; channel lists loaded: %d; shelves of films read: %d\n",
		st.streams.Load(), st.failedStreams.Load(), float64(st.bytes.Load())/1e9, st.listings.Load(), st.shelves.Load())
	fmt.Printf("API calls: %d, %d failed\n", st.requests.Load(), st.failedRequests.Load())
	if o.transcode != "" {
		tc.report(o)
	}
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
	if o.transcode != "" && tc.seen == 0 {
		broken = append(broken, "no stream was transcoded")
	}
	if o.ffmpegMB > 0 && mb(tc.peakKB) > o.ffmpegMB {
		broken = append(broken, fmt.Sprintf("a transcode's ffmpeg took %.1f MB, over %.0f MB", mb(tc.peakKB), o.ffmpegMB))
	}
	if len(broken) > 0 {
		return errors.New("over budget: " + strings.Join(broken, "; "))
	}
	return nil
}
