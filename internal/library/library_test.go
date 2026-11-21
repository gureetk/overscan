// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
)

// panel is a fictional Xtream panel with films and series. A request can be
// made to wait until release is closed.
type panel struct {
	mu       sync.Mutex
	replies  map[string]string // by action
	calls    map[string]int
	inFlight int
	most     int // the most requests in flight at once
	hold     chan struct{}
}

const password = "pw/secret"

func newPanel(t *testing.T) (*panel, *httptest.Server) {
	t.Helper()
	p := &panel{calls: map[string]int{}, replies: map[string]string{
		"get_vod_categories":    `[{"category_id":"21","category_name":"Drama"},{"category_id":"22","category_name":"Comedy"}]`,
		"get_vod_streams":       `[{"stream_id":5001,"name":"The Long Field","category_id":"21","container_extension":"mkv","added":"1767225600"},{"stream_id":5002,"name":"Quiet Harbour","category_id":"22"}]`,
		"get_series_categories": `[{"category_id":"31","category_name":"Documentary"}]`,
		"get_series":            `[{"series_id":7001,"name":"Coastline","category_id":"31","plot":"Waves."}]`,
		"get_vod_info":          `{"info":{"plot":"A long field.","duration_secs":6730},"movie_data":{"stream_id":5001,"container_extension":"avi"}}`,
		"get_series_info": `{"info":{"name":"Coastline","plot":"Waves, closer."},"seasons":[{"season_number":1,"name":"Season 1"}],
			"episodes":{"1":[{"id":"9001","episode_num":1,"title":"Rocks","container_extension":"mkv"}]}}`,
	}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		action := q.Get("action")
		p.mu.Lock()
		p.calls[action]++
		p.inFlight++
		p.most = max(p.most, p.inFlight)
		hold := p.hold
		reply, ok := p.replies[action]
		p.mu.Unlock()
		defer func() {
			p.mu.Lock()
			p.inFlight--
			p.mu.Unlock()
		}()
		if hold != nil {
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if q.Get("username") != "viewer" || q.Get("password") != password || !ok {
			http.NotFound(w, r)
			return
		}
		if strings.HasPrefix(reply, "503") {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return p, srv
}

func (p *panel) count(action string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[action]
}

func (p *panel) set(action, reply string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.replies[action] = reply
}

// newLibrary returns a library over a store with one Xtream source, and its
// log.
func newLibrary(t *testing.T, srv *httptest.Server) (*Library, *store.Store, int64, *syncBuffer) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), store.FileName),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	t.Cleanup(func() { _ = st.Close() })
	id, err := st.AddSource(context.Background(), source.Settings{Name: "main", Type: source.TypeXtream, URL: srv.URL,
		Username: "viewer", Password: password})
	if err != nil {
		t.Fatal(err)
	}
	logs := &syncBuffer{}
	l := New(st, slog.New(slog.NewTextHandler(logs, nil)), "test")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return l, st, id, logs
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestWantReadsOnce(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, logs := newLibrary(t, srv)
	ctx := context.Background()
	state, err := l.Want(ctx, store.KindMovie, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Ready() || state.Sources != 1 || state.Loading || state.Error != "" {
		t.Fatalf("state %+v", state)
	}
	shelves, err := st.Shelves(ctx, store.KindMovie, store.EverySource)
	if err != nil || len(shelves) != 2 {
		t.Fatalf("shelves %+v, %v", shelves, err)
	}
	if state, _ = l.Want(ctx, store.KindMovie, 0); !state.Ready() || state.Loading {
		t.Errorf("state %+v", state)
	}
	if p.count("get_vod_streams") != 1 || p.count("get_vod_categories") != 1 || p.count("get_series") != 0 {
		t.Errorf("calls %v", p.calls)
	}
	if !strings.Contains(logs.String(), "read a source's films") || strings.Contains(logs.String(), password) {
		t.Errorf("log %s", logs)
	}
}

func TestWantReadsAgainWhenOld(t *testing.T) {
	p, srv := newPanel(t)
	l, _, _, _ := newLibrary(t, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindSeries, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	// A day later the old list is there at once, while a new one is read.
	later := time.Now().Add(ListTTL + time.Minute)
	l.now = func() time.Time { return later }
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
	state, err := l.Want(ctx, store.KindSeries, 5*time.Second)
	if err != nil || !state.Ready() || !state.Loading {
		t.Errorf("state %+v, %v", state, err)
	}
	close(hold)
	waitFor(t, func() bool { return p.count("get_series") == 2 })
}

func TestWantWaitsAndGivesUp(t *testing.T) {
	p, srv := newPanel(t)
	l, _, _, _ := newLibrary(t, srv)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
	state, err := l.Want(context.Background(), store.KindMovie, 50*time.Millisecond)
	if err != nil || state.Ready() || !state.Loading {
		t.Errorf("state %+v, %v", state, err)
	}
	// A second device's request joins the read under way.
	if state, _ = l.Want(context.Background(), store.KindMovie, 0); !state.Loading {
		t.Errorf("state %+v", state)
	}
	close(hold)
	waitFor(t, func() bool {
		st, _ := l.Want(context.Background(), store.KindMovie, 0)
		return st.Ready()
	})
	if n := p.count("get_vod_streams"); n != 1 {
		t.Errorf("%d reads", n)
	}
}

func TestWantFailedRead(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, logs := newLibrary(t, srv)
	ctx := context.Background()
	p.set("get_vod_streams", "503")
	state, err := l.Want(ctx, store.KindMovie, 5*time.Second)
	if err != nil || state.Ready() || state.Loading || !strings.Contains(state.Error, "503") {
		t.Fatalf("state %+v, %v", state, err)
	}
	// It does not try again at once, however often devices ask.
	for range 3 {
		if state, _ = l.Want(ctx, store.KindMovie, time.Second); state.Loading {
			t.Errorf("state %+v", state)
		}
	}
	if n := p.count("get_vod_streams"); n != 1 {
		t.Errorf("%d reads", n)
	}
	reads, _ := st.LibraryReads(ctx)
	if len(reads) != 1 || !strings.Contains(reads[0].Error, "503") || !reads[0].ReadAt.IsZero() {
		t.Errorf("reads %+v", reads)
	}
	if !strings.Contains(logs.String(), "cannot read a source's films") || strings.Contains(logs.String(), srv.URL) {
		t.Errorf("log %s", logs)
	}
	// Ten minutes later it does.
	p.set("get_vod_streams", `[{"stream_id":1,"name":"Back"}]`)
	later := time.Now().Add(retryAfter + time.Minute)
	l.now = func() time.Time { return later }
	if state, _ = l.Want(ctx, store.KindMovie, 5*time.Second); !state.Ready() {
		t.Errorf("state %+v", state)
	}
}

func TestWantWithoutSources(t *testing.T) {
	_, srv := newPanel(t)
	l, st, id, _ := newLibrary(t, srv)
	if err := st.SetSourceEnabled(context.Background(), id, false); err != nil {
		t.Fatal(err)
	}
	state, err := l.Want(context.Background(), store.KindMovie, time.Second)
	if err != nil || state != (State{}) {
		t.Errorf("state %+v, %v", state, err)
	}
	if _, err := l.Want(context.Background(), "radio", 0); err == nil {
		t.Error("a kind that does not exist")
	}
}

func TestOneRequestAtATime(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, _ := newLibrary(t, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	shelves, _ := st.Shelves(ctx, store.KindMovie, store.EverySource)
	titles, _ := st.ShelfTitles(ctx, store.KindMovie, shelves[0].ID, store.ByName, store.EverySource)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
	// The series' list, and two devices opening the same film.
	if _, err := l.Want(ctx, store.KindSeries, 0); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			if _, err := l.Open(ctx, titles[0].ID); err != nil {
				t.Error(err)
			}
		})
	}
	time.Sleep(100 * time.Millisecond)
	close(hold)
	wg.Wait()
	waitFor(t, func() bool { return p.count("get_series") == 1 })
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.most != 1 || p.calls["get_vod_info"] != 1 {
		t.Errorf("at most %d at once; calls %v", p.most, p.calls)
	}
}

func TestOpenMovie(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, _ := newLibrary(t, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	recent, _ := st.RecentTitles(ctx, store.KindMovie, 1, store.EverySource)
	o, err := l.Open(ctx, recent[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Name != "The Long Field" || o.Details == nil || o.Details.Plot != "A long field." || o.Details.Duration != 6730 ||
		o.Container != "avi" || o.ReadError != "" || o.Episodes != nil {
		t.Errorf("opened %+v, details %+v", o, o.Details)
	}
	if o, err = l.Open(ctx, recent[0].ID); err != nil || o.Details == nil || p.count("get_vod_info") != 1 {
		t.Errorf("again: %+v, %v, %d reads", o, err, p.count("get_vod_info"))
	}
	// A week on, it is read again; if that fails, the old details stay.
	later := time.Now().Add(MovieDetailsTTL + time.Hour)
	l.now = func() time.Time { return later }
	p.set("get_vod_info", "503")
	if o, err = l.Open(ctx, recent[0].ID); err != nil || o.Details == nil || !strings.Contains(o.ReadError, "503") {
		t.Errorf("stale: %+v, %v", o, err)
	}
	if _, err := l.Open(ctx, 4242); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown title: %v", err)
	}
}

func TestOpenSeries(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, _ := newLibrary(t, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindSeries, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	shelves, _ := st.Shelves(ctx, store.KindSeries, store.EverySource)
	titles, _ := st.ShelfTitles(ctx, store.KindSeries, shelves[0].ID, store.ByAdded, store.EverySource)
	o, err := l.Open(ctx, titles[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if o.Details == nil || o.Details.Plot != "Waves, closer." || len(o.Details.Seasons) != 1 || len(o.Episodes) != 1 ||
		o.Episodes[0].Name != "Rocks" || o.Summary.Plot != "Waves." {
		t.Errorf("opened %+v", o)
	}
	// A series that the source knows nothing about opens with what the
	// list said.
	p.set("get_series_info", `[]`)
	later := time.Now().Add(SeriesDetailsTTL + time.Hour)
	l.now = func() time.Time { return later }
	if o, err = l.Open(ctx, titles[0].ID); err != nil || o.ReadError != errNoDetails.Error() || len(o.Episodes) != 1 {
		t.Errorf("opened %+v, %v", o, err)
	}
}

func TestOpenGivesUpWithTheDevice(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, _ := newLibrary(t, srv)
	if _, err := l.Want(context.Background(), store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	recent, _ := st.RecentTitles(context.Background(), store.KindMovie, 1, store.EverySource)
	hold := make(chan struct{})
	p.mu.Lock()
	p.hold = hold
	p.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := l.Open(ctx, recent[0].ID); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err %v", err)
	}
	// The read goes on, and the next device finds its result.
	close(hold)
	waitFor(t, func() bool {
		o, err := l.Open(context.Background(), recent[0].ID)
		return err == nil && o.Details != nil
	})
	if n := p.count("get_vod_info"); n != 1 {
		t.Errorf("%d reads", n)
	}
}

// TestOpenAsAReadEnds checks that a device that looked a title up just
// before a read of its details ended does not read them again.
func TestOpenAsAReadEnds(t *testing.T) {
	p, srv := newPanel(t)
	l, st, _, _ := newLibrary(t, srv)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	recent, _ := st.RecentTitles(ctx, store.KindMovie, 1, store.EverySource)
	before, err := st.Title(ctx, recent[0].ID)
	if err != nil || before.Details != nil {
		t.Fatalf("before %+v, %v", before, err)
	}
	if o, err := l.Open(ctx, recent[0].ID); err != nil || o.Details == nil {
		t.Fatalf("open %+v, %v", o, err)
	}
	// The copy from before the read wants the details, which are there.
	<-l.startDetails(before).done
	if n := p.count("get_vod_info"); n != 1 {
		t.Errorf("%d reads", n)
	}
	// A minute later, a copy that old reads them again.
	l.now = func() time.Time { return time.Now().Add(detailedFor) }
	<-l.startDetails(before).done
	if n := p.count("get_vod_info"); n != 2 {
		t.Errorf("a minute later: %d reads", n)
	}
}

// waitFor waits up to five seconds for ok.
func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	for range 500 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out")
}
