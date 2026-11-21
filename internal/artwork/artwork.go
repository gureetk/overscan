// SPDX-License-Identifier: AGPL-3.0-or-later

// Package artwork serves the pictures that devices show, from Overscan's own
// address (D54): channel logos, and the posters, wide pictures and episode
// pictures of films and series. It fetches each image from its source once,
// scales it down to the size that screens draw, keeps it on disk, and
// serves it to every device from there. Devices never learn the source's
// address, and a box draws a small picture faster than it scales a large
// one.
//
// An image's address can name its provider, so no error or log line from
// this package quotes it.
package artwork

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for image.Decode
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"overscan.gureet.ca/internal/netguard"
)

// Limits.
const (
	// LogoWidth and LogoHeight bound a logo, about twice the size that the
	// box's screens draw, so it stays sharp on a 1080p picture.
	LogoWidth  = 240
	LogoHeight = 96

	maxFetchBytes = 4 << 20    // of an image as its source sends it
	maxPassBytes  = 512 << 10  // of a WebP image kept as it is
	maxPixels     = 25_000_000 // of an image to decode
	fetchTimeout  = 20 * time.Second
	retryAfter    = time.Hour // after a fetch fails
	maxFetches    = 4         // at once
)

// Kind is a kind of picture: the most it may measure, and whether it is kept
// as JPEG, for photographs, or as PNG, which keeps transparency.
type Kind struct {
	Name          string // in its path on Overscan, and its file's name
	Width, Height int
	JPEG          bool
}

// The kinds of picture. A logo is about twice the size that the box's
// screens draw, so it stays sharp on a 1080p picture; the others are drawn
// at about their size on a 1280×720 page, which the box scales up.
var (
	LogoKind     = Kind{Name: "logo", Width: LogoWidth, Height: LogoHeight}
	PosterKind   = Kind{Name: "poster", Width: 240, Height: 360, JPEG: true}
	ThumbKind    = Kind{Name: "thumb", Width: 96, Height: 144, JPEG: true} // a poster, small, for a list's rows
	BackdropKind = Kind{Name: "backdrop", Width: 960, Height: 540, JPEG: true}
	StillKind    = Kind{Name: "still", Width: 416, Height: 234, JPEG: true}
)

// jpegQuality is the quality that photographs are kept at.
const jpegQuality = 82

// matte is what a photograph's transparent parts become: the page's own
// background, near-black.
var matte = color.RGBA{0x16, 0x16, 0x18, 0xff}

// Cache fetches images and keeps them, scaled, in a directory.
type Cache struct {
	dir       string
	log       *slog.Logger
	client    *http.Client
	userAgent string
	now       func() time.Time
	slots     chan struct{}

	mu       sync.Mutex
	inflight map[string]*fetch
	failed   map[string]time.Time
}

// fetch is a fetch under way, which callers for the same image wait for.
type fetch struct {
	done chan struct{}
	err  error
}

// New returns a cache that keeps images in dir, fetching them as
// Overscan/version unless a source names another user agent.
func New(dir string, log *slog.Logger, version string) *Cache {
	return &Cache{
		dir:       dir,
		log:       log,
		client:    &http.Client{Timeout: fetchTimeout},
		userAgent: "Overscan/" + version,
		now:       time.Now,
		slots:     make(chan struct{}, maxFetches),
		inflight:  map[string]*fetch{},
		failed:    map[string]time.Time{},
	}
}

// Key returns the name that the image at an address is kept under. It
// changes with the address, so a device may cache what it names forever.
func Key(address string) string {
	sum := sha256.Sum256([]byte("overscan artwork\x00" + address))
	return hex.EncodeToString(sum[:12])
}

// LogoPath returns the path on Overscan that serves a lineup entry's logo,
// "" for a channel without one.
func LogoPath(entryID int64, logo string) string {
	if logo == "" {
		return ""
	}
	return "/artwork/logo/" + strconv.FormatInt(entryID, 10) + "/" + Key(logo)
}

// ErrUnusable is the error for an address that gives no image Overscan can
// serve.
var ErrUnusable = errors.New("artwork: not an image that devices can be given")

// TitlePath returns the path on Overscan that serves a picture of a kind
// for a film, a series or an episode, by its ID, "" for one without it.
func TitlePath(kind Kind, id int64, address string) string {
	if address == "" {
		return ""
	}
	return "/artwork/" + kind.Name + "/" + strconv.FormatInt(id, 10) + "/" + Key(address)
}

// files returns the names of the files that may keep a picture of a kind:
// a scaled image, or a WebP image as its source sent it, and their content
// types. A logo's names hold only its key, as they did before other kinds.
func (k Kind) files(key string) [2]struct{ name, contentType string } {
	stem, scaled, ext := key, "image/png", ".png"
	if k.Name != LogoKind.Name {
		stem = key + "-" + k.Name
	}
	if k.JPEG {
		scaled, ext = "image/jpeg", ".jpg"
	}
	return [2]struct{ name, contentType string }{{stem + ext, scaled}, {stem + ".webp", "image/webp"}}
}

// Logo returns the file that holds the logo at address, and its content
// type, as Get does.
func (c *Cache) Logo(ctx context.Context, address, userAgent string) (string, string, error) {
	return c.Get(ctx, LogoKind, address, userAgent)
}

// Get returns the file that holds the picture of a kind at address, and its
// content type, fetching and scaling it the first time. userAgent is the
// source's, "" for Overscan's own. A fetch that failed is not tried again
// for an hour.
func (c *Cache) Get(ctx context.Context, kind Kind, address, userAgent string) (string, string, error) {
	return c.GetWith(ctx, kind, address, func() ([]byte, error) { return c.get(address, userAgent, false) })
}

