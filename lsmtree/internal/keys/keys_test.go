package keys_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/lsmtree/internal/keys"
)

func TestValidate(t *testing.T) {
	key := func(n int) []byte { return bytes.Repeat([]byte{'k'}, n) }
	val := func(n int) []byte { return bytes.Repeat([]byte{'v'}, n) }
	cases := []struct {
		name    string
		e       keys.Entry
		wantErr string // "" means valid
	}{
		{"key of 0 bytes", keys.Entry{Key: key(0), Kind: keys.KindPut}, "empty key"},
		{"key of 1 byte", keys.Entry{Key: key(1), Kind: keys.KindPut}, ""},
		{"key at the limit", keys.Entry{Key: key(keys.MaxKeySize), Kind: keys.KindPut}, ""},
		{"key one over the limit", keys.Entry{Key: key(keys.MaxKeySize + 1), Kind: keys.KindPut}, "key of 4097 bytes"},
		{"empty value", keys.Entry{Key: key(1), Kind: keys.KindPut, Value: val(0)}, ""},
		{"value at the limit", keys.Entry{Key: key(1), Kind: keys.KindPut, Value: val(keys.MaxValueSize)}, ""},
		{"value one over the limit", keys.Entry{Key: key(1), Kind: keys.KindPut, Value: val(keys.MaxValueSize + 1)}, "value of 1048577 bytes"},
		{"tombstone", keys.Entry{Key: key(1), Kind: keys.KindDelete}, ""},
		{"tombstone with a value", keys.Entry{Key: key(1), Kind: keys.KindDelete, Value: val(1)}, "tombstone with a 1-byte value"},
		{"invalid kind", keys.Entry{Key: key(1), Kind: 2}, "invalid kind 2"},
		{"max seq", keys.Entry{Key: key(1), Seq: ^uint64(0), Kind: keys.KindPut}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.e.Validate()
			switch {
			case c.wantErr == "" && err != nil:
				t.Fatalf("Validate() = %v, want nil", err)
			case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
				t.Fatalf("Validate() = %v, want an error containing %q", err, c.wantErr)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	e := func(key string, seq uint64) keys.Entry { return keys.Entry{Key: []byte(key), Seq: seq} }
	cases := []struct {
		name string
		a, b keys.Entry
		want int
	}{
		{"smaller key first", e("a", 1), e("b", 1), -1},
		{"larger key last", e("b", 1), e("a", 1), 1},
		{"key decides before seq", e("a", 1), e("b", 9), -1},
		{"prefix sorts first", e("a", 1), e("ab", 1), -1},
		{"same key, newer first", e("k", 9), e("k", 1), -1},
		{"same key, older last", e("k", 1), e("k", 9), 1},
		{"same key and seq", e("k", 5), e("k", 5), 0},
		{"seq extremes", e("k", ^uint64(0)), e("k", 0), -1},
		{"kind and value ignored", keys.Entry{Key: []byte("k"), Seq: 1, Kind: keys.KindPut, Value: []byte("x")},
			keys.Entry{Key: []byte("k"), Seq: 1, Kind: keys.KindDelete}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := keys.Compare(c.a, c.b); got != c.want {
				t.Fatalf("Compare = %d, want %d", got, c.want)
			}
		})
	}
}

func TestCloneSharesNoMemory(t *testing.T) {
	orig := keys.Entry{Key: []byte("k"), Seq: 3, Kind: keys.KindPut, Value: []byte("v")}
	c := orig.Clone()
	orig.Key[0], orig.Value[0] = 'x', 'x'
	if string(c.Key) != "k" || string(c.Value) != "v" || c.Seq != 3 || c.Kind != keys.KindPut {
		t.Fatalf("clone changed with the original: %+v", c)
	}
}
