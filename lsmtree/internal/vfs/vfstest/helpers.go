package vfstest

import (
	"io"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// WriteFile creates name holding data, closing it, and fails the test on
// any error. It neither syncs the file nor its directory.
func WriteFile(t testing.TB, fsys vfs.FS, name string, data []byte) {
	t.Helper()
	f, err := fsys.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// ReadFile returns the contents of name, failing the test on any error.
func ReadFile(t testing.TB, fsys vfs.FS, name string) []byte {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
