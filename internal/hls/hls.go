// SPDX-License-Identifier: AGPL-3.0-or-later

// Package hls reads HLS playlists (RFC 8216), as far as Overscan needs:
// master playlists to pick a variant from, and media playlists to follow.
package hls

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MaxVariantHeight is the tallest picture that Pick chooses: the MAG322w1
// decodes up to 1080 lines.
const MaxVariantHeight = 1080

// bom is the byte order mark that some servers put before a playlist.
var bom = []byte{0xEF, 0xBB, 0xBF}

// MediaPlaylist is an HLS media playlist: a list of segments, the last few
// of a live stream or all of one that has ended.
type MediaPlaylist struct {
	Target      time.Duration // the target duration; 0 if it has none
	Segments    []Segment     // numbered from the media sequence on
	Ended       bool          // no segment will be added
	Unsupported string        // what in it Overscan cannot play, if anything
}

// Segment is a segment of a media playlist.
type Segment struct {
	Seq int64
	URI string // absolute http or https address; "" if it has none
	Key *Key   // nil if the segment is not encrypted
}

// Key is an AES-128 key that segments are encrypted with.
type Key struct {
	URI string // absolute http or https address
	IV  []byte // 16 bytes; nil to use the segment's sequence number
}

// IV returns the initialization vector to decrypt an encrypted segment with.
func (seg Segment) IV() []byte {
	if seg.Key.IV != nil {
		return seg.Key.IV
	}
	iv := make([]byte, 16)
	binary.BigEndian.PutUint64(iv[8:], uint64(seg.Seq))
	return iv
}

// lines returns the lines of a playlist, trimmed, without a byte order mark.
func lines(data []byte) []string {
	data = bytes.TrimPrefix(data, bom)
	var out []string
	for line := range strings.Lines(string(data)) {
		out = append(out, strings.TrimSpace(line))
	}
	return out
}

// IsPlaylist reports whether data starts like an M3U playlist: with
// #EXTM3U, after any byte order mark and blank space.
func IsPlaylist(data []byte) bool {
	return bytes.HasPrefix(bytes.TrimLeft(bytes.TrimPrefix(data, bom), " \t\r\n"), []byte("#EXTM3U"))
}

// IsMaster reports whether a playlist is a master playlist, which lists
// variants of a stream rather than segments.
func IsMaster(data []byte) bool {
	for _, line := range lines(data) {
		if strings.HasPrefix(line, "#EXT-X-STREAM-INF:") {
			return true
		}
	}
	return false
}

// ParseMedia reads a media playlist whose addresses are relative to base.
// A playlist without a target duration is a plain list of streams rather
// than live HLS: it is played once, in order, like one that has ended.
func ParseMedia(data []byte, base *url.URL) *MediaPlaylist {
	pl := &MediaPlaylist{}
	var seq int64
	var key *Key
	for _, line := range lines(data) {
		tag, value, _ := strings.Cut(line, ":")
		switch {
		case line == "":
		case tag == "#EXT-X-TARGETDURATION":
			if d, err := strconv.ParseFloat(value, 64); err == nil && d > 0 && d < 3600 {
				pl.Target = time.Duration(d * float64(time.Second))
			}
		case tag == "#EXT-X-MEDIA-SEQUENCE":
			if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
				seq = n
			}
		case tag == "#EXT-X-KEY":
			attrs := attributes(value)
			method := strings.ToUpper(attrs["METHOD"])
			if f, ok := attrs["KEYFORMAT"]; ok && f != "identity" {
				// A key for a DRM system, which no box here has.
				if method != "NONE" {
					pl.Unsupported = "segments protected by DRM"
				}
				continue
			}
			switch method {
			case "NONE":
				key = nil
			case "AES-128":
				key = &Key{URI: resolve(base, attrs["URI"])}
				if key.URI == "" {
					pl.Unsupported = "a key without an address the hub can fetch"
				}
				if v, ok := attrs["IV"]; ok {
					if key.IV, ok = parseIV(v); !ok {
						pl.Unsupported = "a key with an invalid initialization vector"
					}
				}
			default:
				pl.Unsupported = "segments encrypted with " + method
			}
		case tag == "#EXT-X-MAP":
			pl.Unsupported = "fragmented MP4 segments"
		case tag == "#EXT-X-BYTERANGE":
			pl.Unsupported = "segments that are byte ranges of a file"
		case line == "#EXT-X-ENDLIST":
			pl.Ended = true
		case strings.HasPrefix(line, "#"):
			// Another tag, or a comment.
		default:
			pl.Segments = append(pl.Segments, Segment{Seq: seq, URI: resolve(base, line), Key: key})
			seq++
		}
	}
	if pl.Target == 0 {
		pl.Ended = true
	}
	return pl
}

