// SPDX-License-Identifier: AGPL-3.0-or-later

package natural

import (
	"slices"
	"testing"
)

func TestCompare(t *testing.T) {
	names := []string{"channel 10", "Channel 2", "channel 1", "Channel 02", "Channel", "channel 1a", "Ärger 3",
		"News 100000000000000000000", "News 99999999999999999999", "b", "A", "a"}
	slices.SortFunc(names, Compare)
	want := []string{"A", "a", "b", "Channel", "channel 1", "channel 1a", "Channel 02", "Channel 2", "channel 10",
		"News 99999999999999999999", "News 100000000000000000000", "Ärger 3"}
	if !slices.Equal(names, want) {
		t.Errorf("sorted\n%q\nwant\n%q", names, want)
	}
	for _, c := range [][2]string{{"x", "x"}, {"", ""}} {
		if Compare(c[0], c[1]) != 0 {
			t.Errorf("%q and %q", c[0], c[1])
		}
	}
}

// Compare is a total order: antisymmetric and transitive on whatever it
// is given.
func FuzzCompare(f *testing.F) {
	f.Add("a2", "a10", "A02")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		if Compare(a, b) != -Compare(b, a) {
			t.Fatalf("not antisymmetric: %q %q", a, b)
		}
		if (Compare(a, b) == 0) != (a == b) {
			t.Fatalf("equal but different: %q %q", a, b)
		}
		if Compare(a, b) <= 0 && Compare(b, c) <= 0 && Compare(a, c) > 0 {
			t.Fatalf("not transitive: %q %q %q", a, b, c)
		}
	})
}
