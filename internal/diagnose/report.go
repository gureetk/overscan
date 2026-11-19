// SPDX-License-Identifier: AGPL-3.0-or-later

package diagnose

import (
	"fmt"
	"io"
	"strings"
	"time"

	"overscan.gureet.ca/internal/sniff"
	"overscan.gureet.ca/internal/tsinfo"
)

// Report is the result of checking a channel.
type Report struct {
	Number   int
	Name     string
	Source   string        // the source's name
	For      time.Duration // how long the channel was played
	Upstream SourceReport
	Player   PlayerReport
	Repair   string   // whether the stream went through ffmpeg (D57), if it matters; "" if not
	Log      []string // what the hub logged meanwhile
}

// WriteTo writes the report as text.
func (r Report) WriteTo(w io.Writer) (int64, error) {
	var b strings.Builder
	line := func(indent int, format string, args ...any) {
		b.WriteString(strings.Repeat("  ", indent))
		fmt.Fprintf(&b, format, args...)
		b.WriteString("\n")
	}

	line(0, "Channel %d: %s, from %s", r.Number, r.Name, r.Source)
	line(0, "")
	line(0, "What the source answers")
	switch {
	case r.Player.Link == ".m3u8":
		line(1, "The channel's address ends in .m3u8, so a box gets a .m3u8 link and plays the HLS itself, with ffmpeg. Below, the hub relays it, as it does for a .ts link.")
	case r.Upstream.Ext == "":
		line(1, "The channel's address has no extension, so a box gets a .ts link and plays it with ffrt.")
	default:
		line(1, "The channel's address ends in %s, so a box gets a .ts link and plays it with ffrt.", r.Upstream.Ext)
	}
	for i, h := range r.Upstream.Hops {
		parts := []string{}
		if h.ContentType != "" {
			parts = append(parts, h.ContentType)
		}
		if h.ContentLength >= 0 {
			parts = append(parts, fmt.Sprintf("length %d", h.ContentLength))
		} else if h.Next == "" {
			parts = append(parts, "length not given")
		}
		if h.Server != "" {
			parts = append(parts, "server "+h.Server)
		}
		text := h.Status
		if h.Next != "" {
			text += ": on to " + h.Next
			if h.NextExt != "" {
				text += ", to an address ending in " + h.NextExt
			}
		}
		if len(parts) > 0 {
			text += " (" + strings.Join(parts, ", ") + ")"
		}
		line(1, "%d. %s", i+1, text)
	}
	switch {
	case r.Upstream.Err != "":
		line(1, "The source could not be asked: %s.", r.Upstream.Err)
	case r.Upstream.Kind != "":
		line(1, "It sends %s.", r.Upstream.Kind)
		for _, n := range r.Upstream.Notes {
			line(2, "%s", n)
		}
	}

	line(0, "")
	line(0, "What a player gets through Overscan, over %s", seconds(r.For))
	if r.Repair != "" {
		line(1, "%s", r.Repair)
	}
	p := r.Player
	s := p.Stream
	switch {
	case p.Status != 200:
		line(1, "Overscan answered %d: %s", p.Status, p.Error)
	case !s.TS:
		line(1, "Not MPEG-TS: %s, %s in all.", sniff.Describe(s.Start), megabytes(s.Bytes))
	default:
		end := "the stream was still going at the end"
		if p.Ended {
			end = "the stream ended after " + seconds(s.Elapsed)
		}
		avg := ""
		if s.Elapsed > 0 {
			avg = ", " + rate(int64(float64(s.Bytes*8)/s.Elapsed.Seconds())) + " on average"
		}
		line(1, "MPEG-TS: %s%s; %s.", megabytes(s.Bytes), avg, end)
		line(1, "The longest wait for data was %s.", seconds(s.LongestGap))
		var streams []string
		for _, st := range s.Streams {
			text := fmt.Sprintf("%s %s (PID %d", st.Codec, st.Kind, st.PID)
			if st.Language != "" {
				text += ", " + st.Language
			}
			streams = append(streams, text+")")
		}
		if len(streams) == 0 {
			line(1, "No program map: the streams are not known.")
		} else {
			line(1, "Streams: %s.", strings.Join(streams, "; "))
		}
		writeClock(line, s)
		var troubles []string
		if s.Lost > 0 {
			troubles = append(troubles, fmt.Sprintf("%d packets missing", s.Lost))
		}
		if s.Scrambled > 0 {
			troubles = append(troubles, fmt.Sprintf("%d packets scrambled, which no box can play", s.Scrambled))
		}
		if s.SyncLosses > 0 {
			troubles = append(troubles, fmt.Sprintf("out of step %d times", s.SyncLosses))
		}
		if s.Discontinuities > 0 {
			troubles = append(troubles, fmt.Sprintf("%d discontinuities announced by the source", s.Discontinuities))
		}
		if len(troubles) == 0 {
			line(1, "No packets missing, scrambled or out of step.")
		} else {
			line(1, "%s.", capitalize(strings.Join(troubles, "; ")))
		}
	}

	if len(r.Log) > 0 {
		line(0, "")
		line(0, "What the hub logged")
		for _, l := range r.Log {
			line(1, "%s", l)
		}
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}

// writeClock describes what the stream's clock did.
func writeClock(line func(int, string, ...any), s tsinfo.Report) {
	c := s.Clock
	if c == nil {
		line(1, "No stream with timestamps was found, so the clock is not known.")
		return
	}
	what := "pictures"
	if c.Kind != "video" {
		what = "sound"
	}
	line(1, "The clock, from the %s %s: %s of new %s.", c.Codec, c.Kind, seconds(c.Covered), what)
	if len(c.Jumps) == 0 {
		line(1, "It ran on smoothly, without jumps.")
	} else {
		line(1, "It jumped %s:", times(len(c.Jumps)))
	}
	for i, j := range c.Jumps {
		if i == maxJumpsShown {
			line(2, "and %d more", len(c.Jumps)-maxJumpsShown)
			break
		}
		text := "ahead " + seconds(j.By)
		if j.By < 0 {
			text = "back " + seconds(-j.By) + ", to new " + what
			if j.Repeat {
				text = "back " + seconds(-j.By) + ", to " + what + " already sent"
			}
		}
		line(2, "at %s: %s", position(j.At), text)
	}
	if c.Repeated > 0 {
		line(1, "%s of %s were sent again.", seconds(c.Repeated), what)
	}
}

// maxJumpsShown caps the jumps listed one by one.
const maxJumpsShown = 20

// times says how many times something happened.
func times(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
