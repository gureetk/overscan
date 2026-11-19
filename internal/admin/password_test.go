// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// cheap are hash costs that keep tests fast.
var cheap = Params{Memory: 64, Time: 1, Threads: 1}

func TestHashAndCheckPassword(t *testing.T) {
	ctx := context.Background()
	hash, err := HashPassword(ctx, "correct horse", cheap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=64,t=1,p=1$") {
		t.Errorf("hash %q is not in the PHC string format", hash)
	}
	if ok, err := CheckPassword(ctx, hash, "correct horse"); !ok || err != nil {
		t.Errorf("the right password: %v, %v", ok, err)
	}
	if ok, err := CheckPassword(ctx, hash, "correct horse "); ok || err != nil {
		t.Errorf("a wrong password: %v, %v", ok, err)
	}
	again, _ := HashPassword(ctx, "correct horse", cheap)
	if again == hash {
		t.Error("two hashes of one password are the same; the salt is not random")
	}
}

func TestDefaultParams(t *testing.T) {
	if testing.Short() {
		t.Skip("a full-cost hash takes a while")
	}
	ctx := context.Background()
	start := time.Now()
	hash, err := HashPassword(ctx, "correct horse", DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") {
		t.Errorf("hash %q", hash)
	}
	if ok, err := CheckPassword(ctx, hash, "correct horse"); !ok || err != nil {
		t.Errorf("the right password: %v, %v", ok, err)
	}
	t.Logf("two full-cost hashes took %v", time.Since(start))
}

func TestBadHashes(t *testing.T) {
	ctx := context.Background()
	good, _ := HashPassword(ctx, "correct horse", cheap)
	parts := strings.Split(good, "$")
	for name, hash := range map[string]string{
		"empty":           "",
		"other algorithm": strings.Replace(good, "argon2id", "argon2i", 1),
		"other version":   strings.Replace(good, "v=19", "v=16", 1),
		"no salt":         "$argon2id$v=19$m=64,t=1,p=1$$" + parts[5],
		"short key":       "$argon2id$v=19$m=64,t=1,p=1$" + parts[4] + "$AAAA",
		"unknown param":   "$argon2id$v=19$m=64,t=1,p=1,x=2$" + parts[4] + "$" + parts[5],
		"huge memory":     "$argon2id$v=19$m=4194304,t=1,p=1$" + parts[4] + "$" + parts[5],
		"no passes":       "$argon2id$v=19$m=64,t=0,p=1$" + parts[4] + "$" + parts[5],
		"no lanes":        "$argon2id$v=19$m=64,t=1,p=0$" + parts[4] + "$" + parts[5],
		"too many lanes":  "$argon2id$v=19$m=64,t=1,p=300$" + parts[4] + "$" + parts[5],
		"not base64":      "$argon2id$v=19$m=64,t=1,p=1$!!!!$" + parts[5],
		"extra part":      good + "$x",
	} {
		t.Run(name, func(t *testing.T) {
			if ok, err := CheckPassword(ctx, hash, "correct horse"); ok || !errors.Is(err, ErrBadHash) {
				t.Errorf("got %v, %v; want ErrBadHash", ok, err)
			}
		})
	}

	// A hash changed by one character no longer matches.
	key := []byte(parts[5])
	key[3] ^= 1
	tampered := strings.Join(append(parts[:5:5], string(key)), "$")
	if ok, _ := CheckPassword(ctx, tampered, "correct horse"); ok {
		t.Error("a tampered hash matched")
	}
}

func TestHashingWaitsItsTurn(t *testing.T) {
	hashing <- struct{}{} // another hash is being worked out
	defer func() { <-hashing }()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := HashPassword(ctx, "correct horse", cheap); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want the context's error", err)
	}
}

func TestCheckNewPassword(t *testing.T) {
	for pw, ok := range map[string]bool{
		"correct horse":              true,
		"12345678":                   true,
		"ééééééé€":                   true, // 8 characters
		"1234567":                    false,
		"two\nlines":                 false,
		"carriage\rreturn":           false,
		"nul\x00inside":              false,
		strings.Repeat("a", 1024):    true,
		strings.Repeat("a", 1025):    false,
		"not utf-8 \xff\xfe\xfd\xfc": false,
	} {
		if err := CheckNewPassword(pw); (err == nil) != ok {
			t.Errorf("%q: %v", pw, err)
		}
	}
}
