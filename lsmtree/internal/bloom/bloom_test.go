package bloom_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/bloom"
	"github.com/nickstrad/systems-learnings/shared/assert/asserttest"
)

func key(prefix string, i int) []byte { return []byte(fmt.Sprintf("%s%08d", prefix, i)) }

func TestProbes(t *testing.T) {
	cases := []struct{ bitsPerKey, want int }{
		{1, 1},  // round(0.69) = 1
		{2, 1},  // round(1.39) = 1
		{3, 2},  // round(2.08)
		{5, 3},  // round(3.47)
		{10, 7}, // round(6.93)
		{20, 14},
		{43, 30}, // round(29.8): the clamp's on point
		{44, 30}, // round(30.5) = 31, clamped
		{1000, 30},
	}
	for _, c := range cases {
		if got := bloom.Probes(c.bitsPerKey); got != c.want {
			t.Errorf("Probes(%d) = %d, want %d", c.bitsPerKey, got, c.want)
		}
	}
}

func TestFalsePositiveRateFormula(t *testing.T) {
	cases := []struct {
		bitsPerKey int
		want       float64
	}{
		{1, 0.6321206},  // k=1: 1 - e^-1
		{5, 0.0918488},  // k=3
		{10, 0.0081937}, // k=7
		{20, 6.7137e-5}, // k=14
	}
	for _, c := range cases {
		got := bloom.FalsePositiveRate(c.bitsPerKey)
		if math.Abs(got-c.want) > 1e-4*c.want {
			t.Errorf("FalsePositiveRate(%d) = %.7g, want %.7g", c.bitsPerKey, got, c.want)
		}
	}
}

func TestBuildSizeAndK(t *testing.T) {
	cases := []struct {
		name          string
		bitsPerKey, n int
		wantLen       int
		wantK         byte
	}{
		{"no keys gets the 64-bit minimum", 10, 0, 8 + 1, 7},
		{"one key gets the minimum", 10, 1, 8 + 1, 7},
		{"exactly 64 bits", 8, 8, 8 + 1, 6},
		{"65 bits rounds up a byte", 13, 5, 9 + 1, 9},
		{"many keys", 10, 1000, 1250 + 1, 7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			b := bloom.NewBuilder(c.bitsPerKey)
			for i := range c.n {
				b.Add(key("k", i))
			}
			if b.Size() != c.wantLen {
				t.Fatalf("Size() = %d before Build, want %d", b.Size(), c.wantLen)
			}
			f := b.Build()
			if len(f) != c.wantLen || f[len(f)-1] != c.wantK {
				t.Fatalf("len=%d k=%d, want len=%d k=%d", len(f), f[len(f)-1], c.wantLen, c.wantK)
			}
		})
	}
}

func TestEmptyFilterRejectsEverything(t *testing.T) {
	f := bloom.New(10, nil)
	for i := range 100 {
		if f.MayContain(key("k", i)) {
			t.Fatalf("an empty filter may contain %q", key("k", i))
		}
	}
}

func TestNoFalseNegatives(t *testing.T) {
	for _, bits := range []int{1, 5, 10, 20} {
		var ks [][]byte
		for i := range 5000 {
			ks = append(ks, key("present", i))
		}
		ks = append(ks, []byte{}) // the empty key is a key too
		f := bloom.New(bits, ks)
		for _, k := range ks {
			if !f.MayContain(k) {
				t.Fatalf("bits=%d: false negative for %q", bits, k)
			}
		}
	}
}

// TestFalsePositiveRate checks the measured rate against
// FalsePositiveRate. With n = 10000 keys every bit array is exactly
// n·bits bits (a whole number of bytes, above minBits), so the formula
// applies without rounding.
func TestFalsePositiveRate(t *testing.T) {
	const n, probes = 10000, 100000
	t.Logf("%-11s %-3s %-10s %-10s %-10s", "bits/key", "k", "expected", "measured", "ratio")
	for _, bits := range []int{5, 10, 20} {
		var ks [][]byte
		for i := range n {
			ks = append(ks, key("in", i))
		}
		f := bloom.New(bits, ks)
		if m := (len(f) - 1) * 8; m != n*bits {
			t.Fatalf("bits=%d: filter has %d bits, want %d", bits, m, n*bits)
		}
		k := f[len(f)-1]
		want := bloom.FalsePositiveRate(bits)

		fp := 0
		for i := range probes {
			if f.MayContain(key("out", i)) {
				fp++
			}
		}
		got := float64(fp) / probes
		t.Logf("%-11d %-3d %-10.5f %-10.5f %-10.2f", bits, k, want, got, got/want)

		// Allow 25% relative error plus a few counts of absolute slack, so
		// the 20-bit case (expected ~7 hits in 100k) is not flaky.
		if math.Abs(got-want) > 0.25*want+5.0/probes {
			t.Errorf("bits=%d: FP rate %.5f, want %.5f within tolerance", bits, got, want)
		}
	}
}

func TestDamagedFiltersMayContainEverything(t *testing.T) {
	cases := []struct {
		name string
		f    bloom.Filter
	}{
		{"nil", nil},
		{"empty", bloom.Filter{}},
		{"only k byte", bloom.Filter{7}},
		{"k = 0", bloom.Filter{0, 0, 0}},
		{"k = 31", bloom.Filter{0, 0, 31}},
		{"k = 255", bloom.Filter{0, 0, 255}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !c.f.MayContain([]byte("anything")) {
				t.Fatal("a damaged filter ruled a key out")
			}
		})
	}
}

// TestFilterIsStable pins the hash: filters are written to disk, so a
// change to the hash would make every existing file give false negatives.
func TestFilterIsStable(t *testing.T) {
	f := bloom.New(10, [][]byte{[]byte("a"), []byte("b")})
	got := fmt.Sprintf("%x", []byte(f))
	const want = "4020010b440812a007"
	if got != want {
		t.Fatalf("filter bytes changed:\n got %s\nwant %s", got, want)
	}
}

func TestNewBuilderAssertsBitsPerKey(t *testing.T) {
	asserttest.Panics(t, "bitsPerKey 0 < 1", func() { bloom.NewBuilder(0) })
}

func TestFalsePositiveRateAssertsBitsPerKey(t *testing.T) {
	asserttest.Panics(t, "bitsPerKey 0 < 1", func() { bloom.FalsePositiveRate(0) })
}
