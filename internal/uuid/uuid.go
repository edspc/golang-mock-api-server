// Package uuid generates and parses the UUIDv8 values that identify callback
// endpoints.
package uuid

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Size is the length of a UUID in bytes.
const Size = 16

// UUID is a 128-bit RFC 9562 identifier.
type UUID [Size]byte

// Version 8 is the RFC 9562 "custom format" version: the layout is defined by
// the implementation. Ours is time-ordered, so endpoint IDs sort by creation
// time and stay readable in logs:
//
//	bits   0..47   unix milliseconds, big-endian
//	bits  48..51   version (8)
//	bits  52..63   per-millisecond sequence counter
//	bits  64..65   variant (0b10)
//	bits  66..127  random
//
// The sequence counter is what makes the ordering real: a millisecond is long
// enough to mint many IDs, and random bits there would order them arbitrarily.
const Version = 8

// maxSeq is the largest value the 12-bit sequence counter can hold.
const maxSeq = 0x0fff

// generator mints monotonic UUIDs. IDs are ordered within one generator, which
// for the server means within one process.
type generator struct {
	mu     sync.Mutex
	lastMs int64
	seq    uint16
}

var defaultGen generator

// NewV8 returns a new time-ordered UUIDv8. It is safe for concurrent use, and
// every ID it returns sorts after every ID it returned before.
func NewV8() (UUID, error) {
	return defaultGen.newAt(time.Now())
}

func (g *generator) newAt(t time.Time) (UUID, error) {
	var u UUID
	if _, err := rand.Read(u[8:]); err != nil {
		return UUID{}, fmt.Errorf("uuid: read random: %w", err)
	}

	ms := t.UnixMilli()
	if ms < 0 {
		ms = 0
	}

	g.mu.Lock()
	if ms > g.lastMs {
		g.lastMs, g.seq = ms, 0
	} else {
		// Same millisecond, or a clock that went backwards: keep counting
		// from the last ID rather than issuing one that sorts earlier.
		if g.seq == maxSeq {
			g.lastMs++
			g.seq = 0
		} else {
			g.seq++
		}
		ms = g.lastMs
	}
	seq := g.seq
	g.mu.Unlock()

	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(ms))
	copy(u[0:6], buf[2:8]) // low 48 bits of the timestamp

	u[6] = (Version << 4) | byte(seq>>8&0x0f)
	u[7] = byte(seq)
	u[8] = (u[8] & 0x3f) | 0x80 // variant 0b10
	return u, nil
}

// String formats the UUID in the canonical 8-4-4-4-12 form.
func (u UUID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}

// Time returns the creation timestamp encoded in the UUID.
func (u UUID) Time() time.Time {
	var buf [8]byte
	copy(buf[2:8], u[0:6])
	return time.UnixMilli(int64(binary.BigEndian.Uint64(buf[:]))).UTC()
}

// ErrInvalid is returned by Parse for anything that is not a canonical UUID.
var ErrInvalid = errors.New("uuid: invalid format")

// Parse decodes a canonical 8-4-4-4-12 UUID string. It accepts any version:
// version checking is IsV8's job, so callers can tell "not a UUID" apart from
// "not one of ours".
func Parse(s string) (UUID, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return UUID{}, ErrInvalid
	}
	var u UUID
	for _, part := range [...]struct{ dst, src [2]int }{
		{[2]int{0, 4}, [2]int{0, 8}},
		{[2]int{4, 6}, [2]int{9, 13}},
		{[2]int{6, 8}, [2]int{14, 18}},
		{[2]int{8, 10}, [2]int{19, 23}},
		{[2]int{10, 16}, [2]int{24, 36}},
	} {
		if _, err := hex.Decode(u[part.dst[0]:part.dst[1]], []byte(s[part.src[0]:part.src[1]])); err != nil {
			return UUID{}, ErrInvalid
		}
	}
	return u, nil
}

// IsV8 reports whether the UUID carries version 8 and the RFC 9562 variant.
func (u UUID) IsV8() bool {
	return u[6]>>4 == Version && u[8]&0xc0 == 0x80
}
