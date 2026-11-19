// SPDX-License-Identifier: AGPL-3.0-or-later

// Package guide reads each source's guide into the store (D51): an Xtream
// server's xmltv.php with the source's login, or the XMLTV guide that an M3U
// playlist names (url-tvg). It matches the guide's channels to the source's
// by guide ID, then by name, keeps the programmes that devices can be shown,
// and writes them as a new generation, so that the guide devices see changes
// all at once. A Stalker portal's guide comes through its API, for the
// portal's own channels (D76).
//
// A guide's address can hold credentials, so no error or log line from
// this package quotes it.
package guide

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/xmltv"
)

// Defaults and limits.
const (
	// DefaultInterval is how often a source's guide is read.
	DefaultInterval = 12 * time.Hour
	// RetryAfter is how long a source whose guide could not be read waits
	// before the next try.
	RetryAfter = time.Hour
	// Ahead is how far ahead programmes are kept.
	Ahead = 14 * 24 * time.Hour
	// Behind is how long a programme is kept after it ends, at least: a
	// day, or the channel's catch-up days where it has more.
	Behind = 24 * time.Hour

	maxGuideBytes = 1 << 30   // of XML, after any gzip
	maxProgrammes = 2_000_000 // stored from one read
	fetchTimeout  = 15 * time.Minute

	// matching names how a guide's channels are matched to a source's. A
	// guide read by another matching is read again at the next refresh, so
	// that a change to it applies after an upgrade, not 12 hours later.
	matching = "2"

	// StalkerHours is how many hours ahead a Stalker portal's guide is
	// asked for, and StalkerInterval how often it is read, so that the
	// guide always reaches at least a day ahead (Assumed: the notes give
	// no period).
	StalkerHours    = 30
	StalkerInterval = 6 * time.Hour
)

// Reader reads guides.
type Reader struct {
	store     *store.Store
	log       *slog.Logger
	client    *http.Client
	userAgent string
	now       func() time.Time
	// Interval is how often a source's guide is read; a Stalker portal's
	// is read at least every StalkerInterval.
	Interval time.Duration
	// stalker holds the sessions of Stalker portals (D76).
	stalker *stalker.Pool

	// Overridden by tests.
	maxBytes int64
}

// New returns a reader that stores guides in st. It fetches them as
// Overscan/version unless a source names another user agent.
func New(st *store.Store, log *slog.Logger, version string) *Reader {
	return &Reader{
		store:     st,
		log:       log,
		client:    &http.Client{Timeout: fetchTimeout},
		userAgent: "Overscan/" + version,
		now:       time.Now,
		Interval:  DefaultInterval,
		stalker:   stalker.NewPool(nil),
		maxBytes:  maxGuideBytes,
	}
}

// UseStalker makes the reader read Stalker portals' guides in the sessions
// of pool, which the stream hub shares (D76).
func (g *Reader) UseStalker(pool *stalker.Pool) { g.stalker = pool }

// Result is what a read of a guide stored.
type Result struct {
	Channels   int // the source's channels it found programmes for
	Programmes int
	Skipped    int // programmes it could not read, or that were too many
	Listed     int // the channels that the guide describes
	ByName     int // the source's channels that it matched by name
}

// Address returns the address of a source's guide: an Xtream server's
// xmltv.php with the source's login, or for a playlist the guide it names,
// listed, which may be "". A Stalker portal's guide comes through its API,
// so its address names the portal and the box, whose guide it is. It
// returns "" for a source without a guide, such as a Jellyfin server, which
// has no channels.
func Address(src source.Settings, listed string) string {
	switch src.Type {
	case source.TypeXtream:
		q := url.Values{"username": {src.Username}, "password": {src.Password}}
		return strings.TrimRight(src.URL, "/") + "/xmltv.php?" + q.Encode()
	case source.TypeM3U:
		return listed
	case source.TypeStalker:
		return "stalker " + src.URL + " " + strings.ToUpper(src.Device.MAC)
	}
	return ""
}

// ListedGuide returns the guide's address that a playlist's #EXTM3U line
// names in url-tvg or x-tvg-url: the first http or https address of what
// may be a list, "" if none.
func ListedGuide(header map[string]string) string {
	for _, attr := range []string{"url-tvg", "x-tvg-url"} {
		for _, candidate := range strings.Split(header[attr], ",") {
			candidate = strings.TrimSpace(candidate)
			if u, err := url.Parse(candidate); err == nil && (u.Scheme == "http" || u.Scheme == "https") &&
				u.Host != "" && len(candidate) <= 4096 {
				return candidate
			}
		}
	}
	return ""
}

