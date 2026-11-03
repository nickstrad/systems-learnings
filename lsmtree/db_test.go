package lsmtree_test

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs/vfstest"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
	"github.com/nickstrad/systems-learnings/shared/errs"
)

func TestGetPutDelete(t *testing.T) {
	db := open(t, tinyOptions(vfstest.NewMemFS()))
	defer db.Close()

	steps := []struct {
		op, key, value string
		want           string // Get(key) after the step
	}{
		{"get", "k", "", "<absent>"},       // empty store
		{"del", "k", "", "<absent>"},       // deleting a missing key is fine
		{"put", "k", "v1", "v1"},           // first write
		{"put", "k", "v2", "v2"},           // overwrite: newest wins
		{"del", "k", "", "<absent>"},       // delete hides it
		{"put", "k", "v3", "v3"},           // put after delete shows again
		{"put", "e", "", ""},               // an empty value is a value
		{"get", "missing", "", "<absent>"}, // another key is unaffected
	}
	for i, s := range steps {
		switch s.op {
		case "put":
			put(t, db, s.key, s.value)
		case "del":
			del(t, db, s.key)
		}
		if got := get(t, db, s.key); got != s.want {
			t.Fatalf("step %d (%s %q): Get = %q, want %q", i, s.op, s.key, got, s.want)
		}
	}
}

func TestKeyAndValueLimits(t *testing.T) {
	db := open(t, tinyOptions(vfstest.NewMemFS()))
	defer db.Close()
	big := func(n int) []byte { return bytes.Repeat([]byte{'x'}, n) }

	cases := []struct {
		name       string
		key, value []byte
		want       error
	}{
		{"empty key", nil, []byte("v"), lsmtree.ErrInvalidKey},
		{"1-byte key", []byte("k"), []byte("v"), nil},
		{"max key", big(keys.MaxKeySize), []byte("v"), nil},
		{"max key + 1", big(keys.MaxKeySize + 1), []byte("v"), lsmtree.ErrInvalidKey},
		{"empty value", []byte("k"), nil, nil},
		{"max value", []byte("k"), big(keys.MaxValueSize), nil},
		{"max value + 1", []byte("k"), big(keys.MaxValueSize + 1), lsmtree.ErrValueTooBig},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := db.Put(c.key, c.value)
			if !errors.Is(err, c.want) {
				t.Fatalf("Put = %v, want %v", err, c.want)
			}
			if c.want != nil {
				assertWellFormed(t, err)
				return
			}
			if got, ok, err := db.Get(c.key); err != nil || !ok || !bytes.Equal(got, c.value) {
				t.Fatalf("Get after Put: ok=%v err=%v, value of %d bytes, want %d", ok, err, len(got), len(c.value))
			}
		})
	}
	if _, _, err := db.Get(nil); !errors.Is(err, lsmtree.ErrInvalidKey) {
		t.Errorf("Get(empty key) = %v, want ErrInvalidKey", err)
	}
	if err := db.Delete(big(keys.MaxKeySize + 1)); !errors.Is(err, lsmtree.ErrInvalidKey) {
		t.Errorf("Delete(oversize key) = %v, want ErrInvalidKey", err)
	}
}

func TestScanBounds(t *testing.T) {
	db := open(t, tinyOptions(vfstest.NewMemFS()))
	defer db.Close()
	for _, k := range []string{"a", "b", "c", "d"} {
		put(t, db, k, "v"+k)
	}
	del(t, db, "c")
	if err := db.Flush(); err != nil { // half on disk, half in the memtable
		t.Fatal(err)
	}
	put(t, db, "b", "newer")
	put(t, db, "e", "ve")

	scan := func(start, end string, limit int) string {
		var s, e []byte
		if start != "" {
			s = []byte(start)
		}
		if end != "" {
			e = []byte(end)
		}
		var got []string
		err := db.Scan(s, e, func(k, v []byte) bool {
			got = append(got, string(k)+"="+string(v))
			return len(got) < limit
		})
		if err != nil {
			t.Fatal(err)
		}
		return strings.Join(got, " ")
	}
	cases := []struct {
		start, end string
		limit      int
		want       string
	}{
		{"", "", 100, "a=va b=newer d=vd e=ve"}, // all; c is deleted; b's newest wins
		{"b", "", 100, "b=newer d=vd e=ve"},     // start is inclusive
		{"", "d", 100, "a=va b=newer"},          // end is exclusive
		{"bb", "dd", 100, "d=vd"},               // bounds between keys
		{"c", "c", 100, ""},                     // empty range
		{"z", "", 100, ""},                      // past the end
		{"", "", 2, "a=va b=newer"},             // fn returning false stops it
	}
	for _, c := range cases {
		if got := scan(c.start, c.end, c.limit); got != c.want {
			t.Errorf("Scan[%q, %q) = %q, want %q", c.start, c.end, got, c.want)
		}
	}
}

