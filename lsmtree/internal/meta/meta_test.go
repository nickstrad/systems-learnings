package meta_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

func file(num uint64, smallest, largest string) meta.FileMeta {
	return meta.FileMeta{Num: num, Size: 10, Smallest: []byte(smallest), Largest: []byte(largest), MinSeq: 1, MaxSeq: 5, Entries: 2}
}

func TestOverlaps(t *testing.T) {
	f := file(1, "c", "f")
	cases := []struct {
		lo, hi string
		want   bool
	}{
		{"a", "b", false}, // entirely below
		{"a", "c", true},  // touches the smallest key
		{"d", "e", true},  // inside
		{"f", "z", true},  // touches the largest key
		{"g", "z", false}, // entirely above
		{"a", "z", true},  // covers the file
	}
	for _, c := range cases {
		if got := f.Overlaps([]byte(c.lo), []byte(c.hi)); got != c.want {
			t.Errorf("[c..f].Overlaps(%q, %q) = %v, want %v", c.lo, c.hi, got, c.want)
		}
	}
}

func TestValidate(t *testing.T) {
	valid := func() meta.Version {
		return meta.Version{
			Levels:      [][]meta.FileMeta{{file(3, "a", "z"), file(4, "b", "c")}, {file(1, "a", "b"), file(2, "c", "d")}},
			NextFileNum: 6, LastSeq: 9, LogNum: 5,
		}
	}
	cases := []struct {
		name    string
		break_  func(v *meta.Version)
		wantErr string // "" means valid
	}{
		{"valid", func(*meta.Version) {}, ""},
		{"empty version", func(v *meta.Version) { *v = meta.Version{} }, ""},
		{"L0 files may overlap", func(v *meta.Version) { v.Levels[0][1] = file(4, "a", "z") }, ""},
		{"L1 files may not share a boundary key", func(v *meta.Version) { v.Levels[1][1] = file(2, "b", "d") }, "overlap"},
		{"L1 out of order", func(v *meta.Version) { v.Levels[1][0], v.Levels[1][1] = v.Levels[1][1], v.Levels[1][0] }, "overlap or are out of order"},
		{"L0 out of order", func(v *meta.Version) { v.Levels[0][0], v.Levels[0][1] = v.Levels[0][1], v.Levels[0][0] }, "L0 files"},
		{"duplicate file number", func(v *meta.Version) { v.Levels[1][1].Num = 3 }, "appears twice"},
		{"smallest above largest", func(v *meta.Version) { v.Levels[1][0] = file(1, "b", "a") }, "above largest"},
		{"minSeq above maxSeq", func(v *meta.Version) { v.Levels[1][0].MinSeq = 6 }, "minSeq"},
		{"file number at NextFileNum", func(v *meta.Version) { v.Levels[1][0].Num = 6 }, "not below next file number"},
		{"maxSeq above LastSeq", func(v *meta.Version) { v.Levels[1][0].MaxSeq = 10 }, "above last seq"},
		{"maxSeq at LastSeq", func(v *meta.Version) { v.Levels[1][0].MaxSeq = 9 }, ""},
		{"log number at NextFileNum", func(v *meta.Version) { v.LogNum = 6 }, "log number"},
		{"negative size", func(v *meta.Version) { v.Levels[1][0].Size = -1 }, "negative"},
		{"one cursor per level", func(v *meta.Version) { v.Cursors = [][]byte{nil, []byte("b")} }, ""},
		{"fewer cursors than levels", func(v *meta.Version) { v.Cursors = [][]byte{nil} }, ""},
		{"more cursors than levels", func(v *meta.Version) { v.Cursors = [][]byte{nil, nil, []byte("b")} }, "cursors"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := valid()
			c.break_(&v)
			err := v.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("Validate = %v, want nil", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("Validate = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

func TestCheckInvariantTrips(t *testing.T) {
	v := meta.Version{Levels: [][]meta.FileMeta{{file(1, "a", "b"), file(1, "c", "d")}}, NextFileNum: 2, LastSeq: 5}
	asserttest.Panics(t, "appears twice", v.CheckInvariant)
}

func TestAddL0Trips(t *testing.T) {
	v := meta.Version{Levels: [][]meta.FileMeta{{file(5, "a", "b")}}, NextFileNum: 9, LastSeq: 5}
	asserttest.Panics(t, "L0 files", func() { v.AddL0(file(4, "a", "b")) })
}

func TestCloneIsIndependent(t *testing.T) {
	v := meta.Version{Levels: [][]meta.FileMeta{{file(1, "a", "b")}}, NextFileNum: 2, LastSeq: 5, Cursors: [][]byte{nil}}
	c := v.Clone()
	c.Levels[0] = append(c.Levels[0], file(9, "x", "y"))
	c.Levels[0][0].Num = 7
	c.Cursors[0] = []byte("m")
	if len(v.Levels[0]) != 1 || v.Levels[0][0].Num != 1 {
		t.Fatalf("changing the clone changed the original: %+v", v.Levels)
	}
	if v.Cursors[0] != nil {
		t.Fatalf("changing the clone's cursor changed the original: %q", v.Cursors)
	}
}

func TestAddL0(t *testing.T) {
	tests := []struct {
		name string
		v    meta.Version
		want []uint64
	}{
		{"no levels yet", meta.Version{NextFileNum: 10, LastSeq: 5}, []uint64{7}},
		{"empty L0", meta.Version{Levels: [][]meta.FileMeta{nil, {file(1, "a", "z")}}, NextFileNum: 10, LastSeq: 5}, []uint64{7}},
		{"appended as the newest L0 file", meta.Version{Levels: [][]meta.FileMeta{{file(2, "a", "z"), file(5, "a", "b")}}, NextFileNum: 10, LastSeq: 5}, []uint64{2, 5, 7}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := len(tt.v.Levels)
			got := tt.v.AddL0(file(7, "c", "d"))
			var l0 []uint64
			for _, f := range got.Levels[0] {
				l0 = append(l0, f.Num)
			}
			if !slices.Equal(l0, tt.want) {
				t.Fatalf("L0 = %v, want %v", l0, tt.want)
			}
			if len(tt.v.Levels) != before || (before > 0 && len(tt.v.Levels[0]) != len(tt.want)-1) {
				t.Fatal("AddL0 changed its input version")
			}
		})
	}
}

func TestFileNames(t *testing.T) {
	if got := meta.TableName(12); got != "000012.sst" {
		t.Errorf("TableName(12) = %q", got)
	}
	if got := meta.LogName(3); got != "000003.log" {
		t.Errorf("LogName(3) = %q", got)
	}
	if got := meta.TableName(1234567); got != "1234567.sst" {
		t.Errorf("TableName(1234567) = %q, want all seven digits", got)
	}

	tests := []struct {
		name string
		num  uint64
		kind meta.FileKind
		ok   bool
	}{
		{meta.TableName(0), 0, meta.KindTable, true},
		{meta.TableName(12), 12, meta.KindTable, true},
		{meta.LogName(12), 12, meta.KindLog, true},
		{"12.sst", 12, meta.KindTable, true}, // fewer than six digits still parses
		{"18446744073709551615.log", 1<<64 - 1, meta.KindLog, true},
		{"18446744073709551616.log", 0, 0, false}, // one over uint64
		{"MANIFEST", 0, 0, false},
		{"MANIFEST.tmp", 0, 0, false},
		{"LOCK", 0, 0, false},
		{"000012.tmp", 0, 0, false},
		{"000012", 0, 0, false},
		{".sst", 0, 0, false},
		{"abc.sst", 0, 0, false},
		{"-1.sst", 0, 0, false},
		{"000012.sst.tmp", 0, 0, false},
	}
	for _, tt := range tests {
		num, kind, ok := meta.ParseFileName(tt.name)
		if num != tt.num || kind != tt.kind || ok != tt.ok {
			t.Errorf("ParseFileName(%q) = %d, %v, %v; want %d, %v, %v", tt.name, num, kind, ok, tt.num, tt.kind, tt.ok)
		}
	}
}
