package kvhttp

import (
	"context"
	"net/http/httptest"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
)

// One server for every fuzz iteration: starting a listener per input would
// make the fuzzer far slower. Each iteration cleans up the keys it wrote.
var (
	fuzzOnce   sync.Once
	fuzzClient *Client
)

// FuzzKVHTTPRoundTrip checks the round trip through the real Client and
// router for any non-empty key and value: Put then Get returns the value,
// All holds exactly what was put, and Delete then Get finds nothing. It
// mostly targets path escaping (url.PathEscape on the client, chi and
// keyParam's RawPath handling on the server).
func FuzzKVHTTPRoundTrip(f *testing.F) {
	for _, k := range []string{"plain", "with/slash", "a//b", "/lead", "trail/", "with space", "pct%25", "100%",
		"%2F", "ünï", "?q=1#f", ".", "..", "a/../b", "+", ";x=1", "\x00", "\xff"} {
		f.Add(k, "v-"+k)
	}
	f.Add("k", "")
	f.Add("k", "\xff\xfe")

	f.Fuzz(func(t *testing.T, key, value string) {
		if key == "" {
			t.Skip("precondition: keys are non-empty (cluster rejects empty keys first)")
		}
		if len(key) > 1<<10 || len(value) > 64<<10 {
			t.Skip("keep well under the server's 1 MiB body limit and URL limits")
		}
		fuzzOnce.Do(func() {
			srv := httptest.NewServer(NewRouter(kv.NewStore()))
			fuzzClient = NewClient(srv.URL) // the server lives as long as the test binary
		})
		c, ctx := fuzzClient, context.Background()
		t.Cleanup(func() { c.Delete(ctx, key) })

		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			// JSON (GET /kv) can't carry these unchanged, so the server
			// refuses them rather than store something All would mangle.
			if err := c.Put(ctx, key, value); err == nil {
				t.Fatalf("put of invalid UTF-8 %q = %q was accepted", key, value)
			}
			if _, ok, _ := c.Get(ctx, key); ok {
				t.Fatalf("refused put of %q left a value behind", key)
			}
			return
		}

		if err := c.Put(ctx, key, value); err != nil {
			t.Fatalf("put %q: %v", key, err)
		}
		if got, ok, err := c.Get(ctx, key); err != nil || !ok || got != value {
			t.Fatalf("get %q: got (%q, %v, %v), want %q", key, got, ok, err, value)
		}
		all, err := c.All(ctx)
		if err != nil {
			t.Fatalf("all: %v", err)
		}
		if len(all) != 1 || all[key] != value {
			t.Fatalf("All() = %q, want exactly {%q: %q}", all, key, value)
		}
		if err := c.Delete(ctx, key); err != nil {
			t.Fatalf("delete %q: %v", key, err)
		}
		if got, ok, err := c.Get(ctx, key); err != nil || ok || got != "" {
			t.Fatalf("get after delete %q: got (%q, %v, %v)", key, got, ok, err)
		}
	})
}
