// Package compaction decides what to merge next. It is a pure planner:
// a Version (which carries the per-level cursors) and the options go in;
// a Plan comes out. It reads no files and writes none, so every rule here is tested
// with plain structs. This is the project's version of jumphash's
// PlanMoves.
//
// # The paper's rolling merge
//
// O'Neil et al. (§2) picture the merge between components Ci and Ci+1 as
// a cursor that circulates through the key space: it takes the next
// multi-page block of Ci, merges it into the matching leaves of Ci+1,
// moves on, and wraps back to the smallest key when it reaches the end.
// Leveled compaction does the same with files instead of blocks. Each
// level i >= 1 has a cursor: the largest key of the last file it
// compacted, kept in meta.Version.Cursors. Pick takes the first file that
// starts after the cursor (wrapping to the first file), merges it with
// every overlapping file one level down, and Apply moves the cursor to
// that file's largest key. Over many compactions the cursor sweeps the
// whole level, as in Figure 2.2. Because the cursor lives in the Version,
// the manifest write that commits a compaction's files also commits its
// cursor, and a reopened store resumes the sweep where it stopped.
//
// # Level sizes
//
// Theorem 3.1 says the merge I/O is lowest when every component is a
// constant ratio r larger than the one above it. Options.LevelTargets
// holds those sizes; the DB fills them from BaseLevelSize·r^(i-1), or
// from explicit targets when an experiment wants to break the rule.
package compaction

import (
	"bytes"
	"slices"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/shared/assert"
)

// Options says when a level needs compacting.
//
// Invariant (checked by Pick, since the DB validates user options first):
//   - L0Trigger >= 1
//   - len(LevelTargets) >= 2: there is at least L0 and L1
//   - LevelTargets[i] > 0 for every i >= 1
type Options struct {
	// L0Trigger is the number of L0 files at which L0 is merged into L1.
	L0Trigger int
	// LevelTargets[i] is the target size in bytes of level i, for i >= 1.
	// Index 0 is unused: L0 is sized by file count. len(LevelTargets) is
	// the number of levels, so the last level is len(LevelTargets)-1.
	LevelTargets []int64
}

func (o Options) check() {
	assert.That(o.L0Trigger >= 1, "compaction: L0 trigger %d is below 1", o.L0Trigger)
	assert.That(len(o.LevelTargets) >= 2, "compaction: %d levels, need at least L0 and L1", len(o.LevelTargets))
	for i := 1; i < len(o.LevelTargets); i++ {
		assert.That(o.LevelTargets[i] > 0, "compaction: L%d target %d is not positive", i, o.LevelTargets[i])
	}
}

// Plan is one compaction: merge Inputs (from Level) with Overlaps (from
// Level+1) and write the result to Level+1.
//
// Invariant (checkPlan):
//   - Inputs is non-empty and every input is on Level
//   - Overlaps is exactly the files on Level+1 that overlap the key range
//     spanned by Inputs, in level order
//   - Bottom is true exactly when no file deeper than Level+1 overlaps
//     the key range spanned by Inputs and Overlaps together
type Plan struct {
	Level    int             // the level being compacted; outputs go to Level+1
	Inputs   []meta.FileMeta // files from Level: all of L0, or one file from Li
	Overlaps []meta.FileMeta // files from Level+1 that overlap Inputs' key range
	// Bottom reports that no deeper level holds any key in the merge's
	// range, so a tombstone has nothing left to hide and may be dropped
	// (the paper's delete node entry, §2.3).
	Bottom bool
	// Cursor is the new rolling-merge cursor for Level, which Apply
	// stores in the Version: the largest key of Inputs. It is nil for an
	// L0 plan, which takes every L0 file and so has no cursor.
	Cursor []byte
}

