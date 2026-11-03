// Package iterator defines how the store walks sorted entries, and
// combines several sorted sources into one.
//
// The memtable, each SSTable, and the merging iterator all implement
// Iterator, and all pass the same contract suite in package itertest.
package iterator

import "github.com/nickstrad/systems-learnings/lsmtree/internal/keys"

// Iterator walks entries in keys.Compare order. A new iterator is not
// positioned; call First or SeekGE before reading.
//
// Iterators are not safe for concurrent use.
type Iterator interface {
	// First moves to the first entry.
	First()

	// SeekGE moves to the first entry whose user key is >= key. If that
	// key has several versions, it lands on the newest.
	SeekGE(key []byte)

	// Valid reports whether the iterator is on an entry. It is false
	// before positioning, after the last entry, and after an error.
	Valid() bool

	// Entry returns the current entry.
	//
	//	pre:  Valid()
	//	post: the returned slices are valid only until the next move;
	//	      call Entry.Clone to keep them
	Entry() keys.Entry

	// Next moves to the next entry.
	//
	//	pre: Valid()
	Next()

	// Err returns the first error the iterator hit, such as a corrupt
	// block. An error also makes Valid false. Errors are sticky:
	// repositioning with First or SeekGE does not clear one.
	Err() error

	// Close releases resources. It returns Err().
	Close() error
}
