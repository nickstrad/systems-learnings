package manifest_test

import (
	"errors"
	"io/fs"
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/manifest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

const dir = "/db"

var (
	oldVersion  = meta.Version{NextFileNum: 3, LastSeq: 10, LogNum: 2, Levels: [][]meta.FileMeta{{file(1, "a", "m")}}}
	newVersion  = manyLevels()
	errInjected = errors.New("injected fault")
)

func newFS(t *testing.T) *vfstest.MemFS {
	t.Helper()
	m := vfstest.NewMemFS()
	if err := m.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	return m
}

func mustRead(t *testing.T, m *vfstest.MemFS) meta.Version {
	t.Helper()
	v, err := manifest.Read(m, dir)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return v
}

func TestWriteRead(t *testing.T) {
	m := newFS(t)
	if err := manifest.Write(m, dir, oldVersion); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, m); !sameVersion(got, oldVersion) {
		t.Fatalf("Read = %+v, want %+v", got, oldVersion)
	}
	if err := manifest.Write(m, dir, newVersion); err != nil {
		t.Fatal(err)
	}
	if got := mustRead(t, m); !sameVersion(got, newVersion) {
		t.Fatal("Read after a second Write did not return the new version")
	}
	names, err := m.List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != manifest.FileName {
		t.Fatalf("directory holds %v, want only %s", names, manifest.FileName)
	}
	m.Crash()
	if got := mustRead(t, m); !sameVersion(got, newVersion) {
		t.Fatal("a crash after a successful Write lost the new version")
	}
}

func TestReadMissing(t *testing.T) {
	tests := []struct {
		name string
		dir  string
	}{
		{"empty directory: a new store", dir},
		{"missing directory", "/nowhere"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := manifest.Read(newFS(t), tt.dir)
			if !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("Read = %v, want fs.ErrNotExist", err)
			}
		})
	}
}

func TestReadCorrupt(t *testing.T) {
	m := newFS(t)
	w, err := m.Create(dir + "/" + manifest.FileName)
	if err != nil {
		t.Fatal(err)
	}
	b := manifest.Encode(oldVersion)
	b[5] ^= 0xff
	if _, err := w.Write(b); err != nil {
		t.Fatal(err)
	}
	w.Close()
	if _, err := manifest.Read(m, dir); !errors.Is(err, manifest.ErrCorrupt) {
		t.Fatalf("Read = %v, want ErrCorrupt", err)
	}
}

// TestWriteFailureThenCrash fails each filesystem step of Write in turn,
// then crashes. Whatever failed, the store must come back with the old
// manifest or the new one: never an error, never a mix. Steps before the
// rename must leave the old one visible even without a crash.
func TestWriteFailureThenCrash(t *testing.T) {
	tests := []struct {
		op           vfstest.Op
		oldStillLive bool // the running program still reads the old version
	}{
		{vfstest.OpCreate, true},
		{vfstest.OpWrite, true},
		{vfstest.OpSync, true},
		{vfstest.OpRename, true},
		{vfstest.OpSyncDir, false},
	}
	for _, firstWrite := range []bool{false, true} {
		for _, tt := range tests {
			name := tt.op.String()
			if firstWrite {
				name = "first manifest/" + name
			}
			t.Run(name, func(t *testing.T) {
				m := newFS(t)
				if !firstWrite {
					if err := manifest.Write(m, dir, oldVersion); err != nil {
						t.Fatal(err)
					}
				}
				m.SetFault(vfstest.FailNth(tt.op, 1, errInjected))
				err := manifest.Write(m, dir, newVersion)
				if !errors.Is(err, errInjected) {
					t.Fatalf("Write with %s failing = %v, want the injected fault", tt.op, err)
				}
				m.SetFault(nil)
				if tt.oldStillLive {
					checkOldOrNew(t, m, firstWrite, false)
				}
				m.Crash()
				checkOldOrNew(t, m, firstWrite, true)
			})
		}
	}
}

// TestWriteFailEveryStep fails the nth filesystem call of Write for every
// n, whatever the call is, so a step added to Write later is covered too.
func TestWriteFailEveryStep(t *testing.T) {
	steps := 0
	m := newFS(t)
	m.SetFault(func(vfstest.Op, string) error { steps++; return nil })
	if err := manifest.Write(m, dir, newVersion); err != nil {
		t.Fatal(err)
	}
	if steps < 5 {
		t.Fatalf("Write made %d filesystem calls, want at least create, write, sync, rename, syncdir", steps)
	}
	for n := 1; n <= steps; n++ {
		m := newFS(t)
		if err := manifest.Write(m, dir, oldVersion); err != nil {
			t.Fatal(err)
		}
		calls := 0
		m.SetFault(func(op vfstest.Op, _ string) error {
			if calls++; calls == n {
				return errInjected
			}
			return nil
		})
		if err := manifest.Write(m, dir, newVersion); !errors.Is(err, errInjected) {
			t.Fatalf("step %d: Write = %v, want the injected fault", n, err)
		}
		m.SetFault(nil)
		m.Crash()
		checkOldOrNew(t, m, false, true)
	}
}

// TestWriteCrashTorn crashes with torn writes and a random prefix of the
// pending directory changes, after a Write that failed at a random step
// or succeeded.
func TestWriteCrashTorn(t *testing.T) {
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 0))
		m := newFS(t)
		if err := manifest.Write(m, dir, oldVersion); err != nil {
			t.Fatal(err)
		}
		failAt, calls := rng.IntN(8), 0 // past the last step means no failure
		m.SetFault(func(vfstest.Op, string) error {
			if calls++; calls == failAt {
				return errInjected
			}
			return nil
		})
		err := manifest.Write(m, dir, newVersion)
		m.SetFault(nil)
		m.CrashTorn(rng)
		got, rerr := manifest.Read(m, dir)
		if rerr != nil {
			t.Fatalf("seed %d: Read after torn crash: %v", seed, rerr)
		}
		if !sameVersion(got, oldVersion) && !sameVersion(got, newVersion) {
			t.Fatalf("seed %d: Read after torn crash = %+v, want old or new", seed, got)
		}
		if err == nil && !sameVersion(got, newVersion) {
			t.Fatalf("seed %d: Write succeeded but the crash lost it", seed)
		}
	}
}

// checkOldOrNew reads the manifest and checks it is the old version or
// the new one (newOK). With no old manifest, "old" means none at all.
func checkOldOrNew(t *testing.T, m *vfstest.MemFS, noOld, newOK bool) {
	t.Helper()
	got, err := manifest.Read(m, dir)
	switch {
	case err == nil && newOK && sameVersion(got, newVersion):
	case noOld && errors.Is(err, fs.ErrNotExist):
	case !noOld && err == nil && sameVersion(got, oldVersion):
	default:
		t.Fatalf("Read = %+v, %v; want the old manifest%s", got, err, map[bool]string{true: " or the new one"}[newOK])
	}
}

func TestReadFails(t *testing.T) {
	m := newFS(t)
	if err := manifest.Write(m, dir, oldVersion); err != nil {
		t.Fatal(err)
	}
	m.SetFault(vfstest.FailNth(vfstest.OpRead, 1, errInjected))
	if _, err := manifest.Read(m, dir); !errors.Is(err, errInjected) {
		t.Fatalf("Read with a failing read = %v, want the injected fault", err)
	}
}
