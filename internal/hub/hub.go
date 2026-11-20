// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hub plays lineup channels, films and episodes to devices (plan,
// section 7).
//
// A device asks for a link and gets one that carries a signed token, bound to
// the device and the lineup entry, which can start a session for a minute
// (D21). The session then carries reconnects and HLS segments for as long as
// the device keeps playing. The hub proxies every stream, so upstream
// addresses, which often hold credentials, never reach a device (D20), and it
// counts sessions against each source's connection limit. A device that
// starts a new stream on a source at its limit replaces its own older stream
// there; another device's stream is never cut off. A device that asked for
// a continuous stream gets one even if the source answers with HLS: the
// relay makes one of the segments (D42). A film or an episode is a file on
// its source: the hub passes on the player's range requests, so the player
// can seek, one request to the source at a time (D64).
package hub

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math"
	mathrand "math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/media"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/streamprofile"
	"overscan.gureet.ca/internal/subtitles"
)

const (
	// linkTTL is how long a new link can start a session.
	linkTTL = time.Minute

	// idleTimeout is how long an HLS session stays active without a request;
	// players fetch a live playlist every few seconds.
	idleTimeout = 30 * time.Second

	// reconnectGrace is how long a session that has stopped keeps its
	// token valid, so that a player can reconnect after the link expired.
	reconnectGrace = 30 * time.Second

	// fileGrace is how long a film's or an episode's session that has
	// stopped keeps its token valid: a player that pauses can close its
	// connection, and opens another when it goes on (D64). Until then the
	// session holds none of its source's connections.
	fileGrace = 6 * time.Hour

	// sweepInterval is how often ended sessions are cleared away.
	sweepInterval = 15 * time.Second

	// unrelayableFor is how long a channel whose HLS the relay could not
	// follow gets HLS links, when it would otherwise be relayed to be
	// repaired (D61).
	unrelayableFor = 6 * time.Hour
)

// Source is what the hub needs to know about a source.
type Source struct {
	Name           string
	MaxConnections int    // 0 for no limit
	UserAgent      string // "" for the hub's default
	// Resolves says that the source gives a link for each stream as it
	// starts, as a Stalker portal does (D76): what the store keeps for its
	// channels is what the hub's Resolver turns into an address.
	Resolves bool
}

// Resolver gives the addresses of the streams of sources that give a link
// for each stream as it starts (Source.Resolves).
type Resolver interface {
	// Resolve returns the address that plays ref, which is what the store
	// keeps for one of the source's channels, films or episodes, as kind
	// (PlaysChannel, PlaysMovie or PlaysEpisode) says. An error that wraps
	// ErrLimit says that the source plays as many streams as it may.
	Resolve(ctx context.Context, sourceID int64, kind, ref string) (Resolved, error)
}

// Resolved is the address of a stream that a Resolver gave.
type Resolved struct {
	Address string
	// Hold, if not nil, keeps the source's session going while the stream
	// plays, until release is called.
	Hold func() (release func())
}

// resolveWait is how long a Resolver may take to give a stream's address.
const resolveWait = 20 * time.Second

// Hub mints links and proxies the streams they play. It is an http.Handler
// for the /live/ paths.
type Hub struct {
	store  *store.Store
	log    *slog.Logger
	client *http.Client
	// transcodes asks sources' servers for the streams they transcode
	// (D71), which can take a while to start.
	transcodes *http.Client
	key        []byte
	userAgent  string
	sources    atomic.Pointer[map[int64]Source]
	now        func() time.Time
	sleep      func(context.Context, time.Duration) error // waits, or fails when the context is done

	ffmpeg         string // repairs streams (D57), and converts films and episodes (D68); "" if none
	repair         Repair // which streams it repairs
	repairsRunning atomic.Int32

	ffprobe         string // reads what the files of films and episodes hold (D68); "" if none
	convertsRunning atomic.Int32
	inputs          inputServer // where ffmpeg and ffprobe read files

	// resolver gives the addresses of the streams of sources that resolve
	// (D76); nil if none.
	resolver Resolver

	// encoder is what ffmpeg can do to transcode video that a box cannot
	// play (D73); encodes counts its transcodes running, by device.
	encoder  Encoder
	encodeMu sync.Mutex
	encodes  map[int64]int

	// shiftDir holds the timeshift buffers of boxes' live channels (D92);
	// "" if there are none. tuners are the buffers' tuners that run.
	shiftDir string
	tuners   sync.WaitGroup

	// players are the players that the outputs serve, by account, address
	// and user agent, and by device ID (D103).
	playersMu  sync.Mutex
	players    map[playerKey]*Player
	playerByID map[int64]*Player
	nextPlayer int64

	mu          sync.Mutex
	sessions    map[uint64]*session // by token nonce
	unrelayable map[int64]time.Time // channels whose HLS the relay could not follow, by ID, until when (D61)
	shared      map[int64]*session  // the shared tuners that run, by channel ID (D93)
	failing     map[int64]time.Time // channels whose streams failed to start, by ID, until when (D109)
}

