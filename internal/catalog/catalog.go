// SPDX-License-Identifier: AGPL-3.0-or-later

// Package catalog keeps the channel catalog up to date: it reads each enabled
// source when the server starts, when its settings change and then on its
// schedule, and stores what the source lists. An M3U source is a playlist;
// an Xtream source is a server, read through its API (D50). A Jellyfin
// server lists no channels: its read checks that it answers and that the
// API key works, and leaves its films and series to the library (D66). So
// does a folder's: it checks that the folder can be read and holds films or
// series (D67). A Stalker portal is read as the box that its provider
// registered (D76), in the session that the source's streams share.
package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"overscan.gureet.ca/internal/adapters/folder"
	"overscan.gureet.ca/internal/adapters/jellyfin"
	"overscan.gureet.ca/internal/adapters/m3u"
	"overscan.gureet.ca/internal/adapters/plex"
	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/adapters/xtream"
	"overscan.gureet.ca/internal/catchup"
	"overscan.gureet.ca/internal/guide"
	"overscan.gureet.ca/internal/netguard"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Limits on what a refresh reads.
const (
	maxPlaylistBytes = 256 << 20
	fetchTimeout     = 5 * time.Minute
	maxChannels      = 50_000
	maxTextLen       = 200  // bytes of a name, group or guide ID
	maxURLLen        = 4096 // bytes of a stream or logo address
	maxArchiveDays   = 60
)

// Refresher reads sources and stores their catalogs. It reads the sources
// themselves from the store (D46).
type Refresher struct {
	store     *store.Store
	log       *slog.Logger
	client    *http.Client
	userAgent string
	now       func() time.Time
	guide     *guide.Reader // reads each source's guide after its channels (D51)
	stalker   *stalker.Pool // the sessions of Stalker portals (D76)

	reload  chan struct{}
	changed func([]store.Source)

	mu    sync.Mutex
	locks map[int64]*sync.Mutex // by source: one refresh of a source at a time
	// guides holds, by source, the lock that a read of its guide takes:
	// one at a time.
	guides map[int64]*sync.Mutex
	// base is Run's context, under which a guide read that RefreshNow
	// starts goes on; background counts those reads, which Run waits for.
	base       context.Context
	background sync.WaitGroup
	// mayBeEmpty is a folder that may hold no films or series: the
	// recordings folder (D89).
	mayBeEmpty string

	// Overridden by tests.
	maxBytes   int64
	firstRetry time.Duration
}

// NewRefresher returns a refresher that stores catalogs in st. It fetches
// playlists as Overscan/version unless a source names another user agent.
func NewRefresher(st *store.Store, log *slog.Logger, version string) *Refresher {
	return &Refresher{
		store:      st,
		log:        log,
		client:     &http.Client{Timeout: fetchTimeout},
		userAgent:  "Overscan/" + version,
		now:        time.Now,
		guide:      guide.New(st, log, version),
		stalker:    stalker.NewPool(nil),
		reload:     make(chan struct{}, 1),
		locks:      map[int64]*sync.Mutex{},
		guides:     map[int64]*sync.Mutex{},
		maxBytes:   maxPlaylistBytes,
		firstRetry: time.Minute,
	}
}

// OnChange makes the refresher call fn with every source each time it reads
// them: when Run starts, and after each Reload. Call it before Run.
func (r *Refresher) OnChange(fn func([]store.Source)) { r.changed = fn }

// UseStalker makes the refresher, and the guide reader, read Stalker
// portals in the sessions of pool, which the stream hub shares (D76).
// Call it before Run.
func (r *Refresher) UseStalker(pool *stalker.Pool) {
	r.stalker = pool
	r.guide.UseStalker(pool)
}

// Reload makes Run read the sources again, after they changed.
func (r *Refresher) Reload() {
	select {
	case r.reload <- struct{}{}:
	default: // one is pending already
	}
}

// Run refreshes each enabled source now and then every src.Refresh, until
// ctx is done. After a failed refresh it tries again sooner: after a minute,
// then twice as long each time, up to the source's interval. When a source's
// settings change, it starts over with the new ones. After a refresh that
// worked, it reads the source's guide if that is due.
func (r *Refresher) Run(ctx context.Context) {
	r.mu.Lock()
	r.base = ctx
	r.mu.Unlock()
	loops := map[int64]*loop{}
	defer func() {
		for _, l := range loops {
			l.stop()
		}
		r.background.Wait()
	}()
	for ctx.Err() == nil {
		r.sync(ctx, loops)
		select {
		case <-ctx.Done():
		case <-r.reload:
		}
	}
}

