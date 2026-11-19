// SPDX-License-Identifier: AGPL-3.0-or-later

package logging

import (
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestRecent(t *testing.T) {
	r := NewRecent(3)
	if got := r.Lines(); len(got) != 0 {
		t.Errorf("at first: %q", got)
	}
	io.WriteString(r, "one\ntwo\n")
	if got := r.Lines(); !slices.Equal(got, []string{"one", "two"}) {
		t.Errorf("two lines: %q", got)
	}
	io.WriteString(r, "thr")
	io.WriteString(r, "ee\nfour\n")
	if got := r.Lines(); !slices.Equal(got, []string{"two", "three", "four"}) {
		t.Errorf("after wrapping: %q", got)
	}
	io.WriteString(r, strings.Repeat("x", maxLine+10))
	if got := r.Lines(); len(got) != 3 || got[2] != strings.Repeat("x", maxLine)+"…" {
		t.Errorf("a long line: %d lines, the last %d bytes", len(got), len(got[len(got)-1]))
	}
}

func TestRecentFromALogger(t *testing.T) {
	r := NewRecent(100)
	log, err := New("info", "text", io.MultiWriter(io.Discard, r))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		log.Info("hello", "n", i)
	}
	log.Debug("not kept")
	got := r.Lines()
	if len(got) != 5 || !strings.Contains(got[4], fmt.Sprintf("msg=hello n=%d", 4)) {
		t.Errorf("lines %q", got)
	}
}
