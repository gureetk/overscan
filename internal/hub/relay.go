// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"overscan.gureet.ca/internal/hls"
)

// The relay plays an HLS stream to a player that asked for a continuous
// one (D42). A channel's address does not always say that it is HLS, so
// the hub may hand out a link for MPEG-TS, and a player such as the MAG
// box's ffrt then gets a playlist where it expects a stream: it plays the
// segments listed, about half a minute, and stops. Instead, the relay
// follows the playlist as an HLS player would and sends the segments one
// after another, which makes a single MPEG-TS stream.

const (
	// liveEdgeSegments is how far from the end of a live playlist the relay
	// starts, as HLS players do (RFC 8216, section 6.3.3).
	liveEdgeSegments = 3

	// relayChunk is the size of the pieces that the relay passes from the
	// source to the player, and relayBuffer how many it keeps ahead of the
	// player: up to 8 MiB, enough to ride out a slow segment.
	relayChunk  = 64 << 10
	relayBuffer = 128

	// playlistTimeout caps loading a playlist or a key.
	playlistTimeout = 15 * time.Second

	// segmentIdleTimeout is how long a segment may send nothing before the
	// relay gives up on it.
	segmentIdleTimeout = 20 * time.Second

	// maxEncryptedSegmentBytes caps an encrypted segment, which the relay
	// decrypts whole.
	maxEncryptedSegmentBytes = 64 << 20

	// playlistFailures and segmentFailures are how many loads of the
	// playlist, and how many segments, may fail in a row before the relay
	// ends; a segment that fails is skipped.
	playlistFailures = 3
	segmentFailures  = 5

	// minStall is the least time a live playlist may go without a new
	// segment before the relay ends; the most is four target durations.
	minStall = 30 * time.Second

	// maxMasterDepth is how many master playlists may lead to the media
	// playlist.
	maxMasterDepth = 2

	// maxKeys caps the keys a relay remembers; sources change keys now and
	// then.
	maxKeys = 16
)

// Why a relay ended, other than the player going away.
var (
	errPlaylistEnded = errors.New("the playlist ended")
	errStalled       = errors.New("the source stopped adding segments")
	errPlaylistFails = errors.New("the source's playlist cannot be loaded")
	errSegmentsFail  = errors.New("the source's segments keep failing")
	errUnrelayable   = errors.New("the stream changed to a kind the hub cannot relay")
)

// chunks are the pieces that pass from the relay to the player.
var chunks = sync.Pool{New: func() any { return new([relayChunk]byte) }}

// relay is one player's relay of an HLS stream.
type relay struct {
	h        *Hub
	s        *session
	out      chan<- []byte
	playlist string            // the media playlist's address, as listed
	current  string            // where the source last served it from
	keys     map[string][]byte // by address
	started  bool              // a segment has been chosen
	lastSeq  int64             // the last segment sent
	lastURI  string
	lastNew  time.Time // when a load last found a new segment
}

