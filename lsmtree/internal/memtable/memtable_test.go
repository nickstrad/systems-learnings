package memtable

import (
	"bytes"
	"cmp"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

// build adds entries in seq order, as the DB would, whatever order they
// are given in.
func build(entries ...keys.Entry) *Memtable {
	bySeq := slices.Clone(entries)
	slices.SortFunc(bySeq, func(a, b keys.Entry) int { return cmp.Compare(a.Seq, b.Seq) })
	m := New(1)
	for _, e := range bySeq {
		m.Add(e)
	}
	return m
}

func TestIteratorContract(t *testing.T) {
	itertest.Contract(t, func(t *testing.T, entries []keys.Entry) iterator.Iterator {
		return build(entries...).NewIterator()
	})
}

func TestGet(t *testing.T) {
	cases := []struct {
		name    string
		entries []keys.Entry
		key     string
		want    keys.Entry
		wantOK  bool
	}{
		{name: "empty memtable", key: "a"},
		{name: "single, hit", entries: []keys.Entry{itertest.Put("a", 1, "x")}, key: "a", want: itertest.Put("a", 1, "x"), wantOK: true},
		{name: "single, absent below", entries: []keys.Entry{itertest.Put("b", 1, "x")}, key: "a"},
		{name: "single, absent above", entries: []keys.Entry{itertest.Put("b", 1, "x")}, key: "c"},
		{name: "absent between keys", entries: []keys.Entry{itertest.Put("a", 1, "x"), itertest.Put("c", 2, "y")}, key: "b"},
		{name: "prefix of a present key is absent", entries: []keys.Entry{itertest.Put("ab", 1, "x")}, key: "a"},
		{name: "extension of a present key is absent", entries: []keys.Entry{itertest.Put("a", 1, "x")}, key: "ab"},
		{name: "empty value", entries: []keys.Entry{itertest.Put("a", 1, "")}, key: "a", want: itertest.Put("a", 1, ""), wantOK: true},
		{
			name:    "many versions, newest wins",
			entries: []keys.Entry{itertest.Put("k", 1, "v1"), itertest.Put("k", 5, "v5"), itertest.Put("k", 3, "v3"), itertest.Put("j", 4, "j"), itertest.Put("l", 2, "l")},
			key:     "k", want: itertest.Put("k", 5, "v5"), wantOK: true,
		},
		{
			name:    "tombstone newest is returned",
			entries: []keys.Entry{itertest.Put("k", 1, "v1"), itertest.Del("k", 2)},
			key:     "k", want: itertest.Del("k", 2), wantOK: true,
		},
		{
			name:    "put after tombstone",
			entries: []keys.Entry{itertest.Put("k", 1, "v1"), itertest.Del("k", 2), itertest.Put("k", 3, "v3")},
			key:     "k", want: itertest.Put("k", 3, "v3"), wantOK: true,
		},
		{
			name:    "binary key with zero bytes",
			entries: []keys.Entry{itertest.Put("\x00", 1, "z"), itertest.Put("\x00\x00", 2, "zz")},
			key:     "\x00", want: itertest.Put("\x00", 1, "z"), wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := build(c.entries...)
			got, ok := m.Get([]byte(c.key))
			if ok != c.wantOK {
				t.Fatalf("Get(%q) ok = %v, want %v", c.key, ok, c.wantOK)
			}
			if ok && !itertest.Equal([]keys.Entry{got}, []keys.Entry{c.want}) {
				t.Fatalf("Get(%q) = %q@%d %v %q, want %q@%d %v %q", c.key,
					got.Key, got.Seq, got.Kind, got.Value, c.want.Key, c.want.Seq, c.want.Kind, c.want.Value)
			}
		})
	}
}

func TestIteratorYieldsAllVersions(t *testing.T) {
	m := build(itertest.Put("b", 1, "b1"), itertest.Put("a", 2, "a2"), itertest.Del("b", 3), itertest.Put("a", 4, "a4"))
	want := []keys.Entry{itertest.Put("a", 4, "a4"), itertest.Put("a", 2, "a2"), itertest.Del("b", 3), itertest.Put("b", 1, "b1")}
	it := m.NewIterator()
	defer it.Close()
	it.First()
	if got := itertest.Drain(t, it); !itertest.Equal(got, want) {
		t.Fatalf("iterator = %v, want %v", got, want)
	}
}

