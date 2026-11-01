package memberfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

func TestFileStoreContract(t *testing.T) {
	clustertest.StoreContract(t, func(t *testing.T) cluster.MembershipStore {
		return File{Path: filepath.Join(t.TempDir(), "cluster.json")}
	})
}

// The on-disk format is what humans read and what older files use.
func TestFileFormat(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "cluster.json")}
	m, _ := cluster.NewMembership([]cluster.Op{
		{Kind: cluster.OpAdd, Node: "a"},
		{Kind: cluster.OpRemove, Node: "a"},
	})
	if err := f.Save(m); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Path)
	if !strings.Contains(string(b), `"op": "remove"`) || !strings.Contains(string(b), `"history"`) {
		t.Fatalf("unexpected file format:\n%s", b)
	}
}

func TestCorruptFileIsAnError(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "cluster.json")}
	os.WriteFile(f.Path, []byte("{not json"), 0o644)
	if _, err := f.Load(); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestBadHistoryIsRejected(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "cluster.json")}
	os.WriteFile(f.Path, []byte(`{"history":[{"op":"remove","node":"ghost"}]}`), 0o644)
	if _, err := f.Load(); err == nil {
		t.Fatal("expected error replaying a remove of an unknown node")
	}
}

func TestErrorsAreWellFormed(t *testing.T) {
	f := File{Path: filepath.Join(t.TempDir(), "cluster.json")}
	os.WriteFile(f.Path, []byte("{not json"), 0o644)
	_, err := f.Load()
	e, ok := errs.Outermost(err)
	if !ok || e.Module != module || e.Misc["path"] != f.Path {
		t.Fatalf("got %s", errs.Describe(err))
	}

	dir := File{Path: filepath.Join(t.TempDir(), "missing-dir", "cluster.json")}
	m, _ := cluster.NewMembership(nil)
	if e, ok := errs.Outermost(dir.Save(m)); !ok || e.Module != module {
		t.Fatalf("save error not well-formed: %v", e)
	}
}

// A path that can't be read or replaced (here, a directory) gives a
// well-formed error that names the path.
func TestUnusablePathIsAnError(t *testing.T) {
	f := File{Path: t.TempDir()}
	m, _ := cluster.NewMembership(nil)
	_, loadErr := f.Load()
	saveErr := f.Save(m)
	for name, err := range map[string]error{"load": loadErr, "save": saveErr} {
		e, ok := errs.Outermost(err)
		if !ok || e.Module != module || e.Misc["path"] == nil {
			t.Errorf("%s: got %s", name, errs.Describe(err))
		}
	}
}