// loop refreshes one source on its schedule.
type loop struct {
	settings source.Settings
	cancel   context.CancelFunc
	done     chan struct{}
}

func (l *loop) stop() {
	l.cancel()
	<-l.done
}

// sync starts a loop for each enabled source that has none, and starts again
// those whose settings changed or that are gone or off.
func (r *Refresher) sync(ctx context.Context, loops map[int64]*loop) {
	sources, err := r.store.Sources(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cannot read the sources", "error", err)
		}
		return
	}
	if r.changed != nil {
		r.changed(sources)
	}
	want := map[int64]source.Settings{}
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		settings, err := r.store.SourceSettings(ctx, src.ID)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.unreadable(ctx, src, err)
			continue
		}
		want[src.ID] = settings
	}
	for id, l := range loops {
		if settings, ok := want[id]; !ok || settings != l.settings {
			l.stop()
			delete(loops, id)
		}
	}
	for id, settings := range want {
		if _, ok := loops[id]; !ok {
			loopCtx, cancel := context.WithCancel(ctx)
			l := &loop{settings: settings, cancel: cancel, done: make(chan struct{})}
			go func() {
				defer close(l.done)
				r.loop(loopCtx, settings, id)
			}()
			loops[id] = l
		}
	}
}

// unreadable records why an enabled source cannot be read.
func (r *Refresher) unreadable(ctx context.Context, src store.Source, err error) {
	why := "its address cannot be read"
	switch {
	case errors.Is(err, store.ErrNoAddress):
		why = "it has no address; enter one in the admin"
	case errors.Is(err, store.ErrUnreadable):
		why = "its address was saved with another key file; enter it again in the admin, or put back the key file that saved it"
	}
	r.log.Warn("source cannot be read: "+why, "source", src.Name)
	if err := r.store.SetRefreshResult(ctx, src.ID, r.now(), why); err != nil {
		r.log.Error("cannot record a refresh", "source", src.Name, "error", err)
	}
}

func (r *Refresher) loop(ctx context.Context, src source.Settings, id int64) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	retry := r.firstRetry
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if _, err := r.Refresh(ctx, src, id); err != nil {
			if ctx.Err() != nil {
				return
			}
			timer.Reset(retry)
			retry = min(2*retry, src.Refresh)
			continue
		}
		retry = r.firstRetry
		r.readGuide(ctx, src, id)
		// A refresh and a guide read build large lists that are garbage
		// once stored. Give that memory back to the system now, rather than
		// over the next minutes, so that the server stays small while it
		// idles (the plan's section 15).
		debug.FreeOSMemory()
		timer.Reset(src.Refresh)
	}
}

// RefreshNow reads a source now, whatever its schedule, and stores its
// catalog. If that works and the source's guide is due, as it is after a
// change to the categories that the source brings (D135), it then reads
// the guide too, after it returns.
func (r *Refresher) RefreshNow(ctx context.Context, id int64) (store.CatalogChanges, error) {
	settings, err := r.store.SourceSettings(ctx, id)
	if err != nil {
		return store.CatalogChanges{}, err
	}
	ch, err := r.Refresh(ctx, settings, id)
	if err != nil {
		return ch, err
	}
	r.mu.Lock()
	base := r.base
	if base != nil && base.Err() == nil {
		r.background.Add(1)
	} else {
		base = nil
	}
	r.mu.Unlock()
	if base != nil {
		go func() {
			defer r.background.Done()
			r.readGuide(base, settings, id)
		}()
	}
	return ch, nil
}

// readGuide reads a source's guide if it is due, one read of a source's
// guide at a time. Its failures are logged and recorded; the next is tried
// after guide.RetryAfter, at a later refresh.
func (r *Refresher) readGuide(ctx context.Context, src source.Settings, id int64) {
	if r.guide == nil {
		return
	}
	r.mu.Lock()
	l, ok := r.guides[id]
	if !ok {
		l = &sync.Mutex{}
		r.guides[id] = l
	}
	r.mu.Unlock()
	l.Lock()
	defer l.Unlock()
	_, _, _ = r.guide.ReadIfDue(ctx, src, id)
}