// New returns a hub for the given sources, keyed by store ID. Its signing
// key is random and lives only in memory, so links stop working when the
// server restarts; clients then ask for a new one. Upstream requests carry
// Overscan/version as the user agent unless a source names another.
func New(st *store.Store, log *slog.Logger, version string, sources map[int64]Source) *Hub {
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       90 * time.Second,
	}
	slow := transport.Clone()
	slow.ResponseHeaderTimeout = transcodeStartTimeout
	h := &Hub{
		store:      st,
		log:        log,
		client:     &http.Client{Transport: transport}, // no overall timeout: live streams run on
		transcodes: &http.Client{Transport: slow},
		key:        key,
		userAgent:  DefaultUserAgent(version),
		now:        time.Now,
		sleep:      sleepContext,
		sessions:   map[uint64]*session{},
		encodes:    map[int64]int{},

		unrelayable: map[int64]time.Time{},
		shared:      map[int64]*session{},
		failing:     map[int64]time.Time{},
		players:     map[playerKey]*Player{},
		playerByID:  map[int64]*Player{},
	}
	h.SetSources(sources)
	return h
}

// UseResolver makes the hub ask r for the addresses of the streams of
// sources that resolve (D76). Call it before the hub serves.
func (h *Hub) UseResolver(r Resolver) { h.resolver = r }

// SetSources replaces what the hub knows about the sources, keyed by store
// ID, as when the admin changes them. Streams playing go on.
func (h *Hub) SetSources(sources map[int64]Source) { h.sources.Store(&sources) }

// source returns what the hub knows about a source.
func (h *Hub) source(id int64) Source { return (*h.sources.Load())[id] }

// DefaultUserAgent is the user agent of upstream requests for a source
// that names none.
func DefaultUserAgent(version string) string { return "Overscan/" + version }

// Link returns the path of a new link that plays a lineup entry for a
// device. It ends in .m3u8 if the stream is HLS, which some players need to
// know before they fetch it, and in .ts otherwise. A repaired channel's HLS
// gets a .ts link while the server repairs HLS streams (D61): the relay then
// makes it one continuous stream, which can go through ffmpeg.
func (h *Hub) Link(deviceID int64, entry store.PlayableEntry) string {
	link, _ := h.link(deviceID, entry, false)
	return link
}

// ShiftLink returns, as Link does, the path of a new link that plays a
// lineup entry for a box, and reports whether it plays through a timeshift
// buffer while timeshift is on (D92). An HLS link does not, nor does any
// while the hub has no folder for buffers.
func (h *Hub) ShiftLink(deviceID int64, entry store.PlayableEntry) (string, bool) {
	return h.link(deviceID, entry, true)
}

func (h *Hub) link(deviceID int64, entry store.PlayableEntry, shift bool) (string, bool) {
	c := claims{
		entryID:  entry.EntryID,
		deviceID: deviceID,
		expires:  h.now().Add(linkTTL).Unix(),
		nonce:    mathrand.Uint64(),
	}
	// A source that resolves gives the address only when the stream
	// starts, so its links are continuous, which the relay makes of HLS.
	ext := ".ts"
	if !h.source(entry.SourceID).Resolves && hlsAddress(entry.StreamURL) && !h.relaysToRepair(entry) &&
		!h.transcodesLive(deviceID) {
		ext = ".m3u8"
	}
	c.shift = shift && ext == ".ts" && h.shiftDir != ""
	return "/live/" + h.sign(c) + ext, c.shift
}

// hlsAddress reports whether a stream's address says that it is HLS.
func hlsAddress(address string) bool {
	u, err := url.Parse(address)
	return err == nil && strings.EqualFold(path.Ext(u.Path), ".m3u8")
}

