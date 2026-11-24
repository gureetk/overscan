// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// A fictional provider: two playlists with a guide, logos and endless
// streams, and a folder of films and series. Every name in it is made up.

type fakeChannel struct {
	key, name, group string
	guide            string // tvg-id; empty for none
	number           int    // tvg-chno; 0 for none
	radio            bool
}

// mainChannels is the playlist of the source "main", which the
// configuration file declares. Harbour News HD is a copy of Harbour News
// under the same guide ID, for the rule that merges copies.
var mainChannels = []fakeChannel{
	{key: "hn", name: "Harbour News", group: "News", guide: "harbour-news.example"},
	{key: "hnhd", name: "Harbour News HD", group: "News", guide: "harbour-news.example"},
	{key: "gn", name: "Granite News", group: "News", guide: "granite-news.example"},
	{key: "cn", name: "Copper News", group: "News", guide: "copper-news.example"},
	{key: "sn", name: "Summit News", group: "News", guide: "summit-news.example"},
	{key: "ms", name: "Meadow Sport", group: "Sport", guide: "meadow-sport.example"},
	{key: "ws", name: "Willow Sport", group: "Sport", guide: "willow-sport.example"},
	{key: "ls", name: "Lantern Sport", group: "Sport"},
	{key: "gk", name: "Granite Kids", group: "Kids", guide: "granite-kids.example"},
	{key: "gk1", name: "Granite Kids +1", group: "Kids"},
	{key: "wk", name: "Willow Kids", group: "Kids", guide: "willow-kids.example"},
	{key: "lf", name: "Lantern Films", group: "Films", guide: "lantern-films.example"},
	{key: "of", name: "Orchard Films", group: "Films", guide: "orchard-films.example"},
	{key: "cf", name: "Cobalt Films", group: "Films"},
	{key: "js", name: "Juniper Shop", group: "Shop"},
	{key: "mfm", name: "Meadow FM", group: "Radio", radio: true},
}

// twoChannels is the playlist of the source "two", which the admin adds,
// numbered by the provider.
var twoChannels = []fakeChannel{
	{key: "hnx", name: "Harbour News Extra", group: "Extra", guide: "harbour-news.example", number: 201},
	{key: "cx", name: "Cobalt Extra", group: "Extra", guide: "cobalt-extra.example", number: 202},
	{key: "ox", name: "Orchard Extra", group: "Extra", number: 203},
}

// threeChannels is the playlist of the source "three", which only the walk
// through M8's Done when adds (donewhen_test.go).
var threeChannels = []fakeChannel{
	{key: "cb", name: "Cobalt Bulletin", group: "Bulletins", guide: "cobalt-bulletin.example"},
	{key: "mb", name: "Meadow Bulletin", group: "Bulletins"},
	{key: "wt", name: "Willow Travel", group: "Travel"},
}

// extraFills are the channels that the guide of the household's own,
// extra.xml, fills by name: their sources' guide has none of them. It
// lists Granite News too, which keeps its source's programmes, and whose
// guide Juniper Shop takes by hand from v1.2.0-alpha.2 on.
var extraFills = []string{"Lantern Sport", "Cobalt Films", "Orchard Extra"}

// libraryFiles are the folder source's films and episodes, relative to its
// folder. Overscan lists them by their names; their bytes are no video.
var libraryFiles = []string{
	"Quiet Harbour (2001)/Quiet Harbour (2001).mkv",
	"Copper Field (1999)/Copper Field (1999).mkv",
	"Silver Tide (2010).mkv",
	"Coastline (2019)/Season 1/Coastline S01E01.mkv",
	"Coastline (2019)/Season 1/Coastline S01E02.mkv",
	"Coastline (2019)/Season 2/Coastline S02E01.mkv",
}

type provider struct {
	srv  *http.Server
	url  string
	base time.Time // the guide's programmes start on the hours from here
	logo []byte

	mu       sync.Mutex
	added    map[string][]fakeChannel // channels added to a playlist since, by its name
	played   []string                 // the keys of the streams asked for, in order
	pictures map[string][]byte        // streams of real pictures, by key; null packets for the others
}

// add adds a channel to a playlist, for its next read.
func (p *provider) add(playlist string, c fakeChannel) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.added == nil {
		p.added = map[string][]fakeChannel{}
	}
	p.added[playlist] = append(p.added[playlist], c)
}

// asked returns the keys of the streams asked for so far.
func (p *provider) asked() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.played)
}

// usePictures makes the provider stream these bytes, again and again, for
// the channels of these keys.
func (p *provider) usePictures(streams map[string][]byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pictures = streams
}