// Tested is what a test of a source found.
type Tested struct {
	Channels int // the channels it lists
	Skipped  int // entries left out: movies, series and unusable addresses or IDs
	// Account is what an Xtream server or a Stalker portal says about the
	// account, nil for other sources and when the server did not say.
	Account *xtream.Account
	// Server is what a media server, Jellyfin or Plex, says about itself,
	// and Libraries are the names of its libraries that hold films or
	// series; nil for other sources.
	Server    *Server
	Libraries []string
	// Folder is a folder's name, and Films and Series what it holds; "" and
	// 0 for other sources.
	Folder        string
	Films, Series int
}

// Server is what a media server says about itself.
type Server struct {
	Name    string // as its owner named it
	Version string // such as 10.11.2
}

// Test reads a source without storing anything, to see whether its settings
// work.
func (r *Refresher) Test(ctx context.Context, src source.Settings) (Tested, error) {
	res, err := r.read(ctx, src, 0)
	if err == nil && len(res.channels) == 0 && src.HasChannels() {
		err = noChannels(src)
	}
	return Tested{Channels: len(res.channels), Skipped: res.skipped, Account: res.account, Server: res.server,
		Libraries: res.libraries, Folder: res.folder, Films: res.films, Series: res.series}, err
}

// noChannels is the error for a source that lists no channels.
func noChannels(src source.Settings) error {
	switch src.Type {
	case source.TypeXtream:
		return errors.New("the server lists no live channels")
	case source.TypeStalker:
		return errors.New("the portal lists no live channels")
	}
	return errors.New("the playlist lists no channels")
}

// lock returns the lock that a source's refreshes take.
func (r *Refresher) lock(id int64) *sync.Mutex {
	r.mu.Lock()
	defer r.mu.Unlock()
	l, ok := r.locks[id]
	if !ok {
		l = &sync.Mutex{}
		r.locks[id] = l
	}
	return l
}

// Refresh reads a source and stores its catalog. If the source cannot be
// read, or lists no channels, the catalog stays as it was. Neither the log
// nor the stored error ever shows the source's address, which can hold
// credentials.
func (r *Refresher) Refresh(ctx context.Context, src source.Settings, id int64) (store.CatalogChanges, error) {
	l := r.lock(id)
	l.Lock()
	defer l.Unlock()
	start := r.now()
	res, err := r.read(ctx, src, id)
	if res.account != nil {
		r.recordAccount(ctx, src, id, *res.account)
	}
	if err == nil && src.Type == source.TypeXtream {
		err = r.adoptPlaylistKeys(ctx, src, id, res.channels)
	}
	if err == nil && len(res.channels) == 0 && src.HasChannels() {
		err = noChannels(src)
	}
	var changes store.CatalogChanges
	if err == nil {
		// For a source without channels, this retires those that it listed
		// as another type.
		changes, err = r.store.ApplyCatalog(ctx, id, res.channels, r.now())
	}
	if err == nil && src.Type != source.TypeXtream && src.Type != source.TypeStalker {
		err = r.store.SetListedGuide(ctx, id, res.listedGuide)
	}
	if err != nil {
		if ctx.Err() != nil {
			return changes, err
		}
		r.log.Warn("source refresh failed; its channels stay as they were", "source", src.Name, "error", err)
		if serr := r.store.SetRefreshResult(ctx, id, r.now(), err.Error()); serr != nil {
			r.log.Error("cannot record a refresh", "source", src.Name, "error", serr)
		}
		return changes, err
	}
	if err := r.store.SetRefreshResult(ctx, id, r.now(), ""); err != nil {
		r.log.Error("cannot record a refresh", "source", src.Name, "error", err)
	}
	switch {
	case res.server != nil:
		r.log.Info("source checked", "source", src.Name, "server_version", res.server.Version,
			"libraries", len(res.libraries), "took", r.now().Sub(start).Round(time.Millisecond))
		return changes, nil
	case src.Type == source.TypeFolder:
		r.log.Info("source checked", "source", src.Name, "films", res.films, "series", res.series,
			"skipped", res.skipped, "took", r.now().Sub(start).Round(time.Millisecond))
		return changes, nil
	}
	r.log.Info("source refreshed", "source", src.Name, "channels", changes.Added+changes.Updated,
		"added", changes.Added, "missing", changes.Missing, "removed", changes.Purged,
		"left_out", changes.LeftOut, "skipped", res.skipped, "took", r.now().Sub(start).Round(time.Millisecond))
	return changes, nil
}

