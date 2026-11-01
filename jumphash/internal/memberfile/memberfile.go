// Package memberfile saves cluster membership as a JSON file. It
// implements cluster.MembershipStore.
package memberfile

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/nickstrad/systems-learnings/jumphash/internal/assert"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

// File is a membership file on disk. A missing file means an empty cluster.
type File struct{ Path string }

// On-disk format. It is kept separate from cluster.Op, so the domain type
// carries no JSON tags.
type fileFormat struct {
	History []fileOp `json:"history"`
}

type fileOp struct {
	Op   string `json:"op"`
	Node string `json:"node"`
}

// module is this package's name in well-formed errors.
const module = "memberfile"

// fail builds this package's well-formed error, recording the file path.
func fail(path string, err error, format string, args ...any) *errs.Error {
	return errs.Wrap(module, err, format, args...).With("path", path)
}

// Load reads the file. Every error it returns is a well-formed memberfile
// error that names the path.
func (f File) Load() (cluster.Membership, error) {
	b, err := os.ReadFile(f.Path)
	if errors.Is(err, os.ErrNotExist) {
		return cluster.NewMembership(nil)
	}
	if err != nil {
		return cluster.Membership{}, fail(f.Path, err, "cannot read membership file %s", f.Path)
	}
	var ff fileFormat
	if err := json.Unmarshal(b, &ff); err != nil {
		return cluster.Membership{}, fail(f.Path, err, "membership file %s is corrupt (not valid JSON)", f.Path)
	}
	history := make([]cluster.Op, len(ff.History))
	for i, op := range ff.History {
		history[i] = cluster.Op{Kind: op.Op, Node: op.Node}
	}
	m, err := cluster.NewMembership(history)
	if err != nil {
		return cluster.Membership{}, fail(f.Path, err, "membership file %s has an impossible history: %v", f.Path, err)
	}
	return m, nil
}

// Save writes the file atomically. Every error it returns is a well-formed
// memberfile error that names the path.
func (f File) Save(m cluster.Membership) error {
	var ff fileFormat
	for _, op := range m.History() {
		ff.History = append(ff.History, fileOp{Op: op.Kind, Node: op.Node})
	}
	b, err := json.MarshalIndent(ff, "", "  ")
	assert.That(err == nil, "memberfile: encoding strings failed: %v", err) // plain strings always encode
	// Write then rename so a crash never leaves a half-written file.
	tmp := f.Path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fail(tmp, err, "cannot write membership file %s", tmp)
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		return fail(f.Path, err, "cannot replace membership file %s", f.Path)
	}
	return nil
}
