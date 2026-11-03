package iterator_test

import (
	"errors"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

var errBlock = errors.New("corrupt block")

func TestMergingChildErrorMidStream(t *testing.T) {
	bad := newScript(2, errBlock, itertest.Put("a", 1, ""), itertest.Put("c", 3, ""), itertest.Put("e", 5, ""))
	good := newScript(-1, nil, itertest.Put("b", 2, ""), itertest.Put("d", 4, ""), itertest.Put("f", 6, ""))
	it := iterator.NewMerging(good, bad)
	it.First()
	got := drainAll(it)
	// a, b, c are yielded; moving bad past c fails, which stops the merge.
	if len(got) != 3 {
		t.Fatalf("yielded %d entries before the error, want 3: %v", len(got), got)
	}
	if !errors.Is(it.Err(), errBlock) || it.Valid() {
		t.Fatalf("after the error: Err = %v, Valid = %v; want errBlock, false", it.Err(), it.Valid())
	}
	// The error is sticky: repositioning does not hide it.
	it.First()
	if it.Valid() || !errors.Is(it.Err(), errBlock) {
		t.Fatalf("after First: Err = %v, Valid = %v; want errBlock, false", it.Err(), it.Valid())
	}
	if err := it.Close(); !errors.Is(err, errBlock) {
		t.Fatalf("Close = %v, want errBlock", err)
	}
	if !good.closed || !bad.closed {
		t.Fatal("Close did not close every child")
	}
}

func TestMergingChildErrorOnPositioning(t *testing.T) {
	for _, seek := range []bool{false, true} {
		bad := newScript(0, errBlock, itertest.Put("a", 1, ""))
		it := iterator.NewMerging(iterator.NewSlice([]keys.Entry{itertest.Put("b", 2, "")}), bad)
		if seek {
			it.SeekGE(nil)
		} else {
			it.First()
		}
		if it.Valid() || !errors.Is(it.Err(), errBlock) {
			t.Errorf("seek=%v: Valid = %v, Err = %v; want false, errBlock", seek, it.Valid(), it.Err())
		}
		if err := it.Close(); !errors.Is(err, errBlock) {
			t.Errorf("seek=%v: Close = %v, want errBlock", seek, err)
		}
	}
}

// Close reports a child's Close error even if the merge never saw it.
func TestMergingCloseReturnsFirstChildError(t *testing.T) {
	errLate := errors.New("late")
	first := newScript(-1, nil)
	second := newScript(0, errBlock)
	third := newScript(0, errLate)
	second.First() // put both in their failed state without the merge
	third.First()
	it := iterator.NewMerging(first, second, third)
	if err := it.Close(); !errors.Is(err, errBlock) {
		t.Fatalf("Close = %v, want errBlock (the first child error)", err)
	}
	if !first.closed || !second.closed || !third.closed {
		t.Fatal("Close stopped at the first error")
	}
}

func TestDedupPropagatesError(t *testing.T) {
	// Fails while skipping the older versions of a.
	bad := newScript(2, errBlock, itertest.Put("a", 3, ""), itertest.Put("a", 2, ""), itertest.Put("a", 1, ""), itertest.Put("b", 4, ""))
	it := iterator.NewDedup(bad, true)
	it.First()
	if got := drainAll(it); len(got) != 1 {
		t.Fatalf("yielded %v, want only a@3", got)
	}
	if !errors.Is(it.Err(), errBlock) {
		t.Fatalf("Err = %v, want errBlock", it.Err())
	}
	if err := it.Close(); !errors.Is(err, errBlock) {
		t.Fatalf("Close = %v, want errBlock", err)
	}
}
