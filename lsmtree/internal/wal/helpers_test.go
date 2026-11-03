package wal_test

import (
	"fmt"
	"hash/crc32"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/wal"
)

const logName = "/000001.log"

// manyEntries returns n distinct puts with sequence numbers 1..n.
func manyEntries(n int) []keys.Entry {
	es := make([]keys.Entry, n)
	for i := range es {
		es[i] = itertest.Put(fmt.Sprintf("key-%04d", i), uint64(i+1), fmt.Sprintf("value-%d", i*i))
	}
	return es
}

// encode writes es to a fresh log and returns the file's bytes and the
// offset at which each record ends.
func encode(t testing.TB, es []keys.Entry) (data []byte, ends []int64) {
	t.Helper()
	fs := vfstest.NewMemFS()
	f, err := fs.Create(logName)
	if err != nil {
		t.Fatal(err)
	}
	w := wal.NewWriter(f)
	for _, e := range es {
		if err := w.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
		ends = append(ends, w.Size())
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return vfstest.ReadFile(t, fs, logName), ends
}

// fileWith returns a MemFS holding data under logName, and the file open
// for reading.
func fileWith(t testing.TB, data []byte) (*vfstest.MemFS, vfs.ReadableFile) {
	t.Helper()
	fs := vfstest.NewMemFS()
	vfstest.WriteFile(t, fs, logName, data)
	r, err := fs.Open(logName)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return fs, r
}

// replayAll replays f and returns clones of every entry it saw.
func replayAll(f vfs.ReadableFile) ([]keys.Entry, wal.Report, error) {
	var got []keys.Entry
	rep, err := wal.Replay(f, func(e keys.Entry) error {
		got = append(got, e.Clone())
		return nil
	})
	return got, rep, err
}

// replayBytes replays a log holding data and fails the test on an error.
func replayBytes(t testing.TB, data []byte) ([]keys.Entry, wal.Report) {
	t.Helper()
	_, f := fileWith(t, data)
	got, rep, err := replayAll(f)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	return got, rep
}

// checkPrefix fails unless got equals want[:len(got)].
func checkPrefix(t testing.TB, got, want []keys.Entry) {
	t.Helper()
	if len(got) > len(want) || !itertest.Equal(got, want[:len(got)]) {
		t.Fatalf("replayed %d entries that are not a prefix of the %d appended:\n got %+v\nwant %+v",
			len(got), len(want), got, want)
	}
}

// crc32c is the record checksum, recomputed independently of the package.
func crc32c(b []byte) uint32 {
	return crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli))
}
