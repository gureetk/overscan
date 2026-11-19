// SPDX-License-Identifier: AGPL-3.0-or-later

package library

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"overscan.gureet.ca/internal/store"
)

// A recording made in a folder shows at once, without waiting for the
// folder's list to be due (D89).
func TestReadFolderAgain(t *testing.T) {
	l, st, dir := newFolderLibrary(t)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	name := "Evening News - Example News - 2026-12-04 20.00 [3].ts"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("ts"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Another folder changes nothing.
	l.ReadFolderAgain(ctx, filepath.Join(dir, "elsewhere"))
	l.ReadFolderAgain(ctx, dir+"/")
	want := []string{"Calm Water", "Evening News - Example News - 2026-12-04 20.00", "The Long Field"}
	for range 200 {
		if got := shelfTitles(t, st, store.KindMovie)["Home Films"]; reflect.DeepEqual(got, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("shelves %v", shelfTitles(t, st, store.KindMovie))
}

// A change to TMDB's settings reads every folder again (D88).
func TestReadFoldersAgain(t *testing.T) {
	l, st, dir := newFolderLibrary(t)
	ctx := context.Background()
	if _, err := l.Want(ctx, store.KindMovie, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Quiet Hills (2020).mkv"), []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	l.ReadFoldersAgain(ctx)
	want := []string{"Calm Water", "Quiet Hills", "The Long Field"}
	for range 200 {
		if got := shelfTitles(t, st, store.KindMovie)["Home Films"]; reflect.DeepEqual(got, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("shelves %v", shelfTitles(t, st, store.KindMovie))
}
