//go:build unix

package osfs_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/osfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

func TestOSFSContract(t *testing.T) {
	vfstest.FSContract(t, func(t *testing.T) (vfs.FS, string) {
		return osfs.FS{}, t.TempDir()
	})
}

func TestSyncDirOnMissingDir(t *testing.T) {
	err := osfs.FS{}.SyncDir(filepath.Join(t.TempDir(), "missing"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SyncDir(missing) = %v, want fs.ErrNotExist", err)
	}
}

func TestLockInMissingDir(t *testing.T) {
	_, err := osfs.FS{}.Lock(filepath.Join(t.TempDir(), "missing", "LOCK"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lock in missing dir = %v, want fs.ErrNotExist", err)
	}
}

func TestListSkipsNonRegularFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	names, err := osfs.FS{}.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "real" {
		t.Fatalf("List = %v, want [real] (symlinks skipped)", names)
	}
}
