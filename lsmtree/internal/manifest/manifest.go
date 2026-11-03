// Package manifest persists a meta.Version: which SSTables are live, on
// which level, and the counters for new files and writes. It is the
// paper's checkpoint (§4.2): after a crash, recovery starts from the
// manifest and replays only the WAL it names.
//
// # Format
//
// The file is one line of compact JSON followed by one trailer line
// holding the CRC32-C of the JSON line:
//
//	{"nextFileNum":4,"lastSeq":17,"logNum":3,"levels":[[],[{"num":2,"size":4096,"smallest":"6170706c65","largest":"70656172",...}]],"cursors":["","70656172"]}
//	crc32c 1a2b3c4d
//
// Compact rather than indented JSON because the manifest is rewritten on
// every flush and compaction, and indenting was most of that cost in the
// experiments. It is still plain JSON: `head -n 1 MANIFEST | jq .`
// pretty-prints it.
//
// "cursors" holds each level's rolling-merge cursor (meta.Version.Cursors),
// with "" for none. It is left out when no level has a cursor yet, and a
// manifest written before cursors were persisted, which lacks the field,
// reads back with no cursors: every level's sweep starts from the
// smallest key.
//
// Keys and cursors are lowercase hex. Keys are arbitrary bytes, and a JSON string
// must be valid UTF-8, so raw keys cannot go in as they are. Go's default
// for []byte is base64, which hides even ASCII keys; hex at least shows
// key structure ("61" is 'a') and has one spelling per byte string, with
// no padding or escaping rules. lsmctl dump is the place to see keys as
// text.
//
// # Atomic replacement
//
// The whole file is rewritten on every change: write MANIFEST.tmp, sync
// it, rename it over MANIFEST, sync the directory. A crash at any point
// leaves either the old MANIFEST or the new one, never a mix; the CRC
// catches anything else.
//
// Rewriting the whole file costs O(live files) per flush or compaction.
// LevelDB instead appends small version edits ("add file 7 to L1, remove
// file 3") to a log and replays them at open, which keeps each change
// O(1). At this project's scale (tens to hundreds of files) the whole
// rewrite is cheap, and a single readable snapshot is the point.
package manifest

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/codec"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/meta"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/vfs"
)

// File names inside the store's directory. A leftover TmpName after a
// crash is harmless: the next Write replaces it, and the DB may delete
// it at open.
const (
	FileName = "MANIFEST"
	TmpName  = "MANIFEST.tmp"
)

// ErrCorrupt reports a manifest that fails its checksum, does not parse,
// or describes an impossible Version.
var ErrCorrupt = errors.New("manifest is corrupt")

const (
	trailerPrefix = "crc32c "
	trailerLen    = len(trailerPrefix) + 8 + 1 // prefix, 8 hex digits, newline
)

// fileJSON and versionJSON are the on-disk shapes of meta.FileMeta and
// meta.Version. They are separate types so the format does not change
// when a field is added to meta, and so keys can be hex.
type fileJSON struct {
	Num      uint64 `json:"num"`
	Size     int64  `json:"size"`
	Smallest string `json:"smallest"`
	Largest  string `json:"largest"`
	MinSeq   uint64 `json:"minSeq"`
	MaxSeq   uint64 `json:"maxSeq"`
	Entries  int64  `json:"entries"`
}

type versionJSON struct {
	NextFileNum uint64       `json:"nextFileNum"`
	LastSeq     uint64       `json:"lastSeq"`
	LogNum      uint64       `json:"logNum"`
	Levels      [][]fileJSON `json:"levels"`
	Cursors     []string     `json:"cursors,omitempty"`
}

func toJSON(f meta.FileMeta) fileJSON {
	return fileJSON{
		Num: f.Num, Size: f.Size,
		Smallest: hex.EncodeToString(f.Smallest), Largest: hex.EncodeToString(f.Largest),
		MinSeq: f.MinSeq, MaxSeq: f.MaxSeq, Entries: f.Entries,
	}
}

func fromJSON(f fileJSON) (meta.FileMeta, error) {
	smallest, err1 := hex.DecodeString(f.Smallest)
	largest, err2 := hex.DecodeString(f.Largest)
	if err := errors.Join(err1, err2); err != nil {
		return meta.FileMeta{}, fmt.Errorf("file %d: bad key: %v", f.Num, err)
	}
	return meta.FileMeta{
		Num: f.Num, Size: f.Size, Smallest: smallest, Largest: largest,
		MinSeq: f.MinSeq, MaxSeq: f.MaxSeq, Entries: f.Entries,
	}, nil
}

// Encode returns the manifest bytes for v.
//
//	pre:  v satisfies the Version invariant (asserted)
//	post: Decode(Encode(v)) returns v, with nil levels read back empty,
//	      empty cursors read back nil, and an empty Cursors list read
//	      back nil
func Encode(v meta.Version) []byte {
	v.CheckInvariant()
	out := versionJSON{NextFileNum: v.NextFileNum, LastSeq: v.LastSeq, LogNum: v.LogNum, Levels: make([][]fileJSON, len(v.Levels))}
	for i, files := range v.Levels {
		out.Levels[i] = make([]fileJSON, 0, len(files)) // [] rather than null
		for _, f := range files {
			out.Levels[i] = append(out.Levels[i], toJSON(f))
		}
	}
	for _, c := range v.Cursors {
		out.Cursors = append(out.Cursors, hex.EncodeToString(c))
	}
	body, err := json.Marshal(out)
	if err != nil {
		panic(fmt.Sprintf("manifest: cannot marshal plain structs: %v", err)) // unreachable
	}
	return seal(append(body, '\n'))
}

