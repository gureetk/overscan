// SPDX-License-Identifier: AGPL-3.0-or-later

// Package secrets keeps the values that could reveal a source's credentials
// encrypted at rest (D46). A value is sealed with AES-256-GCM under a key kept
// in a file outside the database, so that a copy of the database, such as a
// backup, holds none of them.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// KeySize is the size of a key in bytes, for AES-256.
const KeySize = 32

// A sealed value is a version byte, the key's ID, a nonce, then the
// ciphertext with GCM's tag. The ID tells a value sealed with another key
// from a damaged one.
const (
	version   = 1
	idSize    = 4
	nonceSize = 12
	tagSize   = 16
	header    = 1 + idSize + nonceSize
)

var (
	// ErrOtherKey is returned for a value sealed with another key.
	ErrOtherKey = errors.New("secrets: sealed with another key")
	// ErrDamaged is returned for a value that this package did not seal, or
	// that has changed since, or that was sealed for another context.
	ErrDamaged = errors.New("secrets: damaged")
)

// Box seals and opens values with one key. It is safe for concurrent use.
type Box struct {
	aead cipher.AEAD
	id   [idSize]byte
}

// NewBox returns a box for a key of KeySize bytes.
func NewBox(key []byte) (*Box, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: a key has %d bytes, not %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	b := &Box{aead: aead}
	sum := sha256.Sum256(append([]byte("overscan key id\x00"), key...))
	copy(b.id[:], sum[:idSize])
	return b, nil
}

// NewKey returns a new random key.
func NewKey() []byte {
	key := make([]byte, KeySize)
	_, _ = rand.Read(key) // crypto/rand.Read does not fail
	return key
}

// NewRandomBox returns a box with a new random key that no file keeps, for
// values that need not outlive the process, as in tests.
func NewRandomBox() *Box {
	b, err := NewBox(NewKey())
	if err != nil {
		panic(err) // a key of KeySize bytes always makes a box
	}
	return b
}

// Seal encrypts plaintext. context says what the value is and whose, such as
// "stream-url/3"; Open needs the same context, so that a value moved to
// another place does not open there.
func (b *Box) Seal(context string, plaintext []byte) []byte {
	out := make([]byte, header, header+len(plaintext)+tagSize)
	out[0] = version
	copy(out[1:], b.id[:])
	_, _ = rand.Read(out[1+idSize : header])
	return b.aead.Seal(out, out[1+idSize:header], plaintext, additional(out[:header], context))
}

// Open decrypts a value that Seal returned for the same context.
func (b *Box) Open(context string, sealed []byte) ([]byte, error) {
	if len(sealed) < header+tagSize || sealed[0] != version {
		return nil, ErrDamaged
	}
	if !b.Owns(sealed) {
		return nil, ErrOtherKey
	}
	plain, err := b.aead.Open(nil, sealed[1+idSize:header], sealed[header:], additional(sealed[:header], context))
	if err != nil {
		return nil, ErrDamaged
	}
	return plain, nil
}

// Owns reports whether a value was sealed with this box's key.
func (b *Box) Owns(sealed []byte) bool {
	return len(sealed) >= 1+idSize && sealed[0] == version && subtle.ConstantTimeCompare(sealed[1:1+idSize], b.id[:]) == 1
}

// KeyID returns the ID that this box's sealed values carry, for finding the
// values sealed with another key in a database.
func (b *Box) KeyID() []byte { return b.id[:] }

// additional is the data that a value's tag covers besides its ciphertext:
// its header and its context.
func additional(h []byte, context string) []byte {
	return append(append(make([]byte, 0, len(h)+len(context)), h...), context...)
}

// KeyInfo says what LoadKey found.
type KeyInfo struct {
	Created bool // the file did not exist, and LoadKey made it
	Exposed bool // users other than the file's owner can read it
}

// LoadKey reads the key in the file at path: the key's bytes in hexadecimal,
// on one line. If there is no such file and create is true, it makes one with
// a new random key, readable by its owner only. A file that holds no key is an
// error: it is never replaced, since values sealed with its key would be lost.
func LoadKey(path string, create bool) ([]byte, KeyInfo, error) {
	var info KeyInfo
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) && create {
		key, err := makeKey(path)
		if errors.Is(err, fs.ErrExist) {
			// Another process made it meanwhile.
			return LoadKey(path, false)
		}
		info.Created = err == nil
		return key, info, err
	}
	if err != nil {
		return nil, info, fmt.Errorf("key file: %w", err)
	}
	key, err := hex.DecodeString(strings.TrimSpace(string(data)))
	if err != nil || len(key) != KeySize {
		return nil, info, fmt.Errorf("key file %s: it holds no key of %d bytes in hexadecimal; restore it from a copy", path, KeySize)
	}
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		info.Exposed = true
	}
	return key, info, nil
}

// makeKey writes a new key to a new file at path, and makes sure it reaches
// the disk before it is used.
func makeKey(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("key file: %w", err)
	}
	key := NewKey()
	_, err = f.WriteString(hex.EncodeToString(key) + "\n")
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = syncDir(filepath.Dir(path))
	}
	if err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("key file: %w", err)
	}
	return key, nil
}

// syncDir makes a new file's entry in dir durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}
