// SPDX-License-Identifier: AGPL-3.0-or-later

// Package library reads the films and series of sources when devices ask
// for them, and keeps them in the store (D63): those of Xtream servers, of
// Jellyfin servers (D66), of folders on the server (D67), of Plex servers
// (D72), and of Stalker portals, a page at a time (D77).
//
// A source's list of films, or of series, is read the first time a device
// asks for it: its categories and the whole list, one request each, as
// players of such servers do when they sign in. A device that asks again
// once the list is a day old gets the old list at once while a new one is
// read. A title's details, and a series' episodes, are read when a device
// opens the title, and kept for a while. Overscan never reads a source title
// by title on its own: that would look like a crawl to a provider (D22).
//
// A Jellyfin server or a folder is the administrator's own, and what it
// holds changes when they add files, so what it lists is read again sooner.
// A Jellyfin server's libraries are the categories, and it lists them a
// page at a time; a folder is one category.
//
// Requests to a source go one at a time, and a read that a device gave up
// waiting for goes on, so that its result is there for the next device.
package library

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/adapters/jellyfin"
	"overscan.gureet.ca/internal/adapters/plex"
	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/xtream"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/subtitles"
	"overscan.gureet.ca/internal/tmdb"
)

// How long what Overscan reads is kept before a device's request reads it
// again.
const (
	ListTTL          = 24 * time.Hour     // a source's list of films or of series
	MovieDetailsTTL  = 7 * 24 * time.Hour // a film's details
	SeriesDetailsTTL = 12 * time.Hour     // a series' details and episodes, which grow
	// OwnTTL is how long what a Jellyfin server or a folder lists is
	// kept, its details and episodes too.
	OwnTTL = 15 * time.Minute
)

// The widths of the pictures asked of a Jellyfin server: a little more than
// the artwork cache keeps, which scales them down.
const (
	posterWidth   = 480
	backdropWidth = 1280
	stillWidth    = 640
)

const (
	// retryAfter is how long after a failed read of a list a device's
	// request waits before it tries again.
	retryAfter = 10 * time.Minute
	// listTimeout and detailsTimeout bound a read.
	listTimeout    = 5 * time.Minute
	detailsTimeout = 30 * time.Second
	// maxTitles caps a source's list of films or of series.
	maxTitles = 200_000
)

// Library reads and keeps the films and series of sources.
type Library struct {
	store     *store.Store
	log       *slog.Logger
	client    *http.Client
	userAgent string
	now       func() time.Time

	mu      sync.Mutex
	base    context.Context // reads run under it, not under a device's request
	stop    context.CancelFunc
	running sync.WaitGroup
	gates   map[int64]chan struct{} // one request at a time to each source
	lists   map[listKey]*pending    // reads of lists under way
	details map[int64]*pending      // reads of titles' details under way, by title
	pages   map[pageKey]*pending    // reads of pages of portals' categories under way
	// detailed holds when a read of a title's details last ended well, for
	// a minute: a device that looked at the title just before then has a
	// copy without them, and needs no read of its own.
	detailed map[int64]time.Time

	// stalker holds the sessions of Stalker portals (D76).
	stalker *stalker.Pool

	// tmdbBase is TMDB's address, and tmdbRuns the lookups on TMDB under
	// way in the background, by source and kind (D88).
	tmdbBase string
	tmdbRuns map[listKey]bool

	// Overridden by tests.
	maxListBytes int64
}

type listKey struct {
	source int64
	kind   string
}

// pending is a read under way, which callers wait for.
type pending struct {
	done chan struct{}
	err  error
}

// New returns a library that keeps what it reads in st, reading sources as
// Overscan/version unless a source names another user agent.
func New(st *store.Store, log *slog.Logger, version string) *Library {
	base, stop := context.WithCancel(context.Background())
	return &Library{
		store:     st,
		log:       log,
		client:    &http.Client{Timeout: listTimeout},
		userAgent: "Overscan/" + version,
		now:       time.Now,
		base:      base,
		stop:      stop,
		gates:     map[int64]chan struct{}{},
		lists:     map[listKey]*pending{},
		details:   map[int64]*pending{},
		pages:     map[pageKey]*pending{},
		detailed:  map[int64]time.Time{},
		stalker:   stalker.NewPool(nil),
		tmdbBase:  tmdb.DefaultBase,
		tmdbRuns:  map[listKey]bool{},
	}
}

// Run waits until ctx is done, then stops the reads under way and waits for
// them, so that none outlives the store.
func (l *Library) Run(ctx context.Context) {
	<-ctx.Done()
	l.stop()
	l.running.Wait()
}

// State says what devices can browse of a kind of title.
type State struct {
	Sources int    // the enabled sources that have films and series
	Read    int    // of those, the ones whose list has been read
	Loading bool   // a read of a list is under way
	Error   string // why the last read failed, when no list has been read
}

// Ready reports whether there is anything to browse.
func (s State) Ready() bool { return s.Read > 0 }

