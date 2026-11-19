// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"net/url"
	"strconv"
	"strings"
)

// What Overscan keeps of a portal's film, series or episode to play it or
// to open it later: a reference, which the library stores with the title
// and the stream hub hands back when it plays.

// Play is what plays a film or an episode: a cmd for vod/create_link, with
// an episode's number for an inline series or a season of a series module;
// or, for a film whose file is asked for apart (has_files), the film's ID,
// whose first file's cmd plays it (MovieFile).
type Play struct {
	Cmd     string
	Episode int    // 0 for none
	Film    string // the ID of a film with has_files; Cmd is then ""
}

// Ref returns the reference that ParsePlay reads.
func (p Play) Ref() string {
	v := url.Values{}
	if p.Film != "" {
		v.Set("film", p.Film)
	} else {
		v.Set("cmd", p.Cmd)
	}
	if p.Episode > 0 {
		v.Set("series", strconv.Itoa(p.Episode))
	}
	return v.Encode()
}

// ParsePlay reads a reference that Play.Ref made.
func ParsePlay(ref string) (Play, bool) {
	v, err := url.ParseQuery(ref)
	if err != nil {
		return Play{}, false
	}
	p := Play{Cmd: v.Get("cmd"), Film: v.Get("film")}
	if s := v.Get("series"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n >= 100_000 {
			return Play{}, false
		}
		p.Episode = n
	}
	if (p.Cmd == "") == (p.Film == "") || len(p.Cmd) > 4096 || len(p.Film) > 64 {
		return Play{}, false // one or the other
	}
	return p, true
}

// The shapes of a series' seasons (section 6).
const (
	ShapeModule  = "module"  // a series module's, with the series' seasons on request
	ShapeInline  = "inline"  // episodes' numbers on the series' item, played with its cmd
	ShapeRequest = "request" // seasons, and their episodes, on request in the module that lists it
)

// Opening is how a series' seasons and episodes are read when it is opened.
type Opening struct {
	Shape  string
	Module string // where a series with seasons on request is listed: vod
	ID     string // the series' ID, for ShapeModule and ShapeRequest
	// Cmd and Episodes are an inline series': its cmd, and its episodes'
	// numbers.
	Cmd      string
	Episodes []int
}

// Ref returns the reference that ParseOpening reads.
func (o Opening) Ref() string {
	v := url.Values{"shape": {o.Shape}}
	switch o.Shape {
	case ShapeInline:
		v.Set("cmd", o.Cmd)
		numbers := make([]string, len(o.Episodes))
		for i, n := range o.Episodes {
			numbers[i] = strconv.Itoa(n)
		}
		v.Set("episodes", strings.Join(numbers, ","))
	case ShapeRequest:
		v.Set("module", o.Module)
		v.Set("id", o.ID)
	default:
		v.Set("id", o.ID)
	}
	return v.Encode()
}

// ParseOpening reads a reference that Opening.Ref made.
func ParseOpening(ref string) (Opening, bool) {
	v, err := url.ParseQuery(ref)
	if err != nil {
		return Opening{}, false
	}
	o := Opening{Shape: v.Get("shape"), Module: v.Get("module"), ID: v.Get("id"), Cmd: v.Get("cmd")}
	switch o.Shape {
	case ShapeModule:
		return o, o.ID != ""
	case ShapeRequest:
		return o, o.ID != "" && (o.Module == ModuleVOD || o.Module == ModuleSeries)
	case ShapeInline:
		for _, s := range strings.Split(v.Get("episodes"), ",") {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n >= 100_000 {
				return Opening{}, false
			}
			o.Episodes = append(o.Episodes, n)
		}
		return o, o.Cmd != "" && len(o.Episodes) > 0
	}
	return Opening{}, false
}
