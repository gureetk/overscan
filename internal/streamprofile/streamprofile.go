// SPDX-License-Identifier: AGPL-3.0-or-later

// Package streamprofile holds the output profiles (D110): how the hub sends
// a live channel to a device or a player, as its source sends it, or made
// H.264 by the hub's ffmpeg at up to a size, for a device that cannot play
// what a source sends or a network too slow for it.
package streamprofile

import "strconv"

// Source is the profile that sends a channel as its source sends it.
const Source = "source"

// Profile is an output profile.
type Profile struct {
	Name  string // as the store and the API know it
	Label string // as the admin offers it

	// For a profile that transcodes: the largest picture, in pixels, and
	// libx264's quality, its limit on the rate, the buffer that the limit
	// is kept over, and the level.
	Width, Height int
	CRF           int
	MaxRate       string
	BufSize       string
	Level         string
}

// Profiles are the output profiles, in the order that the admin offers them.
var Profiles = []Profile{
	{Name: Source, Label: "As the source sends it"},
	{Name: "h264-720p", Label: "H.264, up to 720p", Width: 1280, Height: 720, CRF: 23, MaxRate: "3M", BufSize: "6M", Level: "4.0"},
	{Name: "h264-1080p", Label: "H.264, up to 1080p", Width: 1920, Height: 1080, CRF: 21, MaxRate: "6M", BufSize: "12M", Level: "4.1"},
}

// Find returns the profile with a name.
func Find(name string) (Profile, bool) {
	for _, p := range Profiles {
		if p.Name == name {
			return p, true
		}
	}
	return Profile{}, false
}

// Transcodes reports whether the profile makes the stream anew.
func (p Profile) Transcodes() bool { return p.Width > 0 }

// Args returns ffmpeg's arguments to transcode a live stream to the
// profile: MPEG-TS from standard input, with pipes the only protocol
// allowed, as stream repair reads it (D57); its first video track
// deinterlaced where it is interlaced, scaled down to fit the profile's
// size, never up, and made H.264 with libx264, with a picture that starts
// anew every two seconds, so that a player can start soon; its first sound
// track made AAC in stereo; MPEG-TS on standard output.
func (p Profile) Args() []string {
	fit := "scale=w='min(" + strconv.Itoa(p.Width) + ",iw)':h='min(" + strconv.Itoa(p.Height) + ",ih)'" +
		":force_original_aspect_ratio=decrease:force_divisible_by=2"
	return []string{
		"-hide_banner", "-nostdin", "-nostats", "-loglevel", "warning",
		"-protocol_whitelist", "pipe",
		"-fflags", "+genpts",
		"-analyzeduration", "2000000", "-probesize", "4000000",
		"-f", "mpegts", "-i", "pipe:0",
		"-map", "0:v:0?", "-map", "0:a:0?",
		"-vf", "bwdif=deint=interlaced," + fit + ",format=yuv420p",
		"-c:v", "libx264", "-preset", "veryfast", "-crf", strconv.Itoa(p.CRF),
		"-maxrate", p.MaxRate, "-bufsize", p.BufSize, "-profile:v", "high", "-level:v", p.Level,
		"-force_key_frames", "expr:gte(t,n_forced*2)",
		"-c:a", "aac", "-b:a", "128k", "-ac", "2",
		"-max_interleave_delta", "2000000",
		"-f", "mpegts", "pipe:1",
	}
}
