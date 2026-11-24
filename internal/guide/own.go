// SPDX-License-Identifier: AGPL-3.0-or-later

package guide

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"runtime/debug"
	"sync"
	"time"

	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/xmltv"
)

// Guides of the household's own (D139). Own reads each enabled guide when
// it is due, as a source's guide is read (D51): every Interval, at once
// once it is added or its address changes, and RetryAfter after a failed
// read; one guide at a time, streamed, gzip or not. A read records the
// channels that the guide lists, works out again which of every guide's
// channels fills which channel (match), and keeps the programmes of this
// guide's channels that fill one. Own works out the matches again when a
// source's channels or guide change, and when the guides change; a guide
// whose channels then fill a channel that its last read did not keep is
// read again, MatchedAgainAfter its last read, or at once after a change
// to the guides.

const (
	// MatchedAgainAfter is how soon after its last read a guide is read
	// again when channels come to need channels of it that that read did
	// not keep.
	MatchedAgainAfter = time.Hour
	// maxListed caps the channels of a guide that a read records.
	maxListed = 100_000
	// maxRequests caps the reads that the admin asks for and that wait.
	maxRequests = 8
)

// ErrBusy is returned when too many reads wait already.
var ErrBusy = errors.New("guide: other reads wait; try again later")

// OwnResult is what a read of a guide of the household's own found.
type OwnResult struct {
	Listed     int // the channels that the guide lists
	Fills      int // the channels that it gives programmes to
	ByName     int // of those, the ones that their names matched
	ByHand     int // and the ones whose guide was chosen by hand (D140)
	Channels   int // of the guide's channels, those it kept programmes for
	Programmes int
	Skipped    int // programmes and channels it could not read, or that were too many
}

// Own reads the guides of the household's own, and keeps channels matched
// with them.
type Own struct {
	store  *store.Store
	log    *slog.Logger
	reader *Reader // fetches guides, with its client, user agent and limits
	now    func() time.Time
	// Interval is how often a guide is read.
	Interval time.Duration

	wake     chan struct{}
	requests chan readRequest
	// changed is told after the channels were matched again or a guide
	// was read, nil for nothing.
	changed func()

	mu      sync.Mutex
	sources bool  // a source's channels or guide changed since the last match
	guides  bool  // the guides changed since the last match
	reading int64 // the guide being read, 0 if none

	// The channels that a channel's guide can be chosen from, for
	// suggestions and search (D140), built when first asked for after
	// they change.
	choicesMu sync.Mutex
	choices   *optionIndex
}

type readRequest struct {
	id   int64
	done chan readOutcome
}

type readOutcome struct {
	res OwnResult
	err error
}

// NewOwn returns what reads the guides of the household's own into st. It
// fetches them as Overscan/version unless a guide names another user
// agent.
func NewOwn(st *store.Store, log *slog.Logger, version string) *Own {
	return &Own{
		store:    st,
		log:      log,
		reader:   New(st, log, version),
		now:      time.Now,
		Interval: DefaultInterval,
		wake:     make(chan struct{}, 1),
		requests: make(chan readRequest, maxRequests),
	}
}

// Changed says that the guides changed in the admin: they are matched
// again, and those that the new matches need are read at once.
func (o *Own) Changed() {
	o.mu.Lock()
	o.guides = true
	o.mu.Unlock()
	o.forgetOptions()
	o.poke()
}

// SourcesChanged says that a source's channels or guide changed, so that
// the channels are matched with the guides again.
func (o *Own) SourcesChanged() {
	o.mu.Lock()
	o.sources = true
	o.mu.Unlock()
	o.forgetOptions()
	o.poke()
}

func (o *Own) poke() {
	select {
	case o.wake <- struct{}{}:
	default: // one is pending already
	}
}

// OnChange makes Own call fn after it matched the channels with the
// guides again or read a guide, so that what follows from the matches,
// such as the checks of logos (D141), can follow at once. Call it before
// Run.
func (o *Own) OnChange(fn func()) { o.changed = fn }

// Reading returns the guide that is being read, 0 if none is.
func (o *Own) Reading() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.reading
}

