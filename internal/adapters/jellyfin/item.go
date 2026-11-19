// SPDX-License-Identifier: AGPL-3.0-or-later

package jellyfin

import (
	"encoding/json"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"
)

// itemJSON is an item as Jellyfin writes it: names in PascalCase, IDs as 32
// hexadecimal digits, and null for what it does not know.
type itemJSON struct {
	ID                  string            `json:"Id"`
	Name                string            `json:"Name"`
	Type                string            `json:"Type"`
	ProductionYear      *int              `json:"ProductionYear"`
	CommunityRating     *float64          `json:"CommunityRating"`
	OfficialRating      string            `json:"OfficialRating"`
	Overview            string            `json:"Overview"`
	Genres              []string          `json:"Genres"`
	People              []personJSON      `json:"People"`
	RunTimeTicks        *int64            `json:"RunTimeTicks"`
	DateCreated         string            `json:"DateCreated"`
	DateLastMediaAdded  string            `json:"DateLastMediaAdded"`
	ProductionLocations []string          `json:"ProductionLocations"`
	PremiereDate        string            `json:"PremiereDate"`
	Container           string            `json:"Container"`
	ImageTags           map[string]string `json:"ImageTags"`
	BackdropImageTags   []string          `json:"BackdropImageTags"`
	IndexNumber         *int              `json:"IndexNumber"`
	ParentIndexNumber   *int              `json:"ParentIndexNumber"`
	SeasonName          string            `json:"SeasonName"`
	LocationType        string            `json:"LocationType"`
	ExtraType           string            `json:"ExtraType"`
}

type personJSON struct {
	Name string `json:"Name"`
	Type string `json:"Type"`
}

// maxPeople is how many actors an item keeps.
const maxPeople = 8

// decodeItem reads one item, reporting whether it could: an item needs an
// ID and a name.
func decodeItem(raw json.RawMessage) (Item, bool) {
	var j itemJSON
	if err := json.Unmarshal(raw, &j); err != nil {
		return Item{}, false
	}
	it := Item{
		ID:         clean(j.ID, 64),
		Name:       clean(j.Name, 300),
		Type:       j.Type,
		AgeRating:  clean(j.OfficialRating, 20),
		Plot:       strings.TrimSpace(strings.ToValidUTF8(j.Overview, "")),
		Container:  Extension(j.Container),
		Poster:     clean(j.ImageTags["Primary"], 64),
		SeasonName: clean(j.SeasonName, 120),
		Missing:    j.LocationType == "Virtual",
		extra:      j.ExtraType != "",
		Added:      parseTime(j.DateCreated),
		Updated:    parseTime(j.DateLastMediaAdded),
		Premiere:   parseTime(j.PremiereDate),
	}
	if it.ID == "" || it.Name == "" {
		return Item{}, false
	}
	if len(it.Plot) > 4000 {
		it.Plot = strings.ToValidUTF8(it.Plot[:4000], "")
	}
	if y := j.ProductionYear; y != nil && *y >= 1870 && *y <= 2200 {
		it.Year = *y
	}
	if r := j.CommunityRating; r != nil && !math.IsNaN(*r) && *r > 0 {
		it.Rating = int(math.Round(min(*r, 10) * 10))
	}
	if t := j.RunTimeTicks; t != nil && *t > 0 && *t < math.MaxInt64/100 {
		it.Duration = time.Duration(*t) * 100 // ticks of 100 ns
	}
	if n := j.IndexNumber; n != nil && *n >= 0 && *n < 100_000 {
		it.Number = *n
	}
	if n := j.ParentIndexNumber; n != nil && *n >= 0 && *n < 10_000 {
		it.Season = *n
	}
	for _, g := range j.Genres {
		if g = clean(g, 60); g != "" && !slices.Contains(it.Genres, g) {
			it.Genres = append(it.Genres, g)
		}
	}
	for _, p := range j.People {
		name := clean(p.Name, 80)
		switch {
		case name == "":
		case p.Type == "Actor" && len(it.Cast) < maxPeople:
			it.Cast = append(it.Cast, name)
		case p.Type == "Director" && len(it.Directors) < 4:
			it.Directors = append(it.Directors, name)
		}
	}
	for _, place := range j.ProductionLocations {
		if place = clean(place, 60); place != "" && len(it.Countries) < 3 && !slices.Contains(it.Countries, place) {
			it.Countries = append(it.Countries, place)
		}
	}
	if len(j.BackdropImageTags) > 0 {
		it.Backdrop = clean(j.BackdropImageTags[0], 64)
	}
	return it, true
}

// parseTime reads a time as Jellyfin writes it, such as
// 2024-03-01T12:00:00.0000000Z; zero if it cannot. A time without a zone
// is taken as UTC.
func parseTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" || strings.HasPrefix(s, "0001-") {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02T15:04:05.9999999", s); err == nil {
		return t
	}
	return time.Time{}
}

var extensionPattern = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// Extension returns the file extension for a container as Jellyfin names
// it: a single name such as mkv, or the list that ffprobe gives a format,
// such as mov,mp4,m4a,3gp,3g2,mj2 for MP4 files. "" if it cannot tell.
func Extension(container string) string {
	names := strings.Split(strings.ToLower(strings.TrimSpace(container)), ",")
	has := func(n string) bool { return slices.Contains(names, n) }
	switch {
	case has("mp4"):
		return "mp4"
	case has("matroska"), has("mkv"):
		return "mkv"
	case has("mpegts"):
		return "ts"
	}
	if first := strings.TrimSpace(names[0]); extensionPattern.MatchString(first) {
		return first
	}
	return ""
}
