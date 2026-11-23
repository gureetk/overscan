// SPDX-License-Identifier: AGPL-3.0-or-later

// Package streaminfo finds out what an MPEG-TS stream holds as it goes by
// (D138): its video's codec, picture size, scan, bit depth, frame rate and
// dynamic range, and its audio's codecs. It reads the program map, then
// the video's parameter set, and stops reading once it has them, or once
// it has read enough to give up, so that it costs a stream almost nothing.
package streaminfo

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"overscan.gureet.ca/internal/mpegts"
)

// Details are what a stream holds.
type Details struct {
	Video        string  // the video's codec, such as H.264 or HEVC; "" if it has none
	Width        int     // the picture's size, 0 if not known
	Height       int     //
	Interlaced   bool    // the video is interlaced
	BitDepth     int     // 0 if not known
	FrameRate    float64 // 0 if not known
	DynamicRange string  // SDR, HDR10 or HLG; "" if not known
	Audio        []string
}

// Resolution names the picture's size the short way, such as 1080p, 2160p
// or 576i; "" if not known.
func (d Details) Resolution() string {
	if d.Height == 0 {
		return ""
	}
	scan := "p"
	if d.Interlaced {
		scan = "i"
	}
	return fmt.Sprint(d.Height, scan)
}

// Label is what a list shows of it: its resolution, with its dynamic range
// when it is high, such as "1080i" or "2160p HDR10"; "" if not known.
func (d Details) Label() string {
	out := d.Resolution()
	if out != "" && d.HDR() {
		out += " " + d.DynamicRange
	}
	return out
}

// HDR reports whether its picture has a high dynamic range.
func (d Details) HDR() bool { return d.DynamicRange == "HDR10" || d.DynamicRange == "HLG" }

// Summary names what it holds, such as "1080i H.264, AC-3" or "2160p HEVC
// HDR10, AAC, AC-3"; "" if nothing is known.
func (d Details) Summary() string {
	var words []string
	for _, w := range []string{d.Resolution(), d.Video} {
		if w != "" {
			words = append(words, w)
		}
	}
	if d.HDR() {
		words = append(words, d.DynamicRange)
	}
	parts := d.Audio
	if len(words) > 0 {
		parts = append([]string{strings.Join(words, " ")}, d.Audio...)
	}
	return strings.Join(parts, ", ")
}

// Rank orders copies of a channel from the best (D113, D138): more lines,
// then HDR, then progressive, then a higher frame rate. Unknown details rank
// last.
func (d Details) Rank() int {
	if d.Height == 0 {
		return 0
	}
	r := d.Height * 100
	if d.HDR() {
		r += 50
	}
	if !d.Interlaced {
		r += 20
	}
	if d.FrameRate > 40 {
		r += 10
	}
	return r
}

// Limits on what a Detector reads.
const (
	maxRead    = 8 << 20 // bytes of the stream before it gives up
	maxPESData = 256 << 10
)

// Detector reads a stream as it arrives: write the stream to it until it
// is Done, then ask for its Details.
type Detector struct {
	buf    []byte
	read   int
	synced bool
	done   bool

	pmts    map[uint16]bool
	pmtRead bool
	video   uint16 // the video's PID, 0 if none
	codec   byte   // its stream type
	pes     []byte // the video's data collected so far
	found   bool   // the video's parameters were read
	d       Details
}

// New returns a Detector.
func New() *Detector { return &Detector{pmts: map[uint16]bool{}} }

// Write reads the next piece of the stream. It never fails.
func (t *Detector) Write(p []byte) (int, error) {
	if t.done {
		return len(p), nil
	}
	t.read += len(p)
	t.buf = append(t.buf, p...)
	t.scan()
	if t.read >= maxRead {
		t.done = true
	}
	if t.done {
		t.buf, t.pes = nil, nil
	}
	return len(p), nil
}

// Done reports whether the Detector has read all it will.
func (t *Detector) Done() bool { return t.done }

// Details returns what the Detector found; ok is false if it found no
// program map, as for a stream that is not MPEG-TS.
func (t *Detector) Details() (d Details, ok bool) {
	d = t.d
	d.Audio = slices.Clone(t.d.Audio)
	return d, t.pmtRead
}

