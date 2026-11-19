// SPDX-License-Identifier: AGPL-3.0-or-later

package mpegts

import (
	"bytes"
	"math/rand/v2"
	"testing"
)

// packets returns n packets whose payloads are stuffing.
func packets(n int) []byte {
	b := bytes.Repeat([]byte{0xFF}, n*PacketSize)
	for i := range n {
		b[i*PacketSize] = SyncByte
	}
	return b
}

func TestStart(t *testing.T) {
	junk := []byte("ICY 200 OK\r\n\r\n")
	text := bytes.Repeat([]byte("not a stream\n"), 300)
	lucky := bytes.Clone(packets(8)[100:])
	lucky[0] = SyncByte // part way through a packet, at a byte that looks like a packet's first
	for _, c := range []struct {
		name      string
		in        []byte
		at        int
		ts, known bool
	}{
		{"nothing", nil, 0, false, false},
		{"two packets", packets(2), 0, true, true},
		{"one packet", packets(1), 0, false, false},
		{"part way", packets(8)[100:], 88, true, true},
		{"part way, too few packets yet", packets(3)[100:], 0, false, false},
		{"after junk", append(bytes.Clone(junk), packets(6)...), len(junk), true, true},
		{"part way at a byte like a packet's first", lucky, 88, true, true},
		{"text", text, 0, false, true},
		{"a little text", text[:100], 0, false, false},
		{"one packet's first byte, then none", append([]byte{SyncByte}, bytes.Repeat([]byte{1}, StartBytes)...), 0, false, true},
		{"packets too far in", append(bytes.Repeat([]byte{0xFF}, startWithin+1), packets(6)...), 0, false, true},
	} {
		at, ts, known := Start(c.in)
		if at != c.at || ts != c.ts || known != c.known {
			t.Errorf("%s: Start = %d, %v, %v; want %d, %v, %v", c.name, at, ts, known, c.at, c.ts, c.known)
		}
	}
}

func TestStartTellsWithinStartBytes(t *testing.T) {
	rng := rand.New(rand.NewPCG(5, 6))
	b := make([]byte, StartBytes)
	for range 1000 {
		for i := range b {
			b[i] = byte(rng.IntN(256))
		}
		if _, _, known := Start(b); !known {
			t.Fatalf("not known after %d bytes starting % x", len(b), b[:16])
		}
	}
}
