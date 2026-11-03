//go:build e2e

package e2e

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree"
)

// Tunings for newEnv. defaults is lsmtree's own; small makes flushes and
// compactions happen after a few thousand keys; tiny makes them happen
// every few dozen writes, so a SIGKILL often lands in one.
var (
	defaults = lsmtree.Options{}
	small    = lsmtree.Options{MemtableSize: 8 << 10, TableSize: 16 << 10, BaseLevelSize: 32 << 10, LevelSizeRatio: 4}
	tiny     = lsmtree.Options{MemtableSize: 2 << 10, TableSize: 4 << 10, BaseLevelSize: 8 << 10, LevelSizeRatio: 4}
)

// 1. The basics: put, get, scan, and delete, each in its own process, so
// every read comes from what an earlier process left on disk.
func TestPersistsAcrossProcesses(t *testing.T) {
	e := newEnv(t, defaults)
	e.lsmctl("put", "greeting", "hello world")
	e.lsmctl("put", "apple", "red")
	e.lsmctl("put", "cherry", "")
	if got := e.lsmctl("get", "greeting"); got != "hello world\n" {
		t.Fatalf("get = %q", got)
	}
	e.lsmctl("put", "apple", "green")
	if got := e.lsmctl("scan"); got != "apple\tgreen\ncherry\t\"\"\ngreeting\thello world\n" {
		t.Fatalf("scan = %q", got)
	}
	if got := e.lsmctl("scan", "apple", "cherry"); got != "apple\tgreen\n" { // [start, end)
		t.Fatalf("scan apple cherry = %q", got)
	}
	e.lsmctl("del", "greeting")
	e.wantError(`key "greeting" not found`, "get", "greeting")
	if got := e.lsmctl("scan"); got != "apple\tgreen\ncherry\t\"\"\n" {
		t.Fatalf("scan after del = %q", got)
	}

	// Ground truth: the files are on disk, and the library reads them.
	tables, _ := filepath.Glob(filepath.Join(e.dir(), "*.sst"))
	if len(tables) == 0 {
		t.Fatal("no .sst files on disk")
	}
	if s := e.inspect(); s.Levels[0].Files == 0 {
		t.Fatalf("the library sees no L0 tables: %+v", s.Levels[0])
	}
}

// 2. A load large enough to fill several levels. The library confirms the
// shape, and every level above the last is within its target. The dump
// is logged: it is the picture of the tree the README and blog use.
func TestLoadBuildsLevels(t *testing.T) {
	e := newEnv(t, small)
	out := e.lsmctl("load", "4000")
	t.Logf("$ lsmctl load 4000\n%s", out)
	if !strings.Contains(out, "write amplification") {
		t.Fatalf("load summary: %q", out)
	}

	s := e.inspect()
	deepest := 0
	for i, l := range s.Levels {
		if l.Files > 0 {
			deepest = i
		}
	}
	if deepest < 3 {
		t.Fatalf("data reached only L%d; want at least L3", deepest)
	}
	for i := 1; i < deepest; i++ {
		if l := s.Levels[i]; l.Bytes > l.Target {
			t.Fatalf("L%d holds %d bytes, over its %d target", i, l.Bytes, l.Target)
		}
	}

	dump := e.lsmctl("dump")
	t.Logf("$ lsmctl dump\n%s", dump)
	for _, want := range []string{"L0 ", "L1 ", fmt.Sprintf("L%d ", deepest), "cursor", "->"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("dump has no %q", want)
		}
	}
	for _, line := range lines(dump) {
		if len(line) > 80 {
			t.Fatalf("dump line is %d columns: %q", len(line), line)
		}
	}
	stats := e.lsmctl("stats")
	t.Logf("$ lsmctl stats\n%s", stats)
	if !strings.Contains(stats, "live data:           4000 keys") {
		t.Fatalf("stats: %s", stats)
	}
	e.lsmctl("verify")
}