// Pick chooses the next compaction, if any level needs one. It starts
// each level's file choice from that level's cursor in v.Cursors.
//
// Each level gets a score:
//   - L0: len(L0) / L0Trigger. It needs compacting at len(L0) >= L0Trigger.
//   - Li: size / LevelTargets[i]. It needs compacting only when it is
//     over target (size > target); a level exactly at its target is
//     within it, so Compact can stop there.
//
// Pick takes the level with the highest score among those that need
// compacting; on a tie, the shallower level wins, since it pushes data
// down toward the levels that are about to need room.
//
// The last level never compacts: there is nowhere to merge it into, so
// it may grow past its target. That is the paper's largest component CK,
// whose size is set by the data, not by the tree.
//
//	pre:  v satisfies its invariant; opts satisfies its invariant;
//	      len(v.Levels) <= len(opts.LevelTargets)
//	post: ok ⇒ p satisfies the Plan invariant against v
//	post: !ok ⇒ no level needs compacting
func Pick(v meta.Version, opts Options) (p Plan, ok bool) {
	opts.check()
	assert.That(len(v.Levels) <= len(opts.LevelTargets),
		"compaction: version has %d levels but options allow %d", len(v.Levels), len(opts.LevelTargets))

	level, ok := pickLevel(v, opts)
	if !ok {
		return Plan{}, false
	}
	if level == 0 {
		p = plan(v, 0, slices.Clone(v.Levels[0]))
	} else {
		var cursor []byte
		if level < len(v.Cursors) {
			cursor = v.Cursors[level]
		}
		in := v.Levels[level][NextInput(v.Levels[level], cursor)]
		p = plan(v, level, []meta.FileMeta{in})
		p.Cursor = in.Largest
	}
	if assert.Enabled {
		checkPlan(v, p)
	}
	return p, true
}

// pickLevel returns the level with the highest score >= 1, preferring
// the shallower level on a tie. ok is false if no level needs compacting.
func pickLevel(v meta.Version, opts Options) (level int, ok bool) {
	best := 0.0
	last := len(opts.LevelTargets) - 1
	for i := 0; i < last && i < len(v.Levels); i++ {
		var score float64
		var needs bool
		if i == 0 {
			n := len(v.Levels[0])
			score, needs = float64(n)/float64(opts.L0Trigger), n >= opts.L0Trigger
		} else {
			size, target := v.LevelSize(i), opts.LevelTargets[i]
			score, needs = float64(size)/float64(target), size > target
		}
		if needs && (!ok || score > best) {
			level, best, ok = i, score, true
		}
	}
	return level, ok
}

// NextInput is the rolling-merge rule: given a level's files in key order
// and its cursor, it returns the index of the file the level's next
// compaction takes. That is the first file whose smallest key is after
// cursor, wrapping to 0 when the cursor is past every file (a nil cursor
// starts at file 0). Pick uses it, and the DB's Stats uses it to show
// which table comes next.
//
//	pre:  len(files) > 0
//	post: 0 <= result < len(files)
func NextInput(files []meta.FileMeta, cursor []byte) int {
	for i, f := range files {
		if bytes.Compare(f.Smallest, cursor) > 0 {
			return i
		}
	}
	return 0
}

// plan builds the Plan that merges inputs from level into level+1.
func plan(v meta.Version, level int, inputs []meta.FileMeta) Plan {
	p := Plan{Level: level, Inputs: inputs}
	lo, hi := span(inputs)
	if level+1 < len(v.Levels) {
		p.Overlaps = overlapping(v.Levels[level+1], lo, hi)
	}
	lo, hi = span(inputs, p.Overlaps)
	p.Bottom = true
	for d := level + 2; d < len(v.Levels); d++ {
		if len(overlapping(v.Levels[d], lo, hi)) > 0 {
			p.Bottom = false
			break
		}
	}
	return p
}

// span returns the smallest and largest keys across every file in
// groups.
//
//	pre: groups[0] is non-empty
func span(groups ...[]meta.FileMeta) (lo, hi []byte) {
	lo, hi = groups[0][0].Smallest, groups[0][0].Largest
	for _, files := range groups {
		for _, f := range files {
			lo, hi = minKey(lo, f.Smallest), maxKey(hi, f.Largest)
		}
	}
	return lo, hi
}

// overlapping returns the files whose range meets [lo, hi], in order.
func overlapping(files []meta.FileMeta, lo, hi []byte) []meta.FileMeta {
	var out []meta.FileMeta
	for _, f := range files {
		if f.Overlaps(lo, hi) {
			out = append(out, f)
		}
	}
	return out
}

