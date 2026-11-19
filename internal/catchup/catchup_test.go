// SPDX-License-Identifier: AGPL-3.0-or-later

package catchup

import (
	"strings"
	"testing"
	"time"
)

// The fixtures use 4 December 2026, 20:30 UTC.
var (
	from   = time.Date(2026, 12, 4, 20, 30, 0, 0, time.UTC)
	now    = from.Add(26 * time.Hour)
	hourly = time.Hour
)

func zone(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestXtream(t *testing.T) {
	got := Xtream("http://panel.example:8080/", "user one", "p/ss", "1234", "ts", from, 61*time.Minute+30*time.Second, zone(t, "Europe/Berlin"))
	want := "http://panel.example:8080/timeshift/user%20one/p%2Fss/62/2026-12-04:21-30/1234.ts"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := Xtream("http://panel.example", "u", "p", "9", "m3u8", from, time.Second, time.UTC); !strings.HasSuffix(got, "/1/2026-12-04:20-30/9.m3u8") {
		t.Errorf("a minute at least, in UTC: %s", got)
	}
}

func TestXtreamRef(t *testing.T) {
	for ref, want := range map[string][2]string{
		"live/1234.ts":   {"1234", "ts"},
		"live/1234.m3u8": {"1234", "m3u8"},
		"live/77":        {"77", "ts"},
	} {
		id, ext, ok := XtreamRef(ref)
		if !ok || id != want[0] || ext != want[1] {
			t.Errorf("%s: %s %s %v", ref, id, ext, ok)
		}
	}
	for _, ref := range []string{"movie/1.mkv", "live/abc.ts", "live/1.mp4", "1.ts", ""} {
		if _, _, ok := XtreamRef(ref); ok {
			t.Errorf("%q is not a live stream", ref)
		}
	}
}

func TestFromPlaylist(t *testing.T) {
	live := "http://streams.example/live/one/two/1234.ts"
	tests := []struct {
		name          string
		entry, header map[string]string
		live          string
		days          int
		want          Playlist
		ok            bool
	}{
		{"append on the entry", map[string]string{"catchup": "append", "catchup-source": "?utc={utc}", "catchup-days": "5"}, nil,
			"http://s.example/a.m3u8", 5, Playlist{Mode: ModeAppend, Source: "?utc={utc}"}, true},
		{"the header's defaults", map[string]string{}, map[string]string{"catchup": "shift", "catchup-days": "2"},
			"http://s.example/a.ts", 2, Playlist{Mode: ModeShift}, true},
		{"the entry's own days win", map[string]string{"catchup-days": "9"}, map[string]string{"catchup": "shift", "catchup-days": "2"},
			"http://s.example/a.ts", 9, Playlist{Mode: ModeShift}, true},
		{"catchup-type", map[string]string{"catchup-type": "fs", "catchup-days": "1"}, nil,
			"http://s.example/x/mpegts", 1, Playlist{Mode: ModeFlussonic}, true},
		{"legacy timeshift", map[string]string{"timeshift": "4"}, nil,
			"http://s.example/a.ts", 4, Playlist{Mode: ModeShift}, true},
		{"a source alone is default", map[string]string{"catchup-source": "http://a.example/{utc}.ts", "catchup-days": "3"}, nil,
			"http://s.example/a.ts", 3, Playlist{Mode: ModeDefault, Source: "http://a.example/{utc}.ts"}, true},
		{"a query alone is append", map[string]string{"catchup-source": "&t={utc}", "catchup-days": "3"}, nil,
			"http://s.example/a.ts?k=1", 3, Playlist{Mode: ModeAppend, Source: "&t={utc}"}, true},
		{"an Xtream address with days", map[string]string{"catchup-days": "7"}, nil, live, 7, Playlist{Mode: ModeXtream}, true},
		{"a mode without days keeps a day", map[string]string{"catchup": "xc"}, nil, live, 1, Playlist{Mode: ModeXtream}, true},
		{"catchup-time in seconds", map[string]string{"catchup": "shift", "catchup-time": "90000"}, nil,
			"http://s.example/a.ts", 2, Playlist{Mode: ModeShift}, true},
		{"a correction, bounded", map[string]string{"catchup": "shift", "catchup-days": "1", "catchup-correction": "-20"}, nil,
			"http://s.example/a.ts", 1, Playlist{Mode: ModeShift, Correction: -12 * time.Hour}, true},
		{"a correction in hours", map[string]string{"catchup": "shift", "catchup-days": "1"}, map[string]string{"catchup-correction": "1.5"},
			"http://s.example/a.ts", 1, Playlist{Mode: ModeShift, Correction: 90 * time.Minute}, true},
		{"days capped", map[string]string{"catchup": "shift", "catchup-days": "999"}, nil, "http://s.example/a.ts", MaxDays,
			Playlist{Mode: ModeShift}, true},
		{"vod is not played", map[string]string{"catchup": "vod", "catchup-days": "3"}, nil, live, 0, Playlist{}, false},
		{"default without a source", map[string]string{"catchup": "default", "catchup-days": "3"}, nil, live, 0, Playlist{}, false},
		{"days alone, not an Xtream address", map[string]string{"catchup-days": "3"}, nil, "http://s.example/a.ts", 0, Playlist{}, false},
		{"nothing", map[string]string{}, nil, live, 0, Playlist{}, false},
		{"a source with a control character", map[string]string{"catchup": "default", "catchup-source": "http://a.example/\x01", "catchup-days": "3"},
			nil, live, 0, Playlist{}, false},
	}
	for _, tt := range tests {
		days, p, ok := FromPlaylist(tt.entry, tt.header, tt.live)
		if ok != tt.ok || days != tt.days || p != tt.want {
			t.Errorf("%s: %d days, %+v, %v; want %d, %+v, %v", tt.name, days, p, ok, tt.days, tt.want, tt.ok)
		}
	}
}

func TestPlaylistAddress(t *testing.T) {
	berlin := zone(t, "Europe/Berlin")
	tests := []struct {
		name string
		p    Playlist
		live string
		zone *time.Location
		want string
	}{
		{"default with times", Playlist{Mode: ModeDefault, Source: "http://a.example/x/video-{utc}-{duration}.m3u8?k=${end}&n={lutc}"},
			"http://s.example/x.m3u8", time.UTC,
			"http://a.example/x/video-1796416200-3600.m3u8?k=1796419800&n=1796509800"},
		{"date parts in a zone", Playlist{Mode: ModeDefault, Source: "http://a.example/{Y}/{m}/{d}/{H}{M}{S}.ts?d={duration:60}"},
			"http://s.example/x.ts", berlin, "http://a.example/2026/12/04/213000.ts?d=60"},
		{"formatted timestamps", Playlist{Mode: ModeDefault, Source: "http://a.example/{utc:Y-m-dTH:M:S}/${end:YmdHM}"},
			"", time.UTC, "http://a.example/2026-12-04T20:30:00/202612042130"},
		{"offsets", Playlist{Mode: ModeDefault, Source: "http://a.example/?o=${offset}&m={offset:60}&x={unknown}&d=${duration}"},
			"", time.UTC, "http://a.example/?o=93600&m=1560&x={unknown}&d=3600"},
		{"append with ?", Playlist{Mode: ModeAppend, Source: "?utc={utc}&lutc={lutc}"}, "http://s.example/1.ts", time.UTC,
			"http://s.example/1.ts?utc=1796416200&lutc=1796509800"},
		{"append with ? to an address with a query", Playlist{Mode: ModeAppend, Source: "?utc={utc}"},
			"http://s.example/1.ts?token=a", time.UTC, "http://s.example/1.ts?token=a&utc=1796416200"},
		{"shift", Playlist{Mode: ModeShift}, "http://s.example/1.m3u8", time.UTC,
			"http://s.example/1.m3u8?utc=1796416200&lutc=1796509800"},
		{"shift with a query", Playlist{Mode: ModeShift}, "http://s.example/1.m3u8?k=v", time.UTC,
			"http://s.example/1.m3u8?k=v&utc=1796416200&lutc=1796509800"},
		{"a correction", Playlist{Mode: ModeShift, Correction: -time.Hour}, "http://s.example/1.ts", time.UTC,
			"http://s.example/1.ts?utc=1796412600&lutc=1796509800"},
		{"Flussonic HLS", Playlist{Mode: ModeFlussonic}, "http://f.example/news/index.m3u8?token=a", time.UTC,
			"http://f.example/news/index-1796416200-3600.m3u8?token=a"},
		{"Flussonic MPEG-TS", Playlist{Mode: ModeFlussonic}, "http://f.example/news/mpegts", time.UTC,
			"http://f.example/news/timeshift_abs-1796416200.ts"},
		{"Xtream from a live address", Playlist{Mode: ModeXtream}, "http://p.example:8080/live/u%20a/p/1234.ts", berlin,
			"http://p.example:8080/timeshift/u%20a/p/60/2026-12-04:21-30/1234.ts"},
		{"Xtream from a short live address", Playlist{Mode: ModeXtream}, "http://p.example/sub/u/p/77", time.UTC, ""},
		{"Xtream from the shortest live address", Playlist{Mode: ModeXtream}, "http://p.example/u/p/77", time.UTC,
			"http://p.example/timeshift/u/p/60/2026-12-04:20-30/77.ts"},
	}
	for _, tt := range tests {
		got, err := tt.p.Address(tt.live, from, hourly, now, tt.zone)
		if tt.want == "" {
			if err == nil {
				t.Errorf("%s: %s, want an error", tt.name, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%s:\ngot  %s, %v\nwant %s", tt.name, got, err, tt.want)
		}
	}
	if _, err := (Playlist{Mode: ModeDefault, Source: "http://a.example/{catchup-id}"}).Address("", from, hourly, now, time.UTC); err != ErrUnsupported {
		t.Errorf("catchup-id: %v", err)
	}
	if _, err := (Playlist{Mode: ModeFlussonic}).Address("http://f.example/news/stream.ts", from, hourly, now, time.UTC); err != ErrUnsupported {
		t.Errorf("Flussonic without its forms: %v", err)
	}
}

func TestArchiveAddress(t *testing.T) {
	a := Archive{Xtream: &XtreamStream{Server: "http://p.example", Username: "u", Password: "p", ID: "5", Ext: "ts"}}
	if got, err := a.Address(from, hourly, now); err != nil || got != "http://p.example/timeshift/u/p/60/2026-12-04:20-30/5.ts" {
		t.Errorf("Xtream, no zone: %s, %v", got, err)
	}
	a = Archive{Live: "http://s.example/1.ts", Playlist: Playlist{Mode: ModeShift}, Zone: zone(t, "America/New_York")}
	if got, err := a.Address(from, hourly, now); err != nil || got != "http://s.example/1.ts?utc=1796416200&lutc=1796509800" {
		t.Errorf("playlist: %s, %v", got, err)
	}
}

func TestParseMode(t *testing.T) {
	for in, want := range map[string]Mode{"Default": ModeDefault, "append": ModeAppend, "SHIFT": ModeShift, "timeshift": ModeShift,
		"flussonic-hls": ModeFlussonic, "flussonic-ts": ModeFlussonic, "fs": ModeFlussonic, "xtream": ModeXtream, "xc": ModeXtream} {
		if got, ok := ParseMode(in); !ok || got != want {
			t.Errorf("%s: %s %v", in, got, ok)
		}
	}
	for _, in := range []string{"vod", "", "flussonic-dash"} {
		if _, ok := ParseMode(in); ok {
			t.Errorf("%q is not played", in)
		}
	}
}

// FuzzPlaylist reads catch-up attributes as a playlist could give them, and
// makes addresses of what it reads: neither may panic, and an address keeps
// what came before the first placeholder.
func FuzzPlaylist(f *testing.F) {
	f.Add("append", "?utc={utc}&lutc={lutc}", "3", "", "http://s.example/1.ts")
	f.Add("default", "http://a.example/{Y}{m}{d}/${start:YmdHMS}-{duration:60}", "1", "-2.5", "http://s.example/1.ts")
	f.Add("fs", "", "2", "", "http://f.example/x/index.m3u8")
	f.Add("", "", "7", "", "http://p.example/live/u/p/1.ts")
	f.Add("default", "{utc:", "1", "x", "")
	f.Add("default", "${", "1", "NaN", "%zz")
	f.Fuzz(func(t *testing.T, mode, source, days, correction, live string) {
		entry := map[string]string{"catchup": mode, "catchup-source": source, "catchup-days": days, "catchup-correction": correction}
		n, p, ok := FromPlaylist(entry, nil, live)
		if !ok {
			return
		}
		if n < 1 || n > MaxDays || p.Correction < minCorrection || p.Correction > maxCorrection {
			t.Fatalf("%d days, %+v", n, p)
		}
		got, err := p.Address(live, from, hourly, now, time.UTC)
		if err != nil {
			return
		}
		if p.Mode == ModeDefault {
			if i := strings.IndexAny(p.Source, "{$"); i >= 0 && !strings.HasPrefix(got, p.Source[:i]) {
				t.Fatalf("%q from %q", got, p.Source)
			}
		}
	})
}