// ReadNow reads a guide now, whatever its schedule, once the read under
// way, if any, ends. If ctx ends first, the read still happens.
func (o *Own) ReadNow(ctx context.Context, id int64) (OwnResult, error) {
	req := readRequest{id: id, done: make(chan readOutcome, 1)}
	select {
	case o.requests <- req:
	default:
		return OwnResult{}, ErrBusy
	}
	select {
	case out := <-req.done:
		return out.res, out.err
	case <-ctx.Done():
		return OwnResult{}, ctx.Err()
	}
}

// Run reads the guides when they are due, and matches the channels with
// them again when sources or guides change, until ctx is done.
func (o *Own) Run(ctx context.Context) {
	o.mu.Lock()
	o.sources = true
	o.mu.Unlock()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		o.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-o.wake:
		case <-tick.C:
		case req := <-o.requests:
			res, err := o.readNow(ctx, req.id)
			req.done <- readOutcome{res, err}
			if err == nil && o.changed != nil {
				o.changed()
			}
		}
	}
}

// step matches the channels again if sources or guides changed, then
// reads each enabled guide that is due.
func (o *Own) step(ctx context.Context) {
	o.mu.Lock()
	again, changed := o.sources || o.guides, o.guides
	o.sources, o.guides = false, false
	o.mu.Unlock()
	matched, read := false, false
	defer func() {
		if (matched || read) && o.changed != nil {
			o.changed()
		}
	}()
	guides, err := o.store.Guides(ctx)
	if err != nil {
		if ctx.Err() == nil {
			o.log.Error("cannot read the guides", "error", err)
		}
		return
	}
	var enabled []store.Guide
	for _, g := range guides {
		if g.Enabled {
			enabled = append(enabled, g)
		}
	}
	if again {
		if err := o.matchAgain(ctx, len(enabled) > 0); err != nil {
			if ctx.Err() == nil {
				o.log.Error("cannot match the channels with the guides", "error", err)
			}
			return
		}
		matched = true
	}
	if len(enabled) == 0 {
		return
	}
	waiting, err := o.store.GuidesWaiting(ctx)
	if err != nil {
		if ctx.Err() == nil {
			o.log.Error("cannot read the guides", "error", err)
		}
		return
	}
	for _, g := range enabled {
		if ctx.Err() != nil {
			return
		}
		settings, err := o.store.GuideSettings(ctx, g.ID)
		if err != nil {
			o.unreadable(ctx, g, err)
			continue
		}
		if !o.due(g, settings.Address, containsID(waiting, g.ID), changed) {
			continue
		}
		_, _ = o.read(ctx, g, settings)
		read = true
	}
	if read {
		o.forgetOptions()
		// A read builds large lists that are garbage once stored: give that
		// memory back now, as a source's read does.
		debug.FreeOSMemory()
	}
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// due reports whether a guide is due to be read: never read, or read from
// another address or with other settings, or read more than Interval ago,
// or a failed read more than RetryAfter ago, or channels need channels of
// it that its last read did not keep, and either the guides changed or
// that read was more than MatchedAgainAfter ago.
func (o *Own) due(g store.Guide, address string, waiting, changed bool) bool {
	age := o.now().Sub(g.ReadAt)
	return g.ReadAt.IsZero() || g.ReadFrom != readFrom(address) || age >= o.Interval ||
		g.ReadError != "" && age >= RetryAfter || waiting && (changed || age >= MatchedAgainAfter)
}

// unreadable records, once a guide is due by its schedule, why its address
// cannot be used.
func (o *Own) unreadable(ctx context.Context, g store.Guide, err error) {
	if ctx.Err() != nil {
		return
	}
	if !errors.Is(err, store.ErrUnreadable) {
		o.log.Error("cannot read a guide's settings", "guide", g.Name, "error", err)
		return
	}
	if age := o.now().Sub(g.ReadAt); !g.ReadAt.IsZero() && age < RetryAfter {
		return
	}
	why := "its address was saved with another key file; enter it again in the admin, or put back the key file that saved it"
	o.log.Warn("guide cannot be read: "+why, "guide", g.Name)
	w, err := o.store.BeginGuideRead(ctx, g.ID)
	if err == nil {
		err = w.Abort(ctx, o.now(), g.ReadFrom, why)
	}
	if err != nil {
		o.log.Error("cannot record a guide's read", "guide", g.Name, "error", err)
	}
}

// matchAgain works out every channel's match with the guides again, and
// keeps it: with none if no guide is on.
func (o *Own) matchAgain(ctx context.Context, any bool) error {
	var matches []store.GuideMatch
	if any {
		var err error
		if matches, _, err = o.allMatches(ctx); err != nil {
			return err
		}
	}
	changed, err := o.store.SetGuideMatches(ctx, matches)
	if err == nil && changed {
		byName, byHand := 0, 0
		for _, m := range matches {
			if m.ByName {
				byName++
			}
			if m.ByHand {
				byHand++
			}
		}
		o.log.Info("channels matched with the guides again", "channels", len(matches), "by_name", byName, "by_hand", byHand)
	}
	return err
}

// readNow reads a guide that the admin asked for.
func (o *Own) readNow(ctx context.Context, id int64) (OwnResult, error) {
	g, err := o.store.Guide(ctx, id)
	if err != nil {
		return OwnResult{}, err
	}
	settings, err := o.store.GuideSettings(ctx, id)
	if err != nil {
		return OwnResult{}, err
	}
	res, err := o.read(ctx, g, settings)
	if err == nil {
		debug.FreeOSMemory()
	}
	return res, err
}

// read reads a guide into a new generation of its programmes. If it fails,
// the guide stays as it was.
func (o *Own) read(ctx context.Context, g store.Guide, settings store.GuideSettings) (OwnResult, error) {
	o.mu.Lock()
	o.reading = g.ID
	o.mu.Unlock()
	defer func() {
		o.mu.Lock()
		o.reading = 0
		o.mu.Unlock()
	}()
	start := o.now()
	from := readFrom(settings.Address)
	w, err := o.store.BeginGuideRead(ctx, g.ID)
	if err != nil {
		return OwnResult{}, err
	}
	res, found, err := o.fill(ctx, settings, w)
	if err != nil {
		if ctx.Err() == nil {
			o.log.Warn("guide read failed; the guide stays as it was", "guide", g.Name, "error", err)
		}
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		defer cancel()
		if aerr := w.Abort(actx, o.now(), from, err.Error()); aerr != nil {
			o.log.Error("cannot record a guide's read", "guide", g.Name, "error", aerr)
		}
		return res, err
	}
	// What has ended and the new read lacks stays, as long as it would
	// have been kept (D83).
	w.KeepPast(Behind)
	if err := w.Commit(ctx, o.now(), from, found); err != nil {
		return res, err
	}
	o.log.Info("guide read", "guide", g.Name, "fills", res.Fills, "by_name", res.ByName, "by_hand", res.ByHand, "channels", res.Channels,
		"programmes", res.Programmes, "skipped", res.Skipped, "guide_channels", res.Listed,
		"took", o.now().Sub(start).Round(time.Millisecond))
	return res, nil
}

// fill fetches a guide and parses it: it records the channels that the
// guide lists, matches every channel with every guide's channels once it
// has them all, at the guide's first programme, and adds the programmes of
// this guide's channels that fill one to w. Channels that the guide lists
// after its first programme, which XMLTV does not allow, are left out.
func (o *Own) fill(ctx context.Context, settings store.GuideSettings, w *store.GuideReader) (OwnResult, store.GuideRead, error) {
	var res OwnResult
	var found store.GuideRead
	body, err := o.open(ctx, settings)
	if err != nil {
		return res, found, err
	}
	defer body.Close()
	r, err := xmltv.Open(body)
	if err != nil {
		return res, found, err
	}
	now := o.now()
	var listed []store.GuideListing
	seen := map[string]bool{}
	extra := 0
	var m *matcher
	begin := func() error {
		ids, err := w.ListChannels(ctx, listed, now)
		if err != nil {
			return err
		}
		found.Listed = len(listed)
		listed, seen = nil, nil
		var archive map[int64]int
		if found.Matches, archive, err = o.allMatches(ctx); err != nil {
			return err
		}
		res.Fills, res.ByName, res.ByHand, found.Kept, m = targets(ids, archive, found.Matches, now)
		return nil
	}
	skipped, err := xmltv.Parse(&capReader{r: r, left: o.reader.maxBytes}, xmltv.Handler{
		Channel: func(c xmltv.Channel) error {
			if m != nil || seen[c.ID] {
				return nil
			}
			if len(listed) >= maxListed {
				extra++
				return nil
			}
			seen[c.ID] = true
			listed = append(listed, store.GuideListing{XMLTVID: c.ID, Names: c.Names, Icon: c.Icon})
			return nil
		},
		Programme: func(p xmltv.Programme) error {
			if m == nil {
				if err := begin(); err != nil {
					return err
				}
			}
			return m.programme(ctx, p, w)
		},
	})
	if err == nil && m == nil {
		// A guide that lists channels and no programmes.
		err = begin()
	}
	if err == nil {
		err = m.finish(ctx, w)
	}
	res.Listed = found.Listed
	res.Programmes, res.Channels = w.Count()
	res.Skipped = skipped + extra
	if m != nil {
		res.Skipped += m.skipped
	}
	return res, found, err
}

// allMatches works out every channel's match with the enabled guides'
// channels: the guides' own, and those that choices by hand make (D140).
// It returns them, with the days of archive of the channels they fill.
func (o *Own) allMatches(ctx context.Context) ([]store.GuideMatch, map[int64]int, error) {
	candidates, err := o.store.MatchCandidates(ctx)
	if err != nil {
		return nil, nil, err
	}
	listed, err := o.store.ListedChannels(ctx)
	if err != nil {
		return nil, nil, err
	}
	hand, err := o.store.HandMatches(ctx)
	if err != nil {
		return nil, nil, err
	}
	matches := match(candidates, listed)
	archive := make(map[int64]int, len(candidates)+len(hand))
	for _, c := range candidates {
		archive[c.ID] = c.ArchiveDays
	}
	for _, h := range hand {
		matches = append(matches, h.GuideMatch)
		archive[h.ChannelID] = h.ArchiveDays
	}
	return matches, archive, nil
}

// targets returns, of the matches, how many fill a channel from this
// guide, whose listed channels ids gives by the guide's IDs for them, how
// many of those by name and how many by hand; the IDs of its channels that
// fill one; and a matcher that keeps their programmes, as long as the
// channels that they fill keep their past, as archive gives it (D51, D81).
func targets(ids map[string]int64, archive map[int64]int, matches []store.GuideMatch, now time.Time) (fills, byName, byHand int, kept []int64, m *matcher) {
	xmltvID := make(map[int64]string, len(ids))
	for x, id := range ids {
		xmltvID[id] = x
	}
	days := map[int64]int{}
	for _, mt := range matches {
		if _, ours := xmltvID[mt.GuideChannelID]; !ours {
			continue
		}
		fills++
		if mt.ByName {
			byName++
		}
		if mt.ByHand {
			byHand++
		}
		if _, ok := days[mt.GuideChannelID]; !ok {
			kept = append(kept, mt.GuideChannelID)
		}
		days[mt.GuideChannelID] = max(days[mt.GuideChannelID], archive[mt.ChannelID])
	}
	m = &matcher{now: now, targets: map[string][]store.GuideChannel{}, matched: true, pending: map[string]xmltv.Programme{}}
	for id, d := range days {
		m.targets[xmltvID[id]] = []store.GuideChannel{{ID: id, ArchiveDays: d}}
	}
	return fills, byName, byHand, kept, m
}

// open opens a guide: a file on the server, which must be a regular file,
// or an address, fetched as its settings say.
func (o *Own) open(ctx context.Context, g store.GuideSettings) (io.ReadCloser, error) {
	if g.IsFile() {
		return openGuideFile(g.Address)
	}
	return o.reader.fetch(ctx, g.Address, g.UserAgent, g.PublicOnly)
}

// Why a guide's file cannot be read, without its path, which is sealed as
// a guide's address is.
var (
	errFileMissing    = errors.New("the guide's file does not exist")
	errFileDenied     = errors.New("Overscan may not read the guide's file")
	errFileNotRegular = errors.New("the guide's file is not a regular file")
)

// openGuideFile opens a guide's file, which must be a regular file, as a
// playlist's must: a named pipe or a device could hold a read up for ever,
// or give data without end.
func openGuideFile(path string) (*os.File, error) {
	opened := func(err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return errFileMissing
		case errors.Is(err, fs.ErrPermission):
			return errFileDenied
		}
		return errors.New("the guide's file cannot be read")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, opened(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errFileNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, opened(err)
	}
	// What the path names may have changed since it was looked at.
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errFileNotRegular
	}
	return f, nil
}