// relaysToRepair reports whether a channel whose address is HLS gets a
// continuous link, so that the relay makes its stream one that ffmpeg can
// repair (D61): the server repairs HLS streams, the hub repairs this
// channel, and its HLS has not lately turned out to be of a kind that the
// relay cannot follow.
func (h *Hub) relaysToRepair(entry store.PlayableEntry) bool {
	if !entry.RepairHLS || !h.repairsChannel(entry.Repair) {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	until, found := h.unrelayable[entry.ChannelID]
	if found && !h.now().Before(until) {
		delete(h.unrelayable, entry.ChannelID)
		found = false
	}
	return !found
}

// relayingToRepair reports whether s plays an HLS address as a continuous
// stream so that it can be repaired (D61).
func (h *Hub) relayingToRepair(s *session) bool {
	return s.repairHLS && hlsAddress(s.streamURL) && h.repairs(s)
}

// cannotRelay remembers that the relay cannot follow the HLS of s's channel,
// so that its links are HLS again for a while, and logs it.
func (h *Hub) cannotRelay(s *session, unsupported string) {
	h.mu.Lock()
	h.unrelayable[s.channelID] = h.now().Add(unrelayableFor)
	h.mu.Unlock()
	h.log.Warn("cannot make an HLS stream one stream to repair it; the channel plays as HLS, unrepaired, from its next start",
		"channel", s.channel, "source", h.source(s.sourceID).Name, "reason", "it has "+unsupported, "for", unrelayableFor)
}

// Available reports whether a source can take a new stream for a device:
// it has no limit, it is under its limit, or the device's own streams there
// would make room.
func (h *Hub) Available(sourceID, deviceID int64) bool {
	limit := h.source(sourceID).MaxConnections
	if limit == 0 {
		return true
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	others := 0
	for _, s := range h.sessions {
		if s.sourceID == sourceID && h.holding(s, now) && !h.owns(s, deviceID, now) {
			others++
		}
	}
	return others < limit
}

// What a stream plays, as Stream.Kind says it.
const (
	PlaysChannel = "live"    // a lineup entry
	PlaysMovie   = "movie"   // a film
	PlaysEpisode = "episode" // an episode of a series
	PlaysArchive = "archive" // a past programme of a lineup entry, from its source's archive (D81)
)

// Stream is a session as the admin shows it.
type Stream struct {
	Kind      string // PlaysChannel, PlaysMovie, PlaysEpisode or PlaysArchive
	DeviceID  int64
	AccountID int64 // the account of the device or player, or of the box that asked for a recording; 0 if none (D102)
	// Player names the player of an output that plays it (D103), "" for a
	// device's stream.
	Player    string
	EntryID   int64 // the lineup entry that plays, or whose past programme plays; 0 for a film or an episode
	ItemID    int64 // the film's title ID or the episode's ID, 0 for a channel
	SourceID  int64
	Source    string // the source's name
	Channel   string // the channel's name, as devices see it, or the film's or episode's
	Started   time.Time
	Bytes     int64 // taken from the source so far
	HLS       bool  // the source sends HLS
	Repaired  bool  // it goes through ffmpeg (D57)
	Converted bool  // a film or an episode that goes through ffmpeg (D68)
	// Transcoded is a film or an episode whose video is transcoded, which
	// TranscodedBy says: its source's server (D71), or this server's
	// ffmpeg (D73).
	Transcoded   bool
	TranscodedBy string // TranscodedBySource or TranscodedByOverscan; "" if not transcoded
	Playing      bool   // a player is fetching it now; otherwise it waits for one to come back
	// Timeshift is a box's live channel that plays through a timeshift
	// buffer (D92).
	Timeshift bool
	// Shared is a channel that plays from a shared tuner, whose one
	// connection to the source serves every screen that plays the channel
	// (D93).
	Shared bool
	// Programme is, for a past programme, its title, and Aired when it
	// began.
	Programme string
	Aired     time.Time
}

// Who transcodes a stream's video (Stream.TranscodedBy).
const (
	TranscodedBySource   = "source"
	TranscodedByOverscan = "overscan"
)

// Streams returns the sessions that have not ended, oldest first.
func (h *Hub) Streams() []Stream {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	out := make([]Stream, 0, len(h.sessions))
	for _, s := range h.sessions {
		if s.ended || s.shared {
			continue // a shared tuner shows as its screens
		}
		// A screen on a shared tuner shows what the tuner takes from the
		// source.
		from := s
		if s.tuner != nil {
			from = s.tuner
		}
		st := Stream{
			Kind: PlaysChannel, DeviceID: s.deviceID, AccountID: s.account, EntryID: s.entryID, SourceID: s.sourceID, Source: h.source(s.sourceID).Name,
			Channel: s.channel, Started: s.started, Bytes: from.bytes.Load(), HLS: from.hls, Repaired: from.repairing.Load() > 0,
			Converted: s.converting.Load() > 0, Playing: s.active(now), Timeshift: s.shift != nil || s.canShift,
			Shared: s.tuner != nil,
		}
		if IsPlayer(s.deviceID) {
			st.Player = "A player"
			if p, ok := h.PlayerOf(s.deviceID); ok {
				st.Player = p.Describe()
			}
		}
		switch {
		case s.transcoding.Load() > 0:
			st.Transcoded, st.TranscodedBy = true, TranscodedBySource
		case s.encoding.Load() > 0:
			st.Transcoded, st.TranscodedBy = true, TranscodedByOverscan
		}
		switch s.kind {
		case kindEntry:
		case kindArchive:
			st.Kind, st.Programme, st.Aired = PlaysArchive, s.programme, s.aired
		default:
			st.Kind, st.EntryID, st.ItemID = playsKind(s.kind), 0, s.entryID
		}
		out = append(out, st)
	}
	slices.SortFunc(out, func(a, b Stream) int {
		if c := a.Started.Compare(b.Started); c != 0 {
			return c
		}
		return cmp.Compare(a.EntryID, b.EntryID)
	})
	return out
}

// Run clears ended sessions away until ctx is done, and then stops serving
// files to ffmpeg and ffprobe.
func (h *Hub) Run(ctx context.Context) {
	t := time.NewTicker(sweepInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			h.closeInputs()
			// Timeshift buffers' tuners stop, and their buffers go (D92).
			h.mu.Lock()
			for _, s := range h.sessions {
				if s.shift != nil {
					h.end(s, "the server stopped")
				}
			}
			h.mu.Unlock()
			h.tuners.Wait()
			return
		case <-t.C:
			h.mu.Lock()
			h.sweep(h.now())
			h.mu.Unlock()
			h.stopEndedPlayers(ctx)
		}
	}
}

// playsKind names a kind of token as Stream.Kind does.
func playsKind(kind byte) string {
	switch kind {
	case kindMovie:
		return PlaysMovie
	case kindEpisode:
		return PlaysEpisode
	}
	return PlaysChannel
}

// session is a device playing a lineup entry, a film or an episode. Fields
// below mu are guarded by Hub.mu.
type session struct {
	nonce     uint64
	token     string
	kind      byte // kindEntry, kindMovie, kindEpisode or kindArchive
	deviceID  int64
	entryID   int64 // the lineup entry, or the film's title ID or the episode's ID
	channelID int64
	sourceID  int64
	channel   string
	streamURL string
	// folder and file are, for a film or an episode in a folder, the
	// folder's path and the video's path in it (D67).
	folder string
	file   string
	// mediaKey is, for a film or an episode, the store's key for what its
	// file holds; info is what it holds, if known, and plan, for one that
	// plays through ffmpeg, how it does (D68).
	mediaKey   [2]int64
	info       atomic.Pointer[media.Info]
	plan       atomic.Pointer[media.Plan]
	converting atomic.Int32 // its requests going through ffmpeg now
	changed    atomic.Bool  // its file turned out to have changed
	// transcodeURL is, for a film or an episode whose source's server
	// transcodes, where it does (D71); transcoding counts its requests
	// for such streams now.
	transcodeURL string
	transcoding  atomic.Int32
	// encoding counts its requests whose video this server's ffmpeg
	// transcodes now (D73).
	encoding atomic.Int32
	// subtitles are, for a film or an episode, the subtitle tracks that its
	// device may load (D69), and cues what was read of them, by index.
	subtitles atomic.Pointer[[]subtitles.Track]
	cuesMu    sync.Mutex
	cues      map[int][]subtitles.Cue

	// ref is, for a source that resolves (D76), what the store keeps for
	// what it plays, which the Resolver turns into an address, and refKind
	// what it plays, as Stream.Kind says it; release lets go of the
	// source's session, nil if none. A film's or an episode's address can
	// lapse while it plays, and readdress is then the one that the
	// Resolver gave again (D77).
	ref       string
	refKind   string
	release   func()
	readdress atomic.Pointer[string]

	// archive is, for a past programme (D81), what makes the addresses at
	// which its source's archive plays it; programme is its title, and
	// aired when it began.
	archive   *catchup.Archive
	programme string
	aired     time.Time

	// shift is, for a box's live channel while timeshift is on, the
	// buffer that it plays through (D92), and for a shared tuner, the
	// buffer that its screens play from (D93); nil otherwise.
	shift *shift

	// tuner is, for a screen that plays a channel from a shared tuner
	// (D93), the tuner's own session, whose buffer it plays from; nil
	// otherwise. canShift says that the screen may go back in that
	// buffer, as a box's link may while timeshift is on (D92).
	tuner    *session
	canShift bool
	// shared marks a shared tuner's own session (D93): it plays its
	// channel from the source into its buffer for the screens in screens.
	shared bool

	// account is the account that the device's streams count against,
	// and accountLimit that account's limit of streams at once, 0 for
	// none (D102).
	account      int64
	accountLimit int

	started   time.Time
	repair    bool // its channel is set to be repaired (D57)
	repairHLS bool // the server repairs HLS streams too (D61)
	ctx       context.Context
	cancel    context.CancelFunc
	bytes     atomic.Int64
	repairing atomic.Int32 // its requests going through ffmpeg now
	// profile is, for a live channel, the output profile that its device
	// or player gets it through (D110).
	profile streamprofile.Profile

	// mu: Hub.mu
	lastActive time.Time
	streams    int  // requests in flight
	hls        bool // the stream turned out to be HLS
	ended      bool
	resources  map[int]string // HLS segments, playlists and keys by index
	indexes    map[string]int // and back
	next       int
	turn       *fileTurn  // a film's or an episode's request to its source now, nil if none
	screens    []*session // for a shared tuner, the screens that play from it (D93)
}

// fileTurn is a request of a film's or an episode's session to its source.
// A session makes one at a time: a new one, as when the player seeks, ends
// the one before and waits until it is done (D64).
type fileTurn struct {
	cancel context.CancelFunc
	done   chan struct{}

	mu sync.Mutex
	// rc is the answer to the request that the turn serves, while it runs;
	// nil for none.
	rc *http.ResponseController
}

// newTurn returns a turn that cancel ends, serving the request that w
// answers, if any.
func newTurn(cancel context.CancelFunc, w http.ResponseWriter) *fileTurn {
	t := &fileTurn{cancel: cancel, done: make(chan struct{})}
	if w != nil {
		t.rc = http.NewResponseController(w)
	}
	return t
}

// end ends t: its context, and a write of its answer that waits for a
// client that has stopped reading, as ffmpeg does with its request before
// once it asks for another part of the file.
func (t *fileTurn) end() {
	t.cancel()
	t.mu.Lock()
	if t.rc != nil {
		_ = t.rc.SetWriteDeadline(time.Now())
	}
	t.mu.Unlock()
}

// finish marks t over, once its request is done.
func (t *fileTurn) finish() {
	t.mu.Lock()
	t.rc = nil
	t.mu.Unlock()
	close(t.done)
}

// buffer returns the buffer that s plays from: its own timeshift buffer
// (D92), its shared tuner's (D93), or nil.
func (s *session) buffer() *shift {
	if s.tuner != nil {
		return s.tuner.shift
	}
	return s.shift
}

// grace returns how long s keeps its token valid after it stops.
func (s *session) grace() time.Duration {
	if s.kind != kindEntry {
		return fileGrace
	}
	return reconnectGrace
}

// what returns the log's name for what s plays: channel, for a channel or
// one of its past programmes, or title.
func (s *session) what() string {
	if s.kind == kindEntry || s.kind == kindArchive {
		return "channel"
	}
	return "title"
}

// active reports whether s holds one of its source's connections: it is
// streaming, or it is HLS and played recently.
func (s *session) active(now time.Time) bool {
	if s.ended {
		return false
	}
	// A box keeps its timeshift buffer while it is paused with its
	// requests' holds (D92), and so does a box that can go back in a
	// shared tuner's (D93).
	return s.streams > 0 || (s.hls && now.Sub(s.lastActive) < idleTimeout) ||
		((s.shift != nil || s.canShift) && now.Sub(s.lastActive) < reconnectGrace)
}

// holds reports whether s holds one of its source's connections: a player
// plays it, or its timeshift buffer's tuner does (D92).
func (s *session) holds(now time.Time) bool {
	return s.active(now) || (!s.ended && s.shift != nil && s.shift.isTuning())
}

// sweep ends the sessions that stopped longer than reconnectGrace ago. It
// needs h.mu.
func (h *Hub) sweep(now time.Time) {
	for _, s := range h.sessions {
		switch {
		case s.shared:
			// A shared tuner ends with its last screen (D93).
			if len(s.screens) == 0 {
				h.end(s, "no screen plays it any more")
			}
		case !s.active(now) && now.Sub(s.lastActive) > s.grace():
			h.end(s, "stopped")
		}
	}
}

// end ends s, stopping any of its requests still running. It needs h.mu.
func (h *Hub) end(s *session, reason string) {
	if s.ended {
		return
	}
	s.ended = true
	s.cancel()
	if s.release != nil {
		s.release()
	}
	if s.shift != nil {
		s.shift.close()
	}
	delete(h.sessions, s.nonce)
	if s.shared {
		// A shared tuner's screens end with it (D93).
		if h.shared[s.channelID] == s {
			delete(h.shared, s.channelID)
		}
		for _, v := range slices.Clone(s.screens) {
			h.end(v, reason)
		}
		h.log.Info("a shared channel's stream ended", "channel", s.channel, "source", h.source(s.sourceID).Name,
			"reason", reason, "duration", h.now().Sub(s.started).Round(time.Second), "bytes", s.bytes.Load())
		return
	}
	h.log.Info("stream ended", s.what(), s.channel, "source", h.source(s.sourceID).Name, "device", s.deviceID,
		"reason", reason, "duration", h.now().Sub(s.started).Round(time.Second), "bytes", s.bytes.Load())
	if t := s.tuner; t != nil {
		// The tuner ends with its last screen.
		t.screens = slices.DeleteFunc(t.screens, func(v *session) bool { return v == s })
		if len(t.screens) == 0 && !t.ended {
			h.end(t, "no screen plays it any more")
		}
	}
}

// admit returns the session that c starts, opening it as s says if it is
// not open. On a source at its limit, the device's own oldest streams there
// end to make room.
func (h *Hub) admit(c claims, s *session) (*session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.sweep(now)
	if open, ok := h.sessions[c.nonce]; ok && !open.shared {
		return open, nil
	}
	if !h.accountRoom(s.account, s.accountLimit, c.deviceID, now) {
		return nil, ErrAccountLimit
	}
	if !h.makeRoom(s.sourceID, c.deviceID, now) {
		return nil, ErrLimit
	}
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.nonce, s.kind, s.deviceID, s.entryID = c.nonce, c.kind, c.deviceID, c.entryID
	s.started, s.lastActive = now, now
	s.resources, s.indexes = map[int]string{}, map[string]int{}
	h.sessions[c.nonce] = s
	h.log.Info("stream started", s.what(), s.channel, "source", h.source(s.sourceID).Name, "device", s.deviceID)
	if s.repair && h.ffmpeg == "" && h.repair == RepairSet {
		h.log.Warn("the channel is set to be repaired, but there is no ffmpeg to repair it with; it plays as it is",
			"channel", s.channel, "source", h.source(s.sourceID).Name)
	}
	return s, nil
}

// makeRoom reports whether a source can take a new stream for a device,
// ending the device's own oldest streams there if the source is at its
// limit. It needs h.mu.
func (h *Hub) makeRoom(sourceID, deviceID int64, now time.Time) bool {
	limit := h.source(sourceID).MaxConnections
	if limit == 0 {
		return true
	}
	// A shared tuner that no screen plays from holds its connection for no
	// one (D93).
	h.endIdleTuners(sourceID, now)
	count := 0
	var own *session
	for _, o := range h.sessions {
		if o.sourceID != sourceID || !h.holding(o, now) {
			continue
		}
		count++
		if h.owns(o, deviceID, now) && (own == nil || o.started.Before(own.started)) {
			own = o
		}
	}
	for count >= limit && own != nil {
		h.end(own, "replaced by a new stream from the same device")
		count--
		own = nil
		for _, o := range h.sessions {
			if o.sourceID == sourceID && h.holding(o, now) && h.owns(o, deviceID, now) &&
				(own == nil || o.started.Before(own.started)) {
				own = o
			}
		}
	}
	return count < limit
}

// roomFor reports whether a source can take a new stream for a device,
// ending the device's own streams there that the source needs to make room
// for it. A source that resolves (D76) gets room before it is asked for an
// address, as a box stops one stream before it asks for the next.
func (h *Hub) roomFor(sourceID, deviceID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.sweep(now)
	return h.makeRoom(sourceID, deviceID, now)
}

// resolve asks the Resolver for the address that plays ref, from a source
// that resolves (D76).
func (h *Hub) resolve(ctx context.Context, sourceID int64, kind, ref string) (Resolved, error) {
	if h.resolver == nil {
		return Resolved{}, errors.New("the server cannot play this source's streams")
	}
	ctx, cancel := context.WithTimeout(ctx, resolveWait)
	defer cancel()
	res, err := h.resolver.Resolve(ctx, sourceID, kind, ref)
	if err == nil && res.Address == "" {
		err = errors.New("the source gave no address")
	}
	return res, err
}

// address returns the address that s plays from now.
func (s *session) address() string {
	if a := s.readdress.Load(); a != nil {
		return *a
	}
	return s.streamURL
}

// ErrNoLink is why a stream of a source that resolves cannot start: the
// source gave no address for it (D76).
var ErrNoLink = errors.New("the source gave no address for it")

// openItem returns the session that c starts for a film or an episode,
// opening it if it is not open. On a source that resolves (D77), the
// device's own streams there first make room if they must, the Resolver
// gives the address, and the session holds the source's while it plays. It
// fails with ErrLimit, or with ErrNoLink and the Resolver's reason.
func (h *Hub) openItem(ctx context.Context, c claims, token string, item store.PlayableItem) (*session, error) {
	s := itemSession(token, item)
	s.account, s.accountLimit = h.viewer(ctx, c.deviceID)
	if h.source(item.SourceID).Resolves {
		if open := h.lookup(c.nonce); open != nil {
			return open, nil
		}
		if !h.roomFor(item.SourceID, c.deviceID) {
			return nil, ErrLimit
		}
		kind := PlaysMovie
		if item.Kind == store.KindEpisode {
			kind = PlaysEpisode
		}
		res, err := h.resolve(ctx, item.SourceID, kind, item.StreamURL)
		if errors.Is(err, ErrLimit) {
			return nil, err
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrNoLink, err)
		}
		s.streamURL, s.ref, s.refKind = res.Address, item.StreamURL, kind
		if res.Hold != nil {
			s.release = res.Hold()
		}
	}
	open, err := h.admit(c, s)
	if open != s && s.release != nil {
		// Refused, or another request opened the session first.
		s.release()
	}
	return open, err
}

