// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Params are the costs of an argon2id password hash.
type Params struct {
	Memory  uint32 // KiB
	Time    uint32 // passes over the memory
	Threads uint8  // lanes
}

// DefaultParams are the second recommended option of RFC 9106: 64 MiB of
// memory, three passes and four lanes. A hash takes a few hundred
// milliseconds on a small server.
var DefaultParams = Params{Memory: 64 * 1024, Time: 3, Threads: 4}

// Lengths of a password, its salt and its hash.
const (
	MinPasswordChars = 8    // characters
	MaxPasswordBytes = 1024 // bytes, in UTF-8
	saltLen          = 16
	keyLen           = 32
)

// Limits on the parameters that a stored hash may name, so that a damaged
// database cannot make a check take all of the server's memory.
const (
	maxMemory  = 1 << 20 // KiB: 1 GiB
	maxTime    = 64
	minKeyLen  = 16
	maxKeyLen  = 64
	minSaltLen = 8
	maxSaltLen = 64
)

// hashing lets one hash be worked out at a time, since each takes the memory
// that its parameters name.
var hashing = make(chan struct{}, 1)

// ErrBadHash is returned for a stored hash that is not one HashPassword makes.
var ErrBadHash = errors.New("admin: the stored password hash is not an argon2id hash")

var b64 = base64.RawStdEncoding

// HashPassword returns the argon2id hash of password, with a new random salt,
// in the PHC string format: $argon2id$v=19$m=...,t=...,p=...$salt$hash.
func HashPassword(ctx context.Context, password string, p Params) (string, error) {
	salt := make([]byte, saltLen)
	_, _ = rand.Read(salt)
	key, err := idKey(ctx, password, salt, p, keyLen)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// CheckPassword reports whether password is the one that hash was made from.
func CheckPassword(ctx context.Context, hash, password string) (bool, error) {
	p, salt, want, err := parseHash(hash)
	if err != nil {
		return false, err
	}
	got, err := idKey(ctx, password, salt, p, uint32(len(want)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// idKey works out an argon2id key, waiting its turn.
func idKey(ctx context.Context, password string, salt []byte, p Params, n uint32) ([]byte, error) {
	select {
	case hashing <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-hashing }()
	return argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, n), nil
}

// parseHash reads a hash in the form HashPassword writes.
func parseHash(hash string) (Params, []byte, []byte, error) {
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" || parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return Params{}, nil, nil, ErrBadHash
	}
	var p Params
	for _, kv := range strings.Split(parts[3], ",") {
		k, v, ok := strings.Cut(kv, "=")
		n, err := strconv.ParseUint(v, 10, 32)
		if !ok || err != nil {
			return Params{}, nil, nil, ErrBadHash
		}
		switch k {
		case "m":
			p.Memory = uint32(n)
		case "t":
			p.Time = uint32(n)
		case "p":
			if n > 255 {
				return Params{}, nil, nil, ErrBadHash
			}
			p.Threads = uint8(n)
		default:
			return Params{}, nil, nil, ErrBadHash
		}
	}
	salt, err1 := b64.DecodeString(parts[4])
	key, err2 := b64.DecodeString(parts[5])
	switch {
	case err1 != nil || err2 != nil,
		p.Time < 1 || p.Time > maxTime,
		p.Threads < 1,
		p.Memory < 8*uint32(p.Threads) || p.Memory > maxMemory,
		len(salt) < minSaltLen || len(salt) > maxSaltLen,
		len(key) < minKeyLen || len(key) > maxKeyLen:
		return Params{}, nil, nil, ErrBadHash
	}
	return p, salt, key, nil
}

// CheckNewPassword says why password cannot be the administrator's, or
// returns nil.
func CheckNewPassword(password string) error {
	switch {
	case !utf8.ValidString(password):
		return errors.New("the password is not valid UTF-8 text")
	case utf8.RuneCountInString(password) < MinPasswordChars:
		return fmt.Errorf("the password needs at least %d characters", MinPasswordChars)
	case len(password) > MaxPasswordBytes:
		return fmt.Errorf("the password can have at most %d bytes", MaxPasswordBytes)
	case strings.ContainsAny(password, "\r\n\x00"):
		return errors.New("the password cannot contain line breaks or NUL characters")
	}
	return nil
}
