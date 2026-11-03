package compaction_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/compaction"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

// TestAssertionsTrip shows each contract in the planner can fail: a plan
// or an Apply that breaks a rule halts instead of corrupting the version.
func TestAssertionsTrip(t *testing.T) {
	v := version(
		nil,
		[]meta.FileMeta{file(1, 200, "d", "f")},
		[]meta.FileMeta{file(10, 1, "a", "b"), file(11, 1, "e", "k")},
		[]meta.FileMeta{file(20, 1, "j", "z")},
	)
	good, ok := compaction.Pick(v, opts)
	if !ok {
		t.Fatal("Pick found nothing")
	}
	compaction.CheckPlan(v, good) // the real plan passes

	tests := []struct {
		name string
		want string // a substring of the assertion message
		fn   func()
	}{
		{"plan misses an overlapping file", "is not in the plan's overlaps", func() {
			p := good
			p.Overlaps = nil
			compaction.CheckPlan(v, p)
		}},
		{"plan includes a file that does not overlap", "overlaps, only", func() {
			p := good
			p.Overlaps = append(p.Overlaps, v.Levels[2][0])
			compaction.CheckPlan(v, p)
		}},
		{"plan says bottom when a deeper level overlaps", "says bottom", func() {
			p := good
			p.Bottom = true
			compaction.CheckPlan(v, p)
		}},
		{"plan with no inputs", "has no inputs", func() {
			p := good
			p.Inputs = nil
			compaction.CheckPlan(v, p)
		}},
		{"plan input not on its level", "is not on L1", func() {
			p := good
			p.Inputs = []meta.FileMeta{file(99, 1, "d", "f")}
			compaction.CheckPlan(v, p)
		}},
		{"Apply removes a file that is not there", "but only 0 are there", func() {
			p := good
			p.Inputs = []meta.FileMeta{file(99, 1, "d", "f")}
			compaction.Apply(v, p, nil)
		}},
		{"Apply output overlaps a file it did not replace", "overlap or are out of order", func() {
			compaction.Apply(v, good, []meta.FileMeta{file(30, 1, "a", "k")})
		}},
		{"Pick with an L0 trigger of zero", "L0 trigger 0", func() {
			compaction.Pick(v, compaction.Options{L0Trigger: 0, LevelTargets: []int64{0, 100, 1000, 10000}})
		}},
		{"Pick with only one level", "need at least L0 and L1", func() {
			compaction.Pick(meta.Version{}, compaction.Options{L0Trigger: 1, LevelTargets: []int64{0}})
		}},
		{"Pick with a zero level target", "L1 target 0 is not positive", func() {
			compaction.Pick(v, compaction.Options{L0Trigger: 1, LevelTargets: []int64{0, 0, 1000, 10000}})
		}},
		{"Pick with more levels in the version than the options", "but options allow", func() {
			compaction.Pick(v, compaction.Options{L0Trigger: 1, LevelTargets: []int64{0, 100}})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { asserttest.Panics(t, tt.want, tt.fn) })
	}
}