// resolveFailed answers a request whose stream the Resolver gave no
// address for, and logs why.
func (h *Hub) resolveFailed(w http.ResponseWriter, r *http.Request, what, name string, sourceID, deviceID int64, err error) {
	src := h.source(sourceID)
	switch {
	case r.Context().Err() != nil:
	case errors.Is(err, ErrAccountLimit):
		h.refused(w, err, what, name, sourceID, deviceID)
	case errors.Is(err, ErrLimit):
		args := []any{what, name, "source", src.Name, "limit", src.MaxConnections, "device", deviceID}
		if err != ErrLimit {
			args = append(args, "error", err) // the source's own word
		}
		h.log.Warn("stream refused: the source is at its connection limit", args...)
		http.Error(w, "the source has no free connection", http.StatusServiceUnavailable)
	default:
		h.log.Warn("the source gave no address for a stream", what, name, "source", src.Name, "device", deviceID,
			"error", err)
		http.Error(w, "the source gave no address for this stream", http.StatusBadGateway)
	}
}

// lookup returns the live session with the given nonce, or nil. A session
// that stopped longer than reconnectGrace ago is gone, even if no sweep has
// cleared it yet.
func (h *Hub) lookup(nonce uint64) *session {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sweep(h.now())
	if s, ok := h.sessions[nonce]; ok && !s.ended && !s.shared {
		return s
	}
	return nil
}

