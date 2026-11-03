package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/nickstrad/systems-learnings/lsmtree"
)

// human formats a byte count, e.g. "512 B", "20.6 KiB", "3.1 MiB".
func human(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f, suffix := float64(n)/unit, "KiB"
	for _, s := range []string{"MiB", "GiB", "TiB"} {
		if f < unit {
			break
		}
		f, suffix = f/unit, s
	}
	return fmt.Sprintf("%.1f %s", f, suffix)
}

// shape is a one-line summary of the tree, e.g. "L0 2 | L1 5 | L2 1 tables".
func shape(s lsmtree.Stats) string {
	var parts []string
	for i, l := range s.Levels {
		if l.Files > 0 || i == 0 {
			parts = append(parts, fmt.Sprintf("L%d %d", i, l.Files))
		}
	}
	return strings.Join(parts, " | ") + " tables"
}

// tables is "1 table" or "n tables".
func tables(n int) string {
	if n == 1 {
		return "1 table"
	}
	return fmt.Sprintf("%d tables", n)
}

// bar draws how full a level is, e.g. "[######----]", capped at the right
// edge with a '+' when the level is over its target.
func bar(have, target int64) string {
	const width = 10
	if target <= 0 {
		return ""
	}
	n := int(have * width / target)
	if n > width {
		return "[" + strings.Repeat("#", width) + "]+"
	}
	return "[" + strings.Repeat("#", n) + strings.Repeat("-", width-n) + "]"
}

// fill is have as a share of target, e.g. "70%".
func fill(have, target int64) string {
	if target <= 0 {
		return ""
	}
	return fmt.Sprintf("%d%%", have*100/target)
}

// printStats prints each level's size against its target, then live data
// and the amplification figures that hold across runs. live and
// liveBytes are the live keys and their key+value bytes, from a scan.
func printStats(w io.Writer, s lsmtree.Stats, live, liveBytes int64) {
	const row = "%-5s %6v %10s %10s %5s\n"
	fmt.Fprintf(w, row, "level", "tables", "size", "target", "fill")
	var files int
	var size int64
	probes := 0 // the most tables one Get can read: every L0 table, one per deeper level
	for i, l := range s.Levels {
		files += l.Files
		size += l.Bytes
		target, full := human(l.Target), fill(l.Bytes, l.Target)
		switch {
		case i == 0:
			probes += l.Files
			target, full = fmt.Sprintf("%d tables", s.L0Trigger), fill(int64(l.Files), int64(s.L0Trigger))
		case l.Files > 0:
			probes++
		}
		fmt.Fprintf(w, row, fmt.Sprintf("L%d", i), l.Files, human(l.Bytes), target, full)
	}
	fmt.Fprintf(w, "%-5s %6d %10s\n\n", "total", files, human(size))

	fmt.Fprintf(w, "live data:           %d keys, %s of keys and values\n", live, human(liveBytes))
	if liveBytes > 0 {
		fmt.Fprintf(w, "space amplification: %.2fx (table bytes / live bytes)\n", float64(size)/float64(liveBytes))
	}
	fmt.Fprintf(w, "read amplification:  a get reads at most %d tables (%d in L0, one per deeper level)\n",
		probes, s.Levels[0].Files)
	fmt.Fprintln(w, "write amplification: counted per run; load prints it for the keys it writes")
}

// keyWidth is the most of a key that dump prints, to keep lines under 80
// columns.
const keyWidth = 20

// printDump prints the level tree: each level's tables with their key
// ranges, and for levels 1 and up the rolling-merge cursor and the table
// the next compaction of that level will take ("->").
func printDump(w io.Writer, s lsmtree.Stats) {
	last := 0  // the deepest level with tables
	width := 0 // the widest key shown, so the columns line up
	for i, l := range s.Levels {
		if l.Files > 0 {
			last = i
		}
		for _, t := range l.Tables {
			width = max(width, len([]rune(clip(show(t.Smallest)))), len([]rune(clip(show(t.Largest)))))
		}
	}
	for i, l := range s.Levels[:last+1] {
		if i == 0 {
			fmt.Fprintf(w, "L0  %s, %s  %s compacts at %d tables (oldest first)\n",
				tables(l.Files), human(l.Bytes), bar(int64(l.Files), int64(s.L0Trigger)), s.L0Trigger)
		} else {
			fmt.Fprintf(w, "L%d  %s, %s of %s  %s %s\n",
				i, tables(l.Files), human(l.Bytes), human(l.Target), bar(l.Bytes, l.Target), fill(l.Bytes, l.Target))
		}
		if l.Next >= 0 {
			cursor := "(start)"
			if l.Cursor != nil {
				cursor = clip(show(l.Cursor))
			}
			fmt.Fprintf(w, "    cursor %s; next merge into L%d takes #%d\n", cursor, i+1, l.Tables[l.Next].Num)
		}
		for j, t := range l.Tables {
			mark := "    "
			if j == l.Next {
				mark = "  ->"
			}
			fmt.Fprintf(w, "%s #%-5d %-*s .. %-*s %7d keys %10s\n", mark, t.Num,
				width, clip(show(t.Smallest)), width, clip(show(t.Largest)), t.Entries, human(t.Size))
		}
	}
	if last+1 < len(s.Levels) {
		if last+2 == len(s.Levels) {
			fmt.Fprintf(w, "L%d  empty\n", last+1)
		} else {
			fmt.Fprintf(w, "L%d..L%d  empty\n", last+1, len(s.Levels)-1)
		}
	}
}

// clip shortens s to keyWidth runes, marking the cut with "~".
func clip(s string) string {
	r := []rune(s)
	if len(r) <= keyWidth {
		return s
	}
	return string(r[:keyWidth-1]) + "~"
}
