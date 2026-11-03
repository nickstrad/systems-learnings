package bloom_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/bloom"
)

// FuzzBloomNoFalseNegatives splits the input into keys, builds a filter,
// and checks that every key is reported as maybe present. It also probes
// the raw input as a filter, which must never panic.
func FuzzBloomNoFalseNegatives(f *testing.F) {
	f.Add(uint8(10), []byte("a,b,c"))
	f.Add(uint8(1), []byte(""))
	f.Add(uint8(0), []byte(",,,"))
	f.Add(uint8(20), []byte("key000001,key000002,key000003,\x00\xff"))
	f.Add(uint8(255), []byte("one-long-key-with-no-separators"))
	f.Fuzz(func(t *testing.T, bits uint8, data []byte) {
		bitsPerKey := int(bits%40) + 1
		var ks [][]byte
		start := 0
		for i, c := range data {
			if c == ',' {
				ks = append(ks, data[start:i])
				start = i + 1
			}
		}
		ks = append(ks, data[start:])

		filter := bloom.New(bitsPerKey, ks)
		for _, k := range ks {
			if !filter.MayContain(k) {
				t.Fatalf("bitsPerKey=%d: false negative for %q", bitsPerKey, k)
			}
		}
		bloom.Filter(data).MayContain([]byte("probe")) // arbitrary bytes: must not panic
	})
}