func startProvider() (*provider, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &provider{url: "http://" + ln.Addr().String(), base: time.Now().UTC().Truncate(time.Hour)}
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := range 32 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{0x3d, 0xd9, 0xc1, 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	p.logo = buf.Bytes()

	mux := http.NewServeMux()
	for name, channels := range map[string][]fakeChannel{"main": mainChannels, "two": twoChannels, "three": threeChannels} {
		mux.HandleFunc("GET /"+name+".m3u", func(w http.ResponseWriter, r *http.Request) {
			p.mu.Lock()
			all := append(slices.Clone(channels), p.added[name]...)
			p.mu.Unlock()
			p.playlist(w, all)
		})
	}
	mux.HandleFunc("GET /guide.xml", p.guide)
	mux.HandleFunc("GET /extra.xml", p.extraGuide)
	mux.HandleFunc("GET /logo/{key}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(p.logo)
	})
	mux.HandleFunc("GET /live/{key}", p.stream)
	p.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

func (p *provider) Close() { _ = p.srv.Close() }

func (p *provider) playlist(w http.ResponseWriter, channels []fakeChannel) {
	w.Header().Set("Content-Type", "audio/x-mpegurl")
	var b strings.Builder
	fmt.Fprintf(&b, "#EXTM3U url-tvg=\"%s/guide.xml\"\n", p.url)
	for _, c := range channels {
		attrs := fmt.Sprintf(`tvg-id="%s" tvg-name="%s" tvg-logo="%s/logo/%s.png" group-title="%s"`, c.guide, c.name, p.url, c.key, c.group)
		if c.number != 0 {
			attrs += fmt.Sprintf(` tvg-chno="%d"`, c.number)
		}
		if c.radio {
			attrs += ` radio="true"`
		}
		fmt.Fprintf(&b, "#EXTINF:-1 %s,%s\n%s/live/%s.ts\n", attrs, c.name, p.url, c.key)
	}
	_, _ = w.Write([]byte(b.String()))
}

// guide serves programmes of an hour each, from 12 hours before the
// provider started to two days after, so that what it serves stays the same
// for the whole test.
func (p *provider) guide(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<tv generator-info-name=\"upgradetest\">\n")
	seen := map[string]bool{}
	var ids []fakeChannel
	for _, c := range slices.Concat(mainChannels, twoChannels, threeChannels) {
		if c.guide != "" && !seen[c.guide] {
			seen[c.guide] = true
			ids = append(ids, c)
			fmt.Fprintf(&b, "<channel id=\"%s\"><display-name>%s</display-name></channel>\n", c.guide, c.name)
		}
	}
	const layout = "20060102150405 -0700"
	for _, c := range ids {
		for h := -12; h < 48; h++ {
			start := p.base.Add(time.Duration(h) * time.Hour)
			fmt.Fprintf(&b, "<programme start=\"%s\" stop=\"%s\" channel=\"%s\"><title lang=\"en\">%s at %s</title><desc lang=\"en\">A made-up programme.</desc></programme>\n",
				start.Format(layout), start.Add(time.Hour).Format(layout), c.guide, c.name, start.Format("15:04"))
		}
	}
	b.WriteString("</tv>\n")
	_, _ = w.Write([]byte(b.String()))
}

// extraGuide serves the guide of the household's own: the channels of
// extraFills under IDs of its own, and Granite News under its source's.
func (p *provider) extraGuide(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/xml")
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<tv generator-info-name=\"upgradetest extra\">\n")
	type listed struct{ id, name string }
	channels := []listed{{"granite-news.example", "Granite News"}}
	for i, name := range extraFills {
		channels = append(channels, listed{fmt.Sprintf("extra-%d.example", i+1), name})
	}
	for _, c := range channels {
		fmt.Fprintf(&b, "<channel id=\"%s\"><display-name>%s</display-name><icon src=\"%s/logo/%s-icon.png\"/></channel>\n",
			c.id, c.name, p.url, c.id)
	}
	const layout = "20060102150405 -0700"
	for _, c := range channels {
		for h := -12; h < 48; h += 2 {
			start := p.base.Add(time.Duration(h) * time.Hour)
			fmt.Fprintf(&b, "<programme start=\"%s\" stop=\"%s\" channel=\"%s\"><title lang=\"en\">%s, from the extra guide</title></programme>\n",
				start.Format(layout), start.Add(2*time.Hour).Format(layout), c.id, c.name)
		}
	}
	b.WriteString("</tv>\n")
	_, _ = w.Write([]byte(b.String()))
}

// programme returns when the programme that starts h hours after the
// provider started begins.
func (p *provider) programme(h int) time.Time { return p.base.Add(time.Duration(h) * time.Hour) }

// stream serves MPEG-TS null packets until the client goes, or the
// channel's pictures again and again if it has some.
func (p *provider) stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "video/mp2t")
	key := strings.TrimSuffix(r.PathValue("key"), ".ts")
	p.mu.Lock()
	p.played = append(p.played, key)
	chunk := p.pictures[key]
	p.mu.Unlock()
	if chunk == nil {
		chunk = make([]byte, 188*50)
		for i := 0; i < len(chunk); i += 188 {
			copy(chunk[i:], []byte{0x47, 0x1f, 0xff, 0x10})
			for j := i + 4; j < i+188; j++ {
				chunk[j] = 0xff
			}
		}
	}
	flusher, _ := w.(http.Flusher)
	deadline := time.After(10 * time.Minute)
	for {
		if _, err := w.Write(chunk); err != nil {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline:
			return
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// writeLibrary makes the folder source's files under dir.
func writeLibrary(dir string) error {
	for _, name := range libraryFiles {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte("not a video: a made-up file for the upgrade test\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}
