// SPDX-License-Identifier: AGPL-3.0-or-later

package jellyfin

import (
	"net/url"
	"testing"
)

func TestTranscodeURL(t *testing.T) {
	base := "http://jellyfin.example:8096/media/Videos/4f2c/stream.ts?ApiKey=k%26y"
	got, err := TranscodeURL(base, Transcode{From: 1234.56789, Audio: 2, AudioCodecs: []string{"ac3", "aac"},
		DeviceID: "overscan-7", PlaySessionID: "a1b2"})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "jellyfin.example:8096" || u.Path != "/media/Videos/4f2c/stream.ts" {
		t.Errorf("address %s", got)
	}
	want := map[string]string{
		"ApiKey": "k&y", "VideoCodec": "h264", "AllowVideoStreamCopy": "false", "Profile": "high", "Level": "41",
		"MaxVideoBitDepth": "8", "MaxWidth": "1920", "MaxHeight": "1080", "VideoBitrate": "8000000",
		"AudioCodec": "ac3,aac", "MaxAudioChannels": "6", "TranscodingMaxAudioChannels": "6", "AudioStreamIndex": "2",
		"StartTimeTicks": "12345678900", "DeviceId": "overscan-7", "PlaySessionId": "a1b2",
	}
	q := u.Query()
	for k, v := range want {
		if q.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, q.Get(k), v)
		}
	}
	if len(q) != len(want) {
		t.Errorf("query %v", q)
	}

	// From the start, with the server's choice of sound.
	got, _ = TranscodeURL(base, Transcode{Audio: -1, DeviceID: "d", PlaySessionID: "p"})
	q = mustQuery(t, got)
	if q.Has("StartTimeTicks") || q.Has("AudioStreamIndex") || q.Has("AudioCodec") {
		t.Errorf("from the start: %v", q)
	}
	if _, err := TranscodeURL("http://jellyfin.example/elsewhere", Transcode{}); err == nil {
		t.Error("an address that is not a stream's")
	}
}

func TestStopURL(t *testing.T) {
	got, err := StopURL("http://jellyfin.example:8096/media/Videos/4f2c/stream.ts?ApiKey=k%26y&VideoCodec=h264", "overscan-7", "a1b2")
	if err != nil || got != "http://jellyfin.example:8096/media/Videos/ActiveEncodings?ApiKey=k%26y&deviceId=overscan-7&playSessionId=a1b2" {
		t.Errorf("StopURL = %q, %v", got, err)
	}
	if _, err := StopURL("http://jellyfin.example/elsewhere", "d", "p"); err == nil {
		t.Error("an address that is not a stream's")
	}
}

func mustQuery(t *testing.T, address string) url.Values {
	t.Helper()
	u, err := url.Parse(address)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