// relayHLS plays an HLS stream to a player that asked for a continuous one,
// as MPEG-TS. target is the stream's address, base where the source served
// it from, and data its playlist. It reports false, having written nothing,
// if the stream is one that the relay cannot play, with what it has that
// the relay cannot play.
func (h *Hub) relayHLS(ctx context.Context, w http.ResponseWriter, s *session, target string, base *url.URL, data []byte) (bool, string) {
	src := h.source(s.sourceID).Name
	r := &relay{h: h, s: s, keys: map[string][]byte{}}
	pl, err := r.media(ctx, target, base, data)
	if err != nil {
		if ctx.Err() == nil {
			h.log.Warn("cannot load an HLS stream's playlist", "channel", s.channel, "source", src, "error", err)
			http.Error(w, "the source's playlist cannot be loaded", http.StatusBadGateway)
		}
		return true, ""
	}
	if pl.Unsupported != "" {
		return false, pl.Unsupported
	}
	h.log.Info("relaying an HLS stream as one continuous stream", "channel", s.channel, "source", src,
		"live", !pl.Ended, "segments", len(pl.Segments), "target_duration", pl.Target)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	out := make(chan []byte, relayBuffer)
	r.out = out
	done := make(chan error, 1)
	go func() {
		err := r.run(ctx, pl)
		close(out)
		done <- err
	}()

	first, ok := <-out
	if !ok {
		err := <-done
		r.ended(err)
		if ctx.Err() == nil {
			h.noteFailed(s)
			http.Error(w, "the source sent nothing to play", http.StatusBadGateway)
		}
		return true, ""
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(http.StatusOK)
	dst := h.sink(ctx, w, s, true)
	defer dst.close()
	filter := h.repeatFilter(dst, s)
	failed := false
	for c := first; ok; c, ok = <-out {
		_, err := filter.Write(c)
		s.bytes.Add(int64(len(c)))
		if err == nil {
			h.passed(s, c)
		}
		chunks.Put((*[relayChunk]byte)(c[:relayChunk]))
		if err != nil {
			failed = true
			cancel()
			break
		}
		dst.flush()
	}
	if !failed && filter.Close() == nil {
		dst.finish()
	}
	r.ended(<-done)
	return true, ""
}

// ended logs why a relay ended, unless the player went away.
func (r *relay) ended(err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	r.h.log.Info("HLS relay ended", "channel", r.s.channel, "source", r.h.source(r.s.sourceID).Name, "reason", err)
}

// media returns the media playlist to relay, given the first playlist
// loaded: itself, or the variant that a master playlist leads to.
func (r *relay) media(ctx context.Context, target string, base *url.URL, data []byte) (*hls.MediaPlaylist, error) {
	for depth := 0; hls.IsMaster(data); depth++ {
		if depth == maxMasterDepth {
			return &hls.MediaPlaylist{Unsupported: "master playlists nested too deep"}, nil
		}
		v, ok := hls.ParseMaster(data, base).Pick()
		if !ok {
			return &hls.MediaPlaylist{Unsupported: "no variant that carries its own sound"}, nil
		}
		var err error
		target = v.URI
		if data, base, err = r.load(ctx, target); err != nil {
			return nil, err
		}
	}
	r.playlist, r.current = target, base.String()
	return hls.ParseMedia(data, base), nil
}

// load fetches a playlist. It returns the playlist and where the source
// served it from.
func (r *relay) load(ctx context.Context, target string) ([]byte, *url.URL, error) {
	ctx, cancel := context.WithTimeout(ctx, playlistTimeout)
	defer cancel()
	resp, err := r.h.get(ctx, r.s, target)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := readPlaylist(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	if !hls.IsPlaylist(data) {
		return nil, nil, errors.New("the source sent something other than a playlist")
	}
	return data, resp.Request.URL, nil
}

// reload loads the media playlist again: from where the source last served
// it, or from its own address if that fails, since the address a source
// redirects to can expire.
func (r *relay) reload(ctx context.Context) (*hls.MediaPlaylist, error) {
	data, base, err := r.load(ctx, r.current)
	if err != nil && r.current != r.playlist && ctx.Err() == nil {
		data, base, err = r.load(ctx, r.playlist)
	}
	if err != nil {
		return nil, err
	}
	r.current = base.String()
	return hls.ParseMedia(data, base), nil
}

// run sends the stream's segments to r.out, following the playlist, until it
// ends, the source fails, or ctx is done. It returns why it stopped.
func (r *relay) run(ctx context.Context, pl *hls.MediaPlaylist) error {
	loaded := r.h.now()
	r.lastNew = loaded
	failed := 0 // segments in a row
	for {
		fresh := r.fresh(pl)
		if len(fresh) > 0 {
			r.lastNew = loaded
		}
		for _, seg := range fresh {
			r.lastSeq, r.lastURI = seg.Seq, seg.URI
			if err := r.segment(ctx, seg); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				r.h.log.Debug("HLS segment skipped", "channel", r.s.channel, "seq", seg.Seq, "error", err)
				if failed++; failed == segmentFailures {
					return fmt.Errorf("%w: %v", errSegmentsFail, err)
				}
				continue
			}
			failed = 0
		}
		if pl.Ended {
			return errPlaylistEnded
		}
		if r.h.now().Sub(r.lastNew) > max(minStall, 4*pl.Target) {
			return errStalled
		}

		// Wait a target duration from the last load before the next, or
		// half of one if that load had nothing new (RFC 8216, section
		// 6.3.4), then load the playlist again.
		wait := pl.Target
		if len(fresh) == 0 {
			wait /= 2
		}
		if err := r.h.sleep(ctx, loaded.Add(min(max(wait, 500*time.Millisecond), 30*time.Second)).Sub(r.h.now())); err != nil {
			return err
		}
		for attempt := 1; ; attempt++ {
			loaded = r.h.now()
			next, err := r.reload(ctx)
			if err == nil {
				if next.Unsupported != "" {
					return fmt.Errorf("%w: it has %s", errUnrelayable, next.Unsupported)
				}
				pl = next
				break
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			r.h.log.Debug("HLS playlist failed to load", "channel", r.s.channel, "attempt", attempt, "error", err)
			if attempt == playlistFailures {
				return fmt.Errorf("%w: %v", errPlaylistFails, err)
			}
			if err := r.h.sleep(ctx, max(pl.Target/2, 500*time.Millisecond)); err != nil {
				return err
			}
		}
	}
}

// fresh returns the segments of pl that the relay has not sent yet. It
// starts near the live edge, or at the start of a playlist that has ended,
// then follows the media sequence numbers.
func (r *relay) fresh(pl *hls.MediaPlaylist) []hls.Segment {
	segs := pl.Segments
	if len(segs) == 0 {
		return nil
	}
	nearEdge := segs[max(0, len(segs)-liveEdgeSegments):]
	if !r.started {
		r.started = true
		if pl.Ended {
			return segs
		}
		return nearEdge
	}
	last := segs[len(segs)-1]
	switch {
	case last.Seq > r.lastSeq:
		if segs[0].Seq > r.lastSeq+1 {
			// The next segment has gone: the relay fell behind, or the
			// source skipped ahead. Start again near the live edge.
			r.h.log.Debug("HLS relay skipped ahead", "channel", r.s.channel, "from", r.lastSeq, "to", nearEdge[0].Seq)
			return nearEdge
		}
		return segs[r.lastSeq+1-segs[0].Seq:]
	case last.Seq == r.lastSeq && last.URI != r.lastURI:
		// The same number at a new address: either a source whose numbers
		// stay put while its segments move on, or one that puts a new
		// token in every address. Only the first has segments after the
		// last one sent.
		for i, seg := range segs {
			if seg.URI == r.lastURI {
				return segs[i+1:]
			}
		}
	case last.Seq < r.lastSeq:
		// The numbers went back: the source started the stream again.
		r.h.log.Debug("HLS stream restarted by its source", "channel", r.s.channel, "from", r.lastSeq, "to", nearEdge[0].Seq)
		return nearEdge
	}
	return nil
}

// segment sends one segment to the player, decrypted if need be.
func (r *relay) segment(ctx context.Context, seg hls.Segment) error {
	if seg.URI == "" {
		return errors.New("the segment's address is not http or https")
	}
	var key []byte
	if seg.Key != nil {
		var err error
		if key, err = r.key(ctx, seg.Key.URI); err != nil {
			return fmt.Errorf("key: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var resp *http.Response
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var se *statusError
		if resp, err = r.h.get(ctx, r.s, seg.URI); err == nil || ctx.Err() != nil ||
			(errors.As(err, &se) && se.code < 500) {
			break
		}
	}
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body := &idleReader{r: resp.Body, timer: time.AfterFunc(segmentIdleTimeout, cancel)}
	defer body.timer.Stop()
	if key == nil {
		return r.send(ctx, body)
	}
	data, err := io.ReadAll(io.LimitReader(body, maxEncryptedSegmentBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxEncryptedSegmentBytes {
		return errors.New("the encrypted segment is too large")
	}
	plain, err := decrypt(data, key, seg.IV())
	if err != nil {
		return err
	}
	return r.emit(ctx, plain)
}

// send passes a segment's body to the player as it arrives.
func (r *relay) send(ctx context.Context, body io.Reader) error {
	for {
		buf := chunks.Get().(*[relayChunk]byte)
		n, err := io.ReadFull(body, buf[:])
		if n > 0 {
			select {
			case r.out <- buf[:n]:
			case <-ctx.Done():
				return ctx.Err()
			}
		} else {
			chunks.Put(buf)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// emit passes data to the player.
func (r *relay) emit(ctx context.Context, data []byte) error {
	for len(data) > 0 {
		buf := chunks.Get().(*[relayChunk]byte)
		n := copy(buf[:], data)
		data = data[n:]
		select {
		case r.out <- buf[:n]:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// key returns the AES-128 key at uri, fetching it once.
func (r *relay) key(ctx context.Context, uri string) ([]byte, error) {
	if k, ok := r.keys[uri]; ok {
		return k, nil
	}
	ctx, cancel := context.WithTimeout(ctx, playlistTimeout)
	defer cancel()
	resp, err := r.h.get(ctx, r.s, uri)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	k, err := io.ReadAll(io.LimitReader(resp.Body, aes.BlockSize+1))
	if err != nil {
		return nil, err
	}
	if len(k) != aes.BlockSize {
		return nil, fmt.Errorf("the key has %d bytes, not 16", len(k))
	}
	if len(r.keys) == maxKeys {
		clear(r.keys)
	}
	r.keys[uri] = k
	return k, nil
}

// decrypt decrypts an AES-128-CBC segment in place and removes its PKCS#7
// padding. Padding that is not valid is left, since some sources pad
// wrongly, and a few stray bytes at the end of a segment do less harm than
// a lost segment.
func decrypt(data, key, iv []byte) ([]byte, error) {
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("the encrypted segment has %d bytes, not whole blocks", len(data))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(data, data)
	pad := int(data[len(data)-1])
	if pad == 0 || pad > aes.BlockSize {
		return data, nil
	}
	for _, b := range data[len(data)-pad:] {
		if int(b) != pad {
			return data, nil
		}
	}
	return data[:len(data)-pad], nil
}

// idleReader cancels a request whose body sends nothing for a while. Only
// the time spent waiting for the source counts.
type idleReader struct {
	r     io.Reader
	timer *time.Timer
}

func (i *idleReader) Read(p []byte) (int, error) {
	i.timer.Reset(segmentIdleTimeout)
	n, err := i.r.Read(p)
	i.timer.Stop()
	return n, err
}

// sleepContext waits for d, or until ctx is done.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
