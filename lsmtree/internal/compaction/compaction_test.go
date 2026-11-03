package compaction_test

import (
	"bytes"
	"reflect"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/compaction"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
)

// file makes a FileMeta with the given number, size, and key range.
func file(num uint64, size int64, smallest, largest string) meta.FileMeta {
	return meta.FileMeta{Num: num, Size: size, Smallest: []byte(smallest), Largest: []byte(largest), MinSeq: 1, MaxSeq: 1, Entries: 1}
}

// version builds a valid Version from levels, with counters above every
// file.
func version(levels ...[]meta.FileMeta) meta.Version {
	v := meta.Version{Levels: levels, NextFileNum: 1000, LastSeq: 1000, LogNum: 999}
	v.CheckInvariant()
	return v
}

// nums returns the file numbers of files, for compact comparisons.
func nums(files []meta.FileMeta) []uint64 {
	out := []uint64{}
	for _, f := range files {
		out = append(out, f.Num)
	}
	return out
}

// opts: L0 triggers at 4 files; L1 100 B, L2 1000 B, L3 10000 B (last).
var opts = compaction.Options{L0Trigger: 4, LevelTargets: []int64{0, 100, 1000, 10000}}

func TestPickWhichLevel(t *testing.T) {
	l0 := func(n int) []meta.FileMeta {
		var fs []meta.FileMeta
		for i := range n {
			fs = append(fs, file(uint64(100+i), 1, "a", "z"))
		}
		return fs
	}
	l1 := func(size int64) []meta.FileMeta { return []meta.FileMeta{file(1, size, "m", "m")} }
	l2 := func(size int64) []meta.FileMeta { return []meta.FileMeta{file(2, size, "m", "m")} }

	tests := []struct {
		name  string
		v     meta.Version
		opts  compaction.Options
		ok    bool
		level int
	}{
		{"nil levels", meta.Version{NextFileNum: 1}, opts, false, 0},
		{"empty levels", version(nil, nil, nil, nil), opts, false, 0},
		{"L0 one below trigger", version(l0(3)), opts, false, 0},
		{"L0 at trigger", version(l0(4)), opts, true, 0},
		{"L0 trigger of one, one file", version(l0(1)), compaction.Options{L0Trigger: 1, LevelTargets: []int64{0, 100}}, true, 0},
		{"L1 exactly at target", version(nil, l1(100)), opts, false, 0},
		{"L1 one byte over", version(nil, l1(101)), opts, true, 1},
		{"L2 exactly at target", version(nil, nil, l2(1000)), opts, false, 0},
		{"L2 one byte over", version(nil, nil, l2(1001)), opts, true, 2},
		{"last level far over target", version(nil, nil, nil, []meta.FileMeta{file(3, 1e9, "a", "z")}), opts, false, 0},
		{"two levels: L1 is last, never picked", version(nil, l1(1e9)), compaction.Options{L0Trigger: 4, LevelTargets: []int64{0, 100}}, false, 0},
		{"tie L0 and L1 at score 2: shallower wins", version(l0(8), l1(200)), opts, true, 0},
		{"tie L1 and L2 at score 2: shallower wins", version(nil, l1(200), l2(2000)), opts, true, 1},
		{"higher score deeper wins", version(l0(4), l1(150), l2(3000)), opts, true, 2},
		{"higher score shallower wins", version(l0(12), l1(150), l2(1001)), opts, true, 0},
		{"L1 over beats L0 under trigger", version(l0(3), l1(101)), opts, true, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := compaction.Pick(tt.v, tt.opts)
			if ok != tt.ok {
				t.Fatalf("Pick ok = %v, want %v (plan %+v)", ok, tt.ok, p)
			}
			if ok && p.Level != tt.level {
				t.Fatalf("Pick level = %d, want %d", p.Level, tt.level)
			}
		})
	}
}