// GetWith is Get for a picture that read gives, such as one in a folder of
// films, rather than one that Overscan fetches; address names it, and
// changes when it does. read runs outside ctx, at most once an hour.
func (c *Cache) GetWith(ctx context.Context, kind Kind, address string, read func() ([]byte, error)) (string, string, error) {
	key := Key(address)
	if file, contentType, ok := c.kept(kind, key); ok {
		return file, contentType, nil
	}
	id := kind.Name + "/" + key
	c.mu.Lock()
	if at, ok := c.failed[id]; ok && c.now().Sub(at) < retryAfter {
		c.mu.Unlock()
		return "", "", ErrUnusable
	}
	f, ok := c.inflight[id]
	if !ok {
		f = &fetch{done: make(chan struct{})}
		c.inflight[id] = f
		go c.run(f, kind, key, read)
	}
	c.mu.Unlock()
	select {
	case <-f.done:
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	if f.err != nil {
		return "", "", f.err
	}
	if file, contentType, ok := c.kept(kind, key); ok {
		return file, contentType, nil
	}
	return "", "", ErrUnusable
}

// kept returns the file kept for a picture, if there is one.
func (c *Cache) kept(kind Kind, key string) (string, string, bool) {
	for _, f := range kind.files(key) {
		file := filepath.Join(c.dir, f.name)
		if _, err := os.Stat(file); err == nil {
			return file, f.contentType, true
		}
	}
	return "", "", false
}

// run fetches an image and keeps it, outside any request, so that a device
// that gives up does not leave the next one without it.
func (c *Cache) run(f *fetch, kind Kind, key string, read func() ([]byte, error)) {
	c.slots <- struct{}{}
	err := c.store(kind, key, read)
	<-c.slots
	id := kind.Name + "/" + key
	c.mu.Lock()
	delete(c.inflight, id)
	if err != nil {
		c.failed[id] = c.now()
	} else {
		delete(c.failed, id)
	}
	c.mu.Unlock()
	if err != nil {
		c.log.Debug("artwork not kept", "kind", kind.Name, "key", key, "error", err)
	}
	f.err = err
	close(f.done)
}

// store reads an image, scales it to its kind and keeps it.
func (c *Cache) store(kind Kind, key string, read func() ([]byte, error)) error {
	data, err := read()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return err
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxPixels {
			return ErrUnusable
		}
		img, _, err := image.Decode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnusable, err)
		}
		files := kind.files(key)
		scaled := Fit(img, kind.Width, kind.Height)
		var out bytes.Buffer
		if kind.JPEG {
			// JPEG has no transparency: put the picture on the page's own
			// background.
			flat := image.NewRGBA(scaled.Bounds())
			draw.Draw(flat, flat.Bounds(), &image.Uniform{C: matte}, image.Point{}, draw.Src)
			draw.Draw(flat, flat.Bounds(), scaled, image.Point{}, draw.Over)
			err = jpeg.Encode(&out, flat, &jpeg.Options{Quality: jpegQuality})
		} else {
			err = png.Encode(&out, scaled)
		}
		if err != nil {
			return err
		}
		return c.write(files[0].name, out.Bytes())
	}
	if isWebP(data) && len(data) <= maxPassBytes {
		return c.write(kind.files(key)[1].name, data)
	}
	return ErrUnusable
}

// get fetches an image, at most maxFetchBytes of it, from a public address
// only if publicOnly (D121).
func (c *Cache) get(address, userAgent string, publicOnly bool) ([]byte, error) {
	u, err := url.Parse(address)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, ErrUnusable
	}
	req, err := http.NewRequest(http.MethodGet, address, nil)
	if err != nil {
		return nil, ErrUnusable
	}
	if userAgent == "" {
		userAgent = c.userAgent
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := netguard.Client(c.client, publicOnly).Do(req)
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err // without the address
		}
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the image's server answered %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes+1))
	if err != nil {
		return nil, errors.New("fetch: the image was cut short")
	}
	if len(data) > maxFetchBytes {
		return nil, fmt.Errorf("%w: larger than %d MiB", ErrUnusable, maxFetchBytes>>20)
	}
	return data, nil
}

// write keeps data under name, whole or not at all.
func (c *Cache) write(name string, data []byte) error {
	tmp, err := os.CreateTemp(c.dir, ".artwork-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(c.dir, name)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}

// isWebP reports whether data starts like a WebP file.
func isWebP(data []byte) bool {
	return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
}

// Fit scales img down to fit within w×h, keeping its shape, by averaging
// the pixels that each new pixel covers. It never scales up.
func Fit(img image.Image, w, h int) *image.RGBA {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	dw, dh := sw, sh
	if dw > w {
		dw, dh = w, max(1, sh*w/sw)
	}
	if dh > h {
		dw, dh = max(1, sw*h/sh), h
	}
	// Premultiplied pixels average without dark fringes around
	// transparent parts.
	src := image.NewRGBA(image.Rect(0, 0, sw, sh))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	if dw == sw && dh == sh {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := range dh {
		y0, y1 := y*sh/dh, max(y*sh/dh+1, (y+1)*sh/dh)
		for x := range dw {
			x0, x1 := x*sw/dw, max(x*sw/dw+1, (x+1)*sw/dw)
			var r, g, bl, a, n int
			for sy := y0; sy < y1; sy++ {
				row := src.Pix[sy*src.Stride:]
				for sx := x0; sx < x1; sx++ {
					p := row[sx*4 : sx*4+4]
					r, g, bl, a = r+int(p[0]), g+int(p[1]), bl+int(p[2]), a+int(p[3])
					n++
				}
			}
			o := dst.Pix[y*dst.Stride+x*4:]
			o[0], o[1], o[2], o[3] = uint8(r/n), uint8(g/n), uint8(bl/n), uint8(a/n)
		}
	}
	return dst
}