// Variant is a variant stream of a master playlist.
type Variant struct {
	URI       string
	Bandwidth int64
	Height    int    // 0 if the playlist does not say
	Audio     string // the group of its audio renditions, if any
}

// MasterPlaylist is an HLS master playlist.
type MasterPlaylist struct {
	Variants []Variant
	// SeparateAudio holds the audio groups whose renditions are all
	// streams of their own, so that a variant using one carries no sound.
	SeparateAudio map[string]bool
}

// ParseMaster reads a master playlist whose addresses are relative to base.
func ParseMaster(data []byte, base *url.URL) *MasterPlaylist {
	m := &MasterPlaylist{SeparateAudio: map[string]bool{}}
	inStream := map[string]bool{} // audio groups with a rendition in the variant itself
	var next *Variant
	for _, line := range lines(data) {
		tag, value, _ := strings.Cut(line, ":")
		switch {
		case line == "":
		case tag == "#EXT-X-STREAM-INF":
			attrs := attributes(value)
			next = &Variant{Audio: attrs["AUDIO"]}
			next.Bandwidth, _ = strconv.ParseInt(attrs["BANDWIDTH"], 10, 64)
			if _, h, ok := strings.Cut(strings.ToLower(attrs["RESOLUTION"]), "x"); ok {
				next.Height, _ = strconv.Atoi(h)
			}
		case tag == "#EXT-X-MEDIA":
			attrs := attributes(value)
			if attrs["TYPE"] != "AUDIO" {
				continue
			}
			group := attrs["GROUP-ID"]
			if attrs["URI"] == "" {
				inStream[group] = true
			} else {
				m.SeparateAudio[group] = true
			}
		case strings.HasPrefix(line, "#"):
		default:
			if next != nil {
				if next.URI = resolve(base, line); next.URI != "" {
					m.Variants = append(m.Variants, *next)
				}
				next = nil
			}
		}
	}
	for group := range inStream {
		delete(m.SeparateAudio, group)
	}
	return m
}

// Pick returns the variant to play: of those that carry their own sound,
// the one with the highest bandwidth that is at most MaxVariantHeight tall,
// or the lowest if all are taller. It reports false if there is none.
func (m *MasterPlaylist) Pick() (Variant, bool) {
	var best, lowest Variant
	found := false
	for _, v := range m.Variants {
		if v.Audio != "" && m.SeparateAudio[v.Audio] {
			continue
		}
		if !found || v.Bandwidth < lowest.Bandwidth {
			lowest = v
		}
		found = true
		if v.Height <= MaxVariantHeight && (best.URI == "" || v.Bandwidth > best.Bandwidth) {
			best = v
		}
	}
	if best.URI == "" {
		return lowest, found
	}
	return best, true
}

// attributes parses an HLS attribute list: NAME=value pairs separated by
// commas, where a quoted value may hold commas. Quotes are removed.
func attributes(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		name, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var value string
		if quoted, ok := strings.CutPrefix(rest, `"`); ok {
			value, rest, _ = strings.Cut(quoted, `"`)
			_, rest, _ = strings.Cut(rest, ",")
		} else {
			value, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.ToUpper(strings.TrimSpace(name))] = strings.TrimSpace(value)
		s = rest
	}
	return out
}

// parseIV parses an initialization vector written as a hexadecimal number
// of up to 128 bits, such as 0x1A.
func parseIV(s string) ([]byte, bool) {
	digits, ok := strings.CutPrefix(strings.ToLower(s), "0x")
	if !ok || digits == "" || len(digits) > 32 {
		return nil, false
	}
	iv, err := hex.DecodeString(strings.Repeat("0", 32-len(digits)) + digits)
	if err != nil {
		return nil, false
	}
	return iv, true
}

// resolve returns ref, an address in a playlist, made absolute against
// base, or "" if it is not an http or https address.
func resolve(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	u, err := base.Parse(ref)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	return u.String()
}