// TestPickCursor walks the rolling-merge cursor over L1 files
// [b..c] [e..f] [h..i], each over target on its own so L1 always compacts.
// NextInput, which Stats uses to mark the next table, must agree with
// Pick on every cursor.
func TestPickCursor(t *testing.T) {
	v := version(nil, []meta.FileMeta{file(1, 100, "b", "c"), file(2, 100, "e", "f"), file(3, 100, "h", "i")})
	tests := []struct {
		name   string
		cursor []byte
		want   uint64
	}{
		{"nil cursor starts at the first file", nil, 1},
		{"before every file", []byte("a"), 1},
		{"equal to first file's smallest: skip it", []byte("b"), 2},
		{"inside the first file", []byte("bb"), 2},
		{"equal to first file's largest", []byte("c"), 2},
		{"between files", []byte("d"), 2},
		{"equal to last file's smallest", []byte("h"), 1},
		{"equal to last file's largest: wrap", []byte("i"), 1},
		{"after every file: wrap", []byte("z"), 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := v
			v.Cursors = [][]byte{nil, tt.cursor}
			p, ok := compaction.Pick(v, opts)
			if !ok || p.Level != 1 {
				t.Fatalf("Pick = %+v, %v; want an L1 plan", p, ok)
			}
			if got := nums(p.Inputs); !slices.Equal(got, []uint64{tt.want}) {
				t.Fatalf("inputs = %v, want [%d]", got, tt.want)
			}
			if !bytes.Equal(p.Cursor, p.Inputs[0].Largest) {
				t.Fatalf("cursor = %q, want the input's largest key %q", p.Cursor, p.Inputs[0].Largest)
			}
			if i := compaction.NextInput(v.Levels[1], tt.cursor); v.Levels[1][i].Num != tt.want {
				t.Fatalf("NextInput = file %d, want %d", v.Levels[1][i].Num, tt.want)
			}
		})
	}
}

// TestPickCursorSweeps follows the cursor from each plan into the next
// Pick, the way Apply stores it in the Version: it visits every file once
// and then wraps, like the paper's rolling merge (Figure 2.2).
func TestPickCursorSweeps(t *testing.T) {
	v := version(nil, []meta.FileMeta{file(1, 100, "b", "c"), file(2, 100, "e", "f"), file(3, 100, "h", "i")})
	v.Cursors = make([][]byte, 2)
	var got []uint64
	for range 4 {
		p, ok := compaction.Pick(v, opts)
		if !ok {
			t.Fatal("Pick found nothing")
		}
		got = append(got, p.Inputs[0].Num)
		v.Cursors[1] = p.Cursor
	}
	if want := []uint64{1, 2, 3, 1}; !slices.Equal(got, want) {
		t.Fatalf("cursor visited %v, want %v", got, want)
	}
}

func TestPickCursorsShorterThanLevels(t *testing.T) {
	v := version(nil, nil, []meta.FileMeta{file(1, 600, "a", "b"), file(2, 600, "c", "d")})
	v.Cursors = [][]byte{nil} // no entry for L2
	p, ok := compaction.Pick(v, opts)
	if !ok || p.Level != 2 || p.Inputs[0].Num != 1 {
		t.Fatalf("Pick = %+v, %v; want L2 starting at file 1", p, ok)
	}
}

