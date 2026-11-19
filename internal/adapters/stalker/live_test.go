// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/adapters/stalker/stalkertest"
)

func TestGenresAndChannels(t *testing.T) {
	for _, paged := range []bool{false, true} {
		p := stalkertest.New(t)
		p.NoAllChannels = paged
		c := newClient(t, p, "/stalker_portal/c/")
		ctx := context.Background()
		genres, err := c.Genres(ctx)
		if err != nil || len(genres) != 2 || genres[0] != (Genre{ID: "10", Title: "News"}) || genres[1].Title != "Sports" {
			t.Fatalf("genres %+v, %v", genres, err)
		}
		channels, skipped, err := c.Channels(ctx)
		if err != nil || len(channels) != 3 || skipped != 0 {
			t.Fatalf("paged %v: channels %+v, %d, %v", paged, channels, skipped, err)
		}
		news, sports, weather := channels[0], channels[1], channels[2]
		if news.ID != "101" || news.Name != "Example News" || news.Number != 1 || news.Cmd != "ffrt http://localhost/ch/101_" ||
			news.GenreID != "10" || news.XMLTVID != "news.example" || news.Logo != "" || news.ArchiveHours != 0 {
			t.Errorf("news %+v", news)
		}
		if sports.ID != "102" || sports.Number != 2 || sports.Logo != "http://img.example/102.png" || sports.ArchiveHours != 24 {
			t.Errorf("sports %+v", sports)
		}
		if weather.Logo != p.URL+"/stalker_portal/misc/logos/103.png" {
			t.Errorf("weather's logo %q", weather.Logo)
		}
		if paged && (p.Calls("itv/get_ordered_list") != 1 || p.Calls("itv/get_all_channels") != 1) {
			t.Errorf("ordered list read %d times", p.Calls("itv/get_ordered_list"))
		}
	}
}

func TestChannelLink(t *testing.T) {
	p := stalkertest.New(t)
	c := newClient(t, p, "/stalker_portal/c/")
	ctx := context.Background()
	link, err := c.ChannelLink(ctx, "ffrt http://localhost/ch/101_")
	if err != nil || !strings.HasPrefix(link, p.URL+"/play/live.ts?token=") {
		t.Fatalf("link %q, %v", link, err)
	}
	resp, err := http.Get(link)
	if err != nil {
		t.Fatal(err)
	}
	packet := make([]byte, 188)
	_, err = io.ReadFull(resp.Body, packet)
	resp.Body.Close()
	if err != nil || packet[0] != 0x47 {
		t.Errorf("playing: %v, %x", err, packet[:4])
	}

	p.RelativeLinks = true
	if link, err := c.ChannelLink(ctx, "ffrt http://localhost/ch/102_"); err != nil || !strings.HasPrefix(link, p.URL+"/play/") {
		t.Errorf("relative link %q, %v", link, err)
	}
	p.LinkError = "limit"
	_, err = c.ChannelLink(ctx, "ffrt http://localhost/ch/101_")
	var lerr *LinkError
	if !errors.As(err, &lerr) || !lerr.Limit() {
		t.Errorf("at the limit: %v", err)
	}
	p.LinkError = ""
	if _, err := c.ChannelLink(ctx, ""); !errors.As(err, &lerr) || lerr.Reason != "nothing_to_play" {
		t.Errorf("no channel: %v", err)
	}
}

func TestGuide(t *testing.T) {
	p := stalkertest.New(t)
	now := time.Date(2026, 12, 4, 10, 30, 0, 0, time.UTC)
	p.Now = func() time.Time { return now }
	c := newClient(t, p, "/stalker_portal/c/")
	byChannel := map[string][]Programme{}
	skipped, err := c.Guide(context.Background(), 6, func(id string, prog Programme) error {
		byChannel[id] = append(byChannel[id], prog)
		return nil
	})
	if err != nil || skipped != 0 {
		t.Fatal(skipped, err)
	}
	news := byChannel["101"]
	if len(news) != 7 || len(byChannel["102"]) != 4 || len(byChannel["103"]) != 0 {
		t.Fatalf("programmes: %d news, %d sports, %d weather", len(news), len(byChannel["102"]), len(byChannel["103"]))
	}
	first := news[0]
	if first.Title != "News 0" || first.Description != "A fictional programme." || first.Category != "News" ||
		!first.Start.Equal(time.Date(2026, 12, 4, 9, 0, 0, 0, time.UTC)) || first.Stop.Sub(first.Start) != time.Hour ||
		!first.Archived || news[1].Archived {
		t.Errorf("first %+v", first)
	}
	// A lapsed token: the guide signs in again.
	p.ForgetToken()
	if _, err := c.Guide(context.Background(), 1, func(string, Programme) error { return nil }); err != nil {
		t.Errorf("after the token lapsed: %v", err)
	}
	// The function's error stops the read.
	stop := errors.New("stop")
	if _, err := c.Guide(context.Background(), 1, func(string, Programme) error { return stop }); !errors.Is(err, stop) {
		t.Errorf("stopped: %v", err)
	}
}

func TestReadGuide(t *testing.T) {
	for body, want := range map[string]int{
		`{"js":{"data":[]}}`:    0,
		`{"js":{"data":false}}`: 0,
		`{"js":[]}`:             -1,
		`{"js":{"data":{"1":[{"name":"A","start_timestamp":"100","stop_timestamp":"200"},` +
			`{"name":"","start_timestamp":"100","stop_timestamp":"200"},{"name":"B","start_timestamp":200,"stop_timestamp":"100"}],` +
			`"2":{"odd":[1,2]},"3":false}}}`: 1,
		`{"other":1,"js":{"total":2,"data":{"1":[{"name":"A","start_timestamp":"100","stop_timestamp":"200"}]}}}`: 1,
		`<html>`:                       -1,
		`{"js":{"data":{"1":[{"name":`: -1,
	} {
		n := 0
		_, err := readGuide(strings.NewReader(body), func(string, Programme) error { n++; return nil })
		switch {
		case want < 0 && err == nil:
			t.Errorf("%s: no error", body)
		case want >= 0 && err != nil && !errors.Is(err, errEmptyGuide):
			t.Errorf("%s: %v", body, err)
		case want >= 0 && n != want:
			t.Errorf("%s: %d programmes, want %d", body, n, want)
		}
	}
}

func FuzzReadGuide(f *testing.F) {
	f.Add(`{"js":{"data":{"1":[{"name":"A","start_timestamp":"100","stop_timestamp":"200","mark_archive":1}]}}}`)
	f.Add(`{"js":{"data":[]}}`)
	f.Add(`{"js":{"x":{"data":{}},"data":{"2":[{}],"3":{"a":[{"b":{}}]}}}}`)
	f.Fuzz(func(t *testing.T, body string) {
		_, _ = readGuide(strings.NewReader(body), func(_ string, p Programme) error {
			if p.Title == "" || !p.Stop.After(p.Start) {
				t.Fatalf("programme %+v", p)
			}
			return nil
		})
	})
}