func TestDataSurvivesReopen(t *testing.T) {
	for _, syncWrites := range []bool{false, true} {
		t.Run(fmt.Sprintf("SyncWrites=%v", syncWrites), func(t *testing.T) {
			fs := vfstest.NewMemFS()
			opts := tinyOptions(fs)
			opts.SyncWrites = syncWrites
			db := open(t, opts)
			model := map[string]string{}
			for i := range 300 {
				k := fmt.Sprintf("k%03d", i%97)
				if i%7 == 0 {
					del(t, db, k)
					delete(model, k)
				} else {
					v := fmt.Sprintf("v%d", i)
					put(t, db, k, v)
					model[k] = v
				}
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			db = open(t, opts)
			defer db.Close()
			checkModel(t, db, model, nil)
		})
	}
}

// TestDeleteDoesNotResurrect is the classic LSM bug: a tombstone dropped
// too early lets an older value on a deeper level come back.
func TestDeleteDoesNotResurrect(t *testing.T) {
	fs := vfstest.NewMemFS()
	db := open(t, tinyOptions(fs))
	put(t, db, "victim", "old")
	for i := range 200 { // flushes and compactions push "victim" down the levels
		put(t, db, fmt.Sprintf("filler%03d", i), strings.Repeat("x", 20))
	}
	del(t, db, "victim")
	for i := range 200 { // drive the tombstone down through compactions
		put(t, db, fmt.Sprintf("filler%03d", i), strings.Repeat("y", 20))
		if got := get(t, db, "victim"); got != "<absent>" {
			t.Fatalf("after %d more writes, deleted key is back: %q", i, got)
		}
	}
	db.Close()
	db = open(t, tinyOptions(fs))
	defer db.Close()
	if got := get(t, db, "victim"); got != "<absent>" {
		t.Fatalf("after reopen, deleted key is back: %q", got)
	}
}

func TestUseAfterClose(t *testing.T) {
	db := open(t, tinyOptions(vfstest.NewMemFS()))
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	k := []byte("k")
	_, _, getErr := db.Get(k)
	for name, err := range map[string]error{
		"Put":     db.Put(k, k),
		"Delete":  db.Delete(k),
		"Get":     getErr,
		"Scan":    db.Scan(nil, nil, func(_, _ []byte) bool { return true }),
		"Flush":   db.Flush(),
		"Compact": db.Compact(),
		"Close":   db.Close(),
	} {
		if !errors.Is(err, lsmtree.ErrClosed) {
			t.Errorf("%s after Close = %v, want ErrClosed", name, err)
		}
	}
}

func TestOneOpenerAtATime(t *testing.T) {
	fs := vfstest.NewMemFS()
	db := open(t, tinyOptions(fs))
	_, err := lsmtree.Open(dir, tinyOptions(fs))
	if !errors.Is(err, vfs.ErrLocked) {
		t.Fatalf("second Open = %v, want vfs.ErrLocked", err)
	}
	assertWellFormed(t, err)
	db.Close()
	db = open(t, tinyOptions(fs)) // the lock is released by Close
	db.Close()
}

func TestBadOptions(t *testing.T) {
	cases := map[string]func(*lsmtree.Options){
		"ratio below 2":       func(o *lsmtree.Options) { o.LevelSizeRatio = 1 },
		"one level":           func(o *lsmtree.Options) { o.MaxLevels = 1; o.LevelTargets = nil },
		"negative memtable":   func(o *lsmtree.Options) { o.MemtableSize = -1 },
		"zero level target":   func(o *lsmtree.Options) { o.LevelTargets = []int64{0, 0} },
		"targets for L0 only": func(o *lsmtree.Options) { o.LevelTargets = []int64{0} },
		"targets overflow":    func(o *lsmtree.Options) { o.LevelSizeRatio = 25; o.MaxLevels = 14 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			opts := tinyOptions(vfstest.NewMemFS())
			mutate(&opts)
			_, err := lsmtree.Open(dir, opts)
			if !errors.Is(err, lsmtree.ErrBadOptions) {
				t.Fatalf("Open = %v, want ErrBadOptions", err)
			}
			assertWellFormed(t, err)
		})
	}

	t.Run("fewer levels than the store has", func(t *testing.T) {
		fs := vfstest.NewMemFS()
		opts := tinyOptions(fs)
		db := open(t, opts)
		db.Close()
		opts.MaxLevels = 3
		if _, err := lsmtree.Open(dir, opts); !errors.Is(err, lsmtree.ErrBadOptions) {
			t.Fatalf("Open = %v, want ErrBadOptions", err)
		}
	})
}

