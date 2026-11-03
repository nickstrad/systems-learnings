package vfstest

import (
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// FSContract runs the vfs.FS contract against the implementation that
// newFS returns. newFS gives a fresh FS and an existing, empty directory
// inside it. The suite checks behaviour every implementation shares; crash
// behaviour is specific to MemFS and is tested there.
func FSContract(t *testing.T, newFS func(t *testing.T) (vfs.FS, string)) {
	t.Run("WriteThenRead", func(t *testing.T) {
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "a")
		writeFile(t, fsys, name, "hello, ", "world")
		if got := readFile(t, fsys, name); got != "hello, world" {
			t.Fatalf("read %q, want %q", got, "hello, world")
		}
	})

	t.Run("EmptyFile", func(t *testing.T) {
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "empty")
		writeFile(t, fsys, name)
		f := open(t, fsys, name)
		if size, err := f.Size(); err != nil || size != 0 {
			t.Fatalf("Size() = %d, %v; want 0, nil", size, err)
		}
		if _, err := f.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("Read on empty file: %v, want io.EOF", err)
		}
	})

	t.Run("CreateReplacesExisting", func(t *testing.T) {
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "a")
		writeFile(t, fsys, name, "old contents")
		writeFile(t, fsys, name, "new")
		if got := readFile(t, fsys, name); got != "new" {
			t.Fatalf("read %q after re-create, want %q", got, "new")
		}
	})

	t.Run("SizeTracksWrites", func(t *testing.T) {
		// A reader opened before the writes sees the file grow.
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "a")
		w, err := fsys.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		defer w.Close()
		r := open(t, fsys, name)
		want := int64(0)
		for _, n := range []int{0, 3, 4} {
			if _, err := w.Write(make([]byte, n)); err != nil {
				t.Fatal(err)
			}
			want += int64(n)
			if got, err := r.Size(); err != nil || got != want {
				t.Fatalf("Size() = %d, %v after writing %d bytes", got, err, want)
			}
		}
	})

	t.Run("ReadAt", func(t *testing.T) {
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "a")
		writeFile(t, fsys, name, "0123456789")
		f := open(t, fsys, name)
		cases := []struct {
			off     int64
			n       int
			want    string
			wantEOF bool
		}{
			{off: 0, n: 3, want: "012"},
			{off: 7, n: 3, want: "789"},               // exactly to the end
			{off: 8, n: 3, want: "89", wantEOF: true}, // runs past the end
			{off: 10, n: 1, want: "", wantEOF: true},  // at the end
		}
		for _, c := range cases {
			buf := make([]byte, c.n)
			n, err := f.ReadAt(buf, c.off)
			if string(buf[:n]) != c.want || (err == io.EOF) != c.wantEOF || (err != nil && err != io.EOF) {
				t.Errorf("ReadAt(%d bytes at %d) = %q, %v; want %q, EOF=%v",
					c.n, c.off, buf[:n], err, c.want, c.wantEOF)
			}
		}
	})

	t.Run("MissingFiles", func(t *testing.T) {
		fsys, dir := newFS(t)
		missing := filepath.Join(dir, "missing")
		if _, err := fsys.Open(missing); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(missing) = %v, want fs.ErrNotExist", err)
		}
		if err := fsys.Remove(missing); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Remove(missing) = %v, want fs.ErrNotExist", err)
		}
		if err := fsys.Rename(missing, filepath.Join(dir, "b")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Rename(missing) = %v, want fs.ErrNotExist", err)
		}
		if _, err := fsys.Create(filepath.Join(dir, "nodir", "a")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Create in a missing directory = %v, want fs.ErrNotExist", err)
		}
		if _, err := fsys.List(filepath.Join(dir, "nodir")); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("List(missing dir) = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("RenameReplacesTarget", func(t *testing.T) {
		fsys, dir := newFS(t)
		a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
		writeFile(t, fsys, a, "from a")
		writeFile(t, fsys, b, "from b")
		if err := fsys.Rename(a, b); err != nil {
			t.Fatal(err)
		}
		if got := readFile(t, fsys, b); got != "from a" {
			t.Fatalf("after rename, b holds %q, want %q", got, "from a")
		}
		if _, err := fsys.Open(a); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("after rename, Open(a) = %v, want fs.ErrNotExist", err)
		}
	})

	t.Run("RemoveThenList", func(t *testing.T) {
		fsys, dir := newFS(t)
		writeFile(t, fsys, filepath.Join(dir, "a"))
		if err := fsys.Remove(filepath.Join(dir, "a")); err != nil {
			t.Fatal(err)
		}
		if names := list(t, fsys, dir); len(names) != 0 {
			t.Fatalf("List after Remove = %v, want empty", names)
		}
	})

	t.Run("ListIsSortedFilesOnly", func(t *testing.T) {
		fsys, dir := newFS(t)
		for _, n := range []string{"c", "a", "b"} {
			writeFile(t, fsys, filepath.Join(dir, n))
		}
		if err := fsys.MkdirAll(filepath.Join(dir, "sub", "deeper")); err != nil {
			t.Fatal(err)
		}
		writeFile(t, fsys, filepath.Join(dir, "sub", "nested"))
		if got := list(t, fsys, dir); !slices.Equal(got, []string{"a", "b", "c"}) {
			t.Fatalf("List = %v, want [a b c] (sorted, no subdirectories or their files)", got)
		}
	})

	t.Run("MkdirAllIsIdempotent", func(t *testing.T) {
		fsys, dir := newFS(t)
		sub := filepath.Join(dir, "x", "y")
		for range 2 {
			if err := fsys.MkdirAll(sub); err != nil {
				t.Fatal(err)
			}
		}
		writeFile(t, fsys, filepath.Join(sub, "f"))
	})

	t.Run("SyncAndSyncDirSucceed", func(t *testing.T) {
		fsys, dir := newFS(t)
		f, err := fsys.Create(filepath.Join(dir, "a"))
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("x"))
		if err := f.Sync(); err != nil {
			t.Fatalf("Sync: %v", err)
		}
		f.Close()
		if err := fsys.SyncDir(dir); err != nil {
			t.Fatalf("SyncDir: %v", err)
		}
	})

	t.Run("ReaderSurvivesRemove", func(t *testing.T) {
		// Compaction deletes input files that open readers may still use.
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "a")
		writeFile(t, fsys, name, "still here")
		f := open(t, fsys, name)
		if err := fsys.Remove(name); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(f)
		if err != nil || string(got) != "still here" {
			t.Fatalf("read after Remove = %q, %v; want %q", got, err, "still here")
		}
	})

	t.Run("UseAfterClose", func(t *testing.T) {
		fsys, dir := newFS(t)
		f, err := fsys.Create(filepath.Join(dir, "a"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write([]byte("x")); err == nil {
			t.Error("Write after Close succeeded")
		}
		if err := f.Sync(); err == nil {
			t.Error("Sync after Close succeeded")
		}
		if err := f.Close(); err == nil {
			t.Error("second Close succeeded")
		}
	})

	t.Run("LockIsExclusive", func(t *testing.T) {
		fsys, dir := newFS(t)
		name := filepath.Join(dir, "LOCK")
		l, err := fsys.Lock(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fsys.Lock(name); !errors.Is(err, vfs.ErrLocked) {
			t.Fatalf("second Lock = %v, want vfs.ErrLocked", err)
		}
		if err := l.Close(); err != nil {
			t.Fatal(err)
		}
		l2, err := fsys.Lock(name)
		if err != nil {
			t.Fatalf("Lock after release: %v", err)
		}
		l2.Close()
	})

	t.Run("LockFileIsListed", func(t *testing.T) {
		// The store lists its directory to find its files, so it must
		// skip the lock file on every implementation alike.
		fsys, dir := newFS(t)
		l, err := fsys.Lock(filepath.Join(dir, "LOCK"))
		if err != nil {
			t.Fatal(err)
		}
		defer l.Close()
		if got := list(t, fsys, dir); !slices.Equal(got, []string{"LOCK"}) {
			t.Fatalf("List after Lock = %v, want [LOCK]", got)
		}
	})
}

func writeFile(t *testing.T, fsys vfs.FS, name string, chunks ...string) {
	t.Helper()
	f, err := fsys.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range chunks {
		if n, err := f.Write([]byte(c)); err != nil || n != len(c) {
			t.Fatalf("Write(%q) = %d, %v", c, n, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func open(t *testing.T, fsys vfs.FS, name string) vfs.ReadableFile {
	t.Helper()
	f, err := fsys.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func readFile(t *testing.T, fsys vfs.FS, name string) string {
	t.Helper()
	b, err := io.ReadAll(open(t, fsys, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func list(t *testing.T, fsys vfs.FS, dir string) []string {
	t.Helper()
	names, err := fsys.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	return names
}
