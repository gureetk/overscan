// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import (
	"reflect"
	"testing"
	"time"
)

func TestParseMovieNFO(t *testing.T) {
	data := "\xef\xbb\xbf" + `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<movie>
  <title>The Long Field</title>
  <originaltitle>Le Long Champ</originaltitle>
  <year>2024</year>
  <plot>A field, again.
And a second line.</plot>
  <outline>Short.</outline>
  <runtime>96 min</runtime>
  <mpaa>Rated PG-13</mpaa>
  <genre>Drama</genre>
  <genre>Mystery / Drama</genre>
  <director>D. Director</director>
  <country>Wales</country>
  <premiered>2024-05-01</premiered>
  <rating>6.1</rating>
  <ratings>
    <rating name="other" max="100"><value>70</value></rating>
    <rating name="main" max="10" default="true"><value>7,4</value></rating>
  </ratings>
  <actor><name>A. Actor</name><role>Lead</role></actor>
  <actor><name>B. Actor</name></actor>
  <actor><name>A. Actor</name></actor>
</movie>
https://example.com/a-page-about-it`
	info, ok := parseNFO([]byte(data))
	want := Info{Title: "The Long Field", Year: 2024, Plot: "A field, again.\nAnd a second line.",
		Genres: []string{"Drama", "Mystery"}, Cast: []string{"A. Actor", "B. Actor"}, Directors: []string{"D. Director"},
		Countries: []string{"Wales"}, AgeRating: "PG-13", Rating: 74, Runtime: 96 * time.Minute,
		Premiered: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC), Season: -1, Episode: -1}
	if !ok || !reflect.DeepEqual(info, want) {
		t.Errorf("movie\n%+v\nwant\n%+v", info, want)
	}
}

func TestParseOtherNFOs(t *testing.T) {
	show, ok := parseNFO([]byte(`<tvshow><title>Coastline</title><premiered>2019-03-02</premiered><mpaa>US:TV-PG</mpaa>
		<plot></plot><outline>Waves.</outline><rating>8</rating></tvshow>`))
	if !ok || show.Title != "Coastline" || show.Year != 2019 || show.AgeRating != "TV-PG" || show.Plot != "Waves." || show.Rating != 80 {
		t.Errorf("show %+v", show)
	}
	// A file for two episodes describes the first.
	ep, ok := parseNFO([]byte(`<episodedetails><title>Rocks</title><season>1</season><episode>1</episode>
		<runtime>40</runtime><aired>2019-03-02</aired></episodedetails>
		<episodedetails><title>Sand</title><season>1</season><episode>2</episode></episodedetails>`))
	if !ok || ep.Title != "Rocks" || ep.Season != 1 || ep.Episode != 1 || ep.Runtime != 40*time.Minute ||
		!ep.Premiered.Equal(time.Date(2019, 3, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("episode %+v", ep)
	}
	// ISO-8859-1, as some older tools wrote.
	latin, ok := parseNFO([]byte("<?xml version=\"1.0\" encoding=\"ISO-8859-1\"?><movie><title>Am\xe9lie</title></movie>"))
	if !ok || latin.Title != "Amélie" {
		t.Errorf("latin-1 %+v, %v", latin, ok)
	}
	for name, data := range map[string]string{
		"an address":          "https://example.com/title/1234",
		"another element":     "<html><title>A page</title></html>",
		"another charset":     `<?xml version="1.0" encoding="Shift_JIS"?><movie><title>x</title></movie>`,
		"nothing":             "",
		"half an element":     "<movie><title>",
		"an entity it lacks":  `<!DOCTYPE movie [<!ENTITY x SYSTEM "file:///etc/passwd">]><movie><title>&x;</title></movie>`,
		"a year out of range": "<movie><year>12345</year><title>x</title></movie>",
	} {
		info, ok := parseNFO([]byte(data))
		if name == "a year out of range" || name == "an entity it lacks" {
			if info.Year != 0 || info.Title == "root:x:0:0" {
				t.Errorf("%s: %+v", name, info)
			}
			continue
		}
		if ok {
			t.Errorf("%s: read as %+v", name, info)
		}
	}
}

func FuzzParseNFO(f *testing.F) {
	for _, s := range []string{"<movie><title>x</title><year>2020</year><ratings><rating default=\"true\" max=\"5\"><value>4</value></rating></ratings></movie>",
		"<episodedetails><season>1</season><episode>2</episode><runtime>45</runtime></episodedetails>",
		"<?xml version=\"1.0\" encoding=\"windows-1252\"?><tvshow><title>\x93x\x94</title></tvshow>", "<movie>"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		info, ok := parseNFO(data)
		if !ok {
			return
		}
		if info.Rating < 0 || info.Rating > 100 || len(info.Cast) > maxPeople || len(info.Plot) > maxPlotLen {
			t.Errorf("out of bounds: %+v", info)
		}
	})
}

func TestNFOTMDBID(t *testing.T) {
	for data, want := range map[string]int64{
		`<movie><title>A</title><uniqueid type="imdb">tt1</uniqueid><uniqueid type="tmdb">4321</uniqueid></movie>`: 4321,
		`<movie><title>A</title><tmdbid>77</tmdbid></movie>`:                                                       77,
		`<movie><title>A</title><uniqueid type="TMDB"> 8 </uniqueid><tmdbid>77</tmdbid></movie>`:                   8,
		`<movie><title>A</title><uniqueid type="tmdb">x</uniqueid></movie>`:                                        0,
	} {
		info, ok := parseNFO([]byte(data))
		if !ok || info.TMDBID != want {
			t.Errorf("%s: %d, want %d", data, info.TMDBID, want)
		}
	}
}
