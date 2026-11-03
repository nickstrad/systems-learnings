//go:build unix

// Package osfs is the real vfs.FS, backed by the operating system.
//
// It is a thin adapter: each method maps to one or two calls in package
// os. Errors are returned as the *fs.PathError values os produces, so
// errors.Is(err, fs.ErrNotExist) works. Callers in the store wrap them
// with context.
package osfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// FS implements vfs.FS on the local disk.
type FS struct{}

var _ vfs.FS = FS{}

// Create implements vfs.FS.
func (FS) Create(name string) (vfs.WritableFile, error) {
	f, err := os.OpenFile(filepath.Clean(name), os.O_WRONLY|os.O_CREATE|os.O_TRUNC|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	return f, nil
}

// Open implements vfs.FS.
func (FS) Open(name string) (vfs.ReadableFile, error) {
	f, err := os.Open(filepath.Clean(name))
	if err != nil {
		return nil, err
	}
	return file{f}, nil
}

// Rename implements vfs.FS.
func (FS) Rename(oldname, newname string) error {
	return os.Rename(filepath.Clean(oldname), filepath.Clean(newname))
}

// Remove implements vfs.FS.
func (FS) Remove(name string) error {
	return os.Remove(filepath.Clean(name))
}

// List implements vfs.FS.
func (FS) List(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() {
			names = append(names, e.Name())
		}
	}
	slices.Sort(names) // ReadDir sorts already; this states the contract
	return names, nil
}

// MkdirAll implements vfs.FS.
func (FS) MkdirAll(dir string) error {
	return os.MkdirAll(filepath.Clean(dir), 0o755)
}

// SyncDir implements vfs.FS by fsyncing the directory itself, which is
// what makes new, renamed, and removed entries survive a crash.
func (FS) SyncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return err
	}
	return d.Close()
}

// Lock implements vfs.FS with flock(2). The lock belongs to the open file,
// so a second Lock call fails even from the same process, and the OS
// releases the lock if the process dies.
func (FS) Lock(name string) (io.Closer, error) {
	f, err := os.OpenFile(filepath.Clean(name), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, &os.PathError{Op: "lock", Path: name, Err: vfs.ErrLocked}
		}
		return nil, &os.PathError{Op: "lock", Path: name, Err: err}
	}
	return f, nil // closing the file releases the flock
}

// file adds Size to *os.File, for vfs.ReadableFile.
type file struct{ *os.File }

func (f file) Size() (int64, error) {
	info, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
