// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"context"
	"testing"

	"overscan.gureet.ca/internal/adapters/m3u"
	"overscan.gureet.ca/internal/store"
)

// An Xtream server's streams of type radio_streams are radio stations
// (D87), for as long as the server lists them so.
func TestXtreamRadioStations(t *testing.T) {
	p, srv := newPanel(t)
	p.set(func(p *fakePanel) {
		p.streams = `[
	{"num":1,"name":"Example News","stream_type":"live","stream_id":101,"category_id":"1"},
	{"num":2,"name":"Example FM","stream_type":"radio_streams","stream_id":102,"category_id":"2"}
]`
	})
	st, r, _ := newStore(t)
	ctx := context.Background()
	settings := xtreamSettings(srv)
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	cs, err := st.LineupChannels(ctx, store.DefaultLineupID)
	if err != nil || len(cs) != 2 {
		t.Fatalf("channels %+v, %v", cs, err)
	}
	if cs[0].Radio || !cs[1].Radio || cs[1].Name != "Example FM" {
		t.Errorf("radio: %+v", cs)
	}
	if n, err := st.RadioStations(ctx); err != nil || n != 1 {
		t.Errorf("radio stations: %d, %v", n, err)
	}
	// A later read that lists it as live makes it a channel again.
	p.set(func(p *fakePanel) {
		p.streams = `[{"num":2,"name":"Example FM","stream_type":"live","stream_id":102,"category_id":"2"}]`
	})
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	if n, err := st.RadioStations(ctx); err != nil || n != 0 {
		t.Errorf("radio stations after the second read: %d, %v", n, err)
	}
}

// A playlist marks a radio station with radio="true", in any case (D87).
func TestChannelRadio(t *testing.T) {
	for v, want := range map[string]bool{"true": true, "TRUE": true, " True ": true, "false": false, "1": false, "": false} {
		e := m3u.Entry{Title: "Example FM", URL: "http://streams.example/fm.mp3", Attrs: map[string]string{"radio": v}}
		if got := channel(e, nil, "k", 0).Radio; got != want {
			t.Errorf("radio=%q: %v, want %v", v, got, want)
		}
	}
	e := m3u.Entry{Title: "Example News", URL: "http://streams.example/1.ts", Attrs: map[string]string{}}
	if channel(e, nil, "k", 0).Radio {
		t.Error("a channel without the attribute is a radio station")
	}
}

// A playlist marks a channel for adults with adult="1" or "true", in any
// case, or with any parent-code (D86).
func TestChannelAdult(t *testing.T) {
	for _, tc := range []struct {
		attrs map[string]string
		want  bool
	}{
		{map[string]string{"adult": "1"}, true},
		{map[string]string{"adult": " TRUE "}, true},
		{map[string]string{"adult": "0"}, false},
		{map[string]string{"adult": "no"}, false},
		{map[string]string{"parent-code": "0000"}, true},
		{map[string]string{"parent-code": " "}, false},
		{map[string]string{"adult": "0", "parent-code": "1234"}, true},
		{map[string]string{}, false},
	} {
		e := m3u.Entry{Title: "Example Late", URL: "http://streams.example/9.ts", Attrs: tc.attrs}
		if got := channel(e, nil, "k", 0).Adult; got != tc.want {
			t.Errorf("%v: %v, want %v", tc.attrs, got, tc.want)
		}
	}
}
