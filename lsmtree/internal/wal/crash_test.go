package wal_test

import (
	"math/rand/v2"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/wal"
)

// The prefix property the whole store relies on: after a crash that tears
// the unsynced tail at a random byte, Replay returns a prefix of the
// appended entries, at least as long as the part that was synced.
func TestCrashRecoversSyncedPrefix(t *testing.T) {
	for seed := range uint64(500) {
		rng := rand.New(rand.NewPCG(seed, 0))
		mfs := vfstest.NewMemFS()
		f, err := mfs.Create(logName)
		if err != nil {
			t.Fatal(err)
		}
		if err := mfs.SyncDir("/"); err != nil {
			t.Fatal(err)
		}
		w := wal.NewWriter(f)

		n := 1 + rng.IntN(40)
		var appended []keys.Entry
		synced := 0
		for i := range n {
			e := itertest.Put(string(rune('a'+i%26)), uint64(i+1), string(make([]byte, rng.IntN(200))))
			if rng.IntN(5) == 0 {
				e = itertest.Del(string(rune('a'+i%26)), uint64(i+1))
			}
			if err := w.Append(e); err != nil {
				t.Fatal(err)
			}
			appended = append(appended, e)
			if rng.IntN(4) == 0 {
				if err := w.Sync(); err != nil {
					t.Fatal(err)
				}
				synced = len(appended)
			}
		}

		mfs.CrashTorn(rng)

		r, err := mfs.Open(logName)
		if err != nil {
			t.Fatalf("seed %d: synced log lost: %v", seed, err)
		}
		got, rep, err := replayAll(r)
		r.Close()
		if err != nil {
			t.Fatalf("seed %d: replay: %v", seed, err)
		}
		checkPrefix(t, got, appended)
		if len(got) < synced {
			t.Fatalf("seed %d: replayed %d entries, but %d were synced", seed, len(got), synced)
		}
		size := int64(len(vfstest.ReadFile(t, mfs, logName)))
		if rep.GoodBytes+rep.DroppedBytes != size {
			t.Fatalf("seed %d: report %+v does not add up to file size %d", seed, rep, size)
		}
	}
}