// Want makes sure that every enabled source's list of films or of series is
// read, starting the reads that are due, and returns what devices
// can browse. When there is nothing to browse yet, it waits for the reads
// under way for up to wait, or until ctx is done.
func (l *Library) Want(ctx context.Context, kind string, wait time.Duration) (State, error) {
	if kind != store.KindMovie && kind != store.KindSeries {
		return State{}, fmt.Errorf("library: %q is not a kind of title", kind)
	}
	st, waits, err := l.want(ctx, kind)
	if err != nil || st.Ready() || len(waits) == 0 || wait <= 0 {
		return st, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for _, p := range waits {
		select {
		case <-p.done:
		case <-timer.C:
			return l.state(ctx, kind)
		case <-ctx.Done():
			return st, ctx.Err()
		}
	}
	return l.state(ctx, kind)
}

// want starts the reads that are due, and returns the state and the reads
// under way.
func (l *Library) want(ctx context.Context, kind string) (State, []*pending, error) {
	sources, reads, err := l.load(ctx, kind)
	if err != nil {
		return State{}, nil, err
	}
	now := l.now()
	var waits []*pending
	for _, src := range sources {
		r := reads[src.ID]
		ttl := ListTTL
		if src.Type == source.TypeJellyfin || src.Type == source.TypePlex || src.Type == source.TypeFolder {
			ttl = OwnTTL
		}
		due := r.ReadAt.IsZero() || now.Sub(r.ReadAt) >= ttl
		failedLately := r.Error != "" && now.Sub(r.TriedAt) < retryAfter
		if p := l.startList(src, kind, due && !failedLately); p != nil {
			waits = append(waits, p)
		}
	}
	return l.summarise(sources, reads, len(waits) > 0), waits, nil
}

// state returns the state without starting reads.
func (l *Library) state(ctx context.Context, kind string) (State, error) {
	sources, reads, err := l.load(ctx, kind)
	if err != nil {
		return State{}, err
	}
	loading := false
	l.mu.Lock()
	for _, src := range sources {
		if _, ok := l.lists[listKey{src.ID, kind}]; ok {
			loading = true
		}
	}
	l.mu.Unlock()
	return l.summarise(sources, reads, loading), nil
}

// load returns the enabled sources that have films and series, and what the
// reads of their lists of kind did, by source.
func (l *Library) load(ctx context.Context, kind string) ([]store.Source, map[int64]store.LibraryRead, error) {
	all, err := l.store.Sources(ctx)
	if err != nil {
		return nil, nil, err
	}
	var sources []store.Source
	for _, src := range all {
		if src.Enabled && src.Settings().HasLibrary() {
			sources = append(sources, src)
		}
	}
	list, err := l.store.LibraryReads(ctx)
	if err != nil {
		return nil, nil, err
	}
	reads := map[int64]store.LibraryRead{}
	for _, r := range list {
		if r.Kind == kind {
			reads[r.SourceID] = r
		}
	}
	return sources, reads, nil
}

func (l *Library) summarise(sources []store.Source, reads map[int64]store.LibraryRead, loading bool) State {
	st := State{Sources: len(sources), Loading: loading}
	for _, src := range sources {
		r := reads[src.ID]
		if !r.ReadAt.IsZero() {
			st.Read++
		} else if r.Error != "" && st.Error == "" {
			st.Error = r.Error
		}
	}
	if st.Read > 0 {
		st.Error = ""
	}
	return st
}

// startList returns the read of a source's list that is under way, starting
// one if start says so, or nil if none is.
func (l *Library) startList(src store.Source, kind string, start bool) *pending {
	key := listKey{src.ID, kind}
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.lists[key]; ok {
		return p
	}
	if !start || l.base.Err() != nil {
		return nil
	}
	p := &pending{done: make(chan struct{})}
	l.lists[key] = p
	l.running.Add(1)
	go func() {
		defer l.running.Done()
		p.err = l.readList(src, kind)
		l.mu.Lock()
		delete(l.lists, key)
		l.mu.Unlock()
		close(p.done)
	}()
	return p
}

// gate takes a source's turn to make a request, and returns the function
// that gives it back.
func (l *Library) gate(ctx context.Context, sourceID int64) (func(), error) {
	l.mu.Lock()
	g, ok := l.gates[sourceID]
	if !ok {
		g = make(chan struct{}, 1)
		l.gates[sourceID] = g
	}
	l.mu.Unlock()
	select {
	case g <- struct{}{}:
		return func() { <-g }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// errNoLibrary is the error for a source without films or series, which
// has changed type since a device asked.
var errNoLibrary = errors.New("the source has no films or series")

// xtreamClient returns an Xtream client for a source's settings.
func (l *Library) xtreamClient(settings source.Settings) *xtream.Client {
	c := xtream.New(settings, l.client, l.userAgent)
	if l.maxListBytes > 0 {
		c.MaxListBytes = l.maxListBytes
	}
	return c
}

// jellyfinClient returns a Jellyfin client for a source's settings.
func (l *Library) jellyfinClient(settings source.Settings) *jellyfin.Client {
	c := jellyfin.New(settings, l.client, l.userAgent)
	if l.maxListBytes > 0 {
		c.MaxBytes = l.maxListBytes
	}
	return c
}

// plexClient returns a Plex client for a source's settings.
func (l *Library) plexClient(settings source.Settings) *plex.Client {
	c := plex.New(settings, l.client, l.userAgent)
	if l.maxListBytes > 0 {
		c.MaxBytes = l.maxListBytes
	}
	return c
}

// readList reads a source's list of films or of series and keeps it. A read
// that fails leaves the list as it was, and records why. Neither the log
// nor the record shows the source's address.
func (l *Library) readList(src store.Source, kind string) error {
	release, err := l.gate(l.base, src.ID)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(l.base, listTimeout)
	defer cancel()
	start := l.now()
	what := map[string]string{store.KindMovie: "films", store.KindSeries: "series"}[kind]
	fail := func(err error) error {
		if l.base.Err() != nil {
			return err
		}
		l.log.Warn("cannot read a source's "+what+"; what was read before stays", "source", src.Name, "error", err)
		if serr := l.store.SetLibraryFailed(context.WithoutCancel(ctx), src.ID, kind, l.now(), err.Error()); serr != nil {
			l.log.Error("cannot record a read of a source's library", "source", src.Name, "error", serr)
		}
		return err
	}
	settings, err := l.store.SourceSettings(ctx, src.ID)
	if err != nil {
		return fail(err)
	}
	if settings.Type == source.TypeStalker {
		// A portal's titles come a page at a time, as devices browse them
		// (D77).
		n, err := l.readPortalCategories(ctx, src.ID, settings, kind)
		if err != nil {
			return fail(err)
		}
		l.log.Info("read a portal's categories of "+what, "source", src.Name, "categories", n,
			"took", l.now().Sub(start).Round(time.Millisecond))
		return nil
	}
	read := &listRead{source: src.ID, tooMany: fmt.Errorf("the server lists more than %d %s", maxTitles, what)}
	switch settings.Type {
	case source.TypeXtream:
		err = l.readXtreamList(ctx, settings, kind, read)
	case source.TypeJellyfin:
		err = l.readJellyfinList(ctx, settings, kind, read)
	case source.TypePlex:
		err = l.readPlexList(ctx, settings, kind, read)
	case source.TypeFolder:
		err = l.readFolderList(ctx, settings, kind, read)
	default:
		err = errNoLibrary
	}
	if err != nil {
		return fail(err)
	}
	changes, err := l.store.ApplyLibrary(ctx, src.ID, kind, read.categories, read.titles, l.now())
	if err != nil {
		return fail(err)
	}
	l.log.Info("read a source's "+what, "source", src.Name, "titles", len(read.titles), "categories", len(read.categories),
		"added", changes.Added, "missing", changes.Missing, "removed", changes.Purged, "skipped", read.skipped,
		"took", l.now().Sub(start).Round(time.Millisecond))
	if len(read.tmdb) > 0 {
		l.startTMDB(src, kind, read.tmdb)
	}
	return nil
}

// listRead is what a read of a list found.
type listRead struct {
	source     int64
	categories []store.LibraryCategory
	titles     []store.LibraryTitle
	skipped    int   // entries that could not be read
	tooMany    error // the error once the list holds maxTitles
	// tmdb are the folder's titles to look up on TMDB (D88).
	tmdb []tmdbWant
}

// keep adds a title to the list, unless it is full.
func (r *listRead) keep(t store.LibraryTitle) error {
	if len(r.titles) == maxTitles {
		return r.tooMany
	}
	r.titles = append(r.titles, t)
	return nil
}

// readXtreamList reads an Xtream server's categories of films or of series,
// and then the whole list.
func (l *Library) readXtreamList(ctx context.Context, settings source.Settings, kind string, read *listRead) error {
	c := l.xtreamClient(settings)
	var categories []xtream.Category
	var err error
	if kind == store.KindMovie {
		if categories, err = c.MovieCategories(ctx); err != nil {
			return fmt.Errorf("categories: %w", err)
		}
		read.skipped, err = c.Movies(ctx, func(m xtream.Movie) error { return read.keep(movieTitle(m)) })
	} else {
		if categories, err = c.SeriesCategories(ctx); err != nil {
			return fmt.Errorf("categories: %w", err)
		}
		read.skipped, err = c.Series(ctx, func(s xtream.Show) error { return read.keep(showTitle(s)) })
	}
	if err != nil {
		return err
	}
	for _, c := range categories {
		read.categories = append(read.categories, store.LibraryCategory{Key: c.ID, Name: c.Name})
	}
	return nil
}

// readJellyfinList reads the films, home videos among them, or the series
// in the libraries of a Jellyfin server that the source's user sees, or in
// all of them (D66). Each library is a category; a title in two libraries is
// kept in the first.
func (l *Library) readJellyfinList(ctx context.Context, settings source.Settings, kind string, read *listRead) error {
	c := l.jellyfinClient(settings)
	libraries, err := c.Libraries(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, lib := range libraries {
		typ := lib.FilmType()
		if kind == store.KindSeries {
			typ = ""
			if lib.HasSeries() {
				typ = jellyfin.TypeSeries
			}
		}
		if typ == "" {
			continue
		}
		read.categories = append(read.categories, store.LibraryCategory{Key: lib.ID, Name: lib.Name})
		skipped, err := c.Items(ctx, lib.ID, typ, func(it jellyfin.Item) error {
			if seen[it.ID] {
				return nil
			}
			seen[it.ID] = true
			return read.keep(jellyfinTitle(c, it, lib.ID))
		})
		read.skipped += skipped
		if err != nil {
			return fmt.Errorf("library %q: %w", lib.Name, err)
		}
	}
	return nil
}

// The sizes at which a Plex server's pictures are fetched: the widths that
// Jellyfin's are, and the heights that the screens show them at.
const (
	posterHeight   = 720
	backdropHeight = 720
	stillHeight    = 360
)

// readPlexList reads the films or the shows in a Plex server's libraries
// that the token's account sees (D72): each library is a category. A film
// without a file, which would not play, is left out.
func (l *Library) readPlexList(ctx context.Context, settings source.Settings, kind string, read *listRead) error {
	c := l.plexClient(settings)
	libraries, err := c.Libraries(ctx)
	if err != nil {
		return err
	}
	typ := plex.TypeMovie
	if kind == store.KindSeries {
		typ = plex.TypeShow
	}
	seen := map[string]bool{}
	for _, lib := range libraries {
		if lib.Type != typ {
			continue
		}
		read.categories = append(read.categories, store.LibraryCategory{Key: lib.Key, Name: lib.Name})
		skipped, err := c.Items(ctx, lib.Key, typ, func(it plex.Item) error {
			switch {
			case seen[it.Key]:
				return nil
			case typ == plex.TypeMovie && it.File.Key == "":
				read.skipped++
				return nil
			}
			seen[it.Key] = true
			return read.keep(plexTitle(it, lib.Key))
		})
		read.skipped += skipped
		if err != nil {
			return fmt.Errorf("library %q: %w", lib.Name, err)
		}
	}
	return nil
}

// plexTitle returns a film or a show of a Plex server, in the library with
// the given key, as the store keeps it.
func plexTitle(it plex.Item, library string) store.LibraryTitle {
	return store.LibraryTitle{
		Key:       it.Key,
		Category:  library,
		Name:      it.Name,
		Year:      it.Year,
		Rating:    it.Rating,
		Poster:    plex.PictureAddress(it.Poster, posterWidth, posterHeight),
		Container: it.File.Container,
		File:      it.File.Key,
		AddedAt:   it.Added,
		Summary:   plexSummary(it),
	}
}

// plexSummary returns what a Plex server says about a title beyond its
// name.
func plexSummary(it plex.Item) store.TitleSummary {
	return store.TitleSummary{Plot: it.Plot, Genre: strings.Join(it.Genres, ", "), Cast: strings.Join(it.Cast, ", "),
		Director: strings.Join(it.Directors, ", "),
		Backdrop: plex.PictureAddress(it.Backdrop, backdropWidth, backdropHeight), Runtime: int(it.Duration / time.Minute)}
}

// movieTitle returns a film as the store keeps it.
func movieTitle(m xtream.Movie) store.LibraryTitle {
	return store.LibraryTitle{
		Key:       strconv.FormatInt(m.ID, 10),
		Category:  m.CategoryID,
		Name:      m.Name,
		Year:      m.Year,
		Rating:    m.Rating,
		Poster:    m.Poster,
		Container: m.Container,
		AddedAt:   m.Added,
		Summary: store.TitleSummary{Plot: m.Plot, Genre: m.Genre, Cast: m.Cast, Director: m.Director,
			Runtime: m.Runtime},
		Adult: m.Adult,
	}
}

// showTitle returns a series as the store keeps it.
func showTitle(s xtream.Show) store.LibraryTitle {
	return store.LibraryTitle{
		Key:      strconv.FormatInt(s.ID, 10),
		Category: s.CategoryID,
		Name:     s.Name,
		Year:     s.Year,
		Rating:   s.Rating,
		Poster:   s.Poster,
		AddedAt:  s.Modified,
		Summary: store.TitleSummary{Plot: s.Plot, Genre: s.Genre, Cast: s.Cast, Director: s.Director,
			Backdrop: s.Backdrop, Runtime: s.Runtime},
	}
}

// jellyfinTitle returns a film or a series of a Jellyfin server, in the
// library with the given ID, as the store keeps it. A series counts as added
// when its latest episode was.
func jellyfinTitle(c *jellyfin.Client, it jellyfin.Item, library string) store.LibraryTitle {
	added := it.Added
	if it.Updated.After(added) {
		added = it.Updated
	}
	return store.LibraryTitle{
		Key:       it.ID,
		Category:  library,
		Name:      it.Name,
		Year:      it.Year,
		Rating:    it.Rating,
		Poster:    c.ImageURL(it.ID, "Primary", it.Poster, posterWidth),
		Container: it.Container,
		AddedAt:   added,
		Summary:   jellyfinSummary(c, it),
	}
}

// jellyfinSummary returns what a Jellyfin server says about a title beyond
// its name.
func jellyfinSummary(c *jellyfin.Client, it jellyfin.Item) store.TitleSummary {
	return store.TitleSummary{Plot: it.Plot, Genre: strings.Join(it.Genres, ", "), Cast: strings.Join(it.Cast, ", "),
		Director: strings.Join(it.Directors, ", "), Backdrop: c.ImageURL(it.ID, "Backdrop", it.Backdrop, backdropWidth),
		Runtime: int(it.Duration / time.Minute)}
}

// Opened is a title that a device opened.
type Opened struct {
	store.Title
	// Episodes are a series' episodes, by season and number.
	Episodes []store.Episode
	// ReadError says why the title's details could not be read from its
	// source just now, "" if they could or did not need to be. What Opened
	// holds is then what was kept before, if anything.
	ReadError string
}

// Open returns a title that a device opened, with its details, and a
// series with its episodes. It reads them from the source when it has none
// or they are old, waiting for the read for as long as ctx lets it. It
// returns store.ErrNotFound for a title that devices do not see.
func (l *Library) Open(ctx context.Context, id int64) (Opened, error) {
	t, err := l.store.Title(ctx, id)
	if err != nil {
		return Opened{}, err
	}
	ttl := MovieDetailsTTL
	switch {
	case t.SourceType == source.TypeJellyfin || t.SourceType == source.TypePlex || t.SourceType == source.TypeFolder:
		ttl = OwnTTL
	case t.Kind == store.KindSeries:
		ttl = SeriesDetailsTTL
	}
	o := Opened{Title: t}
	if t.Details == nil || l.now().Sub(t.DetailsAt) >= ttl {
		p := l.startDetails(t)
		select {
		case <-p.done:
			if p.err != nil {
				o.ReadError = p.err.Error()
			} else if o.Title, err = l.store.Title(ctx, id); err != nil {
				return Opened{}, err
			}
		case <-ctx.Done():
			return Opened{}, ctx.Err()
		}
	}
	if o.Kind == store.KindSeries {
		if o.Episodes, err = l.store.Episodes(ctx, id); err != nil {
			return Opened{}, err
		}
	}
	return o, nil
}

// detailedFor is how long the library remembers that a read of a title's
// details ended well.
const detailedFor = time.Minute

// startDetails returns the read of a title's details that is under way,
// starting one if there is none. A read that ended well after t was looked
// up counts as done: t is a copy from before it.
func (l *Library) startDetails(t store.Title) *pending {
	l.mu.Lock()
	defer l.mu.Unlock()
	if p, ok := l.details[t.ID]; ok {
		return p
	}
	p := &pending{done: make(chan struct{})}
	if l.base.Err() != nil {
		p.err = l.base.Err()
		close(p.done)
		return p
	}
	if at, ok := l.detailed[t.ID]; ok && t.DetailsAt.Before(at) && l.now().Sub(at) < detailedFor {
		close(p.done)
		return p
	}
	l.details[t.ID] = p
	l.running.Add(1)
	go func() {
		defer l.running.Done()
		p.err = l.readDetails(t)
		l.mu.Lock()
		delete(l.details, t.ID)
		if p.err == nil {
			now := l.now()
			for id, at := range l.detailed {
				if now.Sub(at) >= detailedFor {
					delete(l.detailed, id)
				}
			}
			l.detailed[t.ID] = now
		}
		l.mu.Unlock()
		close(p.done)
	}()
	return p
}

// errNoDetails is the error for a title that its source knows nothing about.
var errNoDetails = errors.New("the source has no details for it")

// readDetails reads a title's details, and a series' episodes, and keeps
// them.
func (l *Library) readDetails(t store.Title) error {
	release, err := l.gate(l.base, t.SourceID)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(l.base, detailsTimeout)
	defer cancel()
	settings, err := l.store.SourceSettings(ctx, t.SourceID)
	if err == nil {
		switch settings.Type {
		case source.TypeXtream:
			err = l.readXtreamDetails(ctx, settings, t)
		case source.TypeJellyfin:
			err = l.readJellyfinDetails(ctx, settings, t)
		case source.TypePlex:
			err = l.readPlexDetails(ctx, settings, t)
		case source.TypeFolder:
			err = l.readFolderDetails(ctx, settings, t)
		case source.TypeStalker:
			err = l.readPortalDetails(ctx, t.SourceID, settings, t)
		default:
			err = errNoLibrary
		}
	}
	if errors.Is(err, xtream.ErrNoTitle) || errors.Is(err, jellyfin.ErrNoTitle) || errors.Is(err, plex.ErrNoTitle) ||
		errors.Is(err, folder.ErrNoTitle) {
		err = errNoDetails
	}
	if err != nil && l.base.Err() == nil {
		l.log.Warn("cannot read a title's details", "source", settings.Name, "title", t.Name, "error", err)
	}
	return err
}

func (l *Library) readXtreamDetails(ctx context.Context, settings source.Settings, t store.Title) error {
	id, err := strconv.ParseInt(t.Key, 10, 64)
	if err != nil {
		return err
	}
	c := l.xtreamClient(settings)
	if t.Kind == store.KindMovie {
		return l.readMovie(ctx, c, t, id)
	}
	return l.readSeries(ctx, c, t, id)
}

func (l *Library) readMovie(ctx context.Context, c *xtream.Client, t store.Title, id int64) error {
	m, err := c.MovieInfo(ctx, id)
	if err != nil {
		return err
	}
	d := store.TitleDetails{
		TitleSummary: store.TitleSummary{Plot: m.Plot, Genre: m.Genre, Cast: m.Cast, Director: m.Director,
			Backdrop: m.Backdrop, Runtime: int(m.Duration / time.Minute)},
		Poster:    m.Poster,
		Country:   m.Country,
		Released:  m.Released,
		AgeRating: m.AgeRating,
		Year:      m.Year,
		Rating:    m.Rating,
		Duration:  int(m.Duration / time.Second),
	}
	return l.store.SetMovieDetails(ctx, t.ID, d, m.Container, l.now())
}

func (l *Library) readSeries(ctx context.Context, c *xtream.Client, t store.Title, id int64) error {
	s, err := c.SeriesInfo(ctx, id)
	if err != nil {
		return err
	}
	d := store.TitleDetails{
		TitleSummary: store.TitleSummary{Plot: s.Plot, Genre: s.Genre, Cast: s.Cast, Director: s.Director,
			Backdrop: s.Backdrop},
		Poster:   s.Poster,
		Released: s.Released,
		Year:     s.Year,
		Rating:   s.Rating,
	}
	for _, season := range s.Seasons {
		d.Seasons = append(d.Seasons, store.SeasonDetails{Number: season.Number, Name: season.Name,
			Poster: season.Poster, Plot: season.Plot})
	}
	episodes := make([]store.LibraryEpisode, len(s.Episodes))
	for i, e := range s.Episodes {
		episodes[i] = store.LibraryEpisode{Key: e.ID, Season: e.Season, Number: e.Number, Name: e.Name,
			Container: e.Container, Plot: e.Plot, Picture: e.Picture, Duration: int(e.Duration / time.Second)}
	}
	return l.store.SetSeriesDetails(ctx, t.ID, d, episodes, l.now())
}

// readJellyfinDetails reads a film or a series of a Jellyfin server again,
// and a series' episodes that have a file. Its seasons are those of its
// episodes.
func (l *Library) readJellyfinDetails(ctx context.Context, settings source.Settings, t store.Title) error {
	c := l.jellyfinClient(settings)
	it, err := c.Item(ctx, t.Key)
	if err != nil {
		return err
	}
	d := store.TitleDetails{
		TitleSummary: jellyfinSummary(c, it),
		Poster:       c.ImageURL(it.ID, "Primary", it.Poster, posterWidth),
		Country:      strings.Join(it.Countries, ", "),
		AgeRating:    it.AgeRating,
		Year:         it.Year,
		Rating:       it.Rating,
	}
	if !it.Premiere.IsZero() {
		d.Released = it.Premiere.Format(time.DateOnly)
	}
	if t.Kind == store.KindMovie {
		d.Duration = int(it.Duration / time.Second)
		return l.store.SetMovieDetails(ctx, t.ID, d, it.Container, l.now())
	}
	list, err := c.Episodes(ctx, t.Key)
	if err != nil {
		return err
	}
	seasons := map[int]bool{}
	episodes := make([]store.LibraryEpisode, len(list))
	for i, e := range list {
		if !seasons[e.Season] {
			seasons[e.Season] = true
			d.Seasons = append(d.Seasons, store.SeasonDetails{Number: e.Season, Name: e.SeasonName})
		}
		episodes[i] = store.LibraryEpisode{Key: e.ID, Season: e.Season, Number: e.Number, Name: e.Name,
			Container: e.Container, Plot: e.Plot, Picture: c.ImageURL(e.ID, "Primary", e.Poster, stillWidth),
			Duration: int(e.Duration / time.Second)}
	}
	return l.store.SetSeriesDetails(ctx, t.ID, d, episodes, l.now())
}

// readPlexDetails reads what a Plex server says of a film, or a show and
// its episodes, which have their own files.
func (l *Library) readPlexDetails(ctx context.Context, settings source.Settings, t store.Title) error {
	c := l.plexClient(settings)
	it, err := c.Item(ctx, t.Key)
	if err != nil {
		return err
	}
	d := store.TitleDetails{
		TitleSummary: plexSummary(it),
		Poster:       plex.PictureAddress(it.Poster, posterWidth, posterHeight),
		Country:      strings.Join(it.Countries, ", "),
		AgeRating:    it.AgeRating,
		Year:         it.Year,
		Rating:       it.Rating,
	}
	if !it.Premiere.IsZero() {
		d.Released = it.Premiere.Format(time.DateOnly)
	}
	if t.Kind == store.KindMovie {
		d.Duration = int(it.Duration / time.Second)
		return l.store.SetMovieDetails(ctx, t.ID, d, it.File.Container, l.now())
	}
	list, err := c.Episodes(ctx, t.Key)
	if err != nil {
		return err
	}
	seasons := map[int]bool{}
	episodes := make([]store.LibraryEpisode, len(list))
	for i, e := range list {
		if !seasons[e.Season] {
			seasons[e.Season] = true
			d.Seasons = append(d.Seasons, store.SeasonDetails{Number: e.Season, Name: e.SeasonName})
		}
		episodes[i] = store.LibraryEpisode{Key: e.Key, Season: e.Season, Number: e.Number, Name: e.Name,
			Container: e.File.Container, File: e.File.Key, Plot: e.Plot,
			Picture: plex.PictureAddress(e.Poster, stillWidth, stillHeight), Duration: int(e.Duration / time.Second)}
	}
	return l.store.SetSeriesDetails(ctx, t.ID, d, episodes, l.now())
}

// folderCategory is the key of a folder's one category, which the folder's
// name names.
const folderCategory = "folder"

// readFolderList reads the films or the series in a folder on the server
// (D67): its whole tree, with the .nfo files and pictures beside them. The
// folder is one category, which its name names.
func (l *Library) readFolderList(ctx context.Context, settings source.Settings, kind string, read *listRead) error {
	f, err := folder.Open(settings.URL)
	if err != nil {
		return err
	}
	defer f.Close()
	read.categories = []store.LibraryCategory{{Key: folderCategory, Name: f.Name()}}
	// With TMDB's key, a title gets what its folder lacks from what TMDB
	// said of it, and one that TMDB has not been asked about is looked up
	// once the list is kept (D88).
	c, err := l.tmdbClient(ctx)
	if err != nil {
		return err
	}
	var known map[string]store.TMDBTitle
	if c != nil {
		if known, err = l.store.TMDBTitles(ctx, read.source, kind); err != nil {
			return err
		}
	}
	withKnown := func(t *store.LibraryTitle, id int64) {
		if c == nil {
			return
		}
		row, asked := known[t.Key]
		if asked && row.ID != 0 {
			withTMDB(t, row.Details)
		}
		if (t.Poster == "" || t.Summary.Plot == "") && l.tmdbDue(row, asked) {
			read.tmdb = append(read.tmdb, tmdbWant{key: t.Key, name: t.Name, year: t.Year, id: id})
		}
	}
	if kind == store.KindMovie {
		films, skipped, err := f.Films(ctx)
		if err != nil {
			return err
		}
		read.skipped = skipped
		for _, film := range films {
			t := store.LibraryTitle{Key: film.Key, Category: folderCategory, Name: film.Name, Year: film.Year,
				Rating: film.Info.Rating, Poster: film.Poster, Container: film.Container, AddedAt: film.Added,
				Summary: folderSummary(film.Info, film.Backdrop)}
			withKnown(&t, film.TMDBID)
			if err := read.keep(t); err != nil {
				return err
			}
		}
		return nil
	}
	shows, skipped, err := f.Shows(ctx)
	if err != nil {
		return err
	}
	read.skipped = skipped
	for _, show := range shows {
		t := store.LibraryTitle{Key: show.Key, Category: folderCategory, Name: show.Name, Year: show.Year,
			Rating: show.Info.Rating, Poster: show.Poster, AddedAt: show.Added,
			Summary: folderSummary(show.Info, show.Backdrop)}
		withKnown(&t, show.TMDBID)
		if err := read.keep(t); err != nil {
			return err
		}
	}
	return nil
}

// folderSummary returns what an .nfo file says about a title beyond its
// name, with its wide picture.
func folderSummary(info folder.Info, backdrop string) store.TitleSummary {
	return store.TitleSummary{Plot: info.Plot, Genre: strings.Join(info.Genres, ", "), Cast: strings.Join(info.Cast, ", "),
		Director: strings.Join(info.Directors, ", "), Backdrop: backdrop, Runtime: int(info.Runtime / time.Minute)}
}

// folderDetails returns what a folder says about a title when it is opened.
func folderDetails(info folder.Info, poster, backdrop string, year int) store.TitleDetails {
	d := store.TitleDetails{
		TitleSummary: folderSummary(info, backdrop),
		Poster:       poster,
		Country:      strings.Join(info.Countries, ", "),
		AgeRating:    info.AgeRating,
		Year:         year,
		Rating:       info.Rating,
	}
	if !info.Premiered.IsZero() {
		d.Released = info.Premiered.Format(time.DateOnly)
	}
	return d
}

// readFolderDetails reads a film in a folder again, or a series with its
// episodes. A series' seasons are those of its episodes.
func (l *Library) readFolderDetails(ctx context.Context, settings source.Settings, t store.Title) error {
	f, err := folder.Open(settings.URL)
	if err != nil {
		return err
	}
	defer f.Close()
	if t.Kind == store.KindMovie {
		film, err := f.Film(ctx, t.Key)
		if err != nil {
			return err
		}
		d := folderDetails(film.Info, film.Poster, film.Backdrop, film.Year)
		d.Duration = int(film.Info.Runtime / time.Second)
		if row, ok := l.openTMDB(ctx, t, film.Name, film.Year, film.TMDBID, d.Poster == "" || d.Plot == "", nil); ok {
			fillDetails(&d, row.Details)
		}
		return l.store.SetMovieDetails(ctx, t.ID, d, film.Container, l.now())
	}
	show, err := f.Show(ctx, t.Key)
	if err != nil {
		return err
	}
	d := folderDetails(show.Info, show.Poster, show.Backdrop, show.Year)
	seasons := map[int]bool{}
	episodes := make([]store.LibraryEpisode, len(show.Episodes))
	for i, e := range show.Episodes {
		if !seasons[e.Season] {
			seasons[e.Season] = true
			d.Seasons = append(d.Seasons, store.SeasonDetails{Number: e.Season})
		}
		episodes[i] = store.LibraryEpisode{Key: e.Key, Season: e.Season, Number: e.Number, Name: e.Name,
			Container: e.Container, Plot: e.Plot, Picture: e.Picture, Duration: int(e.Runtime / time.Second)}
	}
	var numbers []int
	needs := d.Poster == "" || d.Plot == ""
	for _, s := range d.Seasons {
		numbers = append(numbers, s.Number)
	}
	for _, e := range episodes {
		needs = needs || e.Plot == "" || e.Picture == ""
	}
	if row, ok := l.openTMDB(ctx, t, show.Name, show.Year, show.TMDBID, needs, numbers); ok {
		fillDetails(&d, row.Details)
		fillEpisodes(episodes, row.Episodes)
	}
	return l.store.SetSeriesDetails(ctx, t.ID, d, episodes, l.now())
}

// openTMDB returns what TMDB says of a folder's title that a device opened,
// asking TMDB first when the folder lacks something and TMDB has not been
// asked lately, and for a series' episodes of its seasons once TMDB found
// it. ok is false without TMDB's key, or when TMDB has no match. A failed
// request leaves the title as its folder has it, and the log says why.
func (l *Library) openTMDB(ctx context.Context, t store.Title, name string, year int, id int64, needs bool, seasons []int) (store.TMDBTitle, bool) {
	c, err := l.tmdbClient(ctx)
	if err != nil || c == nil {
		return store.TMDBTitle{}, false
	}
	row, asked, err := l.store.TMDBTitleOf(ctx, t.SourceID, t.Kind, t.Key)
	if err != nil {
		return store.TMDBTitle{}, false
	}
	fail := func(err error) (store.TMDBTitle, bool) {
		l.log.Warn("cannot look a title up on TMDB; it shows what its folder has", "title", name, "error", err)
		return row, asked && row.ID != 0
	}
	changed := false
	if needs && l.tmdbDue(row, asked) {
		found, err := l.findTMDB(ctx, c, t.Kind, name, year, id)
		if err != nil {
			return fail(err)
		}
		if found.ID == row.ID {
			found.Episodes = row.Episodes
		}
		row, asked, changed = found, true, true
	}
	if t.Kind == store.KindSeries && row.ID != 0 && len(row.Episodes) == 0 && needs && len(seasons) > 0 {
		eps, err := l.tmdbEpisodes(ctx, c, row.ID, seasons)
		if err != nil && len(eps) == 0 {
			return fail(err)
		}
		row.Episodes, changed = eps, true
	}
	if changed {
		if err := l.store.SetTMDBTitle(ctx, t.SourceID, t.Kind, t.Key, row); err != nil {
			l.log.Error("cannot keep what TMDB says of a title", "title", name, "error", err)
		}
	}
	return row, row.ID != 0
}

// subtitleTimeout caps finding a film's or an episode's subtitles.
const subtitleTimeout = 5 * time.Second

// Subtitles returns the text subtitle tracks that a box can show over a
// film or an episode (D69): the files beside it in a folder, the tracks
// that a Jellyfin server gives of it, or the files beside it that a Plex
// server gives. Others have none that Overscan knows of; nor does one
// whose subtitles cannot be found, which the log says.
func (l *Library) Subtitles(ctx context.Context, item store.PlayableItem) []subtitles.Track {
	ctx, cancel := context.WithTimeout(ctx, subtitleTimeout)
	defer cancel()
	var out []subtitles.Track
	switch {
	case item.Folder != "":
		files, err := folder.Subtitles(item.Folder, item.File)
		if err != nil {
			l.log.Info("cannot look for a title's subtitles", "title", item.Name, "error", err)
			return nil
		}
		for _, f := range files {
			out = append(out, subtitles.Track{Language: f.Language, Title: f.Title, Forced: f.Forced, Hearing: f.Hearing,
				Default: f.Default, Format: f.Format, File: f.Path})
		}
	case item.Key != "":
		settings, err := l.store.SourceSettings(ctx, item.SourceID)
		if err == nil && settings.Type == source.TypePlex {
			return l.plexSubtitles(ctx, settings, item)
		}
		if err != nil || settings.Type != source.TypeJellyfin {
			return nil
		}
		c := l.jellyfinClient(settings)
		mediaSource, subs, err := c.Subtitles(ctx, item.Key)
		if err != nil {
			l.log.Info("cannot ask a source for a title's subtitles", "title", item.Name, "source", settings.Name,
				"error", err)
			return nil
		}
		for _, s := range subs {
			address, format := c.SubtitleURL(item.Key, mediaSource, s)
			out = append(out, subtitles.Track{Language: s.Language, Title: s.Title, Forced: s.Forced, Hearing: s.Hearing,
				Default: s.Default, Format: format, URL: address})
		}
	}
	return out
}

// plexSubtitles returns the subtitle files beside a Plex server's film or
// episode, which the server sends. Subtitles in the file stay with the
// box's player, as they do for other sources.
func (l *Library) plexSubtitles(ctx context.Context, settings source.Settings, item store.PlayableItem) []subtitles.Track {
	c := l.plexClient(settings)
	subs, err := c.Subtitles(ctx, item.Key)
	if err != nil {
		l.log.Info("cannot ask a source for a title's subtitles", "title", item.Name, "source", settings.Name,
			"error", err)
		return nil
	}
	var out []subtitles.Track
	for _, s := range subs {
		address, format := c.SubtitleURL(s)
		if address == "" {
			continue
		}
		out = append(out, subtitles.Track{Language: s.Language, Title: s.Title, Forced: s.Forced, Hearing: s.Hearing,
			Default: s.Default, Format: format, URL: address})
	}
	return out
}
