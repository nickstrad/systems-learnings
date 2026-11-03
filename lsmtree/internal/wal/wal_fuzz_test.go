package wal_test

import (
	"bytes"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

// entriesFrom turns fuzz input into a list of valid entries. Each entry
// takes a control byte (bit 0: tombstone), a key length byte, the key, a
// value length byte, and the value. Lengths are cut to what is left; an
// empty key becomes "k", since keys are never empty.
func entriesFrom(data []byte, seq uint64) []keys.Entry {
	var es []keys.Entry
	take := func(n int) []byte {
		n = min(n, len(data))
		b := data[:n]
		data = data[n:]
		return b
	}
	for len(data) >= 2 {
		ctl, klen := data[0], int(data[1])
		data = data[2:]
		e := keys.Entry{Key: take(klen), Seq: seq, Kind: keys.KindPut}
		if len(e.Key) == 0 {
			e.Key = []byte("k")
		}
		seq += uint64(ctl >> 1)
		if ctl&1 == 1 {
			e.Kind = keys.KindDelete
		} else if len(data) > 0 {
			vlen := int(data[0]) * 16 // reach past bufio's buffer now and then
			data = data[1:]
			e.Value = take(vlen)
		}
		es = append(es, e)
	}
	return es
}

// Any list of valid entries round trips exactly, and nothing is dropped.
func FuzzWALRoundTrip(f *testing.F) {
	f.Add([]byte{}, uint64(0))
	f.Add([]byte{0, 1, 'k', 1, 'v'}, uint64(1))
	f.Add([]byte{1, 1, 'k'}, uint64(1<<63))
	f.Add([]byte{0, 0, 0}, uint64(5))                                     // empty key (made "k") and value
	f.Add([]byte{2, 3, 'a', 'b', 'c', 2, 'x', 'y', 1, 1, 'a'}, uint64(9)) // put then delete
	f.Add(bytes.Repeat([]byte{0xfe, 0xff}, 600), ^uint64(0)-5)
	f.Fuzz(func(t *testing.T, data []byte, seq uint64) {
		es := entriesFrom(data, seq)
		enc, _ := encode(t, es)
		got, rep := replayBytes(t, enc)
		if len(got) != len(es) {
			t.Fatalf("replayed %d entries, want %d", len(got), len(es))
		}
		checkPrefix(t, got, es)
		if rep.Records != len(es) || rep.GoodBytes != int64(len(enc)) || rep.DroppedBytes != 0 {
			t.Fatalf("report %+v for %d records in %d bytes", rep, len(es), len(enc))
		}
	})
}

// Arbitrary bytes never make Replay panic or fail, every entry it returns
// is valid, and the report accounts for every byte.
func FuzzWALReplay(f *testing.F) {
	good, _ := encode(f, []keys.Entry{itertest.Put("a", 1, "1"), itertest.Del("b", 2), itertest.Put("c", 3, "")})
	f.Add([]byte{})
	f.Add(good)
	f.Add(good[:len(good)-1])
	f.Add(append(bytes.Clone(good), 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff))
	f.Add(record(payload([]byte{0x81, 0x20}, 1, 1, []byte{0}), 0)) // key over the limit, good CRC
	f.Add(record(payload([]byte{1, 'k'}, 1, 2, []byte{0}), 0))     // invalid kind, good CRC
	f.Add(record(payload([]byte{0}, 1, 1, []byte{1, 'v'}), 0))     // empty key, good CRC
	f.Fuzz(func(t *testing.T, data []byte) {
		got, rep := replayBytes(t, data)
		if rep.Records != len(got) {
			t.Fatalf("report says %d records, fn saw %d", rep.Records, len(got))
		}
		if rep.GoodBytes+rep.DroppedBytes != int64(len(data)) || rep.GoodBytes < 0 || rep.DroppedBytes < 0 {
			t.Fatalf("report %+v does not account for %d bytes", rep, len(data))
		}
		for _, e := range got {
			if err := e.Validate(); err != nil {
				t.Fatalf("replayed an invalid entry %+v: %v", e, err)
			}
		}
		// The good prefix re-encodes to the same bytes: decoding is exact.
		enc, _ := encode(t, got)
		if !bytes.Equal(enc, data[:rep.GoodBytes]) {
			t.Fatalf("good prefix does not re-encode to itself")
		}
	})
}
