// SPDX-License-Identifier: AGPL-3.0-or-later

package logging

import (
	"bytes"
	"sync"
)

// maxLine caps a line that Recent keeps.
const maxLine = 4096

// Recent keeps the last lines of the log in memory, for the support bundle.
// Write it the log's output, as with io.MultiWriter.
type Recent struct {
	mu      sync.Mutex
	lines   []string // a ring
	next    int
	full    bool
	partial []byte
}

// NewRecent returns a Recent that keeps the last n lines.
func NewRecent(n int) *Recent { return &Recent{lines: make([]string, max(n, 1))} }

// Write takes in log output: whole lines, as a handler writes them, or
// pieces of them.
func (r *Recent) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	data := p
	if len(r.partial) > 0 {
		data = append(r.partial, p...)
		r.partial = nil
	}
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		r.add(data[:i])
		data = data[i+1:]
	}
	if len(data) > maxLine {
		r.add(data)
		data = nil
	}
	if len(data) > 0 {
		r.partial = append([]byte(nil), data...)
	}
	return len(p), nil
}

// add keeps a line. It needs r.mu.
func (r *Recent) add(line []byte) {
	if len(line) > maxLine {
		line = append(line[:maxLine:maxLine], "…"...)
	}
	r.lines[r.next] = string(line)
	r.next++
	if r.next == len(r.lines) {
		r.next, r.full = 0, true
	}
}

// Lines returns the lines kept, oldest first.
func (r *Recent) Lines() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	if r.full {
		out = append(out, r.lines[r.next:]...)
	}
	return append(out, r.lines[:r.next]...)
}