// scan reads the whole packets in t.buf.
func (t *Detector) scan() {
	b := t.buf
	for len(b) >= mpegts.PacketSize && !t.done {
		if b[0] == mpegts.SyncByte && (t.synced || (len(b) >= 2*mpegts.PacketSize && b[mpegts.PacketSize] == mpegts.SyncByte)) {
			t.synced = true
			t.packet(mpegts.Packet(b[:mpegts.PacketSize]))
			b = b[mpegts.PacketSize:]
			continue
		}
		if b[0] == mpegts.SyncByte && len(b) < 2*mpegts.PacketSize {
			break
		}
		t.synced = false
		i := 1
		for i < len(b) && b[i] != mpegts.SyncByte {
			i++
		}
		b = b[i:]
	}
	t.buf = append(t.buf[:0], b...)
}

func (t *Detector) packet(p mpegts.Packet) {
	pid := p.PID()
	payload := p.Payload()
	if payload == nil || !p.HasPayload() || p.Scrambled() {
		return
	}
	switch {
	case pid == 0:
		if p.Start() && len(t.pmts) == 0 {
			for _, pmt := range mpegts.PAT(mpegts.Section(payload)) {
				t.pmts[pmt] = true
			}
		}
	case t.pmts[pid]:
		if p.Start() && !t.pmtRead {
			t.readPMT(mpegts.Section(payload))
		}
	case t.pmtRead && pid == t.video && t.video != 0:
		t.videoData(payload, p.Start())
	}
}

// readPMT reads the first program map: the streams' codecs, and which one
// is the video.
func (t *Detector) readPMT(sec []byte) {
	streams := mpegts.PMT(sec)
	if streams == nil {
		return
	}
	t.pmtRead = true
	for _, e := range streams {
		kind, codec, _ := e.Describe()
		switch {
		case kind == "video" && t.video == 0:
			t.video, t.codec, t.d.Video = e.PID, e.Type, codec
		case kind == "audio" && !slices.Contains(t.d.Audio, codec):
			t.d.Audio = append(t.d.Audio, codec)
		}
	}
	if t.video == 0 || !readable(t.codec) {
		// Nothing more to learn: radio, or a codec whose parameters it
		// does not read.
		t.done = true
	}
}

// readable reports whether a Detector reads the parameters of a video of
// this stream type: H.264, HEVC or MPEG-2.
func readable(streamType byte) bool {
	return streamType == 0x1B || streamType == 0x24 || streamType == 0x02
}

// videoData collects the video's data, and looks in it for the parameter
// set as each PES packet ends.
func (t *Detector) videoData(payload []byte, start bool) {
	if start {
		t.look()
		if t.done {
			return
		}
		t.pes = t.pes[:0]
		if _, header, ok := mpegts.PTS(payload); ok {
			payload = payload[header:]
		} else if len(payload) >= 9 && payload[0] == 0 && payload[1] == 0 && payload[2] == 1 {
			payload = payload[min(len(payload), 9+int(payload[8])):]
		}
	}
	if len(t.pes)+len(payload) <= maxPESData {
		t.pes = append(t.pes, payload...)
	}
}

// look reads the parameters in the PES packet's data collected, if it
// holds them.
func (t *Detector) look() {
	if len(t.pes) == 0 {
		return
	}
	data := t.pes
	for {
		i := bytes.Index(data, []byte{0, 0, 1})
		if i < 0 || i+4 > len(data) {
			return
		}
		data = data[i+3:]
		end := bytes.Index(data, []byte{0, 0, 1})
		if end < 0 {
			end = len(data)
		}
		unit := bytes.TrimRight(data[:end], "\x00")
		if len(unit) == 0 {
			continue
		}
		var p picture
		var err error
		switch t.codec {
		case 0x1B:
			if unit[0]&0x1F != 7 {
				continue
			}
			p, err = h264SPS(unit)
		case 0x24:
			if len(unit) < 2 || unit[0]>>1&0x3F != 33 {
				continue
			}
			p, err = hevcSPS(unit)
		case 0x02:
			if unit[0] != 0xB3 {
				continue
			}
			p, err = mpeg2Sequence(unit)
			// Its sequence extension, which follows, says whether it is
			// interlaced.
			if ext := bytes.Index(data[end:], []byte{0, 0, 1, 0xB5}); err == nil && ext >= 0 {
				if x := data[end+ext+4:]; len(x) >= 2 && x[0]>>4 == 1 {
					p.interlaced = x[1]>>3&1 == 0
				}
			}
		}
		if err != nil {
			continue
		}
		t.d.Width, t.d.Height, t.d.Interlaced, t.d.BitDepth, t.d.FrameRate = p.width, p.height, p.interlaced, p.bitDepth, p.frameRate
		switch p.transfer {
		case 16:
			t.d.DynamicRange = "HDR10"
		case 18:
			t.d.DynamicRange = "HLG"
		default:
			t.d.DynamicRange = "SDR"
		}
		t.found, t.done = true, true
		return
	}
}
