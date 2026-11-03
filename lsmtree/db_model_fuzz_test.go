package lsmtree_test

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
)

// keySpace is small, so scripts revisit keys often: overwrites, deletes
// of live keys, and puts after deletes. It includes the extreme bytes.
var keySpace = []string{"a", "b", "c", "d", "e", "ab", "ba", "\x00", "\xff", "\xff\xff"}

// script reads fuzz bytes as a sequence of operations.
type script struct{ b []byte }

func (s *script) more() bool { return len(s.b) > 0 }

func (s *script) next() int {
	if len(s.b) == 0 {
		return 0
	}
	v := int(s.b[0])
	s.b = s.b[1:]
	return v
}

func (s *script) key() string { return keySpace[s.next()%len(keySpace)] }

// value makes a value whose length the script picks (0 to ~120 bytes),
// tagged with the step so every write is distinguishable.
func (s *script) value(step int) string {
	return fmt.Sprintf("%d:%s", step, strings.Repeat("v", s.next()%120))
}

// FuzzDBModel is the main correctness test: any sequence of puts,
// deletes, gets, scans, flushes, compactions, reopens, and crashes must
// leave the store agreeing with a plain map. With SyncWrites on, a crash
// loses nothing that was acknowledged. Tiny options make flushes and
// compactions to every level happen within a few dozen steps.
func FuzzDBModel(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 1, 5, 0, 2, 9, 1, 1, 2, 1, 7, 2, 1})
	f.Add([]byte(strings.Repeat("\x00\x03\x50", 80) + "\x06\x07" + strings.Repeat("\x01\x03", 20) + "\x05\x06"))
	f.Add([]byte(strings.Repeat("\x00\x01\x70\x00\x02\x70\x01\x01\x04", 40)))
	r := rand.New(rand.NewPCG(1, 2))
	for range 4 {
		b := make([]byte, 600)
		for i := range b {
			b[i] = byte(r.IntN(256))
		}
		f.Add(b)
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		fs := vfstest.NewMemFS()
		opts := tinyOptions(fs)
		db := open(t, opts)
		defer func() { db.Close() }()
		model := map[string]string{}
		s := &script{b: data}

		for step := 0; s.more() && step < 400; step++ {
			switch op := s.next() % 10; op {
			case 0, 1, 2: // puts are the common case
				k, v := s.key(), s.value(step)
				put(t, db, k, v)
				model[k] = v
			case 3:
				k := s.key()
				del(t, db, k)
				delete(model, k)
			case 4:
				checkGet(t, db, model, s.key())
			case 5:
				if got := dump(t, db); !maps.Equal(got, model) {
					t.Fatalf("step %d: Scan disagrees with the model:\n%s", step, diff(got, model))
				}
			case 6:
				if err := db.Flush(); err != nil {
					t.Fatalf("step %d: Flush: %v", step, err)
				}
			case 7:
				if err := db.Compact(); err != nil {
					t.Fatalf("step %d: Compact: %v", step, err)
				}
			case 8:
				if err := db.Close(); err != nil {
					t.Fatalf("step %d: Close: %v", step, err)
				}
				db = open(t, opts)
			case 9:
				fs.Crash() // the old handle is abandoned, as after a power cut
				db = open(t, opts)
			}
		}
		checkModel(t, db, model, keySpace)
	})
}

// FuzzCrashRecovery checks the prefix property without SyncWrites: after
// a crash (clean or torn), the store holds exactly the state after some
// prefix of the writes, and that prefix includes every write made before
// the last explicit Flush or Close. A crash may lose recent writes, but
// never an older write while keeping a newer one.
func FuzzCrashRecovery(f *testing.F) {
	f.Add([]byte{}, uint64(0))
	f.Add([]byte(strings.Repeat("\x00\x01\x30\x01\x02", 30)+"\x08"+strings.Repeat("\x00\x03\x10", 30)), uint64(1))
	f.Add([]byte(strings.Repeat("\x00\x04\x60\x06\x00\x05\x20", 25)), uint64(7))

	f.Fuzz(func(t *testing.T, data []byte, seed uint64) {
		fs := vfstest.NewMemFS()
		opts := tinyOptions(fs)
		opts.SyncWrites = false
		db := open(t, opts)
		defer func() { db.Close() }()

		// history[i] is the model after the first i writes.
		model := map[string]string{}
		history := []map[string]string{maps.Clone(model)}
		durable := 0 // writes known to be on disk
		rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		s := &script{b: data}

		recoverFrom := func(step int, torn bool) {
			if torn {
				fs.CrashTorn(rng)
			} else {
				fs.Crash()
			}
			db = open(t, opts)
			got := dump(t, db)
			for i := len(history) - 1; i >= durable; i-- {
				if maps.Equal(got, history[i]) {
					// Recovery chose prefix i; later writes are lost.
					model = maps.Clone(history[i])
					history = history[:i+1]
					durable = i
					return
				}
			}
			t.Fatalf("step %d: after a crash the store matches no prefix of at least %d writes (of %d).\nGot %v\nLast durable state %v",
				step, durable, len(history)-1, got, history[durable])
		}

		for step := 0; s.more() && step < 300; step++ {
			switch op := s.next() % 8; op {
			case 0, 1, 2, 3:
				k, v := s.key(), s.value(step)
				put(t, db, k, v)
				model[k] = v
				history = append(history, maps.Clone(model))
			case 4:
				k := s.key()
				del(t, db, k)
				delete(model, k)
				history = append(history, maps.Clone(model))
			case 5:
				if err := db.Flush(); err != nil {
					t.Fatalf("step %d: Flush: %v", step, err)
				}
				durable = len(history) - 1
			case 6:
				recoverFrom(step, false)
			case 7:
				recoverFrom(step, true)
			}
		}
		recoverFrom(-1, true)
	})
}

// TestModelLongRun is a fixed-seed workload long enough to fill four
// levels, checked against the model as it goes. It is the deterministic
// counterpart of FuzzDBModel, and runs with plain go test. With -short
// (make race) it runs a tenth of the steps, which still reach L3.
func TestModelLongRun(t *testing.T) {
	steps := 20000
	if testing.Short() {
		steps = 2000
	}
	fs := vfstest.NewMemFS()
	opts := tinyOptions(fs)
	db := open(t, opts)
	defer func() { db.Close() }()
	r := rand.New(rand.NewPCG(42, 42))
	model := map[string]string{}
	keySpace := make([]string, 400)
	for i := range keySpace {
		keySpace[i] = fmt.Sprintf("user:%04d", i)
	}
	for step := range steps {
		k := keySpace[r.IntN(len(keySpace))]
		switch n := r.IntN(100); {
		case n < 70:
			v := fmt.Sprintf("%d-%s", step, strings.Repeat("x", r.IntN(40)))
			put(t, db, k, v)
			model[k] = v
		case n < 90:
			del(t, db, k)
			delete(model, k)
		case n < 99:
			checkGet(t, db, model, k)
		default:
			db.Close()
			db = open(t, opts)
		}
	}
	checkModel(t, db, model, keySpace)
	s := db.Stats()
	if s.Levels[3].Files == 0 {
		t.Errorf("%d steps never reached the last level: %+v", steps, s.Levels)
	}
	t.Logf("levels %+v; write amp %.2f over %d flushes and %d compactions",
		s.Levels, s.WriteAmplification(), s.Flushes, s.Compactions)
}