// recordAccount stores what an Xtream server or a Stalker portal said about
// a source's account.
// When the account's limit of streams changes, OnChange's function hears of
// the sources again, so that the stream hub keeps to the new limit.
func (r *Refresher) recordAccount(ctx context.Context, src source.Settings, id int64, a xtream.Account) {
	changed, err := r.store.SetSourceAccount(ctx, id, store.SourceAccount{
		Status: a.Status, ExpiresAt: a.ExpiresAt, MaxConnections: a.MaxConnections, TimeZone: a.TimeZone})
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cannot record a source's account", "source", src.Name, "error", err)
		}
		return
	}
	if !changed || r.changed == nil {
		return
	}
	sources, err := r.store.Sources(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("cannot read the sources", "error", err)
		}
		return
	}
	r.changed(sources)
}

// adoptPlaylistKeys finds, for an Xtream source that was a playlist of the
// same server, the channels that the playlist stored, by the stream IDs in
// their addresses, and makes their keys the AltKeys of the channels it lists
// now. Stream IDs are exact where the names that readXtream matches by can
// differ, so when any channel is found this way, only these AltKeys count.
func (r *Refresher) adoptPlaylistKeys(ctx context.Context, src source.Settings, id int64, channels []store.CatalogChannel) error {
	old, err := r.store.StreamsByKey(ctx, id, xtreamKeyPrefix)
	if err != nil || len(old) == 0 {
		return err
	}
	byStream := map[string]string{}
	for key, address := range old {
		if sid, ok := xtreamStreamID(address, src.Username, src.Password); ok {
			byStream[sid] = key
		}
	}
	if len(byStream) == 0 {
		return nil
	}
	for i := range channels {
		channels[i].AltKey = byStream[strings.TrimPrefix(channels[i].IdentityKey, xtreamKeyPrefix)]
	}
	return nil
}

// xtreamKeyPrefix starts the identity key of an Xtream source's channel,
// before its stream ID.
const xtreamKeyPrefix = "xtream:"

// xtreamStreamID returns the stream ID in the address of a live stream on
// an Xtream server: .../live/USER/PASS/ID.ts or .m3u8, or /USER/PASS/ID
// (docs/protocol/xtream-api-notes.md, section 4). Given a login, USER and
// PASS must be it.
func xtreamStreamID(address, username, password string) (string, bool) {
	u, err := url.Parse(address)
	if err != nil {
		return "", false
	}
	segs := strings.Split(strings.Trim(u.EscapedPath(), "/"), "/")
	n := len(segs)
	if n < 3 || !(n == 3 || segs[n-4] == "live") {
		return "", false
	}
	sid, ext, _ := strings.Cut(segs[n-1], ".")
	if sid == "" || len(sid) > 18 || strings.Trim(sid, "0123456789") != "" || (ext != "" && ext != "ts" && ext != "m3u8") {
		return "", false
	}
	user, err1 := url.PathUnescape(segs[n-3])
	pass, err2 := url.PathUnescape(segs[n-2])
	if err1 != nil || err2 != nil || username != "" && (user != username || pass != password) {
		return "", false
	}
	return sid, true
}

// readResult is what reading a source found.
type readResult struct {
	channels []store.CatalogChannel
	skipped  int             // entries left out: movies, series, and unusable addresses or IDs
	account  *xtream.Account // what an Xtream server or a Stalker portal says about the account
	// listedGuide is the guide's address that a playlist names, "" if none.
	listedGuide string
	// server is what a media server says about itself, and libraries the
	// names of its libraries of films or series.
	server    *Server
	libraries []string
	// folder is a folder's name, and films and series what it holds.
	folder        string
	films, series int
}

