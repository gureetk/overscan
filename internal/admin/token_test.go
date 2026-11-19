// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToken(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadToken(dir); err == nil {
		t.Error("read a token that does not exist")
	}
	first, err := Token(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Errorf("token %q", first)
	}
	fi, err := os.Stat(filepath.Join(dir, TokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode %o, want 600", perm)
	}
	again, err := Token(dir)
	if err != nil || again != first {
		t.Errorf("second call: %q, %v; want %q", again, err, first)
	}
	if read, err := ReadToken(dir); err != nil || read != first {
		t.Errorf("ReadToken: %q, %v", read, err)
	}

	if !Valid(first, first) || Valid(first, first[:31]) || Valid("", "") {
		t.Error("Valid")
	}
}
