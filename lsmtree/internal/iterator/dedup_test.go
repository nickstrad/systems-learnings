package iterator_test

import (
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/iterator/itertest"
	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

func TestDedup(t *testing.T) {
	cases := []struct {
		name     string
		in       []keys.Entry
		keepTomb []keys.Entry // want with dropTombstones = false
		dropTomb []keys.Entry // want with dropTombstones = true
	}{
		{
			name: "empty",
		},
		{
			name:     "one put",
			in:       []keys.Entry{itertest.Put("a", 1, "v")},
			keepTomb: []keys.Entry{itertest.Put("a", 1, "v")},
			dropTomb: []keys.Entry{itertest.Put("a", 1, "v")},
		},
		{
			name:     "many versions, newest wins",
			in:       sorted(itertest.Put("a", 1, "v1"), itertest.Put("a", 5, "v5"), itertest.Put("a", 3, "v3"), itertest.Put("b", 2, "b")),
			keepTomb: []keys.Entry{itertest.Put("a", 5, "v5"), itertest.Put("b", 2, "b")},
			dropTomb: []keys.Entry{itertest.Put("a", 5, "v5"), itertest.Put("b", 2, "b")},
		},
		{
			name:     "newest is a delete",
			in:       sorted(itertest.Put("a", 1, "old"), itertest.Del("a", 2), itertest.Put("b", 3, "b")),
			keepTomb: []keys.Entry{itertest.Del("a", 2), itertest.Put("b", 3, "b")},
			dropTomb: []keys.Entry{itertest.Put("b", 3, "b")},
		},
		{
			name:     "put after delete shows",
			in:       sorted(itertest.Put("a", 1, "old"), itertest.Del("a", 2), itertest.Put("a", 3, "new")),
			keepTomb: []keys.Entry{itertest.Put("a", 3, "new")},
			dropTomb: []keys.Entry{itertest.Put("a", 3, "new")},
		},
		{
			name:     "only tombstones",
			in:       sorted(itertest.Del("a", 1), itertest.Del("b", 2), itertest.Del("b", 1)),
			keepTomb: []keys.Entry{itertest.Del("a", 1), itertest.Del("b", 2)},
			dropTomb: nil,
		},
		{
			name:     "tombstones at both ends",
			in:       sorted(itertest.Del("a", 1), itertest.Put("m", 2, "m"), itertest.Del("z", 3), itertest.Put("z", 1, "z")),
			keepTomb: []keys.Entry{itertest.Del("a", 1), itertest.Put("m", 2, "m"), itertest.Del("z", 3)},
			dropTomb: []keys.Entry{itertest.Put("m", 2, "m")},
		},
	}
	for _, c := range cases {
		for _, drop := range []bool{false, true} {
			want := c.keepTomb
			if drop {
				want = c.dropTomb
			}
			it := iterator.NewDedup(iterator.NewSlice(c.in), drop)
			it.First()
			if got := itertest.Drain(t, it); !itertest.Equal(got, want) {
				t.Errorf("%s, drop=%v: got %v, want %v", c.name, drop, got, want)
			}
			it.Close()
		}
	}
}

// SeekGE onto a deleted key skips to the next live key when tombstones
// are dropped.
func TestDedupSeekGE(t *testing.T) {
	in := sorted(itertest.Put("a", 1, ""), itertest.Del("b", 4), itertest.Put("b", 2, ""), itertest.Put("b", 1, ""), itertest.Put("c", 3, ""))
	cases := []struct {
		target string
		drop   bool
		want   string // "" means exhausted
		seq    uint64
	}{
		{"b", false, "b", 4},
		{"b", true, "c", 3},
		{"bb", true, "c", 3},
		{"d", true, "", 0},
	}
	for _, c := range cases {
		it := iterator.NewDedup(iterator.NewSlice(in), c.drop)
		it.SeekGE([]byte(c.target))
		switch {
		case c.want == "" && it.Valid():
			t.Errorf("SeekGE(%q) drop=%v on %q, want exhausted", c.target, c.drop, it.Entry().Key)
		case c.want != "" && (!it.Valid() || string(it.Entry().Key) != c.want || it.Entry().Seq != c.seq):
			t.Errorf("SeekGE(%q) drop=%v: valid=%v, want %q@%d", c.target, c.drop, it.Valid(), c.want, c.seq)
		}
		it.Close()
	}
}