// read fetches and parses a source: the one stored under id, or for a test
// of settings, 0.
func (r *Refresher) read(ctx context.Context, src source.Settings, id int64) (readResult, error) {
	switch src.Type {
	case source.TypeXtream:
		return r.readXtream(ctx, src)
	case source.TypeStalker:
		ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
		defer cancel()
		return r.readStalker(ctx, src, id)
	case source.TypeJellyfin:
		return r.readJellyfin(ctx, src)
	case source.TypePlex:
		return r.readPlex(ctx, src)
	case source.TypeFolder:
		return r.readFolder(ctx, src)
	}
	body, err := r.open(ctx, src)
	if err != nil {
		return readResult{}, err
	}
	defer body.Close()

	var entries []m3u.Entry
	skipped := 0
	header, err := m3u.Parse(body, func(e m3u.Entry) error {
		if !usableStream(e.URL) || isMovieOrSeries(e.URL) {
			skipped++
			return nil
		}
		if len(entries) == maxChannels {
			return fmt.Errorf("the playlist lists more than %d channels", maxChannels)
		}
		entries = append(entries, e)
		return nil
	})
	if err != nil {
		return readResult{}, err
	}
	fields := make([]keyFields, len(entries))
	for i, e := range entries {
		name := e.Title
		if strings.TrimSpace(name) == "" {
			name = e.Attrs["tvg-name"]
		}
		fields[i] = keyFields{id: e.Attrs["tvg-id"], name: name, group: e.Group}
	}
	keys := identityKeys(fields)
	channels := make([]store.CatalogChannel, len(entries))
	for i, e := range entries {
		channels[i] = channel(e, header.Attrs, keys[i], i)
		// A playlist from an Xtream server that this source was before
		// finds its channels under their stream IDs.
		if sid, ok := xtreamStreamID(e.URL, "", ""); ok {
			channels[i].AltKey = xtreamKeyPrefix + sid
		}
	}
	return readResult{channels: channels, skipped: skipped, listedGuide: guide.ListedGuide(header.Attrs)}, nil
}

// readXtream reads an Xtream source through the API: the account, then the
// live categories and channels (D50). A channel's identity is the server's
// stream ID; its AltKey is the key that the server's own M3U playlist would
// give it, so that a playlist source that becomes an Xtream source keeps its
// channels. Its stream reference names the format that the account allows,
// ts where it can.
func (r *Refresher) readXtream(ctx context.Context, src source.Settings) (readResult, error) {
	c := xtream.New(src, r.client, r.userAgent)
	c.MaxBytes = r.maxBytes
	var res readResult
	account, err := c.Account(ctx)
	var aerr *xtream.AccountError
	if err == nil || errors.As(err, &aerr) {
		res.account = &account
	}
	if err != nil {
		return res, err
	}
	categories, err := c.LiveCategories(ctx)
	if err != nil {
		return res, fmt.Errorf("live categories: %w", err)
	}
	streams, skipped, err := c.LiveStreams(ctx)
	if err != nil {
		return res, fmt.Errorf("live channels: %w", err)
	}
	names := make(map[string]string, len(categories))
	for _, cat := range categories {
		names[cat.ID] = cat.Name
	}
	seen := make(map[int64]bool, len(streams))
	kept := streams[:0]
	for _, st := range streams {
		if seen[st.ID] {
			skipped++
			continue
		}
		seen[st.ID] = true
		if len(kept) == maxChannels {
			return res, fmt.Errorf("the server lists more than %d channels", maxChannels)
		}
		kept = append(kept, st)
	}
	fields := make([]keyFields, len(kept))
	for i, st := range kept {
		fields[i] = keyFields{id: st.EPGChannelID, name: st.Name, group: names[st.CategoryID]}
	}
	alt := identityKeys(fields)
	format := account.Format()
	res.channels = make([]store.CatalogChannel, len(kept))
	for i, st := range kept {
		id := strconv.FormatInt(st.ID, 10)
		ch := store.CatalogChannel{
			IdentityKey: xtreamKeyPrefix + id,
			AltKey:      alt[i],
			Name:        clean(st.Name),
			Category:    clean(names[st.CategoryID]),
			XMLTVID:     clean(st.EPGChannelID),
			StreamURL:   "live/" + id + "." + format,
			ArchiveDays: min(st.ArchiveDays, maxArchiveDays),
			Logo:        logo(st.Icon),
			Adult:       st.Adult,
			// The Xtream notes take streams of this type for radio
			// stations (section 3, D87).
			Radio: st.Type == "radio_streams",
		}
		if ch.Name == "" {
			ch.Name = "Channel " + strconv.Itoa(i+1)
		}
		if st.Num > 0 && st.Num < 100_000 {
			ch.Number = int(st.Num)
		}
		res.channels[i] = ch
	}
	res.skipped = skipped
	return res, nil
}

