// SPDX-License-Identifier: AGPL-3.0-or-later

package folder

import "testing"

func TestCleanName(t *testing.T) {
	for in, want := range map[string]struct {
		name string
		year int
	}{
		"The Long Field (2024)":                        {"The Long Field", 2024},
		"The.Long.Field.2024.1080p.WEB-DL.x264-GROUP":  {"The Long Field", 2024},
		"The_Long_Field_(2024)":                        {"The Long Field", 2024},
		"The.Long.Field.2024":                          {"The Long Field", 2024},
		"Blade.Runner.2049.2017.1080p":                 {"Blade Runner 2049", 2017},
		"Blade.Runner.2049":                            {"Blade Runner 2049", 0},
		"Blade Runner 2049":                            {"Blade Runner 2049", 0},
		"2001 A Space Odyssey (1968)":                  {"2001 A Space Odyssey", 1968},
		"1917.2019.1080p":                              {"1917", 2019},
		"2012":                                         {"2012", 0},
		"Quiet Harbour [1080p] [tmdbid-12345]":         {"Quiet Harbour", 0},
		"Amélie (2001) - Director's Cut":               {"Amélie", 2001},
		"Night Shift (2019) {imdb-tt0000001}":          {"Night Shift", 2019},
		"calm.water.REMUX.2160p":                       {"calm water", 0},
		"Paper Tide 1080p":                             {"Paper Tide", 0},
		"(2019)":                                       {"(2019)", 0},
		"  ":                                           {"", 0},
		"The Office (US)":                              {"The Office (US)", 0},
		"Some.Film.1999.PROPER.DVDRip":                 {"Some Film", 1999},
		"Film Title (2020) [Remux-1080p][DTS-HD MA]":   {"Film Title", 2020},
		"An Old Film (1931) (Restored)":                {"An Old Film", 1931},
		"Silver.Orchard.3.2022.720p.BluRay.x264-GROUP": {"Silver Orchard 3", 2022},
	} {
		name, year := cleanName(in)
		if name != want.name || year != want.year {
			t.Errorf("cleanName(%q) = %q, %d; want %q, %d", in, name, year, want.name, want.year)
		}
	}
}

func TestEpisodeOf(t *testing.T) {
	for in, want := range map[string]struct {
		season, number int
		before, name   string
		ok             bool
	}{
		"Coastline S01E02 - Rocks":            {1, 2, "Coastline ", "Rocks", true},
		"coastline.s1e2.1080p.web":            {1, 2, "coastline.", "", true},
		"Coastline - 1x02 - Rocks":            {1, 2, "Coastline - ", "Rocks", true},
		"The.Office.US.S09E23.Finale.720p":    {9, 23, "The.Office.US.", "Finale", true},
		"S01E01-E02 Two Parts":                {1, 1, "", "Two Parts", true},
		"Coastline.S01.E03":                   {1, 3, "Coastline.", "", true},
		"Show S2024E05":                       {2024, 5, "Show ", "", true},
		"A Film 1920x1080":                    {0, 0, "", "", false},
		"Film.2020.x264":                      {0, 0, "", "", false},
		"Classes 1x":                          {0, 0, "", "", false},
		"Business.Seasons.Greetings.2020.mkv": {0, 0, "", "", false},
	} {
		season, number, before, after, ok := episodeOf(in)
		if ok != want.ok || season != want.season || number != want.number || before != want.before ||
			ok && episodeName(after) != want.name {
			t.Errorf("episodeOf(%q) = %d, %d, %q, %q (%q), %v", in, season, number, before, after, episodeName(after), ok)
		}
	}
}

func TestSeasonsAndNumbers(t *testing.T) {
	for in, want := range map[string]int{"Season 1": 1, "season 01": 1, "S02": 2, "Specials": 0, "Special": 0,
		"Season 0": 0, "Staffel 3": 3, "Series.4": 4, "Season-10": 10} {
		if got, ok := seasonOfFolder(in); !ok || got != want {
			t.Errorf("seasonOfFolder(%q) = %d, %v", in, got, ok)
		}
	}
	for _, in := range []string{"Seasonal", "Coastline", "S", "Season", "Season One"} {
		if got, ok := seasonOfFolder(in); ok {
			t.Errorf("seasonOfFolder(%q) = %d", in, got)
		}
	}
	for in, want := range map[string]struct {
		n    int
		name string
	}{"02 - Sand": {2, "Sand"}, "E03": {3, ""}, "Episode 4 - The Tide": {4, "The Tide"}, "ep.5": {5, ""}} {
		n, after, ok := numberInSeason(in)
		if !ok || n != want.n || episodeName(after) != want.name {
			t.Errorf("numberInSeason(%q) = %d, %q, %v", in, n, episodeName(after), ok)
		}
	}
	if _, _, ok := numberInSeason("Sand"); ok {
		t.Error("a name without a number")
	}
}

func TestExtrasAndFolders(t *testing.T) {
	for in, want := range map[string]bool{"Film-trailer": true, "Film.trailer": true, "film_sample": true, "sample": true,
		"Film-behindthescenes": true, "Film": false, "Trailer Park": false, "The Sample Man": false} {
		if got := isExtra(in); got != want {
			t.Errorf("isExtra(%q) = %v", in, got)
		}
	}
	for in, want := range map[string]bool{"Extras": true, "Featurettes": true, ".hidden": true, "@eaDir": true,
		"#recycle": true, "$RECYCLE.BIN": true, "VIDEO_TS": true, "Behind The Scenes": true, "Season 1": false,
		"Specials": false, "Comedy": false, "": false} {
		if got := skipFolder(in); got != want {
			t.Errorf("skipFolder(%q) = %v", in, got)
		}
	}
	if !sameName("the.office.us", "The Office (US) (2005)") || sameName("Coastline", "Night Shift") || sameName("", "") {
		t.Error("sameName")
	}
}

func FuzzNames(f *testing.F) {
	for _, s := range []string{"The.Long.Field.2024.1080p", "Coastline S01E02 - Rocks", "02 - Sand", "(2019)", "Season 1",
		"a1x2b", "S01E01-E02-E03", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		cleanName(s)
		if _, _, _, after, ok := episodeOf(s); ok {
			episodeName(after)
		}
		numberInSeason(s)
		seasonOfFolder(s)
		isExtra(s)
		normal(s)
	})
}