// ReadIfDue reads a source's guide if it has one and it is due: never read,
// or read from another address, or read more than Interval ago, or a failed
// read more than RetryAfter ago. It reports whether it read.
func (g *Reader) ReadIfDue(ctx context.Context, src source.Settings, id int64) (bool, Result, error) {
	address, err := g.address(ctx, src, id)
	if err != nil || address == "" {
		return false, Result{}, err
	}
	state, err := g.store.SourceGuide(ctx, id)
	if err != nil {
		return false, Result{}, err
	}
	interval := g.Interval
	if src.Type == source.TypeStalker {
		interval = min(interval, StalkerInterval)
	}
	age := g.now().Sub(state.ReadAt)
	due := state.ReadAt.IsZero() || state.ReadFrom != readFrom(address) || age >= interval ||
		state.Error != "" && age >= RetryAfter
	if !due {
		return false, Result{}, nil
	}
	res, err := g.read(ctx, src, id, address)
	return true, res, err
}

// Read reads a source's guide now. It returns an error for a source that
// has no guide.
func (g *Reader) Read(ctx context.Context, src source.Settings, id int64) (Result, error) {
	address, err := g.address(ctx, src, id)
	if err == nil && address == "" {
		err = errors.New("the playlist names no guide")
		if !src.HasChannels() {
			err = errors.New("the source has no channels, so no guide")
		}
	}
	if err != nil {
		return Result{}, err
	}
	return g.read(ctx, src, id, address)
}

func (g *Reader) address(ctx context.Context, src source.Settings, id int64) (string, error) {
	listed := ""
	if src.Type == source.TypeM3U {
		var err error
		if listed, err = g.store.ListedGuide(ctx, id); err != nil {
			return "", err
		}
	}
	return Address(src, listed), nil
}

// readFrom is what a read records it was read from: the address, with how
// its channels were matched.
func readFrom(address string) string {
	return store.GuideAddressHash(address + "\n" + matching)
}

// read reads the guide at address into a new generation of the source's
// programmes. If it fails, the guide stays as it was.
func (g *Reader) read(ctx context.Context, src source.Settings, id int64, address string) (Result, error) {
	start := g.now()
	from := readFrom(address)
	w, err := g.store.BeginGuide(ctx, id)
	if err != nil {
		return Result{}, err
	}
	res, err := g.fill(ctx, src, id, address, w)
	if err != nil {
		if ctx.Err() == nil {
			g.log.Warn("guide read failed; the guide stays as it was", "source", src.Name, "error", err)
		}
		// Record the failure even when the read was stopped, with a
		// context that outlives it.
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if aerr := w.Abort(actx, g.now(), from, err.Error()); aerr != nil {
			g.log.Error("cannot record a guide read", "source", src.Name, "error", aerr)
		}
		return res, err
	}
	if err := w.Commit(ctx, g.now(), from); err != nil {
		return res, err
	}
	g.log.Info("guide read", "source", src.Name, "channels", res.Channels, "programmes", res.Programmes,
		"skipped", res.Skipped, "guide_channels", res.Listed, "by_name", res.ByName,
		"took", g.now().Sub(start).Round(time.Millisecond))
	return res, nil
}

// fill fetches the guide, matches its channels, and adds the programmes to
// keep to w.
func (g *Reader) fill(ctx context.Context, src source.Settings, id int64, address string, w *store.GuideWriter) (Result, error) {
	channels, err := g.store.GuideChannels(ctx, id)
	if err != nil {
		return Result{}, err
	}
	if src.Type == source.TypeStalker {
		return g.fillStalker(ctx, src, id, channels, w)
	}
	body, err := g.open(ctx, src, address)
	if err != nil {
		return Result{}, err
	}
	defer body.Close()
	r, err := xmltv.Open(body)
	if err != nil {
		return Result{}, err
	}
	m := newMatcher(channels, g.now())
	skipped, err := xmltv.Parse(&capReader{r: r, left: g.maxBytes}, xmltv.Handler{
		Channel:   m.channel,
		Programme: func(p xmltv.Programme) error { return m.programme(ctx, p, w) },
	})
	if err == nil {
		err = m.finish(ctx, w)
	}
	programmes, matched := w.Count()
	return Result{Channels: matched, Programmes: programmes, Skipped: skipped + m.skipped,
		Listed: m.listed, ByName: m.byNameCount}, err
}

