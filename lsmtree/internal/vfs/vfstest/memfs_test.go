package vfstest_test

import (
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

func TestMemFSContract(t *testing.T) {
	vfstest.FSContract(t, func(t *testing.T) (vfs.FS, string) {
		return newDB(t), "/db"
	})
}

// newDB returns a MemFS with a /db directory.
func newDB(t *testing.T) *vfstest.MemFS {
	t.Helper()
	m := vfstest.NewMemFS()
	if err := m.MkdirAll("/db"); err != nil {
		t.Fatal(err)
	}
	return m
}

// write creates name with data, optionally syncing the file and its
// directory.
func write(t *testing.T, m *vfstest.MemFS, name, data string, syncFile, syncDir bool) vfs.WritableFile {
	t.Helper()
	f, err := m.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
	if syncFile {
		if err := f.Sync(); err != nil {
			t.Fatal(err)
		}
	}
	if syncDir {
		if err := m.SyncDir("/db"); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

// contents returns a file's data, or "<missing>".
func contents(t *testing.T, m *vfstest.MemFS, name string) string {
	t.Helper()
	f, err := m.Open(name)
	if errors.Is(err, fs.ErrNotExist) {
		return "<missing>"
	}
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCrashKeepsOnlyDurableState walks the durability model in package
// vfs: file data needs Sync, and directory entries need SyncDir.
func TestCrashKeepsOnlyDurableState(t *testing.T) {
	cases := []struct {
		name              string
		syncFile, syncDir bool
		want              string
	}{
		{"neither synced: file gone", false, false, "<missing>"},
		{"data synced, entry not: file gone", true, false, "<missing>"},
		{"entry synced, data not: empty file", false, true, ""},
		{"both synced: data kept", true, true, "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := newDB(t)
			write(t, m, "/db/a", "abc", c.syncFile, c.syncDir)
			m.Crash()
			if got := contents(t, m, "/db/a"); got != c.want {
				t.Fatalf("after crash: %q, want %q", got, c.want)
			}
		})
	}
}

func TestCrashDropsUnsyncedTail(t *testing.T) {
	m := newDB(t)
	f := write(t, m, "/db/a", "synced", true, true)
	f.Write([]byte("+lost"))
	m.Crash()
	if got := contents(t, m, "/db/a"); got != "synced" {
		t.Fatalf("after crash: %q, want %q", got, "synced")
	}
}

func TestCrashUndoesUnsyncedRenameAndRemove(t *testing.T) {
	m := newDB(t)
	write(t, m, "/db/a", "A", true, true)
	write(t, m, "/db/b", "B", true, true)
	if err := m.Rename("/db/a", "/db/c"); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove("/db/b"); err != nil {
		t.Fatal(err)
	}
	m.Crash()
	got := []string{contents(t, m, "/db/a"), contents(t, m, "/db/b"), contents(t, m, "/db/c")}
	if want := []string{"A", "B", "<missing>"}; !slices.Equal(got, want) {
		t.Fatalf("after crash a, b, c = %q, want %q", got, want)
	}
}

func TestSyncedRenameSurvivesCrash(t *testing.T) {
	m := newDB(t)
	write(t, m, "/db/tmp", "new manifest", true, false)
	write(t, m, "/db/cur", "old manifest", true, true) // SyncDir also makes tmp durable
	if err := m.Rename("/db/tmp", "/db/cur"); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncDir("/db"); err != nil {
		t.Fatal(err)
	}
	m.Crash()
	if got := contents(t, m, "/db/cur"); got != "new manifest" {
		t.Fatalf("after crash cur = %q, want %q", got, "new manifest")
	}
	if got := contents(t, m, "/db/tmp"); got != "<missing>" {
		t.Fatalf("after crash tmp = %q, want it gone", got)
	}
}

// TestSyncDirIsOrdered shows the ordered-journal simplification: syncing
// one directory also makes earlier changes in other directories durable.
func TestSyncDirIsOrdered(t *testing.T) {
	m := newDB(t)
	if err := m.MkdirAll("/other"); err != nil {
		t.Fatal(err)
	}
	f, _ := m.Create("/other/x")
	f.Sync()
	write(t, m, "/db/a", "A", true, true) // SyncDir("/db") only
	m.Crash()
	if got := contents(t, m, "/other/x"); got != "" {
		t.Fatalf("/other/x = %q; an earlier change should be durable after a later SyncDir", got)
	}
}

func TestCrashInvalidatesHandlesAndLocks(t *testing.T) {
	m := newDB(t)
	f := write(t, m, "/db/a", "x", true, true)
	r, err := m.Open("/db/a")
	if err != nil {
		t.Fatal(err)
	}
	lock, err := m.Lock("/db/LOCK")
	if err != nil {
		t.Fatal(err)
	}
	m.Crash()
	if _, err := f.Write([]byte("y")); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("Write on a pre-crash handle = %v, want fs.ErrClosed", err)
	}
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, fs.ErrClosed) {
		t.Errorf("Read on a pre-crash handle = %v, want fs.ErrClosed", err)
	}
	l2, err := m.Lock("/db/LOCK")
	if err != nil {
		t.Fatalf("Lock after crash = %v; the crash should release it", err)
	}
	lock.Close() // the old holder's release must not free the new holder's lock
	if _, err := m.Lock("/db/LOCK"); !errors.Is(err, vfs.ErrLocked) {
		t.Fatalf("stale unlock freed the new lock: %v", err)
	}
	l2.Close()
}

// TestCrashTornKeepsAPrefix checks CrashTorn over many seeds: each file
// keeps between its synced length and its full length, and a pending
// rename is atomic (exactly one of the two names survives).
func TestCrashTornKeepsAPrefix(t *testing.T) {
	sawTorn, sawRenamed, sawNotRenamed := false, false, false
	for seed := range uint64(200) {
		m := newDB(t)
		f := write(t, m, "/db/log", "durable", true, true)
		f.Write([]byte("-maybe-torn"))
		write(t, m, "/db/old", "R", true, true)
		if err := m.Rename("/db/old", "/db/new"); err != nil {
			t.Fatal(err)
		}
		m.CrashTorn(rand.New(rand.NewPCG(seed, 0)))

		log := contents(t, m, "/db/log")
		if !strings.HasPrefix("durable-maybe-torn", log) || len(log) < len("durable") {
			t.Fatalf("seed %d: log = %q, want a prefix of the written data at least as long as the synced part", seed, log)
		}
		sawTorn = sawTorn || (len(log) > len("durable") && len(log) < len("durable-maybe-torn"))

		oldName, newName := contents(t, m, "/db/old"), contents(t, m, "/db/new")
		switch {
		case oldName == "R" && newName == "<missing>":
			sawNotRenamed = true
		case oldName == "<missing>" && newName == "R":
			sawRenamed = true
		default:
			t.Fatalf("seed %d: rename not atomic: old=%q new=%q", seed, oldName, newName)
		}
	}
	if !sawTorn || !sawRenamed || !sawNotRenamed {
		t.Fatalf("200 seeds never produced every outcome: torn=%v renamed=%v notRenamed=%v",
			sawTorn, sawRenamed, sawNotRenamed)
	}
}

func TestFailNthFailsExactlyOneCallWithNoEffect(t *testing.T) {
	m := newDB(t)
	boom := errors.New("injected")
	m.SetFault(vfstest.FailNth(vfstest.OpCreate, 2, boom))
	if _, err := m.Create("/db/a"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := m.Create("/db/b"); !errors.Is(err, boom) {
		t.Fatalf("second create = %v, want the injected error", err)
	}
	if _, err := m.Create("/db/c"); err != nil {
		t.Fatalf("third create: %v", err)
	}
	names, _ := m.List("/db")
	if !slices.Equal(names, []string{"a", "c"}) {
		t.Fatalf("List = %v; the failed create must leave no file", names)
	}
}

func TestFaultsReachFileOperations(t *testing.T) {
	m := newDB(t)
	boom := errors.New("disk full")
	f, _ := m.Create("/db/a")
	f.Write([]byte("kept"))
	m.SetFault(func(op vfstest.Op, _ string) error {
		if op == vfstest.OpWrite || op == vfstest.OpSync {
			return boom
		}
		return nil
	})
	if _, err := f.Write([]byte("lost")); !errors.Is(err, boom) {
		t.Errorf("Write = %v, want injected error", err)
	}
	if err := f.Sync(); !errors.Is(err, boom) {
		t.Errorf("Sync = %v, want injected error", err)
	}
	m.SetFault(nil)
	if got := contents(t, m, "/db/a"); got != "kept" {
		t.Errorf("file holds %q; a failed write must not change it", got)
	}
}

func TestSizeIsAFaultPoint(t *testing.T) {
	m := newDB(t)
	vfstest.WriteFile(t, m, "/db/a", []byte("abc"))
	f, err := m.Open("/db/a")
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("stat failed")
	m.SetFault(vfstest.FailNth(vfstest.OpStat, 1, boom))
	if _, err := f.Size(); !errors.Is(err, boom) {
		t.Fatalf("Size = %v, want the injected error", err)
	}
	if got := string(vfstest.ReadFile(t, m, "/db/a")); got != "abc" {
		t.Fatalf("ReadFile = %q", got)
	}
}
