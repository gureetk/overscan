// SPDX-License-Identifier: AGPL-3.0-or-later

package jellyfin

import (
	"errors"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Transcodes (D71). A Jellyfin server makes a stream of a file that it
// transcodes when its stream address is asked for without static=true: in
// the container that the address names, with what the query asks for. The
// stream starts at once and grows as the server's ffmpeg makes it; it has
// no ranges. The server stops making it a few seconds after the request
// ends, or at once when asked to stop its play session.

// What a box gets of a transcode: H.264 in High profile, level 4.1, at 8
// bits, at most 1920×1080 and 8 Mbit/s, which every MAG box plays, and its
// sound with at most six channels.
const (
	transcodeVideo    = "h264"
	transcodeProfile  = "high"
	transcodeLevel    = "41"
	transcodeWidth    = 1920
	transcodeHeight   = 1080
	transcodeBitrate  = 8_000_000
	transcodeChannels = 6
)

// Transcode is what Overscan asks a server to make of a film or an episode
// whose video a box cannot play.
type Transcode struct {
	// From is where the stream starts, in seconds from the file's start.
	From float64
	// Audio is the index of the sound track that the stream carries, as
	// the server numbers the file's streams, which ffprobe does too; -1 for
	// the server's choice.
	Audio int
	// AudioCodecs are the sound codecs, as ffmpeg names them, that the box
	// plays from the stream: a track in one of them is copied, and others
	// are converted to the first.
	AudioCodecs []string
	// DeviceID and PlaySessionID name the stream to the server: a request
	// with the same three as one whose stream is still being made joins
	// it, so each request needs a play session of its own.
	DeviceID, PlaySessionID string
}

// TranscodeURL returns the address of the stream that the server makes
// as t asks, from base, a source's transcode address (TranscodeURL of
// source.Settings), which holds the key.
func TranscodeURL(base string, t Transcode) (string, error) {
	u, err := url.Parse(base)
	if err != nil || !strings.Contains(u.Path, "/Videos/") {
		return "", errors.New("not a Jellyfin stream address")
	}
	q := u.Query()
	q.Set("VideoCodec", transcodeVideo)
	q.Set("AllowVideoStreamCopy", "false")
	q.Set("Profile", transcodeProfile)
	q.Set("Level", transcodeLevel)
	q.Set("MaxVideoBitDepth", "8")
	q.Set("MaxWidth", strconv.Itoa(transcodeWidth))
	q.Set("MaxHeight", strconv.Itoa(transcodeHeight))
	q.Set("VideoBitrate", strconv.Itoa(transcodeBitrate))
	if len(t.AudioCodecs) > 0 {
		q.Set("AudioCodec", strings.Join(t.AudioCodecs, ","))
	}
	q.Set("MaxAudioChannels", strconv.Itoa(transcodeChannels))
	q.Set("TranscodingMaxAudioChannels", strconv.Itoa(transcodeChannels))
	if t.Audio >= 0 {
		q.Set("AudioStreamIndex", strconv.Itoa(t.Audio))
	}
	if t.From > 0 {
		q.Set("StartTimeTicks", strconv.FormatInt(int64(math.Round(t.From*1e7)), 10))
	}
	q.Set("DeviceId", t.DeviceID)
	q.Set("PlaySessionId", t.PlaySessionID)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// StopURL returns the address to which a DELETE stops what the server
// makes for a play session of a device, from base as for TranscodeURL.
func StopURL(base, deviceID, playSessionID string) (string, error) {
	u, err := url.Parse(base)
	i := -1
	if err == nil {
		i = strings.LastIndex(u.Path, "/Videos/")
	}
	if i < 0 {
		return "", errors.New("not a Jellyfin stream address")
	}
	u.Path = u.Path[:i] + "/Videos/ActiveEncodings"
	u.RawPath = ""
	u.RawQuery = url.Values{"ApiKey": {u.Query().Get("ApiKey")}, "deviceId": {deviceID},
		"playSessionId": {playSessionID}}.Encode()
	return u.String(), nil
}
