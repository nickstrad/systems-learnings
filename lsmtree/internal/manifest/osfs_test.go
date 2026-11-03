package manifest_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/osfs"
)

// TestOSFS writes and reads the manifest on the real filesystem, and
// checks a byte flipped on disk is caught.
func TestOSFS(t *testing.T) {
	d := t.TempDir()
	var fsys osfs.FS
	if _, err := manifest.Read(fsys, d); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Read of a new directory = %v, want fs.ErrNotExist", err)
	}
	if err := manifest.Write(fsys, d, oldVersion); err != nil {
		t.Fatal(err)
	}
	if err := manifest.Write(fsys, d, newVersion); err != nil {
		t.Fatal(err)
	}
	got, err := manifest.Read(fsys, d)
	if err != nil || !sameVersion(got, newVersion) {
		t.Fatalf("Read = %+v, %v; want the second version", got, err)
	}
	if _, err := os.Stat(filepath.Join(d, manifest.TmpName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("%s left behind: %v", manifest.TmpName, err)
	}

	path := filepath.Join(d, manifest.FileName)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/2] ^= 0x20
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Read(fsys, d); !errors.Is(err, manifest.ErrCorrupt) {
		t.Fatalf("Read of a damaged file = %v, want ErrCorrupt", err)
	}
}
