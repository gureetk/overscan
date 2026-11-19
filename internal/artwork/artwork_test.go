// SPDX-License-Identifier: AGPL-3.0-or-later

package artwork

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func solid(w, h int, c color.Color) image.Image {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, c)
		}
	}
	return img
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestFit(t *testing.T) {
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{1000, 500, 192, 96}, // as wide as it may be, then as high
		{600, 100, 240, 40},
		{50, 500, 9, 96},
		{100, 40, 100, 40}, // small enough already
	} {
		got := Fit(solid(tc.w, tc.h, color.White), LogoWidth, LogoHeight).Bounds()
		if got.Dx() != tc.wantW || got.Dy() != tc.wantH {
			t.Errorf("%d×%d: %d×%d, want %d×%d", tc.w, tc.h, got.Dx(), got.Dy(), tc.wantW, tc.wantH)
		}
	}
	// Opaque red beside a transparent pixel averages to half-transparent
	// red, not to a darker red.
	img := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	img.Set(0, 0, color.NRGBA{255, 0, 0, 255})
	img.Set(1, 0, color.NRGBA{0, 0, 0, 0})
	got := color.NRGBAModel.Convert(Fit(img, 1, 1).At(0, 0)).(color.NRGBA)
	if got.R < 250 || got.G != 0 || got.B != 0 || got.A < 120 || got.A > 135 {
		t.Errorf("averaged %+v", got)
	}
}

// provider serves made-up images and counts the requests for each path.
type provider struct {
	*httptest.Server
	mu    sync.Mutex
	hits  map[string]int
	agent string
}

func newProvider(t *testing.T) *provider {
	t.Helper()
	big := encodePNG(t, solid(1000, 400, color.NRGBA{20, 120, 200, 255}))
	var jpg bytes.Buffer
	if err := jpeg.Encode(&jpg, solid(64, 64, color.Gray{128}), nil); err != nil {
		t.Fatal(err)
	}
	webp := append([]byte("RIFF\x10\x00\x00\x00WEBPVP8 "), make([]byte, 8)...)
	p := &provider{hits: map[string]int{}}
	p.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.hits[r.URL.Path]++
		p.agent = r.UserAgent()
		p.mu.Unlock()
		switch r.URL.Path {
		case "/logo.png":
			w.Write(big)
		case "/logo.jpg":
			w.Write(jpg.Bytes())
		case "/logo.webp":
			w.Write(webp)
		case "/logo.svg":
			io.WriteString(w, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
		case "/huge.png":
			w.Write(bytes.Repeat([]byte{0}, maxFetchBytes+10))
		case "/slow.png":
			time.Sleep(50 * time.Millisecond)
			w.Write(big)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *provider) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits[path]
}

func TestLogo(t *testing.T) {
	p := newProvider(t)
	c := New(t.TempDir(), quiet(), "test")
	ctx := context.Background()

	file, contentType, err := c.Logo(ctx, p.URL+"/logo.png", "")
	if err != nil || contentType != "image/png" {
		t.Fatalf("png: %q %q %v", file, contentType, err)
	}
	data, _ := os.ReadFile(file)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "png" || cfg.Width != LogoWidth || cfg.Height != LogoWidth*400/1000 {
		t.Errorf("kept %s %d×%d, %v", format, cfg.Width, cfg.Height, err)
	}
	if p.agent != "Overscan/test" {
		t.Errorf("user agent %q", p.agent)
	}
	// Kept: a second request fetches nothing.
	if _, _, err := c.Logo(ctx, p.URL+"/logo.png", ""); err != nil || p.count("/logo.png") != 1 {
		t.Errorf("again: %v, %d fetches", err, p.count("/logo.png"))
	}

	if _, contentType, err := c.Logo(ctx, p.URL+"/logo.jpg", "Box/1.0"); err != nil || contentType != "image/png" || p.agent != "Box/1.0" {
		t.Errorf("jpeg: %q %v, agent %q", contentType, err, p.agent)
	}
	if file, contentType, err := c.Logo(ctx, p.URL+"/logo.webp", ""); err != nil || contentType != "image/webp" ||
		!strings.HasSuffix(file, ".webp") {
		t.Errorf("webp: %q %q %v", file, contentType, err)
	}
	for _, path := range []string{"/logo.svg", "/missing.png", "/huge.png"} {
		_, _, err := c.Logo(ctx, p.URL+path, "")
		if err == nil || strings.Contains(err.Error(), p.Listener.Addr().String()) || strings.Contains(err.Error(), path) {
			t.Errorf("%s: %v", path, err)
		}
	}
	for _, address := range []string{"file:///etc/passwd", "ftp://example.com/logo.png", "not an address"} {
		if _, _, err := c.Logo(ctx, address, ""); err == nil {
			t.Errorf("%s: no error", address)
		}
	}

	// A failure is not tried again for an hour.
	now := time.Now()
	c.now = func() time.Time { return now }
	c.Logo(ctx, p.URL+"/missing2.png", "")
	c.Logo(ctx, p.URL+"/missing2.png", "")
	if n := p.count("/missing2.png"); n != 1 {
		t.Errorf("tried a failure again at once: %d fetches", n)
	}
	c.now = func() time.Time { return now.Add(retryAfter) }
	c.Logo(ctx, p.URL+"/missing2.png", "")
	if n := p.count("/missing2.png"); n != 2 {
		t.Errorf("after an hour: %d fetches", n)
	}
}

func TestLogoFetchedOnce(t *testing.T) {
	p := newProvider(t)
	c := New(t.TempDir(), quiet(), "test")
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.Logo(context.Background(), p.URL+"/slow.png", ""); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := p.count("/slow.png"); n != 1 {
		t.Errorf("%d fetches for eight devices at once", n)
	}
}

func TestLogoPath(t *testing.T) {
	if got := LogoPath(17, ""); got != "" {
		t.Errorf("no logo: %q", got)
	}
	a, b := LogoPath(17, "http://img.example/a.png"), LogoPath(17, "http://img.example/b.png")
	if !strings.HasPrefix(a, "/artwork/logo/17/") || a == b || strings.Contains(a, "img.example") {
		t.Errorf("paths %q and %q", a, b)
	}
}
