// SPDX-License-Identifier: AGPL-3.0-or-later

package sniff

import (
	"strings"
	"testing"
)

func TestScrub(t *testing.T) {
	for in, want := range map[string]string{
		"nginx/1.24.0": "nginx/1.24.0",
		"Invalid line http://192.0.2.1/u/p/1.ts for you": "Invalid line [address] for you",
		"see user@example.com\tnow":                      "see [address] now",
		"index.m3u8?token=abc and more":                  "[address] and more",
		strings.Repeat("x", 100):                         strings.Repeat("x", 79) + "…",
	} {
		if got := Scrub(in); got != want {
			t.Errorf("Scrub(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDescribe(t *testing.T) {
	for in, want := range map[string]string{
		"<html><head><title>403 http://192.0.2.1/x?user=a</title></head>": `a web page titled "403 [address]"`,
		"  <!DOCTYPE html><p>nothing":                                     "a web page",
		"Access denied\nmore":                                             `text starting "Access denied"`,
		"ID3\x04\x00rest":                                                 "audio with an ID3 tag",
		"\x00\x00\x00\x18ftypisom":                                        "MP4",
		"\x01\x02\x03":                                                    "unknown data starting 01 02 03",
		"":                                                                "nothing",
		"\x47\x40\x11" + strings.Repeat("\xff", 20):                       "unknown data starting 47 40 11 ff ff ff ff ff ff ff ff ff ff ff ff ff",
	} {
		if got := Describe([]byte(in)); got != want {
			t.Errorf("Describe(%q) = %q, want %q", in, got, want)
		}
	}
}
