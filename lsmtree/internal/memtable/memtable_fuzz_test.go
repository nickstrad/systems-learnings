package memtable

import (
	"bytes"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// FuzzMemtable runs a script of Add, Get, and SeekGE against the
// memtable and a sorted slice, and checks they agree.
//
// Each op takes two bytes: an opcode byte and a key byte. Keys come from
// a four-letter alphabet with lengths 1 to 3, so the script revisits
// keys (many versions), and some keys are prefixes of others.
//
//	opcode%4: 0 put, 1 delete, 2 Get, 3 SeekGE then up to 3 Nexts
//	opcode/4: the seed for the memtable (first byte only), else the
//	          value length for a put
//	key byte: bits 6–7 give the length - 1 (3 means 1), bits 0–5 the
//	          letters, two bits each
func FuzzMemtable(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0x00, 2, 0x00})                         // put a, get a
	f.Add([]byte{0, 0x40, 0, 0x00, 1, 0x40, 2, 0x40, 3, 0}) // prefixes, delete, seek
	f.Add([]byte{4, 0x81, 0, 0x81, 0, 0x81, 1, 0x81, 2, 0x81, 3, 0x81})
	f.Add(bytes.Repeat([]byte{0, 0x95, 0, 0x2a, 1, 0x15, 2, 0x6a, 3, 0x40}, 40))
	f.Fuzz(func(t *testing.T, script []byte) {
		seed := uint64(0)
		if len(script) > 0 {
			seed = uint64(script[0] / 4)
		}
		m := New(seed)
		var model []keys.Entry // sorted by keys.Compare
		seq := uint64(0)

		for i := 0; i+1 < len(script); i += 2 {
			op, key := script[i], fuzzKey(script[i+1])
			switch op % 4 {
			case 0, 1:
				seq++
				e := keys.Entry{Key: key, Seq: seq, Kind: keys.KindPut, Value: bytes.Repeat([]byte{'v'}, int(op/4))}
				if op%4 == 1 {
					e.Kind, e.Value = keys.KindDelete, nil
				}
				m.Add(e)
				at, _ := slices.BinarySearchFunc(model, e, keys.Compare)
				model = slices.Insert(model, at, e)

			case 2:
				got, ok := m.Get(key)
				at := seekModel(model, key)
				wantOK := at < len(model) && bytes.Equal(model[at].Key, key)
				if ok != wantOK {
					t.Fatalf("op %d: Get(%q) ok = %v, model says %v", i/2, key, ok, wantOK)
				}
				if ok && !itertest.Equal([]keys.Entry{got}, model[at:at+1]) {
					t.Fatalf("op %d: Get(%q) = %v, model has %v", i/2, key, got, model[at])
				}

			case 3:
				it := m.NewIterator()
				it.SeekGE(key)
				var got []keys.Entry
				for ; it.Valid() && len(got) < 4; it.Next() {
					got = append(got, it.Entry())
				}
				at := seekModel(model, key)
				want := model[at:min(at+4, len(model))]
				if !itertest.Equal(got, want) {
					t.Fatalf("op %d: SeekGE(%q) then Next gave %v, model has %v", i/2, key, got, want)
				}
				if err := it.Close(); err != nil {
					t.Fatalf("Close = %v", err)
				}
			}
		}

		m.checkInvariant()
		if m.Len() != len(model) {
			t.Fatalf("Len = %d, model has %d", m.Len(), len(model))
		}
		it := m.NewIterator()
		it.First()
		if got := itertest.Drain(t, it); !itertest.Equal(got, model) {
			t.Fatalf("full walk differs from the model:\n got %v\nwant %v", got, model)
		}
	})
}

// fuzzKey turns one byte into a 1- to 3-letter key over {0x00, 'a', 'b', 0xff}.
func fuzzKey(b byte) []byte {
	const alphabet = "\x00ab\xff"
	n := int(b>>6)%3 + 1
	key := make([]byte, n)
	for i := range key {
		key[i] = alphabet[(b>>(2*i))&3]
	}
	return key
}

// seekModel returns the index of the first model entry whose user key is
// >= key.
func seekModel(model []keys.Entry, key []byte) int {
	at, _ := slices.BinarySearchFunc(model, key, func(e keys.Entry, k []byte) int {
		if bytes.Compare(e.Key, k) < 0 {
			return -1
		}
		return 1
	})
	return at
}
