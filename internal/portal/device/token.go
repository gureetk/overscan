// SPDX-License-Identifier: AGPL-3.0-or-later

package device

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
)

// tokenVersion is the first byte of every session token, so the format can
// change.
const tokenVersion = 2

const (
	claimsLen = 1 + 8 + 6 + 8 + 8 // version, device ID, MAC address, secret tag, issued
	macLen    = 16
)

// sessionClaims are what a session token says: which device it is for, by
// ID and MAC address, since a removed device's ID can be given to a new one;
// which secret it was paired with, so that pairing again ends the session;
// and when the token was issued.
type sessionClaims struct {
	deviceID  int64
	mac       string // as the store keeps it
	secretTag uint64 // see secretTag
	issued    int64  // Unix seconds
}

// secretTag names a device's secret by the first 64 bits of its hash, or is
// 0 for an unpaired device.
func secretTag(secretHash string) uint64 {
	b, err := hex.DecodeString(secretHash)
	if err != nil || len(b) < 8 {
		return 0
	}
	return binary.BigEndian.Uint64(b)
}

// sign returns c as a token: the claims and an HMAC-SHA256 of them, cut to
// 128 bits, in URL-safe base64.
func (a *API) sign(c sessionClaims) string {
	hw, err := net.ParseMAC(c.mac)
	if err != nil || len(hw) != 6 {
		panic("device: signing a token for an invalid MAC address " + c.mac)
	}
	b := make([]byte, claimsLen, claimsLen+sha256.Size)
	b[0] = tokenVersion
	binary.BigEndian.PutUint64(b[1:], uint64(c.deviceID))
	copy(b[9:15], hw)
	binary.BigEndian.PutUint64(b[15:], c.secretTag)
	binary.BigEndian.PutUint64(b[23:], uint64(c.issued))
	m := hmac.New(sha256.New, a.key)
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(m.Sum(b)[:claimsLen+macLen])
}

// verify returns the claims of a token that this API signed.
func (a *API) verify(token string) (sessionClaims, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) != claimsLen+macLen || b[0] != tokenVersion {
		return sessionClaims{}, false
	}
	m := hmac.New(sha256.New, a.key)
	m.Write(b[:claimsLen])
	if !hmac.Equal(m.Sum(nil)[:macLen], b[claimsLen:]) {
		return sessionClaims{}, false
	}
	return sessionClaims{
		deviceID:  int64(binary.BigEndian.Uint64(b[1:])),
		mac:       strings.ToUpper(net.HardwareAddr(b[9:15]).String()),
		secretTag: binary.BigEndian.Uint64(b[15:]),
		issued:    int64(binary.BigEndian.Uint64(b[23:])),
	}, true
}
