// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// A source that brings only some categories of films reads its whole list,
// as before, and keeps only theirs; ReadList reads it again at once, so that
// a change takes effect (D135).
func TestReadListKeepsTickedCategories(t *testing.T) {
	p, srv := newPanel(t)
	l, st, id, logs := newLibrary(t, srv)
	ctx := context.Background()
	if err := l.ReadList(ctx, id, store.KindMovie); err != nil {
		t.Fatal(err)
	}
	if got := p.count("get_vod_streams"); got != 1 {
		t.Errorf("list read %d times", got)
	}
	cats, _, err := st.SourceCategories(ctx, id, store.KindMovie, "")
	if err != nil || len(cats) != 2 || !cats[0].Ticked || cats[0].Items != 1 {
		t.Fatalf("categories %+v, %v", cats, err)
	}

	if _, err := st.SetCategoryChoice(ctx, id, store.KindMovie, true, false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.TickCategories(ctx, id, store.KindMovie, map[string]bool{"22": false}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := l.ReadList(ctx, id, store.KindMovie); err != nil {
		t.Fatal(err)
	}
	if got := p.count("get_vod_streams"); got != 2 || p.count("get_vod_categories") != 2 {
		t.Errorf("list read %d times", got)
	}
	shelves, err := st.Shelves(ctx, store.KindMovie, store.Sources{})
	if err != nil || len(shelves) != 1 || shelves[0].Name != "Drama" || shelves[0].Count != 1 {
		t.Errorf("shelves %+v, %v", shelves, err)
	}
	if !strings.Contains(logs.String(), "left_out=1") {
		t.Errorf("the log does not say what it left out:\n%s", logs.String())
	}

	// Only a source that has films and series, and is on, has a list.
	if err := l.ReadList(ctx, 99, store.KindMovie); !errors.Is(err, ErrNoList) {
		t.Errorf("an unknown source: %v", err)
	}
	if err := st.SetSourceEnabled(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := l.ReadList(ctx, id, store.KindSeries); !errors.Is(err, ErrNoList) {
		t.Errorf("a source that is off: %v", err)
	}
	if err := l.ReadList(ctx, id, "episode"); err == nil {
		t.Error("a list of episodes")
	}
}

// ReadList gives up waiting with its context, and the read goes on.
func TestReadListGivesUp(t *testing.T) {
	p, srv := newPanel(t)
	l, st, id, _ := newLibrary(t, srv)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := l.ReadList(ctx, id, store.KindSeries); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a read that takes long: %v", err)
	}
	close(hold)
	waitFor(t, func() bool {
		reads, err := st.LibraryReads(context.Background())
		return err == nil && len(reads) == 1 && !reads[0].ReadAt.IsZero()
	})
}
