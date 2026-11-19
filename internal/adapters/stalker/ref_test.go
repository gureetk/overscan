// SPDX-License-Identifier: AGPL-3.0-or-later

package stalker

import (
	"reflect"
	"testing"
)

func TestRefs(t *testing.T) {
	for _, p := range []Play{{Cmd: "/media/1001.mpg"}, {Cmd: "ffmpeg http://portal.example/a b?x=1&y=2", Episode: 4},
		{Film: "1003"}} {
		if got, ok := ParsePlay(p.Ref()); !ok || got != p {
			t.Errorf("%+v came back as %+v, %v", p, got, ok)
		}
	}
	for _, ref := range []string{"", "cmd=&film=", "cmd=a&film=b", "cmd=a&series=0", "cmd=a&series=x", "%zz"} {
		if p, ok := ParsePlay(ref); ok {
			t.Errorf("%q read as %+v", ref, p)
		}
	}
	for _, o := range []Opening{{Shape: ShapeModule, ID: "2001"}, {Shape: ShapeRequest, Module: ModuleVOD, ID: "2001"},
		{Shape: ShapeInline, Cmd: "/media/2001.mpg", Episodes: []int{1, 2, 5}}} {
		if got, ok := ParseOpening(o.Ref()); !ok || !reflect.DeepEqual(got, o) {
			t.Errorf("%+v came back as %+v, %v", o, got, ok)
		}
	}
	for _, ref := range []string{"", "shape=module", "shape=request&id=1&module=itv", "shape=inline&cmd=x&episodes=",
		"shape=inline&cmd=x&episodes=1,0", "shape=other&id=1"} {
		if o, ok := ParseOpening(ref); ok {
			t.Errorf("%q read as %+v", ref, o)
		}
	}
}
