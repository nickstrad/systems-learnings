package lsmtree

import (
	"fmt"
	"math"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/osfs"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// Options configure a DB. The zero value is valid: every zero field takes
// the default listed below. The defaults are small on purpose, so that
// flushes and compactions happen in tests and demos with little data.
type Options struct {
	// MemtableSize is the approximate memtable size, in bytes, that
	// triggers a flush to L0. The paper's C0 threshold. Default 64 KiB.
	MemtableSize int64

	// BlockSize is the target size of an SSTable data block: the unit of
	// I/O, like the paper's page. Default 4 KiB.
	BlockSize int

	// BloomBitsPerKey sizes each table's Bloom filter. Default 10 (about
	// a 1% false-positive rate). Set NoBloom to turn filters off.
	BloomBitsPerKey int
	NoBloom         bool

	// L0CompactionTrigger is the number of L0 files that triggers an L0
	// to L1 compaction. Default 4.
	L0CompactionTrigger int

	// BaseLevelSize is the target size of L1, in bytes. Default 256 KiB.
	BaseLevelSize int64

	// LevelSizeRatio is r: each level's target is r times the one above.
	// Default 10.
	LevelSizeRatio int

	// MaxLevels is the number of levels, L0 included. Default 7.
	MaxLevels int

	// LevelTargets, if set, gives the target size of each level directly
	// and replaces BaseLevelSize, LevelSizeRatio, and MaxLevels.
	// LevelTargets[0] is ignored (L0 is sized by file count); the number
	// of levels is len(LevelTargets). Used to test Theorem 3.1.
	LevelTargets []int64

	// TableSize is the target size of one SSTable written by compaction.
	// Default 64 KiB.
	TableSize int64

	// SyncWrites makes every Put and Delete fsync the WAL before
	// returning. Without it, a crash can lose recent writes (but never
	// an older write while keeping a newer one).
	SyncWrites bool

	// FS is the filesystem. Default: the real one (osfs).
	FS vfs.FS

	// Events, if set, is called after each flush, compaction, and WAL
	// recovery, with the DB lock held. It must not call back into the
	// DB. The store reports; the caller decides whether to log.
	Events func(Event)
}

// withDefaults fills zero fields and checks the rest. It returns an error
// for settings that cannot work, such as a ratio below 2.
func (o Options) withDefaults() (Options, error) {
	setDefault(&o.MemtableSize, 64<<10)
	setDefault(&o.BlockSize, 4<<10)
	setDefault(&o.BloomBitsPerKey, 10)
	setDefault(&o.L0CompactionTrigger, 4)
	setDefault(&o.BaseLevelSize, 256<<10)
	setDefault(&o.LevelSizeRatio, 10)
	setDefault(&o.MaxLevels, 7)
	setDefault(&o.TableSize, 64<<10)
	if o.FS == nil {
		o.FS = osfs.FS{}
	}
	if o.NoBloom {
		o.BloomBitsPerKey = 0
	}

	switch {
	case o.MemtableSize < 0, o.BlockSize < 0, o.BloomBitsPerKey < 0, o.L0CompactionTrigger < 0,
		o.BaseLevelSize < 0, o.TableSize < 0:
		return o, fmt.Errorf("sizes and counts must not be negative")
	case o.LevelSizeRatio < 2:
		return o, fmt.Errorf("LevelSizeRatio is %d; it must be at least 2", o.LevelSizeRatio)
	case o.LevelTargets == nil && o.MaxLevels < 2:
		return o, fmt.Errorf("MaxLevels is %d; it must be at least 2 (L0 and L1)", o.MaxLevels)
	case o.LevelTargets != nil && len(o.LevelTargets) < 2:
		return o, fmt.Errorf("LevelTargets has %d entries; it needs at least 2 (L0 and L1)", len(o.LevelTargets))
	}

	if o.LevelTargets == nil {
		o.LevelTargets = make([]int64, o.MaxLevels)
		target := o.BaseLevelSize
		for i := 1; i < o.MaxLevels; i++ {
			if target <= 0 || target > math.MaxInt64/int64(o.LevelSizeRatio) && i+1 < o.MaxLevels {
				return o, fmt.Errorf("BaseLevelSize %d × LevelSizeRatio %d over %d levels overflows; use fewer levels",
					o.BaseLevelSize, o.LevelSizeRatio, o.MaxLevels)
			}
			o.LevelTargets[i] = target
			target *= int64(o.LevelSizeRatio)
		}
	}
	for i, t := range o.LevelTargets[1:] {
		if t <= 0 {
			return o, fmt.Errorf("LevelTargets[%d] is %d; level targets must be positive", i+1, t)
		}
	}
	o.MaxLevels = len(o.LevelTargets)
	return o, nil
}

func setDefault[T int | int64](field *T, def T) {
	if *field == 0 {
		*field = def
	}
}