func TestCorruptTableIsReported(t *testing.T) {
	fs := vfstest.NewMemFS()
	db := open(t, tinyOptions(fs))
	put(t, db, "k", "v")
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	db.Close()

	names, _ := fs.List(dir)
	for _, name := range names {
		if _, kind, ok := meta.ParseFileName(name); !ok || kind != meta.KindTable {
			continue
		}
		b := vfstest.ReadFile(t, fs, path.Join(dir, name))
		b[len(b)/2] ^= 0xff
		vfstest.WriteFile(t, fs, path.Join(dir, name), b)
	}

	db, err := lsmtree.Open(dir, tinyOptions(fs))
	if err == nil {
		defer db.Close()
		if err = db.Verify(); err == nil {
			t.Fatal("Verify accepted a table with a flipped byte")
		}
		_, _, err = db.Get([]byte("k"))
	}
	if !errors.Is(err, lsmtree.ErrCorrupt) {
		t.Fatalf("a flipped byte in a table gave %v, want ErrCorrupt from Open or Get", err)
	}
	assertWellFormed(t, err)
}

func TestEventsAndStats(t *testing.T) {
	var events []lsmtree.Event
	opts := tinyOptions(vfstest.NewMemFS())
	opts.Events = func(e lsmtree.Event) { events = append(events, e) }
	db := open(t, opts)
	defer db.Close()
	if len(events) != 1 || events[0].Kind != lsmtree.EventRecovery {
		t.Fatalf("Open emitted %+v, want one recovery event", events)
	}
	for i := range 500 {
		put(t, db, fmt.Sprintf("key%04d", i*7919%500), strings.Repeat("v", 30))
	}

	counts := map[lsmtree.EventKind]int{}
	for _, e := range events {
		counts[e.Kind]++
		if e.Kind != lsmtree.EventRecovery && (e.BytesWritten <= 0 || e.OutputBlocks <= 0) {
			t.Errorf("%s event wrote nothing: %+v", e.Kind, e)
		}
		if e.Kind == lsmtree.EventCompaction && e.ToLevel != e.FromLevel+1 {
			t.Errorf("compaction from L%d to L%d", e.FromLevel, e.ToLevel)
		}
	}
	s := db.Stats()
	if counts[lsmtree.EventFlush] != int(s.Flushes) || counts[lsmtree.EventCompaction] != int(s.Compactions) {
		t.Errorf("events %v disagree with stats: %d flushes, %d compactions", counts, s.Flushes, s.Compactions)
	}
	if s.Flushes == 0 || s.Compactions == 0 {
		t.Fatalf("500 writes with tiny options caused %d flushes and %d compactions", s.Flushes, s.Compactions)
	}
	if wa := s.WriteAmplification(); wa <= 1 {
		t.Errorf("write amplification = %.2f; compaction rewrites data, so it must exceed 1", wa)
	}
	if s.Writes != 500 || len(s.Levels) != 4 || s.Levels[0].Target != 0 || s.Levels[1].Target != 512 || s.L0Trigger != 2 {
		t.Errorf("unexpected stats: %+v", s)
	}
	for i, l := range s.Levels {
		hasNext := i > 0 && i < len(s.Levels)-1 && l.Files > 0
		if hasNext != (l.Next >= 0) || l.Next >= l.Files {
			t.Errorf("L%d (%d tables) has Next %d", i, l.Files, l.Next)
		}
	}
	if err := db.Verify(); err != nil {
		t.Fatalf("Verify on a healthy store: %v", err)
	}
	tables := 0
	for _, l := range s.Levels {
		tables += len(l.Tables)
		if len(l.Tables) != l.Files {
			t.Errorf("level lists %d tables but counts %d files", len(l.Tables), l.Files)
		}
	}
	if tables == 0 {
		t.Error("Stats lists no tables after compactions")
	}
	t.Logf("after 500 writes: write amp %.2f, %d flushes, %d compactions, levels %+v",
		s.WriteAmplification(), s.Flushes, s.Compactions, s.Levels)
}

// assertWellFormed checks the error rule from docs/coding.md: every error
// leaving the package is this module's *errs.Error.
func assertWellFormed(t *testing.T, err error) {
	t.Helper()
	e, ok := errs.Outermost(err)
	if !ok || e.Module != lsmtree.Module || e.Message == "" {
		t.Fatalf("error %q is not a well-formed lsmtree error: %s", err, errs.Describe(err))
	}
}

