package manifest_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

func file(num uint64, smallest, largest string) meta.FileMeta {
	return meta.FileMeta{Num: num, Size: int64(100 * num), Smallest: []byte(smallest), Largest: []byte(largest), MinSeq: num, MaxSeq: num + 1, Entries: 3}
}

// sameVersion compares versions, treating a nil level list or level as
// equal to an empty one (Decode always returns non-nil level slices), and
// a nil cursor or cursor list as equal to an empty one.
func sameVersion(a, b meta.Version) bool {
	if a.NextFileNum != b.NextFileNum || a.LastSeq != b.LastSeq || a.LogNum != b.LogNum ||
		len(a.Levels) != len(b.Levels) || len(a.Cursors) != len(b.Cursors) {
		return false
	}
	for i := range a.Cursors {
		if !bytes.Equal(a.Cursors[i], b.Cursors[i]) {
			return false
		}
	}
	for i := range a.Levels {
		if len(a.Levels[i]) != len(b.Levels[i]) {
			return false
		}
		for j, f := range a.Levels[i] {
			g := b.Levels[i][j]
			if f.Num != g.Num || f.Size != g.Size || f.MinSeq != g.MinSeq || f.MaxSeq != g.MaxSeq || f.Entries != g.Entries ||
				!bytes.Equal(f.Smallest, g.Smallest) || !bytes.Equal(f.Largest, g.Largest) {
				return false
			}
		}
	}
	return true
}

// manyLevels has seven levels, some empty, binary keys at both ends of
// the byte range, and cursors on some levels.
func manyLevels() meta.Version {
	return meta.Version{
		NextFileNum: 100, LastSeq: 500, LogNum: 99,
		Cursors: [][]byte{nil, []byte("\x00\x02"), nil, []byte("key\n")},
		Levels: [][]meta.FileMeta{
			{file(10, "\x00", "\xff\xff"), file(11, "a", "z"), file(12, "\x00", "\x00")},
			{file(1, "\x00", "\x00\x01"), file(2, "\x00\x02", "abc"), file(3, "b\x00c", "\xff")},
			nil,
			{file(4, "key\n\"quoted\"", "key\\slash")},
			{},
			{file(5, strings.Repeat("k", 4096), strings.Repeat("k", 4096))},
			{file(6, "\xff\xff\xff", "\xff\xff\xff\xff")},
		},
	}
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		v    meta.Version
	}{
		{"zero version", meta.Version{}},
		{"counters only", meta.Version{NextFileNum: 7, LastSeq: 1 << 63, LogNum: 6}},
		{"max counters", meta.Version{NextFileNum: ^uint64(0), LastSeq: ^uint64(0), LogNum: ^uint64(0) - 1}},
		{"empty levels", meta.Version{NextFileNum: 1, Levels: make([][]meta.FileMeta, 3)}},
		{"one file", meta.Version{NextFileNum: 2, LastSeq: 9, LogNum: 0, Levels: [][]meta.FileMeta{{file(1, "a", "a")}}}},
		{"many levels, binary keys, cursors", manyLevels()},
		{"one cursor per level", meta.Version{NextFileNum: 1, Levels: make([][]meta.FileMeta, 2), Cursors: [][]byte{nil, {0xff}}}},
		{"only nil cursors", meta.Version{NextFileNum: 1, Levels: make([][]meta.FileMeta, 2), Cursors: [][]byte{nil, nil}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := manifest.Encode(tt.v)
			got, err := manifest.Decode(b)
			if err != nil {
				t.Fatalf("Decode(Encode(v)): %v\n%s", err, b)
			}
			if !sameVersion(got, tt.v) {
				t.Fatalf("round trip changed the version:\n got %+v\nwant %+v", got, tt.v)
			}
			if again := manifest.Encode(got); !bytes.Equal(again, b) {
				t.Fatalf("encoding is not stable:\n%s\nthen\n%s", b, again)
			}
		})
	}
}