// readStalker reads a Stalker portal as the box that its provider
// registered (D76): the account, then the genres and the live channels. A
// channel's identity is the portal's ID for it, its AltKey the key that a
// playlist of the same channels would give it, and its stream reference
// its cmd, which the stream hub turns into a link when it plays. A refresh
// reads in the session that the source's streams share; a test (id 0)
// signs in with a client of its own.
func (r *Refresher) readStalker(ctx context.Context, src source.Settings, id int64) (readResult, error) {
	config := stalker.FromSettings(src)
	var c *stalker.Client
	var err error
	if id == 0 {
		c, err = r.stalker.NewClient(config)
		if err == nil {
			_, err = c.SignIn(ctx)
		}
	} else {
		c, err = r.stalker.Client(id, config)
	}
	if err != nil {
		return readResult{}, err
	}
	var res readResult
	ends, err := c.Account(ctx)
	if errors.Is(err, stalker.ErrNoAction) {
		err = nil // a portal without account_info
	}
	if err != nil {
		return res, err
	}
	if ends.IsZero() {
		ends = c.Profile().Expires
	}
	res.account = &xtream.Account{Status: "Active", ExpiresAt: ends}
	if !ends.IsZero() && ends.Before(r.now()) {
		res.account.Status = "Expired"
	}
	genres, err := c.Genres(ctx)
	if err != nil {
		return res, err
	}
	channels, skipped, err := c.Channels(ctx)
	if err != nil {
		return res, err
	}
	if len(channels) > maxChannels {
		return res, fmt.Errorf("the portal lists more than %d channels", maxChannels)
	}
	names := make(map[string]string, len(genres))
	censored := map[string]bool{}
	for _, g := range genres {
		names[g.ID] = g.Title
		censored[g.ID] = g.Censored
	}
	fields := make([]keyFields, len(channels))
	for i, ch := range channels {
		fields[i] = keyFields{id: ch.XMLTVID, name: ch.Name, group: names[ch.GenreID]}
	}
	alt := identityKeys(fields)
	res.channels = make([]store.CatalogChannel, len(channels))
	for i, ch := range channels {
		out := store.CatalogChannel{
			IdentityKey: source.StalkerKeyPrefix + ch.ID,
			AltKey:      alt[i],
			Name:        clean(ch.Name),
			Category:    clean(names[ch.GenreID]),
			XMLTVID:     clean(ch.XMLTVID),
			StreamURL:   ch.Cmd,
			ArchiveDays: min((ch.ArchiveHours+23)/24, maxArchiveDays),
			Logo:        logo(ch.Logo),
			Adult:       ch.Censored || censored[ch.GenreID],
		}
		if out.Name == "" {
			out.Name = "Channel " + strconv.Itoa(i+1)
		}
		if ch.Number > 0 && ch.Number < 100_000 {
			out.Number = ch.Number
		}
		res.channels[i] = out
	}
	res.skipped = skipped
	return res, nil
}

// readJellyfin checks a Jellyfin server (D66): that it answers, that it
// takes the API key and, for a source that names a user, that the user is
// there; and finds the libraries of films and series that boxes will see.
// A server without any is an error, since boxes would find nothing.
func (r *Refresher) readJellyfin(ctx context.Context, src source.Settings) (readResult, error) {
	c := jellyfin.New(src, r.client, r.userAgent)
	c.MaxBytes = r.maxBytes
	info, err := c.Info(ctx)
	if err != nil {
		return readResult{}, err
	}
	libraries, err := c.Libraries(ctx)
	if err != nil {
		return readResult{}, err
	}
	if len(libraries) == 0 {
		if src.Username != "" {
			return readResult{}, fmt.Errorf("the user %q sees no libraries of films or shows", src.Username)
		}
		return readResult{}, errors.New("the server has no libraries of films or shows")
	}
	res := readResult{server: &Server{Name: info.Name, Version: info.Version}}
	for _, l := range libraries {
		res.libraries = append(res.libraries, l.Name)
	}
	return res, nil
}

