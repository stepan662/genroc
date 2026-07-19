// Package idgen mints instance ids as time-ordered UUIDv7 values, with helpers to
// keep a process tree sortable: sibling ids are a contiguous increasing run, and a
// child id is always strictly greater than its parent's. That lets the DB order
// (and lock) a tree by id alone — ancestors before descendants, creation order
// within a level.
package idgen

import (
	"bytes"
	"encoding/binary"

	"github.com/google/uuid"
)

func New() string { return NewV7().String() }

// NewV7 returns a fresh time-ordered UUIDv7. uuid.NewV7 only errors when crypto/rand
// fails; the v4 fallback fails the same way, so this never returns a non-unique id.
func NewV7() uuid.UUID {
	if v7, err := uuid.NewV7(); err == nil {
		return v7
	}
	return uuid.New()
}

// Add returns base + n as a 128-bit big-endian integer (carrying low→high word).
// Sibling ids base, base+1, … form a strictly increasing run that sorts in spawn order.
func Add(base uuid.UUID, n uint64) uuid.UUID {
	hi := binary.BigEndian.Uint64(base[:8])
	lo := binary.BigEndian.Uint64(base[8:])
	sum := lo + n
	if sum < lo { // overflow of the low word carries into the high word
		hi++
	}
	binary.BigEndian.PutUint64(base[:8], hi)
	binary.BigEndian.PutUint64(base[8:], sum)
	return base
}

// After returns a UUIDv7 that sorts strictly after prev: a fresh v7 usually does, else
// (same-millisecond mint) prev+1 — guaranteeing a child id exceeds its parent's.
func After(prev uuid.UUID) uuid.UUID {
	v := NewV7()
	if bytes.Compare(v[:], prev[:]) > 0 {
		return v
	}
	return Add(prev, 1)
}

// ChildBase returns a v7 base id that sorts after parentID, for its children. Falls
// back to a plain v7 if parentID isn't a valid UUID (it always is for real instances).
func ChildBase(parentID string) uuid.UUID {
	if p, err := uuid.Parse(parentID); err == nil {
		return After(p)
	}
	return NewV7()
}
