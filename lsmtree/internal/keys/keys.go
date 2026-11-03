// Package keys defines the entries the store keeps and the order they
// sort in.
//
// Every write gets a sequence number, one higher than the last. An entry
// is a user key, that sequence number, a kind (put or delete), and a
// value. A delete is stored as an entry too: a tombstone. It hides older
// puts of the same key until compaction drops both (the paper's "delete
// node entry", §2.3).
//
// Entries sort by user key ascending, then by sequence number descending,
// so the newest version of a key comes first. Every sorted structure in
// the store (memtable, SSTable, merging iterator) uses this one order.
package keys

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
)

// Limits enforced at the public API. Inside the store they are trusted
// and only asserted.
const (
	MaxKeySize   = 4 << 10 // 4 KiB
	MaxValueSize = 1 << 20 // 1 MiB
)

// Kind says whether an entry sets a value or deletes the key.
type Kind uint8

const (
	KindDelete Kind = 0 // a tombstone; Value is empty
	KindPut    Kind = 1
)

func (k Kind) String() string {
	switch k {
	case KindDelete:
		return "delete"
	case KindPut:
		return "put"
	}
	return "invalid"
}

// Valid reports whether k is one of the defined kinds. Decoders use it to
// reject corrupt bytes.
func (k Kind) Valid() bool { return k == KindDelete || k == KindPut }

// Entry is one version of one key.
type Entry struct {
	Key   []byte
	Seq   uint64
	Kind  Kind
	Value []byte // empty for KindDelete
}

// Compare orders entries by user key ascending, then by sequence number
// descending (newest first). It returns -1, 0, or +1.
func Compare(a, b Entry) int {
	if c := bytes.Compare(a.Key, b.Key); c != 0 {
		return c
	}
	return cmp.Compare(b.Seq, a.Seq) // reversed: higher seq first
}

// Validate checks the rules every stored entry obeys: a key of 1 to
// MaxKeySize bytes, a value of at most MaxValueSize bytes, a valid kind,
// and no value on a tombstone. Decoders call it on untrusted bytes;
// writers assert it on entries the store built itself.
//
//	post: err == nil ⇔ e is a well-formed entry
func (e Entry) Validate() error {
	switch {
	case len(e.Key) == 0:
		return errors.New("keys: empty key")
	case len(e.Key) > MaxKeySize:
		return fmt.Errorf("keys: key of %d bytes is over the %d-byte limit", len(e.Key), MaxKeySize)
	case len(e.Value) > MaxValueSize:
		return fmt.Errorf("keys: value of %d bytes is over the %d-byte limit", len(e.Value), MaxValueSize)
	case !e.Kind.Valid():
		return fmt.Errorf("keys: invalid kind %d", e.Kind)
	case e.Kind == KindDelete && len(e.Value) != 0:
		return fmt.Errorf("keys: tombstone with a %d-byte value", len(e.Value))
	}
	return nil
}

// Clone returns a copy of e that shares no memory with it. Iterators may
// reuse their buffers, so callers that keep an entry clone it.
func (e Entry) Clone() Entry {
	e.Key = bytes.Clone(e.Key)
	e.Value = bytes.Clone(e.Value)
	return e
}
