// SPDX-License-Identifier: AGPL-3.0-or-later

package streaminfo

import (
	"bytes"
	"encoding/hex"
	"reflect"
	"testing"

	"overscan.gureet.ca/internal/tsinfo/tstest"
)

// Parameter sets that ffmpeg's encoders wrote for fictional test pictures.
var (
	h264720p  = "6764001facd9405005bb011000000300100000030320f1831960"
	h2641080i = "67640028acd94078044fde0220000003002000000643e2c5b2c0"
	hevcPQ    = "420101022000000300900000030000030099a001e020021c4d96566924caf016a122012080000003008000001904"
	hevcHLG   = "420101022000000300900000030000030078a003c08010e4d96566924caf016a04240408000003000800000300c840"
	mpeg2Seq  = "b32d024013ffffe018"
	mpeg2Ext  = "b5148a0001"
)

// stream writes a fictional stream: a PAT, a PMT with a video stream of
// the given type and audio streams of the given types, then two video
// pictures, the first of which holds the given units, each after a start
// code.
func stream(t *testing.T, video byte, audio []byte, units ...string) []byte {
	t.Helper()
	var raw [][]byte
	for _, u := range units {
		b, err := hex.DecodeString(u)
		if err != nil {
			t.Fatal(err)
		}
		raw = append(raw, b)
	}
	return streamOf(video, audio, raw...)
}

// streamOf is stream, with the units as they are.
func streamOf(video byte, audio []byte, units ...[]byte) []byte {
	m := tstest.Bare()
	pat := []byte{0x00, 0xB0, 13, 0x00, 0x01, 0xC1, 0, 0, 0x00, 0x01, 0xE0 | tstest.PMTPID>>8, tstest.PMTPID & 0xFF, 0, 0, 0, 0}
	m.Packet(0, true, append([]byte{0}, pat...), 0)
	var streams []byte
	if video != 0 {
		streams = append(streams, video, 0xE0|tstest.VideoPID>>8, tstest.VideoPID&0xFF, 0xF0, 0)
	}
	for i, a := range audio {
		pid := tstest.AudioPID + uint16(i)
		streams = append(streams, a, 0xE0|byte(pid>>8), byte(pid), 0xF0, 0)
	}
	pmt := append([]byte{0x02, 0xB0, byte(9 + len(streams) + 4), 0x00, 0x01, 0xC1, 0, 0,
		0xE0 | tstest.VideoPID>>8, tstest.VideoPID & 0xFF, 0xF0, 0}, streams...)
	m.Packet(tstest.PMTPID, true, append([]byte{0}, append(pmt, 0, 0, 0, 0)...), 0)
	var data []byte
	for _, u := range units {
		data = append(append(data, 0, 0, 0, 1), u...)
	}
	data = append(data, 0, 0, 0, 1, 0x65)
	data = append(data, bytes.Repeat([]byte{0x88}, 500)...)
	m.Picture(0, data)
	m.Picture(tstest.Frame, []byte{0, 0, 0, 1, 0x41, 0x9a})
	return m.Bytes()
}

// detect writes a stream to a Detector in small pieces until it is done.
func detect(b []byte) (*Detector, Details, bool) {
	d := New()
	for i := 0; i < len(b) && !d.Done(); i += 100 {
		d.Write(b[i:min(i+100, len(b))])
	}
	got, ok := d.Details()
	return d, got, ok
}