// stalkerKeyPrefix starts the identity key of a Stalker portal's channel,
// before the portal's ID for it, as the catalog stores it.
const stalkerKeyPrefix = "stalker:"

// fillStalker reads a Stalker portal's guide through its API (D76), in the
// session that the source's streams share. The portal gives programmes by
// its own channel IDs, so they go to the channels stored under them, with
// no matching by guide ID or name.
func (g *Reader) fillStalker(ctx context.Context, src source.Settings, id int64, channels []store.GuideChannel, w *store.GuideWriter) (Result, error) {
	c, err := g.stalker.Client(id, stalker.FromSettings(src))
	if err != nil {
		return Result{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	byID := map[string][]store.GuideChannel{}
	for _, ch := range channels {
		if portalID, ok := strings.CutPrefix(ch.IdentityKey, stalkerKeyPrefix); ok {
			byID[portalID] = append(byID[portalID], ch)
		}
	}
	m := newMatcher(nil, g.now())
	listed := map[string]bool{}
	skipped, err := c.Guide(ctx, StalkerHours, func(channelID string, p stalker.Programme) error {
		if len(listed) < maxChannelsListed {
			listed[channelID] = true
		}
		ours := byID[channelID]
		if len(ours) == 0 {
			return nil
		}
		return m.keep(ctx, xmltv.Programme{Channel: channelID, Start: p.Start, Stop: p.Stop, Title: p.Title,
			Desc: p.Description, Category: p.Category}, ours, w)
	})
	programmes, matched := w.Count()
	return Result{Channels: matched, Programmes: programmes, Skipped: skipped + m.skipped, Listed: len(listed)}, err
}

// maxChannelsListed caps how many of a Stalker portal guide's channels are
// counted.
const maxChannelsListed = 100_000

// open fetches a guide.
func (g *Reader) open(ctx context.Context, src source.Settings, address string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("the guide's address is not valid")
	}
	ua := src.UserAgent
	if ua == "" {
		ua = g.userAgent
	}
	req.Header.Set("User-Agent", ua)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", withoutAddress(err))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("the guide's server answered %s", xmltv.Clean(resp.Status, 60))
	}
	return resp.Body, nil
}

// withoutAddress drops the address that a url.Error quotes.
func withoutAddress(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Err
	}
	return err
}

var errTooLarge = fmt.Errorf("the guide is larger than %d MiB", maxGuideBytes>>20)

// capReader fails once more than left bytes have been read, and drops the
// address from read errors.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if err != nil && !errors.Is(err, io.EOF) {
		err = withoutAddress(err)
	}
	return n, err
}

// matcher matches a guide's channels to a source's, and turns the guide's
// programmes into the ones to keep.
type matcher struct {
	now     time.Time
	byID    map[string][]store.GuideChannel // by guide ID, folded to lower case
	byName  map[string][]store.GuideChannel // by name key
	named   []xmltv.Channel                 // the guide's channels, until the first programme
	targets map[string][]store.GuideChannel // the source's channels for each of the guide's
	matched bool                            // targets is complete
	pending map[string]xmltv.Programme      // per guide channel, a programme waiting for its stop
	skipped int
	stored  int
	// For the log: the guide's channels, and the source's that names matched.
	listed      int
	byNameCount int
}

func newMatcher(channels []store.GuideChannel, now time.Time) *matcher {
	m := &matcher{now: now, byID: map[string][]store.GuideChannel{}, byName: map[string][]store.GuideChannel{},
		targets: map[string][]store.GuideChannel{}, pending: map[string]xmltv.Programme{}}
	for _, c := range channels {
		if id := strings.ToLower(strings.TrimSpace(c.XMLTVID)); id != "" {
			m.byID[id] = append(m.byID[id], c)
		}
		if key := NameKey(c.Name); key != "" {
			m.byName[key] = append(m.byName[key], c)
		}
	}
	return m
}

func (m *matcher) channel(c xmltv.Channel) error {
	if !m.matched {
		m.named = append(m.named, c)
	}
	return nil
}

