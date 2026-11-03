package compaction_test

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/compaction"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
)

// script reads fuzz bytes as small numbers; it returns 0 once they run
// out, so every input decodes to something.
type script struct{ b []byte }

func (s *script) next(n int) int {
	if len(s.b) == 0 {
		return 0
	}
	x := int(s.b[0]) % n
	s.b = s.b[1:]
	return x
}

// key turns k into a 2-byte big-endian key, so byte order equals numeric
// order and keys include 0x00 bytes.
func key(k int) []byte { return binary.BigEndian.AppendUint16(nil, uint16(k)) }

// genVersion builds a random Version that satisfies the invariant: L0
// files with random (possibly overlapping) ranges, and on each deeper
// level sorted, disjoint ranges with random gaps.
func genVersion(s *script) (meta.Version, compaction.Options) {
	levels := 2 + s.next(4) // L0 plus 1..4 disk levels
	opts := compaction.Options{L0Trigger: 1 + s.next(4), LevelTargets: make([]int64, levels)}
	for i := 1; i < levels; i++ {
		opts.LevelTargets[i] = int64(1 + s.next(64))
	}
	v := meta.Version{Levels: make([][]meta.FileMeta, levels), LastSeq: 100}
	num := uint64(1)
	add := func(level, lo, hi int) {
		v.Levels[level] = append(v.Levels[level], meta.FileMeta{
			Num: num, Size: int64(1 + s.next(32)), Smallest: key(lo), Largest: key(hi),
			MinSeq: 1, MaxSeq: uint64(1 + s.next(100)), Entries: 1,
		})
		num++
	}
	for range s.next(6) {
		lo := s.next(64)
		add(0, lo, lo+s.next(16))
	}
	for level := 1; level < levels; level++ {
		pos := 0
		for range s.next(6) {
			lo := pos + s.next(8)
			hi := lo + s.next(8)
			add(level, lo, hi)
			pos = hi + 1
		}
	}
	v.NextFileNum = num
	return v, opts
}

// genOutputs splits [lo, hi] into up to three disjoint output files, or
// none, numbered from v.NextFileNum.
func genOutputs(s *script, v *meta.Version, lo, hi int) []meta.FileMeta {
	var out []meta.FileMeta
	n := s.next(4)
	width := (hi - lo + 1 + n - 1) / max(n, 1)
	for start := lo; len(out) < n && start <= hi; start += width {
		end := min(start+width-1, hi)
		out = append(out, meta.FileMeta{Num: v.NextFileNum, Size: int64(1 + s.next(64)),
			Smallest: key(start), Largest: key(end), MinSeq: 1, MaxSeq: 1, Entries: 1})
		v.NextFileNum++
	}
	return out
}

func keyNum(k []byte) int { return int(binary.BigEndian.Uint16(k)) }

func overlapsRange(f meta.FileMeta, lo, hi int) bool {
	return keyNum(f.Smallest) <= hi && lo <= keyNum(f.Largest)
}

// FuzzPlan checks the planner's properties on random valid versions,
// applying each plan and picking again from the result for a few rounds,
// so each Pick follows the cursors that Apply stored in v.Cursors:
//   - a plan below L0 takes the first file after the level's cursor,
//     wrapping to the first file
//   - Overlaps is exactly the Level+1 files meeting the inputs' range
//   - Bottom is right, by brute force over every deeper file
//   - Apply never changes its input, keeps the Version invariant, and
//     leaves every file not in the plan where it was; it moves only the
//     plan level's cursor, to p.Cursor
func FuzzPlan(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 5, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	f.Add([]byte{3, 1, 1, 1, 1, 5, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17})
	f.Add(bytes.Repeat([]byte{7, 200, 3}, 40))
	f.Add(bytes.Repeat([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13}, 20))
	f.Fuzz(func(t *testing.T, data []byte) {
		s := &script{b: data}
		v, opts := genVersion(s)
		v.CheckInvariant()
		for round := 0; round < 8; round++ {
			p, ok := compaction.Pick(v, opts)
			if !ok {
				checkNothingToDo(t, v, opts)
				return
			}
			checkPlan(t, v, p, opts)

			lo, hi := keyNum(p.Inputs[0].Smallest), keyNum(p.Inputs[0].Largest)
			for _, f := range slices.Concat(p.Inputs, p.Overlaps) {
				lo, hi = min(lo, keyNum(f.Smallest)), max(hi, keyNum(f.Largest))
			}
			next := v.Clone()
			outputs := genOutputs(s, &next, lo, hi)
			before := deepCopy(next)
			after := compaction.Apply(next, p, outputs)
			if !reflect.DeepEqual(next, before) {
				t.Fatal("Apply changed its input version")
			}
			checkApplied(t, next, p, outputs, after)
			v = after
		}
	})
}

