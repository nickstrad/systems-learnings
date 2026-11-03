package sstable_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

var errInjected = errors.New("injected fault")

// TestWriterSurfacesEachIOFailure fails each Write in turn, then the
// Sync, and checks that the error reaches the caller (from Add or
// Finish), is not mistaken for corruption, and that the file is closed.
func TestWriterSurfacesEachIOFailure(t *testing.T) {
	entries := numbered(30, 10)
	opts := sstable.WriterOptions{BlockSize: 64, BloomBitsPerKey: 10}

	// Count the writes a clean run makes: one per data block, then the
	// filter, the index, and the footer.
	sum := write(t, newFS(t), opts, entries)
	writes := sum.Blocks + 3

	type fault struct {
		op vfstest.Op
		n  int
	}
	var faults []fault
	for n := 1; n <= writes; n++ {
		faults = append(faults, fault{vfstest.OpWrite, n})
	}
	faults = append(faults, fault{vfstest.OpSync, 1})

	for _, fl := range faults {
		t.Run(fmt.Sprintf("%s#%d", fl.op, fl.n), func(t *testing.T) {
			fs := newFS(t)
			fs.SetFault(vfstest.FailNth(fl.op, fl.n, errInjected))
			f, err := fs.Create(tablePath)
			if err != nil {
				t.Fatal(err)
			}
			w := sstable.NewWriter(f, opts)
			var addErr error
			for _, e := range entries {
				if addErr = w.Add(e); addErr != nil {
					break
				}
			}
			_, finishErr := w.Finish()
			if !errors.Is(finishErr, errInjected) {
				t.Fatalf("Finish = %v (Add = %v), want the injected fault", finishErr, addErr)
			}
			if addErr != nil && !errors.Is(addErr, errInjected) {
				t.Fatalf("Add = %v, want the injected fault", addErr)
			}
			if errors.Is(finishErr, sstable.ErrCorrupt) {
				t.Fatalf("an I/O failure was reported as corruption: %v", finishErr)
			}
			if _, err := f.Write([]byte("x")); err == nil {
				t.Fatal("Finish left the file open after a failure")
			}
		})
	}
	if t.Failed() {
		return
	}
	// One more write than a clean run makes must not fail anything: this
	// checks that the loop above really covered every write.
	fs := newFS(t)
	fs.SetFault(vfstest.FailNth(vfstest.OpWrite, writes+1, errInjected))
	write(t, fs, opts, entries)
}

// TestReaderSurfacesReadFailures checks that a failed read is reported as
// an I/O error, not as corruption, from Open, Get, and the iterator.
func TestReaderSurfacesReadFailures(t *testing.T) {
	entries := numbered(30, 10)
	fs := newFS(t)
	write(t, fs, sstable.WriterOptions{BlockSize: 64}, entries)

	for n := 1; n <= 3; n++ { // footer, filter, index
		fs.SetFault(vfstest.FailNth(vfstest.OpRead, n, errInjected))
		f, _ := fs.Open(tablePath)
		if _, err := sstable.Open(f); !errors.Is(err, errInjected) || errors.Is(err, sstable.ErrCorrupt) {
			t.Fatalf("Open with read %d failing = %v, want the injected fault", n, err)
		}
	}

	fs.SetFault(nil)
	r := open(t, fs)
	fs.SetFault(vfstest.FailNth(vfstest.OpRead, 1, errInjected))
	if _, ok, err := r.Get(entries[0].Key); ok || !errors.Is(err, errInjected) {
		t.Fatalf("Get with a failing read = %v, %v; want the injected fault", ok, err)
	}
	fs.SetFault(vfstest.FailNth(vfstest.OpRead, 2, errInjected))
	it := r.NewIterator()
	for it.First(); it.Valid(); it.Next() {
	}
	if err := it.Close(); !errors.Is(err, errInjected) {
		t.Fatalf("iterator with its second block read failing = %v, want the injected fault", err)
	}
}
