// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
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
