package cluster

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
)

// scanResult is one node's answer. The error travels with the result, on
// the same channel, so the parent can decide what to do. This is the
// Result pattern from Cox-Buday's "Concurrency in Go", chapter 4: a
// goroutine reports, the parent decides.
type scanResult struct {
	node    string
	entries map[string]string
	err     error
	panic   any // set if the goroutine panicked (a bug); the parent re-panics
}

// scan asks every node for its entries at the same time, one goroutine per
// node, and returns the results as they arrive.
//
// Leak safety: this function starts the goroutines, so it makes sure they
// always finish. The channel is buffered to len(nodes), so a goroutine
// never blocks on send even if the parent stops reading early. Each request
// uses ctx, so cancelling ctx stops in-flight requests. The channel is
// closed once every goroutine is done.
//
// A panic in a goroutine would crash the whole program, and main's recover
// can't catch it. So each goroutine recovers its own panic and passes it to
// the parent in the result.
//
//	post: exactly one result per node, then the channel is closed
func (c *Cluster) scan(ctx context.Context, nodes []string) <-chan scanResult {
	results := make(chan scanResult, len(nodes))
	var wg sync.WaitGroup
	for _, n := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					results <- scanResult{node: n, panic: fmt.Sprintf("%v\n\npanicking goroutine:\n%s", p, debug.Stack())}
				}
			}()
			entries, err := c.dial(n).All(ctx)
			results <- scanResult{node: n, entries: entries, err: err}
		}()
	}
	go func() {
		wg.Wait()
		close(results)
	}()
	return results
}

// repanic re-raises a panic from a scan goroutine on the caller's goroutine,
// where the top level's recover can see it.
func (r scanResult) repanic() {
	if r.panic != nil {
		panic(r.panic)
	}
}