// readPlex checks a Plex server (D72): that it answers and takes the token;
// and finds the libraries of films and shows that boxes will see, which are
// the ones the token's account sees. A server without any is an error,
// since boxes would find nothing.
func (r *Refresher) readPlex(ctx context.Context, src source.Settings) (readResult, error) {
	c := plex.New(src, r.client, r.userAgent)
	c.MaxBytes = r.maxBytes
	info, err := c.Info(ctx)
	if err != nil {
		return readResult{}, err
	}
	libraries, err := c.Libraries(ctx)
	if err != nil {
		return readResult{}, err
	}
	if len(libraries) == 0 {
		return readResult{}, errors.New("the token's account sees no libraries of films or shows")
	}
	res := readResult{server: &Server{Name: info.Name, Version: info.Version}}
	for _, l := range libraries {
		res.libraries = append(res.libraries, l.Name)
	}
	return res, nil
}

// AllowEmpty lets the folder at dir hold no films or series without its
// source's checks failing: the recordings folder, which is empty until the
// first recording is made (D89).
func (r *Refresher) AllowEmpty(dir string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mayBeEmpty = filepath.Clean(dir)
}

// readFolder checks a folder (D67): that it can be read, and that it holds
// films or series, which it counts, unless it may be empty. Videos that
// cannot be read are counted as skipped.
func (r *Refresher) readFolder(ctx context.Context, src source.Settings) (readResult, error) {
	f, err := folder.Open(src.URL)
	if err != nil {
		return readResult{}, err
	}
	defer f.Close()
	films, series, skipped, err := f.Count(ctx)
	if err != nil {
		return readResult{}, err
	}
	r.mu.Lock()
	mayBeEmpty := r.mayBeEmpty != "" && filepath.Clean(src.URL) == r.mayBeEmpty
	r.mu.Unlock()
	if films+series == 0 && !mayBeEmpty {
		return readResult{}, errors.New("the folder holds no films or series")
	}
	return readResult{folder: f.Name(), films: films, series: series, skipped: skipped}, nil
}

// open returns the playlist's content.
func (r *Refresher) open(ctx context.Context, src source.Settings) (io.ReadCloser, error) {
	if src.IsFile() {
		f, err := openPlaylistFile(src.URL)
		if err != nil {
			return nil, err
		}
		return &capReader{r: f, c: f, left: r.maxBytes}, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.URL, nil)
	if err != nil {
		return nil, errors.New("the address is not valid")
	}
	ua := src.UserAgent
	if ua == "" {
		ua = r.userAgent
	}
	req.Header.Set("User-Agent", ua)
	resp, err := netguard.Client(r.client, src.PublicOnly).Do(req)
	if err != nil {
		// A url.Error quotes the address; keep only what went wrong.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("the server answered %s", resp.Status)
	}
	return &capReader{r: resp.Body, c: resp.Body, left: r.maxBytes}, nil
}

// Why a playlist file cannot be read, without its path, which is sealed
// as a source's address is (D46).
var (
	errPlaylistMissing    = errors.New("the playlist file does not exist")
	errPlaylistDenied     = errors.New("Overscan may not read the playlist file")
	errPlaylistNotRegular = errors.New("the playlist is not a regular file")
)

// openPlaylistFile opens a playlist file, which must be a regular file: a
// named pipe or a device could hold a refresh up for ever, or give data
// without end, and a directory is no playlist.
func openPlaylistFile(path string) (*os.File, error) {
	opened := func(err error) error {
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return errPlaylistMissing
		case errors.Is(err, fs.ErrPermission):
			return errPlaylistDenied
		}
		return errors.New("the playlist file cannot be read")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, opened(err)
	}
	if !info.Mode().IsRegular() {
		return nil, errPlaylistNotRegular
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, opened(err)
	}
	// What the path names may have changed since it was looked at.
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, errPlaylistNotRegular
	}
	return f, nil
}

