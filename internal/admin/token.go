// SPDX-License-Identifier: AGPL-3.0-or-later

package admin

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TokenFile is the admin token's file in the data directory.
const TokenFile = "admin-token"

// Token returns the admin token kept in dataDir, creating a random one,
// readable by its owner only, if there is none.
func Token(dataDir string) (string, error) {
	path := filepath.Join(dataDir, TokenFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, fs.ErrExist) {
		return ReadToken(dataDir)
	}
	if err != nil {
		return "", fmt.Errorf("admin token: %w", err)
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	if _, err := f.WriteString(token + "\n"); err != nil {
		f.Close()
		return "", fmt.Errorf("admin token: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("admin token: %w", err)
	}
	return token, nil
}

// ReadToken returns the admin token kept in dataDir, which the server
// creates when it starts.
func ReadToken(dataDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dataDir, TokenFile))
	if err != nil {
		return "", fmt.Errorf("admin token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 16 {
		return "", errors.New("admin token: the file holds no usable token")
	}
	return token, nil
}

// Valid reports whether got is the token, in constant time.
func Valid(token, got string) bool {
	return token != "" && subtle.ConstantTimeCompare([]byte(token), []byte(got)) == 1
}
