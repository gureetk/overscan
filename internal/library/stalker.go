// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Stalker portals (D77). A portal lists its films and series 14 to a page,
// and reading a whole list would take thousands of requests: a crawl (D22).
// So its categories are read as other sources' lists are, when a device
// first asks for films or series and again when they are a day old, and a
// category's pages are read when devices browse its shelf, each kept a day
// too. A film's details are what its page said; a series' seasons and
// episodes are read when a device opens it, in whichever of the three
// shapes the portal gives them (section 6 of the protocol notes).

// PortalPagesPerPart caps the pages of a portal's category that one part of
// its shelf reads.
const PortalPagesPerPart = 4

// pageTimeout bounds a read of a page of a portal's category.
const pageTimeout = 30 * time.Second

// UseStalker makes the library read Stalker portals in the sessions of
// pool, which the catalog and the stream hub share (D76). Call it before
// devices ask for films or series.
func (l *Library) UseStalker(pool *stalker.Pool) { l.stalker = pool }

// portalClient returns the client of a portal's session.
func (l *Library) portalClient(sourceID int64, settings source.Settings) (*stalker.Client, error) {
	return l.stalker.Client(sourceID, stalker.FromSettings(settings))
}

// readPortalCategories reads a portal's categories of films, in its vod
// module, or of series, in its series module, and keeps them, each with its
// module before its ID as its key. A portal without a series module has no
// categories of series: it lists its series among its films. It returns how
// many categories it kept.
func (l *Library) readPortalCategories(ctx context.Context, sourceID int64, settings source.Settings, kind string) (int, error) {
	c, err := l.portalClient(sourceID, settings)
	if err != nil {
		return 0, err
	}
	module := stalker.ModuleVOD
	if kind == store.KindSeries {
		module = stalker.ModuleSeries
	}
	genres, err := c.Categories(ctx, module)
	if kind == store.KindSeries && errors.Is(err, stalker.ErrNoAction) {
		genres, err = nil, nil
	}
	if err != nil {
		return 0, err
	}
	categories := make([]store.LibraryCategory, 0, len(genres))
	for _, g := range genres {
		categories = append(categories, store.LibraryCategory{Key: module + ":" + g.ID, Name: g.Title})
	}
	return len(categories), l.store.ApplyPortalCategories(ctx, sourceID, kind, categories, l.now())
}

// Part is a part of a portal's shelf.
type Part struct {
	Titles []store.TitleListing // films and series alike, in the portal's order
	Total  int                  // how many items the portal says the shelf holds; -1 if not known
	Next   int                  // the place where the next part starts; -1 after the last
}

// PortalPart returns the part of a portal's shelf at places from to
// from+count in the portal's order, counting from 0: the titles on the
// pages that cover it, each read from the portal unless it was read within
// ListTTL, at most PortalPagesPerPart of them, so the part ends where they
// do. A page that cannot be read is what its last read found; one never
// read ends the part, and is an error if it is the part's first.
func (l *Library) PortalPart(ctx context.Context, cat store.PortalCategory, from, count int) (Part, error) {
	size := store.PortalPageSize
	// What the store says now of the shelf's total, which a page read since
	// cat was looked up may have changed.
	cat, err := l.store.PortalCategory(ctx, cat.Kind, cat.ID)
	if err != nil {
		return Part{}, err
	}
	part := Part{Titles: []store.TitleListing{}, Total: cat.Total, Next: -1}
	if from < 0 || count <= 0 {
		return part, nil
	}
	first := from/size + 1
	last := min((from+count-1)/size+1, first+PortalPagesPerPart-1)
	there := 0 // the last page of the part that is kept
	for p := first; p <= last; p++ {
		if part.Total >= 0 && (p-1)*size >= part.Total {
			break
		}
		at, err := l.store.PortalPageReadAt(ctx, cat.ID, p)
		if err != nil {
			return Part{}, err
		}
		if at.IsZero() || l.now().Sub(at) >= ListTTL {
			err := l.portalPage(ctx, cat, p)
			if err == nil {
				if cat, err = l.store.PortalCategory(ctx, cat.Kind, cat.ID); err != nil {
					return Part{}, err
				}
				part.Total = cat.Total
			} else if ctx.Err() != nil {
				return Part{}, ctx.Err()
			} else if at.IsZero() {
				if p == first {
					return Part{}, err
				}
				break
			}
		}
		there = p
	}
	if there == 0 {
		return part, nil
	}
	end := min(from+count, there*size)
	if part.Total >= 0 {
		end = min(end, part.Total)
	}
	if end <= from {
		return part, nil
	}
	titles, err := l.store.PortalTitles(ctx, cat.ID, from, end-from)
	if err != nil {
		return Part{}, err
	}
	part.Titles = titles
	if part.Total < 0 || end < part.Total {
		part.Next = end
	}
	return part, nil
}