// match decides which of the source's channels each of the guide's channels
// gives programmes to: those with its guide ID, and, among the source's
// channels that no guide ID matched, those with one of its names. So a guide
// channel can serve both the channel that has its ID and the same channel in
// another quality that has none. A channel that a name matched takes the
// first guide channel that matched it.
func (m *matcher) match() {
	m.matched = true
	m.listed = len(m.named)
	byID := map[int64]bool{}
	for _, gc := range m.named {
		if ours := m.byID[strings.ToLower(gc.ID)]; len(ours) > 0 {
			m.targets[gc.ID] = ours
			for _, c := range ours {
				byID[c.ID] = true
			}
		}
	}
	taken := map[int64]bool{}
	for _, gc := range m.named {
		var ours []store.GuideChannel
		for _, name := range gc.Names {
			for _, c := range m.byName[NameKey(name)] {
				if !byID[c.ID] && !taken[c.ID] {
					taken[c.ID] = true
					ours = append(ours, c)
				}
			}
		}
		if len(ours) > 0 {
			// A new slice, since the ID's channels share theirs with byID.
			m.targets[gc.ID] = slices.Concat(m.targets[gc.ID], ours)
			m.byNameCount += len(ours)
		}
	}
	m.named = nil
}

// programme takes a programme of the guide. One without a stop waits for
// the next programme on its channel, whose start is its stop.
func (m *matcher) programme(ctx context.Context, p xmltv.Programme, w *store.GuideWriter) error {
	if !m.matched {
		m.match()
	}
	ours, ok := m.targets[p.Channel]
	if !ok {
		// A programme of a channel that the guide did not describe.
		ours = m.byID[strings.ToLower(p.Channel)]
		m.targets[p.Channel] = ours
	}
	if len(ours) == 0 {
		return nil
	}
	if prev, ok := m.pending[p.Channel]; ok {
		delete(m.pending, p.Channel)
		if p.Start.After(prev.Start) {
			prev.Stop = p.Start
			if err := m.keep(ctx, prev, ours, w); err != nil {
				return err
			}
		} else {
			m.skipped++
		}
	}
	if p.Stop.IsZero() {
		m.pending[p.Channel] = p
		return nil
	}
	return m.keep(ctx, p, ours, w)
}

// finish drops the programmes still waiting for a stop: the last of their
// channels, whose end the guide does not say.
func (m *matcher) finish(context.Context, *store.GuideWriter) error {
	m.skipped += len(m.pending)
	m.pending = nil
	return nil
}

// keep adds a programme to each of the source's channels it belongs to, if
// devices can be shown it: it ends no longer ago than the channel keeps,
// and starts within Ahead.
func (m *matcher) keep(ctx context.Context, p xmltv.Programme, ours []store.GuideChannel, w *store.GuideWriter) error {
	if !p.Start.Before(m.now.Add(Ahead)) {
		return nil
	}
	for _, c := range ours {
		behind := max(Behind, time.Duration(c.ArchiveDays)*24*time.Hour)
		if !p.Stop.After(m.now.Add(-behind)) {
			continue
		}
		if m.stored >= maxProgrammes {
			m.skipped++
			continue
		}
		m.stored++
		if err := w.Add(ctx, store.GuideProgramme{ChannelID: c.ID, Start: p.Start, Stop: p.Stop, Title: p.Title,
			SubTitle: p.SubTitle, Description: p.Desc, Category: p.Category}); err != nil {
			return err
		}
	}
	return nil
}

// qualityTags are words at the end of a channel's name that say how it is
// sent, not what it is.
var qualityTags = map[string]bool{"hd": true, "fhd": true, "uhd": true, "sd": true, "4k": true, "8k": true,
	"hevc": true, "h264": true, "h265": true, "1080p": true, "1080i": true, "720p": true, "50fps": true,
	"60fps": true, "hq": true, "lq": true, "backup": true, "raw": true}

// NameKey returns what a channel's name is matched by: lower case, without
// a leading country or provider tag such as "UK:" or "US |", without words
// in brackets, without quality tags at the end, and with only its letters,
// digits and plus signs, so that "UK: BBC One HD" and "BBC One" match, and
// "Channel 4 +1" and "Channel 4" do not.
func NameKey(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	// A short tag before a colon or a bar, such as "uk:" or "de |".
	if i := strings.IndexAny(s, ":|"); i > 0 && i <= 4 {
		tag := strings.TrimSpace(s[:i])
		if tag != "" && strings.IndexFunc(tag, func(r rune) bool { return !unicode.IsLetter(r) }) < 0 {
			s = s[i+1:]
		}
	}
	// Words in brackets.
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			depth++
			b.WriteRune(' ')
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			b.WriteRune(' ')
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	words := strings.FieldsFunc(b.String(), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '+'
	})
	for len(words) > 1 && qualityTags[words[len(words)-1]] {
		words = words[:len(words)-1]
	}
	return strings.Join(words, "")
}