// ServeHTTP serves /live/<token>.ts or .m3u8, which starts or resumes a
// session, and /live/<token>/r/<n>, an HLS resource of a session. A .ts
// link asks for a continuous stream, which the relay makes of an HLS one.
// It serves /vod/<token>.<ext>, a film or an episode, and
// /archive/<token>.ts, a past programme, too.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if rest, ok := strings.CutPrefix(r.URL.Path, "/vod/"); ok {
		h.serveItem(w, r, rest)
		return
	}
	if rest, ok := strings.CutPrefix(r.URL.Path, "/archive/"); ok {
		h.serveArchive(w, r, rest)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/live/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	first, resource, isResource := strings.Cut(rest, "/")
	token := strings.TrimSuffix(strings.TrimSuffix(first, ".ts"), ".m3u8")
	c, ok := h.verify(token)
	if !ok || c.kind != kindEntry {
		http.Error(w, "invalid link", http.StatusForbidden)
		return
	}

	if isResource {
		n, ok := resourceIndex(resource)
		s := h.lookup(c.nonce)
		if !ok || s == nil {
			http.Error(w, "this stream has ended", http.StatusNotFound)
			return
		}
		h.mu.Lock()
		target, found := s.resources[n]
		h.mu.Unlock()
		if !found {
			http.Error(w, "no such segment", http.StatusNotFound)
			return
		}
		h.proxy(w, r, s, target, false)
		return
	}

	continuous := !strings.HasSuffix(first, ".m3u8")
	s := h.lookup(c.nonce)
	if q := r.URL.Query(); q.Get("hold") != "" || q.Get("stop") != "" {
		h.holdOrStop(w, s, q.Get("stop") != "")
		return
	}
	if s != nil && s.buffer() != nil {
		h.serveShift(w, r, s)
		return
	}
	target := ""
	if s != nil {
		target = s.streamURL
	}
	if s != nil && continuous {
		// Players reconnect to a continuous stream on their own when it
		// ends or stalls, which the log should show.
		h.log.Info("a player reconnected to a stream", "channel", s.channel, "source", h.source(s.sourceID).Name,
			"device", s.deviceID, "range", r.Header.Get("Range"))
		h.mu.Lock()
		hls := s.hls
		h.mu.Unlock()
		if s.ref != "" && !hls {
			// A source's link can lapse once played, so a new
			// connection gets a new one, as a box's does. A playlist
			// that the player fetches again keeps the one it has.
			res, err := h.resolve(r.Context(), s.sourceID, PlaysChannel, s.ref)
			if err != nil {
				h.resolveFailed(w, r, "channel", s.channel, s.sourceID, s.deviceID, err)
				return
			}
			target = res.Address
		}
	}
	if s == nil {
		if h.now().Unix() > c.expires {
			http.Error(w, "this link has expired; ask for a new one", http.StatusForbidden)
			return
		}
		entry, err := h.store.PlayableEntry(r.Context(), c.entryID)
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "this channel is not available", http.StatusNotFound)
			return
		}
		if err != nil {
			h.log.Error("cannot look up a channel to play", "entry", c.entryID, "error", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		h.openChannel(w, r, c, token, entry, continuous)
		return
	}
	h.proxy(w, r, s, target, continuous)
}