// checkNothingToDo confirms no level below the last needs compacting.
func checkNothingToDo(t *testing.T, v meta.Version, opts compaction.Options) {
	t.Helper()
	last := len(opts.LevelTargets) - 1
	for i := 0; i < last && i < len(v.Levels); i++ {
		if i == 0 && len(v.Levels[0]) >= opts.L0Trigger {
			t.Fatalf("no plan, but L0 has %d files, trigger %d", len(v.Levels[0]), opts.L0Trigger)
		}
		if i > 0 && v.LevelSize(i) > opts.LevelTargets[i] {
			t.Fatalf("no plan, but L%d is %d bytes, target %d", i, v.LevelSize(i), opts.LevelTargets[i])
		}
	}
}

func checkPlan(t *testing.T, v meta.Version, p compaction.Plan, opts compaction.Options) {
	t.Helper()
	if p.Level >= len(opts.LevelTargets)-1 {
		t.Fatalf("plan compacts L%d, the last level", p.Level)
	}
	if p.Level == 0 && !slices.Equal(nums(p.Inputs), nums(v.Levels[0])) {
		t.Fatalf("L0 plan inputs %v, want all of L0 %v", nums(p.Inputs), nums(v.Levels[0]))
	}
	if p.Level > 0 && (len(p.Inputs) != 1 || !bytes.Equal(p.Cursor, p.Inputs[0].Largest)) {
		t.Fatalf("L%d plan: inputs %v cursor %q; want one file and its largest key", p.Level, nums(p.Inputs), p.Cursor)
	}
	if p.Level > 0 {
		cursor := cursorAt(v, p.Level)
		files := v.Levels[p.Level]
		want := files[0] // wrap, unless a file starts after the cursor
		for _, f := range files {
			if bytes.Compare(f.Smallest, cursor) > 0 {
				want = f
				break
			}
		}
		if p.Inputs[0].Num != want.Num {
			t.Fatalf("L%d plan takes file %d, want %d, the first after cursor %q", p.Level, p.Inputs[0].Num, want.Num, cursor)
		}
	}

	lo, hi := keyNum(p.Inputs[0].Smallest), keyNum(p.Inputs[0].Largest)
	for _, f := range p.Inputs {
		lo, hi = min(lo, keyNum(f.Smallest)), max(hi, keyNum(f.Largest))
	}
	var want []uint64
	if p.Level+1 < len(v.Levels) {
		for _, f := range v.Levels[p.Level+1] {
			if overlapsRange(f, lo, hi) {
				want = append(want, f.Num)
			}
		}
	}
	if got := nums(p.Overlaps); !slices.Equal(got, want) {
		t.Fatalf("overlaps = %v, want %v (inputs span %d..%d)", got, want, lo, hi)
	}

	for _, f := range p.Overlaps {
		lo, hi = min(lo, keyNum(f.Smallest)), max(hi, keyNum(f.Largest))
	}
	bottom := true
	for d := p.Level + 2; d < len(v.Levels); d++ {
		for _, f := range v.Levels[d] {
			if overlapsRange(f, lo, hi) {
				bottom = false
			}
		}
	}
	if p.Bottom != bottom {
		t.Fatalf("Bottom = %v, want %v", p.Bottom, bottom)
	}
}

// checkApplied confirms after = before with the plan applied: the
// invariant holds (checked here too, so the noassert build tests it),
// inputs and overlaps are gone, outputs are on Level+1, and every other
// file is on its old level.
func checkApplied(t *testing.T, before meta.Version, p compaction.Plan, outputs []meta.FileMeta, after meta.Version) {
	t.Helper()
	for level, files := range after.Levels {
		for i := 1; level > 0 && i < len(files); i++ {
			if bytes.Compare(files[i-1].Largest, files[i].Smallest) >= 0 {
				t.Fatalf("L%d files %d and %d overlap or are out of order", level, files[i-1].Num, files[i].Num)
			}
		}
	}
	gone := map[uint64]bool{}
	for _, f := range slices.Concat(p.Inputs, p.Overlaps) {
		gone[f.Num] = true
	}
	want := map[uint64]int{} // file number → level
	for level, files := range before.Levels {
		for _, f := range files {
			if !gone[f.Num] {
				want[f.Num] = level
			}
		}
	}
	for _, f := range outputs {
		want[f.Num] = p.Level + 1
	}
	got := map[uint64]int{}
	for level, files := range after.Levels {
		for _, f := range files {
			got[f.Num] = level
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("files after Apply = %v, want %v", got, want)
	}

	for level := range max(len(before.Cursors), len(after.Cursors)) {
		wantCursor, gotCursor := cursorAt(before, level), cursorAt(after, level)
		if p.Level > 0 && level == p.Level {
			wantCursor = p.Cursor
		}
		if !bytes.Equal(gotCursor, wantCursor) {
			t.Fatalf("L%d cursor after Apply = %q, want %q", level, gotCursor, wantCursor)
		}
	}
}

func cursorAt(v meta.Version, level int) []byte {
	if level < len(v.Cursors) {
		return v.Cursors[level]
	}
	return nil
}