// pageKey names a page of a portal's category.
type pageKey struct {
	category int64
	page     int
}

// portalPage reads page p of a portal's category, or waits for the read of
// it that is under way, for as long as ctx lets it. The read goes on when
// ctx ends, so that its page is there for the next device.
func (l *Library) portalPage(ctx context.Context, cat store.PortalCategory, p int) error {
	key := pageKey{cat.ID, p}
	l.mu.Lock()
	pr, ok := l.pages[key]
	if !ok {
		pr = &pending{done: make(chan struct{})}
		if l.base.Err() != nil {
			pr.err = l.base.Err()
			close(pr.done)
		} else {
			l.pages[key] = pr
			l.running.Add(1)
			go func() {
				defer l.running.Done()
				pr.err = l.readPortalPage(cat, p)
				l.mu.Lock()
				delete(l.pages, key)
				l.mu.Unlock()
				close(pr.done)
			}()
		}
	}
	l.mu.Unlock()
	select {
	case <-pr.done:
		return pr.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// readPortalPage reads page p of a portal's category and keeps it.
func (l *Library) readPortalPage(cat store.PortalCategory, p int) error {
	release, err := l.gate(l.base, cat.SourceID)
	if err != nil {
		return err
	}
	defer release()
	ctx, cancel := context.WithTimeout(l.base, pageTimeout)
	defer cancel()
	start := l.now()
	settings, err := l.store.SourceSettings(ctx, cat.SourceID)
	if err == nil && settings.Type != source.TypeStalker {
		err = errNoLibrary
	}
	var page stalker.Page
	if err == nil {
		var c *stalker.Client
		if c, err = l.portalClient(cat.SourceID, settings); err == nil {
			module, id, _ := strings.Cut(cat.Key, ":")
			if page, err = c.Titles(ctx, module, id, p); err == nil {
				items := make([]store.PortalItem, 0, len(page.Items))
				for _, it := range page.Items {
					items = append(items, portalItem(module, it))
				}
				err = l.store.ApplyPortalPage(ctx, cat.ID, p, items, page.Total, l.now())
			}
		}
	}
	if err != nil {
		if l.base.Err() == nil {
			l.log.Warn("cannot read a page of a portal's shelf", "source", settings.Name, "shelf", cat.Name, "page", p,
				"error", err)
		}
		return err
	}
	l.log.Info("read a page of a portal's shelf", "source", settings.Name, "shelf", cat.Name, "page", p,
		"titles", len(page.Items), "total", page.Total, "skipped", page.Skipped,
		"took", l.now().Sub(start).Round(time.Millisecond))
	return nil
}

// portalItem returns an item of a page of a portal's module as the store
// keeps it: a film, which plays by its cmd, or by its first file's; or a
// series, which a series module's items are, and a vod item with its
// episodes' numbers inline or its seasons on request.
func portalItem(module string, it stalker.Item) store.PortalItem {
	t := store.LibraryTitle{Key: module + ":" + it.ID, Name: it.Name, Year: it.Year, Rating: it.Rating,
		Poster: it.Poster, AddedAt: it.Added, Summary: store.TitleSummary{Plot: it.Description, Genre: it.Genres,
			Cast: it.Actors, Director: it.Director, Runtime: it.Minutes}}
	out := store.PortalItem{Kind: store.KindSeries, Title: t}
	switch {
	case module == stalker.ModuleSeries:
		out.Title.File = stalker.Opening{Shape: stalker.ShapeModule, ID: it.ID}.Ref()
	case len(it.Series) > 0 && it.Cmd != "":
		out.Title.File = stalker.Opening{Shape: stalker.ShapeInline, Cmd: it.Cmd, Episodes: it.Series}.Ref()
	case it.IsSeries:
		out.Title.File = stalker.Opening{Shape: stalker.ShapeRequest, Module: module, ID: it.ID}.Ref()
	default:
		out.Kind = store.KindMovie
		switch {
		case it.HasFiles:
			out.Title.File = stalker.Play{Film: it.ID}.Ref()
		case it.Cmd != "":
			out.Title.File = stalker.Play{Cmd: it.Cmd}.Ref()
		}
	}
	return out
}

// readPortalDetails keeps a portal's film's details, which are what its
// page said, and reads a series' seasons and episodes in the shape that
// its page gave.
func (l *Library) readPortalDetails(ctx context.Context, sourceID int64, settings source.Settings, t store.Title) error {
	d := store.TitleDetails{TitleSummary: t.Summary, Poster: t.Poster, Year: t.Year, Rating: t.Rating}
	if t.Kind == store.KindMovie {
		d.Duration = t.Summary.Runtime * 60
		return l.store.SetMovieDetails(ctx, t.ID, d, "", l.now())
	}
	o, ok := stalker.ParseOpening(t.File)
	if !ok {
		return errNoDetails
	}
	c, err := l.portalClient(sourceID, settings)
	if err != nil {
		return err
	}
	var episodes []store.LibraryEpisode
	switch o.Shape {
	case stalker.ShapeInline:
		d.Seasons = []store.SeasonDetails{{Number: 1}}
		for _, n := range o.Episodes {
			episodes = append(episodes, store.LibraryEpisode{Key: strconv.Itoa(n), Season: 1, Number: n,
				File: stalker.Play{Cmd: o.Cmd, Episode: n}.Ref()})
		}
	case stalker.ShapeModule:
		seasons, err := c.SeriesSeasons(ctx, o.ID)
		if err != nil {
			return err
		}
		for _, s := range seasons {
			d.Seasons = append(d.Seasons, store.SeasonDetails{Number: s.Number, Name: s.Name})
			if s.Cmd == "" {
				continue
			}
			for _, n := range s.Episodes {
				episodes = append(episodes, store.LibraryEpisode{Key: s.ID + ":" + strconv.Itoa(n), Season: s.Number,
					Number: n, File: stalker.Play{Cmd: s.Cmd, Episode: n}.Ref()})
			}
		}
	case stalker.ShapeRequest:
		seasons, err := c.Seasons(ctx, o.Module, o.ID)
		if err != nil {
			return err
		}
		for _, s := range seasons {
			d.Seasons = append(d.Seasons, store.SeasonDetails{Number: s.Number, Name: s.Name})
			eps, err := c.Episodes(ctx, o.Module, o.ID, s.ID)
			if err != nil {
				return err
			}
			for _, e := range eps {
				if e.Cmd == "" {
					continue
				}
				episodes = append(episodes, store.LibraryEpisode{Key: e.ID, Season: s.Number, Number: e.Number,
					Name: e.Name, Plot: e.Description, Picture: e.Picture, Duration: e.Minutes * 60,
					File: stalker.Play{Cmd: e.Cmd}.Ref()})
			}
		}
	}
	if len(episodes) == 0 {
		return errNoDetails
	}
	return l.store.SetSeriesDetails(ctx, t.ID, d, episodes, l.now())
}
