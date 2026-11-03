// Package countfs wraps a vfs.FS and counts the I/O that passes through
// it, by file kind. The experiments use it to measure write and read
// amplification in bytes and calls rather than wall-clock time, so the
// results are the same on every machine.
package countfs

import (
	"io"
	"maps"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// Kind groups files by what they hold, from the file name.
type Kind string

const (
	KindWAL      Kind = "wal"      // NNNNNN.log
	KindTable    Kind = "table"    // NNNNNN.sst
	KindManifest Kind = "manifest" // MANIFEST and MANIFEST.tmp
	KindOther    Kind = "other"
)

// KindOf classifies a file by its name, using the names meta and
// manifest give the store's files.
func KindOf(name string) Kind {
	base := filepath.Base(name)
	if _, kind, ok := meta.ParseFileName(base); ok {
		switch kind {
		case meta.KindLog:
			return KindWAL
		case meta.KindTable:
			return KindTable
		}
	}
	if strings.HasPrefix(base, manifest.FileName) {
		return KindManifest
	}
	return KindOther
}

// Counts is the I/O done on one kind of file.
type Counts struct {
	BytesWritten int64
	BytesRead    int64
	Writes       int64 // Write calls
	Reads        int64 // Read and ReadAt calls
	Syncs        int64 // File.Sync calls
}

// FS is a vfs.FS that counts I/O. It is safe for concurrent use.
type FS struct {
	inner vfs.FS

	mu       sync.Mutex
	counts   map[Kind]Counts
	syncDirs int64
}

var _ vfs.FS = (*FS)(nil)

// New wraps inner.
func New(inner vfs.FS) *FS {
	return &FS{inner: inner, counts: map[Kind]Counts{}}
}

// Snapshot returns the counts so far, by kind, and the number of SyncDir
// calls.
func (c *FS) Snapshot() (map[Kind]Counts, int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return maps.Clone(c.counts), c.syncDirs
}

// Reset sets every count to zero, e.g. after loading data and before
// measuring reads.
func (c *FS) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = map[Kind]Counts{}
	c.syncDirs = 0
}

func (c *FS) add(kind Kind, f func(*Counts)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := c.counts[kind]
	f(&n)
	c.counts[kind] = n
}

// Create implements vfs.FS.
func (c *FS) Create(name string) (vfs.WritableFile, error) {
	f, err := c.inner.Create(name)
	if err != nil {
		return nil, err
	}
	return &writable{WritableFile: f, fs: c, kind: KindOf(name)}, nil
}

// Open implements vfs.FS.
func (c *FS) Open(name string) (vfs.ReadableFile, error) {
	f, err := c.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &readable{ReadableFile: f, fs: c, kind: KindOf(name)}, nil
}

// SyncDir implements vfs.FS.
func (c *FS) SyncDir(dir string) error {
	c.mu.Lock()
	c.syncDirs++
	c.mu.Unlock()
	return c.inner.SyncDir(dir)
}

// Rename implements vfs.FS.
func (c *FS) Rename(oldname, newname string) error { return c.inner.Rename(oldname, newname) }

// Remove implements vfs.FS.
func (c *FS) Remove(name string) error { return c.inner.Remove(name) }

// List implements vfs.FS.
func (c *FS) List(dir string) ([]string, error) { return c.inner.List(dir) }

// MkdirAll implements vfs.FS.
func (c *FS) MkdirAll(dir string) error { return c.inner.MkdirAll(dir) }

// Lock implements vfs.FS.
func (c *FS) Lock(name string) (io.Closer, error) { return c.inner.Lock(name) }

type writable struct {
	vfs.WritableFile
	fs   *FS
	kind Kind
}

func (w *writable) Write(p []byte) (int, error) {
	n, err := w.WritableFile.Write(p)
	w.fs.add(w.kind, func(c *Counts) { c.Writes++; c.BytesWritten += int64(n) })
	return n, err
}

func (w *writable) Sync() error {
	w.fs.add(w.kind, func(c *Counts) { c.Syncs++ })
	return w.WritableFile.Sync()
}

type readable struct {
	vfs.ReadableFile
	fs   *FS
	kind Kind
}

func (r *readable) Read(p []byte) (int, error) {
	n, err := r.ReadableFile.Read(p)
	r.fs.add(r.kind, func(c *Counts) { c.Reads++; c.BytesRead += int64(n) })
	return n, err
}

func (r *readable) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.ReadableFile.ReadAt(p, off)
	r.fs.add(r.kind, func(c *Counts) { c.Reads++; c.BytesRead += int64(n) })
	return n, err
}
