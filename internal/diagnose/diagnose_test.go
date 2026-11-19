// SPDX-License-Identifier: AGPL-3.0-or-later

package diagnose

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/hub"
	"overscan.gureet.ca/internal/secrets"
	"overscan.gureet.ca/internal/source"
	"overscan.gureet.ca/internal/store"
	"overscan.gureet.ca/internal/tsinfo/tstest"
)

var base, _ = url.Parse("http://streams.example/live/channel")

func TestDescribePlaylist(t *testing.T) {
	kind, notes := describePlaylist([]byte("#EXTM3U\n"+
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="x",URI="a.m3u8"`+"\n"+
		"#EXT-X-STREAM-INF:BANDWIDTH=2500000,RESOLUTION=1280x720\nhd.m3u8\n"+
		`#EXT-X-STREAM-INF:BANDWIDTH=6000000,AUDIO="a"`+"\nsilent.m3u8\n"), base)
	want := []string{"variant: 2.5 Mbit/s, 720 lines", "variant: 6.0 Mbit/s, sound in a separate stream", "Overscan picks the variant of 2.5 Mbit/s."}
	if kind != "an HLS master playlist with 2 variants" || fmt.Sprint(notes) != fmt.Sprint(want) {
		t.Errorf("master: %q %q", kind, notes)
	}

	kind, notes = describePlaylist([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXT-X-MEDIA-SEQUENCE:7\n"+
		"#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:6,\na.ts\n#EXTINF:6,\nb.ts\n#EXT-X-MAP:URI=\"i.mp4\"\n"), base)
	want = []string{"2 segments, target duration 6.0s, numbered from 7", "Its segments are encrypted with AES-128.", "It has fragmented MP4 segments, which Overscan cannot relay."}
	if kind != "a live HLS playlist" || fmt.Sprint(notes) != fmt.Sprint(want) {
		t.Errorf("media: %q %q", kind, notes)
	}

	if kind, _ := describePlaylist([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:6\n#EXTINF:6,\na.ts\n#EXT-X-ENDLIST\n"), base); kind != "an HLS playlist that has ended" {
		t.Errorf("ended: %q", kind)
	}
	if kind, _ := describePlaylist([]byte("#EXTM3U\n#EXTINF:-1,One\nhttp://streams.example/1.ts\n"), base); kind != "an M3U list of 1 streams, not live HLS" {
		t.Errorf("list: %q", kind)
	}
}

// liveHLS is a fictional source that serves a live HLS stream at /channel,
// an address without .m3u8: two-second segments, cut from one stream so
// that they follow on from each other, of which the playlist lists the
// last four, moving on by one with every load.
func liveHLS(t *testing.T) *httptest.Server {
	t.Helper()
	m := tstest.Bare()
	var segments [][]byte
	for i := range 40 {
		start := len(m.Bytes())
		m.Tables()
		m.Pictures(i*50, i*50+49, "")
		segments = append(segments, bytes.Clone(m.Bytes()[start:]))
	}
	loads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channel" {
			last := min(loads+3, len(segments)-1)
			loads++
			var b strings.Builder
			fmt.Fprintf(&b, "#EXTM3U\n#EXT-X-TARGETDURATION:2\n#EXT-X-MEDIA-SEQUENCE:%d\n", last-3)
			for n := last - 3; n <= last; n++ {
				fmt.Fprintf(&b, "#EXTINF:2.0,\nseg/%d.ts\n", n)
			}
			io.WriteString(w, b.String())
			return
		}
		var n int
		if _, err := fmt.Sscanf(r.URL.Path, "/seg/%d.ts", &n); err != nil || n >= len(segments) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(segments[n])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestCheckAnHLSChannel(t *testing.T) {
	up := liveHLS(t)
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), store.FileName), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	st.UseKey(secrets.NewRandomBox())
	defer st.Close()
	srcID, err := st.AddSource(ctx, source.Settings{Name: "main", URL: "http://provider.example/main.m3u", MaxConnections: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ApplyCatalog(ctx, srcID, []store.CatalogChannel{{IdentityKey: "a", Name: "Live", StreamURL: up.URL + "/channel"}}, time.Now()); err != nil {
		t.Fatal(err)
	}
	channels, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := st.PlayableEntry(ctx, channels[0].EntryID)
	if err != nil {
		t.Fatal(err)
	}

	r := Report{Number: 1, Name: "Live", Source: "main", For: 1500 * time.Millisecond}
	r.Upstream = Source(ctx, http.DefaultClient, entry.StreamURL, "test")
	var logs bytes.Buffer
	h := hub.New(st, slog.New(slog.NewTextHandler(&logs, nil)), "test", map[int64]hub.Source{srcID: {Name: "main", MaxConnections: 1}})
	r.Player = Player(ctx, h, entry, r.For)
	var out strings.Builder
	r.WriteTo(&out)
	text := out.String()

	for _, want := range []string{
		"It sends a live HLS playlist.\n    4 segments, target duration 2.0s, numbered from 0\n",
		"MPEG-TS: ",
		"the stream was still going at the end.",
		"The clock, from the H.264 video: 6.0s of new pictures.",
		"It ran on smoothly, without jumps.",
		"No packets missing, scrambled or out of step.",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in\n%s", want, text)
		}
	}
	if !strings.Contains(logs.String(), "relaying an HLS stream as one continuous stream") {
		t.Errorf("logs: %s", logs.String())
	}
}

func TestSourceThatStartsPartWayThroughAPacket(t *testing.T) {
	m := tstest.New()
	m.Program(0, 50, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(m.Bytes()[100:])
	}))
	defer srv.Close()
	r := Source(context.Background(), http.DefaultClient, srv.URL+"/live.ts", "test")
	if want := "MPEG-TS whose first whole packet starts 88 bytes in"; r.Kind != want {
		t.Errorf("kind %q, want %q", r.Kind, want)
	}
}

func TestSourceThatSendsAShortMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "Geo-blocked: "+strings.Repeat("this channel is not available here. ", 10))
	}))
	defer srv.Close()
	r := Source(context.Background(), http.DefaultClient, srv.URL+"/live.ts", "test")
	if want := `text starting "Geo-blocked: this channel is not available here.`; !strings.HasPrefix(r.Kind, want) {
		t.Errorf("kind %q, want it to start %q", r.Kind, want)
	}
}
