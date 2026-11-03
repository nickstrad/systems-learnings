package sstable

import "github.com/nickstrad/systems-learnings/lsmtree/internal/keys"

// Decoders behind the CRCs, exported for fuzzing: a random file almost
// never passes a CRC, so FuzzSSTableOpen rarely reaches them.
var (
	DecodeBlock = func(b []byte, hasLower bool, lower, last []byte) ([]keys.Entry, error) {
		return decodeBlock(nil, b, hasLower, lower, last)
	}
	DecodeIndex = func(b []byte, dataEnd uint64) (int, error) {
		index, err := decodeIndex(b, dataEnd)
		return len(index), err
	}
)

// CheckInvariant runs the writer's invariant check, and SkewOffset breaks
// it, so a test can show the check trips.
func (w *Writer) CheckInvariant() { w.checkInvariant() }
func (w *Writer) SkewOffset()     { w.offset++ }
