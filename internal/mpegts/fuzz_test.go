// SPDX-License-Identifier: AGPL-3.0-or-later

package mpegts

import "testing"

// FuzzTables reads what a source streams as packets, tables and
// timestamps, which must never fail on it or reach past it.
func FuzzTables(f *testing.F) {
	pat := make([]byte, PacketSize*2)
	pat[0], pat[1], pat[2], pat[3] = SyncByte, 0x40, 0x00, 0x10
	copy(pat[4:], []byte{0x00, 0x00, 0xB0, 0x0D, 0x00, 0x01, 0xC1, 0x00, 0x00, 0x00, 0x01, 0xF0, 0x00})
	pat[PacketSize] = SyncByte
	f.Add(pat)
	f.Add([]byte{0x00, 0x00, 0x01, 0xE0, 0x00, 0x00, 0x80, 0x80, 0x05, 0x21, 0x00, 0x01, 0x00, 0x01})
	f.Add([]byte{SyncByte})
	f.Fuzz(func(t *testing.T, b []byte) {
		if at, ts, _ := Start(b); at < 0 || at > len(b) || (ts && at+PacketSize > len(b)) {
			t.Errorf("Start: %d, %v of %d bytes", at, ts, len(b))
		}
		for i := 0; i+PacketSize <= len(b); i += PacketSize {
			p := Packet(b[i : i+PacketSize])
			payload := p.Payload()
			if len(payload) > PacketSize {
				t.Errorf("payload of %d bytes", len(payload))
			}
			sec := Section(payload)
			for _, pid := range PAT(sec) {
				if pid > NullPID {
					t.Errorf("a program map at PID %#x", pid)
				}
			}
			for _, es := range PMT(sec) {
				if es.PID > NullPID {
					t.Errorf("a stream at PID %#x", es.PID)
				}
			}
		}
		if pts, header, ok := PTS(b); ok && (pts < 0 || pts >= timestampWrap || header < 0 || header > len(b)) {
			t.Errorf("PTS %d, header %d of %d bytes", pts, header, len(b))
		}
		_ = Fingerprint(b)
	})
}
