package memberfile

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
)

// FuzzMemberfileLoad writes arbitrary bytes as the membership file. Load
// must never panic. It either returns a well-formed memberfile error that
// names the path, or a valid membership; and a loaded membership survives
// Save then Load with the same history and nodes.
func FuzzMemberfileLoad(f *testing.F) {
	f.Add([]byte(`{"history":[]}`))
	f.Add([]byte(`{"history":[{"op":"add","node":"a"},{"op":"add","node":"b"},{"op":"remove","node":"a"}]}`))
	f.Add([]byte(`{"history":[{"op":"remove","node":"ghost"}]}`))
	f.Add([]byte(`{"history":[{"op":"add","node":""}]}`))
	f.Add([]byte(`{"history":[{"op":"rename","node":"a"}]}`))
	f.Add([]byte(`{"history":[{"op":"add","node":"\xff"}]}`))
	f.Add([]byte(`{"history":null}`))
	f.Add([]byte(`null`))
	f.Add([]byte(`{not json`))
	f.Add([]byte{})

	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		file := File{Path: filepath.Join(dir, "cluster.json")}
		if err := os.WriteFile(file.Path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		m, err := file.Load()
		if err != nil {
			if e, ok := errs.Outermost(err); !ok || e.Module != module || e.Misc["path"] != file.Path {
				t.Fatalf("Load error is not a well-formed memberfile error: %s", errs.Describe(err))
			}
			return
		}
		if !m.Valid() {
			t.Fatal("Load returned an invalid membership without an error")
		}

		if err := file.Save(m); err != nil {
			t.Fatalf("Save of a loaded membership failed: %v", err)
		}
		again, err := file.Load()
		if err != nil {
			t.Fatalf("reloading a saved membership failed: %v", err)
		}
		if !slices.Equal(again.History(), m.History()) || !slices.Equal(again.Nodes(), m.Nodes()) {
			t.Fatalf("save then load changed membership:\n got %v %v\nwant %v %v", again.History(), again.Nodes(), m.History(), m.Nodes())
		}
	})
}