// capReader fails once more than left bytes have been read.
type capReader struct {
	r    io.Reader
	c    io.Closer
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left < 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > c.left+1 {
		p = p[:c.left+1]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left < 0 {
		return n, errTooLarge
	}
	if err != nil {
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
	}
	return n, err
}

func (c *capReader) Close() error { return c.c.Close() }

var errTooLarge = fmt.Errorf("the playlist is larger than %d MiB", maxPlaylistBytes>>20)

// usableStream reports whether s looks like a stream address: a URL with a
// scheme, not too long.
func usableStream(s string) bool {
	if len(s) > maxURLLen {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && u.Scheme != "" && (u.Host != "" || u.Opaque != "")
}

// isMovieOrSeries reports whether a stream address is a movie or an episode
// in the layout that Xtream-style providers use in their M3U playlists
// (/movie/... and /series/...). Those belong to libraries, not to live TV.
func isMovieOrSeries(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	p := strings.ToLower(u.Path)
	return strings.Contains(p, "/movie/") || strings.Contains(p, "/series/")
}

// channel turns an entry into a catalog channel, with the playlist's
// header attributes as defaults for its catch-up (D82). i is its place in
// the playlist, for a fallback name.
func channel(e m3u.Entry, header map[string]string, key string, i int) store.CatalogChannel {
	name := clean(e.Title)
	if name == "" {
		name = clean(e.Attrs["tvg-name"])
	}
	if name == "" {
		name = "Channel " + strconv.Itoa(i+1)
	}
	c := store.CatalogChannel{
		IdentityKey: key,
		Name:        name,
		Category:    clean(e.Group),
		XMLTVID:     clean(e.Attrs["tvg-id"]),
		StreamURL:   e.URL,
		// radio="true" marks a radio station (playlist notes, D87).
		Radio: strings.EqualFold(strings.TrimSpace(e.Attrs["radio"]), "true"),
		Adult: playlistAdult(e.Attrs),
	}
	if n, err := strconv.Atoi(e.Attrs["tvg-chno"]); err == nil && n > 0 && n < 100_000 {
		c.Number = n
	}
	c.Logo = logo(e.Attrs["tvg-logo"])
	if days, archive, ok := catchup.FromPlaylist(e.Attrs, header, e.URL); ok {
		c.ArchiveDays, c.Catchup = min(days, maxArchiveDays), archive
	} else if days, err := strconv.Atoi(e.Attrs["catchup-days"]); err == nil && days > 0 {
		// An archive that Overscan cannot play: its guide is kept as long.
		c.ArchiveDays = min(days, maxArchiveDays)
	}
	return c
}

// playlistAdult reports whether a playlist's attributes mark a channel for
// adults: adult="1" or "true", in any case, or any parent-code (playlist
// notes, D86).
func playlistAdult(attrs map[string]string) bool {
	switch strings.ToLower(strings.TrimSpace(attrs["adult"])) {
	case "1", "true":
		return true
	}
	return strings.TrimSpace(attrs["parent-code"]) != ""
}

// logo returns s if it is an http or https address that is not too long,
// and "" otherwise.
func logo(s string) string {
	if len(s) > maxURLLen {
		return ""
	}
	if u, err := url.Parse(s); err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
		return s
	}
	return ""
}

// keyFields are what a playlist's identity key is made from: tvg-id, the
// name and the group.
type keyFields struct{ id, name, group string }

// identityKeys gives each entry of a playlist a key that stays the same
// across refreshes (plan, section 4): its tvg-id when no other entry shares
// it, or else its normalized name and group, with a running number for
// repeats.
func identityKeys(entries []keyFields) []string {
	ids := map[string]int{}
	for _, e := range entries {
		if id := strings.TrimSpace(e.id); id != "" {
			ids[id]++
		}
	}
	keys := make([]string, len(entries))
	seen := map[string]int{}
	for i, e := range entries {
		if id := strings.TrimSpace(e.id); id != "" && ids[id] == 1 {
			keys[i] = "id:" + id
			continue
		}
		key := "name:" + normalize(e.name) + "|" + normalize(e.group)
		seen[key]++
		if n := seen[key]; n > 1 {
			key += "#" + strconv.Itoa(n)
		}
		keys[i] = key
	}
	return keys
}

// normalize lower-cases s and collapses its white space.
func normalize(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// clean trims s, replaces control characters and runs of white space with
// single spaces, and cuts it to maxTextLen bytes.
func clean(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
	if len(s) <= maxTextLen {
		return s
	}
	n := maxTextLen
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
}
