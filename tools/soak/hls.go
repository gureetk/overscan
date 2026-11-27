// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// The soak's HLS players (D153, D155): players that sign in with the
// household's login, ask for channels as HLS, and read their playlists and
// segments as a player does, beside the boxes; and the disk that the
// server's buffers take for them.

const (
	hlsUser     = "soak"
	hlsPassword = "soak-player-password"
)

type hlsStats struct {
	playlists, segments, failed atomic.Int64
	bytes                       atomic.Int64
	// diskKB is the most that the server's buffers took on disk, in KB.
	diskKB atomic.Int64
}

// hlsPlayers turns on the outputs, the household's login and its HLS for
// players, and returns the stream IDs of the channels that the players
// watch: those numbered up to only, or every one if only is 0.
func (s *server) hlsPlayers(only int) ([]int64, error) {
	a, err := s.signIn()
	if err != nil {
		return nil, err
	}
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", "settings", map[string]any{"outputs": true}},
		{"PATCH", "accounts/1", map[string]any{"outputs": map[string]bool{"m3u": true, "xtream": true, "hls": true}}},
		{"PUT", "accounts/1/login", map[string]any{"username": hlsUser, "password": hlsPassword}},
	} {
		if err := a.call(c.method, c.path, c.body, nil); err != nil {
			return nil, fmt.Errorf("%s %s: %w", c.method, c.path, err)
		}
	}
	resp, err := http.Get(s.base + "/player_api.php?" + url.Values{"username": {hlsUser}, "password": {hlsPassword},
		"action": {"get_live_streams"}}.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var list []struct {
		Num      int64 `json:"num"`
		StreamID int64 `json:"stream_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("get_live_streams: %w", err)
	}
	var ids []int64
	for _, c := range list {
		if only == 0 || c.Num <= int64(only) {
			ids = append(ids, c.StreamID)
		}
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("get_live_streams lists no channel up to %d", only)
	}
	return ids, nil
}

// hlsPlayer is a player that watches channels as HLS one after another.
type hlsPlayer struct {
	base, agent string
	ids         []int64
	zap         time.Duration
	rng         *rand.Rand
	stats       *hlsStats
	client      *http.Client
}

func (p *hlsPlayer) run(ctx context.Context) {
	for ctx.Err() == nil {
		d := max(time.Duration(p.rng.ExpFloat64()*float64(p.zap)), 20*time.Second)
		wctx, cancel := context.WithTimeout(ctx, d)
		p.watch(wctx, p.ids[p.rng.IntN(len(p.ids))])
		cancel()
	}
}

// get fetches an address as the player, and returns its body, counting a
// failure that is not the end of ctx.
func (p *hlsPlayer) get(ctx context.Context, address string) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", p.agent)
	resp, err := p.client.Do(req)
	if err != nil {
		if ctx.Err() == nil {
			p.stats.failed.Add(1)
		}
		return nil, false
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || err != nil {
		if ctx.Err() == nil {
			p.stats.failed.Add(1)
		}
		return nil, false
	}
	return b, true
}

// watch plays a channel as HLS until ctx ends: its playlist of renditions,
// then its media playlist once a target duration, and each segment that
// it lists, from the third from its end at first, as players start.
func (p *hlsPlayer) watch(ctx context.Context, id int64) {
	master, ok := p.get(ctx, fmt.Sprintf("%s/live/%s/%s/%d.m3u8", p.base, hlsUser, hlsPassword, id))
	if !ok {
		return
	}
	media := ""
	for _, l := range strings.Split(strings.TrimSpace(string(master)), "\n") {
		if l != "" && !strings.HasPrefix(l, "#") {
			media = p.base + l
		}
	}
	if media == "" {
		p.stats.failed.Add(1)
		return
	}
	dir := strings.TrimSuffix(media, "hls.m3u8")
	seen := map[string]bool{}
	for first := true; ctx.Err() == nil; first = false {
		text, ok := p.get(ctx, media)
		if !ok {
			return
		}
		p.stats.playlists.Add(1)
		target := 4
		var names []string
		for _, l := range strings.Split(string(text), "\n") {
			if v, ok := strings.CutPrefix(l, "#EXT-X-TARGETDURATION:"); ok {
				target, _ = strconv.Atoi(v)
			} else if l != "" && !strings.HasPrefix(l, "#") {
				names = append(names, l)
			}
		}
		if first {
			for _, n := range names[:max(len(names)-3, 0)] {
				seen[n] = true
			}
		}
		for _, n := range names {
			if seen[n] {
				continue
			}
			seen[n] = true
			b, ok := p.get(ctx, dir+n)
			if !ok {
				break
			}
			p.stats.segments.Add(1)
			p.stats.bytes.Add(int64(len(b)))
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(max(target, 1)) * time.Second):
		}
	}
}

// sampleDisk notes how much the buffers under dir take on disk now.
func (st *hlsStats) sampleDisk(dir string) {
	var kb int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				kb += info.Size() / 1024
			}
		}
		return nil
	})
	for {
		old := st.diskKB.Load()
		if kb <= old || st.diskKB.CompareAndSwap(old, kb) {
			return
		}
	}
}

func (st *hlsStats) report(players int) {
	fmt.Printf("HLS: %d players, %d media playlists and %d segments read, %.2f GB, %d failed; the buffers took at most %.1f MB of disk\n",
		players, st.playlists.Load(), st.segments.Load(), float64(st.bytes.Load())/1e9, st.failed.Load(),
		float64(st.diskKB.Load())/1024)
}
