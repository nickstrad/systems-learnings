package codec_test

import (
	"bytes"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
)

// FuzzDecodeEntry feeds arbitrary bytes to DecodeEntry. It must never
// panic, and whatever it accepts must be a valid entry whose canonical
// encoding is exactly the bytes it consumed.
func FuzzDecodeEntry(f *testing.F) {
	f.Add(codec.AppendEntry(nil, itertest.Put("key", 42, "value")))
	f.Add(codec.AppendEntry(nil, itertest.Del("k", 1)))
	f.Add(append(codec.AppendEntry(nil, itertest.Put("a", 1, "")), codec.AppendEntry(nil, itertest.Put("b", 2, "x"))...))
	f.Add(encode(uv(1), "k", 1, 1, []byte{0x80, 0x00}, ""))
	f.Add(encode(uv(1), "k", 1, 0, uv(1), "v"))
	f.Add([]byte{})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x01})
	f.Fuzz(func(t *testing.T, b []byte) {
		e, rest, ok := codec.DecodeEntry(b)
		if !ok {
			return
		}
		if err := e.Validate(); err != nil {
			t.Fatalf("accepted an invalid entry: %v", err)
		}
		consumed := b[:len(b)-len(rest)]
		if !bytes.Equal(codec.AppendEntry(nil, e), consumed) {
			t.Fatalf("re-encoding %+v gives %x, but it was decoded from %x", e, codec.AppendEntry(nil, e), consumed)
		}
		if codec.EntrySize(e) != len(consumed) {
			t.Fatalf("EntrySize %d, consumed %d", codec.EntrySize(e), len(consumed))
		}
	})
}
