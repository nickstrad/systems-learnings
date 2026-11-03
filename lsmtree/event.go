package lsmtree

import "time"

// EventKind says what an Event reports.
type EventKind string

const (
	EventFlush      EventKind = "flush"      // memtable written to one L0 table
	EventCompaction EventKind = "compaction" // tables merged from FromLevel into ToLevel
	EventRecovery   EventKind = "recovery"   // WAL replayed at Open
)

// Event describes one flush, compaction, or recovery. Its fields are
// flat and tagged for JSON, so the experiments can write events as JSON
// lines that DuckDB reads with read_json_auto.
type Event struct {
	Kind      EventKind     `json:"kind"`
	FromLevel int           `json:"from_level"` // -1 for a flush (the memtable) and for recovery
	ToLevel   int           `json:"to_level"`
	Duration  time.Duration `json:"duration_ns"`

	InputFiles   int   `json:"input_files"`   // tables read (0 for a flush)
	OutputFiles  int   `json:"output_files"`  // tables written
	BytesRead    int64 `json:"bytes_read"`    // table bytes read
	BytesWritten int64 `json:"bytes_written"` // table bytes written

	EntriesIn         int64 `json:"entries_in"`  // entries read, all versions
	EntriesOut        int64 `json:"entries_out"` // entries written
	TombstonesDropped int64 `json:"tombstones_dropped"`

	// NewEntries is how many entries came from the upper input (the
	// memtable, or the FromLevel tables), and OutputBlocks is how many data
	// blocks were written. NewEntries / OutputBlocks is the paper's batch
	// merge parameter M (Definition 3.2.1).
	NewEntries   int64 `json:"new_entries"`
	OutputBlocks int64 `json:"output_blocks"`

	// Recovery only.
	WALRecords      int   `json:"wal_records,omitempty"`
	WALDroppedBytes int64 `json:"wal_dropped_bytes,omitempty"`
}
