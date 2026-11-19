// SPDX-License-Identifier: AGPL-3.0-or-later

package m3u

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func parseAll(t *testing.T, input string) (Header, []Entry, error) {
	t.Helper()
	var entries []Entry
	h, err := Parse(strings.NewReader(input), func(e Entry) error {
		entries = append(entries, e)
		return nil
	})
	return h, entries, err
}

// A fictional playlist.
const playlist = "\ufeff#EXTM3U url-tvg=\"http://guide.example/epg.xml\" x-tvg-url=\"http://guide.example/b.xml\"\r\n" +
	"#EXTINF:-1 tvg-id=\"news.example\" tvg-name=\"Example News\" tvg-logo=\"http://logos.example/news.png\" tvg-chno=\"101\" group-title=\"News, Weather\",Example News HD\r\n" +
	"http://streams.example/live/101.ts\r\n" +
	"\r\n" +
	"#EXTINF:0 tvg-id=sports.example catchup=\"default\" catchup-days=\"7\",Sports, Live\n" +
	"#EXTGRP:Sports\n" +
	"#EXTVLCOPT:http-user-agent=TestPlayer\n" +
	"http://streams.example/live/102.ts\n" +
	"# a comment\n" +
	"http://streams.example/orphan.ts\n" +
	"#extinf:-1,Lower Case Tag\n" +
	"udp://@239.0.0.1:1234\n"

func TestParse(t *testing.T) {
	h, entries, err := parseAll(t, playlist)
	if err != nil {
		t.Fatal(err)
	}
	if h.Attrs["url-tvg"] != "http://guide.example/epg.xml" || h.Attrs["x-tvg-url"] != "http://guide.example/b.xml" {
		t.Errorf("header %v", h.Attrs)
	}
	want := []Entry{
		{
			Title: "Example News HD",
			Attrs: map[string]string{
				"tvg-id": "news.example", "tvg-name": "Example News", "tvg-logo": "http://logos.example/news.png",
				"tvg-chno": "101", "group-title": "News, Weather",
			},
			Group: "News, Weather",
			URL:   "http://streams.example/live/101.ts",
			Line:  2,
		},
		{
			Title: "Sports, Live",
			Attrs: map[string]string{"tvg-id": "sports.example", "catchup": "default", "catchup-days": "7"},
			Group: "Sports",
			URL:   "http://streams.example/live/102.ts",
			Line:  5,
		},
		{
			Title: "Lower Case Tag",
			Attrs: map[string]string{},
			URL:   "udp://@239.0.0.1:1234",
			Line:  11,
		},
	}
	if !reflect.DeepEqual(entries, want) {
		t.Errorf("entries\n%+v\nwant\n%+v", entries, want)
	}
}

func TestParseRejectsOtherContent(t *testing.T) {
	for name, input := range map[string]string{
		"html":  "<!DOCTYPE html><html><body>Account expired</body></html>",
		"json":  `{"user_info":{"auth":0}}`,
		"empty": "",
		"blank": "\n\n  \n",
	} {
		if _, _, err := parseAll(t, input); !errors.Is(err, ErrNotPlaylist) {
			t.Errorf("%s: %v, want ErrNotPlaylist", name, err)
		}
	}
}

func TestParseStopsOnCallbackError(t *testing.T) {
	stop := errors.New("stop")
	calls := 0
	_, err := Parse(strings.NewReader(playlist), func(Entry) error {
		calls++
		return stop
	})
	if !errors.Is(err, stop) || calls != 1 {
		t.Errorf("error %v after %d calls", err, calls)
	}
}

func TestParseRejectsOverlongLine(t *testing.T) {
	input := "#EXTM3U\n#EXTINF:-1," + strings.Repeat("x", MaxLineLen) + "\nhttp://streams.example/1.ts\n"
	if _, _, err := parseAll(t, input); err == nil || !strings.Contains(err.Error(), "longer than") {
		t.Errorf("error %v", err)
	}
}

func TestParseAttrs(t *testing.T) {
	for in, want := range map[string]struct {
		title string
		attrs map[string]string
	}{
		`-1,Plain`:                             {"Plain", map[string]string{}},
		`-1 a="1" B="2",Title`:                 {"Title", map[string]string{"a": "1", "b": "2"}},
		`-1 a="x, y" b=bare,Title, with comma`: {"Title, with comma", map[string]string{"a": "x, y", "b": "bare"}},
		`-1 a="unterminated`:                   {"", map[string]string{"a": "unterminated"}},
		`-1 word a="1",T`:                      {"T", map[string]string{"a": "1"}},
		`-1 =x,T`:                              {"T", map[string]string{}},
		``:                                     {"", map[string]string{}},
	} {
		title, attrs := parseAttrs(in)
		if title != want.title || !reflect.DeepEqual(attrs, want.attrs) {
			t.Errorf("parseAttrs(%q) = %q, %v; want %q, %v", in, title, attrs, want.title, want.attrs)
		}
	}
}

func FuzzParse(f *testing.F) {
	f.Add(playlist)
	f.Add("#EXTM3U\n#EXTINF:-1 a=\"\",\n\n")
	f.Add("#EXTM3U\n#EXTINF:")
	f.Fuzz(func(t *testing.T, input string) {
		_, _ = Parse(strings.NewReader(input), func(e Entry) error {
			if e.URL == "" || strings.HasPrefix(e.URL, "#") {
				t.Errorf("entry with URL %q", e.URL)
			}
			return nil
		})
	})
}
