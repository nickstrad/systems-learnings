package sstable_test

import (
	"fmt"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

const tablePath = "/db/000001.sst"

// newFS returns a MemFS with the /db directory made.
func newFS(t testing.TB) *vfstest.MemFS {
	t.Helper()
	fs := vfstest.NewMemFS()
	if err := fs.MkdirAll("/db"); err != nil {
		t.Fatal(err)
	}
	return fs
}

// write writes entries to a new table at tablePath and returns its
// summary.
func write(t testing.TB, fs *vfstest.MemFS, opts sstable.WriterOptions, entries []keys.Entry) sstable.Summary {
	t.Helper()
	f, err := fs.Create(tablePath)
	if err != nil {
		t.Fatal(err)
	}
	w := sstable.NewWriter(f, opts)
	for _, e := range entries {
		if err := w.Add(e); err != nil {
			t.Fatalf("Add(%q): %v", e.Key, err)
		}
	}
	sum, err := w.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return sum
}

// open opens the table at tablePath and closes it when the test ends.
func open(t testing.TB, fs *vfstest.MemFS) *sstable.Reader {
	t.Helper()
	f, err := fs.Open(tablePath)
	if err != nil {
		t.Fatal(err)
	}
	r, err := sstable.Open(f)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// build writes entries and opens the result.
func build(t testing.TB, opts sstable.WriterOptions, entries []keys.Entry) (*sstable.Reader, sstable.Summary) {
	t.Helper()
	fs := newFS(t)
	sum := write(t, fs, opts, entries)
	return open(t, fs), sum
}

// numbered returns n puts with keys k0000, k0001, ... and value size
// valueLen.
func numbered(n, valueLen int) []keys.Entry {
	out := make([]keys.Entry, n)
	for i := range out {
		v := make([]byte, valueLen)
		for j := range v {
			v[j] = byte('a' + (i+j)%26)
		}
		out[i] = keys.Entry{Key: []byte(fmt.Sprintf("k%04d", i)), Seq: uint64(i + 1), Kind: keys.KindPut, Value: v}
	}
	return out
}

// encodedSize is the size of one data block entry with a key and value
// shorter than 128 bytes (one-byte uvarints).
func encodedSize(key, value int) int { return 1 + key + 8 + 1 + 1 + value }
