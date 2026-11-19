// SPDX-License-Identifier: AGPL-3.0-or-later

package catalog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"overscan.gureet.ca/internal/source"
)

func TestFolderCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "Films")
	for _, name := range []string{"Calm Water (2021).mkv", "Coastline/Season 1/Coastline S01E01.mkv", "Extras/x.mkv"} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("video"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, r, logs := newStore(t)
	ctx := context.Background()
	settings := source.Settings{Name: "films", Type: source.TypeFolder, URL: dir, Refresh: time.Hour,
		Numbers: source.NumbersPlaylist}
	got, err := r.Test(ctx, settings)
	if err != nil || !reflect.DeepEqual(got, Tested{Folder: "Films", Films: 1, Series: 1}) {
		t.Errorf("test %+v, %v", got, err)
	}
	id, err := st.AddSource(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Refresh(ctx, settings, id); err != nil {
		t.Fatal(err)
	}
	if src, _ := st.Source(ctx, id); src.RefreshedAt.IsZero() || src.RefreshError != "" {
		t.Errorf("after a check %+v", src)
	}
	if !strings.Contains(logs.String(), "source checked") || !strings.Contains(logs.String(), "films=1 series=1") {
		t.Errorf("logs:\n%s", logs)
	}

	empty := settings
	empty.URL = t.TempDir()
	if _, err := r.Test(ctx, empty); err == nil || err.Error() != "the folder holds no films or series" {
		t.Errorf("an empty folder: %v", err)
	}
	// The recordings folder may be empty (D89).
	r.AllowEmpty(empty.URL + "/")
	if got, err := r.Test(ctx, empty); err != nil || got.Films+got.Series != 0 {
		t.Errorf("the empty recordings folder: %+v, %v", got, err)
	}
	gone := settings
	gone.URL = filepath.Join(dir, "gone")
	if _, err := r.Refresh(ctx, gone, id); err == nil || err.Error() != "the folder does not exist" {
		t.Errorf("a folder that is gone: %v", err)
	}
	if src, _ := st.Source(ctx, id); src.RefreshError != "the folder does not exist" {
		t.Errorf("recorded %q", src.RefreshError)
	}
}