// TestEncodeReadable pins the format: one line of compact JSON, hex keys
// and cursors, and a checksum trailer line.
func TestEncodeReadable(t *testing.T) {
	v := meta.Version{
		NextFileNum: 3, LastSeq: 5, LogNum: 2,
		Levels:  [][]meta.FileMeta{nil, {{Num: 1, Size: 10, Smallest: []byte("a"), Largest: []byte{0x61, 0x00, 0xff}, MinSeq: 1, MaxSeq: 5, Entries: 2}}},
		Cursors: [][]byte{nil, []byte("a")},
	}
	got := string(manifest.Encode(v))
	body := `{"nextFileNum":3,"lastSeq":5,"logNum":2,` +
		`"levels":[[],[{"num":1,"size":10,"smallest":"61","largest":"6100ff","minSeq":1,"maxSeq":5,"entries":2}]],` +
		`"cursors":["","61"]}` + "\n"
	if !strings.HasPrefix(got, body) {
		t.Fatalf("body =\n%s\nwant\n%s", got, body)
	}
	trailer := got[len(body):]
	var crc uint32
	if n, err := fmt.Sscanf(trailer, "crc32c %08x\n", &crc); n != 1 || err != nil || len(trailer) != len("crc32c 00000000\n") {
		t.Fatalf("trailer = %q, want \"crc32c <8 hex digits>\\n\"", trailer)
	}
}

// TestDecodeWithoutCursors reads a manifest written before cursors were
// persisted: it has no "cursors" field, and every level starts its sweep
// from the smallest key. Encode also leaves the field out when there are
// no cursors, so such a manifest re-encodes byte for byte.
func TestDecodeWithoutCursors(t *testing.T) {
	old := manifest.Seal([]byte(`{"nextFileNum": 3, "lastSeq": 5, "logNum": 2, "levels": [[], []]}`))
	v, err := manifest.Decode(old)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if v.Cursors != nil {
		t.Fatalf("cursors = %q, want nil", v.Cursors)
	}
	if b := manifest.Encode(v); bytes.Contains(b, []byte("cursors")) {
		t.Fatalf("Encode of a version with no cursors wrote the field:\n%s", b)
	}
}

