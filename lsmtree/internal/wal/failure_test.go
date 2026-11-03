package wal_test

import (
	"errors"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/wal"
)

var errInjected = errors.New("injected fault")

// fn's error stops Replay and comes back unchanged; the report covers the
// records fn accepted before it.
func TestReplayReturnsCallbackError(t *testing.T) {
	es := manyEntries(5)
	data, ends := encode(t, es)
	_, f := fileWith(t, data)
	calls := 0
	rep, err := wal.Replay(f, func(keys.Entry) error {
		calls++
		if calls == 3 {
			return errInjected
		}
		return nil
	})
	if err != errInjected {
		t.Fatalf("got %v, want the callback's error unchanged", err)
	}
	if calls != 3 {
		t.Fatalf("fn called %d times after failing, want it to stop at 3", calls)
	}
	want := wal.Report{Records: 2, GoodBytes: ends[1]}
	if rep != want {
		t.Fatalf("report %+v, want %+v", rep, want)
	}
}

// A read error is an I/O failure, not a torn tail: Replay returns it
// instead of quietly dropping the rest of the log.
func TestReplayReturnsReadError(t *testing.T) {
	data, _ := encode(t, manyEntries(3))
	mfs, f := fileWith(t, data)
	mfs.SetFault(vfstest.FailNth(vfstest.OpRead, 1, errInjected))
	got, rep, err := replayAll(f)
	if !errors.Is(err, errInjected) {
		t.Fatalf("got %v, want the injected error", err)
	}
	if len(got) != 0 || rep != (wal.Report{}) {
		t.Fatalf("replayed %d entries, report %+v; want none", len(got), rep)
	}
}

// A log big enough to need several reads, failing on a read after some
// records were delivered: the report covers what was delivered.
func TestReplayReadErrorMidway(t *testing.T) {
	es := manyEntries(2000) // well over bufio's 4 KiB buffer
	data, _ := encode(t, es)
	mfs, f := fileWith(t, data)
	mfs.SetFault(vfstest.FailNth(vfstest.OpRead, 3, errInjected))
	got, rep, err := replayAll(f)
	if !errors.Is(err, errInjected) {
		t.Fatalf("got %v, want the injected error", err)
	}
	if rep.Records == 0 || rep.Records != len(got) {
		t.Fatalf("report %+v after %d entries; want the records delivered before the failure", rep, len(got))
	}
	checkPrefix(t, got, es)
}

// Replay starts by asking the file's size; if that fails it reads nothing
// and returns the error.
func TestReplayReturnsSizeError(t *testing.T) {
	data, _ := encode(t, manyEntries(1))
	mfs, f := fileWith(t, data)
	mfs.SetFault(vfstest.FailNth(vfstest.OpStat, 1, errInjected))
	got, rep, err := replayAll(f)
	if !errors.Is(err, errInjected) {
		t.Fatalf("got %v, want the injected Size error", err)
	}
	if len(got) != 0 || rep != (wal.Report{}) {
		t.Fatalf("replayed %d entries, report %+v; want none", len(got), rep)
	}
}

// After a failed Write or Sync the writer refuses all further work: the
// file may end in a partial record, and anything after it would be lost.
func TestWriterFailureIsSticky(t *testing.T) {
	cases := []struct {
		name  string
		op    vfstest.Op
		fails func(w *wal.Writer) error
	}{
		{"write", vfstest.OpWrite, func(w *wal.Writer) error { return w.Append(itertest.Put("b", 2, "2")) }},
		{"sync", vfstest.OpSync, func(w *wal.Writer) error { return w.Sync() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mfs := vfstest.NewMemFS()
			f, err := mfs.Create(logName)
			if err != nil {
				t.Fatal(err)
			}
			w := wal.NewWriter(f)
			if err := w.Append(itertest.Put("a", 1, "1")); err != nil {
				t.Fatal(err)
			}
			size := w.Size()
			mfs.SetFault(vfstest.FailNth(tc.op, 1, errInjected))
			if err := tc.fails(w); !errors.Is(err, errInjected) {
				t.Fatalf("got %v, want the injected error", err)
			}
			mfs.SetFault(nil)
			if err := w.Append(itertest.Put("c", 3, "3")); !errors.Is(err, wal.ErrFailed) || !errors.Is(err, errInjected) {
				t.Fatalf("append after failure: got %v, want ErrFailed wrapping the cause", err)
			}
			if err := w.Sync(); !errors.Is(err, wal.ErrFailed) {
				t.Fatalf("sync after failure: got %v, want ErrFailed", err)
			}
			if w.Size() != size {
				t.Fatalf("Size() = %d after failure, want %d", w.Size(), size)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("close after failure: %v", err)
			}
		})
	}
}

func TestWriterCloseError(t *testing.T) {
	f, err := vfstest.NewMemFS().Create(logName)
	if err != nil {
		t.Fatal(err)
	}
	w := wal.NewWriter(f)
	f.Close() // the underlying file's Close now fails
	if err := w.Close(); err == nil {
		t.Fatal("close succeeded though the file's Close failed")
	}
}
