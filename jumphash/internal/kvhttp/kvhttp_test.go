package kvhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster"
	"github.com/nickstrad/systems-learnings/jumphash/internal/cluster/clustertest"
	"github.com/nickstrad/systems-learnings/jumphash/internal/errs"
	"github.com/nickstrad/systems-learnings/jumphash/internal/kv"
)

// The HTTP client must honor the same cluster.Node contract as the fakes,
// with a real server on the other end.
func TestClientNodeContract(t *testing.T) {
	clustertest.NodeContract(t, func(t *testing.T) cluster.Node {
		srv := httptest.NewServer(NewRouter(kv.NewStore()))
		t.Cleanup(srv.Close)
		return NewClient(srv.URL)
	})
}

// Bad input at the HTTP edge gets an HTTP error, never a panic.
func TestServerRejectsEmptyKey(t *testing.T) {
	srv := httptest.NewServer(NewRouter(kv.NewStore()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPut, srv.URL+"/kv/", strings.NewReader("v"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode < 400 || resp.StatusCode >= 500 {
		t.Fatalf("PUT /kv/ = %d, want a 4xx", resp.StatusCode)
	}
}

func TestClientServerRoundTrip(t *testing.T) {
	srv := httptest.NewServer(NewRouter(kv.NewStore()))
	defer srv.Close()
	c := NewClient(srv.URL)
	ctx := context.Background()

	if err := c.Health(ctx); err != nil {
		t.Fatal(err)
	}
	// Keys with characters that need escaping must round-trip unchanged.
	keys := []string{"plain", "with/slash", "with space", "pct%25", "ünï"}
	for _, k := range keys {
		if err := c.Put(ctx, k, "v-"+k); err != nil {
			t.Fatalf("put %q: %v", k, err)
		}
	}
	for _, k := range keys {
		v, ok, err := c.Get(ctx, k)
		if err != nil || !ok || v != "v-"+k {
			t.Fatalf("get %q: got (%q, %v, %v)", k, v, ok, err)
		}
	}
	all, err := c.All(ctx)
	if err != nil || len(all) != len(keys) {
		t.Fatalf("All: got %d entries, err %v", len(all), err)
	}
	if err := c.Delete(ctx, "with/slash"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := c.Get(ctx, "with/slash"); ok {
		t.Fatal("key still present after delete")
	}
	if err := c.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("delete of missing key should not error: %v", err)
	}
}

// Boundary: a value of exactly MaxValueBytes is accepted and intact; one
// byte more is refused with 413 and nothing is stored.
func TestPutValueSizeLimit(t *testing.T) {
	tests := []struct {
		name       string
		size       int
		wantStatus int
		wantStored bool
	}{
		{"empty", 0, http.StatusNoContent, true},
		{"on point: at the limit", MaxValueBytes, http.StatusNoContent, true},
		{"off point: one byte over", MaxValueBytes + 1, http.StatusRequestEntityTooLarge, false},
		{"far over", 3 * MaxValueBytes, http.StatusRequestEntityTooLarge, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := kv.NewStore()
			srv := httptest.NewServer(NewRouter(store))
			defer srv.Close()
			value := strings.Repeat("x", tt.size)

			req, _ := http.NewRequest(http.MethodPut, srv.URL+"/kv/big", strings.NewReader(value))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != tt.wantStatus {
				t.Fatalf("PUT of %d bytes = %d, want %d", tt.size, resp.StatusCode, tt.wantStatus)
			}
			got, ok := store.Get("big")
			if ok != tt.wantStored || (ok && got != value) {
				t.Fatalf("stored = %v (len %d), want stored = %v with the full value", ok, len(got), tt.wantStored)
			}
		})
	}
}

// The client turns 413 into a well-formed kvhttp error that names the limit.
func TestClientPutTooLargeIsWellFormed(t *testing.T) {
	srv := httptest.NewServer(NewRouter(kv.NewStore()))
	defer srv.Close()
	err := NewClient(srv.URL).Put(context.Background(), "big", strings.Repeat("x", MaxValueBytes+1))
	var e *errs.Error
	if !errors.As(err, &e) || e.Module != module {
		t.Fatalf("want a kvhttp *errs.Error, got %T: %v", err, err)
	}
	if !strings.Contains(e.Message, "too large") {
		t.Fatalf("message should say the value is too large: %q", e.Message)
	}
}