// 3. The crash test. `lsmctl -sync load` prints each key once Put has
// returned, i.e. once the write is fsynced. SIGKILL at a few different
// points (with a small memtable, so a kill can land in a flush or a
// compaction), then: verify passes, every acknowledged key is there with
// its value, and nothing else in the store is garbage.
func TestSigkillLosesNoAcknowledgedWrite(t *testing.T) {
	const seed, valueSize = 7, 40
	for _, killAfter := range []int{50, 300, 700} {
		t.Run(fmt.Sprintf("kill after %d acks", killAfter), func(t *testing.T) {
			t.Parallel()
			e := newEnv(t, tiny)
			l := e.startLoad(200000, "-seed", fmt.Sprint(seed), "-value-size", fmt.Sprint(valueSize))
			acked := append(l.waitAcks(t, killAfter), l.kill()...)

			out := e.lsmctl("verify")
			got := e.scan()
			t.Logf("%d acknowledged, %d in the store after recovery; %s", len(acked), len(got), strings.TrimSpace(out))
			for _, k := range acked {
				if want := loadValue(k, seed, valueSize); got[k] != want {
					t.Fatalf("acknowledged key %s = %q, want %q", k, got[k], want)
				}
			}
			// Unacknowledged writes may or may not have survived, but
			// whatever survived must be a real write, not garbage.
			for k, v := range got {
				if v != loadValue(k, seed, valueSize) {
					t.Fatalf("store holds %s = %q, which was never written", k, v)
				}
			}
		})
	}
}

// 4. Separate put processes each leave a small L0 table (Open replays the
// WAL into one). Once L0 reaches the trigger, `lsmctl compact` merges it
// down.
func TestCompactShrinksL0(t *testing.T) {
	e := newEnv(t, defaults)
	for i := range 6 {
		e.lsmctl("put", fmt.Sprintf("k%d", i), "v")
	}
	before := e.inspect().Levels[0].Files
	if before < 4 {
		t.Fatalf("L0 has %d tables before compact; want at least the trigger, 4", before)
	}
	out := e.lsmctl("compact")
	s := e.inspect()
	t.Logf("L0 %d -> %d tables; lsmctl compact: %s", before, s.Levels[0].Files, strings.TrimSpace(out))
	if s.Levels[0].Files >= 4 || s.Levels[1].Files == 0 {
		t.Fatalf("after compact: L0 %d tables, L1 %d; want L0 below 4 and data in L1", s.Levels[0].Files, s.Levels[1].Files)
	}
	if got := e.scan(); len(got) != 6 {
		t.Fatalf("scan after compact has %d keys, want 6", len(got))
	}
}

// 5. A damaged table is a known edge case: a friendly message with a log
// ID and exit 1, never a panic or a bug report.
func TestCorruptTableIsReportedNotPanicked(t *testing.T) {
	e := newEnv(t, small)
	// 100 keys of ~110 bytes overflow the 8 KiB memtable once: one
	// flushed table of two 4 KiB data blocks is all the test needs.
	e.lsmctl("load", "100")
	tables, _ := filepath.Glob(filepath.Join(e.dir(), "*.sst"))
	// The largest table, so a third of the way in is inside a data block.
	victim := slices.MaxFunc(tables, func(a, b string) int { return cmp.Compare(size(t, a), size(t, b)) })
	b, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	b[len(b)/3] ^= 0xff
	if err := os.WriteFile(victim, b, 0o644); err != nil {
		t.Fatal(err)
	}
	e.wantError("corrupt", "verify")
	e.wantError("corrupt", "scan")

	// A table cut short loses its footer, so the store cannot even open.
	if err := os.WriteFile(victim, b[:len(b)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	e.wantError("corrupt", "get", "key-00000001")

	logged, err := os.ReadFile(filepath.Join(e.root, "lsmctl.log"))
	if err != nil || !strings.Contains(string(logged), `"log_id"`) {
		t.Fatalf("the log should hold the errors' details: %v", err)
	}
}

// 6. The store is locked while a process uses it. A second opener fails
// cleanly, and the store works again once the first process is gone.
func TestSecondOpenerFailsCleanly(t *testing.T) {
	e := newEnv(t, defaults)
	l := e.startLoad(200000)
	first := l.waitAcks(t, 1)[0] // the store is open
	e.wantError("another process is using it", "get", first)
	l.kill()
	// The OS dropped the lock with the process.
	e.lsmctl("verify")
	e.lsmctl("get", first)
}

func size(t *testing.T, path string) int64 {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Size()
}
