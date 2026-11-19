// SPDX-License-Identifier: AGPL-3.0-or-later

package xmltv

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// A fictional guide that bends the format the ways guides in the wild do.
const guide = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE tv SYSTEM "xmltv.dtd">
<tv generator-info-name="test">
  <channel id="news.example">
    <display-name lang="en">Example News</display-name>
    <display-name>News &amp; More</display-name>
    <icon src="http://img.example/news.png"/>
  </channel>
  <channel id="sports.example"><display-name>  Example
     Sports  </display-name></channel>
  <channel><display-name>No ID</display-name></channel>
  <programme start="20261204180000 +0100" stop="20261204190000 +0100" channel="news.example">
    <title lang="en">Evening&nbsp;News</title>
    <sub-title>Part <b>one</b></sub-title>
    <desc><![CDATA[What <happened> today.]]></desc>
    <category>News</category>
    <category>Current affairs</category>
    <credits><presenter>Someone</presenter></credits>
  </programme>
  <programme start="202612041900" channel="news.example"><title>No stop</title></programme>
  <programme start="20261204190000 -05:30" stop="20261204180000" channel="sports.example"><title>Stop first</title></programme>
  <programme start="20261204200000 Z" stop="20261204210000 UTC" channel="sports.example"><title>Zulu</title></programme>
  <programme start="20261204200000 CET" channel="sports.example"><title>Named zone</title></programme>
  <programme start="20261204200000" channel="sports.example"><desc>No title</desc></programme>
  <programme start="20261204200000" channel=""><title>No channel</title></programme>
  <programme start="soon" channel="sports.example"><title>Bad start</title></programme>
</tv>`

type collected struct {
	channels   []Channel
	programmes []Programme
}

func (c *collected) handler() Handler {
	return Handler{
		Channel:   func(ch Channel) error { c.channels = append(c.channels, ch); return nil },
		Programme: func(p Programme) error { c.programmes = append(c.programmes, p); return nil },
	}
}

func parse(t *testing.T, input string) (*collected, int) {
	t.Helper()
	r, err := Open(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	var c collected
	skipped, err := Parse(r, c.handler())
	if err != nil {
		t.Fatal(err)
	}
	return &c, skipped
}

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParse(t *testing.T) {
	c, skipped := parse(t, guide)
	if len(c.channels) != 2 || c.channels[0].ID != "news.example" ||
		strings.Join(c.channels[0].Names, "|") != "Example News|News & More" ||
		strings.Join(c.channels[1].Names, "|") != "Example Sports" {
		t.Errorf("channels %+v", c.channels)
	}
	want := []Programme{
		{Channel: "news.example", Start: at("2026-12-04T17:00:00Z"), Stop: at("2026-12-04T18:00:00Z"),
			Title: "Evening News", SubTitle: "Part one", Desc: "What <happened> today.", Category: "News"},
		{Channel: "news.example", Start: at("2026-12-04T19:00:00Z"), Title: "No stop"},
		{Channel: "sports.example", Start: at("2026-12-05T00:30:00Z"), Title: "Stop first"},
		{Channel: "sports.example", Start: at("2026-12-04T20:00:00Z"), Stop: at("2026-12-04T21:00:00Z"), Title: "Zulu"},
	}
	if skipped != 4 || len(c.programmes) != len(want) {
		t.Fatalf("skipped %d, programmes %+v", skipped, c.programmes)
	}
	for i := range want {
		if got := c.programmes[i]; !got.Start.Equal(want[i].Start) || !got.Stop.Equal(want[i].Stop) ||
			got.Title != want[i].Title || got.SubTitle != want[i].SubTitle || got.Desc != want[i].Desc ||
			got.Category != want[i].Category || got.Channel != want[i].Channel {
			t.Errorf("programme %d\n%+v\nwant\n%+v", i, got, want[i])
		}
	}
}

func TestGzipAndLatin1(t *testing.T) {
	latin := "<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><tv><channel id=\"a\"><display-name>Caf\xe9</display-name></channel>" +
		"<programme start=\"20261204180000\" channel=\"a\"><title>D\xe9j\xe0 vu</title></programme></tv>"
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	io.WriteString(zw, latin)
	zw.Close()
	c, _ := parse(t, buf.String())
	if len(c.channels) != 1 || c.channels[0].Names[0] != "Café" || len(c.programmes) != 1 || c.programmes[0].Title != "Déjà vu" {
		t.Errorf("%+v %+v", c.channels, c.programmes)
	}
	r, err := Open(strings.NewReader(`<?xml version="1.0" encoding="KOI8-R"?><tv></tv>`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(r, Handler{}); err == nil {
		t.Error("an encoding Overscan cannot read was taken")
	}
}

func TestNotAGuide(t *testing.T) {
	for _, input := range []string{"", "<html><body>Forbidden</body></html>", "Unauthorized", "{\"user_info\":{}}",
		"<programme start=\"20261204180000\" channel=\"a\"><title>x</title></programme>"} {
		r, err := Open(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Parse(r, Handler{}); !errors.Is(err, ErrNotXMLTV) {
			t.Errorf("%q: %v", input, err)
		}
	}
}

func TestBrokenGuide(t *testing.T) {
	input := guide[:strings.Index(guide, `<programme start="202612041900"`)] + `<programme start="2026`
	r, _ := Open(strings.NewReader(input))
	var c collected
	_, err := Parse(r, c.handler())
	if err == nil || errors.Is(err, ErrNotXMLTV) || len(c.programmes) != 1 {
		t.Errorf("%v, %d programmes", err, len(c.programmes))
	}
	stop := errors.New("stop")
	r, _ = Open(strings.NewReader(guide))
	if _, err := Parse(r, Handler{Programme: func(Programme) error { return stop }}); !errors.Is(err, stop) {
		t.Errorf("a handler's error: %v", err)
	}
}

