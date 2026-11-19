// SPDX-License-Identifier: AGPL-3.0-or-later

// Package diagnose checks a channel, for the "overscan debug channel"
// command: what its source answers, and what a player gets through the
// stream hub. Its reports name no addresses, since they can hold
// credentials, so that they can be shared.
package diagnose

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/hls"
	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/mpegts"
	"overscan.gureet.ca/internal/sniff"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tsinfo"
)

const (
	// sourceTimeout caps the look at what the source answers.
	sourceTimeout = 20 * time.Second

	// maxPlaylistBytes caps a playlist read from the source.
	maxPlaylistBytes = 2 << 20

	// sniffBytes is how much of a body is read to tell what it is.
	sniffBytes = max(4096, mpegts.StartBytes)
)

// Hop is one answer from the source on the way to the stream.
type Hop struct {
	Status        string
	ContentType   string
	ContentLength int64 // -1 if not given
	Server        string
	// Where a redirect leads: "the same host" or "another host", and the
	// extension of the address, if it has one.
	Next, NextExt string
}

// SourceReport is what the source answers when asked for the channel.
type SourceReport struct {
	Ext   string // the extension of the channel's address, if any
	Hops  []Hop
	Err   string   // why the source could not be asked, if it could not
	Kind  string   // what the source sends, such as "MPEG-TS"
	Notes []string // more about it, such as a playlist's details
}

// Source asks the source for the stream at target, as the hub does, and
// reports what it answers.
func Source(ctx context.Context, client *http.Client, target, userAgent string) SourceReport {
	r := SourceReport{Ext: extension(target)}
	ctx, cancel := context.WithTimeout(ctx, sourceTimeout)
	defer cancel()
	c := *client
	c.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("more than 10 redirects")
		}
		r.Hops = append(r.Hops, hop(req.Response, via[len(via)-1].URL, req.URL))
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		r.Err = "the channel's address is not valid"
		return r
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.Do(req)
	if err != nil {
		r.Err = describeError(err)
		return r
	}
	defer resp.Body.Close()
	r.Hops = append(r.Hops, hop(resp, nil, nil))
	if resp.StatusCode != http.StatusOK {
		return r
	}

	head := make([]byte, sniffBytes)
	n, err := io.ReadAtLeast(resp.Body, head, mpegts.StartBytes)
	head = head[:n]
	if n == 0 {
		r.Kind = "nothing: the body is empty"
		if err != nil && !errors.Is(err, io.EOF) {
			r.Kind = "nothing: " + describeError(err)
		}
		return r
	}
	// A body too short for Start to tell is no stream to play: it is
	// described as it is.
	at, ts, _ := mpegts.Start(head)
	switch {
	case ts && at == 0:
		r.Kind = "MPEG-TS"
	case ts:
		r.Kind = fmt.Sprintf("MPEG-TS whose first whole packet starts %d bytes in", at)
	case hls.IsPlaylist(head):
		rest, _ := io.ReadAll(io.LimitReader(resp.Body, maxPlaylistBytes))
		r.Kind, r.Notes = describePlaylist(append(head, rest...), resp.Request.URL)
	default:
		r.Kind = sniff.Describe(head)
	}
	return r
}

// hop describes an answer; for a redirect, from and to are the addresses it
// leads from and to.
func hop(resp *http.Response, from, to *url.URL) Hop {
	h := Hop{
		Status:        sniff.Scrub(resp.Status),
		ContentType:   sniff.Scrub(resp.Header.Get("Content-Type")),
		ContentLength: resp.ContentLength,
		Server:        sniff.Scrub(resp.Header.Get("Server")),
	}
	if to != nil {
		h.Next = "another host"
		if from != nil && strings.EqualFold(from.Host, to.Host) {
			h.Next = "the same host"
		}
		h.NextExt = extension(to.String())
	}
	return h
}