// TestStatsL0TriggerDefault checks that Stats reports the trigger in
// effect, not the zero the caller passed.
func TestStatsL0TriggerDefault(t *testing.T) {
	db := open(t, lsmtree.Options{FS: vfstest.NewMemFS()})
	defer db.Close()
	if got := db.Stats().L0Trigger; got != 4 {
		t.Fatalf("L0Trigger = %d, want the default 4", got)
	}
}

// TestStatsNextIsTheNextCompaction checks LevelStats.Next against what
// compaction really does. Each round reopens the store with L1's target
// one byte under its size, so Compact runs exactly one L1 compaction,
// and the one table it removes from L1 must be the one Stats marked.
// The cursor is saved in the manifest, so rounds follow it across
// reopens. Halfway through, keys that sort before the cursor are added
// to L1, so the marked table is not simply the first one, and the
// sweep must later wrap back to them.
func TestStatsNextIsTheNextCompaction(t *testing.T) {
	const huge = 1 << 40
	opts := tinyOptions(vfstest.NewMemFS())
	opts.MaxLevels = 0

	// fill writes n keys with prefix and compacts them into L1, which no
	// target lets go of.
	fill := func(prefix string, n int) {
		opts.L0CompactionTrigger = 2
		opts.LevelTargets = []int64{0, huge, huge, huge}
		db := open(t, opts)
		for i := range n {
			put(t, db, fmt.Sprintf("%s%04d", prefix, i*7919%n), strings.Repeat("v", 30))
		}
		if err := db.Compact(); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	// round runs one L1 compaction and reports whether L1 had two or
	// more tables to run it on.
	round := func() bool {
		opts.L0CompactionTrigger = 100 // L0 stays put; only L1 is over target
		opts.LevelTargets = []int64{0, huge, huge, huge}
		db := open(t, opts)
		l1 := db.Stats().Levels[1]
		db.Close()
		if l1.Files < 2 {
			return false
		}
		opts.LevelTargets[1] = l1.Bytes - 1
		db = open(t, opts)
		defer db.Close()
		before := db.Stats()
		marked := before.Levels[1].Tables[before.Levels[1].Next].Num
		if err := db.Compact(); err != nil {
			t.Fatal(err)
		}
		after := db.Stats()
		if n := after.Compactions - before.Compactions; n != 1 {
			t.Fatalf("Compact ran %d compactions, want 1", n)
		}
		var gone []uint64
		for _, tb := range before.Levels[1].Tables {
			if !slices.ContainsFunc(after.Levels[1].Tables, func(a lsmtree.TableInfo) bool { return a.Num == tb.Num }) {
				gone = append(gone, tb.Num)
			}
		}
		if !slices.Equal(gone, []uint64{marked}) {
			t.Fatalf("Stats marked table %d (Next %d, cursor %q), but compaction took %v",
				marked, before.Levels[1].Next, before.Levels[1].Cursor, gone)
		}
		return true
	}

	fill("key", 300)
	for range 3 {
		if !round() {
			t.Fatal("setup: L1 ran out of tables")
		}
	}
	fill("aaa", 100) // sorts before the cursor
	rounds := 0
	for round() {
		rounds++
	}
	if rounds < 10 {
		t.Fatalf("only %d rounds after adding keys before the cursor", rounds)
	}
}

// TestCompactAfterReopen covers a store reopened with L0 over its
// trigger: the flush at Open does not compact, and Compact must, even
// with an empty memtable.
func TestCompactAfterReopen(t *testing.T) {
	fs := vfstest.NewMemFS()
	opts := tinyOptions(fs)
	opts.L0CompactionTrigger = 3
	for i := range 4 { // each reopen flushes the WAL to a new L0 table
		db := open(t, opts)
		put(t, db, fmt.Sprintf("k%d", i), "v")
		db.Close()
	}
	db := open(t, opts)
	defer db.Close()
	if l0 := db.Stats().Levels[0].Files; l0 < 3 {
		t.Fatalf("setup: L0 has %d files, want at least the trigger, 3", l0)
	}
	if err := db.Compact(); err != nil {
		t.Fatal(err)
	}
	if l0 := db.Stats().Levels[0].Files; l0 >= 3 {
		t.Fatalf("after Compact, L0 still has %d files", l0)
	}
}

// TestDBInvariantTrips shows the check that every live table has an open
// reader can fail.
func TestDBInvariantTrips(t *testing.T) {
	db := open(t, tinyOptions(vfstest.NewMemFS()))
	defer db.Close()
	put(t, db, "k", "v")
	if err := db.Flush(); err != nil {
		t.Fatal(err)
	}
	db.CheckInvariant() // holds
	db.ForgetReader(db.Stats().Levels[0].Tables[0].Num)
	asserttest.Panics(t, "no open reader for live table", db.CheckInvariant)
}