func TestDetails(t *testing.T) {
	for name, c := range map[string]struct {
		video byte
		audio []byte
		units []string
		want  Details
		res   string
	}{
		"H.264 720p": {0x1B, []byte{0x0F}, []string{h264720p},
			Details{Video: "H.264", Width: 1280, Height: 720, BitDepth: 8, FrameRate: 25, DynamicRange: "SDR", Audio: []string{"AAC"}}, "720p"},
		"H.264 1080i": {0x1B, nil, []string{h2641080i},
			Details{Video: "H.264", Width: 1920, Height: 1080, Interlaced: true, BitDepth: 8, FrameRate: 25, DynamicRange: "SDR"}, "1080i"},
		"HEVC HDR10": {0x24, []byte{0x81, 0x0F}, []string{hevcPQ},
			Details{Video: "HEVC", Width: 3840, Height: 2160, BitDepth: 10, DynamicRange: "HDR10", Audio: []string{"AC-3", "AAC"}}, "2160p"},
		"HEVC HLG": {0x24, nil, []string{hevcHLG},
			Details{Video: "HEVC", Width: 1920, Height: 1080, BitDepth: 10, DynamicRange: "HLG"}, "1080p"},
		"MPEG-2": {0x02, []byte{0x03}, []string{mpeg2Seq, mpeg2Ext},
			Details{Video: "MPEG-2 video", Width: 720, Height: 576, BitDepth: 8, FrameRate: 25, DynamicRange: "SDR", Audio: []string{"MPEG audio"}}, "576p"},
		"MPEG-2 interlaced": {0x02, nil, []string{mpeg2Seq, "b51482"},
			Details{Video: "MPEG-2 video", Width: 720, Height: 576, Interlaced: true, BitDepth: 8, FrameRate: 25, DynamicRange: "SDR"}, "576i"},
		"radio": {0, []byte{0x0F, 0x03}, nil, Details{Audio: []string{"AAC", "MPEG audio"}}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			d, got, ok := detect(stream(t, c.video, c.audio, c.units...))
			if !ok || !d.Done() || !reflect.DeepEqual(got, c.want) || got.Resolution() != c.res {
				t.Errorf("got %+v, %v, done %v, %q; want %+v", got, ok, d.Done(), got.Resolution(), c.want)
			}
		})
	}
}

// A stream that is not MPEG-TS, or whose parameters never come, is read
// for a while, then given up on.
func TestGivesUp(t *testing.T) {
	d := New()
	d.Write([]byte("#EXTM3U\n"))
	if _, ok := d.Details(); ok || d.Done() {
		t.Error("a playlist is not MPEG-TS")
	}
	for range maxRead / 4096 {
		d.Write(make([]byte, 4096))
	}
	if !d.Done() {
		t.Error("not done after reading as much as it reads")
	}
	// A video whose parameter set is not in the stream: the codecs only.
	b := stream(t, 0x1B, []byte{0x0F})
	for len(b) < maxRead {
		b = append(b, b[2*tstest.PacketSize:]...)
	}
	d, got, ok := detect(b)
	if !ok || !d.Done() || got.Video != "H.264" || got.Height != 0 || got.Resolution() != "" {
		t.Errorf("got %+v, %v, done %v", got, ok, d.Done())
	}
}

func TestRank(t *testing.T) {
	order := []Details{
		{Height: 2160, DynamicRange: "HDR10"},
		{Height: 2160, DynamicRange: "SDR"},
		{Height: 1080, FrameRate: 50},
		{Height: 1080, FrameRate: 25},
		{Height: 1080, Interlaced: true},
		{Height: 720},
		{},
	}
	for i := 1; i < len(order); i++ {
		if order[i-1].Rank() <= order[i].Rank() {
			t.Errorf("%+v does not rank above %+v", order[i-1], order[i])
		}
	}
}

// Whatever a Detector reads, it neither fails nor reads too much, and the
// parameter set readers never fail on a unit that is not one.
func FuzzDetector(f *testing.F) {
	for _, u := range []string{h264720p, h2641080i, hevcPQ, hevcHLG, mpeg2Seq} {
		b, _ := hex.DecodeString(u)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, unit []byte) {
		_, _ = h264SPS(unit)
		_, _ = hevcSPS(unit)
		_, _ = mpeg2Sequence(unit)
		for _, video := range []byte{0x1B, 0x24, 0x02} {
			if len(unit) > 60000 {
				return
			}
			_, _, _ = detect(streamOf(video, []byte{0x0F}, unit))
		}
	})
}

func TestSummary(t *testing.T) {
	for _, c := range []struct {
		d            Details
		label, summa string
	}{
		{Details{}, "", ""},
		{Details{Audio: []string{"AAC"}}, "", "AAC"},
		{Details{Video: "H.264", Height: 1080, Interlaced: true, DynamicRange: "SDR", Audio: []string{"AC-3"}}, "1080i", "1080i H.264, AC-3"},
		{Details{Video: "HEVC", Height: 2160, DynamicRange: "HDR10", Audio: []string{"AAC", "AC-3"}}, "2160p HDR10", "2160p HEVC HDR10, AAC, AC-3"},
		{Details{Video: "MPEG-2"}, "", "MPEG-2"},
	} {
		if got := c.d.Label(); got != c.label {
			t.Errorf("%+v: label %q, want %q", c.d, got, c.label)
		}
		if got := c.d.Summary(); got != c.summa {
			t.Errorf("%+v: summary %q, want %q", c.d, got, c.summa)
		}
	}
}