func TestAddCopiesBuffers(t *testing.T) {
	m := New(1)
	key, value := []byte("key"), []byte("value")
	m.Add(keys.Entry{Key: key, Seq: 1, Kind: keys.KindPut, Value: value})
	copy(key, "XXX")
	copy(value, "XXXXX")

	got, ok := m.Get([]byte("key"))
	if !ok || string(got.Value) != "value" {
		t.Fatalf("after the caller reused its buffers, Get = %q, %v; want \"value\", true", got.Value, ok)
	}
	// Appending to the stored key must not overwrite the stored value.
	_ = append(got.Key, 'Z')
	if got, _ := m.Get([]byte("key")); string(got.Value) != "value" {
		t.Fatalf("appending to a returned key changed the value to %q", got.Value)
	}
}

func TestLenAndSize(t *testing.T) {
	cases := []struct {
		name string
		e    keys.Entry
	}{
		{"one-byte key, empty value", itertest.Put("a", 0, "")},
		{"tombstone", itertest.Del("a", 0)},
		{"max key size", keys.Entry{Key: bytes.Repeat([]byte{'k'}, keys.MaxKeySize), Kind: keys.KindPut}},
		{"max value size", keys.Entry{Key: []byte("k"), Kind: keys.KindPut, Value: make([]byte, keys.MaxValueSize)}},
	}
	m := New(1)
	if m.Len() != 0 || m.Size() != 0 {
		t.Fatalf("new memtable: Len %d Size %d, want 0 0", m.Len(), m.Size())
	}
	for i, c := range cases {
		c.e.Seq = uint64(i + 1)
		beforeLen, beforeSize := m.Len(), m.Size()
		m.Add(c.e)
		wantGrowth := int64(len(c.e.Key)+len(c.e.Value)) + EntryOverhead
		if m.Len() != beforeLen+1 || m.Size()-beforeSize != wantGrowth {
			t.Errorf("%s: Len %d→%d, Size grew %d; want +1 and +%d",
				c.name, beforeLen, m.Len(), m.Size()-beforeSize, wantGrowth)
		}
	}
}

func TestSizeGrowsMonotonically(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 7))
	m := New(7)
	last := m.Size()
	for seq := uint64(1); seq <= 2000; seq++ {
		m.Add(itertest.Put(fmt.Sprintf("k%d", rng.IntN(300)), seq, strings.Repeat("v", rng.IntN(20))))
		if m.Size() <= last {
			t.Fatalf("after add %d Size went from %d to %d", seq, last, m.Size())
		}
		last = m.Size()
	}
}

func TestFirstAddMayUseSeqZero(t *testing.T) {
	m := New(1)
	m.Add(itertest.Put("a", 0, "x"))
	if got, ok := m.Get([]byte("a")); !ok || got.Seq != 0 {
		t.Fatalf("Get = %v, %v", got, ok)
	}
}

// TestManyRandomAdds builds a list large enough to reach several levels
// and checks the full invariant and the order of the walk.
func TestManyRandomAdds(t *testing.T) {
	const n = 5000
	rng := rand.New(rand.NewPCG(3, 3))
	m := New(3)
	var want []keys.Entry
	for seq := uint64(1); seq <= n; seq++ {
		e := itertest.Put(fmt.Sprintf("%05d", rng.IntN(n/2)), seq, "v")
		m.Add(e)
		want = append(want, e)
	}
	m.checkInvariant()
	if m.height < 4 {
		t.Errorf("height %d after %d adds; expected the list to use several levels", m.height, n)
	}
	slices.SortFunc(want, keys.Compare)
	it := m.NewIterator()
	it.First()
	if got := itertest.Drain(t, it); !itertest.Equal(got, want) {
		t.Fatalf("walk does not match the sorted adds")
	}
}

// TestSameSeedSameShape checks that heights depend only on the seed and
// the order of adds, so failures reproduce.
func TestSameSeedSameShape(t *testing.T) {
	shape := func(seed uint64) []int {
		m := New(seed)
		for seq := uint64(1); seq <= 200; seq++ {
			m.Add(itertest.Put(fmt.Sprintf("k%03d", seq), seq, ""))
		}
		var hs []int
		for x := m.head.next[0]; x != nil; x = x.next[0] {
			hs = append(hs, len(x.next))
		}
		return hs
	}
	if !slices.Equal(shape(42), shape(42)) {
		t.Fatal("two memtables with the same seed have different shapes")
	}
	if slices.Equal(shape(42), shape(43)) {
		t.Fatal("different seeds gave the same 200 heights; is the seed used?")
	}
}