// describePlaylist tells what a playlist is, and what the relay would do
// with it.
func describePlaylist(data []byte, base *url.URL) (string, []string) {
	if hls.IsMaster(data) {
		m := hls.ParseMaster(data, base)
		notes := []string{}
		for _, v := range m.Variants {
			line := fmt.Sprintf("variant: %s", rate(v.Bandwidth))
			if v.Height > 0 {
				line += fmt.Sprintf(", %d lines", v.Height)
			}
			if v.Audio != "" && m.SeparateAudio[v.Audio] {
				line += ", sound in a separate stream"
			}
			notes = append(notes, line)
		}
		if v, ok := m.Pick(); ok {
			notes = append(notes, fmt.Sprintf("Overscan picks the variant of %s.", rate(v.Bandwidth)))
		} else {
			notes = append(notes, "No variant carries its own sound, so Overscan cannot relay it.")
		}
		return fmt.Sprintf("an HLS master playlist with %d variants", len(m.Variants)), notes
	}
	pl := hls.ParseMedia(data, base)
	if pl.Target == 0 {
		return fmt.Sprintf("an M3U list of %d streams, not live HLS", len(pl.Segments)), nil
	}
	kind := "a live HLS playlist"
	if pl.Ended {
		kind = "an HLS playlist that has ended"
	}
	notes := []string{fmt.Sprintf("%d segments, target duration %s", len(pl.Segments), seconds(pl.Target))}
	if len(pl.Segments) > 0 {
		notes[0] += fmt.Sprintf(", numbered from %d", pl.Segments[0].Seq)
		if pl.Segments[0].Key != nil {
			notes = append(notes, "Its segments are encrypted with AES-128.")
		}
	}
	if pl.Unsupported != "" {
		notes = append(notes, "It has "+pl.Unsupported+", which Overscan cannot relay.")
	}
	return kind, notes
}

// describeError describes a failed request without its address.
func describeError(err error) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		err = uerr.Err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "the source did not answer in time"
	}
	return sniff.Scrub(err.Error())
}

// extension returns the extension of an address's path, if it is short and
// plain, such as .ts or .m3u8.
func extension(address string) string {
	u, err := url.Parse(address)
	if err != nil {
		return ""
	}
	ext := strings.ToLower(path.Ext(u.Path))
	if len(ext) < 2 || len(ext) > 6 {
		return ""
	}
	return ext
}

// PlayerReport is what a player gets through the hub.
type PlayerReport struct {
	Link        string // the link a box gets: ".ts" or ".m3u8"
	Status      int
	Error       string // the hub's answer if it is an error
	ContentType string
	Stream      tsinfo.Report
	Ended       bool // the stream ended before the time was up
}

// Player plays the channel through h as a player that asked for a
// continuous stream does, as the MAG box does, for the duration d, and
// reports what it gets. A channel whose address ends in .m3u8 is played
// through the relay too, which the box does not do.
func Player(ctx context.Context, h *hub.Hub, entry store.PlayableEntry, d time.Duration) PlayerReport {
	link := h.Link(0, entry)
	r := PlayerReport{Link: path.Ext(link)}
	link = strings.TrimSuffix(link, ".m3u8")
	if !strings.HasSuffix(link, ".ts") {
		link += ".ts"
	}
	ctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://overscan.invalid"+link, nil)
	if err != nil {
		r.Error = err.Error()
		return r
	}

	pr, pw := io.Pipe()
	resp := &pipeResponse{header: http.Header{}, pw: pw, ready: make(chan struct{})}
	start := time.Now()
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.ServeHTTP(resp, req)
		resp.WriteHeader(http.StatusOK) // in case nothing was written
		pw.Close()
	}()
	defer func() {
		pr.Close()
		<-done
	}()

	<-resp.ready
	r.Status = resp.status
	r.ContentType = resp.header.Get("Content-Type")
	if r.Status != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(pr, 1000))
		r.Error = sniff.Scrub(string(msg))
		return r
	}
	a := tsinfo.New(func() time.Duration { return time.Since(start) })
	_, _ = io.Copy(a, pr)
	r.Ended = ctx.Err() == nil
	r.Stream = a.Report()
	return r
}

// pipeResponse is an http.ResponseWriter whose body goes into a pipe.
type pipeResponse struct {
	header http.Header
	pw     *io.PipeWriter
	once   sync.Once
	status int
	ready  chan struct{} // closed once the status is known
}

func (p *pipeResponse) Header() http.Header { return p.header }

func (p *pipeResponse) WriteHeader(code int) {
	p.once.Do(func() {
		p.status = code
		close(p.ready)
	})
}

func (p *pipeResponse) Write(b []byte) (int, error) {
	p.WriteHeader(http.StatusOK)
	return p.pw.Write(b)
}

func (p *pipeResponse) Flush() {}

// rate formats a bit rate.
func rate(bitsPerSecond int64) string {
	return fmt.Sprintf("%.1f Mbit/s", float64(bitsPerSecond)/1e6)
}

// seconds formats a duration in seconds, to a tenth.
func seconds(d time.Duration) string {
	return fmt.Sprintf("%.1fs", d.Seconds())
}

// position formats how far into the reading something happened, as m:ss.
func position(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// megabytes formats a number of bytes.
func megabytes(n int64) string {
	return fmt.Sprintf("%.1f MB", float64(n)/1e6)
}