func TestDecodeCorrupt(t *testing.T) {
	good := manifest.Encode(manyLevels())
	body := good[:bytes.LastIndex(good[:len(good)-1], []byte("\n"))+1]
	flip := func(i int) []byte {
		b := bytes.Clone(good)
		b[i] ^= 0x01
		return b
	}
	sealed := func(s string) []byte { return manifest.Seal([]byte(s)) }
	valid := func(levels string) []byte {
		return sealed(`{"nextFileNum": 10, "lastSeq": 10, "logNum": 1, "levels": ` + levels + `}`)
	}
	f := func(num int, lo, hi string, minSeq, maxSeq int) string {
		return fmt.Sprintf(`{"num": %d, "size": 1, "smallest": %q, "largest": %q, "minSeq": %d, "maxSeq": %d, "entries": 1}`, num, lo, hi, minSeq, maxSeq)
	}
	if _, err := manifest.Decode(valid(`[[` + f(2, "61", "62", 1, 2) + `]]`)); err != nil {
		t.Fatalf("the base case for the invariant rows must decode: %v", err)
	}

	tests := []struct {
		name string
		b    []byte
	}{
		{"empty", nil},
		{"one byte short of a trailer", []byte("crc32c 0000000\n")[1:]},
		{"trailer only, empty body", []byte("crc32c 00000000\n")},
		{"body with no trailer", body},
		{"trailer missing its newline", good[:len(good)-1]},
		{"trailer with a bad prefix", append(bytes.Clone(body), "crc32x 00000000\n"...)},
		{"trailer with non-hex checksum", append(bytes.Clone(body), "crc32c 0000zz00\n"...)},
		{"checksum bit flipped", flip(len(good) - 2)},
		{"body bit flipped", flip(10)},
		{"body bit flipped in a key", flip(bytes.Index(good, []byte(`"6b6b`)) + 3)},
		{"checksum valid, not JSON", sealed("hello\n")},
		{"checksum valid, JSON array", sealed("[]\n")},
		{"checksum valid, truncated JSON", sealed(`{"nextFileNum": 1`)},
		{"checksum valid, unknown field", sealed(`{"nextFileNum": 1, "extra": 2}`)},
		{"checksum valid, two objects", sealed(`{"nextFileNum": 1} {"nextFileNum": 1}`)},
		{"checksum valid, negative counter", sealed(`{"nextFileNum": -1}`)},
		{"checksum valid, counter overflows", sealed(`{"nextFileNum": 18446744073709551616}`)},
		{"checksum valid, string counter", sealed(`{"nextFileNum": "1"}`)},
		{"bad hex key", valid(`[[` + f(2, "6", "62", 1, 2) + `]]`)},
		{"non-hex key", valid(`[[` + f(2, "zz", "62", 1, 2) + `]]`)},
		{"invariant: smallest > largest", valid(`[[` + f(2, "62", "61", 1, 2) + `]]`)},
		{"invariant: minSeq > maxSeq", valid(`[[` + f(2, "61", "62", 3, 2) + `]]`)},
		{"invariant: maxSeq above lastSeq", valid(`[[` + f(2, "61", "62", 1, 11) + `]]`)},
		{"invariant: file number at next file number", valid(`[[` + f(10, "61", "62", 1, 2) + `]]`)},
		{"invariant: duplicate file number", valid(`[[` + f(2, "61", "62", 1, 2) + `], [` + f(2, "61", "62", 1, 2) + `]]`)},
		{"invariant: L0 out of order", valid(`[[` + f(3, "61", "62", 1, 2) + `, ` + f(2, "61", "62", 1, 2) + `]]`)},
		{"invariant: L1 ranges overlap", valid(`[[], [` + f(2, "61", "63", 1, 2) + `, ` + f(3, "63", "64", 1, 2) + `]]`)},
		{"invariant: L1 out of order", valid(`[[], [` + f(2, "65", "66", 1, 2) + `, ` + f(3, "61", "62", 1, 2) + `]]`)},
		{"invariant: log number at next file number", sealed(`{"nextFileNum": 10, "logNum": 10}`)},
		{"negative size", valid(`[[{"num": 2, "size": -1, "smallest": "61", "largest": "61"}]]`)},
		{"negative entries", valid(`[[{"num": 2, "entries": -1, "smallest": "61", "largest": "61"}]]`)},
		{"non-hex cursor", sealed(`{"nextFileNum": 1, "levels": [[], []], "cursors": ["", "zz"]}`)},
		{"odd-length cursor", sealed(`{"nextFileNum": 1, "levels": [[], []], "cursors": ["", "6"]}`)},
		{"cursor that is not a string", sealed(`{"nextFileNum": 1, "levels": [[], []], "cursors": [1]}`)},
		{"invariant: more cursors than levels", sealed(`{"nextFileNum": 1, "levels": [[]], "cursors": ["", "61"]}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := manifest.Decode(tt.b)
			if !errors.Is(err, manifest.ErrCorrupt) {
				t.Fatalf("Decode = %+v, %v; want ErrCorrupt", v, err)
			}
		})
	}
}

// TestDecodeEveryTruncation cuts a valid manifest at every length: each
// cut must be reported as corrupt, never accepted or panicking.
func TestDecodeEveryTruncation(t *testing.T) {
	good := manifest.Encode(manyLevels())
	for n := range len(good) {
		if _, err := manifest.Decode(good[:n]); !errors.Is(err, manifest.ErrCorrupt) {
			t.Fatalf("Decode of the first %d of %d bytes: err = %v, want ErrCorrupt", n, len(good), err)
		}
	}
}

// TestEncodeAssertsInvariant shows Encode refuses to persist a broken
// Version: that would be our bug, so it halts instead of writing it.
func TestEncodeAssertsInvariant(t *testing.T) {
	asserttest.Panics(t, "not below next file number", func() {
		manifest.Encode(meta.Version{NextFileNum: 1, Levels: [][]meta.FileMeta{{file(5, "a", "b")}}})
	})
}
