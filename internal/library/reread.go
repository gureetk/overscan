// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// ReadFolderAgain reads again the lists of films and of series of the
// enabled folder sources that read dir, so that a recording made or deleted
// there shows on boxes at once (D89), and not only once the lists are due.
// It does not wait for the reads.
func (l *Library) ReadFolderAgain(ctx context.Context, dir string) {
	sources, err := l.store.Sources(ctx)
	if err != nil {
		l.log.Warn("cannot list the sources to read a folder again", "error", err)
		return
	}
	dir = filepath.Clean(dir)
	for _, src := range sources {
		if !src.Enabled || src.Type != source.TypeFolder {
			continue
		}
		settings, err := l.store.SourceSettings(ctx, src.ID)
		if err != nil || filepath.Clean(settings.URL) != dir {
			continue
		}
		for _, kind := range []string{store.KindMovie, store.KindSeries} {
			l.readAgain(src, kind)
		}
	}
}

// ReadFoldersAgain reads again the lists of films and of series of every
// enabled folder source, so that a new key for TMDB, or a new language,
// takes effect at once (D88). It does not wait for the reads.
func (l *Library) ReadFoldersAgain(ctx context.Context) {
	sources, err := l.store.Sources(ctx)
	if err != nil {
		l.log.Warn("cannot list the sources to read the folders again", "error", err)
		return
	}
	for _, src := range sources {
		if src.Enabled && src.Type == source.TypeFolder {
			for _, kind := range []string{store.KindMovie, store.KindSeries} {
				l.readAgain(src, kind)
			}
		}
	}
}

// readAgain starts a new read of a source's list, after the one under way,
// if any, which may have begun before the change.
func (l *Library) readAgain(src store.Source, kind string) {
	l.mu.Lock()
	if l.base.Err() != nil {
		l.mu.Unlock()
		return
	}
	l.running.Add(1)
	l.mu.Unlock()
	go func() {
		defer l.running.Done()
		if p := l.startList(src, kind, false); p != nil {
			select {
			case <-p.done:
			case <-l.base.Done():
				return
			}
		}
		l.startList(src, kind, true)
	}()
}

// ErrNoList is the error for a source that has no list of films or of
// series to read: one that is off, has none, or is gone.
var ErrNoList = errors.New("the source has no such list to read")

// ReadList reads a source's list of films or of series now, after the read
// under way if any, which may have begun before a change, and waits for it
// until ctx is done. The read goes on after ctx is done, for the next
// device that asks. A change to the categories that the source brings
// (D135) takes effect so.
func (l *Library) ReadList(ctx context.Context, sourceID int64, kind string) error {
	if kind != store.KindMovie && kind != store.KindSeries {
		return fmt.Errorf("library: %q is not a kind of title", kind)
	}
	src, err := l.store.Source(ctx, sourceID)
	if errors.Is(err, store.ErrNotFound) || err == nil && (!src.Enabled || !src.Settings().HasLibrary()) {
		return ErrNoList
	}
	if err != nil {
		return err
	}
	if p := l.startList(src, kind, false); p != nil {
		select {
		case <-p.done:
		case <-ctx.Done():
			l.readAgain(src, kind)
			return ctx.Err()
		}
	}
	p := l.startList(src, kind, true)
	if p == nil {
		return ErrNoList // the library is stopping
	}
	select {
	case <-p.done:
		return p.err
	case <-ctx.Done():
		return ctx.Err()
	}
}