func TestPickOverlaps(t *testing.T) {
	below := []meta.FileMeta{file(10, 1, "a", "b"), file(11, 1, "d", "f"), file(12, 1, "h", "j"), file(13, 1, "m", "n")}
	tests := []struct {
		name  string
		input meta.FileMeta
		want  []uint64
	}{
		{"no overlap: gap between files", file(1, 200, "c", "c"), nil},
		{"no overlap: after every file", file(1, 200, "x", "z"), nil},
		{"one file, strictly inside", file(1, 200, "e", "e"), []uint64{11}},
		{"touches a file's smallest key", file(1, 200, "c", "d"), []uint64{11}},
		{"touches a file's largest key", file(1, 200, "f", "g"), []uint64{11}},
		{"spans two files", file(1, 200, "e", "i"), []uint64{11, 12}},
		{"overlaps every file", file(1, 200, "a", "z"), []uint64{10, 11, 12, 13}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := version(nil, []meta.FileMeta{tt.input}, below)
			p, ok := compaction.Pick(v, opts)
			if !ok || p.Level != 1 {
				t.Fatalf("Pick = %+v, %v; want an L1 plan", p, ok)
			}
			if got := nums(p.Overlaps); !slices.Equal(got, tt.want) {
				t.Fatalf("overlaps = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestPickL0 checks that an L0 plan takes every L0 file and every L1 file
// in their combined range, even one that sits in a gap between L0 files.
func TestPickL0(t *testing.T) {
	v := version(
		[]meta.FileMeta{file(20, 1, "a", "b"), file(21, 1, "g", "h"), file(22, 1, "a", "c"), file(23, 1, "e", "e")},
		[]meta.FileMeta{file(10, 1, "0", "0"), file(11, 1, "d", "d"), file(12, 1, "f", "f"), file(13, 1, "x", "y")},
	)
	p, ok := compaction.Pick(v, opts)
	if !ok || p.Level != 0 {
		t.Fatalf("Pick = %+v, %v; want an L0 plan", p, ok)
	}
	if got := nums(p.Inputs); !slices.Equal(got, []uint64{20, 21, 22, 23}) {
		t.Fatalf("inputs = %v, want all of L0", got)
	}
	if got := nums(p.Overlaps); !slices.Equal(got, []uint64{11, 12}) {
		t.Fatalf("overlaps = %v, want [11 12]", got)
	}
	if p.Cursor != nil {
		t.Fatalf("L0 plan cursor = %q, want nil", p.Cursor)
	}
}

// TestPickBottom: the input is L1 [d..f]; the L2 file below it is [e..k],
// which widens the merge's range to [d..k].
func TestPickBottom(t *testing.T) {
	in := []meta.FileMeta{file(1, 200, "d", "f")}
	l2 := []meta.FileMeta{file(2, 1, "e", "k")}
	tests := []struct {
		name   string
		v      meta.Version
		bottom bool
	}{
		{"no deeper levels", version(nil, in), true},
		{"next level is the deepest", version(nil, in, l2), true},
		{"empty deeper level", version(nil, in, l2, nil), true},
		{"deeper file outside the range", version(nil, in, l2, []meta.FileMeta{file(3, 1, "m", "z")}), true},
		{"deeper file overlaps the input", version(nil, in, l2, []meta.FileMeta{file(3, 1, "a", "d")}), false},
		{"deeper file overlaps only the widened part", version(nil, in, l2, []meta.FileMeta{file(3, 1, "k", "z")}), false},
		{"no overlaps below: deeper file overlaps the input", version(nil, in, nil, []meta.FileMeta{file(3, 1, "f", "g")}), false},
	}
	o := compaction.Options{L0Trigger: 4, LevelTargets: []int64{0, 100, 1000, 10000, 100000}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, ok := compaction.Pick(tt.v, o)
			if !ok || p.Level != 1 {
				t.Fatalf("Pick = %+v, %v; want an L1 plan", p, ok)
			}
			if p.Bottom != tt.bottom {
				t.Fatalf("Bottom = %v, want %v", p.Bottom, tt.bottom)
			}
		})
	}
}

func TestApply(t *testing.T) {
	v := version(
		[]meta.FileMeta{file(30, 1, "a", "a")},
		[]meta.FileMeta{file(1, 100, "a", "c"), file(2, 200, "e", "g"), file(3, 100, "x", "y")},
		[]meta.FileMeta{file(10, 1, "a", "b"), file(11, 1, "d", "f"), file(12, 1, "g", "h"), file(13, 1, "z", "z")},
	)
	v.Cursors = [][]byte{nil, []byte("c")}
	before := deepCopy(v)
	p, ok := compaction.Pick(v, opts)
	if !ok || p.Level != 1 || p.Inputs[0].Num != 2 {
		t.Fatalf("Pick = %+v, %v; want L1 file 2", p, ok)
	}
	if got := nums(p.Overlaps); !slices.Equal(got, []uint64{11, 12}) {
		t.Fatalf("overlaps = %v, want [11 12]", got)
	}

	tests := []struct {
		name    string
		outputs []meta.FileMeta
		wantL1  []uint64
		wantL2  []uint64
	}{
		{"one output", []meta.FileMeta{file(40, 1, "d", "h")}, []uint64{1, 3}, []uint64{10, 40, 13}},
		{"outputs given out of order are sorted", []meta.FileMeta{file(41, 1, "g", "h"), file(40, 1, "d", "e")}, []uint64{1, 3}, []uint64{10, 40, 41, 13}},
		{"no outputs: every entry was a dropped tombstone", nil, []uint64{1, 3}, []uint64{10, 13}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := compaction.Apply(v, p, tt.outputs)
			if l1 := nums(got.Levels[1]); !slices.Equal(l1, tt.wantL1) {
				t.Errorf("L1 = %v, want %v", l1, tt.wantL1)
			}
			if l2 := nums(got.Levels[2]); !slices.Equal(l2, tt.wantL2) {
				t.Errorf("L2 = %v, want %v", l2, tt.wantL2)
			}
			if l0 := nums(got.Levels[0]); !slices.Equal(l0, []uint64{30}) {
				t.Errorf("L0 = %v, want it untouched", l0)
			}
			if !bytes.Equal(got.Cursors[1], []byte("g")) {
				t.Errorf("L1 cursor = %q, want the input's largest key \"g\"", got.Cursors[1])
			}
			if !reflect.DeepEqual(v, before) {
				t.Fatalf("Apply changed its input version:\n got %+v\nwant %+v", v, before)
			}
		})
	}
}

func TestApplyL0IntoMissingLevel(t *testing.T) {
	v := version([]meta.FileMeta{file(1, 1, "a", "c"), file(2, 1, "b", "d"), file(3, 1, "e", "f"), file(4, 1, "a", "a")})
	before := deepCopy(v)
	p, ok := compaction.Pick(v, opts)
	if !ok || p.Level != 0 || len(p.Overlaps) != 0 || !p.Bottom {
		t.Fatalf("Pick = %+v, %v; want a bottom L0 plan with no overlaps", p, ok)
	}
	got := compaction.Apply(v, p, []meta.FileMeta{file(5, 4, "a", "f")})
	if len(got.Levels) != 2 || len(got.Levels[0]) != 0 || !slices.Equal(nums(got.Levels[1]), []uint64{5}) {
		t.Fatalf("Apply = %+v, want L0 empty and L1 = [5]", got.Levels)
	}
	if !reflect.DeepEqual(v, before) {
		t.Fatal("Apply changed its input version")
	}
}

// TestApplyCursor checks that Apply moves the compacted level's cursor
// and no other, growing Cursors when it is shorter than the level.
func TestApplyCursor(t *testing.T) {
	l1 := []meta.FileMeta{file(1, 200, "a", "c")}
	l2 := []meta.FileMeta{file(2, 2000, "m", "p")}
	tests := []struct {
		name    string
		v       meta.Version
		cursors [][]byte
		want    [][]byte
	}{
		{"nil cursors, L1 plan", version(nil, l1), nil, [][]byte{nil, []byte("c")}},
		{"nil cursors, L2 plan", version(nil, nil, l2, nil), nil, [][]byte{nil, nil, []byte("p")}},
		{"other level's cursor is kept", version(nil, nil, l2, nil), [][]byte{nil, []byte("x")}, [][]byte{nil, []byte("x"), []byte("p")}},
		{"L0 plan leaves cursors alone", version([]meta.FileMeta{file(3, 1, "a", "b"), file(4, 1, "a", "b"), file(5, 1, "a", "b"), file(6, 1, "a", "b")}), [][]byte{nil}, [][]byte{nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.v.Cursors = tt.cursors
			p, ok := compaction.Pick(tt.v, opts)
			if !ok {
				t.Fatal("Pick found nothing")
			}
			got := compaction.Apply(tt.v, p, nil)
			if !reflect.DeepEqual(got.Cursors, tt.want) {
				t.Fatalf("cursors = %q, want %q", got.Cursors, tt.want)
			}
			if !reflect.DeepEqual(tt.v.Cursors, tt.cursors) {
				t.Fatalf("Apply changed the input's cursors to %q", tt.v.Cursors)
			}
		})
	}
}

// deepCopy copies v down to the key bytes, so a test can tell whether a
// function changed anything reachable from v.
func deepCopy(v meta.Version) meta.Version {
	if v.Levels == nil {
		return v // Clone would turn nil levels into an empty slice
	}
	c := v.Clone()
	for _, l := range c.Levels {
		for i := range l {
			l[i].Smallest = bytes.Clone(l[i].Smallest)
			l[i].Largest = bytes.Clone(l[i].Largest)
		}
	}
	return c
}
