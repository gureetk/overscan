// SPDX-License-Identifier: AGPL-3.0-or-later

package subtitles

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

func ms(n int) time.Duration { return time.Duration(n) * time.Millisecond }

func TestParseSRT(t *testing.T) {
	in := "\uFEFF1\r\n00:00:01,000 --> 00:00:03,500\r\nHello there.\r\n<i>Who's there?</i>\r\n\r\n" +
		"2\r\n00:00:05,000 --> 00:00:07,250 X1:10 X2:20\r\n{\\an8}Up top.\r\n\r\n" +
		"3\r\n00:01:00.5 --> 00:01:02.75\r\n42\r\n\r\n" +
		"4\r\n00:02:00,000 --> 00:01:00,000\r\nEnds before it starts.\r\n\r\n" +
		"5\r\n01:00:00,000 --> 01:00:02,000\r\n\r\n\r\n" +
		"6\r\n00:00:04,000 --> 00:00:04,500\r\nOut of order.\r\n"
	got, err := Parse([]byte(in), "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{
		{ms(1000), ms(3500), "Hello there.\n<i>Who's there?</i>"},
		{ms(4000), ms(4500), "Out of order."},
		{ms(5000), ms(7250), "Up top."},
		{ms(60500), ms(62750), "42"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%q\nwant\n%q", got, want)
	}
}

func TestParseSRTWithoutNumbers(t *testing.T) {
	in := "00:00:01,000 --> 00:00:02,000\nOne.\n\n00:00:03,000 --> 00:00:04,000\nTwo.\nlines\n"
	got, err := Parse([]byte(in), "srt", "")
	if err != nil || len(got) != 2 || got[0].Text != "One." || got[1].Text != "Two.\nlines" {
		t.Errorf("Parse = %q, %v", got, err)
	}
}

func TestParseVTT(t *testing.T) {
	in := "WEBVTT - with a title\nKind: captions\n\nNOTE a note\nthat runs on\n\nSTYLE\n::cue { color: lime }\n\n" +
		"intro\n00:01.000 --> 00:03.000 align:start position:10%\n<v Ana>Hi, <c.loud>you</c>.</v> <00:00:02.000><b>Now</b>\n\n" +
		"01:02:03.004 --> 01:02:05.000\nLater.\n"
	got, err := Parse([]byte(in), "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{
		{ms(1000), ms(3000), "Hi, you. <b>Now</b>"},
		{time.Hour + 2*time.Minute + 3*time.Second + ms(4), time.Hour + 2*time.Minute + 5*time.Second, "Later."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%q\nwant\n%q", got, want)
	}
}

func TestParseASS(t *testing.T) {
	in := "[Script Info]\nTitle: A test\nScriptType: v4.00+\n\n[V4+ Styles]\nFormat: Name, Fontname\nStyle: Default,Arial\n\n" +
		"[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: 0,0:00:05.00,0:00:06.50,Default,,0,0,0,,{\\i1}Second{\\i0}, with a comma\\Nand a line\n" +
		"Comment: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,Not shown\n" +
		"Dialogue: 0,0:00:01.00,0:00:02.50,Default,,0,0,0,,First\\hone\n" +
		"Dialogue: 0,0:00:03.00,0:00:04.00,Sign,,0,0,0,,{\\p1}m 0 0 l 100 0 100 100{\\p0}\n" +
		"Dialogue: 0,0:00:07.00,0:00:08.00,Default,,0,0,0,,{\\an8\\pos(10,10)}Top {\\p1}m 0 0{\\p0}line\n"
	got, err := Parse([]byte(in), "", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []Cue{
		{ms(1000), ms(2500), "First one"},
		{ms(5000), ms(6500), "Second, with a comma\nand a line"},
		{ms(7000), ms(8000), "Top line"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Parse =\n%q\nwant\n%q", got, want)
	}

	// SSA's own Format, with Marked first.
	ssa := "[Events]\nFormat: Marked, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
		"Dialogue: Marked=0,0:00:01.00,0:00:02.00,*Default,NTP,0000,0000,0000,!Effect,Old style\n"
	if got, err := Parse([]byte(ssa), "ssa", ""); err != nil || len(got) != 1 || got[0].Text != "Old style" {
		t.Errorf("SSA: %q, %v", got, err)
	}
}

func TestDecode(t *testing.T) {
	srt := func(text string) []byte {
		return []byte("1\n00:00:01,000 --> 00:00:02,000\n" + text + "\n")
	}
	cp1251, err := charmap.Windows1251.NewEncoder().Bytes(srt("Привет"))
	if err != nil {
		t.Fatal(err)
	}
	cp1252, err := charmap.Windows1252.NewEncoder().Bytes(srt("Ça va, Zoë?"))
	if err != nil {
		t.Fatal(err)
	}
	units := utf16.Encode([]rune(string(srt("Grüße"))))
	le := []byte{0xFF, 0xFE}
	be := []byte{0xFE, 0xFF}
	for _, u := range units {
		le = append(le, byte(u), byte(u>>8))
		be = append(be, byte(u>>8), byte(u))
	}
	for _, c := range []struct {
		name, lang string
		data       []byte
		want       string
	}{
		{"UTF-8", "rus", srt("Привет"), "Привет"},
		{"Windows-1251 for Russian", "rus", cp1251, "Привет"},
		{"Windows-1251 by its two-letter code", "uk", cp1251, "Привет"},
		{"Windows-1252 by default", "", cp1252, "Ça va, Zoë?"},
		{"UTF-16LE", "", le, "Grüße"},
		{"UTF-16BE", "", be, "Grüße"},
	} {
		got, err := Parse(c.data, "", c.lang)
		if err != nil || len(got) != 1 || got[0].Text != c.want {
			t.Errorf("%s: %q, %v", c.name, got, err)
		}
	}
}

func TestParseRefuses(t *testing.T) {
	for _, in := range []string{"", "just some text\nwith lines\n", "WEBVTT\n"} {
		got, err := Parse([]byte(in), "", "")
		if in == "WEBVTT\n" {
			if err != nil || len(got) != 0 {
				t.Errorf("an empty WebVTT file: %q, %v", got, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("Parse(%q) = %q", in, got)
		}
	}
	if _, err := Parse(make([]byte, MaxBytes+1), "", ""); err == nil {
		t.Error("took a file too big")
	}
}

func TestShiftAndWrite(t *testing.T) {
	cues := []Cue{
		{ms(1000), ms(3000), "Gone."},
		{ms(9000), ms(12000), "Across."},
		{ms(20000), ms(21500), "Later,\nin two lines."},
	}
	got := Shift(cues, 10*time.Second)
	want := []Cue{{0, ms(2000), "Across."}, {ms(10000), ms(11500), "Later,\nin two lines."}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Shift = %q", got)
	}
	if got := Shift(cues, 0); !reflect.DeepEqual(got, cues) {
		t.Errorf("Shift by 0 = %q", got)
	}
	var b bytes.Buffer
	if err := WriteSRT(&b, append(want, Cue{time.Hour + ms(61001), time.Hour + ms(62000), "Last."})); err != nil {
		t.Fatal(err)
	}
	wantSRT := "1\r\n00:00:00,000 --> 00:00:02,000\r\nAcross.\r\n\r\n" +
		"2\r\n00:00:10,000 --> 00:00:11,500\r\nLater,\r\nin two lines.\r\n\r\n" +
		"3\r\n01:01:01,001 --> 01:01:02,000\r\nLast.\r\n\r\n"
	if b.String() != wantSRT {
		t.Errorf("WriteSRT =\n%q\nwant\n%q", b.String(), wantSRT)
	}
	// What WriteSRT writes reads back.
	back, err := Parse(b.Bytes(), "", "")
	if err != nil || len(back) != 3 || back[1].Text != "Later,\nin two lines." {
		t.Errorf("read back %q, %v", back, err)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("1\n00:00:01,000 --> 00:00:02,000\nOne\n"), "srt", "")
	f.Add([]byte("WEBVTT\n\n00:01.000 --> 00:02.000\n<v A>Hi</v>\n"), "vtt", "")
	f.Add([]byte("[Events]\nFormat: Start, End, Text\nDialogue: 0:00:01.00,0:00:02.00,{\\p1}x{\\p0}Hi\n"), "ass", "rus")
	f.Fuzz(func(t *testing.T, data []byte, format, lang string) {
		cues, err := Parse(data, format, lang)
		if err != nil {
			return
		}
		for i, c := range cues {
			if c.End <= c.Start || c.Start < 0 || c.End > maxTime+time.Hour || c.Text == "" || len(c.Text) > maxText ||
				!utf8.ValidString(c.Text) || i > 0 && c.Start < cues[i-1].Start {
				t.Fatalf("cue %d: %+v", i, c)
			}
		}
		var b bytes.Buffer
		if err := WriteSRT(&b, Shift(cues, 3*time.Second)); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(b.String(), "\x00") && !bytes.Contains(data, []byte{0}) {
			t.Fatal("wrote a NUL that was not there")
		}
	})
}
