// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSubtitles(t *testing.T) {
	top := t.TempDir()
	root := filepath.Join(top, "films")
	files := map[string]string{
		"Quiet Harbour (2019)/Quiet Harbour (2019).mkv":                   "video",
		"Quiet Harbour (2019)/Quiet Harbour (2019).srt":                   "plain",
		"Quiet Harbour (2019)/Quiet Harbour (2019).en.srt":                "english",
		"Quiet Harbour (2019)/quiet harbour (2019).eng.forced.SRT":        "forced",
		"Quiet Harbour (2019)/Quiet Harbour (2019).en.hi.srt":             "hearing",
		"Quiet Harbour (2019)/Quiet Harbour (2019).French.Commentary.ass": "commentary",
		"Quiet Harbour (2019)/Quiet Harbour (2019).hi.vtt":                "hindi",
		"Quiet Harbour (2019)/Quiet Harbour (2019).de.sdh.default.ssa":    "german",
		"Quiet Harbour (2019)/Quiet Harbour (2019).de.sub":                "vobsub, not read",
		"Quiet Harbour (2019)/Other Film.en.srt":                          "another film's",
		"Quiet Harbour (2019)/Subs/2_English.srt":                         "release",
		"Quiet Harbour (2019)/Subs/Quiet Harbour (2019).pt.srt":           "portuguese",
		"Quiet Harbour (2019)/Quiet Harbour (2019)-trailer.mkv":           "an extra",
		"Two Films/First.mkv":                                             "video",
		"Two Films/Second.mkv":                                            "video",
		"Two Films/Subs/English.srt":                                      "whose?",
		"Two Films/Subs/First.es.srt":                                     "spanish",
		"../outside.srt":                                                  "outside",
	}
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(top, "outside.srt"), filepath.Join(root, "Two Films", "First.fr.srt")); err != nil {
		t.Fatal(err)
	}

	got, err := Subtitles(root, "Quiet Harbour (2019)/Quiet Harbour (2019).mkv")
	if err != nil {
		t.Fatal(err)
	}
	dir := "Quiet Harbour (2019)/"
	want := []Sidecar{
		{Path: dir + "Quiet Harbour (2019).French.Commentary.ass", Format: "ass", Language: "fre", Title: "Commentary"},
		{Path: dir + "Quiet Harbour (2019).de.sdh.default.ssa", Format: "ssa", Language: "ger", Hearing: true, Default: true},
		{Path: dir + "Quiet Harbour (2019).en.hi.srt", Format: "srt", Language: "eng", Hearing: true},
		{Path: dir + "Quiet Harbour (2019).en.srt", Format: "srt", Language: "eng"},
		{Path: dir + "Quiet Harbour (2019).hi.vtt", Format: "vtt", Language: "hin"},
		{Path: dir + "Quiet Harbour (2019).srt", Format: "srt"},
		{Path: dir + "quiet harbour (2019).eng.forced.SRT", Format: "srt", Language: "eng", Forced: true},
		{Path: dir + "Subs/2_English.srt", Format: "srt", Language: "eng"},
		{Path: dir + "Subs/Quiet Harbour (2019).pt.srt", Format: "srt", Language: "por"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Subtitles =\n%+v\nwant\n%+v", got, want)
	}
	if data, err := ReadSidecar(root, dir+"Quiet Harbour (2019).en.srt"); err != nil || string(data) != "english" {
		t.Errorf("ReadSidecar = %q, %v", data, err)
	}

	// In a folder of two films, a subtitle file in Subs that names neither
	// belongs to neither.
	got, err = Subtitles(root, "Two Films/First.mkv")
	if err != nil {
		t.Fatal(err)
	}
	want = []Sidecar{
		{Path: "Two Films/First.fr.srt", Format: "srt", Language: "fre"},
		{Path: "Two Films/Subs/First.es.srt", Format: "srt", Language: "spa"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Subtitles of one of two =\n%+v\nwant\n%+v", got, want)
	}
	// A link that leads out of the folder cannot be read.
	if _, err := ReadSidecar(root, "Two Films/First.fr.srt"); err == nil {
		t.Error("read a subtitle file outside the folder")
	}
	for _, p := range []string{"../outside.srt", "/etc/passwd", "Quiet Harbour (2019)/Quiet Harbour (2019).mkv", "Two Films/Subs/missing.srt"} {
		if _, err := ReadSidecar(root, p); err == nil {
			t.Errorf("ReadSidecar(%q) read it", p)
		}
	}
	if _, err := Subtitles(root, "../outside.mkv"); err == nil {
		t.Error("listed subtitles of a video outside the folder")
	}
}
