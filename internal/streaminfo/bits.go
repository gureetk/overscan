// SPDX-License-Identifier: AGPL-3.0-or-later

package streaminfo

import "errors"

// errShort is what a bit reader gives when the data ends early.
var errShort = errors.New("streaminfo: the data ends early")

// bits reads a NAL unit's payload, without its emulation prevention bytes,
// a bit at a time, most significant first. Reading past its end sets err,
// and every read after gives 0.
type bits struct {
	b   []byte
	pos int // in bits
	err error
}

func (r *bits) u(n int) uint64 {
	var v uint64
	for range n {
		if r.pos >= len(r.b)*8 {
			r.err = errShort
			return 0
		}
		bit := r.b[r.pos/8] >> (7 - r.pos%8) & 1
		v = v<<1 | uint64(bit)
		r.pos++
	}
	return v
}

func (r *bits) flag() bool { return r.u(1) == 1 }

func (r *bits) skip(n int) {
	if r.pos+n > len(r.b)*8 {
		r.pos, r.err = len(r.b)*8, errShort
		return
	}
	r.pos += n
}

// ue reads an unsigned Exp-Golomb code. A code longer than 32 bits, which
// no parameter set holds, is an error.
func (r *bits) ue() uint64 {
	zeros := 0
	for !r.flag() {
		if r.err != nil {
			return 0
		}
		zeros++
		if zeros > 32 {
			r.err = errors.New("streaminfo: an Exp-Golomb code is too long")
			return 0
		}
	}
	return 1<<zeros - 1 + r.u(zeros)
}

// se reads a signed Exp-Golomb code.
func (r *bits) se() int64 {
	k := r.ue()
	if k%2 == 1 {
		return int64(k+1) / 2
	}
	return -int64(k / 2)
}

// unescape removes the emulation prevention bytes from a NAL unit: each 3
// after two zero bytes.
func unescape(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for _, c := range nal {
		if zeros >= 2 && c == 3 {
			zeros = 0
			continue
		}
		out = append(out, c)
		if c == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}
