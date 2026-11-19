// SPDX-License-Identifier: AGPL-3.0-or-later

package secrets

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newBox(t *testing.T) *Box {
	t.Helper()
	b, err := NewBox(NewKey())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestSealAndOpen(t *testing.T) {
	b := newBox(t)
	plain := []byte("http://provider.example/get.php?username=u&password=p")
	sealed := b.Seal("stream-url/3", plain)
	if bytes.Contains(sealed, []byte("provider")) || len(sealed) != header+len(plain)+tagSize {
		t.Fatalf("sealed value of %d bytes shows its plaintext or has the wrong size", len(sealed))
	}
	got, err := b.Open("stream-url/3", sealed)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("Open = %q, %v", got, err)
	}
	if again := b.Seal("stream-url/3", plain); bytes.Equal(again, sealed) {
		t.Error("sealing twice gave the same bytes; the nonce must differ")
	}
	if empty, err := b.Open("x", b.Seal("x", nil)); err != nil || len(empty) != 0 {
		t.Errorf("an empty value: %q, %v", empty, err)
	}

	// Another context, a changed byte anywhere, a cut value: damaged.
	if _, err := b.Open("stream-url/4", sealed); !errors.Is(err, ErrDamaged) {
		t.Errorf("another context: %v", err)
	}
	for i := range sealed {
		if i >= 1 && i < 1+idSize {
			continue // the key's ID: another key, below
		}
		changed := bytes.Clone(sealed)
		changed[i] ^= 0x01
		if _, err := b.Open("stream-url/3", changed); !errors.Is(err, ErrDamaged) {
			t.Fatalf("byte %d changed: %v", i, err)
		}
	}
	for _, short := range [][]byte{nil, sealed[:header], sealed[:header+tagSize-1]} {
		if _, err := b.Open("stream-url/3", short); !errors.Is(err, ErrDamaged) {
			t.Errorf("%d bytes: %v", len(short), err)
		}
	}
}

func TestAnotherKey(t *testing.T) {
	a, b := newBox(t), newBox(t)
	sealed := a.Seal("c", []byte("secret"))
	if !a.Owns(sealed) || b.Owns(sealed) {
		t.Errorf("Owns: a %v, b %v", a.Owns(sealed), b.Owns(sealed))
	}
	if _, err := b.Open("c", sealed); !errors.Is(err, ErrOtherKey) {
		t.Errorf("another key: %v", err)
	}
	if bytes.Equal(a.KeyID(), b.KeyID()) {
		t.Error("two keys with the same ID")
	}
	if _, err := NewBox(make([]byte, 16)); err == nil {
		t.Error("a 16-byte key was taken")
	}
}

func TestLoadKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "secret.key")

	if _, _, err := LoadKey(path, false); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no file, without create: %v", err)
	}
	key, info, err := LoadKey(path, true)
	if err != nil || !info.Created || info.Exposed || len(key) != KeySize {
		t.Fatalf("create: %d bytes, %+v, %v", len(key), info, err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the new file: %v, %v", fi.Mode(), err)
	}
	data, _ := os.ReadFile(path)
	if len(data) != 2*KeySize+1 || !strings.HasSuffix(string(data), "\n") {
		t.Errorf("the file holds %q", data)
	}

	// The same key comes back, and is never replaced.
	again, info, err := LoadKey(path, true)
	if err != nil || info.Created || !bytes.Equal(again, key) {
		t.Errorf("load: %+v, %v", info, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, info, _ = LoadKey(path, false); !info.Exposed {
		t.Error("a file that others can read is not reported")
	}
	for _, bad := range []string{"", "not hex\n", strings.Repeat("ab", KeySize-1) + "\n"} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := LoadKey(path, true)
		if err == nil || strings.Contains(err.Error(), bad) && bad != "" {
			t.Errorf("file %q: %v", bad, err)
		}
		if data, _ := os.ReadFile(path); string(data) != bad {
			t.Errorf("file %q was replaced", bad)
		}
	}
	if _, _, err := LoadKey(filepath.Join(dir, "missing", "secret.key"), true); err == nil {
		t.Error("a key file in a missing directory was made")
	}
}
