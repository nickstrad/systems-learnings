// Package vfs is the filesystem interface the store is written against.
//
// The store never calls package os directly. It reaches the disk only
// through FS, so tests can swap in vfstest.MemFS. MemFS can inject a
// failure into any operation and simulate a crash that loses everything
// not yet made durable. The real adapter is package osfs.
//
// # Durability model
//
// Two separate things must be synced, as on a real POSIX filesystem:
//
//   - A file's contents are durable only after File.Sync.
//   - A directory entry (a Create, Rename, or Remove) is durable only after
//     SyncDir on its parent directory.
//
// A file that was synced but whose directory entry was not can vanish in a
// crash. The store's write ordering (write, Sync, then rename or SyncDir)
// exists because of this rule.
//
// Paths are full paths. Implementations clean them with filepath.Clean.
package vfs

import (
	"errors"
	"io"
)

// ErrLocked is returned by FS.Lock when another holder has the lock.
var ErrLocked = errors.New("lock is held by another user")

// FS is the filesystem the store uses. Every method that names a missing
// file or directory returns an error for which
// errors.Is(err, fs.ErrNotExist) is true.
type FS interface {
	// Create creates name for writing, replacing any existing file of that
	// name. Writes append.
	//
	//	pre:  the parent directory exists
	//	post: err == nil ⇒ name exists with size 0
	Create(name string) (WritableFile, error)

	// Open opens an existing file for reading. The file keeps reading the
	// same data even if name is later removed or replaced, as POSIX file
	// handles do.
	Open(name string) (ReadableFile, error)

	// Rename atomically moves oldname to newname, replacing newname if it
	// exists. Readers see either the old or the new state, never both.
	Rename(oldname, newname string) error

	// Remove deletes the file name.
	Remove(name string) error

	// List returns the base names of the regular files directly inside
	// dir, sorted. Subdirectories are left out.
	List(dir string) ([]string, error)

	// MkdirAll creates dir and any missing parents. It succeeds if dir
	// already exists.
	MkdirAll(dir string) error

	// SyncDir makes the creates, renames, and removes in dir durable.
	SyncDir(dir string) error

	// Lock takes an exclusive lock on the file name, creating the file if
	// needed (so List shows it). It fails with ErrLocked if the lock is
	// already held, even by this process. Close the returned Closer to
	// release the lock.
	Lock(name string) (io.Closer, error)
}

// WritableFile is a file from Create: the WAL and SSTable writers only
// append and sync. After Close, every method returns an error.
type WritableFile interface {
	io.Writer
	io.Closer

	// Sync makes the data written so far durable. It does not make the
	// file's directory entry durable; see FS.SyncDir.
	Sync() error
}

// ReadableFile is a file from Open: the WAL reader reads it in order, and
// the SSTable reader reads blocks at offsets. After Close, every method
// returns an error.
type ReadableFile interface {
	io.Reader
	io.ReaderAt
	io.Closer

	// Size returns the current length of the file in bytes.
	Size() (int64, error)
}