// holdOrStop answers ?hold=1 on a live link, which keeps its session while
// its box is paused (D92), and ?stop=1, which ends it now.
func (h *Hub) holdOrStop(w http.ResponseWriter, s *session, stop bool) {
	if s == nil {
		if stop {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Error(w, "this stream has ended", http.StatusNotFound)
		return
	}
	h.mu.Lock()
	if stop {
		h.end(s, "the box stopped it")
	} else {
		s.lastActive = h.now()
	}
	h.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

// resourceIndex parses "r/<n>" with an optional extension after n.
func resourceIndex(s string) (int, bool) {
	s, ok := strings.CutPrefix(s, "r/")
	if !ok {
		return 0, false
	}
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0 && n < math.MaxInt32
}

// begin and finish bracket a request of s to upstream.
func (h *Hub) begin(s *session) {
	h.mu.Lock()
	s.streams++
	s.lastActive = h.now()
	h.mu.Unlock()
}

func (h *Hub) finish(s *session) {
	h.mu.Lock()
	s.streams--
	s.lastActive = h.now()
	h.mu.Unlock()
}

// fetchError describes an upstream failure without the address, which a
// url.Error quotes and which can hold credentials.
func fetchError(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return fmt.Errorf("fetch: %w", uerr.Err)
	}
	return err
}