// seal appends the CRC trailer line to body.
func seal(body []byte) []byte {
	return fmt.Appendf(body, "%s%08x\n", trailerPrefix, codec.Checksum(body))
}

// Decode parses manifest bytes. The bytes come from disk, so they are
// untrusted: every problem is an error wrapping ErrCorrupt, never a
// panic.
//
//	post: err == nil ⇒ the result satisfies the Version invariant
func Decode(b []byte) (meta.Version, error) {
	if len(b) < trailerLen {
		return meta.Version{}, fmt.Errorf("%w: %d bytes is too short to hold the checksum line", ErrCorrupt, len(b))
	}
	body, trailer := b[:len(b)-trailerLen], b[len(b)-trailerLen:]
	if !bytes.HasPrefix(trailer, []byte(trailerPrefix)) || trailer[trailerLen-1] != '\n' {
		return meta.Version{}, fmt.Errorf("%w: last line is not a checksum line: %q", ErrCorrupt, trailer)
	}
	want, err := strconv.ParseUint(string(trailer[len(trailerPrefix):trailerLen-1]), 16, 32)
	if err != nil {
		return meta.Version{}, fmt.Errorf("%w: bad checksum %q", ErrCorrupt, trailer)
	}
	if got := codec.Checksum(body); got != uint32(want) {
		return meta.Version{}, fmt.Errorf("%w: checksum is %08x, file says %08x", ErrCorrupt, got, want)
	}

	var in versionJSON
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return meta.Version{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return meta.Version{}, fmt.Errorf("%w: data after the JSON object", ErrCorrupt)
	}

	v := meta.Version{NextFileNum: in.NextFileNum, LastSeq: in.LastSeq, LogNum: in.LogNum, Levels: make([][]meta.FileMeta, len(in.Levels))}
	for i, files := range in.Levels {
		v.Levels[i] = make([]meta.FileMeta, 0, len(files))
		for _, jf := range files {
			f, err := fromJSON(jf)
			if err != nil {
				return meta.Version{}, fmt.Errorf("%w: L%d %v", ErrCorrupt, i, err)
			}
			v.Levels[i] = append(v.Levels[i], f)
		}
	}
	for i, h := range in.Cursors {
		c, err := hex.DecodeString(h)
		if err != nil {
			return meta.Version{}, fmt.Errorf("%w: L%d cursor: %v", ErrCorrupt, i, err)
		}
		if len(c) == 0 {
			c = nil // "" is "no cursor"
		}
		v.Cursors = append(v.Cursors, c)
	}
	if err := v.Validate(); err != nil {
		return meta.Version{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return v, nil
}

// Write replaces the manifest in dir with v, atomically: it writes
// TmpName, syncs it, renames it over FileName, then syncs dir.
//
// On error, the message says which step failed. Before the rename, the
// old manifest is untouched. If only the final directory sync fails, the
// new manifest is in place but may not survive a crash: the caller must
// treat the outcome as unknown (old or new) until a later Write succeeds.
//
//	pre:  v satisfies the Version invariant (asserted); dir exists
//	post: err == nil ⇒ Read(fsys, dir) returns v, even after a crash
func Write(fsys vfs.FS, dir string, v meta.Version) error {
	tmp := filepath.Join(dir, TmpName)
	if err := writeSynced(fsys, tmp, Encode(v)); err != nil {
		return fmt.Errorf("manifest: old manifest unchanged: %w", err)
	}
	if err := fsys.Rename(tmp, filepath.Join(dir, FileName)); err != nil {
		return fmt.Errorf("manifest: old manifest unchanged: rename %s: %w", tmp, err)
	}
	if err := fsys.SyncDir(dir); err != nil {
		return fmt.Errorf("manifest: sync dir %s: new manifest in place but may not survive a crash: %w", dir, err)
	}
	return nil
}

// writeSynced creates name, writes b to it, syncs it, and closes it. The
// error names the step that failed.
func writeSynced(fsys vfs.FS, name string, b []byte) error {
	f, err := fsys.Create(name)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close() // the write error is the one worth reporting
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}

// Read loads the manifest from dir. A missing manifest (a new store)
// gives an error for which errors.Is(err, fs.ErrNotExist) is true; a
// damaged one gives an error wrapping ErrCorrupt.
func Read(fsys vfs.FS, dir string) (meta.Version, error) {
	name := filepath.Join(dir, FileName)
	f, err := fsys.Open(name)
	if err != nil {
		return meta.Version{}, fmt.Errorf("manifest: open %s: %w", name, err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return meta.Version{}, fmt.Errorf("manifest: read %s: %w", name, err)
	}
	v, err := Decode(b)
	if err != nil {
		return meta.Version{}, fmt.Errorf("manifest: %s: %w", name, err)
	}
	return v, nil
}