func TestParseTime(t *testing.T) {
	for in, want := range map[string]string{
		"20261204180000 +0100":  "2026-12-04T17:00:00Z",
		"20261204180000+0100":   "2026-12-04T17:00:00Z",
		"202612041800 -0530":    "2026-12-04T23:30:00Z",
		"20261204180000 +05:45": "2026-12-04T12:15:00Z",
		"20261204180000":        "2026-12-04T18:00:00Z",
		" 20261204180000 GMT ":  "2026-12-04T18:00:00Z",
	} {
		got, ok := ParseTime(in)
		if !ok || !got.Equal(at(want)) {
			t.Errorf("%q: %v %v, want %s", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "2026", "20261204", "2026120418000", "20261314180000", "20261204180000 BST",
		"20261204180000 +2500", "20261204180000 +01", "2026120418000000"} {
		if got, ok := ParseTime(in); ok {
			t.Errorf("%q taken as %v", in, got)
		}
	}
}

func TestClean(t *testing.T) {
	if got := Clean(" a\tb\n\x00c d ", 100); got != "a b c d" {
		t.Errorf("%q", got)
	}
	if got := Clean("ééé", 5); got != "éé" {
		t.Errorf("%q", got)
	}
	if got := Clean("a\xffb", 10); got != "ab" {
		t.Errorf("%q", got)
	}
}

func FuzzParse(f *testing.F) {
	f.Add(guide)
	f.Add(`<tv><programme start="20261204180000 +0100" channel="a"><title>x</title></programme></tv>`)
	f.Add(`<tv><channel id="a"><display-name>x</display-name></channel></tv>`)
	f.Add("<?xml version=\"1.0\" encoding=\"latin1\"?><tv><programme start=\"202612041800\" channel=\"\xe9\"><title>\xe9</title></programme></tv>")
	f.Fuzz(func(t *testing.T, input string) {
		r, err := Open(strings.NewReader(input))
		if err != nil {
			return
		}
		_, _ = Parse(r, Handler{
			Channel: func(c Channel) error {
				if c.ID == "" || len(c.ID) > MaxIDLen || len(c.Names) > maxNames {
					t.Errorf("channel %+v", c)
				}
				return nil
			},
			Programme: func(p Programme) error {
				if p.Channel == "" || p.Title == "" || p.Start.IsZero() || (!p.Stop.IsZero() && !p.Stop.After(p.Start)) ||
					len(p.Desc) > MaxDescLen || len(p.Title) > MaxNameLen {
					t.Errorf("programme %+v", p)
				}
				return nil
			},
		})
	})
}