// checkPlan asserts the Plan invariant against v. It recomputes every
// set by brute force rather than reusing plan's helpers, so a bug in one
// is not hidden by the same bug in the other.
func checkPlan(v meta.Version, p Plan) {
	assert.That(len(p.Inputs) > 0, "compaction: plan for L%d has no inputs", p.Level)
	assert.That(p.Level+1 < len(v.Levels) || len(p.Overlaps) == 0,
		"compaction: plan for L%d has overlaps but there is no L%d", p.Level, p.Level+1)
	inLevel := map[uint64]bool{}
	for _, f := range v.Levels[p.Level] {
		inLevel[f.Num] = true
	}
	lo, hi := p.Inputs[0].Smallest, p.Inputs[0].Largest
	for _, f := range p.Inputs {
		assert.That(inLevel[f.Num], "compaction: input %d is not on L%d", f.Num, p.Level)
		lo, hi = minKey(lo, f.Smallest), maxKey(hi, f.Largest)
	}
	var below []meta.FileMeta
	if p.Level+1 < len(v.Levels) {
		below = v.Levels[p.Level+1]
	}
	j := 0
	for _, f := range below {
		if !meets(f, lo, hi) {
			continue
		}
		assert.That(j < len(p.Overlaps) && p.Overlaps[j].Num == f.Num,
			"compaction: L%d file %d overlaps the inputs but is not in the plan's overlaps", p.Level+1, f.Num)
		j++
	}
	assert.That(j == len(p.Overlaps), "compaction: plan has %d overlaps, only %d L%d files overlap",
		len(p.Overlaps), j, p.Level+1)
	for _, f := range p.Overlaps {
		lo, hi = minKey(lo, f.Smallest), maxKey(hi, f.Largest)
	}
	bottom := true
	for d := p.Level + 2; d < len(v.Levels); d++ {
		for _, f := range v.Levels[d] {
			if meets(f, lo, hi) {
				bottom = false
			}
		}
	}
	assert.That(p.Bottom == bottom, "compaction: plan for L%d says bottom=%v, but it is %v", p.Level, p.Bottom, bottom)
}

// meets reports whether f's range meets [lo, hi]. checkPlan spells the
// test out instead of calling meta.FileMeta.Overlaps, which plan uses, so
// the brute-force check stays independent of the code it checks.
func meets(f meta.FileMeta, lo, hi []byte) bool {
	return bytes.Compare(f.Smallest, hi) <= 0 && bytes.Compare(lo, f.Largest) <= 0
}

func minKey(a, b []byte) []byte {
	if bytes.Compare(b, a) < 0 {
		return b
	}
	return a
}

func maxKey(a, b []byte) []byte {
	if bytes.Compare(b, a) > 0 {
		return b
	}
	return a
}

// Apply returns the Version after p ran: p's inputs and overlaps are
// gone, outputs sit on Level+1 in key order, and for a plan below L0 the
// Level's cursor is p.Cursor. It never changes v. Since the cursor moves
// in the same Version as the files, one manifest write commits both.
// NextFileNum and LastSeq are left alone: the DB hands out output file
// numbers from v.NextFileNum and must advance it before calling Apply.
//
//	pre:  p came from Pick(v, ...); outputs are sorted, non-overlapping
//	      files numbered below v.NextFileNum, whose keys lie inside the
//	      span of p.Inputs and p.Overlaps (outputs may be empty, when
//	      every entry was a dropped tombstone)
//	post: the result satisfies the Version invariant (asserted)
//	post: every file not in p.Inputs or p.Overlaps is still on its level
//	post: p.Level > 0 ⇒ result.Cursors[p.Level] is p.Cursor; every other
//	      cursor is unchanged
func Apply(v meta.Version, p Plan, outputs []meta.FileMeta) meta.Version {
	n := v.Clone()
	for len(n.Levels) <= p.Level+1 {
		n.Levels = append(n.Levels, nil)
	}
	n.Levels[p.Level] = without(n.Levels[p.Level], p.Inputs, p.Level)
	below := append(without(n.Levels[p.Level+1], p.Overlaps, p.Level+1), outputs...)
	slices.SortStableFunc(below, func(a, b meta.FileMeta) int { return bytes.Compare(a.Smallest, b.Smallest) })
	n.Levels[p.Level+1] = below
	if p.Level > 0 {
		for len(n.Cursors) <= p.Level {
			n.Cursors = append(n.Cursors, nil)
		}
		n.Cursors[p.Level] = p.Cursor
	}
	n.CheckInvariant()
	return n
}

// without returns files minus drop, matching by file number. Every file
// in drop must be present: a plan naming a file that is not there is a
// bug in the caller. files is modified in place, so pass a copy.
func without(files, drop []meta.FileMeta, level int) []meta.FileMeta {
	gone := make(map[uint64]bool, len(drop))
	for _, f := range drop {
		gone[f.Num] = true
	}
	kept := slices.DeleteFunc(files, func(f meta.FileMeta) bool { return gone[f.Num] })
	assert.That(len(files)-len(kept) == len(drop),
		"compaction: plan removes %d files from L%d but only %d are there", len(drop), level, len(files)-len(kept))
	return kept
}
