package countfs_test

import (
	"io"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/countfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

func newFS(t *testing.T) (*countfs.FS, string) {
	t.Helper()
	m := vfstest.NewMemFS()
	if err := m.MkdirAll("/db"); err != nil {
		t.Fatal(err)
	}
	return countfs.New(m), "/db"
}

// The wrapper must still behave exactly like the FS it wraps.
func TestContract(t *testing.T) {
	vfstest.FSContract(t, func(t *testing.T) (vfs.FS, string) { return newFS(t) })
}

func TestKindOf(t *testing.T) {
	cases := map[string]countfs.Kind{
		"/db/000001.log":   countfs.KindWAL,
		"/db/000002.sst":   countfs.KindTable,
		"/db/MANIFEST":     countfs.KindManifest,
		"/db/MANIFEST.tmp": countfs.KindManifest,
		"/db/LOCK":         countfs.KindOther,
		"/db/notes.log":    countfs.KindOther, // not a numbered WAL
		"1234567.sst":      countfs.KindTable,
	}
	for name, want := range cases {
		if got := countfs.KindOf(name); got != want {
			t.Errorf("KindOf(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestCountsByKind(t *testing.T) {
	c, dir := newFS(t)
	w, _ := c.Create(dir + "/000001.sst")
	w.Write([]byte("hello"))
	w.Write([]byte("!!"))
	w.Sync()
	w.Close()
	c.SyncDir(dir)

	r, _ := c.Open(dir + "/000001.sst")
	r.ReadAt(make([]byte, 3), 1)
	io.ReadAll(r)
	r.Close()

	counts, syncDirs := c.Snapshot()
	got := counts[countfs.KindTable]
	want := countfs.Counts{BytesWritten: 7, Writes: 2, Syncs: 1, BytesRead: 3 + 7, Reads: 3}
	// io.ReadAll makes two Read calls: one with data, one returning EOF.
	if got != want || syncDirs != 1 {
		t.Fatalf("counts = %+v, syncDirs = %d; want %+v, 1", got, syncDirs, want)
	}
	if len(counts) != 1 {
		t.Fatalf("I/O on one .sst file was counted under %d kinds: %v", len(counts), counts)
	}

	c.Reset()
	if counts, syncDirs := c.Snapshot(); len(counts) != 0 || syncDirs != 0 {
		t.Fatalf("after Reset: %v, %d", counts, syncDirs)
	}
}
