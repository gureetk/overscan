// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"time"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// Pictures from TMDB for the films and series of Xtream servers and
// Stalker portals (D143). Where the administrator turns them on and has
// given TMDB's key (D88), a title whose poster its source gives none of,
// or one that does not load, or one that many of its titles share, is
// looked up on TMDB: by the TMDB ID that its source gives, else by its
// name and year, as a folder's title is. Lookups run in the background,
// one request at a time, at most tmdbBatch titles a run: after each read
// of an Xtream server's list, when the setting or the key changes, and
// every hour, which also covers the titles of a portal's pages that
// devices browsed, and the pictures found not to load.

// PicturesEvery is how often the library looks at its providers'
// pictures.
const PicturesEvery = time.Hour

// hasPictures reports whether a source's titles take TMDB's pictures.
func hasPictures(typ string) bool { return typ == source.TypeXtream || typ == source.TypeStalker }

// FillPictures starts a run for the films and the series of every enabled
// Xtream server and Stalker portal, unless one is under way. It does not
// wait for them.
func (l *Library) FillPictures(ctx context.Context) {
	sources, err := l.store.Sources(ctx)
	if err != nil {
		l.log.Warn("cannot list the sources to look their pictures up on TMDB", "error", err)
		return
	}
	for _, src := range sources {
		if src.Enabled && hasPictures(src.Type) {
			for _, kind := range []string{store.KindMovie, store.KindSeries} {
				l.startPictures(src, kind)
			}
		}
	}
}

// startPictures runs fillPictures for a source's films or series in the
// background, unless a run for them is under way.
func (l *Library) startPictures(src store.Source, kind string) {
	key := listKey{src.ID, kind}
	l.mu.Lock()
	if l.pictureRuns[key] || l.base.Err() != nil {
		l.mu.Unlock()
		return
	}
	l.pictureRuns[key] = true
	l.running.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.running.Done()
		l.fillPictures(src, kind)
		l.mu.Lock()
		delete(l.pictureRuns, key)
		l.mu.Unlock()
	}()
}

// fillPictures gives a source's films or series TMDB's pictures where
// theirs want replacing, and their own back where they no longer do, and
// looks up on TMDB those that it has not been asked about. It stops at the
// first sign that TMDB will not answer.
func (l *Library) fillPictures(src store.Source, kind string) {
	ctx := l.base
	if on, err := l.store.TMDBPictures(ctx); err != nil || !on {
		return
	}
	c, err := l.tmdbClient(ctx)
	if err != nil || c == nil {
		return
	}
	what := map[string]string{store.KindMovie: "films", store.KindSeries: "series"}[kind]
	start := l.now()
	restored, filled, err := l.store.SettlePictures(ctx, src.ID, kind)
	if err != nil {
		l.log.Error("cannot give titles TMDB's pictures", "source", src.Name, "error", err)
		return
	}
	wants, err := l.store.PictureWants(ctx, src.ID, kind, l.now().Add(-tmdbMissTTL), tmdbBatch)
	if err != nil {
		l.log.Error("cannot give titles TMDB's pictures", "source", src.Name, "error", err)
		return
	}
	asked, found := 0, 0
	for _, w := range wants {
		names, year := tmdbQueries(w.Name, w.Year)
		var t store.TMDBTitle
		var err error
		for _, name := range names {
			if t, err = l.findTMDB(ctx, c, kind, name, year, w.TMDBID); err != nil || t.ID != 0 || w.TMDBID != 0 {
				break
			}
		}
		if err != nil {
			if ctx.Err() == nil {
				l.log.Warn("cannot look a source's "+what+" up on TMDB for their pictures; the next run tries again",
					"source", src.Name, "title", w.Name, "error", err)
			}
			break
		}
		if err := l.store.SetTMDBTitle(ctx, src.ID, kind, w.Key, t); err != nil {
			l.log.Error("cannot keep what TMDB says of a title", "source", src.Name, "title", w.Name, "error", err)
			break
		}
		asked++
		if t.ID != 0 && t.Details.Poster != "" {
			if err := l.store.SetPictureFill(ctx, w.TitleID, t.Details.Poster, t.Details.Backdrop); err != nil {
				l.log.Error("cannot give titles TMDB's pictures", "source", src.Name, "error", err)
				break
			}
			found++
		}
	}
	if restored+filled+asked > 0 {
		l.log.Info("gave a source's "+what+" TMDB's pictures where theirs want replacing", "source", src.Name,
			"asked", asked, "found", found, "filled", filled+found, "restored", restored, "left", len(wants)-asked,
			"took", l.now().Sub(start).Round(time.Millisecond))
	}
}

// Words and marks that providers put in titles' names beyond the name: a
// country or language before it, such as "EN - " or "[UK]", and the
// quality or language of its file after it.
var (
	leadingTag  = regexp.MustCompile(`^\s*(?:[\[(|]\s*[A-Za-z]{2,3}(?:[-_][A-Za-z]{2})?\s*[\])|]\s*[:|\-–]?|[A-Z]{2,3}(?:[-_][A-Z]{2})?\s*[:|\-–])\s*`)
	bracketed   = regexp.MustCompile(`\s*\[[^\]]*\]\s*`)
	yearMark    = regexp.MustCompile(`\s*[(\[]\s*((?:19|20)\d\d)\s*[)\]]\s*`)
	trailYear   = regexp.MustCompile(`\s+[-–]\s*((?:19|20)\d\d)\s*$`)
	qualityMark = regexp.MustCompile(`(?i)\s*\(\s*(?:` + qualityWords + `)(?:[\s/,+]+(?:` + qualityWords + `))*\s*\)\s*$`)
	qualityTail = regexp.MustCompile(`(?i)(?:\s+|\s*[-–|]\s*)(?:` + qualityWords + `)\s*$`)
)

const qualityWords = `4k|uhd|fhd|hd|sd|hevc|h\.?26[45]|x26[45]|1080p|720p|2160p|480p|hdr(?:10)?|multi(?:[- ]?subs?|[- ]?audio)?|subbed|dubbed|vostfr|vo|vf`

// tmdbQueries returns the names and the year to look a provider's title
// up on TMDB by: its name without marks in brackets or its file's quality
// after it, and without a country or language before it, such as "EN - "
// or "[UK]"; and, where it had one, with it too, as some names begin with
// what looks like one ("IT: Chapter Two"). Its year is its source's, or
// else the one in its name.
func tmdbQueries(name string, year int) ([]string, int) {
	s := strings.TrimSpace(name)
	if m := yearMark.FindStringSubmatch(s); m != nil {
		if year == 0 {
			year, _ = strconv.Atoi(m[1])
		}
		s = yearMark.ReplaceAllString(s, " ")
	}
	for {
		t := qualityTail.ReplaceAllString(qualityMark.ReplaceAllString(s, ""), "")
		if t == s {
			break
		}
		s = t
	}
	if m := trailYear.FindStringSubmatch(s); m != nil && strings.TrimSpace(s[:len(s)-len(m[0])]) != "" {
		if year == 0 {
			year, _ = strconv.Atoi(m[1])
		}
		s = s[:len(s)-len(m[0])]
	}
	whole := strings.Join(strings.Fields(bracketed.ReplaceAllString(s, " ")), " ")
	bare := strings.Join(strings.Fields(bracketed.ReplaceAllString(leadingTag.ReplaceAllString(s, ""), " ")), " ")
	switch {
	case bare == "":
		return []string{whole}, year
	case bare != whole && whole != "":
		return []string{bare, whole}, year
	}
	return []string{bare}, year
}