func TestAddAssertions(t *testing.T) {
	cases := []struct {
		name string
		e    keys.Entry
		want string
	}{
		{"seq equal to last", itertest.Put("b", 10, "x"), "seq 10 not above last seq 10"},
		{"seq below last", itertest.Put("b", 9, "x"), "seq 9 not above last seq 10"},
		{"empty key", itertest.Put("", 11, "x"), "empty key"},
		{"key one over max", keys.Entry{Key: make([]byte, keys.MaxKeySize+1), Seq: 11, Kind: keys.KindPut}, "key of 4097 bytes"},
		{"value one over max", keys.Entry{Key: []byte("k"), Seq: 11, Kind: keys.KindPut, Value: make([]byte, keys.MaxValueSize+1)}, "value of 1048577 bytes"},
		{"invalid kind", keys.Entry{Key: []byte("k"), Seq: 11, Kind: 7}, "invalid kind 7"},
		{"tombstone with a value", keys.Entry{Key: []byte("k"), Seq: 11, Kind: keys.KindDelete, Value: []byte("x")}, "tombstone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := New(1)
			m.Add(itertest.Put("a", 10, "x"))
			asserttest.Panics(t, c.want, func() { m.Add(c.e) })
			if m.Len() != 1 {
				t.Fatalf("a rejected Add changed Len to %d", m.Len())
			}
		})
	}
}

// TestCheckInvariantCatchesCorruption breaks the list by hand and checks
// that the full walk notices.
func TestCheckInvariantCatchesCorruption(t *testing.T) {
	fresh := func() *Memtable {
		m := New(5)
		for seq := uint64(1); seq <= 300; seq++ {
			m.Add(itertest.Put(fmt.Sprintf("k%03d", seq), seq, "v"))
		}
		return m
	}
	tallest := func(m *Memtable) *node {
		return m.head.next[m.height-1]
	}
	cases := []struct {
		name    string
		want    string
		corrupt func(m *Memtable)
	}{
		{"level 0 out of order", "level 0 out of order", func(m *Memtable) { m.head.next[0].entry.Key = []byte("zzz") }},
		{"Len wrong", "Len is", func(m *Memtable) { m.n++ }},
		{"Size wrong", "Size is", func(m *Memtable) { m.size-- }},
		{"upper level skips back", "not a subsequence", func(m *Memtable) {
			// Unlink the tallest node from level 0 only: it is still on
			// the upper levels, which are then not a subsequence.
			x := tallest(m)
			p := &m.head
			for p.next[0] != x {
				p = p.next[0]
			}
			p.next[0] = x.next[0]
			m.n--
			m.size -= int64(len(x.entry.Key)+len(x.entry.Value)) + EntryOverhead
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := fresh()
			m.checkInvariant() // intact: no panic
			c.corrupt(m)
			asserttest.Panics(t, c.want, m.checkInvariant)
		})
	}
}

func BenchmarkAdd(b *testing.B) {
	rng := rand.New(rand.NewPCG(1, 1))
	keyList := make([][]byte, 1<<16)
	for i := range keyList {
		keyList[i] = fmt.Appendf(nil, "key%08d", rng.IntN(1<<20))
	}
	value := []byte("value-value-value")
	m := New(1)
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		if i%len(keyList) == 0 && i > 0 {
			m = New(1) // keep the list at a memtable-like size
		}
		m.Add(keys.Entry{Key: keyList[i%len(keyList)], Seq: uint64(i), Kind: keys.KindPut, Value: value})
	}
}

func BenchmarkGet(b *testing.B) {
	for _, n := range []int{1 << 10, 1 << 16} {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			m := New(1)
			keyList := make([][]byte, n)
			for i := range keyList {
				keyList[i] = fmt.Appendf(nil, "key%08d", i)
				m.Add(keys.Entry{Key: keyList[i], Seq: uint64(i + 1), Kind: keys.KindPut, Value: []byte("v")})
			}
			rng := rand.New(rand.NewPCG(2, 2))
			b.ReportAllocs()
			for b.Loop() {
				if _, ok := m.Get(keyList[rng.IntN(n)]); !ok {
					b.Fatal("missing key")
				}
			}
		})
	}
}
