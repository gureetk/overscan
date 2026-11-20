// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		`msg="new device waiting for approval" mac=00:1A:79:12:34:56 model=MAG322`:   `msg="new device waiting for approval" mac=00:1A:79:xx:xx:xx model=MAG322`,
		"00-1a-79-ab-cd-ef in a sentence":                                            "00-1a-79-xx-xx-xx in a sentence",
		`error="fetch: dial tcp 203.0.113.9:8080: connect: connection refused"`:      `error="fetch: dial tcp [address]:8080: connect: connection refused"`,
		"from 10.20.30.40 and 127.0.0.1 and 169.254.1.1":                             "from [local address] and [loopback] and [local address]",
		"dial tcp [2001:db8::1]:443: i/o timeout":                                    "dial tcp [IPv6 address]:443: i/o timeout",
		"dial tcp: lookup provider.example on 192.168.1.1:53: no such host":          "dial tcp: lookup [host] on [local address]:53: no such host",
		"x509: certificate is valid for a.example, b.example, not provider.example":  "x509: certificate is valid for [hosts], not [host]",
		"see http://provider.example/get.php?username=u&password=p for it":           "see [address] for it",
		"type=itv&action=create_link&token=[redacted]&sn=0123456789&auth_key=abc123": "type=itv&action=create_link&token=[redacted]&sn=[redacted]&auth_key=[redacted]",
		"user=someone pass ok":                               "user=[redacted] pass ok",
		"version 6.1.1 at 07:15:00 on 2026-11-16, schema 15": "version 6.1.1 at 07:15:00 on 2026-11-16, schema 15",
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestSupportBundle(t *testing.T) {
	ta := newTestAdmin(t)
	c, _ := ta.withActivity()
	if err := ta.st.SetRefreshResult(context.Background(), ta.sourceID("main"), ta.clock.Add(-time.Hour),
		"fetch: dial tcp 203.0.113.9:8080: connect: connection refused"); err != nil {
		t.Fatal(err)
	}
	d, err := ta.st.Device(context.Background(), "00:1A:79:00:00:01")
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetPlaybackCheck(context.Background(), store.PlaybackCheck{DeviceID: d.ID, Model: "MAG322",
		Findings: map[string]bool{"format:matroska": true, "audio:dts": false},
		Results: []byte(`[{"id":"base","answer":"yes","started":true,"ms":640,"width":640,"height":360,"tracks":1,"position":3.4},
			{"id":"dts","answer":"no","started":true,"ms":700,"width":640,"height":360,"tracks":2,"position":4.1,
			 "video":"{pictureWidth:640}","audio":"[{pid:257,type:7}]","events":"\"2\" \"7\" \"4\""},
			{"id":"truehd","answer":"","started":false,"ms":15000},{"id":"av1","answer":"","failed":true}]`),
		CheckedAt: ta.clock.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	th, _, err := ta.st.InstallTheme(context.Background(), "Evergreen", []byte(evergreenTheme), ta.clock)
	if err != nil {
		t.Fatal(err)
	}
	if err := ta.st.SetAccountTheme(context.Background(), householdAccount, th.ID); err != nil {
		t.Fatal(err)
	}
	ta.UseSupport(Support{
		Log: func() []string {
			return []string{
				`level=INFO msg="new device waiting for approval" mac=00:1A:79:00:00:02 model=MAG322`,
				`level=WARN msg="source refresh failed" source=main error="dial tcp: lookup provider.example on 127.0.0.53:53: no such host"`,
			}
		},
		Facts: []Fact{{Name: "Listens on", Value: ":8080"}, {Name: "ffmpeg", Value: "7.1.1 at /usr/bin/ffmpeg"}},
	})

	rec := ta.do("GET", "/admin/support-bundle", nil, c)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/zip" ||
		!strings.HasPrefix(rec.Header().Get("Content-Disposition"), `attachment; filename="overscan-support-`) {
		t.Fatalf("status %d, headers %v", rec.Code, rec.Header())
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	var all strings.Builder
	for _, f := range zr.File {
		r, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		files[f.Name] = string(data)
		all.Write(data)
	}
	for name, wants := range map[string][]string{
		"README.txt":   {"Read it before you share it."},
		"overscan.txt": {"Version", "test", "Database schema", "Listens on", "7.1.1 at /usr/bin/ffmpeg", "Stream repair", "available", "Transcoding", "available, with tone mapping; off\n", "Sound conversion", "Jellyfin transcodes"},
		"sources.txt":  {"main (m3u, ID ", "Channels", "Last error", "dial tcp [address]:8080"},
		"lineup.txt": {"Channels: 4, 4 shown", "Stream repair: every channel by default: false; HLS too: false; 0 channels set to be repaired, 0 set not to be; 0 repaired in all", "Groups: 2",
			"Recordings: room: 0 GB (0 is off); the server records: false; to come: 0, under way: 0, made: 0, failed: 0, called off: 0",
			"Timeshift: keeps 0 minutes of a box's channel (0 is off)",
			fmt.Sprintf("Themes: 1 installed; the household's: theme %d; profiles with their own: 0", th.ID),
			"Messages: 0 in the admin's list, waiting on 0 boxes"},
		"devices.txt": {"00:1A:79:xx:xx:xx", "Living room", "MAG322", "Playback checks, the latest first:",
			"Living room (00:1A:79:xx:xx:xx), MAG322, firmware not said, checked 1 hour ago", "  Plays: MKV\n  Does not play: DTS\n",
			"  base: yes; started in 640 ms, picture 640x360, 1 sound track, at 3.4 s",
			"  dts: no; started in 700 ms, picture 640x360, 2 sound tracks, at 4.1 s\n    the player said {pictureWidth:640} of the picture and [{pid:257,type:7}] of the sound\n    the player's events: \"2\" \"7\" \"4\"\n",
			"  truehd: not answered; the box did not start it in 15000 ms", "  av1: not answered; the box could not open it"},
		"playing.txt": {"Alpha", "Living room", "a removed device"},
		"capture.txt": {"itv/set_fav (fav_ch,type): 1 times", "token=[redacted]"},
		"log.txt":     {"mac=00:1A:79:xx:xx:xx", "lookup [host] on [loopback]:53"},
	} {
		text, ok := files[name]
		if !ok {
			t.Errorf("no %s in the bundle", name)
			continue
		}
		mustContain(t, name, text, wants...)
	}
	for _, leak := range []string{"provider.example", "203.0.113.9", "00:1A:79:00:00", "streams.example"} {
		if strings.Contains(all.String(), leak) {
			t.Errorf("the bundle holds %q", leak)
		}
	}

	// Without a session, the bundle is not there to take.
	if rec := ta.do("GET", "/admin/support-bundle", nil, nil); rec.Code == http.StatusOK {
		t.Error("the bundle without signing in")
	}
	mustContain(t, "the overview", ta.do("GET", "/admin/", nil, c).Body.String(), `<a href="/admin/support-bundle" download>`)
}
