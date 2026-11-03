package sstable_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/sstable"
)

// TestIteratorContract runs the shared iterator suite against SSTable
// iterators, with blocks small enough that most cases cross many blocks.
//
// A table holds at least one entry and one version per key, so inputs
// with no entries or with two versions of a key are skipped.
func TestIteratorContract(t *testing.T) {
	for _, blockSize := range []int{1, 64, sstable.DefaultBlockSize} {
		t.Run(blockSizeName(blockSize), func(t *testing.T) {
			itertest.Contract(t, func(t *testing.T, entries []keys.Entry) iterator.Iterator {
				if len(entries) == 0 {
					t.Skip("a table holds at least one entry")
				}
				if len(itertest.NewestOnly(entries, false)) != len(entries) {
					t.Skip("a table holds one version per key")
				}
				r, _ := build(t, sstable.WriterOptions{BlockSize: blockSize, BloomBitsPerKey: 10}, entries)
				return r.NewIterator()
			})
		})
	}
}

func blockSizeName(n int) string {
	switch n {
	case 1:
		return "OneEntryPerBlock"
	case 64:
		return "SmallBlocks"
	}
	return "DefaultBlocks"
}
