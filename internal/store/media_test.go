// SPDX-License-Identifier: AGPL-3.0-or-later

package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/media"
)

func TestMedia(t *testing.T) {
	s := openTest(t, newPath(t))
	ctx := context.Background()
	ids := addSources(t, s, xtreamSource("main"))
	at := time.Date(2026, 12, 4, 10, 0, 0, 0, time.UTC)
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindMovie, filmCategories, films, at); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ApplyLibrary(ctx, ids["main"], KindSeries, nil, []LibraryTitle{{Key: "7001", Name: "Coastline"}}, at); err != nil {
		t.Fatal(err)
	}
	titles := titleIDs(t, s)
	film, series := titles["The Long Field"], titles["Coastline"]
	episodes := []LibraryEpisode{{Key: "9001", Season: 1, Number: 1}, {Key: "9002", Season: 1, Number: 2}}
	if err := s.SetSeriesDetails(ctx, series, TitleDetails{}, episodes, at); err != nil {
		t.Fatal(err)
	}
	eps, err := s.Episodes(ctx, series)
	if err != nil || len(eps) != 2 {
		t.Fatalf("episodes %+v, %v", eps, err)
	}

	movie, err := s.PlayableMovie(ctx, film)
	if err != nil {
		t.Fatal(err)
	}
	if ti, ep := movie.MediaKey(); ti != film || ep != 0 {
		t.Errorf("a film's key %d/%d", ti, ep)
	}
	episode, err := s.PlayableEpisode(ctx, eps[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if ti, ep := episode.MediaKey(); ti != series || ep != eps[1].ID {
		t.Errorf("an episode's key %d/%d", ti, ep)
	}

	if _, ok, err := s.Media(ctx, film, 0); ok || err != nil {
		t.Fatalf("a film never read: %v, %v", ok, err)
	}
	info := media.Info{Format: "matroska,webm", Duration: 5400, Size: 1 << 30, Streams: []media.Stream{
		{Index: 0, Type: media.Video, Codec: "h264", Width: 1920, Height: 1080},
		{Index: 1, Type: media.Audio, Codec: "dts", Channels: 6, Language: "eng"},
	}}
	for _, key := range [][2]int64{{film, 0}, {series, eps[0].ID}, {series, eps[1].ID}} {
		if err := s.SetMedia(ctx, key[0], key[1], info, at); err != nil {
			t.Fatal(err)
		}
	}
	info.Duration = 5401
	if err := s.SetMedia(ctx, film, 0, info, at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Media(ctx, film, 0)
	if !ok || err != nil || !reflect.DeepEqual(got, info) {
		t.Errorf("Media = %+v, %v, %v", got, ok, err)
	}

	// Nothing is kept of a title or an episode that does not exist.
	for _, key := range [][2]int64{{99999, 0}, {series, 99999}, {film, eps[0].ID}} {
		if err := s.SetMedia(ctx, key[0], key[1], info, at); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := s.Media(ctx, key[0], key[1]); ok {
			t.Errorf("kept media of %d/%d", key[0], key[1])
		}
	}

	if err := s.ForgetMedia(ctx, film, 0); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Media(ctx, film, 0); ok {
		t.Error("the film's media is still kept")
	}

	// An episode's goes with the episode, once it has been missing a
	// while.
	if err := s.SetSeriesDetails(ctx, series, TitleDetails{}, episodes[:1], at.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSeriesDetails(ctx, series, TitleDetails{}, episodes[:1], at.Add(LibraryGrace+2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Media(ctx, series, eps[1].ID); ok {
		t.Error("a purged episode's media is still kept")
	}
	if _, ok, _ := s.Media(ctx, series, eps[0].ID); !ok {
		t.Error("the other episode's media is gone")
	}

	// What a build wrote otherwise is read again.
	if _, err := s.db.ExecContext(ctx, `UPDATE media SET info = '{"streams": 3}'`); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Media(ctx, series, eps[0].ID); ok || err != nil {
		t.Errorf("media in another shape: %v, %v", ok, err)
	}
}
