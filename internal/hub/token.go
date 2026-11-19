// SPDX-License-Identifier: AGPL-3.0-or-later

package hub

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
)

// The first byte of every token says its form, so the forms can change: 1
// for a lineup entry's link, 2 for a film's or an episode's, which also
// says which of the two it plays, 3 for a past programme's (D81), 4 for a
// lineup entry's link that may play through a timeshift buffer (D92).
const (
	tokenVersion        = 1
	itemTokenVersion    = 2
	archiveTokenVersion = 3
	shiftTokenVersion   = 4
)

const (
	claimsLen        = 1 + 8 + 8 + 8 + 8         // version, entry, device, expiry, nonce
	itemClaimsLen    = 1 + 1 + 8 + 8 + 8 + 8     // version, kind, item, device, expiry, nonce
	archiveClaimsLen = 1 + 8 + 8 + 8 + 8 + 8 + 8 // version, entry, device, expiry, nonce, start, stop
	macLen           = 16
)

// What a token plays.
const (
	kindEntry   byte = iota // a lineup entry: a live channel
	kindMovie               // a film
	kindEpisode             // an episode of a series
	kindArchive             // a past programme of a lineup entry, from its source's archive (D81)
)

// convertFlag, in a film's or an episode's kind byte, marks a link that
// plays it through ffmpeg (D68); with transcodeFlag, through its source's
// server, which transcodes it (D71).
const (
	convertFlag   byte = 0x80
	transcodeFlag byte = 0x40
)

// claims are what a link's token says: which device may play which lineup
// entry, film or episode, until when the link can start a session, and a
// nonce that names the session it starts.
type claims struct {
	kind     byte
	entryID  int64 // the lineup entry's ID, or the film's or the episode's
	deviceID int64
	expires  int64 // Unix seconds
	nonce    uint64
	convert  bool // a film or an episode that plays through ffmpeg
	// transcode, with convert, says that its source's server transcodes
	// it.
	transcode bool
	// start and stop are, for a past programme, when it began and ended,
	// in Unix seconds.
	start, stop int64
	// shift says that a lineup entry's link plays through a timeshift
	// buffer while timeshift is on (D92).
	shift bool
}

// sign returns c as a token: the claims and an HMAC-SHA256 of them, cut to
// 128 bits, in URL-safe base64.
func (h *Hub) sign(c claims) string {
	var b []byte
	switch c.kind {
	case kindArchive:
		b = make([]byte, archiveClaimsLen, archiveClaimsLen+sha256.Size)
		b[0] = archiveTokenVersion
		binary.BigEndian.PutUint64(b[1:], uint64(c.entryID))
		binary.BigEndian.PutUint64(b[9:], uint64(c.deviceID))
		binary.BigEndian.PutUint64(b[17:], uint64(c.expires))
		binary.BigEndian.PutUint64(b[25:], c.nonce)
		binary.BigEndian.PutUint64(b[33:], uint64(c.start))
		binary.BigEndian.PutUint64(b[41:], uint64(c.stop))
	case kindEntry:
		b = make([]byte, claimsLen, claimsLen+sha256.Size)
		b[0] = tokenVersion
		if c.shift {
			b[0] = shiftTokenVersion
		}
		binary.BigEndian.PutUint64(b[1:], uint64(c.entryID))
		binary.BigEndian.PutUint64(b[9:], uint64(c.deviceID))
		binary.BigEndian.PutUint64(b[17:], uint64(c.expires))
		binary.BigEndian.PutUint64(b[25:], c.nonce)
	default:
		b = make([]byte, itemClaimsLen, itemClaimsLen+sha256.Size)
		b[0], b[1] = itemTokenVersion, c.kind
		if c.convert {
			b[1] |= convertFlag
		}
		if c.transcode {
			b[1] |= transcodeFlag
		}
		binary.BigEndian.PutUint64(b[2:], uint64(c.entryID))
		binary.BigEndian.PutUint64(b[10:], uint64(c.deviceID))
		binary.BigEndian.PutUint64(b[18:], uint64(c.expires))
		binary.BigEndian.PutUint64(b[26:], c.nonce)
	}
	mac := hmac.New(sha256.New, h.key)
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(b)[:len(b)+macLen])
}

// verify returns the claims of a token that this hub signed.
func (h *Hub) verify(token string) (claims, bool) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(b) < 1 {
		return claims{}, false
	}
	n := 0
	switch b[0] {
	case tokenVersion, shiftTokenVersion:
		n = claimsLen
	case itemTokenVersion:
		n = itemClaimsLen
	case archiveTokenVersion:
		n = archiveClaimsLen
	}
	if n == 0 || len(b) != n+macLen {
		return claims{}, false
	}
	mac := hmac.New(sha256.New, h.key)
	mac.Write(b[:n])
	if !hmac.Equal(mac.Sum(nil)[:macLen], b[n:]) {
		return claims{}, false
	}
	switch b[0] {
	case tokenVersion, shiftTokenVersion:
		return claims{
			kind:     kindEntry,
			entryID:  int64(binary.BigEndian.Uint64(b[1:])),
			deviceID: int64(binary.BigEndian.Uint64(b[9:])),
			expires:  int64(binary.BigEndian.Uint64(b[17:])),
			nonce:    binary.BigEndian.Uint64(b[25:]),
			shift:    b[0] == shiftTokenVersion,
		}, true
	case archiveTokenVersion:
		c := claims{
			kind:     kindArchive,
			entryID:  int64(binary.BigEndian.Uint64(b[1:])),
			deviceID: int64(binary.BigEndian.Uint64(b[9:])),
			expires:  int64(binary.BigEndian.Uint64(b[17:])),
			nonce:    binary.BigEndian.Uint64(b[25:]),
			start:    int64(binary.BigEndian.Uint64(b[33:])),
			stop:     int64(binary.BigEndian.Uint64(b[41:])),
		}
		return c, c.stop > c.start
	}
	kind := b[1] &^ (convertFlag | transcodeFlag)
	if kind != kindMovie && kind != kindEpisode {
		return claims{}, false
	}
	return claims{
		kind:      kind,
		convert:   b[1]&convertFlag != 0,
		transcode: b[1]&transcodeFlag != 0,
		entryID:   int64(binary.BigEndian.Uint64(b[2:])),
		deviceID:  int64(binary.BigEndian.Uint64(b[10:])),
		expires:   int64(binary.BigEndian.Uint64(b[18:])),
		nonce:     binary.BigEndian.Uint64(b[26:]),
	}, true
}
