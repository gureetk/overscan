// SPDX-License-Identifier: AGPL-3.0-or-later

package hls

import (
	"bytes"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

var playlistBase, _ = url.Parse("http://streams.example/live/channel?u=1")

func TestAttributes(t *testing.T) {
	got := attributes(`METHOD=AES-128,URI="https://keys.example/k?a=1,b=2",IV=0x1A, keyformat="identity"`)
	want := map[string]string{"METHOD": "AES-128", "URI": "https://keys.example/k?a=1,b=2", "IV": "0x1A", "KEYFORMAT": "identity"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := attributes(`BANDWIDTH=1,NAME="unterminated`); got["BANDWIDTH"] != "1" || got["NAME"] != "unterminated" {
		t.Errorf("got %q", got)
	}
}

func TestParseIV(t *testing.T) {
	iv, ok := parseIV("0x1A")
	if want := append(make([]byte, 15), 0x1A); !ok || !bytes.Equal(iv, want) {
		t.Errorf("0x1A: %x, %v", iv, ok)
	}
	if iv, ok := parseIV("0X000102030405060708090A0B0C0D0E0F"); !ok || iv[15] != 0x0F || iv[1] != 0x01 {
		t.Errorf("full IV: %x, %v", iv, ok)
	}
	for _, bad := range []string{"", "0x", "1A", "0xZZ", "0x" + string(bytes.Repeat([]byte("1"), 33))} {
		if _, ok := parseIV(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestParseMedia(t *testing.T) {
	data := "\xEF\xBB\xBF#EXTM3U\r\n#EXT-X-TARGETDURATION:6\r\n#EXT-X-MEDIA-SEQUENCE:41\r\n" +
		"#EXTINF:6.0,\r\nseg41.ts\r\n" +
		"#EXT-X-KEY:METHOD=AES-128,URI=\"/key?id=1\",IV=0x01\r\n" +
		"#EXTINF:6.0,\r\n/other/seg42.ts\r\n" +
		"#EXT-X-DISCONTINUITY\r\n" +
		"#EXTINF:6.0,\r\nftp://elsewhere.example/seg43.ts\r\n" +
		"#EXT-X-KEY:METHOD=NONE\r\n\r\n" +
		"#EXTINF:6.0,\r\nhttps://cdn.example/seg44.ts\r\n" +
		"#EXT-X-ENDLIST\r\n"
	pl := ParseMedia([]byte(data), playlistBase)
	if pl.Target != 6*time.Second || !pl.Ended || pl.Unsupported != "" || len(pl.Segments) != 4 {
		t.Fatalf("%+v", pl)
	}
	key := &Key{URI: "http://streams.example/key?id=1", IV: append(make([]byte, 15), 1)}
	want := []Segment{
		{Seq: 41, URI: "http://streams.example/live/seg41.ts"},
		{Seq: 42, URI: "http://streams.example/other/seg42.ts", Key: key},
		{Seq: 43, URI: "", Key: key},
		{Seq: 44, URI: "https://cdn.example/seg44.ts"},
	}
	if !reflect.DeepEqual(pl.Segments, want) {
		t.Errorf("segments %+v\nwant %+v", pl.Segments, want)
	}

	// Without an IV, a segment's sequence number is its IV.
	seg := Segment{Seq: 0x0102, Key: &Key{}}
	if iv := seg.IV(); !bytes.Equal(iv, append(make([]byte, 14), 1, 2)) {
		t.Errorf("iv %x", iv)
	}

	// A list of streams that is not live HLS is played once.
	if pl := ParseMedia([]byte("#EXTM3U\n#EXTINF:-1,\nhttp://streams.example/a.ts\n"), playlistBase); !pl.Ended || len(pl.Segments) != 1 {
		t.Errorf("plain list: %+v", pl)
	}
	if pl := ParseMedia([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n"), playlistBase); pl.Ended || pl.Target != 4*time.Second {
		t.Errorf("empty live playlist: %+v", pl)
	}
}

func TestParseMediaUnsupported(t *testing.T) {
	for tag, want := range map[string]string{
		`#EXT-X-MAP:URI="init.mp4"`:                 "fragmented MP4 segments",
		"#EXT-X-BYTERANGE:1000@0":                   "segments that are byte ranges of a file",
		`#EXT-X-KEY:METHOD=SAMPLE-AES,URI="k"`:      "segments encrypted with SAMPLE-AES",
		`#EXT-X-KEY:METHOD=AES-128`:                 "a key without an address the hub can fetch",
		`#EXT-X-KEY:METHOD=AES-128,URI="k",IV=0xZZ`: "a key with an invalid initialization vector",
		`#EXT-X-KEY:METHOD=SAMPLE-AES,URI="skd://k",KEYFORMAT="com.apple.streamingkeydelivery"`: "segments protected by DRM",
	} {
		pl := ParseMedia([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n"+tag+"\n#EXTINF:4,\nseg.ts\n"), playlistBase)
		if pl.Unsupported != want {
			t.Errorf("%s: %q, want %q", tag, pl.Unsupported, want)
		}
	}
	pl := ParseMedia([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-KEY:METHOD=NONE,KEYFORMAT=\"com.example.drm\"\n#EXTINF:4,\nseg.ts\n"), playlistBase)
	if pl.Unsupported != "" {
		t.Errorf("a DRM key that is not used: %q", pl.Unsupported)
	}
}

func TestPickVariant(t *testing.T) {
	pick := func(data string) (string, bool) {
		if !IsMaster([]byte(data)) {
			t.Fatalf("not a master playlist:\n%s", data)
		}
		v, ok := ParseMaster([]byte(data), playlistBase).Pick()
		return v.URI, ok
	}

	// All too tall: the lowest.
	uri, ok := pick("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=20000000,RESOLUTION=3840x2160\nuhd.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=12000000,RESOLUTION=2560x1440\nqhd.m3u8\n")
	if !ok || uri != "http://streams.example/live/qhd.m3u8" {
		t.Errorf("all too tall: %q, %v", uri, ok)
	}

	// Audio renditions that include one in the variant itself leave the
	// variant its sound.
	uri, ok = pick("#EXTM3U\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Main",DEFAULT=YES` + "\n" +
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Other",URI="other.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=2000000,AUDIO="a"` + "\nmain.m3u8\n" +
		"#EXT-X-STREAM-INF:BANDWIDTH=1000000\nlow.m3u8\n")
	if !ok || uri != "http://streams.example/live/main.m3u8" {
		t.Errorf("audio in the variant: %q, %v", uri, ok)
	}

	// Only variants whose sound is elsewhere: none.
	if uri, ok := pick("#EXTM3U\n" + `#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="Main",URI="a.m3u8"` + "\n" +
		`#EXT-X-STREAM-INF:BANDWIDTH=2000000,AUDIO="a"` + "\nmain.m3u8\n"); ok {
		t.Errorf("silent variants: %q", uri)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte("#EXTM3U\n#EXT-X-TARGETDURATION:4\n#EXT-X-MEDIA-SEQUENCE:7\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\",IV=0x1A\n#EXTINF:4.0,\nseg.ts\n#EXT-X-ENDLIST\n"))
	f.Add([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000,RESOLUTION=1920x1080,CODECS=\"avc1,mp4a\"\nv1.m3u8\n"))
	f.Add([]byte("#EXTM3U\r\n#EXT-X-MAP:URI=\"init.mp4\"\r\n#EXT-X-BYTERANGE:100@0\r\n#EXTINF:,\r\n../x.ts\r\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		IsPlaylist(data)
		if IsMaster(data) {
			m := ParseMaster(data, playlistBase)
			if v, ok := m.Pick(); ok && !strings.HasPrefix(v.URI, "http") {
				t.Errorf("picked a variant at %q", v.URI)
			}
			return
		}
		web := func(u string) bool { return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") }
		p := ParseMedia(data, playlistBase)
		for _, seg := range p.Segments {
			if seg.URI != "" && !web(seg.URI) {
				t.Errorf("a segment at %q", seg.URI)
			}
			if seg.Key == nil {
				continue
			}
			// A key that the hub cannot fetch makes the playlist one that
			// it does not relay.
			if !web(seg.Key.URI) && p.Unsupported == "" {
				t.Errorf("a key at %q", seg.Key.URI)
			}
			if iv := seg.IV(); len(iv) != 16 {
				t.Errorf("an IV of %d bytes", len(iv))
			}
		}
	})
}
